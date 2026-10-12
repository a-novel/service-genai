package core

import (
	"context"
	"fmt"
	"time"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
)

// GenerationQueueDepthDao reads the backlog.
type GenerationQueueDepthDao interface {
	Exec(ctx context.Context, request *dao.GenerationQueueDepthRequest) (*dao.GenerationQueueDepth, error)
}

// GenerationQueueDepthRequest holds the parameters for a [GenerationQueueDepth.Exec] call.
type GenerationQueueDepthRequest struct{}

// GenerationQueueDepthResult is the backlog, as the health report states it.
type GenerationQueueDepthResult struct {
	// Pending is how many generations wait for the provider to accept their call.
	Pending int64
	// OldestPendingAge is how long the one waiting longest has waited.
	OldestPendingAge time.Duration
}

// A GenerationQueueDepth reads the backlog.
type GenerationQueueDepth struct {
	dao GenerationQueueDepthDao
}

// NewGenerationQueueDepth returns a new GenerationQueueDepth service.
func NewGenerationQueueDepth(dao GenerationQueueDepthDao) *GenerationQueueDepth {
	return &GenerationQueueDepth{dao: dao}
}

func (service *GenerationQueueDepth) Exec(
	ctx context.Context, _ *GenerationQueueDepthRequest,
) (*GenerationQueueDepthResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationQueueDepth")
	defer span.End()

	depth, err := service.dao.Exec(ctx, &dao.GenerationQueueDepthRequest{})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("read queue depth: %w", err))
	}

	return &GenerationQueueDepthResult{
		Pending:          depth.Pending,
		OldestPendingAge: time.Duration(depth.OldestPendingAgeSeconds * float64(time.Second)),
	}, nil
}
