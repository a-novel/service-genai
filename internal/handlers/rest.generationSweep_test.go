package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/config"
	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/handlers"
	handlersmocks "github.com/a-novel/service-genai/internal/handlers/mocks"
)

func TestRestGenerationSweep(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		result     *core.GenerationSweepResult
		serviceErr error

		expectStatus int
		expectBody   string
	}{
		{
			name: "Success",

			result: &core.GenerationSweepResult{Checked: 3},

			expectStatus: http.StatusOK,
			expectBody:   `{"checked":3}`,
		},
		{
			name: "Error/Internal",

			serviceErr: errFoo,

			expectStatus: http.StatusInternalServerError,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			service := handlersmocks.NewMockRestGenerationSweepService(t)
			service.EXPECT().
				Exec(mock.Anything, &core.GenerationSweepRequest{}).
				Return(testCase.result, testCase.serviceErr)

			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/sweep", nil)
			w := httptest.NewRecorder()

			handlers.NewRestGenerationSweep(service, config.LoggerDev).ServeHTTP(w, request)

			require.Equal(t, testCase.expectStatus, w.Result().StatusCode)

			if testCase.expectBody != "" {
				require.JSONEq(t, testCase.expectBody, w.Body.String())
			}
		})
	}
}
