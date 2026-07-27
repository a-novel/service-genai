package core_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
)

func TestQueueDepth(t *testing.T) {
	t.Parallel()

	type daoMock struct {
		resp *dao.GenerationQueueDepth
		err  error
	}

	testCases := []struct {
		name string

		daoMock *daoMock

		expectPending int64
		expectAge     time.Duration
		expectErr     error
	}{
		{
			name: "Success/EmptyQueue",

			daoMock: &daoMock{resp: &dao.GenerationQueueDepth{}},
		},
		{
			// The seconds the query produced become a duration here, because converting a result is
			// this layer's job and not the data access's.
			name: "Success/Backlog",

			daoMock: &daoMock{resp: &dao.GenerationQueueDepth{
				Pending: 3, OldestPendingAgeSeconds: 90,
			}},

			expectPending: 3,
			expectAge:     90 * time.Second,
		},
		{
			// A count alone cannot tell a queue absorbing a burst from a stalled one, so a
			// sub-second age must survive the conversion rather than round to zero.
			name: "Success/SubSecondAge",

			daoMock: &daoMock{resp: &dao.GenerationQueueDepth{
				Pending: 1, OldestPendingAgeSeconds: 0.25,
			}},

			expectPending: 1,
			expectAge:     250 * time.Millisecond,
		},
		{
			name: "Error/Internal",

			daoMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			depthDao := coremocks.NewMockQueueDepthDao(t)

			depthDao.EXPECT().
				Exec(mock.Anything).
				Return(testCase.daoMock.resp, testCase.daoMock.err)

			result, err := core.NewQueueDepth(depthDao).Exec(t.Context())
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)

				return
			}

			require.Equal(t, testCase.expectPending, result.Pending)
			require.Equal(t, testCase.expectAge, result.OldestPendingAge)

			depthDao.AssertExpectations(t)
		})
	}
}
