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
	libmocks "github.com/a-novel/service-genai/internal/lib/mocks"
)

// submitRequest is a valid submission; a case adjusts it.
func submitRequest(adjust func(request *core.GenerationSubmitRequest)) *core.GenerationSubmitRequest {
	request := &core.GenerationSubmitRequest{
		OwnerID:  testOwner,
		Purpose:  testPurpose,
		Provider: testProvider,
		Payload:  json.RawMessage(`{"model": "a-model", "input": "Continue the scene."}`),
	}

	if adjust != nil {
		adjust(request)
	}

	return request
}

// payloadOfSize makes the payload the given number of bytes.
func payloadOfSize(size int) func(request *core.GenerationSubmitRequest) {
	return func(request *core.GenerationSubmitRequest) {
		const envelope = len(`{"input":""}`)

		request.Payload = json.RawMessage(`{"input":"` + strings.Repeat("x", size-envelope) + `"}`)
	}
}

// testAdapters serve the test provider in the Async mode, and another provider without it.
func testAdapters(t *testing.T) lib.Adapters {
	t.Helper()

	return lib.Adapters{
		testProvider: libmocks.NewMockAsyncAdapter(t),
		"another":    libmocks.NewMockAsyncAdapter(t),
		"sync-only":  struct{}{},
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
		checkErr   error
		attemptErr error

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
			// Stored as written, so it reaches the provider as written.
			name: "Success/HTMLCharacters",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`{"input": "Tom & Jerry <3>"}`)
			}),
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration(), Created: true}},

			expectCreated: true,
		},
		{
			name: "Success/PayloadAtCeiling",

			request: submitRequest(payloadOfSize(core.PayloadSizeCeiling)),
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

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Purpose = " " }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// PostgreSQL text cannot hold it.
			name: "Error/PurposeWithNUL",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Purpose = "studio\x00generation" }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/NoProvider",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Provider = "" }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/UnknownProvider",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Provider = "gpt-5" }),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// The request is valid; this service cannot run it in the Async mode.
			name: "Error/ModeUnsupported",

			request: submitRequest(func(request *core.GenerationSubmitRequest) { request.Provider = "sync-only" }),

			expectErr: lib.ErrModeUnsupported,
		},
		{
			name: "Error/PayloadNotJSON",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`not json`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// A provider request is always an object; anything else is a caller bug.
			name: "Error/PayloadNotAnObject",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`["model", "a-model"]`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/PayloadNull",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`null`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// Canonicalizing would keep one of the values, so two different requests would share a key.
			name: "Error/PayloadDuplicateNames",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`{"input": "a door", "input": "a window"}`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/PayloadUnpairedSurrogate",

			request: submitRequest(func(request *core.GenerationSubmitRequest) {
				request.Payload = json.RawMessage(`{"input": "\ud800"}`)
			}),

			expectErr: core.ErrInvalidRequest,
		},
		{
			// Refused here rather than at the transport, so the caller gets an error it can act on.
			name: "Error/PayloadTooLarge",

			request: submitRequest(payloadOfSize(core.PayloadSizeCeiling + 1)),

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
			name: "Error/Attempts",

			request:    submitRequest(nil),
			daoMock:    &daoMock{resp: &dao.GenerationSubmitResult{Generation: pendingGeneration()}},
			attemptErr: errFoo,

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
			attemptDao := coremocks.NewMockGenerationSubmitAttemptListDao(t)
			check := coremocks.NewMockGenerationSubmitServiceCheck(t)

			if testCase.daoMock != nil {
				submitDao.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationSubmitRequest) bool {
						// The payload is stored exactly as sent, and the digest is derived rather than
						// left empty.
						return request.Purpose == testCase.request.Purpose &&
							request.Provider == testCase.request.Provider &&
							string(request.Payload) == string(testCase.request.Payload) &&
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
					attemptDao.EXPECT().
						Exec(mock.Anything, &dao.GenerationAttemptListRequest{GenerationID: testGenerationID}).
						Return([]*dao.GenerationAttempt{}, testCase.attemptErr)
				}
			}

			service, err := core.NewGenerationSubmit(
				core.GenerationSubmitConfig{MaxAttempts: maxAttempts}, testAdapters(t), submitDao, attemptDao, check,
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
			attemptDao.AssertExpectations(t)
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
				request.Payload = json.RawMessage(`{ "input" : "Continue the scene.", "model" : "a-model" }`)
			},

			expectSame: true,
		},
		{name: "Owner", adjust: func(request *core.GenerationSubmitRequest) { request.OwnerID = uuid.New() }},
		{name: "Purpose", adjust: func(request *core.GenerationSubmitRequest) { request.Purpose = "studio.analysis" }},
		{name: "Provider", adjust: func(request *core.GenerationSubmitRequest) { request.Provider = "another" }},
		{name: "Payload", adjust: func(request *core.GenerationSubmitRequest) {
			request.Payload = json.RawMessage(`{"model": "a-model", "input": "Stop."}`)
		}},
		{name: "Variant", adjust: func(request *core.GenerationSubmitRequest) { request.Variant = 1 }},
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
		testAdapters(t),
		submitDao,
		coremocks.NewMockGenerationSubmitAttemptListDao(t),
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
