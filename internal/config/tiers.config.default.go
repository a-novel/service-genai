package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed tiers.openai.json
var tiersOpenAI []byte

// TiersOpenAI binds each Tier to an OpenAI model, effort and token ceilings. The starting bindings
// come from narrative-engine's quality presets; a benchmark is what changes them. The input ceiling is
// each model's published maximum input, 922,000 tokens.
var TiersOpenAI = mustTiers(tiersOpenAI)

// tiersOrOpenAI decodes the bindings PROVIDER_TIERS sets, or keeps the OpenAI ones when it is unset.
func tiersOrOpenAI(raw string) map[string]TierBinding {
	if raw == "" {
		return TiersOpenAI
	}

	return mustTiers([]byte(raw))
}

// mustTiers refuses an unknown field: a misspelled one, such as the provider's own reasoning_effort,
// would otherwise drop the setting without a word.
func mustTiers(data []byte) map[string]TierBinding {
	var tiers map[string]TierBinding

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()

	err := decoder.Decode(&tiers)
	if err != nil {
		panic(fmt.Errorf("decode tier bindings: %w", err))
	}

	return tiers
}
