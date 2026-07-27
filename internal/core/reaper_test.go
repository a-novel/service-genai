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

func TestReaper(t *testing.T) {
	t.Parallel()

	testConfig := core.ReaperConfig{Grace: 30 * time.Second, BatchSize: 10, Retention: time.Hour}

	type daoMock struct {
		resp []*dao.Generation
		err  error
	}

	testCases := []struct {
		name string

		config core.ReaperConfig

		daoMock *daoMock

		expectWorked bool
		expectErr    error
	}{
		{
			name: "Success/Recovered",

			config:  testConfig,
			daoMock: &daoMock{resp: []*dao.Generation{{}, {}}},

			expectWorked: true,
		},
		{
			name: "Success/NothingStranded",

			config:  testConfig,
			daoMock: &daoMock{},
		},
		{
			name: "Error/Internal",

			config:  testConfig,
			daoMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			reaperDao := coremocks.NewMockReaperDao(t)

			reaperDao.EXPECT().
				Exec(mock.Anything, &dao.GenerationReapRequest{
					Grace: testCase.config.Grace, Retention: testCase.config.Retention,
					Limit: testCase.config.BatchSize,
				}).
				Return(testCase.daoMock.resp, testCase.daoMock.err)

			reaper, err := core.NewReaper(testCase.config, reaperDao)
			require.NoError(t, err)

			worked, err := reaper.RunOnce(t.Context())
			require.ErrorIs(t, err, testCase.expectErr)
			require.Equal(t, testCase.expectWorked, worked)

			reaperDao.AssertExpectations(t)
		})
	}
}

func TestNewReaper(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		config core.ReaperConfig

		expectErr error
	}{
		{
			name:   "Success",
			config: core.ReaperConfig{Grace: 0, BatchSize: 10, Retention: time.Hour},
		},
		{
			// An unbounded sweep would materialise every stranded claim at once.
			name:      "Error/BatchOverCeiling",
			config:    core.ReaperConfig{BatchSize: core.ClaimLimitCeiling + 1, Retention: time.Hour},
			expectErr: core.ErrInvalidRequest,
		},
		{
			name:      "Error/NoBatchSize",
			config:    core.ReaperConfig{Retention: time.Hour},
			expectErr: core.ErrInvalidRequest,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			reaper, err := core.NewReaper(testCase.config, coremocks.NewMockReaperDao(t))
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, reaper)
			} else {
				require.NotNil(t, reaper)
			}
		})
	}
}
