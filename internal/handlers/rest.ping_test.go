package handlers_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/handlers"
)

func TestRestPing(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		expectStatus int
		expectBody   string
	}{
		{
			name:         "Success",
			expectStatus: http.StatusOK,
			expectBody:   "pong",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			handler := handlers.NewRestPing()
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ping", nil))

			res := w.Result()
			require.Equal(t, testCase.expectStatus, res.StatusCode)

			body, err := io.ReadAll(res.Body)
			require.NoError(t, errors.Join(err, res.Body.Close()))
			require.Equal(t, testCase.expectBody, string(body))
		})
	}
}
