package dao

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// GenerationUsage is what one attempt consumed.
//
// It is purged with the generation it describes: callers keep long-term usage.
//
// Provider, Model and ReasoningEffort come from the provider's response, not the request. A provider
// may serve a different snapshot or effort than the one asked for.
type GenerationUsage struct {
	bun.BaseModel `bun:"table:generation_usage,alias:generation_usage"`

	GenerationID uuid.UUID `bun:"generation_id,pk,type:uuid"`
	Attempt      int16     `bun:"attempt,pk"`

	OwnerID  uuid.UUID `bun:"owner_id,type:uuid"`
	Purpose  string    `bun:"purpose"`
	Provider string    `bun:"provider"`
	Model    string    `bun:"model"`
	// ReasoningEffort is absent for a model without one.
	ReasoningEffort *string `bun:"reasoning_effort,nullzero"`

	// Totals include their detail counts, as the provider reports them.
	InputTokens       int64 `bun:"input_tokens"`
	CachedInputTokens int64 `bun:"cached_input_tokens"`
	OutputTokens      int64 `bun:"output_tokens"`
	ReasoningTokens   int64 `bun:"reasoning_tokens"`

	CreatedAt time.Time `bun:"created_at"`
}
