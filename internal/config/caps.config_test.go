package config_test

import (
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/config"
)

func TestCap(t *testing.T) {
	t.Parallel()

	inputLimit, outputLimit, concurrency := int64(100), int64(10), 2

	testCases := []struct {
		name string

		document string

		expect    config.Cap
		expectErr error
		// expectFailure is a decoding error with no sentinel of its own.
		expectFailure bool
	}{
		{
			name: "Success/Unlimited",

			document: `-1`,

			expect: config.Cap{Unlimited: true},
		},
		{
			name: "Success/Forbidden",

			document: `0`,

			expect: config.Cap{Forbidden: true},
		},
		{
			name: "Success/Windows",

			document: `
concurrency: 2
windows:
  5h:
    inputTokens: 100
    outputTokens: 10
  720h:
    outputTokens: 10
`,

			expect: config.Cap{Concurrency: &concurrency, Windows: map[time.Duration]config.Limit{
				5 * time.Hour:   {InputTokens: &inputLimit, OutputTokens: &outputLimit},
				720 * time.Hour: {OutputTokens: &outputLimit},
			}},
		},
		{
			// A cap may bound only how many generations run at once.
			name: "Success/ConcurrencyOnly",

			document: `concurrency: 2`,

			expect: config.Cap{Concurrency: &concurrency, Windows: map[time.Duration]config.Limit{}},
		},
		{
			// Windows written straight under the tier, as before, are unknown keys rather than limits.
			name: "Error/WindowsNotNested",

			document: `
5h:
  inputTokens: 100
`,

			expectFailure: true,
		},
		{
			name: "Error/OtherNumber",

			document: `5`,

			expectErr: config.ErrCapScalar,
		},
		{
			// Go durations stop at hours: a day is 24h.
			name: "Error/NotADuration",

			document: `
windows:
  1d:
    inputTokens: 100
`,

			expectErr: config.ErrCapWindow,
		},
		{
			name: "Error/NegativeWindow",

			document: `
windows:
  -5h:
    inputTokens: 100
`,

			expectErr: config.ErrCapWindow,
		},
		{
			// A misspelled limit must fail rather than leave the window unlimited.
			name: "Error/UnknownLimit",

			document: `
windows:
  5h:
    inputToken: 100
`,

			expectFailure: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var decoded config.Cap

			err := yaml.UnmarshalWithOptions([]byte(testCase.document), &decoded, yaml.DisallowUnknownField())

			switch {
			case testCase.expectFailure:
				require.Error(t, err)
			case testCase.expectErr != nil:
				require.ErrorIs(t, err, testCase.expectErr)
			default:
				require.NoError(t, err)
				require.Equal(t, testCase.expect, decoded)
			}
		})
	}
}

func TestTierCaps(t *testing.T) {
	t.Parallel()

	caps := config.TierCaps{config.DefaultTier: {Forbidden: true}, "admin": {Unlimited: true}}

	require.Equal(t, config.Cap{Unlimited: true}, caps.For("admin"))
	// An unrecognized or absent tier answers to the default caps.
	require.Equal(t, config.Cap{Forbidden: true}, caps.For("premium"))
	require.Equal(t, config.Cap{Forbidden: true}, caps.For(""))
}
