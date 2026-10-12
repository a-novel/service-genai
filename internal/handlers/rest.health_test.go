package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/a-novel-kit/golib/postgres"
	postgrespresets "github.com/a-novel-kit/golib/postgres/presets"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/handlers"
)

func TestRestHealth(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		skipPostgres  bool
		closePostgres bool
		transaction   bool
		cancel        bool
		expectStatus  int
		expectHealth  string
	}{
		{
			name:         "Success",
			expectStatus: http.StatusOK,
			expectHealth: handlers.RestHealthStatusUp,
		},
		{
			name:         "Error/MissingPostgres",
			skipPostgres: true,
			expectStatus: http.StatusServiceUnavailable,
			expectHealth: handlers.RestHealthStatusDown,
		},
		{
			name:          "Error/ClosedPostgres",
			closePostgres: true,
			expectStatus:  http.StatusServiceUnavailable,
			expectHealth:  handlers.RestHealthStatusDown,
		},
		{
			name:         "Error/TransactionContext",
			transaction:  true,
			expectStatus: http.StatusServiceUnavailable,
			expectHealth: handlers.RestHealthStatusDown,
		},
		{
			name:         "Error/CancelledProbe",
			cancel:       true,
			expectStatus: http.StatusServiceUnavailable,
			expectHealth: handlers.RestHealthStatusDown,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			if !testCase.skipPostgres {
				var err error

				preset := postgrespresets.NewDefault(configtest.PostgresPreset.Options()...)
				ctx, err = postgres.NewContext(ctx, preset)
				require.NoError(t, err)
				pg, err := postgres.GetContext(ctx)
				require.NoError(t, err)

				db, ok := pg.(*bun.DB)
				require.True(t, ok)
				t.Cleanup(func() { require.NoError(t, db.Close()) })

				if testCase.closePostgres {
					require.NoError(t, db.Close())
				}

				if testCase.transaction {
					tx, err := db.BeginTx(ctx, nil)

					require.NoError(t, err)
					defer func() {
						require.NoError(t, ctx.Err())
						require.NoError(t, tx.Rollback())
					}()

					ctx = context.WithValue(ctx, postgres.ContextKey{}, tx)
				}
			}

			if testCase.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()

				ctx = cancelled
			}

			request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/healthcheck", nil)
			recorder := httptest.NewRecorder()
			handlers.NewRestHealth().ServeHTTP(recorder, request)
			response := recorder.Result()
			require.Equal(t, testCase.expectStatus, response.StatusCode)
			require.Contains(t, response.Header.Get("Content-Type"), "application/json")

			body, err := io.ReadAll(response.Body)
			require.NoError(t, errors.Join(err, response.Body.Close()))

			var report any
			require.NoError(t, json.Unmarshal(body, &report))
			// Exact matching protects the public report against raw dependency errors.
			require.Equal(t, map[string]any{
				"client:postgres": map[string]any{"status": testCase.expectHealth},
			}, report)
		})
	}
}
