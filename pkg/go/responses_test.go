package servicegenai_test

import (
	_ "embed"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	servicegenai "github.com/a-novel/service-genai/pkg/go"
)

//go:embed testdata/responses.yaml
var responsesFixtures []byte

func TestExtractResponsesOutputText(t *testing.T) {
	t.Parallel()

	var testCases []struct {
		Name string `yaml:"name"`

		Output any     `yaml:"output"`
		Raw    *string `yaml:"raw"`

		Expect    string `yaml:"expect"`
		ExpectErr string `yaml:"expect_error"`
	}

	require.NoError(t, yaml.Unmarshal(responsesFixtures, &testCases))

	errorsByName := map[string]error{
		"empty":     servicegenai.ErrResponsesOutputEmpty,
		"malformed": servicegenai.ErrResponsesOutputMalformed,
		"missing":   servicegenai.ErrResponsesOutputTextMissing,
		"refused":   servicegenai.ErrResponsesRefused,
	}

	for _, testCase := range testCases {
		t.Run(testCase.Name, func(t *testing.T) {
			t.Parallel()

			var output []byte

			if testCase.Raw != nil {
				output = []byte(*testCase.Raw)
			} else if testCase.Output != nil {
				var err error

				output, err = json.Marshal(testCase.Output)
				require.NoError(t, err)
			}

			expectErr, knownError := errorsByName[testCase.ExpectErr]
			if testCase.ExpectErr != "" {
				require.True(t, knownError, "unknown fixture error %q", testCase.ExpectErr)
			}

			result, err := servicegenai.ExtractResponsesOutputText(output)

			require.ErrorIs(t, err, expectErr)
			require.Equal(t, testCase.Expect, result)
		})
	}
}
