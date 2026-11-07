// Package transportsecurity enforces encrypted connector transport independently of host access policy.
package transportsecurity

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/url"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type plaintextKey struct{}

// WithPlaintext permits local development fixtures. Deployment validation must reject this policy outside local profiles.
func WithPlaintext(ctx context.Context) context.Context {
	return context.WithValue(ctx, plaintextKey{}, true)
}

func PlaintextAllowed(ctx context.Context) bool {
	allowed, _ := ctx.Value(plaintextKey{}).(bool)
	return allowed
}

// TLS retains the configured hostname when a destination policy pins the socket to an IP.
func TLS(host string) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
}

func ValidateURL(target *url.URL, allowPlaintext bool) error {
	if target == nil || (target.Scheme != "https" && (!allowPlaintext || target.Scheme != "http")) {
		return xerror.ValidationFailed().Detail("connector endpoint requires HTTPS; plaintext is restricted to explicit local development policy")
	}
	return nil
}

// CheckRedirect forbids downgrades even when local development permits an initial HTTP endpoint.
func CheckRedirect(req *http.Request, via []*http.Request) error {
	for _, previous := range via {
		if previous != nil && previous.URL != nil && previous.URL.Scheme == "https" && req.URL.Scheme != "https" {
			return xerror.ValidationFailed().Detail("connector redirects must not downgrade HTTPS")
		}
	}
	return nil
}

type guardedTransport struct {
	base           http.RoundTripper
	allowPlaintext bool
}

// HTTPTransport also checks dynamically constructed execution URLs before any credentials reach the network.
func HTTPTransport(base http.RoundTripper, allowPlaintext bool) http.RoundTripper {
	if allowPlaintext {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &guardedTransport{base: base, allowPlaintext: allowPlaintext}
}

func (t *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := ValidateURL(req.URL, t.allowPlaintext); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

func (t *guardedTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
