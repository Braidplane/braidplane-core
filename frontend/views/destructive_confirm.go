package views

import "strings"

// DestructiveResourceLabel returns a compact identifier for confirmation copy.
func DestructiveResourceLabel(name, slug string) string {
	name = strings.TrimSpace(name)
	slug = strings.TrimSpace(slug)
	if name == "" {
		return slug
	}
	if slug == "" || slug == name {
		return name
	}
	return name + " (" + slug + ")"
}

// DestructiveConfirmation formats the standard confirmation copy for simple destructive actions.
func DestructiveConfirmation(verb, entity, nameOrSlug, impact string) string {
	verb = strings.TrimSpace(verb)
	entity = strings.TrimSpace(entity)
	nameOrSlug = strings.TrimSpace(nameOrSlug)
	impact = strings.TrimSpace(impact)

	target := "this " + entity
	if nameOrSlug != "" {
		target = entity + " \"" + nameOrSlug + "\""
	}

	message := strings.TrimSpace(verb + " " + target)
	if !strings.HasSuffix(message, "?") {
		message += "?"
	}
	if impact == "" {
		return message
	}

	impact = strings.TrimRight(impact, ".") + "."
	return message + " " + impact
}
