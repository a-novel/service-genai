package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/samber/lo"
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
	// Global caps one user across every purpose. Its default tier must set a concurrency, so a user
	// whose tier is unknown is always bounded.
	Global TierCaps `json:"global" yaml:"global"`
}

// Validate refuses caps a submit could not apply.
func (caps Caps) Validate() error {
	for purpose, tiers := range caps.Purposes {
		err := tiers.validate()
		if err != nil {
			return fmt.Errorf("purpose %q: %w", purpose, err)
		}
	}

	err := caps.Global.validate()
	if err != nil {
		return fmt.Errorf("global: %w", err)
	}

	// A user whose tier is unknown must always be bounded: -1 lifts the limit, but only on purpose.
	if globalDefault := caps.Global[DefaultTier]; globalDefault.Concurrency == nil &&
		!globalDefault.Unlimited && !globalDefault.Forbidden {
		return fmt.Errorf("%w: global: the %q tier sets no concurrency", ErrInvalidConfig, DefaultTier)
	}

	return nil
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

func (caps TierCaps) validate() error {
	if _, ok := caps[DefaultTier]; !ok {
		return fmt.Errorf("%w: no %q tier", ErrInvalidConfig, DefaultTier)
	}

	for tier, capOfTier := range caps {
		if capOfTier.Unlimited || capOfTier.Forbidden {
			continue
		}

		if len(capOfTier.Windows) == 0 && capOfTier.Concurrency == nil {
			return fmt.Errorf("%w: tier %q declares neither a window nor a concurrency", ErrInvalidConfig, tier)
		}

		if lo.FromPtr(capOfTier.Concurrency) < -1 {
			return fmt.Errorf("%w: tier %q allows a concurrency below -1", ErrInvalidConfig, tier)
		}

		for window, limit := range capOfTier.Windows {
			if limit.InputTokens == nil && limit.OutputTokens == nil {
				return fmt.Errorf("%w: tier %q limits nothing over %s", ErrInvalidConfig, tier, window)
			}

			if lo.FromPtr(limit.InputTokens) < 0 || lo.FromPtr(limit.OutputTokens) < 0 {
				return fmt.Errorf("%w: tier %q has a negative limit over %s", ErrInvalidConfig, tier, window)
			}
		}
	}

	return nil
}

// Cap is one user tier's limits within a scope: a purpose, or every purpose for the global caps.
type Cap struct {
	// Unlimited lifts every limit, concurrency included, written -1 in the file.
	Unlimited bool
	// Forbidden allows no usage, written 0 in the file.
	Forbidden bool
	// Concurrency is how many generations of the scope one user may have pending or running at
	// once; -1 lifts it. Nil sets no concurrency limit.
	Concurrency *int
	// Windows maps a rolling duration to the usage it allows.
	Windows map[time.Duration]Limit
}

// Limit is the usage one window allows.
type Limit struct {
	InputTokens  *int64 `json:"inputTokens"  yaml:"inputTokens"`  // Nil when the window does not limit input.
	OutputTokens *int64 `json:"outputTokens" yaml:"outputTokens"` // Nil when the window does not limit output.
}

// capFile is a cap as the file writes it when it is not -1 or 0.
type capFile struct {
	Concurrency *int             `yaml:"concurrency"`
	Windows     map[string]Limit `yaml:"windows"`
}

// UnmarshalYAML reads a cap written as -1, as 0, or as a concurrency and windows from durations to
// limits.
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

	var written capFile

	err := unmarshal(&written)
	if err != nil {
		return fmt.Errorf("decode cap: %w", err)
	}

	capOfTier.Concurrency = written.Concurrency
	capOfTier.Windows = make(map[time.Duration]Limit, len(written.Windows))

	for name, limit := range written.Windows {
		window, err := time.ParseDuration(name)
		if err != nil || window <= 0 {
			return fmt.Errorf("%w: got %q", ErrCapWindow, name)
		}

		capOfTier.Windows[window] = limit
	}

	return nil
}
