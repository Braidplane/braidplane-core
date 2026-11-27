package mcpauthsvc

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type metadataRoundTripper func(*http.Request) (*http.Response, error)

func (f metadataRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestClientMetadataResolution(t *testing.T) {
	const id = "https://client.example.test/metadata.json"
	const document = `{"client_id":"https://client.example.test/metadata.json","client_name":"Example","redirect_uris":["https://client.example.test/callback"]}`
	for _, tc := range []struct {
		name, body, mediaType string
		status                int
		want                  error
	}{
		{"public client", document, "application/json", 200, nil},
		{"public and asymmetric methods", strings.TrimSuffix(document, "}") + `,"token_endpoint_auth_methods_supported":["none","private_key_jwt"]}`, "application/json", 200, nil},
		{"identity mismatch", strings.Replace(document, "/metadata.json", "/different.json", 1), "application/json", 200, ErrInvalidClient},
		{"confidential client", strings.TrimSuffix(document, "}") + `,"token_endpoint_auth_method":"private_key_jwt"}`, "application/json", 200, ErrInvalidClient},
		{"symmetric method", strings.TrimSuffix(document, "}") + `,"token_endpoint_auth_methods_supported":["none","client_secret_basic"]}`, "application/json", 200, ErrInvalidClient},
		{"secret field", strings.TrimSuffix(document, "}") + `,"client_secret":"synthetic"}`, "application/json", 200, ErrInvalidClient},
		{"duplicate identity", strings.TrimSuffix(document, "}") + `,"client_id":"https://client.example.test/metadata.json"}`, "application/json", 200, ErrInvalidClient},
		{"incorrect identity field case", strings.Replace(document, "client_id", "Client_ID", 1), "application/json", 200, ErrInvalidClient},
		{"other grant", strings.TrimSuffix(document, "}") + `,"grant_types":["client_credentials"]}`, "application/json", 200, ErrInvalidClient},
		{"too large", document + strings.Repeat(" ", maxClientMetadataBytes), "application/json", 200, ErrInvalidClient},
		{"trailing JSON", document + "{}", "application/json", 200, ErrInvalidClient},
		{"wrong media type", document, "text/html", 200, ErrInvalidClient},
		{"redirect", "", "text/html", 302, ErrInvalidClient},
		{"remote error", "", "text/plain", 503, errClientMetadataUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := New(testIssuer, testResource, nil)
			require.NoError(t, err)
			calls := 0
			d.metadataHTTP.Transport = metadataRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, id, req.URL.String())
				require.Equal(t, http.MethodGet, req.Method)
				require.Equal(t, "application/json", req.Header.Get("Accept"))
				require.Empty(t, req.Header.Get("Authorization"))
				require.Empty(t, req.Header.Get("Cookie"))
				header := http.Header{"Content-Type": {tc.mediaType}, "Location": {"https://elsewhere.example.test/clients"}}
				return &http.Response{StatusCode: tc.status, Header: header, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			client, err := d.resolveClient(t.Context(), id)
			require.ErrorIs(t, err, tc.want)
			require.Equal(t, 1, calls)
			if tc.want == nil {
				require.Equal(t, Client{ID: id, Name: "Example", RedirectURIs: []string{"https://client.example.test/callback"}}, client)
			}
		})
	}
}

func TestMetadataURLRejectsInvalidIDsBeforeFetching(t *testing.T) {
	d, err := New(testIssuer, testResource, nil)
	require.NoError(t, err)
	d.metadataHTTP.Transport = metadataRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid client ID reached the HTTP client")
		return nil, ErrInvalidClient
	})
	for _, id := range []string{
		"unknown", "http://client.example.test/metadata.json", "https://client.example.test/",
		"https://client.example.test/apps/%2e%2e/metadata.json", "https://client.example.test/metadata.json?",
		"https://client.example.test/metadata.json#", "https://name@client.example.test/metadata.json",
		"https://localhost/metadata.json",
	} {
		_, err := d.resolveClient(t.Context(), id)
		require.ErrorIs(t, err, ErrInvalidClient, "%s", id)
	}
}

func TestMetadataDialRejectsNonPublicAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "169.254.169.254", "::ffff:10.0.0.1", "64:ff9b::a00:1", "2002:0a00:1::", "3fff::1"} {
		t.Run(address, func(t *testing.T) {
			for _, target := range []string{address, "client.example.test"} {
				_, err := dialMetadataHost(t.Context(), "tcp", net.JoinHostPort(target, "443"),
					func(context.Context, string) ([]net.IPAddr, error) {
						return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP(address)}}, nil
					},
					func(context.Context, string, string) (net.Conn, error) {
						t.Fatal("a blocked destination reached the network dialer")
						return nil, ErrInvalidClient
					})
				require.ErrorIs(t, err, ErrInvalidClient)
			}
		})
	}
}

func TestMetadataDialPinsResolvedPublicIP(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	lookups := 0
	conn, err := dialMetadataHost(t.Context(), "tcp", "client.example.test:443",
		func(_ context.Context, host string) ([]net.IPAddr, error) {
			lookups++
			require.Equal(t, "client.example.test", host)
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		},
		func(_ context.Context, network, address string) (net.Conn, error) {
			require.Equal(t, "tcp", network)
			require.Equal(t, "8.8.8.8:443", address)
			return client, nil
		})
	require.NoError(t, err)
	require.Same(t, client, conn)
	require.Equal(t, 1, lookups)
}
