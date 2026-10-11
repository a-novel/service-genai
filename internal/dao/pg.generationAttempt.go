package dao

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// GenerationAttempt is the provider's answer to one attempt of a generation.
//
// It is purged with the generation it belongs to.
type GenerationAttempt struct {
	bun.BaseModel `bun:"table:generation_attempts,alias:generation_attempts"`

	GenerationID uuid.UUID `bun:"generation_id,pk,type:uuid"`
	Attempt      int16     `bun:"attempt,pk"`
	// Response is the provider's final answer, as sent: the finished response, or the error body of
	// a rejected request.
	Response json.RawMessage `bun:"response,type:json"`

	CreatedAt time.Time `bun:"created_at"`
}
