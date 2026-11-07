package http

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/httpoauth"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	// maxOpenAPISize is the maximum allowed size for an OpenAPI specification (10MB).
	maxOpenAPISize = 10 * 1024 * 1024

	// defaultOpenAPIFetchTimeout is the default timeout for fetching OpenAPI specs (10s).
	defaultOpenAPIFetchTimeout = 10 * time.Second

	// maxOpenAPIFetchTimeout is the maximum allowed timeout (60s).
	maxOpenAPIFetchTimeout = 60 * time.Second
)

type (
	lookupIPAddrFunc func(context.Context, string) ([]net.IPAddr, error)
	dialContextFunc  func(context.Context, string, string) (net.Conn, error)
)

func ssrfProtectedDialContextWithLookup(
	lookup lookupIPAddrFunc,
	dial dialContextFunc,
) func(context.Context, string, string) (net.Conn, error) {
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}

	return func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, dialErr := hostguard.NewDestinationPolicy(false).DialContextWithDialer(
			ctx,
			network,
			address,
			lookup,
			dial,
		)
		if dialErr != nil {
			return nil, xerror.Wrapf("ssrf protection: %w", dialErr)
		}
		return conn, nil
	}
}

// validateOpenAPIURL validates that the OpenAPI URL is safe to fetch.
// Checks for:
// - Valid http/https scheme
// - Not a file:// URL
// - Not resolving to a private IP
// Same-origin documents are allowed; external references remain disabled.
func validateOpenAPIURL(openAPIURLString, baseURLString string, allowPrivateHosts bool) error {
	if openAPIURLString == "" {
		return ErrInvalidConfig.Detail("openapi_url is empty")
	}

	openAPIURL, err := url.Parse(openAPIURLString)
	if err != nil {
		return ErrInvalidConfig.Detailf("invalid openapi_url: %w", err)
	}

	// Only allow http and https schemes
	if openAPIURL.Scheme != "http" && openAPIURL.Scheme != "https" {
		return ErrInvalidConfig.Detailf(
			"openapi_url must use http or https scheme (got %s)",
			openAPIURL.Scheme,
		)
	}

	// Check hostname for private IPs
	hostname := openAPIURL.Hostname()
	if hostname == "" {
		return ErrInvalidConfig.Detail("openapi_url has no hostname")
	}

	if !allowPrivateHosts && hostguard.IsBlockedHostnameLiteral(hostname) {
		return ErrInvalidConfig.Detail(
			"openapi_url resolves to a private ip address (SSRF protection)",
		)
	}

	if openAPIURL.User != nil {
		return ErrInvalidConfig.Detail("openapi_url must not contain user information")
	}

	return nil
}

// fetchOpenAPISpecWithDialer is the internal implementation that accepts an injectable dial
// function and private-host policy.
func fetchOpenAPISpecWithDialer(
	ctx context.Context,
	openAPIURL string,
	timeoutSeconds int,
	dialCtxFn func(context.Context, string, string) (net.Conn, error),
	allowPrivateHosts bool,
	headers http.Header,
	oauthDestination string,
) ([]byte, error) {
	parsed, err := url.Parse(openAPIURL)
	if err != nil {
		return nil, ErrInvalidConfig.With(err)
	}
	if err := transportsecurity.ValidateURL(parsed, transportsecurity.PlaintextAllowed(ctx)); err != nil {
		return nil, err
	}
	// Determine timeout
	timeout := defaultOpenAPIFetchTimeout
	if timeoutSeconds > 0 {
		if timeoutSeconds > int(maxOpenAPIFetchTimeout.Seconds()) {
			timeoutSeconds = int(maxOpenAPIFetchTimeout.Seconds())
		}
		timeout = time.Duration(timeoutSeconds) * time.Second
	}

	client, err := newOpenAPIHTTPClient(timeout, dialCtxFn, allowPrivateHosts)
	if err != nil {
		return nil, err
	}
	client.Transport = transportsecurity.HTTPTransport(client.Transport.(*http.Transport), transportsecurity.PlaintextAllowed(ctx))
	defer client.CloseIdleConnections()
	if len(headers) > 0 {
		checkRedirect := client.CheckRedirect
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if oauthDestination != "" {
				if err := httpoauth.ValidateDestination(oauthDestination, req.URL.String()); err != nil {
					return ErrInvalidConfig.With(err)
				}
			}
			if len(via) > 0 && !sameOrigin(req.URL, via[0].URL) {
				return ErrInvalidConfig.Detail("authenticated OpenAPI redirects must remain on the original origin")
			}
			return checkRedirect(req, via)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openAPIURL, nil)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "braidplane-openapi-introspection/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, xerror.Unexpected().Detailf(
			"unexpected status code %d when fetching openapi spec",
			resp.StatusCode,
		)
	}

	// Limit response body size to prevent memory exhaustion
	readLimit := schemaquota.RemainingBytes(ctx, maxOpenAPISize)
	if readLimit <= 0 {
		return nil, xerror.ExecutionUnavailable().Detail("schema result exceeds configured byte limit")
	}
	limitedReader := io.LimitReader(resp.Body, int64(readLimit)+1)
	specBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	// Check if we hit the size limit
	if len(specBytes) > readLimit {
		return nil, ErrInvalidConfig.Detailf("openapi spec exceeds %d byte limit", readLimit)
	}
	if err := schemaquota.Charge(ctx, 0, len(specBytes)); err != nil {
		return nil, err
	}

	return specBytes, nil
}

// newOpenAPIHTTPClient creates a direct pinned transport for schema discovery. Proxies are
// disabled because a proxy would resolve and dial the target outside the destination policy.
func newOpenAPIHTTPClient(
	timeout time.Duration,
	dialCtxFn func(context.Context, string, string) (net.Conn, error),
	allowPrivateHosts bool,
) (*http.Client, error) {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, xerror.Unexpected().Detail("default HTTP transport has unexpected type")
	}
	clonedTransport := transport.Clone()
	clonedTransport.Proxy = nil
	clonedTransport.DialContext = dialCtxFn

	return &http.Client{
		Timeout:   timeout,
		Transport: clonedTransport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := transportsecurity.CheckRedirect(req, via); err != nil {
				return err
			}
			if len(via) >= 5 {
				return xerror.Unexpected().Detail("too many redirects")
			}
			if _, err := hostguard.NewDestinationPolicy(allowPrivateHosts).Resolve(req.Context(), req.URL.Hostname()); err != nil {
				return xerror.Wrapf("validate OpenAPI redirect destination: %w", err)
			}
			return nil
		},
	}, nil
}
