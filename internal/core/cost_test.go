package core_test

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/models/catalog"
)

func price(input, cachedInput, output string) catalog.Price {
	return catalog.Price{
		Currency:             "USD",
		InputPerMToken:       decimal.RequireFromString(input),
		CachedInputPerMToken: decimal.RequireFromString(cachedInput),
		OutputPerMToken:      decimal.RequireFromString(output),
	}
}

func TestCostOf(t *testing.T) {
	t.Parallel()

	// The published rates for gpt-5.6-terra, so the arithmetic below checks against a real price
	// sheet rather than invented numbers.
	terra := price("2.50", "0.25", "15.00")

	testCases := []struct {
		name string

		usage core.Usage
		price catalog.Price

		expect    string
		expectErr error
	}{
		{
			// 6,000 uncached input at 2.50 -> 0.015
			// 4,000 cached input   at 0.25 -> 0.001
			// 2,000 output         at 15.00 -> 0.030
			//                                 =====
			//                                  0.046
			//
			// The naive version — full rate on all 10,000 input, reasoning as a fourth term —
			// gives 0.0625, 36% over. That gap is why this is a function and not a multiplication.
			name: "Success/CachedAndReasoning",

			usage: core.Usage{
				InputTokens:       10_000,
				CachedInputTokens: 4_000,
				OutputTokens:      2_000,
				ReasoningTokens:   500,
			},
			price: terra,

			expect: "0.046",
		},
		{
			name: "Success/NoCache",

			usage: core.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
			price: terra,

			// A million of each is exactly one unit of the quoted rate: 2.50 + 15.00.
			expect: "17.5",
		},
		{
			name: "Success/FullyCached",

			usage: core.Usage{InputTokens: 1_000_000, CachedInputTokens: 1_000_000},
			price: terra,

			expect: "0.25",
		},
		{
			// Moving tokens into reasoning must not change the price. If it does, reasoning is
			// being counted twice.
			name: "Success/ReasoningDoesNotChangeTheCost",

			usage: core.Usage{OutputTokens: 2_000, ReasoningTokens: 2_000},
			price: terra,

			expect: "0.03",
		},
		{
			name: "Success/ZeroTokens",

			usage: core.Usage{},
			price: terra,

			expect: "0",
		},
		{
			// 0.0000025 exactly, which no binary float represents. This is why it is decimal.
			name: "Success/SingleToken",

			usage: core.Usage{InputTokens: 1},
			price: terra,

			expect: "0.0000025",
		},
		{
			name: "Error/CachedExceedsInput",

			usage: core.Usage{InputTokens: 100, CachedInputTokens: 101},
			price: terra,

			expectErr: core.ErrUsageCachedExceedsInput,
		},
		{
			name: "Error/ReasoningExceedsOutput",

			usage: core.Usage{OutputTokens: 100, ReasoningTokens: 101},
			price: terra,

			expectErr: core.ErrUsageReasoningExceedsOutput,
		},
		{
			name: "Error/NegativeTokens",

			usage: core.Usage{InputTokens: -1},
			price: terra,

			expectErr: core.ErrUsageNegative,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result, err := core.CostOf(testCase.usage, testCase.price)
			require.ErrorIs(t, err, testCase.expectErr)

			if testCase.expectErr != nil {
				return
			}

			require.True(t,
				result.Equal(decimal.RequireFromString(testCase.expect)),
				"expected %s, got %s", testCase.expect, result,
			)
		})
	}
}

// Prices one call per shipped model, so the catalog and the formula are checked together.
func TestCostOfAgainstPublishedRates(t *testing.T) {
	t.Parallel()

	loaded, err := catalog.Load()
	require.NoError(t, err)

	// A million of each, so the expected cost is the published input rate plus the output rate —
	// readable off the price sheet, with no arithmetic for the test itself to get wrong.
	usage := core.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}

	for _, profile := range loaded.Profiles() {
		t.Run(profile.Name, func(t *testing.T) {
			t.Parallel()

			modelPrice, err := loaded.Price(profile.Provider, profile.Model, time.Now())
			require.NoError(t, err)

			cost, err := core.CostOf(usage, modelPrice)
			require.NoError(t, err)

			expect := modelPrice.InputPerMToken.Add(modelPrice.OutputPerMToken)
			require.True(t, cost.Equal(expect), "expected %s, got %s", expect, cost)
		})
	}
}
