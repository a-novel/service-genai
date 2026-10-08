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
	"github.com/a-novel/service-genai/internal/lib"
)

func TestGenerationGet(t *testing.T) {
	t.Parallel()

	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	generationID := uuid.MustParse("01999999-0000-7000-8000-000000000001")
	createdAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	settledAt := updatedAt.Add(time.Minute)
	expiresAt := settledAt.Add(time.Hour)
	generationError := "the provider rejected the request"
	failureKind := "invalid_request"
	coreFailure := lib.FailureInvalidRequest
	effort := "medium"

	const checkInterval = 2 * time.Second

	type daoMock struct {
		resp *dao.Generation
		err  error
	}

	testCases := []struct {
		name string

		request *core.GenerationGetRequest

		daoMock   *daoMock
		electMock *daoMock
		checkMock *daoMock
		usageErr  error

		expect       *core.Generation
		expectStatus core.GenerationStatus
		expectErr    error
	}{
		{
			name: "Success",

			request: &core.GenerationGetRequest{ID: generationID, OwnerID: owner},
			daoMock: &daoMock{resp: &dao.Generation{
				ID: generationID, OwnerID: owner, Purpose: "studio.generation",
				Failure: &failureKind, Error: &generationError,
				Status: dao.GenerationStatusFailed, Attempt: 2, MaxAttempts: 3,
				CreatedAt: createdAt, UpdatedAt: updatedAt, SettledAt: &settledAt,
				ExpiresAt: &expiresAt,
			}},
			// A failure still reports what it consumed.
			expect: &core.Generation{
				ID: generationID, OwnerID: owner, Purpose: "studio.generation",
				Failure: &coreFailure, Error: &generationError,
				Status: core.GenerationStatusFailed,
				Usage: []*core.GenerationUsage{{
					Attempt: 1, Provider: "openai", Model: "a-model-snapshot", ReasoningEffort: &effort,
					InputTokens: 1000, CachedInputTokens: 200, CacheWriteTokens: 300, OutputTokens: 500,
				}},
				CreatedAt: createdAt, UpdatedAt: updatedAt, SettledAt: &settledAt,
				ExpiresAt: &expiresAt,
			},
		},
		{
			name: "Success/CheckedRecently",

			request:   &core.GenerationGetRequest{ID: generationID, OwnerID: owner},
			daoMock:   &daoMock{resp: runningGeneration(1)},
			electMock: &daoMock{err: dao.ErrGenerationCheckNotDue},

			expectStatus: core.GenerationStatusRunning,
		},
		{
			// A stale generation is checked before it is returned: polls are what move it forward.
			name: "Success/Checked",

			request:   &core.GenerationGetRequest{ID: generationID, OwnerID: owner},
			daoMock:   &daoMock{resp: runningGeneration(1)},
			electMock: &daoMock{resp: runningGeneration(1)},
			checkMock: &daoMock{resp: settledGeneration(dao.GenerationStatusSucceeded)},

			expectStatus: core.GenerationStatusSucceeded,
		},
		{
			name: "Error/Usage",

			request:  &core.GenerationGetRequest{ID: generationID, OwnerID: owner},
			daoMock:  &daoMock{resp: settledGeneration(dao.GenerationStatusSucceeded)},
			usageErr: errFoo,

			expectErr: errFoo,
		},
		{
			name: "Error/Elect",

			request:   &core.GenerationGetRequest{ID: generationID, OwnerID: owner},
			daoMock:   &daoMock{resp: runningGeneration(1)},
			electMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
		{
			name: "Error/Check",

			request:   &core.GenerationGetRequest{ID: generationID, OwnerID: owner},
			daoMock:   &daoMock{resp: runningGeneration(1)},
			electMock: &daoMock{resp: runningGeneration(1)},
			checkMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
		{
			// The data access reports not-found for another owner's generation too, so the
			// translation must not turn that into anything more specific.
			name: "Error/NotFound",

			request: &core.GenerationGetRequest{ID: generationID, OwnerID: owner},
			daoMock: &daoMock{err: dao.ErrGenerationGetNotFound},

			expectErr: core.ErrGenerationNotFound,
		},
		{
			name: "Error/NoOwner",

			request: &core.GenerationGetRequest{ID: generationID},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/NoID",

			request: &core.GenerationGetRequest{OwnerID: owner},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/Internal",

			request: &core.GenerationGetRequest{ID: generationID, OwnerID: owner},
			daoMock: &daoMock{err: errFoo},

			expectErr: errFoo,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			getDao := coremocks.NewMockGenerationGetDao(t)
			electDao := coremocks.NewMockGenerationGetElectCheckDao(t)
			check := coremocks.NewMockGenerationGetServiceCheck(t)
			usageDao := coremocks.NewMockGenerationGetUsageListDao(t)

			if testCase.expect != nil || testCase.expectStatus != "" || testCase.usageErr != nil {
				usageDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationUsageListRequest{GenerationID: testCase.request.ID}).
					Return(testUsageRows(), testCase.usageErr)
			}

			if testCase.daoMock != nil {
				getDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationGetRequest{
						ID: testCase.request.ID, OwnerID: testCase.request.OwnerID,
					}).
					Return(testCase.daoMock.resp, testCase.daoMock.err)
			}

			if testCase.electMock != nil {
				electDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationElectCheckRequest{
						ID:            testCase.request.ID,
						OwnerID:       testCase.request.OwnerID,
						Interval:      checkInterval,
						ProviderEpoch: testEpoch,
					}).
					Return(testCase.electMock.resp, testCase.electMock.err)
			}

			if testCase.checkMock != nil {
				check.EXPECT().
					Exec(mock.Anything, &core.GenerationCheckRequest{Generation: testCase.electMock.resp}).
					Return(testCase.checkMock.resp, testCase.checkMock.err)
			}

			service, err := core.NewGenerationGet(
				core.GenerationGetConfig{CheckInterval: checkInterval, ProviderEpoch: testEpoch},
				getDao, electDao, usageDao, check,
			)
			require.NoError(t, err)

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
			usageDao.AssertExpectations(t)
		})
	}
}
