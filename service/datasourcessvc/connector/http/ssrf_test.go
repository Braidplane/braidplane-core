package http

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/hostguard"
)

// permissiveDialContext returns a plain dialer with no SSRF blocking.
// Used in tests to allow connections to httptest.Server (127.0.0.1).
func permissiveDialContext() func(context.Context, string, string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext
}

func TestNewUsesProxyFreePinnedTransportAndValidatesRedirects(t *testing.T) {
	t.Parallel()

	connector, err := New(
		transportsecurity.WithPlaintext(context.Background()),
		Config{BaseURL: "https://93.184.216.34"},
		Credentials{},
	)
	require.NoError(t, err)

	transport, ok := connector.client.Transport.(*http.Transport)
	require.True(t, ok)
	require.Nil(t, transport.Proxy)

	_, err = transport.DialContext(transportsecurity.WithPlaintext(context.Background()), "tcp", "127.0.0.1:80")
	require.ErrorContains(t, err, "non-public ip address")

	blockedRedirect, err := http.NewRequestWithContext(
		transportsecurity.WithPlaintext(context.Background()), http.MethodGet, "http://169.254.169.254/latest/meta-data", nil,
	)
	require.NoError(t, err)
	require.ErrorContains(t, connector.client.CheckRedirect(blockedRedirect, nil), "non-public ip address")

	allowedRedirect, err := http.NewRequestWithContext(
		transportsecurity.WithPlaintext(context.Background()), http.MethodGet, "https://93.184.216.34/redirect", nil,
	)
	require.NoError(t, err)
	require.NoError(t, connector.client.CheckRedirect(allowedRedirect, nil))
}

func TestIsBlockedHostnameLiteral(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		hostname      string
		expectBlocked bool
	}{
		// IPv4 loopback
		{
			name:          "IPv4LoopbackIsPrivate",
			hostname:      "127.0.0.1",
			expectBlocked: true,
		},
		{
			name:          "IPv4Localhost255IsPrivate",
			hostname:      "127.255.255.255",
			expectBlocked: true,
		},
		// IPv4 private ranges
		{
			name:          "IPv4ClassAPrivateIsPrivate",
			hostname:      "10.0.0.1",
			expectBlocked: true,
		},
		{
			name:          "IPv4ClassBPrivateIsPrivate",
			hostname:      "172.16.0.1",
			expectBlocked: true,
		},
		{
			name:          "IPv4ClassCPrivateIsPrivate",
			hostname:      "192.168.1.1",
			expectBlocked: true,
		},
		// IPv4 link-local
		{
			name:          "IPv4LinkLocalIsPrivate",
			hostname:      "169.254.1.1",
			expectBlocked: true,
		},
		// IPv6 loopback
		{
			name:          "IPv6LoopbackIsPrivate",
			hostname:      "::1",
			expectBlocked: true,
		},
		{
			name:          "IPv6LoopbackBracketedIsPrivate",
			hostname:      "[::1]",
			expectBlocked: true,
		},
		// IPv6 private
		{
			name:          "IPv6UniqueLocalIsPrivate",
			hostname:      "fc00::1",
			expectBlocked: true,
		},
		{
			name:          "IPv6UniqueLocalBracketedIsPrivate",
			hostname:      "[fd00::1]",
			expectBlocked: true,
		},
		// IPv6 link-local
		{
			name:          "IPv6LinkLocalIsPrivate",
			hostname:      "fe80::1",
			expectBlocked: true,
		},
		// Public IPs
		{
			name:          "PublicIPv4IsNotPrivate",
			hostname:      "8.8.8.8",
			expectBlocked: false,
		},
		{
			name:          "AnotherPublicIPv4IsNotPrivate",
			hostname:      "1.1.1.1",
			expectBlocked: false,
		},
		{
			name:          "PublicIPv6IsNotPrivate",
			hostname:      "2001:4860:4860::8888",
			expectBlocked: false,
		},
		// Domain names
		{
			name:          "DomainNameIsNotPrivate",
			hostname:      "example.com",
			expectBlocked: false,
		},
		{
			name:          "SubdomainIsNotPrivate",
			hostname:      "api.example.com",
			expectBlocked: false,
		},
		{
			name:          "LocalhostDomainIsNotPrivate",
			hostname:      "localhost",
			expectBlocked: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := hostguard.IsBlockedHostnameLiteral(tt.hostname)
			require.Equal(t, tt.expectBlocked, result)
		})
	}
}

func TestValidateOpenAPIURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		openAPIURL      string
		baseURL         string
		allowPrivate    bool
		wantErrContains string
	}{
		// Valid URLs
		{
			name:       "ValidHTTPSURL",
			openAPIURL: "https://docs.example.com/openapi.json",
			baseURL:    "https://api.example.com",
		},
		{
			name:       "ValidHTTPURL",
			openAPIURL: "http://docs.example.com/openapi.json",
			baseURL:    "http://api.example.com",
		},
		{
			name:       "ValidURLDifferentDomain",
			openAPIURL: "https://docs.example.com/openapi.json",
			baseURL:    "https://api.example.com",
		},
		{
			name:       "ValidURLDifferentPort",
			openAPIURL: "https://api.example.com:8080/openapi.json",
			baseURL:    "https://api.example.com:9090",
		},
		// Invalid schemes
		{
			name:            "FileSchemeIsRejected",
			openAPIURL:      "file:///etc/passwd",
			baseURL:         "https://api.example.com",
			wantErrContains: "must use http or https scheme",
		},
		{
			name:            "FTPSchemeIsRejected",
			openAPIURL:      "ftp://api.example.com/openapi.json",
			baseURL:         "https://api.example.com",
			wantErrContains: "must use http or https scheme",
		},
		{
			name:            "DataSchemeIsRejected",
			openAPIURL:      "data:text/plain,hello",
			baseURL:         "https://api.example.com",
			wantErrContains: "must use http or https scheme",
		},
		// Private IPs
		{
			name:            "PrivateIPv4IsRejected",
			openAPIURL:      "http://192.168.1.1/openapi.json",
			baseURL:         "https://api.example.com",
			wantErrContains: "private ip address",
		},
		{
			name:            "CGNATIPv4IsRejected",
			openAPIURL:      "http://100.64.0.1/openapi.json",
			baseURL:         "https://api.example.com",
			wantErrContains: "private ip address",
		},
		{
			name:         "PrivateIPv4IsAllowedWhenAllowPrivateHostsEnabled",
			openAPIURL:   "http://192.168.1.1/openapi.json",
			baseURL:      "https://api.example.com",
			allowPrivate: true,
		},
		{
			name:            "LoopbackIPIsRejected",
			openAPIURL:      "http://127.0.0.1/openapi.json",
			baseURL:         "https://api.example.com",
			wantErrContains: "private ip address",
		},
		{
			name:            "IPv6LoopbackIsRejected",
			openAPIURL:      "http://[::1]/openapi.json",
			baseURL:         "https://api.example.com",
			wantErrContains: "private ip address",
		},
		{
			name:            "IPv6PrivateIsRejected",
			openAPIURL:      "http://[fc00::1]/openapi.json",
			baseURL:         "https://api.example.com",
			wantErrContains: "private ip address",
		},
		{
			name:            "IPv6MulticastIsRejected",
			openAPIURL:      "http://[ff00::1]/openapi.json",
			baseURL:         "https://api.example.com",
			wantErrContains: "private ip address",
		},
		// Self-reference
		{
			name:       "SameOriginIsAllowed",
			openAPIURL: "https://api.example.com/openapi.json",
			baseURL:    "https://api.example.com/v1",
		},
		{
			name:       "SameOriginWithPortIsAllowed",
			openAPIURL: "https://api.example.com:443/openapi.json",
			baseURL:    "https://api.example.com:443/v1",
		},
		// Empty/invalid
		{
			name:            "EmptyURLIsRejected",
			openAPIURL:      "",
			baseURL:         "https://api.example.com",
			wantErrContains: "openapi_url is empty",
		},
		{
			name:            "MalformedURLIsRejected",
			openAPIURL:      "://invalid",
			baseURL:         "https://api.example.com",
			wantErrContains: "invalid openapi_url",
		},
		{
			name:            "NoHostnameIsRejected",
			openAPIURL:      "https:///openapi.json",
			baseURL:         "https://api.example.com",
			wantErrContains: "has no hostname",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateOpenAPIURL(tt.openAPIURL, tt.baseURL, tt.allowPrivate)

			if tt.wantErrContains != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.wantErrContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestSSRFDialContext_DialsResolvedIPAddress(t *testing.T) {
	t.Parallel()

	var dialedNetwork string
	var dialedAddress string
	var resolvedHosts []string

	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	dialContext := ssrfProtectedDialContextWithLookup(
		func(_ context.Context, host string) ([]net.IPAddr, error) {
			resolvedHosts = append(resolvedHosts, host)
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		},
		func(_ context.Context, network, address string) (net.Conn, error) {
			dialedNetwork = network
			dialedAddress = address
			return clientConn, nil
		},
	)

	conn, err := dialContext(transportsecurity.WithPlaintext(context.Background()), "tcp", "api.example.test:443")
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.Equal(t, []string{"api.example.test"}, resolvedHosts)
	require.Equal(t, "tcp", dialedNetwork)
	require.Equal(t, "93.184.216.34:443", dialedAddress)
}

func TestSSRFDialContext_RejectsNonPublicResolvedIPAddress(t *testing.T) {
	t.Parallel()

	dialCalled := false

	dialContext := ssrfProtectedDialContextWithLookup(
		func(_ context.Context, _ string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		},
		func(_ context.Context, _, _ string) (net.Conn, error) {
			dialCalled = true
			return nil, assert.AnError
		},
	)

	_, err := dialContext(transportsecurity.WithPlaintext(context.Background()), "tcp", "api.example.test:443")
	require.Error(t, err)
	require.ErrorContains(t, err, "non-public ip address")
	require.False(t, dialCalled)
}

func TestFetchOpenAPISpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		setupServer     func() *httptest.Server
		timeoutSeconds  int
		wantErrContains string
	}{
		{
			name: "SuccessfulFetchReturnsSpec",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						assert.Equal(t, "application/json", r.Header.Get("Accept"))
						assert.Contains(t, r.Header.Get("User-Agent"), "braidplane")
						w.WriteHeader(http.StatusOK)
						_, _ = w.Write([]byte(`{"openapi": "3.0.0"}`))
					}),
				)
			},
		},
		{
			name: "NotFoundReturnsError",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusNotFound)
					}),
				)
			},
			wantErrContains: "unexpected status code 404",
		},
		{
			name: "InternalServerErrorReturnsError",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusInternalServerError)
					}),
				)
			},
			wantErrContains: "unexpected status code 500",
		},
		{
			name: "SpecTooLargeIsRejected",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.WriteHeader(http.StatusOK)
						// Write more than maxOpenAPISize (10MB)
						largeData := make([]byte, maxOpenAPISize+1000)
						_, _ = w.Write(largeData)
					}),
				)
			},
			wantErrContains: "exceeds",
		},
		{
			name: "RedirectToPrivateIPIsRejected",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						http.Redirect(w, r, "http://192.168.1.1/openapi.json", http.StatusFound)
					}),
				)
			},
			wantErrContains: "non-public ip address",
		},
		{
			name: "CustomTimeoutIsRespected",
			setupServer: func() *httptest.Server {
				return httptest.NewServer(
					http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						// Delay longer than custom timeout (1 second)
						time.Sleep(2 * time.Second)
						w.WriteHeader(http.StatusOK)
					}),
				)
			},
			timeoutSeconds:  1,
			wantErrContains: "context deadline exceeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := tt.setupServer()
			defer server.Close()

			url := server.URL

			ctx := transportsecurity.WithPlaintext(context.Background())
			// Use short default timeout for tests that don't test timeout
			timeoutSeconds := tt.timeoutSeconds
			if timeoutSeconds == 0 && !strings.Contains(tt.name, "Timeout") {
				timeoutSeconds = 5
			}

			specBytes, err := fetchOpenAPISpecWithDialer(
				ctx,
				url,
				timeoutSeconds,
				permissiveDialContext(),
				false, nil, "",
			)

			if tt.wantErrContains != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.wantErrContains)
			} else {
				require.NoError(t, err)
				require.NotEmpty(t, specBytes)
			}
		})
	}
}

func TestFetchOpenAPISpec_ContextCancellation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(transportsecurity.WithPlaintext(context.Background()))
	cancel() // Cancel immediately

	_, err := fetchOpenAPISpecWithDialer(ctx, server.URL, 10, permissiveDialContext(), false, nil, "")
	require.Error(t, err)
	require.ErrorContains(t, err, "context canceled")
}

func TestFetchOpenAPISpec_MaxTimeoutEnforced(t *testing.T) {
	t.Parallel()

	// Test that timeout is clamped to maxOpenAPIFetchTimeout
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"openapi": "3.0.0"}`))
	}))
	defer server.Close()

	// Request a timeout longer than maxOpenAPIFetchTimeout (60s)
	// Should be clamped to 60s, but the request will still succeed quickly
	specBytes, err := fetchOpenAPISpecWithDialer(
		transportsecurity.WithPlaintext(context.Background()),
		server.URL,
		120,
		permissiveDialContext(),
		false, nil, "",
	)
	require.NoError(t, err)
	require.NotEmpty(t, specBytes)
}

// TestNewOpenAPIHTTPClient_DisablesProxy confirms discovery dials the vetted target directly.
func TestNewOpenAPIHTTPClient_DisablesProxy(t *testing.T) {
	t.Parallel()

	client, err := newOpenAPIHTTPClient(time.Second, permissiveDialContext(), false)
	require.NoError(t, err)
	t.Cleanup(client.CloseIdleConnections)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.Nil(t, transport.Proxy)
}
