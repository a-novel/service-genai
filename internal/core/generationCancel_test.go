package core_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
)

func TestGenerationCancel(t *testing.T) {
	t.Parallel()

	const retention = time.Hour

	type daoMock struct {
		resp *dao.Generation
		err  error
	}

	testCases := []struct {
		name string

		request *core.GenerationCancelRequest

		daoMock *daoMock
		// getMock is the owner-scoped read of a generation the cancel could not stop.
		getMock    *daoMock
		checkMock  *daoMock
		attemptErr error
		// unanswered marks a generation whose only attempt is in flight, so no answer is read.
		unanswered bool

		expectStatus core.GenerationStatus
		expectErr    error
	}{
		{
			// Nothing was sent, so the data access settles it in the same statement.
			name: "Success/NeverStarted",

			request: &core.GenerationCancelRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock: &daoMock{resp: settledGeneration(dao.GenerationStatusCancelled)},

			expectStatus: core.GenerationStatusCancelled,
		},
		{
			// The start may be accepted at any moment with no id recorded yet. The next check stops it.
			name: "Success/StartInFlight",

			request:    &core.GenerationCancelRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock:    &daoMock{resp: withCancel(startingGeneration(time.Second))},
			unanswered: true,

			expectStatus: core.GenerationStatusPending,
		},
		{
			// A known call is stopped now, and settles with what it consumed before the stop.
			name: "Success/Running",

			request:   &core.GenerationCancelRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock:   &daoMock{resp: withCancel(runningGeneration(1))},
			checkMock: &daoMock{resp: settledGeneration(dao.GenerationStatusCancelled)},

			expectStatus: core.GenerationStatusCancelled,
		},
		{
			// Settled before the cancel arrived, or a cancel retried after its answer was lost: the
			// generation comes back as it stands, its attempts included.
			name: "Success/AlreadySettled",

			request: &core.GenerationCancelRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock: &daoMock{err: dao.ErrGenerationNotCancellable},
			getMock: &daoMock{resp: settledGeneration(dao.GenerationStatusSucceeded)},

			expectStatus: core.GenerationStatusSucceeded,
		},
		{
			// The read is owner-scoped, so somebody else's generation reports as absent.
			name: "Error/NotFound",

			request: &core.GenerationCancelRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock: &daoMock{err: dao.ErrGenerationNotCancellable},
			getMock: &daoMock{err: dao.ErrGenerationGetNotFound},

			expectErr: core.ErrGenerationNotFound,
		},
		{
			name: "Error/NoOwner",

			request: &core.GenerationCancelRequest{ID: testGenerationID},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/Internal",

			request: &core.GenerationCancelRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
		{
			name: "Error/Attempts",

			request:    &core.GenerationCancelRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock:    &daoMock{resp: settledGeneration(dao.GenerationStatusCancelled)},
			attemptErr: errFoo,

			expectErr: errFoo,
		},
		{
			name: "Error/Check",

			request:   &core.GenerationCancelRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock:   &daoMock{resp: withCancel(runningGeneration(1))},
			checkMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cancelDao := coremocks.NewMockGenerationCancelDao(t)
			getDao := coremocks.NewMockGenerationCancelDaoGet(t)
			check := coremocks.NewMockGenerationCancelServiceCheck(t)
			attemptDao := coremocks.NewMockGenerationCancelDaoAttemptList(t)

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

			if testCase.getMock != nil {
				getDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationGetRequest{ID: testCase.request.ID, OwnerID: testCase.request.OwnerID}).
					Return(testCase.getMock.resp, testCase.getMock.err)
			}

			if testCase.checkMock != nil {
				check.EXPECT().
					Exec(mock.Anything, &core.GenerationCheckRequest{Generation: testCase.daoMock.resp}).
					Return(testCase.checkMock.resp, testCase.checkMock.err)
			}

			if (testCase.expectStatus != "" || testCase.attemptErr != nil) && !testCase.unanswered {
				attemptDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationAttemptListRequest{GenerationID: testGenerationID}).
					Return(testAttemptRows(), testCase.attemptErr)
			}

			service := core.NewGenerationCancel(
				cancelDao, getDao, attemptDao, check, config.Generations{Retention: retention},
			)

			result, err := service.Exec(t.Context(), testCase.request)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, testCase.expectStatus, result.Status)

				if !testCase.unanswered {
					require.Equal(t, []*core.GenerationAttempt{{Number: 1, Response: testResponse}}, result.Attempts)
				}
			}

			cancelDao.AssertExpectations(t)
			getDao.AssertExpectations(t)
			check.AssertExpectations(t)
			attemptDao.AssertExpectations(t)
		})
	}
}
