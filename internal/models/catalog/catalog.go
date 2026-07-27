// Package catalog holds the three closed vocabularies that turn an abstract request into a priced
// call: the purposes a generation may be billed to, the profiles it may run under, and what each
// model costs.
//
// All three are YAML embedded at build time and parsed at boot, so a price change is a reviewed
// commit with a git history.
package catalog

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gopkg.in/yaml.v3"
)

// priceBookVersionLength is enough to be unique across a price book's revisions, short enough to
// read in a ledger row.
const priceBookVersionLength = 12

//go:embed purposes.yaml
var purposesFile []byte

//go:embed profiles.yaml
var profilesFile []byte

//go:embed prices.yaml
var pricesFile []byte

// A missing price must never resolve to a free call, and an unregistered purpose must never become
// a billing category by accident, so every miss is an error.
var (
	// ErrPurposeUnknown is returned when no registered purpose carries the requested name.
	ErrPurposeUnknown = errors.New("unknown purpose")
	// ErrProfileUnknown is returned when no registered profile carries the requested name.
	ErrProfileUnknown = errors.New("unknown profile")
	// ErrPriceUnknown is returned when the price book has no entry in force for a provider and
	// model at the requested time.
	ErrPriceUnknown = errors.New("no price in force for provider and model")
	// ErrCatalogInvalid wraps every parse and validation failure, so a caller branches on the class
	// rather than on a message.
	ErrCatalogInvalid = errors.New("invalid catalog")
)

// Purpose is one entry in the closed vocabulary of why the platform spent money on a generation.
type Purpose struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Profile is how a generation runs, resolved to a provider, a model and its parameters.
//
// Callers name a profile and never a model, so a model can be swapped without any caller changing,
// and no caller can request an expensive one or raise its own ceiling.
type Profile struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Provider    string `yaml:"provider"`
	Model       string `yaml:"model"`
	// MaxOutputTokens is the ceiling. A request may ask for less, never more.
	MaxOutputTokens int `yaml:"maxOutputTokens"`
	// ReasoningEffort is passed to providers that accept one. Empty leaves their default.
	ReasoningEffort string `yaml:"reasoningEffort"`
}

// Price is what one model costs over a period, per million tokens — the unit providers publish, so
// a stored rate reads against a public price sheet directly.
type Price struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Currency string `yaml:"currency"`
	// EffectiveFrom is when this rate came into force. The one in force at a moment is the latest
	// at or before it.
	EffectiveFrom time.Time `yaml:"effectiveFrom"`

	// Quoted strings, because a bare YAML number is a float and would round the rate before use.
	InputPerMTokenRaw       string `yaml:"inputPerMtoken"`
	CachedInputPerMTokenRaw string `yaml:"cachedInputPerMtoken"`
	OutputPerMTokenRaw      string `yaml:"outputPerMtoken"`

	InputPerMToken       decimal.Decimal `yaml:"-"`
	CachedInputPerMToken decimal.Decimal `yaml:"-"`
	OutputPerMToken      decimal.Decimal `yaml:"-"`
}

// Catalog is the parsed set of all three vocabularies.
type Catalog struct {
	purposes map[string]Purpose
	profiles map[string]Profile
	// Oldest first, so a lookup walks backwards to the entry in force.
	prices map[priceKey][]Price

	priceBookVersion string
}

type priceKey struct {
	provider string
	model    string
}

type purposesDocument struct {
	Purposes []Purpose `yaml:"purposes"`
}

type profilesDocument struct {
	Profiles []Profile `yaml:"profiles"`
}

type pricesDocument struct {
	Prices []Price `yaml:"prices"`
}

// Load parses the embedded catalogs. It is what the service calls at boot.
func Load() (*Catalog, error) {
	return LoadFrom(purposesFile, profilesFile, pricesFile)
}

// LoadFrom parses catalogs from raw YAML.
//
// It fails rather than degrades: a service that booted with a hole in its price book would run
// generations it cannot price, and that charge is unrecoverable. Taking bytes is what lets the
// refusals be tested; [Load] is the entry point everything else uses.
func LoadFrom(purposesYAML, profilesYAML, pricesYAML []byte) (*Catalog, error) {
	purposes, err := loadPurposes(purposesYAML)
	if err != nil {
		return nil, err
	}

	profiles, err := loadProfiles(profilesYAML)
	if err != nil {
		return nil, err
	}

	prices, err := loadPrices(pricesYAML)
	if err != nil {
		return nil, err
	}

	catalog := &Catalog{
		purposes: purposes,
		profiles: profiles,
		prices:   prices,
		// A digest rather than a hand-maintained number, so it cannot drift from the rates it names.
		priceBookVersion: priceBookVersion(pricesYAML),
	}

	err = catalog.validate()
	if err != nil {
		return nil, err
	}

	return catalog, nil
}

// Purpose returns the registered purpose with this name.
func (catalog *Catalog) Purpose(name string) (Purpose, error) {
	purpose, found := catalog.purposes[name]
	if !found {
		return Purpose{}, fmt.Errorf("%w: %q", ErrPurposeUnknown, name)
	}

	return purpose, nil
}

// Profile returns the registered profile with this name.
func (catalog *Catalog) Profile(name string) (Profile, error) {
	profile, found := catalog.profiles[name]
	if !found {
		return Profile{}, fmt.Errorf("%w: %q", ErrProfileUnknown, name)
	}

	return profile, nil
}

// Price returns the rate in force at a moment: the latest entry effective at or before it. Taking
// the time is what lets a generation that ran yesterday be priced with yesterday's rate.
func (catalog *Catalog) Price(provider, model string, at time.Time) (Price, error) {
	entries := catalog.prices[priceKey{provider: provider, model: model}]

	for index := len(entries) - 1; index >= 0; index-- {
		if !entries[index].EffectiveFrom.After(at) {
			return entries[index], nil
		}
	}

	return Price{}, fmt.Errorf("%w: %s/%s at %s", ErrPriceUnknown, provider, model, at.Format(time.RFC3339))
}

// PriceBookVersion traces a disputed ledger row to the commit that set its rates.
func (catalog *Catalog) PriceBookVersion() string {
	return catalog.priceBookVersion
}

// Purposes returns every registered purpose, ordered by name.
func (catalog *Catalog) Purposes() []Purpose {
	return slices.SortedFunc(maps.Values(catalog.purposes), func(a, b Purpose) int {
		return strings.Compare(a.Name, b.Name)
	})
}

// Profiles returns every registered profile, ordered by name.
func (catalog *Catalog) Profiles() []Profile {
	return slices.SortedFunc(maps.Values(catalog.profiles), func(a, b Profile) int {
		return strings.Compare(a.Name, b.Name)
	})
}

// validate enforces the cross-catalog invariant: every profile must be priceable. One that is not
// would run, cost money, and fail at settle with the charge already incurred.
func (catalog *Catalog) validate() error {
	for _, profile := range catalog.profiles {
		_, found := catalog.prices[priceKey{provider: profile.Provider, model: profile.Model}]
		if !found {
			return fmt.Errorf(
				"%w: profile %q resolves to %s/%s, which the price book does not cover",
				ErrCatalogInvalid, profile.Name, profile.Provider, profile.Model,
			)
		}
	}

	return nil
}

func loadPurposes(source []byte) (map[string]Purpose, error) {
	var document purposesDocument

	err := yaml.Unmarshal(source, &document)
	if err != nil {
		return nil, fmt.Errorf("%w: purposes: %w", ErrCatalogInvalid, err)
	}

	purposes := make(map[string]Purpose, len(document.Purposes))

	for _, purpose := range document.Purposes {
		if purpose.Name == "" {
			return nil, fmt.Errorf("%w: purposes: entry with no name", ErrCatalogInvalid)
		}

		_, duplicate := purposes[purpose.Name]
		if duplicate {
			return nil, fmt.Errorf("%w: purposes: duplicate entry %q", ErrCatalogInvalid, purpose.Name)
		}

		purposes[purpose.Name] = purpose
	}

	return purposes, nil
}

func loadProfiles(source []byte) (map[string]Profile, error) {
	var document profilesDocument

	err := yaml.Unmarshal(source, &document)
	if err != nil {
		return nil, fmt.Errorf("%w: profiles: %w", ErrCatalogInvalid, err)
	}

	profiles := make(map[string]Profile, len(document.Profiles))

	for _, profile := range document.Profiles {
		switch {
		case profile.Name == "":
			return nil, fmt.Errorf("%w: profiles: entry with no name", ErrCatalogInvalid)
		case profile.Provider == "":
			return nil, fmt.Errorf("%w: profiles: %q has no provider", ErrCatalogInvalid, profile.Name)
		case profile.Model == "":
			return nil, fmt.Errorf("%w: profiles: %q has no model", ErrCatalogInvalid, profile.Name)
		case profile.MaxOutputTokens <= 0:
			return nil, fmt.Errorf("%w: profiles: %q has no output ceiling", ErrCatalogInvalid, profile.Name)
		}

		_, duplicate := profiles[profile.Name]
		if duplicate {
			return nil, fmt.Errorf("%w: profiles: duplicate entry %q", ErrCatalogInvalid, profile.Name)
		}

		profiles[profile.Name] = profile
	}

	return profiles, nil
}

func loadPrices(source []byte) (map[priceKey][]Price, error) {
	var document pricesDocument

	err := yaml.Unmarshal(source, &document)
	if err != nil {
		return nil, fmt.Errorf("%w: prices: %w", ErrCatalogInvalid, err)
	}

	prices := make(map[priceKey][]Price)

	for _, price := range document.Prices {
		parsed, err := parsePrice(price)
		if err != nil {
			return nil, err
		}

		key := priceKey{provider: parsed.Provider, model: parsed.Model}

		for _, existing := range prices[key] {
			if existing.EffectiveFrom.Equal(parsed.EffectiveFrom) {
				return nil, fmt.Errorf(
					"%w: prices: %s/%s has two entries effective %s",
					ErrCatalogInvalid, parsed.Provider, parsed.Model, parsed.EffectiveFrom,
				)
			}
		}

		prices[key] = append(prices[key], parsed)
	}

	// Oldest first, so a lookup walks backwards and takes the first entry already in force.
	for key := range prices {
		entries := prices[key]
		slices.SortFunc(entries, func(a, b Price) int {
			return a.EffectiveFrom.Compare(b.EffectiveFrom)
		})

		prices[key] = entries
	}

	return prices, nil
}

func parsePrice(price Price) (Price, error) {
	switch {
	case price.Provider == "":
		return Price{}, fmt.Errorf("%w: prices: entry with no provider", ErrCatalogInvalid)
	case price.Model == "":
		return Price{}, fmt.Errorf("%w: prices: %s entry with no model", ErrCatalogInvalid, price.Provider)
	case price.Currency == "":
		return Price{}, fmt.Errorf(
			"%w: prices: %s/%s has no currency", ErrCatalogInvalid, price.Provider, price.Model,
		)
	case price.EffectiveFrom.IsZero():
		return Price{}, fmt.Errorf(
			"%w: prices: %s/%s has no effective date", ErrCatalogInvalid, price.Provider, price.Model,
		)
	}

	rates := []struct {
		name  string
		raw   string
		field *decimal.Decimal
	}{
		{"inputPerMtoken", price.InputPerMTokenRaw, &price.InputPerMToken},
		{"cachedInputPerMtoken", price.CachedInputPerMTokenRaw, &price.CachedInputPerMToken},
		{"outputPerMtoken", price.OutputPerMTokenRaw, &price.OutputPerMToken},
	}

	for _, rate := range rates {
		if rate.raw == "" {
			return Price{}, fmt.Errorf(
				"%w: prices: %s/%s has no %s", ErrCatalogInvalid, price.Provider, price.Model, rate.name,
			)
		}

		value, err := decimal.NewFromString(rate.raw)
		if err != nil {
			return Price{}, fmt.Errorf(
				"%w: prices: %s/%s %s: %w", ErrCatalogInvalid, price.Provider, price.Model, rate.name, err,
			)
		}

		if value.IsNegative() {
			return Price{}, fmt.Errorf(
				"%w: prices: %s/%s %s is negative", ErrCatalogInvalid, price.Provider, price.Model, rate.name,
			)
		}

		*rate.field = value
	}

	return price, nil
}

func priceBookVersion(file []byte) string {
	digest := sha256.Sum256(file)

	return "sha256:" + hex.EncodeToString(digest[:])[:priceBookVersionLength]
}
