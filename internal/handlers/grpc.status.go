package handlers

import (
	"context"

	"github.com/samber/lo"
	"github.com/uptrace/bun"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"

	"github.com/a-novel/service-genai/internal/handlers/protogen"
)

// NewGrpcHealthStatus converts an error into a DependencyHealth proto message,
// mapping nil to DEPENDENCY_STATUS_UP and any non-nil error to DEPENDENCY_STATUS_DOWN.
//
// The error itself is dropped from the message: a raw dependency error routinely embeds
// internal hostnames, ports, or schema names. The health probe records it on its trace
// span, where operators can read it.
func NewGrpcHealthStatus(err error) *protogen.DependencyHealth {
	return &protogen.DependencyHealth{
		Status: lo.Ternary(
			err == nil,
			protogen.DependencyStatus_DEPENDENCY_STATUS_UP,
			protogen.DependencyStatus_DEPENDENCY_STATUS_DOWN,
		),
	}
}

// GrpcStatus is the gRPC handler for the Status RPC, reporting the health of the
// service's external dependencies.
type GrpcStatus struct {
	protogen.UnimplementedStatusServiceServer
}

func NewGrpcStatus() *GrpcStatus {
	return new(GrpcStatus)
}

// Status probes each dependency and returns its current health.
func (handler *GrpcStatus) Status(ctx context.Context, _ *protogen.StatusRequest) (*protogen.StatusResponse, error) {
	ctx, span := otel.Tracer().Start(ctx, "grpc.Status")
	defer span.End()

	return &protogen.StatusResponse{
		Postgres: NewGrpcHealthStatus(handler.reportPostgres(ctx)),
	}, nil
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
