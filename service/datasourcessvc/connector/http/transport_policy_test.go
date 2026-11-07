package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/stretchr/testify/require"
)

func TestTransportPolicyRejectsPlaintextBeforeSendingCredentials(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("plaintext endpoint contacted") }))
	defer server.Close()
	_, err := New(t.Context(), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{BearerToken: "synthetic-token"})
	require.ErrorContains(t, err, "requires HTTPS")
}

func TestTransportPolicyRejectsDowngradeForConnectionAndExecution(t *testing.T) {
	t.Parallel()
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("downgrade destination contacted") }))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	c, err := New(t.Context(), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	defer func() { require.NoError(t, c.Close()) }()
	c.client.Transport = transportsecurity.HTTPTransport(server.Client().Transport, false)
	require.ErrorContains(t, c.TestConnection(t.Context()), "downgrade")
	result, err := c.Execute(t.Context(), []byte(`{"method":"GET","path":"/"}`), nil)
	require.NoError(t, err)
	require.ErrorContains(t, result.Error(), "downgrade")
}

func TestOpenAPITransportRejectsPlaintextWithoutDevelopmentPolicy(t *testing.T) {
	t.Parallel()
	_, err := fetchOpenAPISpecWithDialer(t.Context(), "http://127.0.0.1:1/schema", 1, permissiveDialContext(), true, http.Header{"Authorization": []string{"Bearer synthetic"}}, "")
	require.ErrorContains(t, err, "requires HTTPS")
}
