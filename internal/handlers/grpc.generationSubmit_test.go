package handlers_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/handlers"
	handlersmocks "github.com/a-novel/service-genai/internal/handlers/mocks"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
	"github.com/a-novel/service-genai/internal/lib"
)

func TestGrpcGenerationSubmit(t *testing.T) {
	t.Parallel()

	// Every request field reaches the request key: it is how a resend finds its generation. A new
	// field must be mapped below and added to the key, or two different requests would share one.
	keyFields := []string{"owner_id", "purpose", "provider", "payload", "variant"}
	// user_tier selects the caps a submit answers to, and changes nothing about the call.
	notKeyed := []string{"user_tier"}

	fields := (&genaiv0.GenerationSubmitRequest{}).ProtoReflect().Descriptor().Fields()
	for index := range fields.Len() {
		require.Contains(t, append(keyFields, notKeyed...), string(fields.Get(index).Name()))
	}

	type serviceMock struct {
		resp *core.GenerationSubmitResult
		err  error
	}

	payload := []byte(`{"model": "a-model", "input": "Continue."}`)

	testCases := []struct {
		name string

		request *genaiv0.GenerationSubmitRequest

		serviceMock *serviceMock

		expectCreated bool
		expectStatus  codes.Code
		// expectMessage is part of the error a caller reads, telling it what to fix.
		expectMessage string
	}{
		{
			name: "Success",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "openai", Payload: payload, Variant: 2,
			},
			serviceMock: &serviceMock{resp: &core.GenerationSubmitResult{
				Generation: testGeneration(), Created: true,
			}},

			expectCreated: true,
		},
		{
			// A caller retrying a request it never saw the answer to attaches to the work already in
			// flight, and is told that is what happened.
			name: "Success/Replayed",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "openai", Payload: payload,
			},
			serviceMock: &serviceMock{resp: &core.GenerationSubmitResult{
				Generation: testGeneration(), Created: false,
			}},
		},
		{
			name: "Error/InvalidOwnerID",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: "not-a-uuid", Purpose: "studio.generation", Provider: "openai", Payload: payload,
			},

			expectStatus: codes.InvalidArgument,
		},
		{
			name: "Error/InvalidRequest",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "unknown", Payload: payload,
			},
			serviceMock: &serviceMock{err: fmt.Errorf("%w: unknown provider", core.ErrInvalidRequest)},

			expectStatus:  codes.InvalidArgument,
			expectMessage: "unknown provider",
		},
		{
			// The message names the cap, so the caller can tell its user which limit was reached.
			name: "Error/UsageCapReached",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", UserTier: "premium", Provider: "openai",
				Payload: payload,
			},
			serviceMock: &serviceMock{err: fmt.Errorf("%w: global over 720h0m0s", core.ErrUsageCapReached)},

			expectStatus:  codes.ResourceExhausted,
			expectMessage: "global over 720h0m0s",
		},
		{
			name: "Error/ModeUnsupported",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "sync-only", Payload: payload,
			},
			serviceMock: &serviceMock{err: fmt.Errorf("provider: %w", lib.ErrModeUnsupported)},

			expectStatus:  codes.Unimplemented,
			expectMessage: "Async mode",
		},
		{
			name: "Error/Internal",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "openai", Payload: payload,
			},
			serviceMock: &serviceMock{err: errFoo},

			expectStatus: codes.Internal,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			service := handlersmocks.NewMockGrpcGenerationSubmitService(t)

			if testCase.serviceMock != nil {
				service.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *core.GenerationSubmitRequest) bool {
						return request.OwnerID == uuid.MustParse(testCase.request.GetOwnerId()) &&
							request.Purpose == testCase.request.GetPurpose() &&
							request.UserTier == testCase.request.GetUserTier() &&
							request.Provider == testCase.request.GetProvider() &&
							string(request.Payload) == string(testCase.request.GetPayload()) &&
							request.Variant == testCase.request.GetVariant()
					})).
					Return(testCase.serviceMock.resp, testCase.serviceMock.err)
			}

			response, err := handlers.NewGrpcGenerationSubmit(service).
				GenerationSubmit(t.Context(), testCase.request)

			if testCase.expectStatus != codes.OK {
				require.Equal(t, testCase.expectStatus, status.Code(err))
				require.Contains(t, status.Convert(err).Message(), testCase.expectMessage)
				require.Nil(t, response)

				return
			}

			require.NoError(t, err)
			require.Equal(t, testCase.expectCreated, response.GetCreated())
			require.Equal(t, testGenerationID, response.GetGeneration().GetId())

			service.AssertExpectations(t)
		})
	}
}
