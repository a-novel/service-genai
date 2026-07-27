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

func TestGenerationCancel(t *testing.T) {
	t.Parallel()

	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	generationID := uuid.MustParse("01999999-0000-7000-8000-000000000001")

	type daoMock struct {
		resp *dao.Generation
		err  error
	}

	testCases := []struct {
		name string

		request *core.GenerationCancelRequest

		daoMock *daoMock

		expectErr error
	}{
		{
			// Marking the request is all this does. The worker stops the provider call and settles,
			// recording what was spent before the stop.
			name: "Success",

			request: &core.GenerationCancelRequest{ID: generationID, OwnerID: owner},
			daoMock: &daoMock{resp: &dao.Generation{ID: generationID}},
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
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			cancelDao := coremocks.NewMockGenerationCancelDao(t)

			if testCase.daoMock != nil {
				cancelDao.EXPECT().
					Exec(mock.Anything, &dao.GenerationRequestCancelRequest{
						ID: testCase.request.ID, OwnerID: testCase.request.OwnerID,
					}).
					Return(testCase.daoMock.resp, testCase.daoMock.err)
			}

			result, err := core.NewGenerationCancel(cancelDao).Exec(t.Context(), testCase.request)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, testCase.daoMock.resp, result)
			}

			cancelDao.AssertExpectations(t)
		})
	}
}
