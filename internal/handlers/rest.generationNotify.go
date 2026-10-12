package handlers

import (
	"context"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/a-novel-kit/golib/httpf"
	"github.com/a-novel-kit/golib/logging"
	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/core"
)

// RestGenerationNotifyService is the service dependency of [RestGenerationNotify].
type RestGenerationNotifyService interface {
	Exec(ctx context.Context, request *core.GenerationNotifyRequest) (*core.GenerationNotifyResult, error)
}

// RestGenerationNotify is the HTTP handler a provider posts its notifications to, at
// /webhooks/{provider}.
//
// A notification handled or ignored answers 204, so the provider stops delivering it. One that fails
// verification answers 400, and a failed check 500, so the provider delivers it again.
type RestGenerationNotify struct {
	service RestGenerationNotifyService
	logger  logging.Log
}

// NewRestGenerationNotify returns a new RestGenerationNotify handler backed by the given service.
func NewRestGenerationNotify(service RestGenerationNotifyService, logger logging.Log) *RestGenerationNotify {
	return &RestGenerationNotify{service: service, logger: logger}
}

func (handler *RestGenerationNotify) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, span := otel.Tracer().Start(r.Context(), "rest.GenerationNotify")
	defer span.End()

	// The signature covers the exact bytes, so the body is read whole, within the router's limit.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpf.HandleError(ctx, handler.logger, w, span, httpf.ErrMap{nil: http.StatusBadRequest}, err)

		return
	}

	_, err = handler.service.Exec(ctx, &core.GenerationNotifyRequest{
		Provider: chi.URLParam(r, "provider"), Body: body, Header: r.Header,
	})
	if err != nil {
		httpf.HandleError(ctx, handler.logger, w, span, httpf.ErrMap{
			core.ErrNotificationInvalid: http.StatusBadRequest,
			core.ErrInvalidRequest:      http.StatusBadRequest,
			core.ErrProviderUnknown:     http.StatusNotFound,
			core.ErrModeUnsupported:     http.StatusNotFound,
		}, err)

		return
	}

	w.WriteHeader(http.StatusNoContent)
}
