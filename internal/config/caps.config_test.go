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

			expect: config.Cap{Concurrency: new(-1)},
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
			// Windows written straight under the tier are unknown keys, not limits.
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
			// One duration written twice: keeping either would silently drop the other's limit.
			name: "Error/RepeatedWindow",

			document: `
windows:
  1h:
    inputTokens: 100
  60m:
    outputTokens: 10
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

func TestCaps(t *testing.T) {
	t.Parallel()

	someLimit := map[time.Duration]config.Limit{time.Hour: {InputTokens: new(int64(1))}}
	someTiers := map[string]config.TierCaps{"studio.generation": {config.DefaultTier: {Windows: someLimit}}}
	someGlobal := config.TierCaps{config.DefaultTier: {Concurrency: new(1), Windows: someLimit}}

	testCases := []struct {
		name string

		caps config.Caps

		expectErr error
	}{
		{
			// The caps built into the image must always pass, or the service cannot start.
			name: "Success/Embedded",

			caps: config.CapsDefault,
		},
		{
			name: "Success/ConcurrencyOnly",

			caps: config.Caps{
				Purposes: map[string]config.TierCaps{"studio.generation": {config.DefaultTier: {Concurrency: new(1)}}},
				Global:   config.TierCaps{config.DefaultTier: {Concurrency: new(-1)}},
			},
		},
		{
			// Lifting every global limit for an unknown tier is a deliberate policy, not an omission.
			name: "Success/GlobalDefaultUnlimited",

			caps: config.Caps{Purposes: someTiers, Global: config.TierCaps{config.DefaultTier: {Concurrency: new(-1)}}},
		},
		{
			name: "Error/PurposeWithoutDefault",

			caps: config.Caps{
				Purposes: map[string]config.TierCaps{"studio.generation": {"admin": {Concurrency: new(-1)}}},
				Global:   someGlobal,
			},

			expectErr: config.ErrInvalidConfig,
		},
		{
			name: "Error/GlobalWithoutDefault",

			caps: config.Caps{Purposes: someTiers},

			expectErr: config.ErrInvalidConfig,
		},
		{
			// An unknown tier would run as many generations at once as it likes.
			name: "Error/GlobalDefaultWithoutConcurrency",

			caps: config.Caps{Purposes: someTiers, Global: config.TierCaps{config.DefaultTier: {Windows: someLimit}}},

			expectErr: config.ErrInvalidConfig,
		},
		{
			// An empty map is neither unlimited nor forbidden: a typo, not a policy.
			name: "Error/NeitherWindowNorConcurrency",

			caps: config.Caps{
				Purposes: map[string]config.TierCaps{"studio.generation": {config.DefaultTier: {}}},
				Global:   someGlobal,
			},

			expectErr: config.ErrInvalidConfig,
		},
		{
			name: "Error/LimitsNothing",

			caps: config.Caps{
				Purposes: map[string]config.TierCaps{"studio.generation": {config.DefaultTier: {
					Windows: map[time.Duration]config.Limit{time.Hour: {}},
				}}},
				Global: someGlobal,
			},

			expectErr: config.ErrInvalidConfig,
		},
		{
			name: "Error/NegativeLimit",

			caps: config.Caps{
				Purposes: map[string]config.TierCaps{"studio.generation": {config.DefaultTier: {
					Windows: map[time.Duration]config.Limit{time.Hour: {OutputTokens: new(int64(-1))}},
				}}},
				Global: someGlobal,
			},

			expectErr: config.ErrInvalidConfig,
		},
		{
			name: "Error/ConcurrencyBelowUnlimited",

			caps: config.Caps{
				Purposes: map[string]config.TierCaps{"studio.generation": {config.DefaultTier: {Concurrency: new(-2)}}},
				Global:   someGlobal,
			},

			expectErr: config.ErrInvalidConfig,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, testCase.caps.Validate(), testCase.expectErr)
		})
	}
}

func TestTierCaps(t *testing.T) {
	t.Parallel()

	caps := config.TierCaps{config.DefaultTier: {Forbidden: true}, "admin": {Concurrency: new(-1)}}

	require.Equal(t, config.Cap{Concurrency: new(-1)}, caps.For("admin"))
	// An unrecognized or absent tier answers to the default caps.
	require.Equal(t, config.Cap{Forbidden: true}, caps.For("premium"))
	require.Equal(t, config.Cap{Forbidden: true}, caps.For(""))
}
