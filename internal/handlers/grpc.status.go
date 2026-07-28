package handlers

import (
	"context"

	"github.com/samber/lo"
	"github.com/uptrace/bun"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/core"
	genaiv1 "github.com/a-novel/service-genai/internal/handlers/protogen/anovel/genai/v1"
)

// NewGrpcHealthStatus converts an error into a DependencyHealth proto message,
// mapping nil to DEPENDENCY_STATUS_UP and any non-nil error to DEPENDENCY_STATUS_DOWN.
//
// The error itself is dropped from the message: a raw dependency error routinely embeds
// internal hostnames, ports, or schema names. The health probe records it on its trace
// span, where operators can read it.
func NewGrpcHealthStatus(err error) *genaiv1.DependencyHealth {
	return &genaiv1.DependencyHealth{
		Status: lo.Ternary(
			err == nil,
			genaiv1.DependencyStatus_DEPENDENCY_STATUS_UP,
			genaiv1.DependencyStatus_DEPENDENCY_STATUS_DOWN,
		),
	}
}

// GrpcStatusQueueDepthService is the service dependency of [GrpcStatus].
type GrpcStatusQueueDepthService interface {
	Exec(ctx context.Context) (*core.QueueDepthResult, error)
}

// GrpcStatus is the gRPC handler for the Status RPC, reporting the health of the
// service's external dependencies and the queue's own backlog.
type GrpcStatus struct {
	genaiv1.UnimplementedStatusServiceServer

	queueDepth GrpcStatusQueueDepthService
}

func NewGrpcStatus(queueDepth GrpcStatusQueueDepthService) *GrpcStatus {
	return &GrpcStatus{queueDepth: queueDepth}
}

// Status probes each dependency and returns its current health, with the backlog beside it.
func (handler *GrpcStatus) Status(ctx context.Context, _ *genaiv1.StatusRequest) (*genaiv1.StatusResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.Status")
	defer span.End()

	response := &genaiv1.StatusResponse{
		Postgres: NewGrpcHealthStatus(handler.reportPostgres(ctx)),
	}

	// The backlog cannot be measured without the database, and postgres already reports down in
	// that case — so a missing queue is a consequence of the health above, not a second failure.
	depth, err := handler.queueDepth.Exec(ctx)
	if err != nil {
		_ = otel.ReportError(span, err)

		return response, nil
	}

	response.Queue = &genaiv1.QueueDepth{
		Pending:                 depth.Pending,
		OldestPendingAgeSeconds: depth.OldestPendingAge.Seconds(),
	}

	return response, nil
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
		// In transaction mode the context carries a transaction, so there is no
		// pooled connection to ping; treat the dependency as healthy.
		return nil
	}

	err = pgdb.Ping()
	if err != nil {
		return otel.ReportError(span, err)
	}

	otel.ReportSuccessNoContent(span)

	return nil
}
