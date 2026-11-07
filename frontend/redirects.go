package frontend

import (
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v3"
)

const (
	defaultPostLoginPath = "/dashboard"
	organizationsPath    = "/organizations"
)

func safeReturnTo(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}

	if parsed.IsAbs() || parsed.Host != "" {
		return ""
	}

	if !strings.HasPrefix(parsed.Path, "/") {
		return ""
	}

	if strings.HasPrefix(trimmed, "//") {
		return ""
	}

	if strings.Contains(trimmed, "\\") {
		return ""
	}

	return trimmed
}

func resolvePostLoginRedirect(returnTo string) string {
	safe := safeReturnTo(returnTo)
	if safe == "" {
		return defaultPostLoginPath
	}

	return safe
}

func loginRedirectPath(returnTo string) string {
	safe := safeReturnTo(returnTo)
	if safe == "" {
		return "/auth/login"
	}

	return "/auth/login?return_to=" + url.QueryEscape(safe)
}

func redirectToLogin(c fiber.Ctx, returnTo string) error {
	return redirectHTMXOrSeeOther(c, loginRedirectPath(returnTo))
}

// Only an inbox in the current organization may be the return target of this journey.
func notificationReturnTo(raw, org string) string {
	parsed, err := url.Parse(safeReturnTo(raw))
	if err != nil || parsed.Path != "/orgs/"+org+"/notifications" {
		return ""
	}
	return parsed.String()
}
