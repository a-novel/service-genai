package dao_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel-kit/golib/postgres/postgrestest"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/models/migrations"
)

func TestPgGenerationAttemptList(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		// answered records the generation's answer before the read.
		answered bool

		expectLen int
	}{
		{
			name: "Success",

			answered:  true,
			expectLen: 1,
		},
		{
			name: "Success/Empty",
		},
	}

	daoAttemptList := dao.NewPgGenerationAttemptList()
	daoAttemptInsert := dao.NewPgGenerationAttemptInsert()

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			postgrestest.RunDBTest(t, configtest.PostgresPreset, migrations.Migrations, func(ctx context.Context, t *testing.T) {
				t.Helper()

				generation := seedGeneration(ctx, t)
				other := seedGeneration(ctx, t)

				// Another generation's answer stays out of the read.
				answered := []*dao.Generation{other}
				if testCase.answered {
					answered = append(answered, generation)
				}

				for _, seeded := range answered {
					_, err := daoAttemptInsert.Exec(ctx, &dao.GenerationAttemptInsertRequest{
						GenerationID: seeded.ID, Response: []byte(`{}`),
					})
					require.NoError(t, err)
				}

				listed, err := daoAttemptList.Exec(ctx, &dao.GenerationAttemptListRequest{GenerationID: generation.ID})
				require.NoError(t, err)
				require.Len(t, listed, testCase.expectLen)

				for _, attempt := range listed {
					require.Equal(t, generation.ID, attempt.GenerationID)
				}
			})
		})
	}
}
