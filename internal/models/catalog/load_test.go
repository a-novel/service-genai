package catalog_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/models/catalog"
)

// The refusals are the point. A catalog that loads with a hole in it runs generations it cannot
// price, and that charge is unrecoverable — so each of these stops a deployment.
func TestLoadFromRefusesBadCatalogs(t *testing.T) {
	t.Parallel()

	const (
		goodPurposes = `purposes:
  - name: studio.generation
    description: valid
`
		goodProfiles = `profiles:
  - name: standard
    provider: openai
    model: a-model
    maxOutputTokens: 1024
`
		goodPrices = `prices:
  - provider: openai
    model: a-model
    currency: USD
    effectiveFrom: 2026-01-01T00:00:00Z
    inputPerMtoken: "2.50"
    cachedInputPerMtoken: "0.25"
    outputPerMtoken: "15.00"
`
	)

	testCases := []struct {
		name string

		purposes string
		profiles string
		prices   string

		expectErr string
	}{
		{
			name:     "Success",
			purposes: goodPurposes, profiles: goodProfiles, prices: goodPrices,
		},
		{
			name:     "Error/MalformedYAML",
			purposes: "purposes: [", profiles: goodProfiles, prices: goodPrices,
			expectErr: "purposes",
		},
		{
			name: "Error/PurposeWithoutName",
			purposes: `purposes:
  - description: nameless
`,
			profiles: goodProfiles, prices: goodPrices,
			expectErr: "entry with no name",
		},
		{
			name: "Error/DuplicatePurpose",
			purposes: `purposes:
  - name: studio.generation
  - name: studio.generation
`,
			profiles: goodProfiles, prices: goodPrices,
			expectErr: "duplicate entry",
		},
		{
			name:     "Error/ProfileWithoutModel",
			purposes: goodPurposes,
			profiles: `profiles:
  - name: standard
    provider: openai
    maxOutputTokens: 1024
`,
			prices:    goodPrices,
			expectErr: "has no model",
		},
		{
			name:     "Error/ProfileWithoutOutputCeiling",
			purposes: goodPurposes,
			profiles: `profiles:
  - name: standard
    provider: openai
    model: a-model
`,
			prices:    goodPrices,
			expectErr: "has no output ceiling",
		},
		{
			// A profile whose model carries no price fails at settle, money already spent.
			name:     "Error/ProfileWithNoPrice",
			purposes: goodPurposes,
			profiles: `profiles:
  - name: standard
    provider: openai
    model: unpriced-model
    maxOutputTokens: 1024
`,
			prices:    goodPrices,
			expectErr: "the price book does not cover",
		},
		{
			name: "Error/PriceWithoutRate", purposes: goodPurposes, profiles: goodProfiles,
			prices: `prices:
  - provider: openai
    model: a-model
    currency: USD
    effectiveFrom: 2026-01-01T00:00:00Z
    inputPerMtoken: "2.50"
    outputPerMtoken: "15.00"
`,
			expectErr: "has no cachedInputPerMtoken",
		},
		{
			name: "Error/PriceWithoutEffectiveDate", purposes: goodPurposes, profiles: goodProfiles,
			prices: `prices:
  - provider: openai
    model: a-model
    currency: USD
    inputPerMtoken: "2.50"
    cachedInputPerMtoken: "0.25"
    outputPerMtoken: "15.00"
`,
			expectErr: "has no effective date",
		},
		{
			name: "Error/PriceNotANumber", purposes: goodPurposes, profiles: goodProfiles,
			prices: `prices:
  - provider: openai
    model: a-model
    currency: USD
    effectiveFrom: 2026-01-01T00:00:00Z
    inputPerMtoken: "free"
    cachedInputPerMtoken: "0.25"
    outputPerMtoken: "15.00"
`,
			expectErr: "inputPerMtoken",
		},
		{
			name: "Error/NegativePrice", purposes: goodPurposes, profiles: goodProfiles,
			prices: `prices:
  - provider: openai
    model: a-model
    currency: USD
    effectiveFrom: 2026-01-01T00:00:00Z
    inputPerMtoken: "-2.50"
    cachedInputPerMtoken: "0.25"
    outputPerMtoken: "15.00"
`,
			expectErr: "is negative",
		},
		{
			// Two rates at one instant make a call's price ambiguous, broken by map ordering.
			name: "Error/TwoRatesAtTheSameInstant", purposes: goodPurposes, profiles: goodProfiles,
			prices: `prices:
  - provider: openai
    model: a-model
    currency: USD
    effectiveFrom: 2026-01-01T00:00:00Z
    inputPerMtoken: "2.50"
    cachedInputPerMtoken: "0.25"
    outputPerMtoken: "15.00"
  - provider: openai
    model: a-model
    currency: USD
    effectiveFrom: 2026-01-01T00:00:00Z
    inputPerMtoken: "3.50"
    cachedInputPerMtoken: "0.35"
    outputPerMtoken: "21.00"
`,
			expectErr: "two entries effective",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			loaded, err := catalog.LoadFrom(
				[]byte(testCase.purposes), []byte(testCase.profiles), []byte(testCase.prices),
			)

			if testCase.expectErr == "" {
				require.NoError(t, err)
				require.NotNil(t, loaded)

				return
			}

			require.Error(t, err)
			require.Contains(t, err.Error(), testCase.expectErr)
			require.Nil(t, loaded)
		})
	}
}

// A later entry supersedes an earlier one from its effective date, never before.
func TestPriceInForceAtATime(t *testing.T) {
	t.Parallel()

	loaded, err := catalog.LoadFrom(
		[]byte("purposes:\n  - name: studio.generation\n"),
		[]byte("profiles:\n  - name: standard\n    provider: openai\n    model: a-model\n    maxOutputTokens: 1024\n"),
		[]byte(`prices:
  - provider: openai
    model: a-model
    currency: USD
    effectiveFrom: 2026-06-01T00:00:00Z
    inputPerMtoken: "4.00"
    cachedInputPerMtoken: "0.40"
    outputPerMtoken: "24.00"
  - provider: openai
    model: a-model
    currency: USD
    effectiveFrom: 2026-01-01T00:00:00Z
    inputPerMtoken: "2.50"
    cachedInputPerMtoken: "0.25"
    outputPerMtoken: "15.00"
`),
	)
	require.NoError(t, err)

	for _, testCase := range []struct {
		name   string
		at     string
		expect string
	}{
		{"BeforeAnyRate", "2025-12-31T23:59:59Z", ""},
		{"OnTheFirstRate", "2026-01-01T00:00:00Z", "2.5"},
		{"BetweenRates", "2026-05-31T23:59:59Z", "2.5"},
		{"OnTheSecondRate", "2026-06-01T00:00:00Z", "4"},
		{"AfterTheSecondRate", "2027-01-01T00:00:00Z", "4"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			at, err := time.Parse(time.RFC3339, testCase.at)
			require.NoError(t, err)

			modelPrice, err := loaded.Price("openai", "a-model", at)

			if testCase.expect == "" {
				require.ErrorIs(t, err, catalog.ErrPriceUnknown)

				return
			}

			require.NoError(t, err)
			require.Equal(t, testCase.expect, modelPrice.InputPerMToken.String())
		})
	}
}
