package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed tiers.openai.json
var tiersOpenAI []byte

// TiersOpenAI binds each Tier to an OpenAI model, effort and token ceilings. The starting bindings
// come from narrative-engine's quality presets; a benchmark is what changes them. Every model has a
// 1,050,000-token context window, which the input ceiling shares with the output.
var TiersOpenAI = mustTiers(tiersOpenAI)

// tiersOrOpenAI decodes the bindings PROVIDER_TIERS sets, or keeps the OpenAI ones when it is unset.
func tiersOrOpenAI(raw string) map[string]TierBinding {
	if raw == "" {
		return TiersOpenAI
	}

	return mustTiers([]byte(raw))
}

func mustTiers(data []byte) map[string]TierBinding {
	var tiers map[string]TierBinding

	err := json.Unmarshal(data, &tiers)
	if err != nil {
		panic(fmt.Errorf("decode tier bindings: %w", err))
	}

	return tiers
}
