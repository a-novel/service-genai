package core

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
)

// QueueDepthDao is the data-access dependency of [QueueDepth].
type QueueDepthDao interface {
	Exec(ctx context.Context) (*dao.GenerationQueueDepth, error)
}

// QueueDepthResult is the backlog, as the health report states it.
type QueueDepthResult struct {
	// Pending is how many generations are due and unclaimed.
	Pending int64
	// OldestPendingAge is how long the oldest of them has waited. A count alone cannot tell a queue
	// absorbing a burst from a stalled one; this is what separates them.
	OldestPendingAge time.Duration
}

// A QueueDepth reads the backlog.
type QueueDepth struct {
	dao QueueDepthDao
}

func NewQueueDepth(depthDao QueueDepthDao) *QueueDepth {
	return &QueueDepth{dao: depthDao}
}

func (service *QueueDepth) Exec(ctx context.Context) (*QueueDepthResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.QueueDepth")
	defer span.End()

	depth, err := service.dao.Exec(ctx)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("read queue depth: %w", err))
	}

	span.SetAttributes(attribute.Int64("queue.pending", depth.Pending))

	return otel.ReportSuccess(span, &QueueDepthResult{
		Pending:          depth.Pending,
		OldestPendingAge: time.Duration(depth.OldestPendingAgeSeconds * float64(time.Second)),
	}), nil
}
