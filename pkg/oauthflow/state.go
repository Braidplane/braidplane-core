package oauthflow

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	randomStateBytes           = 32
	maxProviderErrorCodeLength = 64
)

var (
	ErrOAuthStateInvalid  = xerror.New(xerror.KindInvalidInput, "OAUTH_STATE_INVALID", "oauth state invalid")
	ErrOAuthStateConsumed = xerror.New(xerror.KindInvalidInput, "OAUTH_STATE_CONSUMED", "oauth state consumed")
	ErrOAuthStateExpired  = xerror.New(xerror.KindInvalidInput, "OAUTH_STATE_EXPIRED", "oauth state expired")
)

// HashState returns a stable non-secret digest for a raw OAuth state value.
func HashState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

type ProviderError struct {
	Code    string
	Message string
}

// SafeProviderError maps provider response fields to a credential-safe error.
func SafeProviderError(code string, fields map[string]string) error {
	cleanCode := sanitizeProviderErrorCode(code)
	if cleanCode == "" {
		cleanCode = sanitizeProviderErrorCode(fields["error"])
	}
	if cleanCode == "" {
		cleanCode = "provider_error"
	}
	return ProviderError{Code: cleanCode, Message: "provider oauth error"}
}

func sanitizeProviderErrorCode(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	var builder strings.Builder
	builder.Grow(min(len(raw), maxProviderErrorCodeLength))
	for _, r := range raw {
		if builder.Len() >= maxProviderErrorCodeLength {
			break
		}
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '_' || r == '-' || r == '.':
			builder.WriteRune(r)
		default:
			builder.WriteByte('_')
		}
	}
	return strings.Trim(builder.String(), "_-.")
}

func (e ProviderError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// RandomState returns a cryptographically random browser-safe OAuth state.
func RandomState() (string, error) {
	var buf [randomStateBytes]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", xerror.Unexpected().With(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}
