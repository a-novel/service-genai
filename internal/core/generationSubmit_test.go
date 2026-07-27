package core_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/core"
	coremocks "github.com/a-novel/service-genai/internal/core/mocks"
	"github.com/a-novel/service-genai/internal/dao"
)

func TestGenerationSubmit(t *testing.T) {
	t.Parallel()

	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	type daoMock struct {
		resp *dao.GenerationSubmitResult
		err  error
	}

	testCases := []struct {
		name string

		request *core.GenerationSubmitRequest

		daoMock *daoMock

		// expectMaxAttempts is what the data access must be asked for, after the default is applied.
		expectMaxAttempts int16
		expectCreated     bool
		expectErr         error
	}{
		{
			name: "Success",

			request: &core.GenerationSubmitRequest{
				OwnerID: owner, Purpose: "studio.generation", IdempotencyKey: "key",
				Request: json.RawMessage(`{"model": "a-model"}`), MaxAttempts: 3,
			},
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{
				Generation: &dao.Generation{}, Created: true,
			}},

			expectMaxAttempts: 3,
			expectCreated:     true,
		},
		{
			// One attempt is the right floor for a priced call, so an unset count is not unlimited.
			name: "Success/DefaultsToOneAttempt",

			request: &core.GenerationSubmitRequest{
				OwnerID: owner, Purpose: "studio.generation", IdempotencyKey: "key",
				Request: json.RawMessage(`{"model": "a-model"}`),
			},
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{
				Generation: &dao.Generation{}, Created: true,
			}},

			expectMaxAttempts: 1,
			expectCreated:     true,
		},
		{
			name: "Success/Replayed",

			request: &core.GenerationSubmitRequest{
				OwnerID: owner, Purpose: "studio.generation", IdempotencyKey: "key",
				Request: json.RawMessage(`{"model": "a-model"}`),
			},
			daoMock: &daoMock{resp: &dao.GenerationSubmitResult{
				Generation: &dao.Generation{}, Created: false,
			}},

			expectMaxAttempts: 1,
		},
		{
			name: "Error/NoOwner",

			request: &core.GenerationSubmitRequest{
				Purpose: "studio.generation", IdempotencyKey: "key",
				Request: json.RawMessage(`{"model": "a-model"}`),
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/NoPurpose",

			request: &core.GenerationSubmitRequest{
				OwnerID: owner, IdempotencyKey: "key",
				Request: json.RawMessage(`{"model": "a-model"}`),
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			// An unkeyed submission of a priced call is a bug the API refuses, not a default it
			// tolerates.
			name: "Error/NoIdempotencyKey",

			request: &core.GenerationSubmitRequest{
				OwnerID: owner, Purpose: "studio.generation",
				Request: json.RawMessage(`{"model": "a-model"}`),
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/BlankIdempotencyKey",

			request: &core.GenerationSubmitRequest{
				OwnerID: owner, Purpose: "studio.generation", IdempotencyKey: "   ",
				Request: json.RawMessage(`{"model": "a-model"}`),
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/TooManyAttempts",

			request: &core.GenerationSubmitRequest{
				OwnerID: owner, Purpose: "studio.generation", IdempotencyKey: "key",
				Request: json.RawMessage(`{"model": "a-model"}`), MaxAttempts: 11,
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			// Refused here rather than at the transport, so the caller gets an error it can act on.
			name: "Error/RequestTooLarge",

			request: &core.GenerationSubmitRequest{
				OwnerID: owner, Purpose: "studio.generation", IdempotencyKey: "key",
				Request: json.RawMessage(`{"a": "` + strings.Repeat("x", core.RequestSizeCeiling) + `"}`),
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			// The adapter merges its two owned fields into the payload, and there is nowhere to
			// merge them into anything but an object.
			name: "Error/RequestIsNotAnObject",

			request: &core.GenerationSubmitRequest{
				OwnerID: owner, Purpose: "studio.generation", IdempotencyKey: "key",
				Request: json.RawMessage(`"not an object"`),
			},

			expectErr: core.ErrInvalidRequest,
		},
		{
			name: "Error/IdempotencyConflict",

			request: &core.GenerationSubmitRequest{
				OwnerID: owner, Purpose: "studio.generation", IdempotencyKey: "key",
				Request: json.RawMessage(`{"model": "a-model"}`),
			},
			daoMock: &daoMock{err: dao.ErrGenerationSubmitConflict},

			expectMaxAttempts: 1,
			expectErr:         core.ErrIdempotencyConflict,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			submitDao := coremocks.NewMockGenerationSubmitDao(t)

			if testCase.daoMock != nil {
				submitDao.EXPECT().
					Exec(mock.Anything, mock.MatchedBy(func(request *dao.GenerationSubmitRequest) bool {
						// The digest is what tells a replay from a reused key, so it must be
						// derived rather than left empty.
						return request.MaxAttempts == testCase.expectMaxAttempts &&
							len(request.RequestFingerprint) == 32 &&
							request.ID != uuid.Nil
					})).
					Return(testCase.daoMock.resp, testCase.daoMock.err)
			}

			result, err := core.NewGenerationSubmit(submitDao).Exec(t.Context(), testCase.request)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, result)
			} else {
				require.Equal(t, testCase.expectCreated, result.Created)
			}

			submitDao.AssertExpectations(t)
		})
	}
}
