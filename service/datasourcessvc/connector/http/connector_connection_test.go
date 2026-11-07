package http

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestTestConnectionClassifiesHealthStatusesWithoutProviderDetail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status int
		want   error
		detail string
	}{
		{status: http.StatusUnauthorized, want: xerror.InvalidCredentials(), detail: "Authentication failed (HTTP status 401). Check credentials and try again."},
		{status: http.StatusForbidden, want: xerror.Forbidden(xerror.DataSource), detail: "Permission denied (HTTP status 403). Check the connection permissions."},
		{status: http.StatusNotFound, want: xerror.NotFound(xerror.DataSource), detail: "Endpoint not found (HTTP status 404). Check the health endpoint."},
		{status: http.StatusRequestTimeout, want: xerror.Timeout(xerror.DataSource), detail: "Connection timed out (HTTP status 408). Try again."},
		{status: http.StatusTooManyRequests, want: xerror.Unavailable(xerror.DataSource), detail: "Rate limited (HTTP status 429). Wait and try again."},
		{status: http.StatusGatewayTimeout, want: xerror.Timeout(xerror.DataSource), detail: "Connection timed out (HTTP status 504). Try again."},
		{status: http.StatusInternalServerError, want: xerror.Unavailable(xerror.DataSource), detail: "Server error (HTTP status 500). Try again later."},
		{status: http.StatusTeapot, want: xerror.ConnectionFailed(xerror.DataSource), detail: "Health endpoint returned HTTP status 418."},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("status %d", tt.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("provider diagnostic"))
			}))
			defer server.Close()

			connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			err = connector.TestConnection(transportsecurity.WithPlaintext(context.Background()))

			require.True(t, xerror.Is(err, tt.want))
			require.Equal(t, tt.detail, xerror.PublicDetail(err))
			require.NotContains(t, err.Error(), "provider diagnostic")
			require.NotContains(t, xerror.PublicDetail(err), "provider diagnostic")
		})
	}
}
