package config_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppPresetDefault(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		checkInterval string

		expectCheckInterval string
		expectErr           string
	}{
		{
			name:                "Success/Default",
			expectCheckInterval: "2s",
		},
		{
			name:                "Success",
			checkInterval:       "5s",
			expectCheckInterval: "5s",
		},
		{
			name:          "Error/InvalidCheckInterval",
			checkInterval: "invalid",
			expectErr:     `time: invalid duration "invalid"`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			command := exec.CommandContext(t.Context(), "go", "run", "./testdata/app")

			command.Env = append(os.Environ(),
				"GOWORK=off",
				"SERVICE_GENAI_ENV_PREFIX=TEST_",
				"CHECK_INTERVAL=invalid",
				"TEST_CHECK_INTERVAL="+testCase.checkInterval,
			)

			output, err := command.CombinedOutput()
			if testCase.expectErr != "" {
				require.Error(t, err)
				require.Contains(t, string(output), testCase.expectErr)

				return
			}

			require.NoError(t, err, string(output))
			require.Equal(t, testCase.expectCheckInterval, strings.TrimSpace(string(output)))
		})
	}
}
