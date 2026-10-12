package core

import (
	"errors"

	"github.com/a-novel/service-genai/internal/dao"
	"github.com/a-novel/service-genai/internal/lib/connectors"
)

// ErrGenerationNotFound is returned when the owner has no such generation. A generation owned by
// somebody else reports this too, so an identifier cannot be probed for existence.
var ErrGenerationNotFound = dao.ErrGenerationGetNotFound

// ErrUsageCapReached is returned when the user already reached a limit the submit answers to. Its
// message names the limit.
var ErrUsageCapReached = errors.New("usage cap reached")

// ErrGenerationNotStreamed is returned when a stream is asked of a generation not submitted for
// streaming.
var ErrGenerationNotStreamed = errors.New("generation not submitted for streaming")

// ErrStreamUnavailable is returned when the provider no longer streams a generation, or has not
// begun it yet: its result is read by polling.
var ErrStreamUnavailable = connectors.ErrStreamUnavailable

// ErrProviderUnknown is returned for a provider no connector serves.
var ErrProviderUnknown = connectors.ErrProviderUnknown

// ErrModeUnsupported is returned when the provider's connector does not offer the mode a request
// needs.
var ErrModeUnsupported = connectors.ErrModeUnsupported

// ErrProviderRetryable is returned when the provider failed transiently, so the same request may
// succeed again: a stream it dropped resumes after the last event received.
var ErrProviderRetryable = connectors.ErrRetryable

// ErrNotificationInvalid is returned for a notification that fails its provider's verification.
var ErrNotificationInvalid = connectors.ErrNotificationInvalid
