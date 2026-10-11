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
	// Model, Class and Tier are what actually ran: the model snapshot, the reasoning effort, and the
	// provider's service tier, nil when it reports none.
	Model string  `bun:"model"`
	Class string  `bun:"class"`
	Tier  *string `bun:"tier,nullzero"`

	// Totals include their detail counts, as the provider reports them.
	InputTokens       int64 `bun:"input_tokens"`
	CachedInputTokens int64 `bun:"cached_input_tokens"`
	CacheWriteTokens  int64 `bun:"cache_write_tokens"`
	OutputTokens      int64 `bun:"output_tokens"`
	ReasoningTokens   int64 `bun:"reasoning_tokens"`

	// StartedAt and CompletedAt are when the provider started and finished the call. CompletedAt is
	// nil when the provider reports no completion.
	StartedAt   time.Time  `bun:"started_at"`
	CompletedAt *time.Time `bun:"completed_at,nullzero"`
	CreatedAt   time.Time  `bun:"created_at"`
}
