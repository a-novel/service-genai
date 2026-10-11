package core

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
)

// GenerationNotifyDao finds the running generation carrying a provider call.
type GenerationNotifyDao interface {
	Exec(ctx context.Context, request *dao.GenerationGetByCallRequest) (*dao.Generation, error)
}

// GenerationNotifyServiceCheck checks the generation a notification names.
type GenerationNotifyServiceCheck interface {
	Exec(ctx context.Context, request *GenerationCheckRequest) (*dao.Generation, error)
}

// GenerationNotifyRequest holds the parameters for a [GenerationNotify.Exec] call.
type GenerationNotifyRequest struct {
	// Provider names the service that sent the notification.
	Provider string `validate:"required,max=255"`
	// Body is the notification's body, as received.
	Body []byte `validate:"required"`
	// Header holds the notification's headers, which carry its signature.
	Header map[string][]string
}

// GenerationNotifyResult reports what a notification led to.
type GenerationNotifyResult struct {
	// Checked is false for a notification that names nothing this service follows.
	Checked bool
}

// A GenerationNotify checks the generation a provider's notification names, the moment the provider
// reports its call ended, instead of when a poll or the sweep next asks.
//
// Checks are fenced, so a notification delivered twice or out of order changes nothing.
type GenerationNotify struct {
	dao          GenerationNotifyDao
	serviceCheck GenerationNotifyServiceCheck
	registry     connectors.Registry
}

// NewGenerationNotify returns a new GenerationNotify service.
func NewGenerationNotify(
	dao GenerationNotifyDao, serviceCheck GenerationNotifyServiceCheck, registry connectors.Registry,
) *GenerationNotify {
	return &GenerationNotify{dao: dao, serviceCheck: serviceCheck, registry: registry}
}

func (service *GenerationNotify) Exec(
	ctx context.Context, request *GenerationNotifyRequest,
) (*GenerationNotifyResult, error) {
	ctx, span := otel.Tracer().Start(ctx, "core.GenerationNotify")
	defer span.End()

	span.SetAttributes(attribute.String("generation.provider", request.Provider))

	err := validate.Struct(request)
	if err != nil {
		return nil, otel.ReportError(span, errors.Join(err, ErrInvalidRequest))
	}

	notifications, err := service.registry.Notifications(request.Provider)
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("provider %q: %w", request.Provider, err))
	}

	notification, err := notifications.Notification(ctx, &connectors.NotificationRequest{
		Body: request.Body, Header: request.Header,
	})
	if errors.Is(err, connectors.ErrNotificationIgnored) {
		return &GenerationNotifyResult{}, nil
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("read notification: %w", err))
	}

	generation, err := service.dao.Exec(ctx, &dao.GenerationGetByCallRequest{
		Provider: request.Provider, CallID: notification.CallID,
	})
	// A call no generation runs: settled already, purged, or started by another user of the provider
	// account.
	if errors.Is(err, dao.ErrGenerationGetByCallNotFound) {
		return &GenerationNotifyResult{}, nil
	}

	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("find generation: %w", err))
	}

	span.SetAttributes(attribute.String("generation.id", generation.ID.String()))

	_, err = service.serviceCheck.Exec(ctx, &GenerationCheckRequest{Generation: generation})
	if err != nil {
		return nil, otel.ReportError(span, fmt.Errorf("check generation: %w", err))
	}

	return &GenerationNotifyResult{Checked: true}, nil
}
