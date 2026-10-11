package dao_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"
	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestPgGenerationGetByCall(t *testing.T) {
	t.Parallel()

	generationID := uuid.MustParse("01999999-0000-7000-8000-000000000001")

	running := &dao.Generation{
		ID:             generationID,
		OwnerID:        uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		Purpose:        "studio.generation",
		Provider:       "openai",
		RequestKey:     []byte("key"),
		Payload:        json.RawMessage(`{"model": "a-model"}`),
		Status:         dao.GenerationStatusRunning,
		MaxAttempts:    1,
		ProviderCallID: lo.ToPtr("resp_1"),
	}

	settled := *running
	settled.Status = dao.GenerationStatusSucceeded
	settled.SettledAt = lo.ToPtr(time.Now())
	settled.ExpiresAt = lo.ToPtr(time.Now().Add(time.Hour))

	testCases := []struct {
		name string

		fixture *dao.Generation

		request *dao.GenerationGetByCallRequest

		expectFound bool
		expectErr   error
	}{
		{
			name: "Success",

			fixture: running,
			request: &dao.GenerationGetByCallRequest{Provider: "openai", CallID: "resp_1"},

			expectFound: true,
		},
		{
			// Call ids are the provider's own, so another provider's may collide.
			name: "Error/NotFound/OtherProvider",

			fixture: running,
			request: &dao.GenerationGetByCallRequest{Provider: "other", CallID: "resp_1"},

			expectErr: dao.ErrGenerationGetByCallNotFound,
		},
		{
			name: "Error/NotFound/NoSuchCall",

			fixture: running,
			request: &dao.GenerationGetByCallRequest{Provider: "openai", CallID: "resp_2"},

			expectErr: dao.ErrGenerationGetByCallNotFound,
		},
		{
			// A settled generation has nothing left to check.
			name: "Error/NotFound/Settled",

			fixture: &settled,
			request: &dao.GenerationGetByCallRequest{Provider: "openai", CallID: "resp_1"},

			expectErr: dao.ErrGenerationGetByCallNotFound,
		},
	}

	daoGenerationGetByCall := dao.NewPgGenerationGetByCall()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				db, err := postgres.GetContext(ctx)
				require.NoError(t, err)

				_, err = db.NewInsert().Model(testCase.fixture).Exec(ctx)
				require.NoError(t, err)

				result, err := daoGenerationGetByCall.Exec(ctx, testCase.request)
				require.ErrorIs(t, err, testCase.expectErr)

				if !testCase.expectFound {
					require.Nil(t, result)

					return
				}

				require.NotNil(t, result)
				require.Equal(t, generationID, result.ID)
				require.Equal(t, dao.GenerationStatusRunning, result.Status)
			})
		})
	}
}
