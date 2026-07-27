package core

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
)

// MaxUsageWindow bounds one query. A month of one owner's consumption is what a billing or quota
// period asks for; a wider window is a report, and a report should page rather than ask the database
// for everything at once.
const MaxUsageWindow = 366 * 24 * time.Hour

// UsageQueryDao is the data-access dependency of [UsageQuery].
type UsageQueryDao interface {
	Exec(ctx context.Context, request *dao.GenerationUsageQueryRequest) ([]*dao.GenerationUsageGroup, error)
}

// UsageQueryRequest holds the parameters for a [UsageQuery.Exec] call.
type UsageQueryRequest struct {
	OwnerID uuid.UUID `validate:"required"`
	// From is inclusive and To exclusive, so adjacent windows neither overlap nor skip a row. Both
	// are required: this table is never purged, so an unbounded scan grows without limit and a
	// caller has no way to know it asked for one.
	From time.Time `validate:"required"`
	To   time.Time `validate:"required,gtfield=From"`
	// Purpose and Model narrow the result. Empty means no filter.
	Purpose string `validate:"max=255"`
	Model   string `validate:"max=255"`
}

// UsageQueryResult is an owner's consumption over the window.
type UsageQueryResult struct {
	// Groups is one entry per purpose and model, ordered by both.
	Groups []*dao.GenerationUsageGroup
	// Total is every group summed. A quota layer wants only this; a pricing layer needs the groups,
	// because a rate applies per model.
	Total UsageTotal
}

// UsageTotal is consumption with the dimensions collapsed.
type UsageTotal struct {
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	ReasoningTokens   int64
	Attempts          int64
}

// A UsageQuery reads what an owner consumed over a window.
//
// It returns tokens, never money. What a token costs is a downstream decision, which is why the
// model that actually billed is carried on every group.
type UsageQuery struct {
	dao UsageQueryDao
}

func NewUsageQuery(queryDao UsageQueryDao) *UsageQuery {
	return &UsageQuery{dao: queryDao}
}

func (service *UsageQuery) Exec(ctx context.Context, request *UsageQueryRequest) (*UsageQueryResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.UsageQuery")
	defer span.End()

	span.SetAttributes(
		attribute.String("usage.owner_id", request.OwnerID.String()),
		attribute.String("usage.from", request.From.Format(time.RFC3339)),
		attribute.String("usage.to", request.To.Format(time.RFC3339)),
	)

	err := validate.Struct(request)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("%w: %w", ErrInvalidRequest, err))
	}

	if request.To.Sub(request.From) > MaxUsageWindow {
		return nil, otel.ReportError(span, fmt.Errorf(
			"%w: window of %s exceeds %s", ErrInvalidRequest, request.To.Sub(request.From), MaxUsageWindow,
		))
	}

	groups, err := service.dao.Exec(ctx, &dao.GenerationUsageQueryRequest{
		OwnerID: request.OwnerID,
		From:    request.From,
		To:      request.To,
		Purpose: request.Purpose,
		Model:   request.Model,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("query usage: %w", err))
	}

	// Summed here rather than in a second statement. The database already collapsed the rows into
	// one group per purpose and model, so what is left is a handful of numbers — a second round
	// trip would cost more than the addition.
	result := &UsageQueryResult{Groups: groups}

	for _, group := range groups {
		result.Total.InputTokens += group.InputTokens
		result.Total.CachedInputTokens += group.CachedInputTokens
		result.Total.OutputTokens += group.OutputTokens
		result.Total.ReasoningTokens += group.ReasoningTokens
		result.Total.Attempts += group.Attempts
	}

	span.SetAttributes(
		attribute.Int("usage.groups", len(groups)),
		attribute.Int64("usage.total_attempts", result.Total.Attempts),
	)

	return otel.ReportSuccess(span, result), nil
}
