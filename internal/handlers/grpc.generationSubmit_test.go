package handlers_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

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
	// user_tier and caps select the limits a submit answers to, and change nothing about the call.
	notKeyed := []string{"user_tier", "caps"}

	fields := (&genaiv0.GenerationSubmitRequest{}).ProtoReflect().Descriptor().Fields()
	for index := range fields.Len() {
		require.Contains(t, append(keyFields, notKeyed...), string(fields.Get(index).Name()))
	}

	type serviceMock struct {
		resp *core.GenerationSubmitResult
		err  error
	}

	payload := []byte(`{"model": "a-model", "input": "Continue."}`)

	// refusal is a window's refusal, which frees usage in an hour.
	refusal := &core.UsageCapError{
		Scope:      `purpose "studio.generation"`,
		Limit:      "input tokens over 5h",
		RetryAfter: time.Hour,
		Reason:     `purpose "studio.generation" over 5h0m0s`,
	}

	testCases := []struct {
		name string

		request *genaiv0.GenerationSubmitRequest
		// expectCaps are the caps the service receives.
		expectCaps *core.GenerationCaps

		serviceMock *serviceMock

		expect       *genaiv0.GenerationSubmitResponse
		expectStatus codes.Code
		// expectMessage is part of the error a caller reads, telling it what to fix.
		expectMessage string
		// expectCause is the error the fault wraps, whose type the call's span records.
		expectCause error
		// expectQuota is the limit a refusal's details name, and expectRetry when its window frees usage.
		expectQuota *errdetails.QuotaFailure_Violation
		expectRetry time.Duration
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
			// The caller's caps reach the service as sent: a limit left out stays unset.
			name: "Success/Caps",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "openai", Payload: payload,
				Caps: &genaiv0.GenerationCaps{
					Concurrency: new(int32(2)),
					Windows: []*genaiv0.GenerationCapWindow{
						{Duration: durationpb.New(5 * time.Hour), InputTokens: new(int64(1000))},
						{Duration: durationpb.New(720 * time.Hour), OutputTokens: new(int64(50))},
					},
				},
			},
			expectCaps: &core.GenerationCaps{
				Concurrency: new(2),
				Windows: []core.GenerationCapWindow{
					{Duration: 5 * time.Hour, InputTokens: new(int64(1000))},
					{Duration: 720 * time.Hour, OutputTokens: new(int64(50))},
				},
			},
			serviceMock: &serviceMock{resp: &core.GenerationSubmitResult{
				Generation: testGeneration(), Created: true,
			}},

			expect: &genaiv0.GenerationSubmitResponse{Generation: testGrpcGeneration(), Created: true},
		},
		{
			// Empty caps set no limit beyond the ceiling, which is not the same as sending none.
			name: "Success/EmptyCaps",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "openai", Payload: payload,
				Caps: &genaiv0.GenerationCaps{},
			},
			expectCaps: &core.GenerationCaps{Windows: []core.GenerationCapWindow{}},
			serviceMock: &serviceMock{resp: &core.GenerationSubmitResult{
				Generation: testGeneration(), Created: true,
			}},

			expect: &genaiv0.GenerationSubmitResponse{Generation: testGrpcGeneration(), Created: true},
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
			// The details name the limit in fields, and tell when the window frees usage.
			name: "Error/UsageCapDetails",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "openai", Payload: payload,
			},
			serviceMock: &serviceMock{err: refusal},

			expectStatus:  codes.ResourceExhausted,
			expectMessage: `usage cap reached: purpose "studio.generation" over 5h0m0s`,
			expectQuota: &errdetails.QuotaFailure_Violation{
				Subject: `purpose "studio.generation"`, Description: "input tokens over 5h",
			},
			expectRetry: time.Hour,
		},
		{
			// A concurrency frees at no known time: the refusal carries no retry delay.
			name: "Error/UsageCapDetailsNoRetry",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", Provider: "openai", Payload: payload,
			},
			serviceMock: &serviceMock{err: &core.UsageCapError{
				Scope: "ceiling", Limit: "concurrency 8", Reason: "ceiling concurrency: 8 generations already in flight",
			}},

			expectStatus:  codes.ResourceExhausted,
			expectMessage: "usage cap reached: ceiling concurrency: 8 generations already in flight",
			expectQuota:   &errdetails.QuotaFailure_Violation{Subject: "ceiling", Description: "concurrency 8"},
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
					Caps:     testCase.expectCaps,
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

			var (
				quota *errdetails.QuotaFailure
				retry *errdetails.RetryInfo
			)

			for _, detail := range resSt.Details() {
				switch detail := detail.(type) {
				case *errdetails.QuotaFailure:
					quota = detail
				case *errdetails.RetryInfo:
					retry = detail
				}
			}

			if testCase.expectQuota == nil {
				require.Nil(t, quota)
			} else {
				require.Len(t, quota.GetViolations(), 1)
				require.Equal(t, testCase.expectQuota.GetSubject(), quota.GetViolations()[0].GetSubject())
				require.Equal(t, testCase.expectQuota.GetDescription(), quota.GetViolations()[0].GetDescription())
			}

			if testCase.expectRetry == 0 {
				require.Nil(t, retry)
			} else {
				require.Equal(t, testCase.expectRetry, retry.GetRetryDelay().AsDuration())
			}

			service.AssertExpectations(t)
		})
	}
}
