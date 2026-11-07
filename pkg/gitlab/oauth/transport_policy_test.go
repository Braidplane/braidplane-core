package oauth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTransportPolicyRejectsPlaintextOAuthWithoutExplicitPermission(t *testing.T) {
	t.Parallel()
	_, err := New(Config{ClientID: "synthetic", ClientSecret: "synthetic", RedirectURI: "https://example.test/callback", BaseURL: "http://127.0.0.1:1"})
	require.ErrorContains(t, err, "requires HTTPS")
}

func TestOAuthTokenExchangeNeverFollowsRedirects(t *testing.T) {
	t.Parallel()
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("OAuth credential redirect followed") }))
	defer destination.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, err := New(Config{ClientID: "synthetic", ClientSecret: "synthetic", RedirectURI: "https://example.test/callback", BaseURL: server.URL})
	require.NoError(t, err)
	client.client.Transport = server.Client().Transport
	_, err = client.ExchangeCode(t.Context(), "synthetic-code")
	require.Error(t, err)
}
