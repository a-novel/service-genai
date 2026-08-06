package core

import (
	"errors"
	"time"
)

// Ceilings the worker configuration is held to. They live here because validation is this layer's
// responsibility; the data access below takes what it is given.
//
// Request-shape ceilings are expressed as validate tags on the request structs instead, since the
// tag syntax takes literals.
const (
	// ClaimLeaseCeiling bounds a worker's lease. Longer than this outlives any generation we run,
	// so a stranded claim would sit invisible for an hour.
	ClaimLeaseCeiling = time.Hour
	// ClaimLimitCeiling bounds one claim, so a single worker cannot take the whole queue.
	ClaimLimitCeiling = 100
	// RequestSizeCeiling bounds a submitted provider payload. It estimates OpenAI's 922,000-token
	// maximum input using the rough English heuristic of four characters per token and one byte per
	// ASCII character, leaving the model's separate 128,000-token output allowance untouched.
	// Tokenization and UTF-8 width vary, so this is a transport and storage bound, not a promise that
	// every payload below it fits every model.
	//
	// The ceiling also remains below gRPC's default 4 MiB receive limit, leaving room for the
	// protobuf envelope so an oversized request reaches application validation.
	RequestSizeCeiling = 3_688_000
)

// Errors a caller can act on. Everything else is a fault.
var (
	// ErrGenerationNotFound is returned when the owner has no such generation. A generation owned by
	// somebody else reports this too, so an identifier cannot be probed for existence.
	ErrGenerationNotFound = errors.New("generation not found")
	// ErrGenerationNotCancellable is returned when a generation cannot be stopped, because it does
	// not exist for this owner or has already settled.
	ErrGenerationNotCancellable = errors.New("generation cannot be cancelled")
	// ErrIdempotencyConflict is returned when an idempotency key is reused with a different request.
	ErrIdempotencyConflict = errors.New("idempotency key already used with a different request")
)
