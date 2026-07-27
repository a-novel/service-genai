package dao_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationGet(t *testing.T) {
	t.Parallel()

	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	otherOwner := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	generationID := uuid.MustParse("01999999-0000-7000-8000-000000000001")

	fixture := func(id, ownerID uuid.UUID) *dao.Generation {
		return &dao.Generation{
			ID:                 id,
			OwnerID:            ownerID,
			Purpose:            "studio.generation",
			IdempotencyKey:     "key-" + id.String(),
			RequestFingerprint: []byte{0x01},
			Request:            json.RawMessage(`{"instructions": "write"}`),
			Status:             dao.GenerationStatusPending,
			MaxAttempts:        1,
		}
	}

	testCases := []struct {
		name string

		fixtures []*dao.Generation

		request *dao.GenerationGetRequest

		expectFound bool
		expectErr   error
	}{
		{
			name: "Success",

			fixtures: []*dao.Generation{fixture(generationID, owner)},

			request: &dao.GenerationGetRequest{ID: generationID, OwnerID: owner},

			expectFound: true,
		},
		{
			// The ownership predicate is the point: another owner's generation must be
			// indistinguishable from one that does not exist, or the identifier becomes probeable.
			name: "Error/NotFound/OtherOwner",

			fixtures: []*dao.Generation{fixture(generationID, otherOwner)},

			request: &dao.GenerationGetRequest{ID: generationID, OwnerID: owner},

			expectErr: dao.ErrGenerationGetNotFound,
		},
		{
			name: "Error/NotFound/NoSuchGeneration",

			request: &dao.GenerationGetRequest{ID: generationID, OwnerID: owner},

			expectErr: dao.ErrGenerationGetNotFound,
		},
	}

	daoGenerationGet := dao.NewGenerationGet()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				db, err := postgres.GetContext(ctx)
				require.NoError(t, err)

				if len(testCase.fixtures) > 0 {
					_, err = db.NewInsert().Model(&testCase.fixtures).Exec(ctx)
					require.NoError(t, err)
				}

				result, err := daoGenerationGet.Exec(ctx, testCase.request)
				require.ErrorIs(t, err, testCase.expectErr)

				if !testCase.expectFound {
					require.Nil(t, result)

					return
				}

				require.NotNil(t, result)
				require.Equal(t, testCase.request.ID, result.ID)
				require.Equal(t, testCase.request.OwnerID, result.OwnerID)
				require.Equal(t, dao.GenerationStatusPending, result.Status)
				require.JSONEq(t, `{"instructions": "write"}`, string(result.Request))
			})
		})
	}
}
