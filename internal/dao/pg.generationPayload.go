package dao

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// GenerationPayload is the caller's request for a generation. It lives apart from the generation row
// because only a start reads it, and goes with its generation when the purge deletes it.
type GenerationPayload struct {
	bun.BaseModel `bun:"table:generation_payloads,alias:generation_payloads"`

	GenerationID uuid.UUID `bun:"generation_id,pk,type:uuid"`
	// Payload is the provider's own request, built by the caller and stored as sent.
	Payload json.RawMessage `bun:"payload,type:json"`
}
