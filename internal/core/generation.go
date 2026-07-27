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
	// RequestSizeCeiling bounds a submitted provider payload, mirroring the max on
	// [GenerationSubmitRequest.Request]. gRPC's own default message limit is 4 MiB; refusing earlier
	// turns a transport failure into an error a caller can act on.
	RequestSizeCeiling = 1 << 20
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
