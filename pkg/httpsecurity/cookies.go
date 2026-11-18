package httpsecurity

import "github.com/gofiber/fiber/v3/extractors"

// BrowserCookieName selects the secure prefix for a host-only cookie's path.
func BrowserCookieName(name, path string, secure bool) string {
	if !secure {
		return name
	}
	if path == "/" {
		return "__Host-" + name
	}
	return "__Secure-" + name
}

// SessionCookieExtractor keeps both browser session stores using the same name.
func SessionCookieExtractor(secure bool) extractors.Extractor {
	return extractors.FromCookie(BrowserCookieName("session_id", "/", secure))
}
