package dao_test

import (
	"context"
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

// seedGeneration submits one pending generation, which carries its start intent as a submit records
// it.
func seedGeneration(ctx context.Context, t *testing.T) *dao.Generation {
	t.Helper()

	submitRequest := &dao.GenerationSubmitRequest{
		ID:         uuid.Must(uuid.NewV7()),
		OwnerID:    testOwner,
		Purpose:    "studio.generation",
		Provider:   "openai",
		RequestKey: []byte(uuid.Must(uuid.NewV7()).String()),
	}

	result, err := dao.NewPgGenerationSubmit().Exec(ctx, submitRequest)
	if err != nil {
		panic(err)
	}

	return result.Generation
}

// seedUnstarted seeds a pending generation as an earlier release recorded it between two starts: no
// start intent.
func seedUnstarted(ctx context.Context, t *testing.T) *dao.Generation {
	t.Helper()

	generation := seedGeneration(ctx, t)

	execute(ctx, t, `UPDATE generations
		SET attempt = 0, start_requested_at = NULL, first_start_requested_at = NULL WHERE id = ?0`, generation.ID)

	generation.Attempt = 0
	generation.StartRequestedAt = nil
	generation.FirstStartRequestedAt = nil

	return generation
}

// runGeneration records the provider call of a generation's start intent, on its first attempt.
func runGeneration(ctx context.Context, t *testing.T, id uuid.UUID) *dao.Generation {
	t.Helper()

	generation, err := dao.NewPgGenerationRecordProviderCall().Exec(ctx, &dao.GenerationRecordProviderCallRequest{
		ID: id, Attempt: 1, ProviderCallID: testCallID,
	})
	if err != nil {
		panic(err)
	}

	return generation
}

// execute runs a statement on the test's database handle.
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

	settleRequest := &dao.GenerationSettleRequest{
		ID:               generation.ID,
		Attempt:          generation.Attempt,
		StartRequestedAt: generation.StartRequestedAt,
		ProviderCallID:   generation.ProviderCallID,
		Status:           dao.GenerationStatusFailed,
		Error:            &reason,
		Retention:        testRetention,
	}

	settled, err := dao.NewPgGenerationSettle().Exec(ctx, settleRequest)
	if err != nil {
		panic(err)
	}

	return settled
}

// seedUsage records one answered attempt's usage for the owner, recorded the given offset from now.
func seedUsage(
	ctx context.Context, t *testing.T, owner uuid.UUID, purpose string, input, output int64, offset time.Duration,
) {
	t.Helper()

	execute(ctx, t, `INSERT INTO generation_usage
		(generation_id, attempt, owner_id, purpose, provider, model, class, input_tokens, output_tokens,
			started_at, created_at)
		VALUES (?0, 1, ?1, ?2, 'openai', 'a-model', 'high', ?3, ?4,
			clock_timestamp() + make_interval(secs => ?5), clock_timestamp() + make_interval(secs => ?5))`,
		uuid.Must(uuid.NewV7()), owner, purpose, input, output, offset.Seconds())
}
