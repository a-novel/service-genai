package handlers

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

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

// GrpcStatusService is the service dependency of [GrpcStatus].
type GrpcStatusService interface {
	Exec(ctx context.Context, request *core.GenerationQueueDepthRequest) (*core.GenerationQueueDepthResult, error)
}

// GrpcStatus is the gRPC handler for the Status RPC, reporting the health of the
// service's external dependencies and the queue's own backlog.
type GrpcStatus struct {
	genaiv0.UnimplementedStatusServiceServer

	service GrpcStatusService
}

// NewGrpcStatus creates a dependency-readiness handler with queue inspection.
func NewGrpcStatus(service GrpcStatusService) *GrpcStatus {
	return &GrpcStatus{service: service}
}

// Status probes PostgreSQL and returns its health with the queue's backlog, or Unavailable when a
// probe fails.
func (handler *GrpcStatus) Status(ctx context.Context, _ *genaiv0.StatusRequest) (*genaiv0.StatusResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.Status")
	defer span.End()

	err := handler.reportPostgres(ctx)
	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Unavailable, "service dependencies unavailable")
	}

	depth, err := handler.service.Exec(ctx, &core.GenerationQueueDepthRequest{})
	if err != nil {
		_ = otel.ReportError(span, err)

		return nil, status.Error(codes.Unavailable, "service dependencies unavailable")
	}

	return &genaiv0.StatusResponse{
		Postgres: NewGrpcHealthStatus(),
		Queue: &genaiv0.QueueDepth{
			Pending:          depth.Pending,
			OldestPendingAge: durationpb.New(depth.OldestPendingAge),
		},
	}, nil
}

func (handler *GrpcStatus) reportPostgres(ctx context.Context) error {
	ctx, span := otel.Tracer().Start(ctx, "grpc.Status(reportPostgres)")
	defer span.End()

	err := postgres.Health(ctx)
	if err != nil {
		return otel.ReportError(span, err)
	}

	return nil
}
