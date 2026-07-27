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
	"github.com/a-novel/service-genai/internal/handlers/protogen"
)

func TestGrpcUsageQuery(t *testing.T) {
	t.Parallel()

	const (
		from = "2026-01-01T00:00:00Z"
		to   = "2026-02-01T00:00:00Z"
	)

	type serviceMock struct {
		resp *core.UsageQueryResult
		err  error
	}

	testCases := []struct {
		name string

		request *protogen.UsageQueryRequest

		serviceMock *serviceMock

		expectGroups int
		expectStatus codes.Code
	}{
		{
			name: "Success",

			request: &protogen.UsageQueryRequest{OwnerId: testOwnerID, From: from, To: to},
			serviceMock: &serviceMock{resp: &core.UsageQueryResult{
				Groups: []*dao.GenerationUsageGroup{
					{Purpose: "studio.generation", Model: "model-a", InputTokens: 100, Attempts: 2},
				},
				Total: core.UsageTotal{InputTokens: 100, Attempts: 2},
			}},

			expectGroups: 1,
		},
		{
			// An owner with no consumption gets an empty report, not an error.
			name: "Success/NoUsage",

			request:     &protogen.UsageQueryRequest{OwnerId: testOwnerID, From: from, To: to},
			serviceMock: &serviceMock{resp: &core.UsageQueryResult{}},
		},
		{
			name: "Error/InvalidOwnerID",

			request: &protogen.UsageQueryRequest{OwnerId: "not-a-uuid", From: from, To: to},

			expectStatus: codes.InvalidArgument,
		},
		{
			name: "Error/InvalidFrom",

			request: &protogen.UsageQueryRequest{OwnerId: testOwnerID, From: "yesterday", To: to},

			expectStatus: codes.InvalidArgument,
		},
		{
			name: "Error/InvalidTo",

			request: &protogen.UsageQueryRequest{OwnerId: testOwnerID, From: from, To: ""},

			expectStatus: codes.InvalidArgument,
		},
		{
			// The window ceiling is a core decision; the handler only has to relay the refusal.
			name: "Error/RejectedByTheService",

			request:     &protogen.UsageQueryRequest{OwnerId: testOwnerID, From: from, To: to},
			serviceMock: &serviceMock{err: core.ErrInvalidRequest},

			expectStatus: codes.InvalidArgument,
		},
		{
			name: "Error/Internal",

			request:     &protogen.UsageQueryRequest{OwnerId: testOwnerID, From: from, To: to},
			serviceMock: &serviceMock{err: errFoo},

			expectStatus: codes.Internal,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			service := handlersmocks.NewMockGrpcUsageQueryService(t)

			if testCase.serviceMock != nil {
				service.EXPECT().
					Exec(mock.Anything, mock.Anything).
					Return(testCase.serviceMock.resp, testCase.serviceMock.err)
			}

			response, err := handlers.NewGrpcUsageQuery(service).UsageQuery(t.Context(), testCase.request)

			if testCase.expectStatus != codes.OK {
				require.Equal(t, testCase.expectStatus, status.Code(err))
				require.Nil(t, response)

				return
			}

			require.NoError(t, err)
			require.Len(t, response.GetGroups(), testCase.expectGroups)
			// The total is always present, so a quota layer reading only it never has to nil-check.
			require.NotNil(t, response.GetTotal())
			require.Equal(t, testCase.serviceMock.resp.Total.InputTokens, response.GetTotal().GetInputTokens())

			service.AssertExpectations(t)
		})
	}
}
