package handlers_test

import (
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

func TestGrpcGenerationCancel(t *testing.T) {
	t.Parallel()

	type serviceMock struct {
		resp *core.Generation
		err  error
	}

	testCases := []struct {
		name string

		request *genaiv0.GenerationCancelRequest

		serviceMock *serviceMock

		expect       *genaiv0.GenerationCancelResponse
		expectStatus codes.Code
	}{
		{
			name: "Success",

			request:     &genaiv0.GenerationCancelRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{resp: testGeneration()},

			expect: &genaiv0.GenerationCancelResponse{Generation: testGrpcGeneration()},
		},
		{
			// Somebody else's generation reports as absent, so an id cannot be probed for existence.
			name: "Error/NotFound",

			request:     &genaiv0.GenerationCancelRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{err: core.ErrGenerationNotFound},

			expectStatus: codes.NotFound,
		},
		{
			// A well-formed but nil id is refused by the core layer, not mistaken for a fault.
			name: "Error/NilGenerationID",

			request:     &genaiv0.GenerationCancelRequest{Id: "00000000-0000-0000-0000-000000000000", OwnerId: testOwnerID},
			serviceMock: &serviceMock{err: core.ErrInvalidRequest},

			expectStatus: codes.InvalidArgument,
		},
		{
			name: "Error/InvalidGenerationID",

			request: &genaiv0.GenerationCancelRequest{Id: "not-a-uuid", OwnerId: testOwnerID},

			expectStatus: codes.InvalidArgument,
		},
		{
			name: "Error/InvalidOwnerID",

			request: &genaiv0.GenerationCancelRequest{Id: testGenerationID, OwnerId: "not-a-uuid"},

			expectStatus: codes.InvalidArgument,
		},
		{
			name: "Error/Internal",

			request:     &genaiv0.GenerationCancelRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{err: errFoo},

			expectStatus: codes.Internal,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			service := handlersmocks.NewMockGrpcGenerationCancelService(t)

			if testCase.serviceMock != nil {
				service.EXPECT().
					Exec(mock.Anything, &core.GenerationCancelRequest{
						ID: uuid.MustParse(testCase.request.GetId()), OwnerID: uuid.MustParse(testCase.request.GetOwnerId()),
					}).
					Return(testCase.serviceMock.resp, testCase.serviceMock.err)
			}

			res, err := handlers.NewGrpcGenerationCancel(service).GenerationCancel(t.Context(), testCase.request)
			resSt, ok := status.FromError(err)
			require.True(t, ok, resSt.Code().String())
			require.Equal(
				t,
				testCase.expectStatus, resSt.Code(),
				"expected status code %s, got %s (%v)", testCase.expectStatus, resSt.Code(), err,
			)
			require.Equal(t, testCase.expect, res)

			service.AssertExpectations(t)
		})
	}
}
