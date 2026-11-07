package views

import "net/url"

// DocumentationPageURL keeps contextual guides on the configured documentation site.
func DocumentationPageURL(base, page string) string {
	if base == "" {
		return ""
	}
	destination, err := url.JoinPath(base, page)
	if err != nil {
		return ""
	}
	return destination
}
