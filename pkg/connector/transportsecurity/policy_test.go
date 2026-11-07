package transportsecurity

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPTransportEnforcesEncryptionAndCertificateVerification(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) })
	plaintext := httptest.NewServer(handler)
	defer plaintext.Close()
	encrypted := httptest.NewTLSServer(handler)
	defer encrypted.Close()
	client := &http.Client{Transport: HTTPTransport(http.DefaultTransport, false)}
	defer client.CloseIdleConnections()
	do := func(target string) error {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		require.NoError(t, err)
		response, err := client.Do(req)
		if response != nil {
			require.NoError(t, response.Body.Close())
		}
		return err
	}
	err := do(plaintext.URL)
	require.ErrorContains(t, err, "requires HTTPS")
	require.Zero(t, calls.Load())
	err = do(encrypted.URL)
	require.ErrorContains(t, err, "certificate")
	require.Zero(t, calls.Load())
	client.Transport = HTTPTransport(encrypted.Client().Transport, false)
	err = do(encrypted.URL)
	require.NoError(t, err)
	require.EqualValues(t, 1, calls.Load())
	// Explicit development permission allows plaintext; private-host routing does not grant this permission.
	client.Transport = HTTPTransport(http.DefaultTransport, PlaintextAllowed(WithPlaintext(t.Context())))
	err = do(plaintext.URL)
	require.NoError(t, err)
}

func TestRedirectNeverDowngradesEncryptedConnection(t *testing.T) {
	t.Parallel()
	for _, scheme := range []string{"http", "ftp", "https"} {
		req := &http.Request{URL: &url.URL{Scheme: scheme, Host: "example.test"}}
		via := []*http.Request{{URL: &url.URL{Scheme: "https", Host: "example.test"}}}
		err := CheckRedirect(req, via)
		if scheme == "https" {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, "downgrade")
		}
	}
}
