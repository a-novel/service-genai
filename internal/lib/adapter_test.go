package lib_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/lib"
)

// syncOnly is an adapter without the Async mode.
type syncOnly struct{}

func (syncOnly) Call(context.Context) {}

func TestAdapters(t *testing.T) {
	t.Parallel()

	openAI, err := lib.NewOpenAI(lib.OpenAIConfig{BaseURL: "https://provider.test/v1", APIKey: "test-key"})
	require.NoError(t, err)

	adapters := lib.Adapters{"openai": openAI, "sync-only": syncOnly{}}

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

			expectErr: lib.ErrProviderUnknown,
		},
		{
			name: "Error/ModeUnsupported",

			provider: "sync-only",

			expectErr: lib.ErrModeUnsupported,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			adapter, err := adapters.Async(testCase.provider)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				require.Nil(t, adapter)

				return
			}

			require.Same(t, openAI, adapter)
		})
	}
}
