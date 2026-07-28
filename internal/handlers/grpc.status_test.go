package handlers_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/handlers"
	handlersmocks "github.com/a-novel/service-genai/internal/handlers/mocks"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

func TestStatus(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		skipPostgres bool

		expect       *genaiv0.StatusResponse
		expectStatus codes.Code
	}{
		{
			name: "Success",

			expect: &genaiv0.StatusResponse{
				Postgres: &genaiv0.DependencyHealth{
					Status: genaiv0.DependencyStatus_DEPENDENCY_STATUS_UP,
				},
				Queue: &genaiv0.QueueDepth{Pending: 2, OldestPendingAgeSeconds: 90},
			},
		},
		{
			// Omitting postgres from the context makes the probe fail, so the entry reports
			// DEPENDENCY_STATUS_DOWN. Comparing the whole response guards the message shape:
			// it fails if a raw error string is ever attached back onto DependencyHealth.
			name: "Success/Degraded",

			skipPostgres: true,

			// The backlog cannot be measured without the database, and postgres already reports
			// down — so a missing queue is a consequence of that, not a second failure.
			expect: &genaiv0.StatusResponse{
				Postgres: &genaiv0.DependencyHealth{
					Status: genaiv0.DependencyStatus_DEPENDENCY_STATUS_DOWN,
				},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			queueDepth := handlersmocks.NewMockGrpcStatusQueueDepthService(t)

			if testCase.skipPostgres {
				queueDepth.EXPECT().Exec(mock.Anything).Return(nil, errFoo)
			} else {
				queueDepth.EXPECT().
					Exec(mock.Anything).
					Return(&core.QueueDepthResult{Pending: 2, OldestPendingAge: 90 * time.Second}, nil)
			}

			handler := handlers.NewGrpcStatus(queueDepth)

			ctx := t.Context()

			if !testCase.skipPostgres {
				var err error

				ctx, err = postgres.NewContext(ctx, configtest.PostgresPreset)
				require.NoError(t, err)
			}

			res, err := handler.Status(ctx, new(genaiv0.StatusRequest))
			resSt, ok := status.FromError(err)
			require.True(t, ok, resSt.Code().String())
			require.Equal(
				t,
				testCase.expectStatus, resSt.Code(),
				"expected status code %s, got %s (%v)", testCase.expectStatus, resSt.Code(), err,
			)
			require.Equal(t, testCase.expect, res)
		})
	}
}
