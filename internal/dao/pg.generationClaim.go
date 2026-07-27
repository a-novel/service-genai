package dao

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationClaim.sql
var generationClaimQuery string

// GenerationClaimRequest is the input to [GenerationClaim.Exec].
type GenerationClaimRequest struct {
	// WorkerID is recorded on each claimed generation so a stranded claim can be traced.
	WorkerID string
	// Limit caps how many generations one claim takes. Bounded by the caller.
	Limit int
	// Lease is how long the claim holds before the reaper may recover the generation. Bounded by the
	// caller, which sizes it to the expected run.
	Lease time.Duration
}

// GenerationClaim takes a batch of pending generations for a worker to run.
type GenerationClaim struct{}

func NewGenerationClaim() *GenerationClaim {
	return new(GenerationClaim)
}

func (dao *GenerationClaim) Exec(ctx context.Context, request *GenerationClaimRequest) ([]*Generation, error) {
	ctx, span := otel.Tracer().Start(ctx, "dao.GenerationClaim")
	defer span.End()

	span.SetAttributes(
		attribute.String("claim.worker_id", request.WorkerID),
		attribute.Int("claim.limit", request.Limit),
		attribute.Float64("claim.lease_seconds", request.Lease.Seconds()),
	)

	tx, err := postgres.GetContext(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("get transaction: %w", err))
	}

	var entities []*Generation

	err = tx.NewRaw(
		generationClaimQuery, request.WorkerID, request.Limit, request.Lease.Seconds(),
	).Scan(ctx, &entities)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("execute query: %w", err))
	}

	span.SetAttributes(attribute.Int("claim.claimed", len(entities)))

	return otel.ReportSuccess(span, entities), nil
}
