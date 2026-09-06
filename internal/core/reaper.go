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
	// Grace delays recovery after lease expiry. Worker authority ends at expiry on every replica.
	Grace time.Duration `validate:"min=0"`
	// BatchSize caps one sweep. The loop repeats until a sweep comes back short, so a large backlog
	// drains without one statement materialising all of it.
	BatchSize int `validate:"required,min=1,max=100"`
	// Retention applies to every terminal outcome recorded by a sweep.
	Retention time.Duration `validate:"required"`
}

// A Reaper recovers generations whose worker died mid-run.
//
// Known provider operations remain resumable under the same inference attempt. Start intent without
// a provider identifier settles as an unknown outcome; unstarted work follows cancellation and
// attempt-budget rules.
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
