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

var (
	submitOwner      = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	submitOtherOwner = uuid.MustParse("00000000-0000-0000-0000-000000000002")
)

// submission is one call in a case's sequence. Empty override fields take the base request's value,
// so a case only states what it changes.
type submission struct {
	ownerID     uuid.UUID
	purpose     string
	fingerprint []byte
	request     json.RawMessage

	expectCreated bool
	// expectReplayOf is the 1-based index of an earlier submission whose generation this one must
	// return. Zero means the assertion does not apply.
	expectReplayOf int
	expectErr      error
}

func (sub submission) build() *dao.GenerationSubmitRequest {
	base := &dao.GenerationSubmitRequest{
		ID:                 uuid.Must(uuid.NewV7()),
		OwnerID:            submitOwner,
		Purpose:            "studio.generation",
		IdempotencyKey:     "the-key",
		RequestFingerprint: []byte{0x01},
		Request:            json.RawMessage(`{"instructions": "write"}`),
		MaxAttempts:        1,
	}

	if sub.ownerID != uuid.Nil {
		base.OwnerID = sub.ownerID
	}

	if sub.purpose != "" {
		base.Purpose = sub.purpose
	}

	if sub.fingerprint != nil {
		base.RequestFingerprint = sub.fingerprint
	}

	if sub.request != nil {
		base.Request = sub.request
	}

	return base
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
			// A retry mints a fresh identifier, exactly as a caller that never saw the first answer
			// would. The stored generation wins, and the caller is told it did.
			name: "Replayed",

			submissions: []submission{
				{expectCreated: true},
				{expectCreated: false, expectReplayOf: 1},
			},
		},
		{
			// The key is scoped to the owner alone, so a caller reusing one across its own features
			// gets a replay. Namespacing the key is the caller's job.
			name: "SameKeyDifferentPurpose",

			submissions: []submission{
				{expectCreated: true},
				{purpose: "discovery.recommendation", expectCreated: false, expectReplayOf: 1},
			},
		},
		{
			name: "SameKeyDifferentOwner",

			submissions: []submission{
				{expectCreated: true},
				{ownerID: submitOtherOwner, expectCreated: true},
			},
		},
		{
			// Same key, different request. A caller bug, not a replay — answering with the earlier
			// generation would answer a question that was never asked.
			name: "Error/Conflict",

			submissions: []submission{
				{expectCreated: true},
				{
					fingerprint: []byte{0x02},
					request:     json.RawMessage(`{"instructions": "something else"}`),
					expectErr:   dao.ErrGenerationSubmitConflict,
				},
			},
		},
	}

	daoGenerationSubmit := dao.NewGenerationSubmit()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				results := make([]*dao.GenerationSubmitResult, 0, len(testCase.submissions))

				for index, sub := range testCase.submissions {
					request := sub.build()

					result, err := daoGenerationSubmit.Exec(ctx, request)
					require.ErrorIsf(t, err, sub.expectErr, "submission %d", index+1)

					if sub.expectErr != nil {
						require.Nilf(t, result, "submission %d", index+1)

						results = append(results, nil)

						continue
					}

					require.Equalf(t, sub.expectCreated, result.Created, "submission %d", index+1)

					if sub.expectCreated {
						require.Equalf(t, request.ID, result.Generation.ID, "submission %d", index+1)
						require.Equal(t, dao.GenerationStatusPending, result.Generation.Status)
						require.Zero(t, result.Generation.Attempt)

						// The database owns these, and a caller must never supply them.
						require.False(t, result.Generation.CreatedAt.IsZero())
						require.False(t, result.Generation.RunAt.IsZero())
						require.Nil(t, result.Generation.SettledAt)
						require.Nil(t, result.Generation.LeaseExpiresAt)
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

	postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
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
