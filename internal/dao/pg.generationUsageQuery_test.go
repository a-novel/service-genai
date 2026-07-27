package dao_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

// usageRow is one attempt's consumption, seeded directly so a case can set the dimensions it cares
// about without running a generation to produce them.
type usageRow struct {
	purpose string
	model   string
	owner   uuid.UUID
	tokens  int64
	// age backdates the row, so a case can put it outside the window.
	age time.Duration
}

func seedUsage(ctx context.Context, t *testing.T, row usageRow) {
	t.Helper()

	db, err := postgres.GetContext(ctx)
	if err != nil {
		panic(err)
	}

	owner := row.owner
	if owner == uuid.Nil {
		owner = testOwner
	}

	_, err = db.NewRaw(`
		INSERT INTO generation_usage (
			generation_id, attempt, owner_id, purpose, provider, model,
			input_tokens, cached_input_tokens, output_tokens, reasoning_tokens, created_at
		) VALUES (?0, ?1, ?2, ?3, 'openai', ?4, ?5, 0, ?5, 0, clock_timestamp() - make_interval(secs => ?6))`,
		uuid.Must(uuid.NewV7()), int16(1), owner, row.purpose, row.model, row.tokens, row.age.Seconds(),
	).Exec(ctx)
	if err != nil {
		panic(err)
	}
}

func TestGenerationUsageQuery(t *testing.T) {
	t.Parallel()

	otherOwner := uuid.MustParse("00000000-0000-0000-0000-000000000002")

	testCases := []struct {
		name string

		seed []usageRow

		// window is how far back the query reaches. Zero means one hour.
		window  time.Duration
		purpose string
		model   string

		// expectGroups is the (purpose, model) pairs expected, in order.
		expectGroups [][2]string
		expectInput  int64
	}{
		{
			name: "Success/GroupsByPurposeAndModel",

			seed: []usageRow{
				{purpose: "studio.generation", model: "model-a", tokens: 100},
				{purpose: "studio.generation", model: "model-a", tokens: 50},
				{purpose: "studio.generation", model: "model-b", tokens: 30},
				{purpose: "discovery.rank", model: "model-a", tokens: 10},
			},

			// Ordered by purpose then model, so a caller reads a stable report.
			expectGroups: [][2]string{
				{"discovery.rank", "model-a"},
				{"studio.generation", "model-a"},
				{"studio.generation", "model-b"},
			},
			expectInput: 190,
		},
		{
			// A retried generation consumed more than once, so both attempts count.
			name: "Success/SumsAcrossAttempts",

			seed: []usageRow{
				{purpose: "studio.generation", model: "model-a", tokens: 100},
				{purpose: "studio.generation", model: "model-a", tokens: 100},
			},

			expectGroups: [][2]string{{"studio.generation", "model-a"}},
			expectInput:  200,
		},
		{
			// The window is the point: rows outside it are another period's bill.
			name: "Success/ExcludesRowsOutsideTheWindow",

			seed: []usageRow{
				{purpose: "studio.generation", model: "model-a", tokens: 100},
				{purpose: "studio.generation", model: "model-a", tokens: 999, age: 48 * time.Hour},
			},

			expectGroups: [][2]string{{"studio.generation", "model-a"}},
			expectInput:  100,
		},
		{
			// Owner-scoped, like every other read here.
			name: "Success/ExcludesOtherOwners",

			seed: []usageRow{
				{purpose: "studio.generation", model: "model-a", tokens: 100},
				{purpose: "studio.generation", model: "model-a", tokens: 999, owner: otherOwner},
			},

			expectGroups: [][2]string{{"studio.generation", "model-a"}},
			expectInput:  100,
		},
		{
			name: "Success/FiltersByPurpose",

			seed: []usageRow{
				{purpose: "studio.generation", model: "model-a", tokens: 100},
				{purpose: "discovery.rank", model: "model-a", tokens: 999},
			},
			purpose: "studio.generation",

			expectGroups: [][2]string{{"studio.generation", "model-a"}},
			expectInput:  100,
		},
		{
			name: "Success/FiltersByModel",

			seed: []usageRow{
				{purpose: "studio.generation", model: "model-a", tokens: 100},
				{purpose: "studio.generation", model: "model-b", tokens: 999},
			},
			model: "model-a",

			expectGroups: [][2]string{{"studio.generation", "model-a"}},
			expectInput:  100,
		},
		{
			name: "Success/NoUsage",
		},
	}

	daoUsageQuery := dao.NewGenerationUsageQuery()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgres.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				for _, row := range testCase.seed {
					seedUsage(ctx, t, row)
				}

				window := testCase.window
				if window == 0 {
					window = time.Hour
				}

				groups, err := daoUsageQuery.Exec(ctx, &dao.GenerationUsageQueryRequest{
					OwnerID: testOwner,
					From:    time.Now().Add(-window),
					To:      time.Now().Add(time.Hour),
					Purpose: testCase.purpose,
					Model:   testCase.model,
				})
				require.NoError(t, err)
				require.Len(t, groups, len(testCase.expectGroups))

				var total int64

				for index, group := range groups {
					require.Equal(t, testCase.expectGroups[index][0], group.Purpose)
					require.Equal(t, testCase.expectGroups[index][1], group.Model)
					require.Positive(t, group.Attempts)

					total += group.InputTokens
				}

				require.Equal(t, testCase.expectInput, total)
			})
		})
	}
}
