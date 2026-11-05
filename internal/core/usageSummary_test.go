package core_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
)

func TestUsageSummary(t *testing.T) {
	t.Parallel()

	type liveMock struct {
		count int64
		err   error
	}

	type usageMock struct {
		used []*dao.GenerationUsageTotals
		err  error
	}

	testCases := []struct {
		name string

		request *core.UsageSummaryRequest

		liveMock  *liveMock
		usageMock *usageMock

		expect    *core.UsageSummaryResult
		expectErr error
	}{
		{
			// The sums come back in the request's order, whatever the durations.
			name: "Success",

			request: &core.UsageSummaryRequest{
				OwnerID: testOwner, Purpose: testPurpose, Windows: []time.Duration{720 * time.Hour, 5 * time.Hour},
			},
			liveMock: &liveMock{count: 2},
			usageMock: &usageMock{used: []*dao.GenerationUsageTotals{
				{InputTokens: 3000, OutputTokens: 300}, {InputTokens: 1000, OutputTokens: 100},
			}},

			expect: &core.UsageSummaryResult{Live: 2, Windows: []*core.UsageSummaryWindow{
				{Duration: 720 * time.Hour, InputTokens: 3000, OutputTokens: 300},
				{Duration: 5 * time.Hour, InputTokens: 1000, OutputTokens: 100},
			}},
		},
		{
			// With no window to sum, the count alone answers.
			name: "Success/NoWindows",

			request:  &core.UsageSummaryRequest{OwnerID: testOwner, Purpose: testPurpose},
			liveMock: &liveMock{count: 1},

			expect: &core.UsageSummaryResult{Live: 1, Windows: []*core.UsageSummaryWindow{}},
		},
		{
			name: "Error/UndeclaredPurpose",

			request: &core.UsageSummaryRequest{OwnerID: testOwner, Purpose: "studio.unknown"},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/NoOwner",

			request: &core.UsageSummaryRequest{OwnerID: uuid.Nil, Purpose: testPurpose},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/RepeatedWindow",

			request: &core.UsageSummaryRequest{
				OwnerID: testOwner, Purpose: testPurpose, Windows: []time.Duration{time.Hour, 60 * time.Minute},
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/WindowPastAYear",

			request: &core.UsageSummaryRequest{
				OwnerID: testOwner, Purpose: testPurpose, Windows: []time.Duration{8761 * time.Hour},
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/EmptyWindow",

			request: &core.UsageSummaryRequest{OwnerID: testOwner, Purpose: testPurpose, Windows: []time.Duration{0}},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/TooManyWindows",

			request: &core.UsageSummaryRequest{OwnerID: testOwner, Purpose: testPurpose, Windows: func() []time.Duration {
				windows := make([]time.Duration, 11)
				for index := range windows {
					windows[index] = time.Duration(index+1) * time.Hour
				}

				return windows
			}()},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/CountLive",

			request:  &core.UsageSummaryRequest{OwnerID: testOwner, Purpose: testPurpose},
			liveMock: &liveMock{err: errFoo},

			expectErr: errFoo,
		},
		{
			name: "Error/UsageSum",

			request:   &core.UsageSummaryRequest{OwnerID: testOwner, Purpose: testPurpose, Windows: []time.Duration{time.Hour}},
			liveMock:  &liveMock{count: 1},
			usageMock: &usageMock{err: errFoo},

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			liveDao := coremocks.NewMockUsageSummaryDaoCountLive(t)
			usageDao := coremocks.NewMockUsageSummaryDaoUsageSum(t)

			if testCase.liveMock != nil {
				// The count a submit's concurrency reads, with the generations whose call may still run.
				liveDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationCountLiveRequest{
						OwnerID: testCase.request.OwnerID, Purpose: &testCase.request.Purpose,
						UnknownOutcomeWindow: 15 * time.Minute,
					}).
					Return(&dao.GenerationLiveCount{Count: testCase.liveMock.count}, testCase.liveMock.err)
			}

			if testCase.usageMock != nil {
				usageDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationUsageSumRequest{
						OwnerID: testCase.request.OwnerID, Purpose: &testCase.request.Purpose,
						Windows: testCase.request.Windows,
					}).
					Return(testCase.usageMock.used, testCase.usageMock.err)
			}

			result, err := core.NewUsageSummary(liveDao, usageDao, testCaps).Exec(t.Context(), testCase.request)
			require.ErrorIs(t, err, testCase.expectErr)
			require.Equal(t, testCase.expect, result)

			mock.AssertExpectationsForObjects(t, liveDao, usageDao)
		})
	}
}
