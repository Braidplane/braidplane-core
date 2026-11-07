package client

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"testing/iotest"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestTransportPolicyRejectsPlaintextProviderAndTokenEndpoints(t *testing.T) {
	t.Parallel()
	for _, cfg := range []Config{
		{BaseURL: "http://127.0.0.1:1", AllowPrivateHosts: true},
		{TokenURL: "http://127.0.0.1:1/token", AllowPrivateHosts: true},
	} {
		_, err := New(t.Context(), cfg, Credentials{AccessToken: "synthetic-token"})
		require.ErrorContains(t, err, "requires HTTPS")
	}
}

func TestProviderFailuresLogOriginalBeforeSanitizing(t *testing.T) {
	cause := xerror.Unexpected().Detail("upstream connection interrupted password=fixture-password")
	for _, operation := range []string{"response read", "token transport"} {
		t.Run(operation, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			var err error
			if operation == "response read" {
				_, err = readProviderResponse(t.Context(), &http.Response{Body: io.NopCloser(iotest.ErrReader(cause))}, 1024)
				require.ErrorIs(t, err, xerror.Unexpected())
			} else {
				transport := tokenTransport{ctx: t.Context(), client: &http.Client{Transport: metadataTransport(func(*http.Request) (*http.Response, error) {
					return nil, cause
				})}}
				req, requestErr := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://provider.test/token", nil)
				require.NoError(t, requestErr)
				response, transportErr := transport.RoundTrip(req)
				if response != nil {
					require.NoError(t, response.Body.Close())
				}
				err = transportErr
				require.ErrorIs(t, err, xerror.Unavailable(xerror.DataSource))
			}
			require.NotContains(t, err.Error(), "upstream connection interrupted")
			require.Contains(t, logs.String(), "upstream connection interrupted")
			require.NotContains(t, logs.String(), "fixture-password")
		})
	}
}
