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

func TestUsageQuery(t *testing.T) {
	t.Parallel()

	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	type daoMock struct {
		resp []*dao.GenerationUsageGroup
		err  error
	}

	testCases := []struct {
		name string

		request *core.UsageQueryRequest

		daoMock *daoMock

		expectTotal core.UsageTotal
		expectErr   error
	}{
		{
			// The groups come from the database; the total is the handful of numbers left, summed
			// here rather than in a second round trip.
			name: "Success",

			request: &core.UsageQueryRequest{OwnerID: owner, From: from, To: to},
			daoMock: &daoMock{resp: []*dao.GenerationUsageGroup{
				{
					Purpose: "studio.generation", Model: "model-a",
					InputTokens: 100, CachedInputTokens: 20, OutputTokens: 50, ReasoningTokens: 10,
					Attempts: 2,
				},
				{
					Purpose: "studio.generation", Model: "model-b",
					InputTokens: 30, CachedInputTokens: 5, OutputTokens: 10, ReasoningTokens: 2,
					Attempts: 1,
				},
			}},

			expectTotal: core.UsageTotal{
				InputTokens: 130, CachedInputTokens: 25, OutputTokens: 60, ReasoningTokens: 12,
				Attempts: 3,
			},
		},
		{
			name: "Success/NoUsage",

			request: &core.UsageQueryRequest{OwnerID: owner, From: from, To: to},
			daoMock: &daoMock{},
		},
		{
			name: "Error/NoOwner",

			request: &core.UsageQueryRequest{From: from, To: to},

			expectErr: core.ErrInvalidRequest,
		},
		{
			// This record is never purged, so an unbounded scan grows without limit and a caller
			// has no way to know it asked for one.
			name: "Error/NoWindow",

			request: &core.UsageQueryRequest{OwnerID: owner},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/InvertedWindow",

			request: &core.UsageQueryRequest{OwnerID: owner, From: to, To: from},

			expectErr: core.ErrInvalidRequest,
		},
		{
			// A wider span is a report, and a report should page rather than ask the database for
			// everything at once.
			name: "Error/WindowOverCeiling",

			request: &core.UsageQueryRequest{
				OwnerID: owner, From: from, To: from.Add(core.MaxUsageWindow + time.Hour),
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/Internal",

			request: &core.UsageQueryRequest{OwnerID: owner, From: from, To: to},
			daoMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			queryDao := coremocks.NewMockUsageQueryDao(t)

			if testCase.daoMock != nil {
				queryDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationUsageQueryRequest{
						OwnerID: testCase.request.OwnerID,
						From:    testCase.request.From,
						To:      testCase.request.To,
					}).
					Return(testCase.daoMock.resp, testCase.daoMock.err)
			}

			result, err := core.NewUsageQuery(queryDao).Exec(t.Context(), testCase.request)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)

				return
			}

			require.Len(t, result.Groups, len(testCase.daoMock.resp))
			require.Equal(t, testCase.expectTotal, result.Total)

			queryDao.AssertExpectations(t)
		})
	}
}
