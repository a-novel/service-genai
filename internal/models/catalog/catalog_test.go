package catalog_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/a-novel/service-genai/internal/models/catalog"
)

func TestLoad(t *testing.T) {
	t.Parallel()

	loaded, err := catalog.Load()
	require.NoError(t, err)

	require.NotEmpty(t, loaded.Purposes())
	require.NotEmpty(t, loaded.Profiles())
}

func TestPurpose(t *testing.T) {
	t.Parallel()

	loaded, err := catalog.Load()
	require.NoError(t, err)

	purpose, err := loaded.Purpose("studio.generation")
	require.NoError(t, err)
	require.Equal(t, "studio.generation", purpose.Name)
	require.NotEmpty(t, purpose.Description)

	// Closed: an unregistered name is a caller bug, not a new attribution category.
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

	// A model name is not a profile name. Callers resolve tiers, never models.
	_, err = loaded.Profile("gpt-5.6-terra")
	require.ErrorIs(t, err, catalog.ErrProfileUnknown)
}

// Every shipped profile must resolve to something runnable, or a caller discovers it at submit.
func TestEveryProfileResolves(t *testing.T) {
	t.Parallel()

	loaded, err := catalog.Load()
	require.NoError(t, err)

	for _, profile := range loaded.Profiles() {
		t.Run(profile.Name, func(t *testing.T) {
			t.Parallel()

			require.NotEmpty(t, profile.Provider)
			require.NotEmpty(t, profile.Model)
			require.Positive(t, profile.MaxOutputTokens)
		})
	}
}

// The refusals are the point. A catalog that loads with a hole in it fails when a caller submits,
// not when the deployment rolls.
func TestLoadFromRefusesBadCatalogs(t *testing.T) {
	t.Parallel()

	const (
		goodPurposes = `purposes:
  - name: studio.generation
    description: valid
`
		goodProfiles = `profiles:
  - name: standard
    provider: openai
    model: a-model
    maxOutputTokens: 1024
`
	)

	testCases := []struct {
		name string

		purposes string
		profiles string

		expectErr string
	}{
		{
			name: "Success", purposes: goodPurposes, profiles: goodProfiles,
		},
		{
			name: "Error/MalformedYAML", purposes: "purposes: [", profiles: goodProfiles,
			expectErr: "purposes",
		},
		{
			name: "Error/PurposeWithoutName",
			purposes: `purposes:
  - description: nameless
`,
			profiles:  goodProfiles,
			expectErr: "entry with no name",
		},
		{
			name: "Error/DuplicatePurpose",
			purposes: `purposes:
  - name: studio.generation
  - name: studio.generation
`,
			profiles:  goodProfiles,
			expectErr: "duplicate entry",
		},
		{
			name: "Error/ProfileWithoutProvider", purposes: goodPurposes,
			profiles: `profiles:
  - name: standard
    model: a-model
    maxOutputTokens: 1024
`,
			expectErr: "has no provider",
		},
		{
			name: "Error/ProfileWithoutModel", purposes: goodPurposes,
			profiles: `profiles:
  - name: standard
    provider: openai
    maxOutputTokens: 1024
`,
			expectErr: "has no model",
		},
		{
			// Without a ceiling a caller's request is unbounded, and an unbounded output on a
			// priced call is the expensive kind of mistake.
			name: "Error/ProfileWithoutOutputCeiling", purposes: goodPurposes,
			profiles: `profiles:
  - name: standard
    provider: openai
    model: a-model
`,
			expectErr: "has no output ceiling",
		},
		{
			name: "Error/DuplicateProfile", purposes: goodPurposes,
			profiles: `profiles:
  - name: standard
    provider: openai
    model: a-model
    maxOutputTokens: 1024
  - name: standard
    provider: openai
    model: another-model
    maxOutputTokens: 2048
`,
			expectErr: "duplicate entry",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			loaded, err := catalog.LoadFrom([]byte(testCase.purposes), []byte(testCase.profiles))

			if testCase.expectErr == "" {
				require.NoError(t, err)
				require.NotNil(t, loaded)

				return
			}

			require.ErrorIs(t, err, catalog.ErrCatalogInvalid)
			require.Contains(t, err.Error(), testCase.expectErr)
			require.Nil(t, loaded)
		})
	}
}
