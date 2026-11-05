package core_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/transaction/transactiontest"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
	connectorsmocks "github.com/a-novel/service-genai/internal/lib/connectors/mocks"
)

func TestGenerationGet(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	settledAt := updatedAt.Add(time.Minute)
	expiresAt := settledAt.Add(time.Hour)
	generationError := "the provider failed the call"

	type daoMock struct {
		resp *dao.Generation
		err  error
	}

	testCases := []struct {
		name string

		request *core.GenerationGetRequest

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
			// A settled generation reads from the database alone.
			name: "Success",

			request: &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock: &daoMock{resp: &dao.Generation{
				ID: testGenerationID, OwnerID: testOwner, Purpose: testPurpose, Provider: testProvider,
				Error:   &generationError,
				Failure: new(dao.GenerationFailureProviderFailed),
				Status:  dao.GenerationStatusFailed, Attempt: 1,
				CreatedAt: createdAt, UpdatedAt: updatedAt, SettledAt: &settledAt,
				ExpiresAt: &expiresAt,
			}},
			expectAttempts: true,
			// A failure still returns the provider's answer, and with it what the call consumed.
			expect: &core.Generation{
				ID: testGenerationID, OwnerID: testOwner, Purpose: testPurpose, Provider: testProvider,
				Error:     &generationError,
				Failure:   new(core.GenerationFailureProviderFailed),
				Status:    core.GenerationStatusFailed,
				Attempts:  []*core.GenerationAttempt{{Number: 1, Response: testResponse}},
				CreatedAt: createdAt, UpdatedAt: updatedAt, SettledAt: &settledAt,
				ExpiresAt: &expiresAt,
			},
		},
		{
			// A poll made after the provider finished returns the settled generation.
			name: "Success/Checked",

			request:        &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock:        &daoMock{resp: runningGeneration(1)},
			checkMock:      &daoMock{resp: settledGeneration(dao.GenerationStatusSucceeded)},
			expectAttempts: true,

			expectStatus: core.GenerationStatusSucceeded,
		},
		{
			// Its only attempt is in flight, so it has no answer to read.
			name: "Success/CheckedStillRunning",

			request:   &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock:   &daoMock{resp: runningGeneration(1)},
			checkMock: &daoMock{resp: runningGeneration(1)},

			expectStatus: core.GenerationStatusRunning,
		},
		{
			// The submit is still sending the call: the check leaves it to the submit.
			name: "Success/StartInFlight",

			request:   &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock:   &daoMock{resp: startingGeneration(time.Second)},
			checkMock: &daoMock{resp: startingGeneration(time.Second)},

			expectStatus: core.GenerationStatusPending,
		},
		{
			// A failed check is redone by the next poll: this one still answers with the generation read.
			name: "Success/CheckFails",

			request:   &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock:   &daoMock{resp: runningGeneration(1)},
			checkMock: &daoMock{err: errFoo},

			expectStatus: core.GenerationStatusRunning,
		},
		{
			name: "Error/Attempts",

			request:        &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock:        &daoMock{resp: settledGeneration(dao.GenerationStatusSucceeded)},
			expectAttempts: true,
			attemptErr:     errFoo,

			expectErr: errFoo,
		},
		{
			// The data access reports not-found for another owner's generation too, so the
			// translation must not turn that into anything more specific.
			name: "Error/NotFound",

			request: &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock: &daoMock{err: dao.ErrGenerationGetNotFound},

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

			request: &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner},
			daoMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			getDao := coremocks.NewMockGenerationGetDao(t)
			check := coremocks.NewMockGenerationGetServiceCheck(t)
			attemptDao := coremocks.NewMockGenerationGetDaoAttemptList(t)

			if testCase.daoMock != nil {
				getDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationGetRequest{
						ID: testCase.request.ID, OwnerID: testCase.request.OwnerID,
					}).
					Return(testCase.daoMock.resp, testCase.daoMock.err)
			}

			if testCase.checkMock != nil {
				check.EXPECT().
					Exec(mock.Anything, &core.GenerationCheckRequest{Generation: testCase.daoMock.resp}).
					Return(testCase.checkMock.resp, testCase.checkMock.err)
			}

			if testCase.expectAttempts {
				attemptDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationAttemptListRequest{GenerationID: testCase.request.ID}).
					Return(testAttemptRows(), testCase.attemptErr)
			}

			service := core.NewGenerationGet(getDao, attemptDao, check)

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

			mock.AssertExpectationsForObjects(t, getDao, check, attemptDao)
		})
	}

	// After its insert, a run writes its generation twice whatever the polls: the call id once the
	// provider accepts the call, and the settle. The case follows one run from its submit through many
	// polls, so it runs outside the table.
	t.Run("Success/RunWritesTwice", func(t *testing.T) {
		t.Parallel()

		const pollsWhileRunning = 5

		connector := connectorsmocks.NewMockAsync(t)
		connector.EXPECT().Start(mock.Anything, mock.Anything).Return(providerCall(connectors.CallRunning), nil).Once()
		connector.EXPECT().Get(mock.Anything, testCallID).Return(providerCall(connectors.CallRunning), nil).
			Times(pollsWhileRunning)
		connector.EXPECT().Get(mock.Anything, testCallID).Return(providerCall(connectors.CallSucceeded), nil).Once()

		// The two writes of the run, each once.
		record := coremocks.NewMockGenerationCheckDaoRecordProviderCall(t)
		record.EXPECT().Exec(mock.Anything, mock.Anything).Return(runningGeneration(1), nil).Once()

		settle := coremocks.NewMockGenerationCheckDaoSettle(t)
		settle.EXPECT().Exec(mock.Anything, mock.Anything).
			Return(settledGeneration(dao.GenerationStatusSucceeded), nil).Once()

		// The settle's transaction records the answer and its usage beside the generation.
		attemptInsert := coremocks.NewMockGenerationCheckDaoAttemptInsert(t)
		attemptInsert.EXPECT().Exec(mock.Anything, mock.Anything).Return(&dao.GenerationAttempt{}, nil).Once()

		usage := coremocks.NewMockGenerationCheckDaoUsageInsert(t)
		usage.EXPECT().Exec(mock.Anything, mock.Anything).Return(&dao.GenerationUsage{}, nil).Once()

		check := core.NewGenerationCheck(
			record, settle, attemptInsert, usage, coremocks.NewMockGenerationCheckDaoGet(t),
			connectors.Registry{testProvider: connector},
			transactiontest.NewTransactor(),
			coremocks.NewMockGenerationCheckLogger(t),
			config.Generations{Retention: time.Hour},
		)

		submitDao := coremocks.NewMockGenerationSubmitDao(t)
		submitDao.EXPECT().Exec(mock.Anything, mock.Anything).
			Return(&dao.GenerationSubmitResult{Generation: startingGeneration(0), Created: true}, nil).Once()

		submit := core.NewGenerationSubmit(
			submitDao,
			coremocks.NewMockGenerationSubmitDaoAttemptList(t),
			coremocks.NewMockGenerationSubmitDaoUsageSum(t),
			coremocks.NewMockGenerationSubmitDaoCountLive(t),
			check,
			testConnectors(t),
			transactiontest.NewTransactor(),
			testCaps,
			config.Cap{},
		)

		submitted, err := submit.Exec(t.Context(), submitRequest(func(request *core.GenerationSubmitRequest) {
			request.Caps = &core.GenerationCaps{}
		}))
		require.NoError(t, err)
		require.Equal(t, core.GenerationStatusRunning, submitted.Generation.Status)

		// Every poll reads the row; the ones after the provider finished find it settled.
		getDao := coremocks.NewMockGenerationGetDao(t)
		getDao.EXPECT().Exec(mock.Anything, mock.Anything).Return(runningGeneration(1), nil).Times(pollsWhileRunning + 1)
		getDao.EXPECT().Exec(mock.Anything, mock.Anything).Return(settledGeneration(dao.GenerationStatusSucceeded), nil)

		attemptList := coremocks.NewMockGenerationGetDaoAttemptList(t)
		attemptList.EXPECT().Exec(mock.Anything, mock.Anything).Return(testAttemptRows(), nil)

		get := core.NewGenerationGet(getDao, attemptList, check)

		statuses := make([]core.GenerationStatus, 0, pollsWhileRunning+3)

		for range pollsWhileRunning + 3 {
			polled, err := get.Exec(t.Context(), &core.GenerationGetRequest{ID: testGenerationID, OwnerID: testOwner})
			require.NoError(t, err)

			statuses = append(statuses, polled.Status)
		}

		require.Equal(t, core.GenerationStatusRunning, statuses[pollsWhileRunning-1])
		require.Equal(t, core.GenerationStatusSucceeded, statuses[pollsWhileRunning])
		require.Equal(t, core.GenerationStatusSucceeded, statuses[len(statuses)-1])

		mock.AssertExpectationsForObjects(t, connector, record, settle, attemptInsert, usage, getDao, attemptList)
	})
}
