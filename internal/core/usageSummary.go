package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/dao"
)

// UsageSummaryDaoCountLive counts a user's generations in flight.
type UsageSummaryDaoCountLive interface {
	Exec(ctx context.Context, request *dao.GenerationCountLiveRequest) (*dao.GenerationLiveCount, error)
}

// UsageSummaryDaoUsageSum adds up a user's usage over each window.
type UsageSummaryDaoUsageSum interface {
	Exec(ctx context.Context, request *dao.GenerationUsageSumRequest) ([]*dao.GenerationUsageTotals, error)
}

// UsageSummaryRequest holds the parameters for a [UsageSummary.Exec] call.
type UsageSummaryRequest struct {
	// OwnerID is the user to report on.
	OwnerID uuid.UUID `validate:"required"`
	// Purpose is the purpose to report on. It must be declared.
	Purpose string `validate:"required,notblank,max=255"`
	// Windows are how far back each sum reaches, within the bounds of a caps window.
	Windows []time.Duration `validate:"max=10,unique,dive,gt=0,lte=8760h"`
}

// UsageSummaryResult is a user's usage of one purpose.
type UsageSummaryResult struct {
	// Live is how many generations of the purpose the user has in flight, as a submit's concurrency
	// counts them.
	Live int64
	// Windows holds the tokens consumed over each requested window, in the request's order.
	Windows []*UsageSummaryWindow
}

// UsageSummaryWindow is what a user consumed for a purpose over one rolling window.
type UsageSummaryWindow struct {
	// Duration is how far back the window reaches.
	Duration time.Duration

	// InputTokens counts the input tokens, cached ones included.
	InputTokens int64
	// OutputTokens counts the output tokens, reasoning ones included.
	OutputTokens int64
}

// A UsageSummary reports what one user consumes of a purpose, counted the way a submit's caps count
// it, so a caller shows its user the room left under the caps it sends.
type UsageSummary struct {
	daoCountLive UsageSummaryDaoCountLive
	daoUsageSum  UsageSummaryDaoUsageSum
	purposes     []string
}

// NewUsageSummary returns a new UsageSummary service.
func NewUsageSummary(
	daoCountLive UsageSummaryDaoCountLive, daoUsageSum UsageSummaryDaoUsageSum, purposes []string,
) *UsageSummary {
	return &UsageSummary{daoCountLive: daoCountLive, daoUsageSum: daoUsageSum, purposes: purposes}
}

func (service *UsageSummary) Exec(ctx context.Context, request *UsageSummaryRequest) (*UsageSummaryResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.UsageSummary")
	defer span.End()

	span.SetAttributes(
		attribute.String("generation.owner_id", request.OwnerID.String()),
		attribute.String("generation.purpose", request.Purpose),
	)

	err := validate.Struct(request)
	if err != nil {
		return nil, otel.ReportError(span, errors.Join(err, ErrInvalidRequest))
	}

	if !slices.Contains(service.purposes, request.Purpose) {
		return nil, otel.ReportError(span, fmt.Errorf("%w: purpose %q is not declared", ErrInvalidRequest, request.Purpose))
	}

	live, err := service.daoCountLive.Exec(ctx, &dao.GenerationCountLiveRequest{
		OwnerID: request.OwnerID, Purpose: &request.Purpose, UnknownOutcomeWindow: config.UnknownOutcomeHold,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("count live generations: %w", err))
	}

	result := &UsageSummaryResult{Live: live.Count, Windows: make([]*UsageSummaryWindow, len(request.Windows))}

	if len(request.Windows) == 0 {
		return result, nil
	}

	used, err := service.daoUsageSum.Exec(ctx, &dao.GenerationUsageSumRequest{
		OwnerID: request.OwnerID, Purpose: &request.Purpose, Windows: request.Windows,
	})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("sum usage: %w", err))
	}

	for index, window := range request.Windows {
		result.Windows[index] = &UsageSummaryWindow{
			Duration: window, InputTokens: used[index].InputTokens, OutputTokens: used[index].OutputTokens,
		}
	}

	return result, nil
}
