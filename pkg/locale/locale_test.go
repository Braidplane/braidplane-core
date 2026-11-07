package locale

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseAcceptLanguage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		acceptLanguage string
		want           string
	}{
		{
			name:           "EmptyHeaderReturnsDefault",
			acceptLanguage: "",
			want:           DefaultLocale,
		},
		{
			name:           "SimpleSupportedLanguageCode",
			acceptLanguage: "en",
			want:           "en",
		},
		{
			name:           "EnglishWithRegion",
			acceptLanguage: "en-US",
			want:           "en",
		},
		{
			name:           "MultipleLanguagesFirstSupported",
			acceptLanguage: "en, pl, de",
			want:           "en",
		},
		{
			name:           "UnsupportedLanguageFallsBackToClosestMatch",
			acceptLanguage: "de-DE",
			want:           "en",
		},
		{
			name:           "PolishLanguageCode",
			acceptLanguage: "pl",
			want:           "pl",
		},
		{
			name:           "PolishWithRegion",
			acceptLanguage: "pl-PL",
			want:           "pl",
		},
		{
			name:           "LanguageWithQualityFactor",
			acceptLanguage: "en-US;q=0.9",
			want:           "en",
		},
		{
			name:           "ComplexHeaderMatchesFirstSupported",
			acceptLanguage: "fr-CH, fr;q=0.9, en;q=0.8, pl;q=0.7",
			want:           "en",
		},
		{
			name:           "UnsupportedFollowedBySupported",
			acceptLanguage: "de-DE, pl, en",
			want:           "pl",
		},
		{
			name:           "LanguageCodeIsCanonicalized",
			acceptLanguage: "EN-gb",
			want:           "en",
		},
		{
			name:           "WhitespaceIsHandled",
			acceptLanguage: "  pl  ",
			want:           "pl",
		},
		{
			name:           "InvalidTagReturnsDefault",
			acceptLanguage: "invalid-tag-123",
			want:           DefaultLocale,
		},
		{
			name:           "WildcardReturnsDefault",
			acceptLanguage: "*",
			want:           DefaultLocale,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ParseAcceptLanguage(tt.acceptLanguage)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		locale string
		want   string
	}{
		{
			name:   "EmptyStringReturnsDefault",
			locale: "",
			want:   DefaultLocale,
		},
		{
			name:   "SimpleSupportedLanguageCode",
			locale: "en",
			want:   "en",
		},
		{
			name:   "EnglishWithRegionMatchesToSupportedBase",
			locale: "en-us",
			want:   "en",
		},
		{
			name:   "MixedCaseNormalizedToSupportedLocale",
			locale: "Fr-ca",
			want:   "en",
		},
		{
			name:   "PolishLanguageSupported",
			locale: "pl",
			want:   "pl",
		},
		{
			name:   "PolishWithRegion",
			locale: "pl-PL",
			want:   "pl",
		},
		{
			name:   "UnsupportedLanguageFallsBackToClosest",
			locale: "de",
			want:   "en",
		},
		{
			name:   "InvalidTagReturnsDefault",
			locale: "invalid-language",
			want:   DefaultLocale,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Normalize(tt.locale)
			assert.Equal(t, tt.want, got)
		})
	}
}
