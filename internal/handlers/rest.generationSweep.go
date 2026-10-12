package handlers

import (
	"context"
	"net/http"

	"github.com/a-novel-kit/golib/httpf"
	"github.com/a-novel-kit/golib/logging"
	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/core"
)

// RestGenerationSweepService is the service dependency of [RestGenerationSweep].
type RestGenerationSweepService interface {
	Exec(ctx context.Context, request *core.GenerationSweepRequest) (*core.GenerationSweepResult, error)
}

// RestGenerationSweep is the HTTP handler a scheduler calls at /sweep to check the generations
// nobody polled. It answers once the sweep is done, so the instance keeps its CPU while the checks
// run, and reports how many generations the sweep took.
type RestGenerationSweep struct {
	service RestGenerationSweepService
	logger  logging.Log
}

// NewRestGenerationSweep returns a new RestGenerationSweep handler backed by the given service.
func NewRestGenerationSweep(service RestGenerationSweepService, logger logging.Log) *RestGenerationSweep {
	return &RestGenerationSweep{service: service, logger: logger}
}

func (handler *RestGenerationSweep) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, span := otel.Tracer().Start(r.Context(), "rest.GenerationSweep")
	defer span.End()

	result, err := handler.service.Exec(ctx, &core.GenerationSweepRequest{})
	if err != nil {
		httpf.HandleError(ctx, handler.logger, w, span, nil, err)

		return
	}

	httpf.SendJSONStatus(ctx, w, span, http.StatusOK, map[string]int{"checked": result.Checked})
}
