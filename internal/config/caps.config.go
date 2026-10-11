package config

import (
	"errors"
	"fmt"
	"time"
)

// DefaultTier is the user tier every cap must declare, applied when a user's tier is absent or unknown.
const DefaultTier = "default"

var (
	// ErrCapScalar reports a tier set to a number other than -1 or 0.
	ErrCapScalar = errors.New("a cap set to a number must be -1 (unlimited) or 0 (none)")
	// ErrCapWindow reports a window that is not a positive duration.
	ErrCapWindow = errors.New("a cap window must be a positive duration, such as 5h or 720h")
)

// Caps is the usage policy every submit answers to.
type Caps struct {
	// Purposes holds the caps of every purpose a caller may submit; any other is refused.
	Purposes map[string]TierCaps `json:"purposes" yaml:"purposes"`
	// Global caps one user's usage across every purpose.
	Global TierCaps `json:"global" yaml:"global"`
}

// TierCaps maps each user tier to its cap.
type TierCaps map[string]Cap

// For returns the cap of a user tier, or the default tier's when the tier is not declared.
func (caps TierCaps) For(tier string) Cap {
	capOfTier, ok := caps[tier]
	if !ok {
		return caps[DefaultTier]
	}

	return capOfTier
}

// Cap is one user tier's limits.
type Cap struct {
	// Unlimited lifts every limit, written -1 in the file.
	Unlimited bool
	// Forbidden allows no usage, written 0 in the file.
	Forbidden bool
	// Windows maps a rolling duration to the usage it allows.
	Windows map[time.Duration]Limit
}

// Limit is the usage one window allows.
type Limit struct {
	InputTokens  *int64 `json:"inputTokens"  yaml:"inputTokens"`  // Nil when the window does not limit input.
	OutputTokens *int64 `json:"outputTokens" yaml:"outputTokens"` // Nil when the window does not limit output.
}

// UnmarshalYAML reads a cap written as -1, as 0, or as a map from durations to limits.
func (capOfTier *Cap) UnmarshalYAML(unmarshal func(any) error) error {
	var scalar int

	if unmarshal(&scalar) == nil {
		switch scalar {
		case -1:
			capOfTier.Unlimited = true
		case 0:
			capOfTier.Forbidden = true
		default:
			return fmt.Errorf("%w: got %d", ErrCapScalar, scalar)
		}

		return nil
	}

	var windows map[string]Limit

	err := unmarshal(&windows)
	if err != nil {
		return fmt.Errorf("decode cap windows: %w", err)
	}

	capOfTier.Windows = make(map[time.Duration]Limit, len(windows))

	for name, limit := range windows {
		window, err := time.ParseDuration(name)
		if err != nil || window <= 0 {
			return fmt.Errorf("%w: got %q", ErrCapWindow, name)
		}

		capOfTier.Windows[window] = limit
	}

	return nil
}
