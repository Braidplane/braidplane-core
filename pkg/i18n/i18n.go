package i18n

import (
	"embed"
	"log/slog"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

//go:embed translations/active.*.toml
var translationFiles embed.FS

// Localizer is the main interface for translating messages
type Localizer interface {
	Localize(messageID string, templateData map[string]any) string
	LocalizeWithPlural(messageID string, pluralCount any, templateData map[string]any) string
}

// localizer wraps go-i18n's localizer
type localizer struct {
	loc *i18n.Localizer
}

// Localize translates a message using the given messageID
func (l *localizer) Localize(messageID string, templateData map[string]any) string {
	msg, err := l.loc.Localize(&i18n.LocalizeConfig{
		MessageID:    messageID,
		TemplateData: templateData,
	})
	if err != nil {
		slog.Error("failed to localize message", "messageID", messageID, "error", err)
		return messageID
	}
	return msg
}

// LocalizeWithPlural translates a message with plural support
func (l *localizer) LocalizeWithPlural(
	messageID string,
	pluralCount any,
	templateData map[string]any,
) string {
	if l.loc == nil {
		return messageID
	}

	msg, err := l.loc.Localize(&i18n.LocalizeConfig{
		MessageID:    messageID,
		PluralCount:  pluralCount,
		TemplateData: templateData,
	})
	if err != nil {
		slog.Error("failed to localize message with plural", "messageID", messageID, "error", err)
		return messageID
	}
	return msg
}

// Bundle manages all loaded message bundles and provides localization from embedded files
type Bundle struct {
	bundle *i18n.Bundle
}

// NewBundle creates a new i18n bundle with the given default language and loads all embedded
// translations
func NewBundle(defaultLanguage string) (*Bundle, error) {
	tag, err := language.Parse(defaultLanguage)
	if err != nil {
		tag = language.English
	}

	bundle := i18n.NewBundle(tag)

	// Register TOML unmarshal function
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)

	// Load all active translation files from embedded filesystem
	entries, err := translationFiles.ReadDir("translations")
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		// Only load active.*.toml files (not translate.*.toml files)
		if len(filename) < 11 || filename[:6] != "active" || filename[len(filename)-5:] != ".toml" {
			continue
		}

		// Load the translation file using go-i18n's built-in loader
		if _, err := bundle.LoadMessageFileFS(
			translationFiles,
			"translations/"+filename,
		); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
	}

	return &Bundle{bundle: bundle}, nil
}

// NewLocalizer creates a new localizer for the given language locale
func (b *Bundle) NewLocalizer(locale string) Localizer {
	loc := i18n.NewLocalizer(b.bundle, locale)

	return &localizer{loc: loc}
}
