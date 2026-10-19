package dao_test

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

// submission is one call in a case's sequence.
type submission struct {
	// key overrides the base request key.
	key []byte
	// settleBefore moves the previous submission's generation to this status first.
	settleBefore dao.GenerationStatus
	// streamed submits the generation for streaming.
	streamed bool

	expectCreated bool
	// expectReplayOf is the 1-based index of an earlier submission whose generation this one must
	// return. Zero means the assertion does not apply.
	expectReplayOf int
}

func (sub submission) build() *dao.GenerationSubmitRequest {
	base := &dao.GenerationSubmitRequest{
		ID:          uuid.Must(uuid.NewV7()),
		OwnerID:     testOwner,
		Purpose:     "studio.generation",
		Provider:    "openai",
		RequestKey:  []byte("the-request-key"),
		MaxAttempts: 1,
		Traceparent: new("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"),
		Streamed:    sub.streamed,
	}

	if sub.key != nil {
		base.RequestKey = sub.key
	}

	return base
}

// settleTo moves a pending generation to the given terminal status the way checks would.
func settleTo(ctx context.Context, t *testing.T, generation *dao.Generation, status dao.GenerationStatus) {
	t.Helper()

	switch status {
	case dao.GenerationStatusFailed:
		settleGeneration(ctx, t, generation)
	case dao.GenerationStatusCancelled:
		_, err := dao.NewPgGenerationRequestCancel().Exec(ctx, &dao.GenerationRequestCancelRequest{
			ID: generation.ID, OwnerID: generation.OwnerID, Error: "generation cancelled", Retention: testRetention,
		})
		if err != nil {
			panic(err)
		}
	case dao.GenerationStatusSucceeded:
		running := runGeneration(ctx, t, generation.ID)

		_, err := dao.NewPgGenerationSettle().Exec(ctx, &dao.GenerationSettleRequest{
			ID: running.ID, Attempt: running.Attempt, StartRequestedAt: running.StartRequestedAt,
			ProviderCallID: running.ProviderCallID,
			Status:         dao.GenerationStatusSucceeded,
			Retention:      testRetention,
		})
		if err != nil {
			panic(err)
		}
	case dao.GenerationStatusRunning:
		runGeneration(ctx, t, generation.ID)
	case dao.GenerationStatusPending:
	}
}

func TestPgGenerationSubmit(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// Each case is a sequence of submissions against one database, because what submit
		// guarantees is only observable across more than one call.
		submissions []submission
	}{
		{
			name: "Success/Created",

			submissions: []submission{{expectCreated: true}},
		},
		{
			name: "Success/CreatedStreamed",

			submissions: []submission{{streamed: true, expectCreated: true}},
		},
		{
			// A resend mints a fresh identifier, exactly as a caller that never saw the first answer
			// would. The stored generation wins, and the caller is told it did.
			name: "Success/Replayed",

			submissions: []submission{
				{expectCreated: true},
				{expectReplayOf: 1},
			},
		},
		{
			name: "Success/DifferentKey",

			submissions: []submission{
				{expectCreated: true},
				{key: []byte("another-request-key"), expectCreated: true},
			},
		},
		{
			name: "Success/ReplayedWhileRunning",

			submissions: []submission{
				{expectCreated: true},
				{settleBefore: dao.GenerationStatusRunning, expectReplayOf: 1},
			},
		},
		{
			name: "Success/ReplayedAfterSuccess",

			submissions: []submission{
				{expectCreated: true},
				{settleBefore: dao.GenerationStatusSucceeded, expectReplayOf: 1},
			},
		},
		{
			// A failure is reported, not cached: resending the request runs it again.
			name: "Success/RerunAfterFailure",

			submissions: []submission{
				{expectCreated: true},
				{settleBefore: dao.GenerationStatusFailed, expectCreated: true},
			},
		},
		{
			name: "Success/RerunAfterCancellation",

			submissions: []submission{
				{expectCreated: true},
				{settleBefore: dao.GenerationStatusCancelled, expectCreated: true},
			},
		},
	}

	daoGenerationSubmit := dao.NewPgGenerationSubmit()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				results := make([]*dao.GenerationSubmitResult, 0, len(testCase.submissions))

				for index, sub := range testCase.submissions {
					if sub.settleBefore != "" {
						settleTo(ctx, t, results[index-1].Generation, sub.settleBefore)
					}

					request := sub.build()

					result, err := daoGenerationSubmit.Exec(ctx, request)
					require.NoErrorf(t, err, "submission %d", index+1)
					require.Equalf(t, sub.expectCreated, result.Created, "submission %d", index+1)

					if sub.expectCreated {
						require.Equalf(t, request.ID, result.Generation.ID, "submission %d", index+1)
						require.Equal(t, dao.GenerationStatusPending, result.Generation.Status)
						require.Zero(t, result.Generation.Attempt)
						require.Equal(t, request.Purpose, result.Generation.Purpose)
						require.Equal(t, request.Provider, result.Generation.Provider)
						// Stored as sent: key order and characters reach the provider unchanged.
						require.Equal(t, request.Traceparent, result.Generation.Traceparent)
						require.Equal(t, request.Streamed, result.Generation.Streamed)

						// The database owns these, and a caller must never supply them.
						require.False(t, result.Generation.CreatedAt.IsZero())
						require.False(t, result.Generation.RunAt.IsZero())
						require.Nil(t, result.Generation.SettledAt)
						require.False(t, result.Generation.CheckedAt.IsZero())
					}

					if sub.expectReplayOf > 0 {
						require.Equalf(t,
							results[sub.expectReplayOf-1].Generation.ID, result.Generation.ID,
							"submission %d", index+1,
						)
					}

					results = append(results, result)
				}
			})
		})
	}

	// Concurrent submissions of one request resolve to a single generation, so a race cannot produce
	// two priced calls. The case asserts a property of a set of calls, so it runs outside the table.
	t.Run("Success/Concurrent", func(t *testing.T) {
		t.Parallel()

		const concurrency = 8

		postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
			t.Helper()

			daoGenerationSubmit := dao.NewPgGenerationSubmit()

			var (
				wg      sync.WaitGroup
				mu      sync.Mutex
				results []*dao.GenerationSubmitResult
			)

			for range concurrency {
				wg.Go(func() {
					result, err := daoGenerationSubmit.Exec(ctx, submission{}.build())
					if err != nil {
						return
					}

					mu.Lock()
					defer mu.Unlock()

					results = append(results, result)
				})
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
