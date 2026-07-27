package dao

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// GenerationUsage is what one attempt consumed.
//
// It carries no user content, so it outlives the generation it describes: the retention purge
// deletes the parent while this row is kept. Owner and purpose are duplicated here for that reason,
// and there is no foreign key.
//
// Provider and Model come from the provider's response, not the request. A provider may serve a
// different snapshot than the one asked for, and the model that billed is the one downstream
// pricing needs.
type GenerationUsage struct {
	bun.BaseModel `bun:"table:generation_usage,alias:generation_usage"`

	GenerationID uuid.UUID `bun:"generation_id,pk,type:uuid"`
	Attempt      int16     `bun:"attempt,pk"`

	OwnerID  uuid.UUID `bun:"owner_id,type:uuid"`
	Purpose  string    `bun:"purpose"`
	Provider string    `bun:"provider"`
	Model    string    `bun:"model"`

	// Totals include their detail counts, as the provider reports them.
	InputTokens       int64 `bun:"input_tokens"`
	CachedInputTokens int64 `bun:"cached_input_tokens"`
	OutputTokens      int64 `bun:"output_tokens"`
	ReasoningTokens   int64 `bun:"reasoning_tokens"`

	CreatedAt time.Time `bun:"created_at"`
}
