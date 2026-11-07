package eventssvc

import (
	"encoding/json"
	"strings"
	"unicode"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const maxSecurityAuditMetadataBytes = 16 * 1024

var unsafeSecurityAuditMetadataKeys = map[string]struct{}{
	"authorization":    {},
	"body":             {},
	"client_secret":    {},
	"content":          {},
	"cookie":           {},
	"credential":       {},
	"file_contents":    {},
	"header":           {},
	"headers":          {},
	"parameter_values": {},
	"password":         {},
	"payload":          {},
	"private_key":      {},
	"query_results":    {},
	"request_body":     {},
	"request_headers":  {},
	"response_body":    {},
	"response_headers": {},
	"session_id":       {},
	"token":            {},
	"token_hash":       {},
}

func normalizeMetadata(eventClass model.EventClass, metadata map[string]any) ([]byte, error) {
	if metadata == nil {
		metadata = map[string]any{}
	}

	payload, err := json.Marshal(metadata)
	if err != nil {
		return nil, xerror.Wrapf("metadata: %w", ErrInvalidInput.With(err))
	}

	if eventClass == model.EventClassSecurityAudit {
		if len(payload) > maxSecurityAuditMetadataBytes {
			return nil, ErrInvalidInput.Detail("security audit metadata exceeds 16384 bytes")
		}

		var serialized map[string]any
		if err := json.Unmarshal(payload, &serialized); err != nil {
			return nil, xerror.Wrapf("metadata: %w", ErrInvalidInput.With(err))
		}
		if key, ok := findUnsafeSecurityAuditMetadataKey(serialized); ok {
			return nil, ErrInvalidInput.Detail("security audit metadata contains unsafe key: " + key)
		}
	}

	return payload, nil
}

func findUnsafeSecurityAuditMetadataKey(value any) (string, bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if isUnsafeSecurityAuditMetadataKey(key) {
				return key, true
			}
			if unsafeKey, ok := findUnsafeSecurityAuditMetadataKey(nested); ok {
				return unsafeKey, true
			}
		}
	case []any:
		for _, nested := range typed {
			if unsafeKey, ok := findUnsafeSecurityAuditMetadataKey(nested); ok {
				return unsafeKey, true
			}
		}
	}
	return "", false
}

func isUnsafeSecurityAuditMetadataKey(key string) bool {
	normalized := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return '_'
	}, strings.TrimSpace(key))
	normalized = strings.Trim(normalized, "_")
	if _, ok := unsafeSecurityAuditMetadataKeys[normalized]; ok {
		return true
	}
	for _, fragment := range []string{"password", "passwd", "secret", "token", "credential", "session_id"} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return strings.HasSuffix(normalized, "_session") ||
		strings.HasSuffix(normalized, "_payload") ||
		strings.HasSuffix(normalized, "_body") ||
		strings.HasSuffix(normalized, "_header") ||
		strings.HasSuffix(normalized, "_headers") ||
		strings.HasSuffix(normalized, "_content") ||
		strings.HasSuffix(normalized, "_contents")
}
