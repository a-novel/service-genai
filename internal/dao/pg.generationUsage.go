package dao

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// GenerationUsage is what one answered attempt consumed: a Usage Record.
//
// It is never purged, and holds no user content: it outlives the generation it describes, so caps
// and the long-term usage record can sum it.
type GenerationUsage struct {
	bun.BaseModel `bun:"table:generation_usage,alias:generation_usage"`

	GenerationID uuid.UUID `bun:"generation_id,pk,type:uuid"`
	Attempt      int16     `bun:"attempt,pk"`

	OwnerID  uuid.UUID `bun:"owner_id,type:uuid"`
	Purpose  string    `bun:"purpose"`
	Provider string    `bun:"provider"`
	// Model is the model snapshot that ran.
	Model string `bun:"model"`
	// Class is the reasoning effort that ran.
	Class string `bun:"class"`
	// Tier is the provider's service tier, nil when it reports none.
	Tier *string `bun:"tier,nullzero"`

	// InputTokens counts every input token, cached ones included.
	InputTokens int64 `bun:"input_tokens"`
	// CachedInputTokens are input tokens read from the provider's prompt cache.
	CachedInputTokens int64 `bun:"cached_input_tokens"`
	// CacheWriteTokens are input tokens written to the provider's prompt cache.
	CacheWriteTokens int64 `bun:"cache_write_tokens"`
	// OutputTokens counts every output token, reasoning ones included.
	OutputTokens int64 `bun:"output_tokens"`
	// ReasoningTokens are output tokens the model spent reasoning.
	ReasoningTokens int64 `bun:"reasoning_tokens"`

	// StartedAt is when the provider started the call.
	StartedAt time.Time `bun:"started_at"`
	// CompletedAt is when the provider finished the call, nil when it reports no completion.
	CompletedAt *time.Time `bun:"completed_at,nullzero"`
	CreatedAt   time.Time  `bun:"created_at"`
}
