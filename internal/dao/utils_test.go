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
	testWorker    = "worker-1"
	testLease     = time.Minute
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

// claimGenerations takes whatever is pending, for the default test worker.
func claimGenerations(ctx context.Context, t *testing.T) []*dao.Generation {
	t.Helper()

	claimed, err := dao.NewGenerationClaim().Exec(ctx, &dao.GenerationClaimRequest{
		WorkerID: testWorker, Limit: 10, Lease: testLease,
	})
	if err != nil {
		panic(err)
	}

	return claimed
}

// expireLease pushes a claim's lease the given age into the past, standing in for a worker that
// died that long ago.
func expireLease(ctx context.Context, t *testing.T, id uuid.UUID, age time.Duration) {
	t.Helper()

	db, err := postgres.GetContext(ctx)
	if err != nil {
		panic(err)
	}

	_, err = db.NewRaw(
		"UPDATE generations SET lease_expires_at = clock_timestamp() - make_interval(secs => ?1) WHERE id = ?0",
		id, age.Seconds(),
	).Exec(ctx)
	if err != nil {
		panic(err)
	}
}
