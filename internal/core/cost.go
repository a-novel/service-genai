package core

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/a-novel/service-genai/internal/models/catalog"
)

// CostScale is the number of decimal places a cost is rounded to. It matches the ledger column, so
// what is computed is what is stored and a re-computation reproduces the row exactly.
const (
	CostScale = 8

	// tokensPerMillionCount is the divisor that turns a token count into the unit rates are quoted
	// in.
	tokensPerMillionCount = 1_000_000
)

// tokensPerMillion converts a token count to the unit prices are quoted in.
var tokensPerMillion = decimal.NewFromInt(tokensPerMillionCount)

// Errors reported when usage cannot describe a real call. Each is a loud failure: a usage report
// that does not add up means the provider's numbers were mapped wrong, and a wrong mapping
// misprices silently for as long as it goes unnoticed.
var (
	// ErrUsageNegative is returned when any token count is below zero.
	ErrUsageNegative = errors.New("token count is negative")
	// ErrUsageCachedExceedsInput is returned when more input tokens were served from cache than
	// were sent.
	ErrUsageCachedExceedsInput = errors.New("cached input tokens exceed input tokens")
	// ErrUsageReasoningExceedsOutput is returned when more output tokens were spent reasoning than
	// were produced.
	ErrUsageReasoningExceedsOutput = errors.New("reasoning tokens exceed output tokens")
)

// Usage is what a provider reported consuming for one attempt.
//
// The two detail counts are subsets of the totals beside them, mirroring the provider's own
// accounting rather than correcting it: InputTokens already includes CachedInputTokens, and
// OutputTokens already includes ReasoningTokens.
type Usage struct {
	InputTokens       int64
	CachedInputTokens int64
	OutputTokens      int64
	ReasoningTokens   int64
}

// Validate reports whether the usage describes a call that could have happened.
func (usage Usage) Validate() error {
	switch {
	case usage.InputTokens < 0, usage.CachedInputTokens < 0,
		usage.OutputTokens < 0, usage.ReasoningTokens < 0:
		return ErrUsageNegative
	case usage.CachedInputTokens > usage.InputTokens:
		return fmt.Errorf("%w: %d of %d", ErrUsageCachedExceedsInput, usage.CachedInputTokens, usage.InputTokens)
	case usage.ReasoningTokens > usage.OutputTokens:
		return fmt.Errorf("%w: %d of %d", ErrUsageReasoningExceedsOutput, usage.ReasoningTokens, usage.OutputTokens)
	}

	return nil
}

// CostOf prices one attempt's usage against the rates in force when it ran.
//
//	cost = (input - cached) x input rate
//	     +  cached          x cached rate
//	     +  output          x output rate
//
// Two properties of the provider's accounting drive the shape, and getting either wrong is a silent
// billing error rather than a failure:
//
// Cached input tokens are counted inside the input total and billed at a lower rate, so they are
// subtracted before the full rate applies. Charging the whole input total at the full rate
// overcharges every call that hit the cache.
//
// Reasoning tokens are counted inside the output total and billed at the output rate. They are
// recorded for visibility and never priced as a fourth term — adding one would charge for them
// twice.
func CostOf(usage Usage, price catalog.Price) (decimal.Decimal, error) {
	err := usage.Validate()
	if err != nil {
		return decimal.Decimal{}, err
	}

	uncachedInput := decimal.NewFromInt(usage.InputTokens - usage.CachedInputTokens)
	cachedInput := decimal.NewFromInt(usage.CachedInputTokens)
	output := decimal.NewFromInt(usage.OutputTokens)

	total := uncachedInput.Mul(price.InputPerMToken).
		Add(cachedInput.Mul(price.CachedInputPerMToken)).
		Add(output.Mul(price.OutputPerMToken)).
		Div(tokensPerMillion)

	return total.Round(CostScale), nil
}
