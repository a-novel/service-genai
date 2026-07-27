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
	"github.com/a-novel/service-genai/internal/handlers/protogen"
)

func TestGrpcGenerationSubmit(t *testing.T) {
	t.Parallel()

	type serviceMock struct {
		resp *core.GenerationSubmitResult
		err  error
	}

	testCases := []struct {
		name string

		request *protogen.GenerationSubmitRequest

		serviceMock *serviceMock

		expectCreated bool
		expectStatus  codes.Code
	}{
		{
			name: "Success",

			request: &protogen.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", IdempotencyKey: "key",
				Request: []byte(`{"model": "a-model"}`), MaxAttempts: 3,
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

			request: &protogen.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", IdempotencyKey: "key",
				Request: []byte(`{"model": "a-model"}`),
			},
			serviceMock: &serviceMock{resp: &core.GenerationSubmitResult{
				Generation: testGeneration(), Created: false,
			}},
		},
		{
			name: "Error/InvalidOwnerID",

			request: &protogen.GenerationSubmitRequest{
				OwnerId: "not-a-uuid", Purpose: "studio.generation", IdempotencyKey: "key",
				Request: []byte(`{"model": "a-model"}`),
			},

			expectStatus: codes.InvalidArgument,
		},
		{
			// An unkeyed submission of a priced call is refused rather than defaulted.
			name: "Error/NoIdempotencyKey",

			request: &protogen.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation",
				Request: []byte(`{"model": "a-model"}`),
			},
			serviceMock: &serviceMock{err: core.ErrInvalidRequest},

			expectStatus: codes.InvalidArgument,
		},
		{
			// The key is held by a different request. Answering with the earlier generation would
			// answer a question the caller never asked.
			name: "Error/IdempotencyConflict",

			request: &protogen.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", IdempotencyKey: "key",
				Request: []byte(`{"model": "something else"}`),
			},
			serviceMock: &serviceMock{err: core.ErrIdempotencyConflict},

			expectStatus: codes.AlreadyExists,
		},
		{
			name: "Error/Internal",

			request: &protogen.GenerationSubmitRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation", IdempotencyKey: "key",
				Request: []byte(`{"model": "a-model"}`),
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
							request.IdempotencyKey == testCase.request.GetIdempotencyKey()
					})).
					Return(testCase.serviceMock.resp, testCase.serviceMock.err)
			}

			response, err := handlers.NewGrpcGenerationSubmit(service).
				GenerationSubmit(t.Context(), testCase.request)

			if testCase.expectStatus != codes.OK {
				require.Equal(t, testCase.expectStatus, status.Code(err))
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
