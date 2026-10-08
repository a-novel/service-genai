package core

import (
	"context"

	"github.com/a-novel-kit/golib/otel"

	"github.com/a-novel/service-genai/internal/lib"
)

// TierCeilings is how much a caller may send to a Tier and receive from it, in tokens.
type TierCeilings struct {
	Tier            lib.Tier
	MaxInputTokens  int64
	MaxOutputTokens int64
}

// A TierList reports every Tier's token ceilings, so a caller sizes its input and its schema before
// submitting. The model and effort behind a Tier stay private: they change with the provider.
type TierList struct {
	tiers []TierCeilings
}

func NewTierList(bindings map[lib.Tier]lib.TierBinding) *TierList {
	tiers := make([]TierCeilings, 0, len(lib.Tiers))

	for _, tier := range lib.Tiers {
		tiers = append(tiers, TierCeilings{
			Tier:            tier,
			MaxInputTokens:  bindings[tier].MaxInputTokens,
			MaxOutputTokens: bindings[tier].MaxOutputTokens,
		})
	}

	return &TierList{tiers: tiers}
}

// Exec returns the Tiers from the fastest to the most capable.
func (service *TierList) Exec(ctx context.Context) []TierCeilings {
	_, span := otel.Tracer().Start(ctx, "core.TierList")
	defer span.End()

	return service.tiers
}
