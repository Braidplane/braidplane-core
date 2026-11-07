package client

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/stretchr/testify/require"
)

func TestTransportPolicyRejectsPlaintextEndpoint(t *testing.T) {
	t.Parallel()
	_, err := New(t.Context(), Config{Endpoint: "http://127.0.0.1:1", Region: "test", Bucket: "test", AllowPrivateHosts: true}, Credentials{AccessKeyID: "synthetic", SecretAccessKey: "synthetic"})
	require.ErrorContains(t, err, "requires HTTPS")
}

func TestTransportPolicyRejectsHTTPSRedirectDowngrade(t *testing.T) {
	t.Parallel()
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("plaintext destination contacted") }))
	defer destination.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer server.Close()
	client, err := newPinnedHTTPClient(defaultTimeout, hostguard.NewDestinationPolicy(true))
	require.NoError(t, err)
	defer client.CloseIdleConnections()
	client.Transport = server.Client().Transport
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	response, err := client.Do(request)
	if response != nil {
		require.NoError(t, response.Body.Close())
	}
	require.ErrorContains(t, err, "downgrade")
}
