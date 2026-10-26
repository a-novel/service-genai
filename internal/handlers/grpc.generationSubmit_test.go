package handlers_test

import (
	"encoding/json"
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
)

func TestGrpcGenerationSubmit(t *testing.T) {
	t.Parallel()

	// Every request field reaches the request key: it is how a resend finds its generation. A new
	// field must be mapped below and added to the key, or two different requests would share one.
	keyFields := []string{"owner_id", "purpose", "provider", "payload", "variant", "stream"}
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

		expect       *genaiv0.GenerationSubmitResponse
		expectStatus codes.Code
		// expectMessage is part of the error a caller reads, telling it what to fix.
		expectMessage string
		// expectCause is the error the fault wraps, which Sentry groups it by.
		expectCause error
	}{
		{
			name: "Success",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "openai", Payload: payload, Variant: 2,
			},
			serviceMock: &serviceMock{resp: &core.GenerationSubmitResult{
				Generation: testGeneration(), Created: true,
			}},

			expect: &genaiv0.GenerationSubmitResponse{Generation: testGrpcGeneration(), Created: true},
		},
		{
			name: "Success/Streamed",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "openai", Payload: payload, Stream: true,
			},
			serviceMock: &serviceMock{resp: &core.GenerationSubmitResult{
				Generation: testGeneration(), Created: true,
			}},

			expect: &genaiv0.GenerationSubmitResponse{Generation: testGrpcGeneration(), Created: true},
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

			expect: &genaiv0.GenerationSubmitResponse{Generation: testGrpcGeneration()},
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
			serviceMock: &serviceMock{err: fmt.Errorf("provider: %w", core.ErrModeUnsupported)},

			expectStatus:  codes.Unimplemented,
			expectMessage: "requested mode",
		},
		{
			name: "Error/Internal",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "openai", Payload: payload,
			},
			serviceMock: &serviceMock{err: errFoo},

			expectStatus: codes.Internal,
			expectCause:  errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			service := handlersmocks.NewMockGrpcGenerationSubmitService(t)

			if testCase.serviceMock != nil {
				serviceRequest := &core.GenerationSubmitRequest{
					OwnerID:  uuid.MustParse(testCase.request.GetOwnerId()),
					Purpose:  testCase.request.GetPurpose(),
					UserTier: testCase.request.GetUserTier(),
					Provider: testCase.request.GetProvider(),
					Payload:  json.RawMessage(testCase.request.GetPayload()),
					Variant:  testCase.request.GetVariant(),
					Stream:   testCase.request.GetStream(),
				}

				service.EXPECT().Exec(mock.Anything, serviceRequest).Return(testCase.serviceMock.resp, testCase.serviceMock.err)
			}

			res, err := handlers.NewGrpcGenerationSubmit(service).GenerationSubmit(t.Context(), testCase.request)
			resSt, ok := status.FromError(err)
			require.True(t, ok, resSt.Code().String())
			require.Equal(
				t,
				testCase.expectStatus, resSt.Code(),
				"expected status code %s, got %s (%v)", testCase.expectStatus, resSt.Code(), err,
			)
			require.Contains(t, resSt.Message(), testCase.expectMessage)

			if testCase.expectCause != nil {
				require.ErrorIs(t, err, testCase.expectCause)
			}

			require.Equal(t, testCase.expect, res)

			service.AssertExpectations(t)
		})
	}
}
