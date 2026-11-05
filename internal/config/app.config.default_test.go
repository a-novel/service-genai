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

		retention string

		expectRetention string
		expectErr       string
	}{
		{
			name:            "Success/Default",
			expectRetention: "6h0m0s",
		},
		{
			name:            "Success",
			retention:       "1h",
			expectRetention: "1h0m0s",
		},
		{
			name:      "Error/InvalidRetention",
			retention: "invalid",
			expectErr: `time: invalid duration "invalid"`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			command := exec.CommandContext(t.Context(), "go", "run", "./testdata/app")

			command.Env = append(os.Environ(),
				"GOWORK=off",
				"SERVICE_GENAI_ENV_PREFIX=TEST_",
				"RETENTION=invalid",
				"TEST_RETENTION="+testCase.retention,
			)

			output, err := command.CombinedOutput()
			if testCase.expectErr != "" {
				require.Error(t, err)
				require.Contains(t, string(output), testCase.expectErr)

				return
			}

			require.NoError(t, err, string(output))
			require.Equal(t, testCase.expectRetention, strings.TrimSpace(string(output)))
		})
	}
}
