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

func TestGenerationQueueDepth(t *testing.T) {
	t.Parallel()

	type daoMock struct {
		resp *dao.GenerationQueueDepth
		err  error
	}

	testCases := []struct {
		name string

		daoMock *daoMock

		expect    *core.GenerationQueueDepthResult
		expectErr error
	}{
		{
			name: "Success/EmptyQueue",

			daoMock: &daoMock{resp: &dao.GenerationQueueDepth{}},

			expect: &core.GenerationQueueDepthResult{},
		},
		{
			// A sub-second age must survive the conversion.
			name: "Success/SubSecondAge",

			daoMock: &daoMock{resp: &dao.GenerationQueueDepth{
				Pending: 1, OldestPendingAgeSeconds: 0.25,
			}},

			expect: &core.GenerationQueueDepthResult{Pending: 1, OldestPendingAge: 250 * time.Millisecond},
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

			depthDao := coremocks.NewMockGenerationQueueDepthDao(t)

			depthDao.EXPECT().
				Exec(mock.Anything, &dao.GenerationQueueDepthRequest{}).
				Return(testCase.daoMock.resp, testCase.daoMock.err)

			result, err := core.NewGenerationQueueDepth(depthDao).Exec(t.Context(), &core.GenerationQueueDepthRequest{})
			require.ErrorIs(t, err, testCase.expectErr)
			require.Equal(t, testCase.expect, result)

			depthDao.AssertExpectations(t)
		})
	}
}
