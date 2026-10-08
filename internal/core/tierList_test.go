package core_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/core"
	"github.com/a-novel/service-genai/internal/lib"
)

func TestTierList(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string

		bindings map[lib.Tier]lib.TierBinding

		expect []core.TierCeilings
	}{
		{
			// Ordered by capability, whatever order the bindings were configured in.
			name: "Success",

			bindings: map[lib.Tier]lib.TierBinding{
				lib.TierDeep:     {Model: "large", ReasoningEffort: "high", MaxInputTokens: 300, MaxOutputTokens: 30},
				lib.TierFast:     {Model: "small", MaxInputTokens: 100, MaxOutputTokens: 10},
				lib.TierBalanced: {Model: "medium", ReasoningEffort: "low", MaxInputTokens: 200, MaxOutputTokens: 20},
			},

			expect: []core.TierCeilings{
				{Tier: lib.TierFast, MaxInputTokens: 100, MaxOutputTokens: 10},
				{Tier: lib.TierBalanced, MaxInputTokens: 200, MaxOutputTokens: 20},
				{Tier: lib.TierDeep, MaxInputTokens: 300, MaxOutputTokens: 30},
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, testCase.expect, core.NewTierList(testCase.bindings).Exec(t.Context()))
		})
	}
}
