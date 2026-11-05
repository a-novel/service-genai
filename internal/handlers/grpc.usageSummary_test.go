package handlers_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/handlers"
	handlersmocks "github.com/a-novel/service-genai/internal/handlers/mocks"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

func TestGrpcUsageSummary(t *testing.T) {
	t.Parallel()

	type serviceMock struct {
		resp *core.UsageSummaryResult
		err  error
	}

	testCases := []struct {
		name string

		request *genaiv0.UsageSummaryRequest
		// expectWindows are the windows the service receives.
		expectWindows []time.Duration

		serviceMock *serviceMock

		expect        *genaiv0.UsageSummaryResponse
		expectStatus  codes.Code
		expectMessage string
		expectCause   error
	}{
		{
			// The windows come back in the request's order, with their durations.
			name: "Success",

			request: &genaiv0.UsageSummaryRequest{
				OwnerId: testOwnerID, Purpose: "studio.generation",
				Windows: []*durationpb.Duration{durationpb.New(720 * time.Hour), durationpb.New(5 * time.Hour)},
			},
			expectWindows: []time.Duration{720 * time.Hour, 5 * time.Hour},
			serviceMock: &serviceMock{resp: &core.UsageSummaryResult{Live: 2, Windows: []*core.UsageSummaryWindow{
				{Duration: 720 * time.Hour, InputTokens: 3000, OutputTokens: 300},
				{Duration: 5 * time.Hour, InputTokens: 1000, OutputTokens: 100},
			}}},

			expect: &genaiv0.UsageSummaryResponse{Live: 2, Windows: []*genaiv0.UsageSummaryWindow{
				{Duration: durationpb.New(720 * time.Hour), InputTokens: 3000, OutputTokens: 300},
				{Duration: durationpb.New(5 * time.Hour), InputTokens: 1000, OutputTokens: 100},
			}},
		},
		{
			name: "Success/NoWindows",

			request:       &genaiv0.UsageSummaryRequest{OwnerId: testOwnerID, Purpose: "studio.generation"},
			expectWindows: []time.Duration{},
			serviceMock:   &serviceMock{resp: &core.UsageSummaryResult{Live: 1, Windows: []*core.UsageSummaryWindow{}}},

			expect: &genaiv0.UsageSummaryResponse{Live: 1, Windows: []*genaiv0.UsageSummaryWindow{}},
		},
		{
			name: "Error/InvalidOwnerID",

			request: &genaiv0.UsageSummaryRequest{OwnerId: "not-a-uuid", Purpose: "studio.generation"},

			expectStatus: codes.InvalidArgument,
		},
		{
			// The reason names the caller's own fields, so it tells the caller what to fix.
			name: "Error/InvalidRequest",

			request:       &genaiv0.UsageSummaryRequest{OwnerId: testOwnerID, Purpose: "studio.unknown"},
			expectWindows: []time.Duration{},
			serviceMock:   &serviceMock{err: fmt.Errorf("%w: purpose not declared", core.ErrInvalidRequest)},

			expectStatus:  codes.InvalidArgument,
			expectMessage: "purpose not declared",
		},
		{
			name: "Error/Internal",

			request:       &genaiv0.UsageSummaryRequest{OwnerId: testOwnerID, Purpose: "studio.generation"},
			expectWindows: []time.Duration{},
			serviceMock:   &serviceMock{err: errFoo},

			expectStatus: codes.Internal,
			expectCause:  errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			service := handlersmocks.NewMockGrpcUsageSummaryService(t)

			if testCase.serviceMock != nil {
				service.EXPECT().
					Exec(mock.Anything, &core.UsageSummaryRequest{
						OwnerID: uuid.MustParse(testCase.request.GetOwnerId()),
						Purpose: testCase.request.GetPurpose(),
						Windows: testCase.expectWindows,
					}).
					Return(testCase.serviceMock.resp, testCase.serviceMock.err)
			}

			res, err := handlers.NewGrpcUsageSummary(service).UsageSummary(t.Context(), testCase.request)
			resSt, ok := status.FromError(err)
			require.True(t, ok, resSt.Code().String())
			require.Equal(t, testCase.expectStatus, resSt.Code(), "got %s (%v)", resSt.Code(), err)
			require.Contains(t, resSt.Message(), testCase.expectMessage)

			if testCase.expectCause != nil {
				require.ErrorIs(t, err, testCase.expectCause)
			}

			require.Equal(t, testCase.expect, res)

			service.AssertExpectations(t)
		})
	}
}
