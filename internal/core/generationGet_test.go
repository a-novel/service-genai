package core_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
)

func TestGenerationGet(t *testing.T) {
	t.Parallel()

	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	generationID := uuid.MustParse("01999999-0000-7000-8000-000000000001")

	type daoMock struct {
		resp *dao.Generation
		err  error
	}

	testCases := []struct {
		name string

		request *core.GenerationGetRequest

		daoMock *daoMock

		expectErr error
	}{
		{
			name: "Success",

			request: &core.GenerationGetRequest{ID: generationID, OwnerID: owner},
			daoMock: &daoMock{resp: &dao.Generation{ID: generationID}},
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

			if testCase.daoMock != nil {
				getDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationGetRequest{
						ID: testCase.request.ID, OwnerID: testCase.request.OwnerID,
					}).
					Return(testCase.daoMock.resp, testCase.daoMock.err)
			}

			result, err := core.NewGenerationGet(getDao).Exec(t.Context(), testCase.request)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, testCase.daoMock.resp, result)
			}

			getDao.AssertExpectations(t)
		})
	}
}
