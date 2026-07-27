// Package catalog holds the two closed vocabularies a caller draws on: the purposes a generation
// may be attributed to, and the profiles it may run under.
//
// Both are YAML embedded at build time and parsed at boot, so a change is a reviewed commit with a
// git history.
//
// Pricing is deliberately absent. This service records what was consumed — provider, model and the
// token breakdown — and whoever bills turns that into money.
package catalog

import (
	_ "embed"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed purposes.yaml
var purposesFile []byte

//go:embed profiles.yaml
var profilesFile []byte

// An unregistered name is a caller bug, never a value to pass through: it would become an
// unattributed generation or an unresolvable model.
var (
	// ErrPurposeUnknown is returned when no registered purpose carries the requested name.
	ErrPurposeUnknown = errors.New("unknown purpose")
	// ErrProfileUnknown is returned when no registered profile carries the requested name.
	ErrProfileUnknown = errors.New("unknown profile")
	// ErrCatalogInvalid wraps every parse and validation failure, so a caller branches on the class
	// rather than on a message.
	ErrCatalogInvalid = errors.New("invalid catalog")
)

// Purpose is one entry in the closed vocabulary of what a generation was spent on.
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

// Catalog is the parsed set of both vocabularies.
type Catalog struct {
	purposes map[string]Purpose
	profiles map[string]Profile
}

type purposesDocument struct {
	Purposes []Purpose `yaml:"purposes"`
}

type profilesDocument struct {
	Profiles []Profile `yaml:"profiles"`
}

// Load parses the embedded catalogs. It is what the service calls at boot.
func Load() (*Catalog, error) {
	return LoadFrom(purposesFile, profilesFile)
}

// LoadFrom parses catalogs from raw YAML.
//
// It fails rather than degrades: a profile that resolves to nothing would otherwise be discovered
// when a caller submits, not when the deployment rolls. Taking bytes is what lets the refusals be
// tested; [Load] is the entry point everything else uses.
func LoadFrom(purposesYAML, profilesYAML []byte) (*Catalog, error) {
	purposes, err := loadPurposes(purposesYAML)
	if err != nil {
		return nil, err
	}

	profiles, err := loadProfiles(profilesYAML)
	if err != nil {
		return nil, err
	}

	return &Catalog{purposes: purposes, profiles: profiles}, nil
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
