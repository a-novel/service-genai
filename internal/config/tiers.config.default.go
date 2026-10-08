package config

import (
	_ "embed"
	"encoding/json"
)

//go:embed tiers.openai.json
var tiersOpenAI []byte

// TiersOpenAI binds each Tier to an OpenAI model, effort and output ceiling. The starting bindings
// come from narrative-engine's quality presets; a benchmark is what changes them.
var TiersOpenAI = mustTiers(tiersOpenAI)

func mustTiers(data []byte) map[string]TierBinding {
	var tiers map[string]TierBinding

	err := json.Unmarshal(data, &tiers)
	if err != nil {
		panic(err)
	}

	return tiers
}
