package httpsecurity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	CSRFFormField  = "_csrf"
	CSRFHeader     = "X-CSRF-Token"
	csrfSessionKey = "csrf_token"
)

type contextKey string

const csrfContextKey = contextKey("csrf_token")

// WithCSRFToken stores the current request's CSRF token in context so frontend
// server-to-backend API calls can forward it when they also forward cookies.
func WithCSRFToken(ctx context.Context, token string) context.Context {
	if ctx == nil || token == "" {
		return ctx
	}
	return context.WithValue(ctx, csrfContextKey, token)
}

// CSRFTokenFromContext returns the token installed by WithCSRFToken, if any.
func CSRFTokenFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	token, _ := ctx.Value(csrfContextKey).(string)
	return token
}

// SafeMethod reports whether a method is defined as safe for CSRF purposes.
func SafeMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions, fiber.MethodTrace:
		return true
	default:
		return false
	}
}

// EnsureCSRFToken returns the session token, creating one when the browser
// session does not have a token yet.
func EnsureCSRFToken(c fiber.Ctx, store *session.Store) (string, error) {
	sess, err := store.Get(c)
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	defer sess.Release()

	if token, ok := sess.Get(csrfSessionKey).(string); ok && token != "" {
		return token, nil
	}

	token, err := newToken()
	if err != nil {
		return "", err
	}
	sess.Set(csrfSessionKey, token)
	if err := sess.Save(); err != nil {
		return "", xerror.Unexpected().With(err)
	}
	return token, nil
}

// ExistingCSRFToken returns the session token only when the request already
// carries a session lookup value. It never creates or saves a new session.
func ExistingCSRFToken(c fiber.Ctx, store *session.Store) (string, error) {
	if !HasSessionLookup(c, store) {
		return "", nil
	}
	sess, err := store.Get(c)
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	defer sess.Release()
	token, _ := sess.Get(csrfSessionKey).(string)
	return token, nil
}

// ValidateCSRFToken compares the submitted header or form token with the
// session token without exposing token values to logs or errors.
func ValidateCSRFToken(c fiber.Ctx, store *session.Store) (bool, error) {
	sess, err := store.Get(c)
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	defer sess.Release()

	want, _ := sess.Get(csrfSessionKey).(string)
	if want == "" {
		return false, nil
	}

	got := strings.TrimSpace(c.Get(CSRFHeader))
	if got == "" {
		got = strings.TrimSpace(c.FormValue(CSRFFormField))
	}
	if got == "" {
		return false, nil
	}

	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1, nil
}

// HasSessionLookup reports whether the request includes the configured session
// lookup value. It lets backend middleware avoid creating sessions for generic
// API clients that did not send browser cookies.
func HasSessionLookup(c fiber.Ctx, store *session.Store) bool {
	if store == nil || store.Extractor.Extract == nil {
		return false
	}
	value, err := store.Extractor.Extract(c)
	return err == nil && strings.TrimSpace(value) != ""
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", xerror.Unexpected().With(err)
	}
	return hex.EncodeToString(b[:]), nil
}
