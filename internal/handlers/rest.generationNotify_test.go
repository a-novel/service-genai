package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/handlers"
	handlersmocks "github.com/a-novel/service-genai/internal/handlers/mocks"
	"github.com/a-novel/service-genai/internal/lib/connectors"
)

func TestRestGenerationNotify(t *testing.T) {
	t.Parallel()

	const body = `{"type": "response.completed"}`

	testCases := []struct {
		name string

		serviceErr error

		expectStatus int
	}{
		{
			name: "Success",

			expectStatus: http.StatusNoContent,
		},
		{
			name: "Error/Invalid",

			serviceErr: connectors.ErrNotificationInvalid,

			expectStatus: http.StatusBadRequest,
		},
		{
			name: "Error/InvalidRequest",

			serviceErr: core.ErrInvalidRequest,

			expectStatus: http.StatusBadRequest,
		},
		{
			name: "Error/UnknownProvider",

			serviceErr: connectors.ErrProviderUnknown,

			expectStatus: http.StatusNotFound,
		},
		{
			name: "Error/ModeUnsupported",

			serviceErr: connectors.ErrModeUnsupported,

			expectStatus: http.StatusNotFound,
		},
		{
			// The provider delivers the notification again.
			name: "Error/Internal",

			serviceErr: errFoo,

			expectStatus: http.StatusInternalServerError,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/webhooks/openai", strings.NewReader(body))
			request.Header.Set("Webhook-Id", "evt_1")

			service := handlersmocks.NewMockRestGenerationNotifyService(t)
			service.EXPECT().
				Exec(mock.Anything, &core.GenerationNotifyRequest{
					Provider: "openai", Body: []byte(body), Header: request.Header,
				}).
				Return(&core.GenerationNotifyResult{}, testCase.serviceErr)

			// The provider is read from the route, as the server mounts it.
			router := chi.NewRouter()
			router.Post("/webhooks/{provider}", handlers.NewRestGenerationNotify(service, config.LoggerDev).ServeHTTP)

			w := httptest.NewRecorder()
			router.ServeHTTP(w, request)

			require.Equal(t, testCase.expectStatus, w.Result().StatusCode)
		})
	}
}
