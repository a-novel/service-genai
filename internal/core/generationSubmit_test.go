package core_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib"
)

// submitRequest is a valid submission; a case adjusts it.
func submitRequest(adjust func(request *core.GenerationSubmitRequest)) *core.GenerationSubmitRequest {
	request := &core.GenerationSubmitRequest{
		OwnerID:        testOwner,
		Purpose:        "studio.generation",
		IdempotencyKey: "key",
		Tier:           lib.TierBalanced,
		Instructions:   "Continue.",
		Input:          json.RawMessage(`{"scene": "a door"}`),
		OutputSchema:   json.RawMessage(`{"type": "object"}`),
	}

	if adjust != nil {
		adjust(request)
	}

	return request
}

// inputOfSize makes the request's three parts add up to the given number of bytes.
func inputOfSize(size int) func(request *core.GenerationSubmitRequest) {
	return func(request *core.GenerationSubmitRequest) {
		const quotes = 2

		filler := size - len(request.Instructions) - len(request.OutputSchema) - quotes
		request.Input = json.RawMessage(`"` + strings.Repeat("x", filler) + `"`)
	}
}

func TestGenerationSubmit(t *testing.T) {
	t.Parallel()

	const maxAttempts = 2

	type daoMock struct {
		resp *dao.GenerationSubmitResult
		err  error
	}

	testCases := []struct {
		name string

		request *core.GenerationSubmitRequest

		daoMock *daoMock
		// checkErr is what starting a created generation returns. A replay is never started here.
		checkErr error
		usageErr error

		expectCreated bool
		expectErr     error
	}{
		{
			name: "Success",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},

			expectCreated: true,
		},
		{
			// A resend attaches to the recorded generation; whoever polls it next checks it.
			name: "Success/Replayed",

			request: submitRequest(nil),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration()}},
		},
		{
			name: "Success/AnyJSONInput",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Input = json.RawMessage(`"a plain string is JSON too"`)
			}),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},

			expectCreated: true,
		},
		{
			name: "Success/RequestAtCeiling",

			request: submitRequest(inputOfSize(core.RequestSizeCeiling)),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},

			expectCreated: true,
		},
		{
			name: "Error/NoOwner",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.OwnerID = uuid.Nil }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/NoPurpose",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Purpose = "" }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/BlankIdempotencyKey",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.IdempotencyKey = "   " }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/NoTier",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Tier = "" }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/UnknownTier",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Tier = "gpt-5" }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/BlankInstructions",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Instructions = " " }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/InputNotJSON",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Input = json.RawMessage(`not json`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// A strict schema's root is always an object; anything else is a caller bug.
			name: "Error/SchemaNotAnObject",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.OutputSchema = json.RawMessage(`["type", "object"]`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// Refused here rather than at the transport, so the caller gets an error it can act on.
			name: "Error/RequestTooLarge",

			request: submitRequest(inputOfSize(core.RequestSizeCeiling + 1)),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// The generation is recorded, so a resend finds it; the caller learns the start failed.
			name: "Error/Start",

			request:  submitRequest(nil),
			daoMock:  &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},
			checkErr: errFoo,

			expectErr: errFoo,
		},
		{
			name: "Error/Usage",

			request:  submitRequest(nil),
			daoMock:  &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration()}},
			usageErr: errFoo,

			expectErr: errFoo,
		},
		{
			name: "Error/IdempotencyConflict",

			request: submitRequest(nil),
			daoMock: &daoMock{err: dao.ErrGenerationSubmitConflict},

			expectErr: core.ErrIdempotencyConflict,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			submitDao := coremocks.NewMockGenerationSubmitDao(t)
			usageDao := coremocks.NewMockGenerationSubmitUsageListDao(t)
			check := coremocks.NewMockGenerationSubmitServiceCheck(t)

			if testCase.daoMock != nil {
				submitDao.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationSubmitRequest) bool {
						var stored struct {
							Tier         lib.Tier        `json:"tier"`
							Instructions string          `json:"instructions"`
							Input        json.RawMessage `json:"input"`
							OutputSchema json.RawMessage `json:"outputSchema"`
						}

						// The stored request carries the caller's fields as sent, and the digest is
						// derived rather than left empty.
						return json.Unmarshal(request.Request, &stored) == nil &&
							stored.Tier == testCase.request.Tier &&
							stored.Instructions == testCase.request.Instructions &&
							string(stored.Input) == compact(testCase.request.Input) &&
							string(stored.OutputSchema) == compact(testCase.request.OutputSchema) &&
							request.MaxAttempts == maxAttempts &&
							len(request.RequestFingerprint) == 32 &&
							request.ID != uuid.Nil
					})).
					Return(testCase.daoMock.resp, testCase.daoMock.err)

				if testCase.daoMock.resp != nil && testCase.daoMock.resp.Created {
					check.EXPECT().
						Exec(mock.Anything, &core.GenerationCheckRequest{Generation: testCase.daoMock.resp.Generation}).
						Return(testCase.daoMock.resp.Generation, testCase.checkErr)
				}

				if testCase.daoMock.resp != nil && testCase.checkErr == nil {
					usageDao.EXPECT().
						Exec(mock.Anything, &dao.GenerationUsageListRequest{GenerationID: testGenerationID}).
						Return([]*dao.GenerationUsage{}, testCase.usageErr)
				}
			}

			service, err := core.NewGenerationSubmit(
				core.GenerationSubmitConfig{MaxAttempts: maxAttempts}, submitDao, usageDao, check,
			)
			require.NoError(t, err)

			result, err := service.Exec(t.Context(), testCase.request)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, testGenerationID, result.Generation.ID)
				require.Equal(t, testCase.expectCreated, result.Created)
			}

			submitDao.AssertExpectations(t)
			usageDao.AssertExpectations(t)
			check.AssertExpectations(t)
		})
	}
}
