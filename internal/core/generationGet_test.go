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

func TestGenerationGet(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	settledAt := updatedAt.Add(time.Minute)
	expiresAt := settledAt.Add(time.Hour)
	generationError := "the provider failed the call"

	const checkInterval = 2 * time.Second

	type daoMock struct {
		resp *dao.Generation
		err  error
	}

	notDue := &daoMock{err: dao.ErrGenerationCheckNotDue}

	testCases := []struct {
		name string

		request *core.GenerationGetRequest

		electMock *daoMock
		daoMock   *daoMock
		checkMock *daoMock
		// expectAttempts lists the provider's answers: only a generation that can have one is read.
		expectAttempts bool
		attemptErr     error

		expect       *core.Generation
		expectStatus core.GenerationStatus
		expectErr    error
	}{
		{
			name: "Success",

			request:   &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			electMock: notDue,
			daoMock: &daoMock{resp: &dao.Generation{
				ID: testGenerationID, OwnerID: testOwner, Purpose: testPurpose, Provider: testProvider,
				Error:  &generationError,
				Status: dao.GenerationStatusFailed, Attempt: 2, MaxAttempts: 3,
				CreatedAt: createdAt, UpdatedAt: updatedAt, SettledAt: &settledAt,
				ExpiresAt: &expiresAt,
			}},
			expectAttempts: true,
			// A failure still returns the provider's answers, and with them what each attempt consumed.
			expect: &core.Generation{
				ID: testGenerationID, OwnerID: testOwner, Purpose: testPurpose, Provider: testProvider,
				Error:     &generationError,
				Status:    core.GenerationStatusFailed,
				Attempts:  []*core.GenerationAttempt{{Number: 1, Response: testResponse}},
				CreatedAt: createdAt, UpdatedAt: updatedAt, SettledAt: &settledAt,
				ExpiresAt: &expiresAt,
			},
		},
		{
			// Its only attempt is in flight, so it has no answer to read.
			name: "Success/CheckedRecently",

			request:   &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			electMock: notDue,
			daoMock:   &daoMock{resp: runningGeneration(1)},

			expectStatus: core.GenerationStatusRunning,
		},
		{
			// A stale generation is checked before it is returned: polls are what move it forward.
			name: "Success/Checked",

			request:        &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			electMock:      &daoMock{resp: runningGeneration(1)},
			checkMock:      &daoMock{resp: settledGeneration(dao.GenerationStatusSucceeded)},
			expectAttempts: true,

			expectStatus: core.GenerationStatusSucceeded,
		},
		{
			// The election returns the generation, so an elected poll of a running one reads nothing more.
			name: "Success/CheckedStillRunning",

			request:   &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			electMock: &daoMock{resp: runningGeneration(1)},
			checkMock: &daoMock{resp: runningGeneration(1)},

			expectStatus: core.GenerationStatusRunning,
		},
		{
			name: "Error/Attempts",

			request:        &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			electMock:      notDue,
			daoMock:        &daoMock{resp: settledGeneration(dao.GenerationStatusSucceeded)},
			expectAttempts: true,
			attemptErr:     errFoo,

			expectErr: errFoo,
		},
		{
			name: "Error/Elect",

			request:   &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			electMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
		{
			name: "Error/Check",

			request:   &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			electMock: &daoMock{resp: runningGeneration(1)},
			checkMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
		{
			// The data access reports not-found for another owner's generation too, so the
			// translation must not turn that into anything more specific.
			name: "Error/NotFound",

			request:   &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			electMock: notDue,
			daoMock:   &daoMock{err: dao.ErrGenerationGetNotFound},

			expectErr: core.ErrGenerationNotFound,
		},
		{
			name: "Error/NoOwner",

			request: &core.GenerationGetRequest{ID: testGenerationID},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/NoID",

			request: &core.GenerationGetRequest{OwnerID: testOwner},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/Internal",

			request:   &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			electMock: notDue,
			daoMock:   &daoMock{err: errFoo},

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			getDao := coremocks.NewMockGenerationGetDao(t)
			electDao := coremocks.NewMockGenerationGetDaoElectCheck(t)
			check := coremocks.NewMockGenerationGetServiceCheck(t)
			attemptDao := coremocks.NewMockGenerationGetDaoAttemptList(t)

			if testCase.electMock != nil {
				electDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationElectCheckRequest{
						ID:       testCase.request.ID,
						OwnerID:  testCase.request.OwnerID,
						Interval: checkInterval,
					}).
					Return(testCase.electMock.resp, testCase.electMock.err)
			}

			if testCase.daoMock != nil {
				getDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationGetRequest{
						ID: testCase.request.ID, OwnerID: testCase.request.OwnerID,
					}).
					Return(testCase.daoMock.resp, testCase.daoMock.err)
			}

			if testCase.checkMock != nil {
				check.EXPECT().
					Exec(mock.Anything, &core.GenerationCheckRequest{Generation: testCase.electMock.resp}).
					Return(testCase.checkMock.resp, testCase.checkMock.err)
			}

			if testCase.expectAttempts {
				attemptDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationAttemptListRequest{GenerationID: testCase.request.ID}).
					Return(testAttemptRows(), testCase.attemptErr)
			}

			service := core.NewGenerationGet(
				core.GenerationGetConfig{CheckInterval: checkInterval},
				getDao, electDao, attemptDao, check,
			)

			result, err := service.Exec(t.Context(), testCase.request)
			require.ErrorIs(t, err, testCase.expectErr)

			switch {
			case testCase.expectErr != nil:
				require.Nil(t, result)
			case testCase.expect != nil:
				require.Equal(t, testCase.expect, result)
			default:
				require.Equal(t, testCase.expectStatus, result.Status)
			}

			getDao.AssertExpectations(t)
			electDao.AssertExpectations(t)
			check.AssertExpectations(t)
			attemptDao.AssertExpectations(t)
		})
	}
}
