package connectors_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/lib/connectors"
)

// syncOnly is a connector without the Async mode.
type syncOnly struct{}

func (syncOnly) Call(context.Context) {}

func TestConnectors(t *testing.T) {
	t.Parallel()

	openAI, err := connectors.NewOpenAI(connectors.OpenAIConfig{BaseURL: "https://provider.test/v1", APIKey: "test-key"})
	require.NoError(t, err)

	registry := connectors.Registry{"openai": openAI, "sync-only": syncOnly{}}

	testCases := []struct {
		name string

		provider string

		expectErr error
	}{
		{
			name: "Success",

			provider: "openai",
		},
		{
			name: "Error/UnknownProvider",

			provider: "another",

			expectErr: connectors.ErrProviderUnknown,
		},
		{
			name: "Error/ModeUnsupported",

			provider: "sync-only",

			expectErr: connectors.ErrModeUnsupported,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			connector, err := registry.Async(testCase.provider)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, connector)

				return
			}

			require.Same(t, openAI, connector)
		})
	}
}
