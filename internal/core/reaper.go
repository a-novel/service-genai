package core

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
)

// ReaperDao is the data-access dependency of [Reaper].
type ReaperDao interface {
	Exec(ctx context.Context, request *dao.GenerationReapRequest) ([]*dao.Generation, error)
}

// ReaperConfig is what a [Reaper] needs to run.
type ReaperConfig struct {
	// Grace is the head start a late settle gets over the sweep. Applied in the predicate rather
	// than at boot, because other replicas would sweep straight through a boot-only grace.
	Grace time.Duration `validate:"min=0"`
	// BatchSize caps one sweep. The loop repeats until a sweep comes back short, so a large backlog
	// drains without one statement materialising all of it.
	BatchSize int `validate:"required,min=1,max=100"`
	// Retention applies to the generations a sweep abandons, which are terminal.
	Retention time.Duration `validate:"required"`
}

// A Reaper recovers generations whose worker died mid-run.
//
// It never abandons work that can still be resumed: a recovered generation keeps its provider call
// identifier, so the next claim re-attaches to the operation already paid for. Only a generation
// with no attempt left settles here.
type Reaper struct {
	config ReaperConfig
	dao    ReaperDao
}

func NewReaper(config ReaperConfig, reaperDao ReaperDao) (*Reaper, error) {
	err := validate.Struct(config)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}

	return &Reaper{config: config, dao: reaperDao}, nil
}

// RunOnce sweeps once, reporting whether it recovered anything. A full sweep reports work so the
// poll loop runs it again immediately and the backlog drains at full speed.
func (reaper *Reaper) RunOnce(ctx context.Context) (bool, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.Reaper.RunOnce")
	defer span.End()

	recovered, err := reaper.dao.Exec(ctx, &dao.GenerationReapRequest{
		Grace:     reaper.config.Grace,
		Retention: reaper.config.Retention,
		Limit:     reaper.config.BatchSize,
	})
	if err != nil {
		return false, otel.ReportError(span, fmt.Errorf("reap generations: %w", err))
	}

	span.SetAttributes(attribute.Int("reaper.recovered", len(recovered)))

	return otel.ReportSuccess(span, len(recovered) > 0), nil
}
