package catalog_test

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/models/catalog"
)

func TestLoad(t *testing.T) {
	t.Parallel()

	loaded, err := catalog.Load()
	require.NoError(t, err)

	require.NotEmpty(t, loaded.Purposes())
	require.NotEmpty(t, loaded.Profiles())
	require.NotEmpty(t, loaded.PriceBookVersion())
}

func TestPurpose(t *testing.T) {
	t.Parallel()

	loaded, err := catalog.Load()
	require.NoError(t, err)

	purpose, err := loaded.Purpose("studio.generation")
	require.NoError(t, err)
	require.Equal(t, "studio.generation", purpose.Name)
	require.NotEmpty(t, purpose.Description)

	// Closed: an unregistered name is a caller bug, not a new billing category.
	_, err = loaded.Purpose("studio.invented")
	require.ErrorIs(t, err, catalog.ErrPurposeUnknown)
}

func TestProfile(t *testing.T) {
	t.Parallel()

	loaded, err := catalog.Load()
	require.NoError(t, err)

	profile, err := loaded.Profile("standard")
	require.NoError(t, err)
	require.Equal(t, "openai", profile.Provider)
	require.NotEmpty(t, profile.Model)
	require.Positive(t, profile.MaxOutputTokens)

	_, err = loaded.Profile("gpt-5.6-terra")
	require.ErrorIs(t, err, catalog.ErrProfileUnknown)
}

// The invariant that matters most: a profile the price book does not cover would run, cost money,
// and fail at settle with the charge already incurred.
func TestEveryProfileIsPriceable(t *testing.T) {
	t.Parallel()

	loaded, err := catalog.Load()
	require.NoError(t, err)

	for _, profile := range loaded.Profiles() {
		t.Run(profile.Name, func(t *testing.T) {
			t.Parallel()

			modelPrice, err := loaded.Price(profile.Provider, profile.Model, time.Now())
			require.NoError(t, err)
			require.Equal(t, "USD", modelPrice.Currency)
			require.True(t, modelPrice.InputPerMToken.IsPositive())
			require.True(t, modelPrice.OutputPerMToken.IsPositive())

			// Every provider we price discounts cached input.
			require.True(t, modelPrice.CachedInputPerMToken.LessThan(modelPrice.InputPerMToken))
		})
	}
}

func TestPriceEffectiveDating(t *testing.T) {
	t.Parallel()

	loaded, err := catalog.Load()
	require.NoError(t, err)

	profile, err := loaded.Profile("standard")
	require.NoError(t, err)

	// Answering with the earliest known rate would price a call at one that did not exist yet.
	_, err = loaded.Price(profile.Provider, profile.Model, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	require.ErrorIs(t, err, catalog.ErrPriceUnknown)

	// An unknown model is a loud failure, never a free call.
	_, err = loaded.Price(profile.Provider, "no-such-model", time.Now())
	require.ErrorIs(t, err, catalog.ErrPriceUnknown)

	_, err = loaded.Price("no-such-provider", profile.Model, time.Now())
	require.ErrorIs(t, err, catalog.ErrPriceUnknown)
}

// Guards the quoting: parsed as a YAML number, 0.25 stops being 0.25.
func TestPricesAreExact(t *testing.T) {
	t.Parallel()

	loaded, err := catalog.Load()
	require.NoError(t, err)

	profile, err := loaded.Profile("standard")
	require.NoError(t, err)

	modelPrice, err := loaded.Price(profile.Provider, profile.Model, time.Now())
	require.NoError(t, err)

	require.True(t, modelPrice.InputPerMToken.Equal(decimal.RequireFromString("2.50")))
	require.True(t, modelPrice.CachedInputPerMToken.Equal(decimal.RequireFromString("0.25")))
	require.True(t, modelPrice.OutputPerMToken.Equal(decimal.RequireFromString("15.00")))
}

// The version identifies the rates rather than being a number someone must remember to bump.
func TestPriceBookVersionTracksTheFile(t *testing.T) {
	t.Parallel()

	first, err := catalog.Load()
	require.NoError(t, err)

	second, err := catalog.Load()
	require.NoError(t, err)

	require.Equal(t, first.PriceBookVersion(), second.PriceBookVersion())
	require.Regexp(t, `^sha256:[0-9a-f]{12}$`, first.PriceBookVersion())
}
