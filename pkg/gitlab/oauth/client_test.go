package oauth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListProjectsFollowsGitLabPagination(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		if got := r.URL.Query().Get("membership"); got != "true" {
			t.Errorf("membership = %q, want true", got)
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			_, _ = w.Write([]byte(`[{"id":1,"name":"One","path_with_namespace":"acme/one"}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":2,"name":"Two","path_with_namespace":"acme/two"}]`))
	}))
	t.Cleanup(server.Close)

	client, err := New(Config{AllowPlaintext: true, ClientID: "id", ClientSecret: "secret", RedirectURI: "https://example.com/callback", BaseURL: server.URL})
	require.NoError(t, err)

	projects, err := client.ListProjects(t.Context(), "access-token")
	require.NoError(t, err)
	require.Len(t, projects, 2)
	require.Equal(t, int64(2), projects[1].ID)
}
