package config

import (
	"errors"
	"fmt"
	"time"
)

// DefaultTier is the user tier every cap must declare. It applies to a user whose tier is absent or
// unrecognized.
const DefaultTier = "default"

var (
	// ErrCapScalar reports a tier set to a number other than -1 or 0.
	ErrCapScalar = errors.New("a cap set to a number must be -1 (unlimited) or 0 (none)")
	// ErrCapWindow reports a window that is not a positive duration.
	ErrCapWindow = errors.New("a cap window must be a positive duration, such as 5h or 720h")
)

// Caps is the usage policy: the caps of every declared purpose, and the global caps one user's
// usage across every purpose answers to. A purpose missing from it cannot be submitted.
type Caps struct {
	Purposes map[string]TierCaps `json:"purposes" yaml:"purposes"`
	Global   TierCaps            `json:"global"   yaml:"global"`
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

// Cap is one user tier's limits: rolling windows mapped to token limits. In the file, the number -1
// lifts every limit and 0 allows no usage.
type Cap struct {
	Unlimited bool
	Forbidden bool
	// Windows maps a rolling duration to the usage it allows.
	Windows map[time.Duration]Limit
}

// Limit is the usage one window allows. A limit left out does not apply.
type Limit struct {
	InputTokens  *int64 `json:"inputTokens"  yaml:"inputTokens"`
	OutputTokens *int64 `json:"outputTokens" yaml:"outputTokens"`
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
