package dao

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"
	"github.com/a-novel-kit/golib/postgres"
)

//go:embed pg.generationClaim.sql
var generationClaimQuery string

// Ceilings on what a caller may ask for. A lease longer than an hour outlives any generation we run,
// and an unbounded batch would let one worker take the whole queue.
const (
	GenerationClaimMaxLease = time.Hour
	GenerationClaimMaxLimit = 100
)

// ErrGenerationClaimInvalid is returned by [GenerationClaim.Exec] for a request outside those
// ceilings.
var ErrGenerationClaimInvalid = errors.New("invalid claim request")

// GenerationClaimRequest is the input to [GenerationClaim.Exec].
type GenerationClaimRequest struct {
	// WorkerID is recorded on each claimed generation so a stranded claim can be traced.
	WorkerID string
	// Limit caps how many generations one claim takes.
	Limit int
	// Lease is how long the claim holds before the reaper may recover the generation. Size it to the
	// expected run: an outrun lease is recoverable, but every lapse burns an attempt.
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

	switch {
	case request.WorkerID == "":
		return nil, otel.ReportError(span, fmt.Errorf("%w: no worker id", ErrGenerationClaimInvalid))
	case request.Limit <= 0, request.Limit > GenerationClaimMaxLimit:
		return nil, otel.ReportError(span, fmt.Errorf("%w: limit %d", ErrGenerationClaimInvalid, request.Limit))
	case request.Lease <= 0, request.Lease > GenerationClaimMaxLease:
		return nil, otel.ReportError(span, fmt.Errorf("%w: lease %s", ErrGenerationClaimInvalid, request.Lease))
	}

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
