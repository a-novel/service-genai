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
	keyFields := []string{"owner_id", "purpose", "tier", "instructions", "input", "output_schema", "variant"}

	fields := (&genaiv0.GenerationSubmitRequest{}).ProtoReflect().Descriptor().Fields()
	for index := range fields.Len() {
		require.Contains(t, keyFields, string(fields.Get(index).Name()))
	}

	type serviceMock struct {
		resp *core.GenerationSubmitResult
		err  error
	}

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
				OwnerId: testOwnerID, Purpose: "studio.generation",
				Tier: genaiv0.Tier_TIER_BALANCED, Instructions: "Continue.",
				Input: []byte(`{"scene": "a door"}`), OutputSchema: []byte(`{"type": "object"}`), Variant: 2,
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
				OwnerId: testOwnerID, Purpose: "studio.generation",
				Tier: genaiv0.Tier_TIER_BALANCED, Instructions: "Continue.",
				Input: []byte(`{"scene": "a door"}`), OutputSchema: []byte(`{"type": "object"}`),
			},
			serviceMock: &serviceMock{resp: &core.GenerationSubmitResult{
				Generation: testGeneration(), Created: false,
			}},
		},
		{
			name: "Error/InvalidOwnerID",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: "not-a-uuid", Purpose: "studio.generation",
				Tier: genaiv0.Tier_TIER_BALANCED, Instructions: "Continue.",
				Input: []byte(`{"scene": "a door"}`), OutputSchema: []byte(`{"type": "object"}`),
			},

			expectStatus: codes.InvalidArgument,
		},
		{
			// An unspecified Tier maps to none, which the core layer refuses.
			name: "Error/UnspecifiedTier",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation",
				Instructions: "Continue.", Input: []byte(`{}`), OutputSchema: []byte(`{"type": "object"}`),
			},
			serviceMock: &serviceMock{err: fmt.Errorf("%w: tier is required", core.ErrInvalidRequest)},

			expectStatus:  codes.InvalidArgument,
			expectMessage: "tier is required",
		},
		{
			name: "Error/Internal",

			request: &genaiv0.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation",
				Tier: genaiv0.Tier_TIER_BALANCED, Instructions: "Continue.",
				Input: []byte(`{"scene": "a door"}`), OutputSchema: []byte(`{"type": "object"}`),
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
						expectTier := map[genaiv0.Tier]lib.Tier{genaiv0.Tier_TIER_BALANCED: lib.TierBalanced}

						return request.OwnerID == uuid.MustParse(testCase.request.GetOwnerId()) &&
							request.Tier == expectTier[testCase.request.GetTier()] &&
							request.Instructions == testCase.request.GetInstructions() &&
							string(request.Input) == string(testCase.request.GetInput()) &&
							string(request.OutputSchema) == string(testCase.request.GetOutputSchema()) &&
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
