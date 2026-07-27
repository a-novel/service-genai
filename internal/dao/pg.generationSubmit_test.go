package dao_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestGenerationSubmit(t *testing.T) {
	t.Parallel()

	owner := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	otherOwner := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	submitRequest := func(id uuid.UUID) *dao.GenerationSubmitRequest {
		return &dao.GenerationSubmitRequest{
			ID:                 id,
			OwnerID:            owner,
			Purpose:            "studio.generation",
			Profile:            "draft",
			IdempotencyKey:     "the-key",
			RequestFingerprint: []byte{0x01},
			Request:            json.RawMessage(`{"instructions": "write"}`),
			MaxAttempts:        1,
		}
	}

	t.Run("Success/Created", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			id := uuid.Must(uuid.NewV7())

			result, err := dao.NewGenerationSubmit().Exec(ctx, submitRequest(id))
			require.NoError(t, err)
			require.True(t, result.Created)
			require.Equal(t, id, result.Generation.ID)
			require.Equal(t, dao.GenerationStatusPending, result.Generation.Status)
			require.Zero(t, result.Generation.Attempt)

			// The database owns these, and a caller must never have to supply them.
			require.False(t, result.Generation.CreatedAt.IsZero())
			require.False(t, result.Generation.RunAt.IsZero())
			require.Nil(t, result.Generation.SettledAt)
			require.Nil(t, result.Generation.LeaseExpiresAt)
		})
	})

	t.Run("Success/Replayed", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			daoSubmit := dao.NewGenerationSubmit()

			first := uuid.Must(uuid.NewV7())
			created, err := daoSubmit.Exec(ctx, submitRequest(first))
			require.NoError(t, err)
			require.True(t, created.Created)

			// A retry mints a fresh identifier, exactly as a caller that never saw the first
			// answer would. The stored generation must win, and the caller must be told it did.
			replayed, err := daoSubmit.Exec(ctx, submitRequest(uuid.Must(uuid.NewV7())))
			require.NoError(t, err)
			require.False(t, replayed.Created)
			require.Equal(t, first, replayed.Generation.ID)
		})
	})

	t.Run("Success/SameKeyDifferentPurpose", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			daoSubmit := dao.NewGenerationSubmit()

			_, err := daoSubmit.Exec(ctx, submitRequest(uuid.Must(uuid.NewV7())))
			require.NoError(t, err)

			// Purpose is part of the key: without it, one owner's keys collide across features and
			// a submission from another feature is mistaken for a replay.
			other := submitRequest(uuid.Must(uuid.NewV7()))
			other.Purpose = "discovery.recommendation"

			result, err := daoSubmit.Exec(ctx, other)
			require.NoError(t, err)
			require.True(t, result.Created)
		})
	})

	t.Run("Success/SameKeyDifferentOwner", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			daoSubmit := dao.NewGenerationSubmit()

			_, err := daoSubmit.Exec(ctx, submitRequest(uuid.Must(uuid.NewV7())))
			require.NoError(t, err)

			other := submitRequest(uuid.Must(uuid.NewV7()))
			other.OwnerID = otherOwner

			result, err := daoSubmit.Exec(ctx, other)
			require.NoError(t, err)
			require.True(t, result.Created)
		})
	})

	t.Run("Error/Conflict", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			daoSubmit := dao.NewGenerationSubmit()

			_, err := daoSubmit.Exec(ctx, submitRequest(uuid.Must(uuid.NewV7())))
			require.NoError(t, err)

			// Same key, different request. This is a caller bug, not a replay, and answering with
			// the earlier generation would answer a question that was never asked.
			conflicting := submitRequest(uuid.Must(uuid.NewV7()))
			conflicting.RequestFingerprint = []byte{0x02}
			conflicting.Request = json.RawMessage(`{"instructions": "something else"}`)

			result, err := daoSubmit.Exec(ctx, conflicting)
			require.ErrorIs(t, err, dao.ErrGenerationSubmitConflict)
			require.Nil(t, result)
		})
	})

	t.Run("Success/ConcurrentSubmitsResolveToOneWinner", func(t *testing.T) {
		t.Parallel()

		postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			const concurrency = 8

			daoSubmit := dao.NewGenerationSubmit()

			var (
				wg      sync.WaitGroup
				mu      sync.Mutex
				results []*dao.GenerationSubmitResult
			)

			// The whole point of the operation is that a race cannot produce two priced calls, so
			// the race is what the test exercises: exactly one submission may report Created, and
			// every one of them must come back pointing at that same generation.
			for range concurrency {
				wg.Add(1)

				go func() {
					defer wg.Done()

					result, err := daoSubmit.Exec(ctx, submitRequest(uuid.Must(uuid.NewV7())))
					if err != nil {
						return
					}

					mu.Lock()
					defer mu.Unlock()

					results = append(results, result)
				}()
			}

			wg.Wait()

			require.Len(t, results, concurrency)

			var (
				createdCount int
				winner       uuid.UUID
			)

			for _, result := range results {
				if result.Created {
					createdCount++
					winner = result.Generation.ID
				}
			}

			require.Equal(t, 1, createdCount)

			for _, result := range results {
				require.Equal(t, winner, result.Generation.ID)
			}
		})
	})
}
