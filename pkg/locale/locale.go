package locale

import (
	"golang.org/x/text/language"
)

const DefaultLocale = "en"

// SupportedLocales defines all locales that the application supports
var SupportedLocales = []language.Tag{
	language.English,
	language.Polish,
}

// Create a language matcher for supported locales
var matcher = language.NewMatcher(SupportedLocales)

// ParseAcceptLanguage parses the Accept-Language header and returns the most preferred
// supported locale as a BCP 47 language tag string (e.g., "en", "en-US", "pl", "de-DE").
// It uses golang.org/x/text/language for proper RFC 4647 parsing and matching.
// If the user's desired language is not supported, it finds the closest match.
// If no suitable match is found, it returns the default locale "en".
func ParseAcceptLanguage(acceptLanguage string) string {
	if acceptLanguage == "" {
		return DefaultLocale
	}

	// Parse Accept-Language header (handles quality values and multiple tags)
	tags, _, err := language.ParseAcceptLanguage(acceptLanguage)
	if err != nil || len(tags) == 0 {
		return DefaultLocale
	}

	// Find the best match from supported locales
	tag, _, _ := matcher.Match(tags...)
	// Extract base language to avoid region extensions being added
	base, _ := tag.Base()
	return base.String()
}

// Normalize ensures a locale string is in a standard BCP 47 format.
// It parses, canonicalizes, and matches against supported locales.
// If the locale is not supported, returns the closest match or default.
func Normalize(locale string) string {
	if locale == "" {
		return DefaultLocale
	}

	tag, err := language.Parse(locale)
	if err != nil {
		return DefaultLocale
	}

	// Match against supported locales to find the best fit
	matched, _, _ := matcher.Match(tag)
	// Extract base language to return just the language code without region
	base, _ := matched.Base()
	return base.String()
}
