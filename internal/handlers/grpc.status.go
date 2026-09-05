package handlers

import (
	"context"

	"github.com/uptrace/bun"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/core"
	genaiv0 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v0"
)

// NewGrpcHealthStatus reports a successfully probed dependency.
func NewGrpcHealthStatus() *genaiv0.DependencyHealth {
	return &genaiv0.DependencyHealth{
		Status: genaiv0.DependencyStatus_DEPENDENCY_STATUS_UP,
	}
}

// GrpcStatusQueueDepthService is the service dependency of [GrpcStatus].
type GrpcStatusQueueDepthService interface {
	Exec(ctx context.Context) (*core.QueueDepthResult, error)
}

// GrpcStatus is the gRPC handler for the Status RPC, reporting the health of the
// service's external dependencies and the queue's own backlog.
type GrpcStatus struct {
	genaiv0.UnimplementedStatusServiceServer

	queueDepth GrpcStatusQueueDepthService
}

// NewGrpcStatus creates a dependency-readiness handler with queue inspection.
func NewGrpcStatus(queueDepth GrpcStatusQueueDepthService) *GrpcStatus {
	return &GrpcStatus{queueDepth: queueDepth}
}

// Status returns the backlog only after PostgreSQL and queue inspection succeed.
// Failed or unassessable dependencies return Unavailable without private error details.
func (handler *GrpcStatus) Status(ctx context.Context, _ *genaiv0.StatusRequest) (*genaiv0.StatusResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.Status")
	defer span.End()

	err := handler.reportPostgres(ctx)
	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Unavailable, "service dependencies unavailable")
	}

	depth, err := handler.queueDepth.Exec(ctx)
	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Unavailable, "service dependencies unavailable")
	}

	return otel.ReportSuccess(span, &genaiv0.StatusResponse{
		Postgres: NewGrpcHealthStatus(),
		Queue: &genaiv0.QueueDepth{
			Pending:                 depth.Pending,
			OldestPendingAgeSeconds: depth.OldestPendingAge.Seconds(),
		},
	}), nil
}

func (handler *GrpcStatus) reportPostgres(ctx context.Context) error {
	ctx, span := otel.Tracer().Start(ctx, "grpc.Status(reportPostgres)")
	defer span.End()

	pg, err := postgres.GetContext(ctx)
	if err != nil {
		return otel.ReportError(span, err)
	}

	pgdb, ok := pg.(*bun.DB)
	if !ok {
		// A transaction does not expose the pool needed to assess dependency readiness.
		return otel.ReportError(span, postgres.ErrNoDbInContext)
	}

	err = pgdb.PingContext(ctx)
	if err != nil {
		return otel.ReportError(span, err)
	}

	otel.ReportSuccessNoContent(span)

	return nil
}
