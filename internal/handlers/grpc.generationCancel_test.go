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
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

func TestGrpcGenerationCancel(t *testing.T) {
	t.Parallel()

	type serviceMock struct {
		resp *dao.Generation
		err  error
	}

	testCases := []struct {
		name string

		request *genaiv0.GenerationCancelRequest

		serviceMock *serviceMock

		expectStatus codes.Code
	}{
		{
			// The request is recorded and the status is unchanged: the worker settles it once the
			// provider operation has actually stopped, recording what was spent.
			name: "Success",

			request:     &genaiv0.GenerationCancelRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{resp: testGeneration()},
		},
		{
			// A settled generation and somebody else's report the same thing. Telling them apart
			// would confirm that another owner's id is real.
			name: "Error/NotCancellable",

			request:     &genaiv0.GenerationCancelRequest{Id: testGenerationID, OwnerId: testOwnerID},
			serviceMock: &serviceMock{err: core.ErrGenerationNotCancellable},

			expectStatus: codes.NotFound,
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
					Exec(mock.Anything, mock.Anything).
					Return(testCase.serviceMock.resp, testCase.serviceMock.err)
			}

			response, err := handlers.NewGrpcGenerationCancel(service).
				GenerationCancel(t.Context(), testCase.request)

			if testCase.expectStatus != codes.OK {
				require.Equal(t, testCase.expectStatus, status.Code(err))
				require.Nil(t, response)

				return
			}

			require.NoError(t, err)
			require.Equal(t, testGenerationID, response.GetGeneration().GetId())

			service.AssertExpectations(t)
		})
	}
}
