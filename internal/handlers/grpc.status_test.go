package handlers_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/a-novel-kit/golib/postgres"
	postgrespresets "github.com/a-novel-kit/golib/postgres/presets"

	"github.com/a-novel/service-genai/internal/config/configtest"
	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/handlers"
	handlersmocks "github.com/a-novel/service-genai/internal/handlers/mocks"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
	servicegenai "github.com/a-novel/service-genai/pkg/go"
)

func TestGrpcStatus(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		skipPostgres  bool
		closePostgres bool
		transaction   bool
		cancel        bool
		queueDepth    *core.QueueDepthResult
		queueError    error
		expectQueue   *genaiv0.QueueDepth
		expectStatus  codes.Code
	}{
		{
			name:         "Success",
			queueDepth:   &core.QueueDepthResult{Pending: 2, OldestPendingAge: 90 * time.Second},
			expectQueue:  &genaiv0.QueueDepth{Pending: 2, OldestPendingAgeSeconds: 90},
			expectStatus: codes.OK,
		},
		{
			name:         "Success/EmptyQueue",
			queueDepth:   &core.QueueDepthResult{},
			expectQueue:  &genaiv0.QueueDepth{},
			expectStatus: codes.OK,
		},
		{name: "Error/MissingPostgres", skipPostgres: true, expectStatus: codes.Unavailable},
		{name: "Error/ClosedPostgres", closePostgres: true, expectStatus: codes.Unavailable},
		{name: "Error/TransactionContext", transaction: true, expectStatus: codes.Unavailable},
		{name: "Error/CancelledProbe", cancel: true, expectStatus: codes.Unavailable},
		{name: "Error/QueueInspection", queueError: errFoo, expectStatus: codes.Unavailable},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			queueDepth := handlersmocks.NewMockGrpcStatusQueueDepthService(t)
			if testCase.queueDepth != nil || testCase.queueError != nil {
				queueDepth.EXPECT().Exec(mock.Anything).Return(testCase.queueDepth, testCase.queueError).Once()
			}

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
					t.Cleanup(func() { require.NoError(t, tx.Rollback()) })

					ctx = context.WithValue(ctx, postgres.ContextKey{}, tx)
				}
			}

			if testCase.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()

				ctx = cancelled
			}

			response, err := handlers.NewGrpcStatus(queueDepth).Status(ctx, &genaiv0.StatusRequest{})
			result, ok := status.FromError(err)
			require.True(t, ok)
			require.Equal(t, testCase.expectStatus, result.Code())

			if testCase.expectStatus != codes.OK {
				require.Nil(t, response)
				require.Equal(t, "service dependencies unavailable", result.Message())
			} else {
				require.Equal(t, &genaiv0.StatusResponse{
					Postgres: &genaiv0.DependencyHealth{Status: genaiv0.DependencyStatus_DEPENDENCY_STATUS_UP},
					Queue:    testCase.expectQueue,
				}, response)
			}

			queueDepth.AssertExpectations(t)
		})
	}

	t.Run("Transport/Unavailable", func(t *testing.T) {
		t.Parallel()

		queueDepth := handlersmocks.NewMockGrpcStatusQueueDepthService(t)
		listener := bufconn.Listen(1024 * 1024)
		server := grpc.NewServer()
		genaiv0.RegisterStatusServiceServer(server, handlers.NewGrpcStatus(queueDepth))

		serveResult := make(chan error, 1)
		go func() { serveResult <- server.Serve(listener) }()

		t.Cleanup(func() {
			server.Stop()
			require.NoError(t, <-serveResult)
		})

		client, err := servicegenai.NewClient(
			"passthrough:///health-test",
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return listener.DialContext(ctx)
			}),
		)
		require.NoError(t, err)

		defer client.Close()

		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		response, err := client.Status(ctx, &servicegenai.StatusRequest{})
		require.Nil(t, response)
		require.Equal(t, codes.Unavailable, status.Code(err))
		require.Equal(t, "service dependencies unavailable", status.Convert(err).Message())
		queueDepth.AssertExpectations(t)
	})
}
