package core

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/a-novel/service-genai/internal/models/catalog"
)

// CostScale matches the ledger column, so what is computed is what is stored.
const (
	CostScale = 8

	// tokensPerMillionCount turns a token count into the unit rates are quoted in.
	tokensPerMillionCount = 1_000_000
)

var tokensPerMillion = decimal.NewFromInt(tokensPerMillionCount)

// A usage report that does not add up means the provider's numbers were mapped wrong, and a wrong
// mapping misprices silently.
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

// Usage is what a provider reported consuming for one attempt. The detail counts sit inside the
// totals, as the provider counts them: InputTokens includes CachedInputTokens, and OutputTokens
// includes ReasoningTokens.
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

// CostOf prices one attempt against the rates in force when it ran.
//
//	cost = (input - cached) x input rate
//	     +  cached          x cached rate
//	     +  output          x output rate
//
// Cached tokens are subtracted before the full rate applies, because they sit inside the input
// total and bill at a discount. Reasoning tokens get no term of their own: they sit inside the
// output total and already bill at the output rate. Either mistake misprices silently.
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
