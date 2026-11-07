package run

import (
	"strings"
)

// This file keeps HTTP media helpers shared by runtime payload and contract
// adapters.

const (
	httpMediaTypeJSON        = "application/json"
	httpMediaTypeCSV         = "text/csv"
	httpMediaTypeCSV2        = "application/csv"
	httpMediaTypeFormEncoded = "application/x-www-form-urlencoded"
	httpMediaTypeMultipart   = "multipart/form-data"
)

func detectHTTPMediaType(headers map[string]string) string {
	ct := httpContentType(headers)
	if ct == "" {
		return ""
	}
	mediaType, _, _ := strings.Cut(ct, ";")
	return strings.TrimSpace(strings.ToLower(mediaType))
}
