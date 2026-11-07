package i18n

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewBundle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		defaultLanguage string
		expectError     bool
	}{
		{
			name:            "WithValidEnglishLocale",
			defaultLanguage: "en",
			expectError:     false,
		},
		{
			name:            "WithValidPolishLocale",
			defaultLanguage: "pl",
			expectError:     false,
		},
		{
			name:            "WithFullLanguageTag",
			defaultLanguage: "en-US",
			expectError:     false,
		},
		{
			name:            "WithInvalidLocaleFallsBackToEnglish",
			defaultLanguage: "invalid-locale",
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bundle, err := NewBundle(tt.defaultLanguage)
			require.NoError(t, err)
			require.NotNil(t, bundle)
			assert.NotNil(t, bundle.bundle)
		})
	}
}

func TestBundleNewLocalizer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		locale string
	}{
		{
			name:   "CreateLocalizerForEnglish",
			locale: "en",
		},
		{
			name:   "CreateLocalizerForPolish",
			locale: "pl",
		},
		{
			name:   "CreateLocalizerWithRegion",
			locale: "en-US",
		},
		{
			name:   "CreateLocalizerWithInvalidLocale",
			locale: "invalid-locale",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bundle, err := NewBundle("en")
			require.NoError(t, err)

			localizer := bundle.NewLocalizer(tt.locale)
			assert.NotNil(t, localizer)
		})
	}
}
