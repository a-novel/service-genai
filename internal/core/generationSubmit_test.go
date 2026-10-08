package core_test

import (
	"context"
	"encoding/json"
	"errors"
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
		OwnerID:      testOwner,
		Purpose:      "studio.generation",
		Tier:         lib.TierBalanced,
		Instructions: "Continue.",
		Input:        json.RawMessage(`{"scene": "a door"}`),
		OutputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
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
			// Stored and sent as written: an escaped & would reach the model as \u0026.
			name: "Success/HTMLCharacters",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Input = json.RawMessage(`{"text": "Tom & Jerry <3>"}`)
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
			name: "Error/SchemaNull",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.OutputSchema = json.RawMessage(`null`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// Canonicalizing would keep one of the values, so two different requests would share a key.
			name: "Error/InputDuplicateNames",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Input = json.RawMessage(`{"scene": "a door", "scene": "a window"}`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/InputUnpairedSurrogate",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Input = json.RawMessage(`{"scene": "\ud800"}`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// PostgreSQL text cannot hold it.
			name: "Error/PurposeWithNUL",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Purpose = "studio\x00generation" }),

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
			name: "Error/Internal",

			request: submitRequest(nil),
			daoMock: &daoMock{err: errFoo},

			expectErr: errFoo,
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
							len(request.RequestKey) == 32 &&
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

	// The key is the request's identity: equal requests share it, and every field the caller sends
	// changes it. A field left out would let two different requests return one generation.
	keyCases := []struct {
		name string

		adjust func(request *core.GenerationSubmitRequest)

		expectSame bool
	}{
		{name: "Same", expectSame: true},
		{
			// A resend rebuilt from the caller's state may reorder keys or reformat; it is still the
			// same request.
			name: "ReformattedJSON",

			adjust: func(request *core.GenerationSubmitRequest) {
				request.Input = json.RawMessage(`{ "scene" : "a door" }`)
				request.OutputSchema = json.RawMessage(`{"properties":{},"type":"object"}`)
			},

			expectSame: true,
		},
		{name: "Owner", adjust: func(request *core.GenerationSubmitRequest) { request.OwnerID = uuid.New() }},
		{name: "Purpose", adjust: func(request *core.GenerationSubmitRequest) { request.Purpose = "studio.analysis" }},
		{name: "Tier", adjust: func(request *core.GenerationSubmitRequest) { request.Tier = lib.TierDeep }},
		{name: "Instructions", adjust: func(request *core.GenerationSubmitRequest) { request.Instructions = "Stop." }},
		{name: "Input", adjust: func(request *core.GenerationSubmitRequest) {
			request.Input = json.RawMessage(`{"scene": "a window"}`)
		}},
		{name: "OutputSchema", adjust: func(request *core.GenerationSubmitRequest) {
			request.OutputSchema = json.RawMessage(`{"type": "object", "properties": {"text": {"type": "string"}}}`)
		}},
		{name: "Variant", adjust: func(request *core.GenerationSubmitRequest) { request.Variant = 1 }},
		{
			// Moving bytes from one field to the next must not produce the same key.
			name: "FieldBoundary",

			adjust: func(request *core.GenerationSubmitRequest) {
				request.Purpose = "studio.generationContinue"
				request.Instructions = "."
			},
		},
	}

	baseKey := submittedKey(t, submitRequest(nil))

	for _, keyCase := range keyCases {
		t.Run("RequestKey/"+keyCase.name, func(t *testing.T) {
			t.Parallel()

			key := submittedKey(t, submitRequest(keyCase.adjust))

			if keyCase.expectSame {
				require.Equal(t, baseKey, key)
			} else {
				require.NotEqual(t, baseKey, key)
			}
		})
	}
}

// submittedKey returns the request key a submission hands to the data access.
func submittedKey(t *testing.T, request *core.GenerationSubmitRequest) []byte {
	t.Helper()

	var key []byte

	submitDao := coremocks.NewMockGenerationSubmitDao(t)
	submitDao.EXPECT().
		Exec(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, submitted *dao.GenerationSubmitRequest) (*dao.GenerationSubmitResult, error) {
			key = submitted.RequestKey

			return nil, errFoo
		})

	service, err := core.NewGenerationSubmit(
		core.GenerationSubmitConfig{MaxAttempts: 1},
		submitDao,
		coremocks.NewMockGenerationSubmitUsageListDao(t),
		coremocks.NewMockGenerationSubmitServiceCheck(t),
	)
	if err != nil {
		panic(err)
	}

	_, err = service.Exec(t.Context(), request)
	if !errors.Is(err, errFoo) {
		panic(err)
	}

	return key
}
