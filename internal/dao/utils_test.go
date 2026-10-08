package dao_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/dao"
)

const (
	testCallID    = "resp_1"
	testRetention = 7 * 24 * time.Hour
)

var testOwner = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// seedGeneration submits one pending generation.
func seedGeneration(ctx context.Context, t *testing.T, maxAttempts int16) *dao.Generation {
	t.Helper()

	result, err := dao.NewGenerationSubmit().Exec(ctx, &dao.GenerationSubmitRequest{
		ID:                 uuid.Must(uuid.NewV7()),
		OwnerID:            testOwner,
		Purpose:            "studio.generation",
		IdempotencyKey:     uuid.Must(uuid.NewV7()).String(),
		RequestFingerprint: []byte{0x01},
		Request:            json.RawMessage(`{"model": "a-model"}`),
		MaxAttempts:        maxAttempts,
	})
	if err != nil {
		panic(err)
	}

	return result.Generation
}

// startGeneration records start intent on a seeded generation, as a check about to send its call.
func startGeneration(ctx context.Context, t *testing.T, id uuid.UUID) *dao.Generation {
	t.Helper()

	generation, err := dao.NewGenerationBeginStart().Exec(ctx, &dao.GenerationBeginStartRequest{ID: id})
	if err != nil {
		panic(err)
	}

	return generation
}

// runGeneration starts a seeded generation and records its provider call.
func runGeneration(ctx context.Context, t *testing.T, id uuid.UUID) *dao.Generation {
	t.Helper()

	started := startGeneration(ctx, t, id)

	generation, err := dao.NewGenerationRecordProviderCall().Exec(ctx, &dao.GenerationRecordProviderCallRequest{
		ID: id, Attempt: started.Attempt, ProviderCallID: testCallID,
	})
	if err != nil {
		panic(err)
	}

	return generation
}

// setCheckedAt moves when a generation was last checked, relative to the database clock.
func setCheckedAt(ctx context.Context, t *testing.T, id uuid.UUID, offset time.Duration) {
	t.Helper()

	execute(ctx, t, "UPDATE generations SET checked_at = clock_timestamp() + make_interval(secs => ?1) WHERE id = ?0",
		id, offset.Seconds())
}

// setRunAt moves when a generation may next start, relative to the database clock.
func setRunAt(ctx context.Context, t *testing.T, id uuid.UUID, offset time.Duration) {
	t.Helper()

	execute(ctx, t, "UPDATE generations SET run_at = clock_timestamp() + make_interval(secs => ?1) WHERE id = ?0",
		id, offset.Seconds())
}

func execute(ctx context.Context, t *testing.T, query string, args ...any) {
	t.Helper()

	db, err := postgres.GetContext(ctx)
	if err != nil {
		panic(err)
	}

	_, err = db.NewRaw(query, args...).Exec(ctx)
	if err != nil {
		panic(err)
	}
}

// settleGeneration fails a generation in whatever state it was read in.
func settleGeneration(ctx context.Context, t *testing.T, generation *dao.Generation) *dao.Generation {
	t.Helper()

	reason := "the provider call failed"
	kind := "failed"

	settled, err := dao.NewGenerationSettle().Exec(ctx, &dao.GenerationSettleRequest{
		ID:             generation.ID,
		Attempt:        generation.Attempt,
		ProviderCallID: generation.ProviderCallID,
		Status:         dao.GenerationStatusFailed,
		Failure:        &kind,
		Error:          &reason,
		Retention:      testRetention,
	})
	if err != nil {
		panic(err)
	}

	return settled
}
