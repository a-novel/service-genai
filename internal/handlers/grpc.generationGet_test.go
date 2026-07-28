package handlers_test

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/handlers"
	handlersmocks "github.com/a-novel/service-genai/internal/handlers/mocks"
	genaiv1 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v1"
)

func TestGrpcGenerationGet(t *testing.T) {
	t.Parallel()

	type serviceMock struct {
		resp *dao.Generation
		err  error
	}

	testCases := []struct {
		name string

		request *genaiv1.GenerationGetRequest

		serviceMock *serviceMock

		expectStatus codes.Code
	}{
		{
			name: "Success",

			request:     &genaiv1.GenerationGetRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{resp: testGeneration()},
		},
		{
			// Another owner's generation reports exactly this, so an id cannot be probed for
			// existence. A distinct "forbidden" would confirm it is real.
			name: "Error/NotFound",

			request:     &genaiv1.GenerationGetRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{err: core.ErrGenerationNotFound},

			expectStatus: codes.NotFound,
		},
		{
			name: "Error/InvalidGenerationID",

			request: &genaiv1.GenerationGetRequest{Id: "not-a-uuid", OwnerId: testOwnerID},

			expectStatus: codes.InvalidArgument,
		},
		{
			name: "Error/InvalidOwnerID",

			request: &genaiv1.GenerationGetRequest{Id: testGenerationID, OwnerId: "not-a-uuid"},

			expectStatus: codes.InvalidArgument,
		},
		{
			name: "Error/Internal",

			request:     &genaiv1.GenerationGetRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{err: errFoo},

			expectStatus: codes.Internal,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			service := handlersmocks.NewMockGrpcGenerationGetService(t)

			if testCase.serviceMock != nil {
				service.EXPECT().
					Exec(mock.Anything, mock.Anything).
					Return(testCase.serviceMock.resp, testCase.serviceMock.err)
			}

			response, err := handlers.NewGrpcGenerationGet(service).
				GenerationGet(t.Context(), testCase.request)

			if testCase.expectStatus != codes.OK {
				require.Equal(t, testCase.expectStatus, status.Code(err))
				require.Nil(t, response)

				return
			}

			require.NoError(t, err)
			require.Equal(t, testGenerationID, response.GetGeneration().GetId())
			// The request and the provider call identifier are never published: the caller already
			// has its request, and the identifier is this service's recovery mechanism.
			require.Equal(t, genaiv1.GenerationStatus_GENERATION_STATUS_PENDING, response.GetGeneration().GetStatus())

			service.AssertExpectations(t)
		})
	}
}
