package client

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type metadataTransport func(*http.Request) (*http.Response, error)

func (f metadataTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOAuthMetadataUsesOnlyExactReadWithResolvedToken(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			transport := &http.Client{Transport: metadataTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "https://bigquery.googleapis.com/bigquery/v2/projects/fixture/datasets/crm/tables/contacts", r.URL.String())
				require.Equal(t, "Bearer fixture-access", r.Header.Get("Authorization"))
				body := `{"tableReference":{"projectId":"fixture","datasetId":"crm","tableId":"contacts"},"type":"TABLE","schema":{"fields":[{"name":"id","type":"STRING"}]}}`
				return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			got, err := ReadTableMetadata(transportsecurity.WithPlaintext(t.Context()), transport, Credentials{AccessToken: "fixture-access"}, GetTableRequest{ProjectID: "fixture", DatasetID: "crm", TableID: "contacts"})
			if status == http.StatusOK {
				require.NoError(t, err)
				require.Equal(t, "contacts", got.TableID)
			} else {
				require.Error(t, err)
				require.Nil(t, got)
			}
			require.Equal(t, 1, calls, "OAuth metadata must not exchange or refresh tokens")
		})
	}
}

func TestOAuthConnectionUsesResolvedToken(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/bigquery/v2/projects/fixture/datasets/crm", r.URL.Path)
		assert.Equal(t, "Bearer fixture-access", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"datasets":[]}`)
	})
	c, err := New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: server.URL, TokenURL: server.URL + "/token", AllowPrivateHosts: true}, Credentials{AccessToken: "fixture-access"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	require.NoError(t, c.TestConnection(transportsecurity.WithPlaintext(t.Context()), TestConnectionRequest{ProjectID: "fixture", DatasetID: "crm"}))
}

func TestOAuthRejectsAmbiguousOrEmptyCredentials(t *testing.T) {
	t.Parallel()
	for _, creds := range []Credentials{{}, {AccessToken: " "}, {AccessToken: "fixture", ServiceAccountJSON: "{}"}} {
		_, err := New(transportsecurity.WithPlaintext(t.Context()), Config{}, creds)
		require.ErrorIs(t, err, xerror.InvalidCredentials())
	}
}

func TestResolvedTokenRequestsNeverFollowRedirectsOrRetry(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.NotEqual(t, "/destination", r.URL.Path)
				assert.NotEqual(t, "/token", r.URL.Path)
				assert.Equal(t, "Bearer fixture-access", r.Header.Get("Authorization"))
				w.Header().Set("Location", "/destination")
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
			})
			c, err := New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: server.URL, TokenURL: server.URL + "/token", AllowPrivateHosts: true}, Credentials{AccessToken: "fixture-access"})
			require.NoError(t, err)
			t.Cleanup(func() { _ = c.Close() })
			require.ErrorIs(t, c.TestConnection(transportsecurity.WithPlaintext(t.Context()), TestConnectionRequest{ProjectID: "fixture"}), xerror.Unavailable(xerror.DataSource))
			_, err = c.InsertRows(transportsecurity.WithPlaintext(t.Context()), InsertRowsRequest{ProjectID: "fixture", DatasetID: "crm", TableID: "contacts", Rows: []map[string]any{{"id": "1"}}})
			require.ErrorIs(t, err, xerror.Unavailable(xerror.DataSource))
			require.Equal(t, 2, calls, "each read/write operation must dispatch once")
		})
	}
}
