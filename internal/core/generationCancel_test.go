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

func TestGenerationCancel(t *testing.T) {
	t.Parallel()

	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	generationID := uuid.MustParse("01999999-0000-7000-8000-000000000001")

	const retention = time.Hour

	type daoMock struct {
		resp *dao.Generation
		err  error
	}

	testCases := []struct {
		name string

		request *core.GenerationCancelRequest

		daoMock   *daoMock
		checkMock *daoMock

		expectStatus core.GenerationStatus
		expectErr    error
	}{
		{
			// Nothing was sent, so the data access settles it in the same statement.
			name: "Success/NeverStarted",

			request: &core.GenerationCancelRequest{ID: generationID, OwnerID: owner},
			daoMock: &daoMock{resp: settledGeneration(dao.GenerationStatusCancelled)},

			expectStatus: core.GenerationStatusCancelled,
		},
		{
			// The start may be accepted at any moment with no id recorded yet. The next check stops it.
			name: "Success/StartInFlight",

			request: &core.GenerationCancelRequest{ID: generationID, OwnerID: owner},
			daoMock: &daoMock{resp: withCancel(startingGeneration(time.Second))},

			expectStatus: core.GenerationStatusPending,
		},
		{
			// A known call is stopped now, and settles with what it consumed before the stop.
			name: "Success/Running",

			request:   &core.GenerationCancelRequest{ID: generationID, OwnerID: owner},
			daoMock:   &daoMock{resp: withCancel(runningGeneration(1))},
			checkMock: &daoMock{resp: settledGeneration(dao.GenerationStatusCancelled)},

			expectStatus: core.GenerationStatusCancelled,
		},
		{
			// A settled generation and somebody else's are one error, so an identifier cannot be
			// probed for existence.
			name: "Error/NotCancellable",

			request: &core.GenerationCancelRequest{ID: generationID, OwnerID: owner},
			daoMock: &daoMock{err: dao.ErrGenerationNotCancellable},

			expectErr: core.ErrGenerationNotCancellable,
		},
		{
			name: "Error/NoOwner",

			request: &core.GenerationCancelRequest{ID: generationID},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/Internal",

			request: &core.GenerationCancelRequest{ID: generationID, OwnerID: owner},
			daoMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
		{
			name: "Error/Check",

			request:   &core.GenerationCancelRequest{ID: generationID, OwnerID: owner},
			daoMock:   &daoMock{resp: withCancel(runningGeneration(1))},
			checkMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cancelDao := coremocks.NewMockGenerationCancelDao(t)
			check := coremocks.NewMockGenerationCancelServiceCheck(t)

			if testCase.daoMock != nil {
				cancelDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationRequestCancelRequest{
						ID:        testCase.request.ID,
						OwnerID:   testCase.request.OwnerID,
						Error:     "generation cancelled",
						Retention: retention,
					}).
					Return(testCase.daoMock.resp, testCase.daoMock.err)
			}

			if testCase.checkMock != nil {
				check.EXPECT().
					Exec(mock.Anything, &core.GenerationCheckRequest{Generation: testCase.daoMock.resp}).
					Return(testCase.checkMock.resp, testCase.checkMock.err)
			}

			service, err := core.NewGenerationCancel(core.GenerationCancelConfig{Retention: retention}, cancelDao, check)
			require.NoError(t, err)

			result, err := service.Exec(t.Context(), testCase.request)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, testCase.expectStatus, result.Status)
			}

			cancelDao.AssertExpectations(t)
			check.AssertExpectations(t)
		})
	}
}
