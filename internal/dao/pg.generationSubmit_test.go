package dao_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

var submitOwner = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// submission is one call in a case's sequence.
type submission struct {
	// key overrides the base request key.
	key []byte
	// settleBefore moves the previous submission's generation to this status first.
	settleBefore dao.GenerationStatus

	expectCreated bool
	// expectReplayOf is the 1-based index of an earlier submission whose generation this one must
	// return. Zero means the assertion does not apply.
	expectReplayOf int
}

func (sub submission) build() *dao.GenerationSubmitRequest {
	base := &dao.GenerationSubmitRequest{
		ID:          uuid.Must(uuid.NewV7()),
		OwnerID:     submitOwner,
		Purpose:     "studio.generation",
		RequestKey:  []byte("the-request-key"),
		Request:     json.RawMessage(`{"instructions": "write"}`),
		MaxAttempts: 1,
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
		_, err := dao.NewGenerationRequestCancel().Exec(ctx, &dao.GenerationRequestCancelRequest{
			ID: generation.ID, OwnerID: generation.OwnerID, Error: "generation cancelled", Retention: testRetention,
		})
		if err != nil {
			panic(err)
		}
	case dao.GenerationStatusSucceeded:
		running := runGeneration(ctx, t, generation.ID)

		_, err := dao.NewGenerationSettle().Exec(ctx, &dao.GenerationSettleRequest{
			ID: running.ID, Attempt: running.Attempt, ProviderCallID: running.ProviderCallID,
			Status: dao.GenerationStatusSucceeded, Output: json.RawMessage(`{"text": "done"}`),
			Retention: testRetention,
		})
		if err != nil {
			panic(err)
		}
	case dao.GenerationStatusRunning:
		runGeneration(ctx, t, generation.ID)
	case dao.GenerationStatusPending:
	}
}

func TestGenerationSubmit(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// Each case is a sequence of submissions against one database, because what submit
		// guarantees is only observable across more than one call.
		submissions []submission
	}{
		{
			name: "Created",

			submissions: []submission{{expectCreated: true}},
		},
		{
			// A resend mints a fresh identifier, exactly as a caller that never saw the first answer
			// would. The stored generation wins, and the caller is told it did.
			name: "Replayed",

			submissions: []submission{
				{expectCreated: true},
				{expectReplayOf: 1},
			},
		},
		{
			name: "DifferentKey",

			submissions: []submission{
				{expectCreated: true},
				{key: []byte("another-request-key"), expectCreated: true},
			},
		},
		{
			name: "ReplayedWhileRunning",

			submissions: []submission{
				{expectCreated: true},
				{settleBefore: dao.GenerationStatusRunning, expectReplayOf: 1},
			},
		},
		{
			name: "ReplayedAfterSuccess",

			submissions: []submission{
				{expectCreated: true},
				{settleBefore: dao.GenerationStatusSucceeded, expectReplayOf: 1},
			},
		},
		{
			// A failure is reported, not cached: resending the request runs it again.
			name: "RerunAfterFailure",

			submissions: []submission{
				{expectCreated: true},
				{settleBefore: dao.GenerationStatusFailed, expectCreated: true},
			},
		},
		{
			name: "RerunAfterCancellation",

			submissions: []submission{
				{expectCreated: true},
				{settleBefore: dao.GenerationStatusCancelled, expectCreated: true},
			},
		},
	}

	daoGenerationSubmit := dao.NewGenerationSubmit()

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
}

// Kept out of the table above because it asserts a property of a set of concurrent calls rather
// than a sequence of outcomes. It is the case that matters most: submit exists so a race cannot
// produce two priced calls, so the race is what it is tested against.
func TestGenerationSubmitConcurrent(t *testing.T) {
	t.Parallel()

	const concurrency = 8

	postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
		t.Helper()

		daoGenerationSubmit := dao.NewGenerationSubmit()

		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			results []*dao.GenerationSubmitResult
		)

		for range concurrency {
			wg.Add(1)

			go func() {
				defer wg.Done()

				result, err := daoGenerationSubmit.Exec(ctx, submission{}.build())
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
}
