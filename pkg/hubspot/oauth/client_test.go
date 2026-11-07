package oauth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// TestClientMapsOAuthErrorsByProviderErrorCode verifies only invalid_grant revokes a credential grant.
func TestClientMapsOAuthErrorsByProviderErrorCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		path        string
		status      int
		body        string
		want        error
		wantInvalid bool
	}{
		{
			name:        "ExchangeInvalidGrant",
			path:        "/oauth/2026-03/token",
			status:      http.StatusBadRequest,
			body:        `{"error":"invalid_grant"}`,
			want:        ErrInvalidGrant,
			wantInvalid: true,
		},
		{
			name:   "ExchangeMalformedRequest",
			path:   "/oauth/2026-03/token",
			status: http.StatusBadRequest,
			body:   `{"error":"invalid_request"}`,
			want:   xerror.InvalidInput(xerror.DataSource),
		},
		{
			name:   "ExchangeInvalidClient",
			path:   "/oauth/2026-03/token",
			status: http.StatusUnauthorized,
			body:   `{"error":"invalid_client"}`,
			want:   xerror.InvalidCredentials(),
		},
		{
			name:   "ExchangeForbiddenClient",
			path:   "/oauth/2026-03/token",
			status: http.StatusForbidden,
			body:   `{"error":"access_denied"}`,
			want:   xerror.Forbidden(xerror.DataSource),
		},
		{
			name:        "RevokeInvalidGrant",
			path:        "/oauth/2026-03/token/revoke",
			status:      http.StatusBadRequest,
			body:        `{"error":"invalid_grant"}`,
			want:        ErrInvalidGrant,
			wantInvalid: true,
		},
		{
			name:   "RevokeInvalidClient",
			path:   "/oauth/2026-03/token/revoke",
			status: http.StatusUnauthorized,
			body:   `{"error":"invalid_client"}`,
			want:   xerror.InvalidCredentials(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, tt.path, r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)

			client := newTestClient(t, server.URL)
			var err error
			if tt.path == "/oauth/2026-03/token" {
				_, err = client.Refresh(t.Context(), "refresh-token")
			} else {
				err = client.Revoke(t.Context(), "refresh-token")
			}
			require.Error(t, err)
			assert.True(t, xerror.Is(err, tt.want))
			assert.Equal(t, tt.wantInvalid, xerror.Is(err, ErrInvalidGrant))
		})
	}
}

// newTestClient constructs a client against a local mock OAuth endpoint.
func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()

	client, err := New(Config{
		AllowPlaintext: true,
		ClientID:       "client-id",
		ClientSecret:   "client-secret",
		RedirectURI:    "http://localhost:3000/provider-connectors/hubspot/callback",
		BaseURL:        baseURL,
	})
	require.NoError(t, err)
	return client
}
