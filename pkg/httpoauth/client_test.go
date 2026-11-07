package httpoauth

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testConfig() Config {
	return Config{Issuer: "https://issuer.example", AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token", ClientID: "client", GrantType: "authorization_code", Scopes: []string{"read"}}
}

func TestAuthorizationPKCEAndIssuer(t *testing.T) {
	config := testConfig()
	client, err := New(config, "", "https://braidplane.example/callback", "https://api.example/v1", false)
	require.NoError(t, err)
	verifier, err := NewVerifier()
	require.NoError(t, err)
	raw, err := client.AuthorizationURL("state", verifier)
	require.NoError(t, err)
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	query := parsed.Query()
	challenge := sha256.Sum256([]byte(verifier))
	require.Equal(t, base64.RawURLEncoding.EncodeToString(challenge[:]), query.Get("code_challenge"))
	require.Equal(t, "S256", query.Get("code_challenge_method"))
	require.Equal(t, "state", query.Get("state"))
	require.Equal(t, "read", query.Get("scope"))
	_, err = client.Exchange(context.Background(), "code", verifier, "https://other.example")
	require.True(t, xerror.Is(err, ErrGrant))
	for _, invalid := range []string{"", strings.Repeat("a", 42), strings.Repeat("a", 129), strings.Repeat("a", 42) + "!"} {
		_, err = client.AuthorizationURL("state", invalid)
		require.True(t, xerror.Is(err, ErrConfig))
	}
}

func TestDestinationBinding(t *testing.T) {
	for _, destination := range []string{"https://api.example/v1", "https://api.example/v1/users?page=2"} {
		require.NoError(t, ValidateDestination("https://api.example/v1", destination))
	}
	for _, destination := range []string{"http://api.example/v1", "https://other.example/v1", "https://api.example:444/v1", "https://api.example/v10/users", "https://api.example/v1/../secret", "https://api.example/v1/%2e%2e/secret", "https://api.example/v1/a%2fb", "https://api.example/v1/a%5cb", "https://api.example/v1/%252e%252e/secret", "https://user@api.example/v1"} {
		require.True(t, xerror.Is(ValidateDestination("https://api.example/v1", destination), ErrConfig), destination)
	}
}

func TestConfigurationRejectsUnsafeEndpoints(t *testing.T) {
	for _, endpoint := range []string{"http://issuer.example/token", "https://127.0.0.1/token", "https://[::1]/token", "https://user:secret@issuer.example/token", "https://issuer.example/token#fragment", "/token"} {
		config := testConfig()
		config.TokenEndpoint = endpoint
		_, err := New(config, "", "https://braidplane.example/callback", "https://api.example", false)
		require.True(t, xerror.Is(err, ErrConfig), endpoint)
	}
	config := testConfig()
	config.GrantType = "password"
	_, err := New(config, "secret", "", "https://api.example", false)
	require.True(t, xerror.Is(err, ErrConfig))
	config.GrantType = "client_credentials"
	_, err = New(config, "", "", "https://api.example", false)
	require.True(t, xerror.Is(err, ErrConfig))
}

func fakeClient(t *testing.T, handler http.HandlerFunc, grant string) *Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	config := testConfig()
	config.TokenEndpoint = server.URL + "/token"
	config.RevocationEndpoint = server.URL + "/revoke"
	config.GrantType = grant
	config.Audience = "https://api.example"
	client, err := New(config, "client-secret", "https://braidplane.example/callback", "https://api.example/v1", true)
	require.NoError(t, err)
	// Trust only this fake provider's certificate while preserving the production pinned dialer.
	client.client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, MinVersion: tls.VersionTLS12}
	return client
}

func TestTokenExchangeRefreshAndRevocation(t *testing.T) {
	calls := 0
	verifier := strings.Repeat("v", 43)
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "POST", r.Method)
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		assert.NoError(t, r.ParseForm())
		user, password, ok := r.BasicAuth()
		assert.True(t, ok)
		assert.Equal(t, "client", user)
		assert.Equal(t, "client-secret", password)
		assert.Empty(t, r.Form.Get("client_id"))
		switch calls {
		case 1:
			assert.Equal(t, "authorization_code", r.Form.Get("grant_type"))
			assert.Equal(t, "one-time-code", r.Form.Get("code"))
			assert.Equal(t, verifier, r.Form.Get("code_verifier"))
		case 2:
			assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
			assert.Equal(t, "refresh", r.Form.Get("refresh_token"))
		case 3:
			assert.Equal(t, "/revoke", r.URL.Path)
			assert.Equal(t, "refresh", r.Form.Get("token"))
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = fmt.Fprint(w, `{"access_token":"access","refresh_token":"refresh","token_type":"Bearer","expires_in":3600,"scope":"read"}`)
	}, "authorization_code")
	tokens, err := client.Exchange(context.Background(), "one-time-code", verifier, client.config.Issuer)
	require.NoError(t, err)
	require.Equal(t, "access", tokens.AccessToken)
	require.WithinDuration(t, time.Now().Add(time.Hour), tokens.ExpiresAt, time.Second)
	_, err = client.Refresh(context.Background(), tokens.RefreshToken, tokens.Scopes)
	require.NoError(t, err)
	require.NoError(t, client.Revoke(context.Background(), tokens.RefreshToken))
	require.Equal(t, 3, calls)
}

func TestClientCredentialsRenewal(t *testing.T) {
	calls := 0
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		assert.NoError(t, r.ParseForm())
		assert.Equal(t, "client_credentials", r.Form.Get("grant_type"))
		assert.Equal(t, "read", r.Form.Get("scope"))
		assert.Equal(t, "https://api.example", r.Form.Get("audience"))
		assert.Empty(t, r.Form.Get("refresh_token"))
		_, _ = fmt.Fprint(w, `{"access_token":"access","token_type":"bearer","expires_in":60}`)
	}, "client_credentials")
	_, err := client.ClientCredentials(context.Background())
	require.NoError(t, err)
	_, err = client.Refresh(context.Background(), "", nil)
	require.NoError(t, err)
	require.Equal(t, 2, calls)
}

func TestProviderFailureRedaction(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"denied", `{"error":"access_denied","error_description":"client-secret"}`, 400, ErrGrant},
		{"expired", `{"error":"invalid_grant","refresh_token":"refresh-secret"}`, 400, ErrGrant},
		{"server", `client-secret`, 500, ErrProvider},
		{"invalid json", `access-secret`, 200, ErrProvider},
		{"token type", `{"access_token":"access-secret","token_type":"mac","expires_in":60}`, 200, ErrProvider},
		{"expiration", `{"access_token":"access-secret","token_type":"bearer","expires_in":0}`, 200, ErrProvider},
		{"scope", `{"access_token":"access-secret","token_type":"bearer","expires_in":60,"scope":"write"}`, 200, ErrGrant},
		{"oversize", strings.Repeat("secret", 200000), 200, ErrProvider},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.body)
			}, "client_credentials")
			_, err := client.ClientCredentials(context.Background())
			require.True(t, xerror.Is(err, test.want))
			require.NotContains(t, err.Error(), "secret")
		})
	}
}

func TestRedirectDoesNotForwardClientSecret(t *testing.T) {
	forwarded := 0
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded++ }))
	defer destination.Close()
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}, "client_credentials")
	_, err := client.ClientCredentials(context.Background())
	require.True(t, xerror.Is(err, ErrProvider))
	require.Zero(t, forwarded)
}

func TestPrivateTokenHostBlockedAtDial(t *testing.T) {
	called := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()
	config := testConfig()
	config.GrantType = "client_credentials"
	config.TokenEndpoint = strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	client, err := New(config, "secret", "", "https://api.example", false)
	require.NoError(t, err)
	_, err = client.ClientCredentials(t.Context())
	require.True(t, xerror.Is(err, ErrProvider))
	require.False(t, called)
}

func TestPublicPKCEClientIdentification(t *testing.T) {
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		assert.NoError(t, r.ParseForm())
		assert.Equal(t, "client", r.Form.Get("client_id"))
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Empty(t, r.Form.Get("client_secret"))
		_, _ = fmt.Fprint(w, `{"access_token":"access","token_type":"bearer","expires_in":60}`)
	}, "authorization_code")
	client.secret = ""
	_, err := client.Exchange(t.Context(), "code", strings.Repeat("v", 43), client.config.Issuer)
	require.NoError(t, err)
}

func TestRefreshPreservesGrantedScopesWhenProviderOmitsScope(t *testing.T) {
	client := fakeClient(t, func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		assert.NoError(t, r.ParseForm())
		assert.Equal(t, "read audit", r.Form.Get("scope"))
		_, _ = fmt.Fprint(w, `{"access_token":"access","token_type":"Bearer","expires_in":60}`)
	}, "authorization_code")
	tokens, err := client.Refresh(t.Context(), "refresh", []string{"read", "audit"})
	require.NoError(t, err)
	require.Equal(t, []string{"read", "audit"}, tokens.Scopes)
	require.Equal(t, []string{"read"}, client.config.Scopes)
}
