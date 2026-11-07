package gitlab_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	gitlabprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/gitlab"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
)

func TestClient_BootstrapRepositoryCreatesPrivateProjectAndOneMultiFileCommit(t *testing.T) {
	t.Parallel()

	var marker string
	var commitBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines":
			w.WriteHeader(http.StatusNotFound)
		case "GET /api/v4/namespaces/acme%2Fplatform":
			writeJSON(t, w, map[string]any{"id": 9, "kind": "group", "full_path": "Acme/Platform"})
		case "POST /api/v4/projects":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			marker, _ = body["description"].(string)
			assert.Equal(t, "pipelines", body["name"])
			assert.Equal(t, "pipelines", body["path"])
			assert.InDelta(t, 9, body["namespace_id"], 0)
			assert.Equal(t, "private", body["visibility"])
			assert.Equal(t, false, body["initialize_with_readme"])
			writeJSON(t, w, map[string]any{"id": 42, "description": marker, "visibility": "private", "path_with_namespace": "Acme/Platform/Pipelines", "http_url_to_repo": "https://gitlab.com/acme/platform/pipelines.git"})
		case "POST /api/v4/projects/42/repository/commits":
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&commitBody))
			writeJSON(t, w, map[string]any{"id": "first-commit-sha"})
		case "PUT /api/v4/projects/42":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "Git-first Braidplane pipeline definitions", body["description"])
			assert.Equal(t, "trunk", body["default_branch"])
			writeJSON(t, w, map[string]any{"id": 42})
		default:
			t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.EscapedPath())
		}
	}))
	t.Cleanup(server.Close)

	got, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{
		Owner: "acme/platform", Name: "pipelines", DefaultBranch: "refs/heads/trunk",
		Files: []gitprovider.RepositoryFile{{Path: "pipelines/README.md", Content: "one"}, {Path: ".braidplane/template.yaml", Content: "two"}},
	})

	require.NoError(t, err)
	require.NotEmpty(t, marker)
	require.Equal(t, "first-commit-sha", got.HeadSHA)
	require.Equal(t, "42", got.RepositoryID)
	require.Equal(t, "https://gitlab.com/acme/platform/pipelines.git", got.RemoteURL)
	require.Equal(t, "group", got.OwnerKind)
	require.Equal(t, "trunk", commitBody["branch"])
	actions := commitBody["actions"].([]any)
	require.Len(t, actions, 2)
	require.Equal(t, "create", actions[0].(map[string]any)["action"])
	require.Equal(t, "pipelines/README.md", actions[0].(map[string]any)["file_path"])
}

func TestClient_BootstrapRepositorySupportsUserNamespace(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v4/projects/alice%2Fpipelines":
			w.WriteHeader(http.StatusNotFound)
		case "GET /api/v4/namespaces/alice":
			writeJSON(t, w, map[string]any{"id": 3, "kind": "user", "full_path": "alice"})
		case "POST /api/v4/projects":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			writeJSON(t, w, map[string]any{"id": 5, "description": body["description"], "visibility": "private", "path_with_namespace": "alice/pipelines", "http_url_to_repo": "https://gitlab.com/alice/pipelines.git"})
		case "POST /api/v4/projects/5/repository/commits":
			writeJSON(t, w, map[string]any{"id": "sha"})
		case "PUT /api/v4/projects/5":
			writeJSON(t, w, map[string]any{"id": 5})
		default:
			t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.EscapedPath())
		}
	}))
	t.Cleanup(server.Close)

	client, err := gitlabprovider.New(transportsecurity.WithPlaintext(context.Background()), gitprovider.Config{BaseURL: server.URL + "/api/v4", AllowPrivateHosts: true}, gitprovider.Credentials{Token: "glpat-secret"}, gitprovider.RepositoryRef{Provider: repostore.ProviderGitLab, Owner: "alice", Name: "pipelines", ProjectPath: "alice/pipelines", FullName: "alice/pipelines"})
	require.NoError(t, err)
	got, err := client.BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "alice", Name: "pipelines", DefaultBranch: "main", Files: []gitprovider.RepositoryFile{{Path: "README.md", Content: "safe"}}})
	require.NoError(t, err)
	require.Equal(t, "user", got.OwnerKind)
}

func TestClient_BootstrapRepositoryNeverReplacesKnownProjectIdentity(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{true: "missing", false: "replaced"}[missing], func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !assert.Equal(t, "GET /api/v4/projects/acme%2Fplatform%2Fpipelines", r.Method+" "+r.URL.EscapedPath()) {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if missing {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				writeJSON(t, w, map[string]any{"id": 43, "description": "Git-first Braidplane pipeline definitions [setup-123]", "visibility": "private", "path_with_namespace": "acme/platform/pipelines", "http_url_to_repo": "https://gitlab.com/acme/platform/pipelines.git"})
			}))
			t.Cleanup(server.Close)
			got, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme/platform", Name: "pipelines", DefaultBranch: "main", Marker: "setup-123", ExpectedRepositoryID: "42", Files: []gitprovider.RepositoryFile{{Path: "README.md", Content: "safe"}}})
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}

func TestClient_BootstrapRepositoryCleansUpOnlyOwnedPartialProject(t *testing.T) {
	t.Parallel()
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines":
			w.WriteHeader(http.StatusNotFound)
		case "GET /api/v4/namespaces/acme%2Fplatform":
			writeJSON(t, w, map[string]any{"id": 9, "kind": "group", "full_path": "acme/platform"})
		case "POST /api/v4/projects":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			writeJSON(t, w, map[string]any{"id": 42, "description": body["description"], "visibility": "private", "path_with_namespace": "acme/platform/pipelines"})
		case "POST /api/v4/projects/42/repository/commits":
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(t, w, map[string]any{"message": "commit failed"})
		case "DELETE /api/v4/projects/42":
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.EscapedPath())
		}
	}))
	t.Cleanup(server.Close)

	_, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme/platform", Name: "pipelines", DefaultBranch: "main", Files: []gitprovider.RepositoryFile{{Path: "README.md", Content: "safe"}}})
	require.ErrorContains(t, err, "commit failed")
	require.True(t, deleted.Load())
}

func TestClient_BootstrapRepositoryResumesExactInitializedMarkerProject(t *testing.T) {
	t.Parallel()
	var commits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines":
			writeJSON(t, w, map[string]any{"id": 42, "description": "Git-first Braidplane pipeline definitions [setup-123]", "visibility": "private", "path_with_namespace": "Acme/Platform/Pipelines", "http_url_to_repo": "https://gitlab.com/acme/platform/pipelines.git", "empty_repo": false, "namespace": map[string]any{"kind": "group"}})
		case "GET /api/v4/projects/42/repository/commits/main":
			writeJSON(t, w, map[string]any{"id": "first-sha", "parent_ids": []string{}})
		case "GET /api/v4/projects/42/repository/tree":
			writeJSON(t, w, []map[string]any{{"type": "tree", "mode": "040000", "path": ".braidplane"}, {"type": "blob", "mode": "100644", "path": "README.md"}, {"type": "blob", "mode": "100644", "path": ".braidplane/template.yaml"}})
		case "GET /api/v4/projects/42/repository/files/README%2Emd/raw":
			_, _ = w.Write([]byte("one"))
		case "GET /api/v4/projects/42/repository/files/%2Ebraidplane%2Ftemplate%2Eyaml/raw":
			_, _ = w.Write([]byte("two"))
		case "POST /api/v4/projects/42/repository/commits":
			commits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.EscapedPath())
		}
	}))
	t.Cleanup(server.Close)

	got, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme/platform", Name: "pipelines", DefaultBranch: "main", Marker: "setup-123", Files: []gitprovider.RepositoryFile{{Path: "README.md", Content: "one"}, {Path: ".braidplane/template.yaml", Content: "two"}}})
	require.NoError(t, err)
	require.Equal(t, "42", got.RepositoryID)
	require.Equal(t, "first-sha", got.HeadSHA)
	require.Equal(t, "group", got.OwnerKind)
	require.Zero(t, commits.Load())
}

func TestClient_BootstrapRepositoryResumesEmptyMarkerProjectAndPreservesMarker(t *testing.T) {
	t.Parallel()
	var commits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines":
			writeJSON(t, w, map[string]any{"id": 42, "description": "Git-first Braidplane pipeline definitions [setup-123]", "visibility": "private", "path_with_namespace": "acme/platform/pipelines", "empty_repo": true})
		case "POST /api/v4/projects/42/repository/commits":
			commits.Add(1)
			writeJSON(t, w, map[string]any{"id": "first-sha"})
		case "PUT /api/v4/projects/42":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "main", body["default_branch"])
			assert.NotContains(t, body, "description")
			writeJSON(t, w, map[string]any{"id": 42})
		default:
			t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.EscapedPath())
		}
	}))
	t.Cleanup(server.Close)

	got, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme/platform", Name: "pipelines", DefaultBranch: "main", Marker: "setup-123", Files: []gitprovider.RepositoryFile{{Path: "README.md", Content: "one"}}})
	require.NoError(t, err)
	require.Equal(t, "first-sha", got.HeadSHA)
	require.Equal(t, int32(1), commits.Load())
}

func TestClient_BootstrapRepositoryRefusesEditedOrUnrelatedMarkerProject(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, description string
		parents           []string
	}{
		{name: "unrelated", description: "created elsewhere"},
		{name: "edited", description: "Git-first Braidplane pipeline definitions [setup-123]", parents: []string{"prior-sha"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.EscapedPath() {
				case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines":
					writeJSON(t, w, map[string]any{"id": 42, "description": tc.description, "visibility": "private", "path_with_namespace": "acme/platform/pipelines", "empty_repo": false})
				case "GET /api/v4/projects/42/repository/commits/main":
					writeJSON(t, w, map[string]any{"id": "head-sha", "parent_ids": tc.parents})
				default:
					writes.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			t.Cleanup(server.Close)
			got, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme/platform", Name: "pipelines", DefaultBranch: "main", Marker: "setup-123", Files: []gitprovider.RepositoryFile{{Path: "README.md", Content: "one"}}})
			require.Error(t, err)
			if tc.name == "edited" {
				require.Equal(t, "42", got.RepositoryID)
			} else {
				require.Nil(t, got)
			}
			require.Zero(t, writes.Load())
		})
	}
}

func TestClient_BootstrapRepositoryReturnsStableProvenanceOnPartialFailureWithoutCleanup(t *testing.T) {
	t.Parallel()
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines":
			w.WriteHeader(http.StatusNotFound)
		case "GET /api/v4/namespaces/acme%2Fplatform":
			writeJSON(t, w, map[string]any{"id": 9, "kind": "group", "full_path": "acme/platform"})
		case "POST /api/v4/projects":
			writeJSON(t, w, map[string]any{"id": 42, "description": "Git-first Braidplane pipeline definitions [setup-123]", "visibility": "private", "path_with_namespace": "acme/platform/pipelines", "http_url_to_repo": "https://gitlab.com/acme/platform/pipelines.git"})
		case "POST /api/v4/projects/42/repository/commits":
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(t, w, map[string]any{"message": "commit failed"})
		case "DELETE /api/v4/projects/42":
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.EscapedPath())
		}
	}))
	t.Cleanup(server.Close)

	got, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme/platform", Name: "pipelines", DefaultBranch: "main", Marker: "setup-123"})
	require.ErrorContains(t, err, "commit failed")
	require.Equal(t, "42", got.RepositoryID)
	require.Equal(t, "https://gitlab.com/acme/platform/pipelines.git", got.RemoteURL)
	require.False(t, deleted.Load())
}

func TestClient_BootstrapRepositoryRefusesPendingDeletionProject(t *testing.T) {
	t.Parallel()
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines":
			writeJSON(t, w, map[string]any{"id": 42, "description": "Git-first Braidplane pipeline definitions [setup-123]", "visibility": "private", "path_with_namespace": "acme/platform/pipelines", "http_url_to_repo": "https://gitlab.com/acme/platform/pipelines.git", "marked_for_deletion_on": "2026-10-21"})
		default:
			writes.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	got, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme/platform", Name: "pipelines", DefaultBranch: "main", Marker: "setup-123"})
	require.ErrorContains(t, err, "pending deletion")
	require.Equal(t, "42", got.RepositoryID)
	require.Zero(t, writes.Load())
}

func TestClient_BootstrapRepositoryDoesNotDeleteDeniedOrConflictingProject(t *testing.T) {
	t.Parallel()
	for name, createStatus := range map[string]int{"denied": http.StatusForbidden, "conflict": http.StatusConflict} {
		t.Run(name, func(t *testing.T) {
			var deleted atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.EscapedPath() {
				case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines":
					w.WriteHeader(http.StatusNotFound)
				case "GET /api/v4/namespaces/acme%2Fplatform":
					writeJSON(t, w, map[string]any{"id": 9, "kind": "group", "full_path": "acme/platform"})
				case "POST /api/v4/projects":
					w.WriteHeader(createStatus)
					writeJSON(t, w, map[string]any{"message": "create denied"})
				case "DELETE /api/v4/projects/42":
					deleted.Store(true)
				default:
					t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.EscapedPath())
				}
			}))
			t.Cleanup(server.Close)

			_, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme/platform", Name: "pipelines", DefaultBranch: "main"})
			require.Error(t, err)
			require.False(t, deleted.Load())
		})
	}
}

func TestClient_BootstrapRepositoryRecoversOnlyMatchingAmbiguousCreate(t *testing.T) {
	t.Parallel()
	var marker string
	var gets atomic.Int32
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines":
			if gets.Add(1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(t, w, map[string]any{"id": 42, "description": marker, "visibility": "private", "path_with_namespace": "acme/platform/pipelines", "http_url_to_repo": "https://gitlab.com/acme/platform/pipelines.git", "empty_repo": true})
		case "GET /api/v4/namespaces/acme%2Fplatform":
			writeJSON(t, w, map[string]any{"id": 9, "kind": "group", "full_path": "acme/platform"})
		case "POST /api/v4/projects":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			marker, _ = body["description"].(string)
			connection, _, err := w.(http.Hijacker).Hijack()
			if !assert.NoError(t, err) {
				return
			}
			assert.NoError(t, connection.Close())
		case "POST /api/v4/projects/42/repository/commits":
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(t, w, map[string]any{"message": "commit failed"})
		case "DELETE /api/v4/projects/42":
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.EscapedPath())
		}
	}))
	t.Cleanup(server.Close)

	got, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme/platform", Name: "pipelines", DefaultBranch: "main", Marker: "setup-123"})
	require.ErrorContains(t, err, "commit failed")
	require.Equal(t, "42", got.RepositoryID)
	require.False(t, deleted.Load(), "an ambiguously created project is never safe to delete")
}

func TestClient_BootstrapRepositoryDoesNotOwnAmbiguousProjectWithDifferentMarker(t *testing.T) {
	t.Parallel()
	var gets atomic.Int32
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines":
			if gets.Add(1) == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(t, w, map[string]any{"id": 42, "description": "created elsewhere", "visibility": "private", "path_with_namespace": "acme/platform/pipelines"})
		case "GET /api/v4/namespaces/acme%2Fplatform":
			writeJSON(t, w, map[string]any{"id": 9, "kind": "group", "full_path": "acme/platform"})
		case "POST /api/v4/projects":
			connection, _, err := w.(http.Hijacker).Hijack()
			if !assert.NoError(t, err) {
				return
			}
			assert.NoError(t, connection.Close())
		case "DELETE /api/v4/projects/42":
			deleted.Store(true)
		default:
			t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.EscapedPath())
		}
	}))
	t.Cleanup(server.Close)

	_, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme/platform", Name: "pipelines", DefaultBranch: "main"})
	require.Error(t, err)
	require.False(t, deleted.Load())
}

func TestClient_BootstrapRepositoryJoinsFailedCleanup(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines":
			w.WriteHeader(http.StatusNotFound)
		case "GET /api/v4/namespaces/acme%2Fplatform":
			writeJSON(t, w, map[string]any{"id": 9, "kind": "group", "full_path": "acme/platform"})
		case "POST /api/v4/projects":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			writeJSON(t, w, map[string]any{"id": 42, "description": body["description"], "visibility": "private", "path_with_namespace": "acme/platform/pipelines"})
		case "POST /api/v4/projects/42/repository/commits":
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(t, w, map[string]any{"message": "commit failed"})
		case "DELETE /api/v4/projects/42":
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(t, w, map[string]any{"message": "cleanup failed"})
		default:
			t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.EscapedPath())
		}
	}))
	t.Cleanup(server.Close)

	_, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme/platform", Name: "pipelines", DefaultBranch: "main"})
	require.ErrorContains(t, err, "commit failed")
	require.ErrorContains(t, err, "cleanup failed")
}

func TestClient_DeleteRepositoryRequiresMatchingImmutableIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, id, path string
		wantDelete     bool
	}{
		{"missing ID", "", "acme/platform/pipelines", false},
		{"invalid ID", "abc", "acme/platform/pipelines", false},
		{"different repository", "42", "other/project", false},
		{"created repository", "42", "acme/platform/pipelines", true},
		{"canonical repository casing", "42", "Acme/Platform/Pipelines", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var deletes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.EscapedPath() {
				case "GET /api/v4/projects/42":
					writeJSON(t, w, map[string]any{"id": 42, "path_with_namespace": tc.path})
				case "DELETE /api/v4/projects/42":
					deletes.Add(1)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected GitLab request: %s %s", r.Method, r.URL.EscapedPath())
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			err := newClient(t, server.URL).DeleteRepository(transportsecurity.WithPlaintext(t.Context()), tc.id)
			if tc.wantDelete {
				require.NoError(t, err)
				require.Equal(t, int32(1), deletes.Load())
			} else {
				require.Error(t, err)
				require.Zero(t, deletes.Load())
			}
		})
	}
}

func TestClient_ValidateRepositoryRequiresWritePermissionAndSelectedBranch(t *testing.T) {
	t.Parallel()
	for name, project := range map[string]map[string]any{
		"forbidden": {"permissions": map[string]any{"project_access": map[string]any{"access_level": 20}}},
		"allowed":   {"permissions": map[string]any{"group_access": map[string]any{"access_level": 30}}},
	} {
		t.Run(name, func(t *testing.T) {
			var branchChecked atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.EscapedPath() {
				case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines":
					writeJSON(t, w, project)
				case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines/repository/branches/main":
					branchChecked.Store(true)
					writeJSON(t, w, map[string]any{"name": "main"})
				default:
					t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.EscapedPath())
				}
			}))
			t.Cleanup(server.Close)

			err := newClient(t, server.URL).ValidateRepository(transportsecurity.WithPlaintext(t.Context()), "refs/heads/main")
			if name == "forbidden" {
				require.ErrorContains(t, err, "cannot write")
				require.False(t, branchChecked.Load())
				return
			}
			require.NoError(t, err)
			require.True(t, branchChecked.Load())
		})
	}
}

func TestClient_BootstrapResumeRejectsTemplateSymlinkWithoutMutation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.EscapedPath() {
		case "GET /api/v4/projects/acme%2Fplatform%2Fpipelines":
			writeJSON(t, w, map[string]any{"id": 42, "description": "Git-first Braidplane pipeline definitions [setup-123]", "visibility": "private", "path_with_namespace": "acme/platform/pipelines", "empty_repo": false})
		case "GET /api/v4/projects/42/repository/commits/main":
			writeJSON(t, w, map[string]any{"id": "initial-sha", "parent_ids": []string{}})
		case "GET /api/v4/projects/42/repository/tree":
			writeJSON(t, w, []map[string]any{{"type": "blob", "mode": "120000", "path": "README.md"}})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	got, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme/platform", Name: "pipelines", DefaultBranch: "main", Marker: "setup-123", Files: []gitprovider.RepositoryFile{{Path: "README.md", Content: "one"}}})
	require.ErrorContains(t, err, "does not match")
	require.Equal(t, "42", got.RepositoryID)
	require.Empty(t, got.HeadSHA)
}
