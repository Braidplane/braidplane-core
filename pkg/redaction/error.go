package redaction

import (
	"regexp"
	"sort"
	"strings"
)

var (
	providerCredentialPattern = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9_]+|glpat-[A-Za-z0-9_-]+)\b`)
	pemPrivateKeyPattern      = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----.*?-----END (?:[A-Z0-9 ]+ )?PRIVATE KEY-----`)
	urlPattern                = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)
	bearerPattern             = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;]+`)
	basicPattern              = regexp.MustCompile(`(?i)\bbasic\s+[A-Za-z0-9+/=]+`)
	sensitiveFieldPattern     = regexp.MustCompile(`(?i)((?:"|')?\b(?:api[_-]?key|(?:access|refresh|id|bearer)[_-]?token|auth(?:orization)?|client[_-]?secret|credentials?|password|passwd|private[_-]?key|secret|session(?:[_-]?id)?|csrf|cookie|state|token)\b(?:"|')?\s*(?:=|:)\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,;)}\]]+)`)
)

// ErrorMessage redacts known credentials without unwrapping already-redacted errors.
// Callers must supply opaque secret values that cannot be recognized by their shape.
func ErrorMessage(err error, sensitiveValues ...string) string {
	if err == nil {
		return ""
	}

	message := err.Error()
	values := make([]string, 0, len(sensitiveValues))
	for _, value := range sensitiveValues {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		message = strings.ReplaceAll(message, value, "[REDACTED]")
	}

	message = pemPrivateKeyPattern.ReplaceAllString(message, "[REDACTED_PRIVATE_KEY]")
	message = urlPattern.ReplaceAllString(message, "[REDACTED_URL]")
	message = providerCredentialPattern.ReplaceAllString(message, "[REDACTED]")
	message = bearerPattern.ReplaceAllString(message, "Bearer [REDACTED]")
	message = basicPattern.ReplaceAllString(message, "Basic [REDACTED]")
	return sensitiveFieldPattern.ReplaceAllString(message, "${1}[REDACTED]")
}
