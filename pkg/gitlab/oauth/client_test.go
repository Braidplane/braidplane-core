package oauth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListProjectsFollowsGitLabPagination(t *testing.T) {
	t.Parallel()

	var fixtureOrigin string
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		if got := r.URL.Query().Get("membership"); got != "true" {
			t.Errorf("membership = %q, want true", got)
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
			_, _ = fmt.Fprintf(w, `[{"id":1,"name":"One","path_with_namespace":"acme/one","web_url":%q},{"id":3,"name":"Outside","web_url":"https://other.example/acme/three"}]`, fixtureOrigin+"/acme/one")
			return
		}
		_, _ = fmt.Fprintf(w, `[{"id":2,"name":"Two","path_with_namespace":"acme/two","web_url":%q}]`, fixtureOrigin+"/acme/two")
	}))
	fixtureOrigin = "http://" + server.Listener.Addr().String()
	server.Start()
	t.Cleanup(server.Close)

	client, err := New(Config{AllowPlaintext: true, ClientID: "id", ClientSecret: "secret", RedirectURI: "https://example.com/callback", BaseURL: server.URL})
	require.NoError(t, err)

	projects, err := client.ListProjects(t.Context(), "access-token")
	require.NoError(t, err)
	require.Len(t, projects, 2)
	require.Equal(t, int64(2), projects[1].ID)
}

func TestProjectWebURLMatchesConfiguredOrigin(t *testing.T) {
	t.Parallel()
	client := &Client{baseURL: "https://git.example.com/gitlab"}
	require.True(t, client.validProjectWebURL("https://GIT.example.com:443/gitlab/group/subgroup/repository"))
	require.False(t, client.validProjectWebURL("https://git.example.com:8443/gitlab/group/repository"))
	require.False(t, client.validProjectWebURL("https://other.example/gitlab/group/repository"))
	require.False(t, client.validProjectWebURL("https://user@git.example.com/gitlab/group/repository"))
	require.False(t, client.validProjectWebURL("http://git.example.com/gitlab/group/repository"))
	require.False(t, client.validProjectWebURL("/gitlab/group/repository"))
	client.baseURL = "https://bücher.example"
	require.True(t, client.validProjectWebURL("https://xn--bcher-kva.example:0443/repository"))
	client.baseURL = "https://[2001:db8::1]"
	require.True(t, client.validProjectWebURL("https://[2001:0db8:0:0::1]/repository"))
}

func TestGetProjectRejectsWebURLOutsideConfiguredOrigin(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":42,"name":"Reports","web_url":"https://other.example/acme/reports"}`))
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{
		ClientID: "id", ClientSecret: "secret", RedirectURI: "https://example.com/callback",
		BaseURL: server.URL, AllowPlaintext: true,
	})
	require.NoError(t, err)
	_, err = client.GetProject(t.Context(), "test-token", "42")
	require.ErrorContains(t, err, "outside its configured origin")
}
