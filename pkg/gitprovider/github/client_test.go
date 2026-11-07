package github_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
)

func TestClient_ListOpenProposals(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/repos/acme/pipelines/pulls", r.URL.Path)
		assert.Equal(t, "Bearer ghp_secret", r.Header.Get("Authorization"))
		assert.Equal(t, "open", r.URL.Query().Get("state"))
		assert.Equal(t, "acme:braidplane/default/daily-sync/member-a", r.URL.Query().Get("head"))
		assert.Equal(t, "main", r.URL.Query().Get("base"))
		writeJSON(t, w, []map[string]any{{
			"number":   42,
			"html_url": "https://github.com/acme/pipelines/pull/42",
			"state":    "open",
			"head": map[string]any{
				"ref": "braidplane/default/daily-sync/member-a",
				"sha": "abc123",
			},
			"base": map[string]any{
				"ref": "main",
			},
			"merged": false,
		}})
	}))
	t.Cleanup(server.Close)

	client := newClient(t, server.URL)

	got, err := client.ListOpenProposals(transportsecurity.WithPlaintext(context.Background()), gitprovider.ProposalListRequest{
		SourceBranch: "braidplane/default/daily-sync/member-a",
		TargetBranch: "refs/heads/main",
	})

	require.NoError(t, err)
	require.Equal(t, []gitprovider.ProposalRef{{
		Provider:     repostore.ProviderGitHub,
		Repository:   githubRepoRef(),
		ID:           "42",
		URL:          "https://github.com/acme/pipelines/pull/42",
		SourceBranch: "braidplane/default/daily-sync/member-a",
		TargetBranch: "main",
		State:        gitprovider.ProposalStateOpen,
		HeadSHA:      "abc123",
		ProviderKind: gitprovider.ProposalKindPullRequest,
		Closed:       false,
		Merged:       false,
	}}, got)
}

func TestClient_PublishProofCheckUpdatesMatchingExternalID(t *testing.T) {
	t.Parallel()
	var updated map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/pipelines/commits/abc123/check-runs":
			writeJSON(t, w, map[string]any{"total_count": 1, "check_runs": []map[string]any{{"id": 9, "external_id": "proof-1"}}})
		case "PATCH /repos/acme/pipelines/check-runs/9":
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&updated))
			writeJSON(t, w, map[string]any{"id": 9})
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	err := newClient(t, server.URL).PublishProofCheck(transportsecurity.WithPlaintext(t.Context()), gitprovider.ProofCheck{
		HeadSHA: "abc123", ExternalID: "proof-1", DetailsURL: "https://app.example.test/proof-1", Summary: "1 passed", Conclusion: "neutral",
	})
	require.NoError(t, err)
	require.Equal(t, "proof-1", updated["external_id"])
	require.Equal(t, "completed", updated["status"])
}

func TestClient_PublishProofCheckActionLimits(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodPost, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			var payload struct {
				Actions []struct{ Label, Description, Identifier string }
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					runs := []map[string]any{}
					if method == http.MethodPatch {
						runs = append(runs, map[string]any{"id": 9, "external_id": "proof-1"})
					}
					writeJSON(t, w, map[string]any{"check_runs": runs})
					return
				}
				assert.Equal(t, method, r.Method)
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
				writeJSON(t, w, map[string]any{"id": 9})
			}))
			t.Cleanup(server.Close)

			err := newClient(t, server.URL).PublishProofCheck(transportsecurity.WithPlaintext(t.Context()), gitprovider.ProofCheck{
				HeadSHA: "abc123", ExternalID: "proof-1", Conclusion: "neutral",
			})
			require.NoError(t, err)
			require.Len(t, payload.Actions, 1)
			action := payload.Actions[0]
			// GitHub limits action labels and identifiers to 20 characters, descriptions to 40.
			assert.NotEmpty(t, action.Label)
			assert.LessOrEqual(t, len(action.Label), 20)
			assert.NotEmpty(t, action.Description)
			assert.LessOrEqual(t, len(action.Description), 40)
			assert.Equal(t, "rerequest", action.Identifier)
		})
	}
}

func TestClient_PublishProofCheckFindsMatchingExternalIDOnLaterPage(t *testing.T) {
	t.Parallel()
	var updated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/pipelines/commits/abc123/check-runs":
			if r.URL.Query().Get("page") == "2" {
				writeJSON(t, w, map[string]any{"check_runs": []map[string]any{{"id": 9, "external_id": "proof-1"}}})
				return
			}
			w.Header().Set("Link", `<http://`+r.Host+`/repos/acme/pipelines/commits/abc123/check-runs?page=2>; rel="next"`)
			writeJSON(t, w, map[string]any{"check_runs": []map[string]any{{"id": 8, "external_id": "other"}}})
		case "PATCH /repos/acme/pipelines/check-runs/9":
			updated = true
			writeJSON(t, w, map[string]any{"id": 9})
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	require.NoError(t, newClient(t, server.URL).PublishProofCheck(transportsecurity.WithPlaintext(t.Context()), gitprovider.ProofCheck{HeadSHA: "abc123", ExternalID: "proof-1", Conclusion: "neutral"}))
	require.True(t, updated)
}

func TestClient_ProofCheckRetriesAndHistoricalInvalidationAvoidNewWrites(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		runs         string
		existingOnly bool
	}{
		{"UnchangedRetry", `{"check_runs":[{"id":9,"external_id":"proof-1","status":"completed","conclusion":"neutral","details_url":"https://app.example.test/proof-1","output":{"summary":"stale"}}]}`, false},
		{"MissingHistoricalCheck", `{"check_runs":[]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method, "retry or invalidation must not create a new check")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.runs))
			}))
			t.Cleanup(server.Close)
			err := newClient(t, server.URL).PublishProofCheck(transportsecurity.WithPlaintext(t.Context()), gitprovider.ProofCheck{HeadSHA: "abc123", ExternalID: "proof-1", DetailsURL: "https://app.example.test/proof-1", Summary: "stale", Conclusion: "neutral", ExistingOnly: tc.existingOnly})
			require.NoError(t, err)
		})
	}
}

func TestClient_CreateProposal(t *testing.T) {
	t.Parallel()

	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/repos/acme/pipelines/pulls", r.URL.Path)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		writeJSON(t, w, map[string]any{
			"number":   43,
			"html_url": "https://github.com/acme/pipelines/pull/43",
			"state":    "open",
			"head": map[string]any{
				"ref": "braidplane/default/daily-sync/member-a",
				"sha": "def456",
			},
			"base": map[string]any{
				"ref": "main",
			},
		})
	}))
	t.Cleanup(server.Close)

	client := newClient(t, server.URL)

	got, err := client.CreateProposal(transportsecurity.WithPlaintext(context.Background()), gitprovider.ProposalRequest{
		SourceBranch: "braidplane/default/daily-sync/member-a",
		TargetBranch: "refs/heads/main",
		Title:        "Propose daily sync",
		Body:         "Pipeline draft proposed by Braidplane.",
	})

	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"title": "Propose daily sync",
		"head":  "braidplane/default/daily-sync/member-a",
		"base":  "main",
		"body":  "Pipeline draft proposed by Braidplane.",
	}, gotBody)
	require.Equal(t, &gitprovider.ProposalRef{
		Provider:     repostore.ProviderGitHub,
		Repository:   githubRepoRef(),
		ID:           "43",
		URL:          "https://github.com/acme/pipelines/pull/43",
		SourceBranch: "braidplane/default/daily-sync/member-a",
		TargetBranch: "main",
		State:        gitprovider.ProposalStateOpen,
		HeadSHA:      "def456",
		ProviderKind: gitprovider.ProposalKindPullRequest,
	}, got)
}

func TestClient_GetProposalIncludesMergedState(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/acme/pipelines/pulls/42", r.URL.Path)
		writeJSON(t, w, map[string]any{
			"number": 42, "html_url": "https://github.com/acme/pipelines/pull/42", "state": "closed", "merged": true,
			"head": map[string]any{"ref": "bp/member", "sha": "head-42"}, "base": map[string]any{"ref": "main", "repo": map[string]any{"id": 8}},
		})
	}))
	t.Cleanup(server.Close)

	got, err := newClient(t, server.URL).GetProposal(transportsecurity.WithPlaintext(context.Background()), gitprovider.ProposalGetRequest{ProposalID: "42"})

	require.NoError(t, err)
	require.Equal(t, gitprovider.ProposalStateMerged, got.State)
	require.Equal(t, "8", got.RepositoryID)
	require.Equal(t, "head-42", got.HeadSHA)
}

func TestClient_UpdateProposal(t *testing.T) {
	t.Parallel()

	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.Equal(t, "/repos/acme/pipelines/pulls/42", r.URL.Path)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		writeJSON(t, w, map[string]any{
			"number":   42,
			"html_url": "https://github.com/acme/pipelines/pull/42",
			"state":    "open",
			"head": map[string]any{
				"ref": "braidplane/default/daily-sync/member-a",
				"sha": "newsha",
			},
			"base": map[string]any{
				"ref": "main",
			},
		})
	}))
	t.Cleanup(server.Close)

	client := newClient(t, server.URL)

	got, err := client.UpdateProposal(transportsecurity.WithPlaintext(context.Background()), gitprovider.ProposalUpdateRequest{
		ProposalID: "42",
		Title:      "Propose daily sync",
		Body:       "Updated proposal body.",
	})

	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"title": "Propose daily sync",
		"body":  "Updated proposal body.",
	}, gotBody)
	require.Equal(t, "42", got.ID)
	require.Equal(t, "newsha", got.HeadSHA)
}

func TestClient_RejectsNonPositiveProposalIDs(t *testing.T) {
	t.Parallel()

	client := newClient(t, "https://api.github.test/")
	for _, proposalID := range []string{"0", "-1"} {
		t.Run(proposalID, func(t *testing.T) {
			t.Parallel()
			_, err := client.GetProposal(transportsecurity.WithPlaintext(t.Context()), gitprovider.ProposalGetRequest{ProposalID: proposalID})
			require.Error(t, err)
			_, err = client.UpdateProposal(transportsecurity.WithPlaintext(t.Context()), gitprovider.ProposalUpdateRequest{ProposalID: proposalID})
			require.Error(t, err)
		})
	}
}

func TestClient_RedactsTokenFromProviderError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(t, w, map[string]any{"message": "bad token ghp_secret"})
	}))
	t.Cleanup(server.Close)

	client := newClient(t, server.URL)

	_, err := client.ListOpenProposals(transportsecurity.WithPlaintext(context.Background()), gitprovider.ProposalListRequest{
		SourceBranch: "braidplane/default/daily-sync/member-a",
		TargetBranch: "main",
	})

	require.Error(t, err)
	require.NotContains(t, err.Error(), "ghp_secret")
	require.Contains(t, err.Error(), "[REDACTED]")
}

func TestClient_BootstrapRepositoryCreatesPrivateExactFirstCommit(t *testing.T) {
	t.Parallel()

	var createBody map[string]any
	var contentWrites []map[string]any
	gitDatabaseWrites := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/pipelines":
			w.WriteHeader(http.StatusNotFound)
			writeJSON(t, w, map[string]any{"message": "not found"})
		case "GET /user":
			writeJSON(t, w, map[string]any{"login": "alice"})
		case "POST /orgs/acme/repos":
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&createBody))
			writeJSON(t, w, map[string]any{"id": 42, "name": "pipelines", "full_name": "Acme/Pipelines", "description": createBody["description"], "private": true, "clone_url": "https://github.com/Acme/Pipelines.git"})
		case "PUT /repos/acme/pipelines/contents/.braidplane/template.yaml", "PUT /repos/acme/pipelines/contents/pipelines/README.md":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			contentWrites = append(contentWrites, body)
			writeJSON(t, w, map[string]any{"commit": map[string]any{"sha": "commit-" + strconv.Itoa(len(contentWrites))}})
		case "PATCH /repos/acme/pipelines":
			writeJSON(t, w, map[string]any{"default_branch": "main"})
		case "GET /repos/acme/pipelines/branches/main":
			writeJSON(t, w, map[string]any{"name": "main", "commit": map[string]any{"sha": "commit-2"}})
		case "GET /repos/acme/pipelines/git/commits/commit-2":
			writeJSON(t, w, map[string]any{"sha": "commit-2", "message": "Bootstrap Braidplane pipeline repository", "parents": []any{map[string]any{"sha": "commit-1"}}, "tree": map[string]any{"sha": "tree-2"}})
		case "GET /repos/acme/pipelines/git/commits/commit-1":
			writeJSON(t, w, map[string]any{"sha": "commit-1", "message": "Bootstrap Braidplane pipeline repository", "parents": []any{}, "tree": map[string]any{"sha": "tree-1"}})
		case "GET /repos/acme/pipelines/git/trees/tree-1":
			writeJSON(t, w, map[string]any{"tree": []map[string]any{{"path": ".braidplane/template.yaml", "type": "blob", "mode": "100644", "sha": "blob-1"}}})
		case "GET /repos/acme/pipelines/git/trees/tree-2":
			writeJSON(t, w, map[string]any{"tree": []map[string]any{{"path": ".braidplane", "type": "tree", "mode": "040000", "sha": "directory-1"}, {"path": "pipelines", "type": "tree", "mode": "040000", "sha": "directory-2"}, {"path": ".braidplane/template.yaml", "type": "blob", "mode": "100644", "sha": "blob-1"}, {"path": "pipelines/README.md", "type": "blob", "mode": "100644", "sha": "blob-2"}}})
		case "GET /repos/acme/pipelines/git/blobs/blob-1":
			writeJSON(t, w, map[string]any{"content": "dmVyc2lvbjogdjEK", "encoding": "base64"})
		case "GET /repos/acme/pipelines/git/blobs/blob-2":
			writeJSON(t, w, map[string]any{"content": "c2FmZQ==", "encoding": "base64"})
		default:
			if strings.Contains(r.URL.Path, "/git/") && r.Method != http.MethodGet {
				gitDatabaseWrites++
				w.WriteHeader(http.StatusConflict)
				return
			}
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	got, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{
		Owner: "acme", Name: "pipelines", DefaultBranch: "main",
		Files: []gitprovider.RepositoryFile{{Path: ".braidplane/template.yaml", Content: "version: v1\n"}, {Path: "pipelines/README.md", Content: "safe"}},
	})

	require.NoError(t, err)
	require.Equal(t, true, createBody["private"])
	require.Equal(t, false, createBody["auto_init"])
	require.Equal(t, "commit-2", got.HeadSHA)
	require.Equal(t, "https://github.com/Acme/Pipelines.git", got.RemoteURL)
	require.Len(t, contentWrites, 2)
	require.Equal(t, "Bootstrap Braidplane pipeline repository", contentWrites[0]["message"])
	require.Equal(t, "main", contentWrites[0]["branch"])
	require.Zero(t, gitDatabaseWrites)
}

func TestClient_BootstrapRepositoryCleansUpPartialFailure(t *testing.T) {
	t.Parallel()
	deleted := false
	getCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/pipelines":
			getCalls++
			if getCalls == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(t, w, map[string]any{"id": 42, "full_name": "Acme/Pipelines", "private": true})
		case "GET /user":
			writeJSON(t, w, map[string]any{"login": "alice"})
		case "POST /orgs/acme/repos":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			writeJSON(t, w, map[string]any{"id": 42, "name": "pipelines", "full_name": "Acme/Pipelines", "description": body["description"], "private": true})
		case "PUT /repos/acme/pipelines/contents/pipelines/README.md":
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(t, w, map[string]any{"message": "file failed"})
		case "DELETE /repos/acme/pipelines":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	_, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{
		Owner: "acme", Name: "pipelines", DefaultBranch: "main",
		Files: []gitprovider.RepositoryFile{{Path: "pipelines/README.md", Content: "safe"}},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "file failed")
	require.True(t, deleted)
}

func TestClient_BootstrapRepositoryResumesStableMarkerAfterPartialContentsFailure(t *testing.T) {
	t.Parallel()

	created := false
	secondFileAttempts := 0
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/pipelines":
			if !created {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(t, w, map[string]any{"id": 42, "full_name": "acme/pipelines", "private": true, "description": "Git-first Braidplane pipeline definitions [setup-123]", "clone_url": "https://github.com/acme/pipelines.git"})
		case "GET /user":
			writeJSON(t, w, map[string]any{"login": "alice"})
		case "POST /orgs/acme/repos":
			created = true
			writeJSON(t, w, map[string]any{"id": 42, "full_name": "acme/pipelines", "private": true, "description": "Git-first Braidplane pipeline definitions [setup-123]", "clone_url": "https://github.com/acme/pipelines.git"})
		case "GET /repos/acme/pipelines/branches/main":
			sha := "commit-1"
			if secondFileAttempts > 1 {
				sha = "commit-2"
			}
			writeJSON(t, w, map[string]any{"name": "main", "commit": map[string]any{"sha": sha}})
		case "PUT /repos/acme/pipelines/contents/a.yaml":
			writeJSON(t, w, map[string]any{"commit": map[string]any{"sha": "commit-1"}})
		case "PUT /repos/acme/pipelines/contents/nested/b.yaml":
			secondFileAttempts++
			if secondFileAttempts == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				writeJSON(t, w, map[string]any{"message": "interrupted"})
				return
			}
			writeJSON(t, w, map[string]any{"commit": map[string]any{"sha": "commit-2"}})
		case "PATCH /repos/acme/pipelines":
			writeJSON(t, w, map[string]any{"default_branch": "main"})
		case "GET /repos/acme/pipelines/git/commits/commit-1":
			writeJSON(t, w, map[string]any{"sha": "commit-1", "message": "Bootstrap Braidplane pipeline repository", "parents": []any{}, "tree": map[string]any{"sha": "tree-1"}})
		case "GET /repos/acme/pipelines/git/commits/commit-2":
			writeJSON(t, w, map[string]any{"sha": "commit-2", "message": "Bootstrap Braidplane pipeline repository", "parents": []any{map[string]any{"sha": "commit-1"}}, "tree": map[string]any{"sha": "tree-2"}})
		case "GET /repos/acme/pipelines/git/trees/tree-1":
			writeJSON(t, w, map[string]any{"tree": []map[string]any{{"path": "a.yaml", "type": "blob", "mode": "100644", "sha": "blob-1"}}})
		case "GET /repos/acme/pipelines/git/trees/tree-2":
			writeJSON(t, w, map[string]any{"tree": []map[string]any{{"path": "a.yaml", "type": "blob", "mode": "100644", "sha": "blob-1"}, {"path": "nested/b.yaml", "type": "blob", "mode": "100644", "sha": "blob-2"}}})
		case "GET /repos/acme/pipelines/git/blobs/blob-1":
			writeJSON(t, w, map[string]any{"content": "b25l", "encoding": "base64"})
		case "GET /repos/acme/pipelines/git/blobs/blob-2":
			writeJSON(t, w, map[string]any{"content": "dHdv", "encoding": "base64"})
		case "DELETE /repos/acme/pipelines":
			deleted = true
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	request := gitprovider.RepositoryBootstrapRequest{Owner: "acme", Name: "pipelines", DefaultBranch: "main", Marker: "setup-123", Files: []gitprovider.RepositoryFile{{Path: "a.yaml", Content: "one"}, {Path: "nested/b.yaml", Content: "two"}}}
	partial, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), request)
	require.ErrorContains(t, err, "interrupted")
	require.NotNil(t, partial)
	require.Equal(t, "42", partial.RepositoryID)
	require.Empty(t, partial.HeadSHA)

	completed, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), request)
	require.NoError(t, err)
	require.Equal(t, "commit-2", completed.HeadSHA)
	require.Equal(t, 2, secondFileAttempts)
	require.False(t, deleted)
}

func TestClient_BootstrapRepositoryRejectsMalformedCreateProvenanceWithoutCleanup(t *testing.T) {
	t.Parallel()
	for name, response := range map[string]map[string]any{
		"missing ID":      {"full_name": "acme/pipelines", "private": true},
		"wrong full name": {"id": 42, "full_name": "acme/replacement", "private": true},
		"public":          {"id": 42, "full_name": "acme/pipelines", "private": false},
	} {
		t.Run(name, func(t *testing.T) {
			deleted := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /repos/acme/pipelines":
					w.WriteHeader(http.StatusNotFound)
				case "GET /user":
					writeJSON(t, w, map[string]any{"login": "alice"})
				case "POST /orgs/acme/repos":
					var body map[string]any
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					response["description"] = body["description"]
					writeJSON(t, w, response)
				case "DELETE /repos/acme/pipelines":
					deleted = true
				default:
					t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
				}
			}))
			t.Cleanup(server.Close)

			_, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme", Name: "pipelines", DefaultBranch: "main", Files: []gitprovider.RepositoryFile{{Path: "pipelines/README.md", Content: "safe"}}})
			require.ErrorContains(t, err, "identity is invalid")
			require.False(t, deleted)
		})
	}
}

func TestClient_DeleteRepositoryRejectsReplacementID(t *testing.T) {
	t.Parallel()
	getCalls := 0
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/pipelines":
			getCalls++
			if getCalls == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(t, w, map[string]any{"id": 43, "full_name": "acme/pipelines", "private": true})
		case "GET /user":
			writeJSON(t, w, map[string]any{"login": "alice"})
		case "POST /orgs/acme/repos":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			writeJSON(t, w, map[string]any{"id": 42, "full_name": "acme/pipelines", "description": body["description"], "private": true})
		case "PUT /repos/acme/pipelines/contents/pipelines/README.md":
			writeJSON(t, w, map[string]any{"commit": map[string]any{"sha": "commit"}})
		case "PATCH /repos/acme/pipelines":
			writeJSON(t, w, map[string]any{"default_branch": "main"})
		case "GET /repos/acme/pipelines/branches/main":
			writeJSON(t, w, map[string]any{"name": "main", "commit": map[string]any{"sha": "commit"}})
		case "GET /repos/acme/pipelines/git/commits/commit":
			writeJSON(t, w, map[string]any{"sha": "commit", "message": "Bootstrap Braidplane pipeline repository", "parents": []any{}, "tree": map[string]any{"sha": "tree"}})
		case "GET /repos/acme/pipelines/git/trees/tree":
			writeJSON(t, w, map[string]any{"tree": []map[string]any{{"path": "pipelines/README.md", "type": "blob", "mode": "100644", "sha": "blob"}}})
		case "GET /repos/acme/pipelines/git/blobs/blob":
			writeJSON(t, w, map[string]any{"content": "c2FmZQ==", "encoding": "base64"})
		case "DELETE /repos/acme/pipelines":
			deleted = true
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	client := newClient(t, server.URL)
	got, err := client.BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme", Name: "pipelines", DefaultBranch: "main", Files: []gitprovider.RepositoryFile{{Path: "pipelines/README.md", Content: "safe"}}})
	require.NoError(t, err)
	require.Equal(t, "42", got.RepositoryID)
	require.ErrorContains(t, client.DeleteRepository(transportsecurity.WithPlaintext(t.Context()), got.RepositoryID), "no longer matches")
	require.False(t, deleted)
}

func TestClient_BootstrapRepositoryDoesNotDeleteWhenCreationWasDenied(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/pipelines":
			w.WriteHeader(http.StatusNotFound)
		case "GET /user":
			writeJSON(t, w, map[string]any{"login": "alice"})
		case "POST /orgs/acme/repos":
			w.WriteHeader(http.StatusForbidden)
			writeJSON(t, w, map[string]any{"message": "creation denied"})
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	_, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{
		Owner: "acme", Name: "pipelines", DefaultBranch: "main",
		Files: []gitprovider.RepositoryFile{{Path: "pipelines/README.md", Content: "safe"}},
	})

	require.ErrorContains(t, err, "forbidden")
}

func TestClient_BootstrapRepositoryRecoversAmbiguousCreateOnlyWithMatchingMarker(t *testing.T) {
	t.Parallel()

	getCalls := 0
	createDescription := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/pipelines":
			getCalls++
			if getCalls == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(t, w, map[string]any{
				"id": 42, "name": "pipelines", "full_name": "acme/pipelines", "description": createDescription, "private": true,
				"clone_url": "https://github.com/acme/pipelines.git",
			})
		case "GET /user":
			writeJSON(t, w, map[string]any{"login": "alice"})
		case "POST /orgs/acme/repos":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			createDescription, _ = body["description"].(string)
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(t, w, map[string]any{"message": "ambiguous create"})
		case "PUT /repos/acme/pipelines/contents/pipelines/README.md":
			writeJSON(t, w, map[string]any{"commit": map[string]any{"sha": "first-commit-sha"}})
		case "PATCH /repos/acme/pipelines":
			writeJSON(t, w, map[string]any{"default_branch": "main"})
		case "GET /repos/acme/pipelines/branches/main":
			writeJSON(t, w, map[string]any{"name": "main", "commit": map[string]any{"sha": "first-commit-sha"}})
		case "GET /repos/acme/pipelines/git/commits/first-commit-sha":
			writeJSON(t, w, map[string]any{"sha": "first-commit-sha", "message": "Bootstrap Braidplane pipeline repository", "parents": []any{}, "tree": map[string]any{"sha": "tree-sha"}})
		case "GET /repos/acme/pipelines/git/trees/tree-sha":
			writeJSON(t, w, map[string]any{"tree": []map[string]any{{"path": "pipelines/README.md", "type": "blob", "mode": "100644", "sha": "blob-sha"}}})
		case "GET /repos/acme/pipelines/git/blobs/blob-sha":
			writeJSON(t, w, map[string]any{"content": "c2FmZQ==", "encoding": "base64"})
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	got, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{
		Owner: "acme", Name: "pipelines", DefaultBranch: "main",
		Files: []gitprovider.RepositoryFile{{Path: "pipelines/README.md", Content: "safe"}},
	})

	require.NoError(t, err)
	require.Equal(t, "first-commit-sha", got.HeadSHA)
}

func TestClient_BootstrapRepositoryDoesNotOwnAmbiguousCreateWithDifferentMarker(t *testing.T) {
	t.Parallel()

	getCalls := 0
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/pipelines":
			getCalls++
			if getCalls == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(t, w, map[string]any{"id": 42, "name": "pipelines", "full_name": "acme/pipelines", "description": "created elsewhere", "private": true})
		case "GET /user":
			writeJSON(t, w, map[string]any{"login": "alice"})
		case "POST /orgs/acme/repos":
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(t, w, map[string]any{"message": "ambiguous create"})
		case "DELETE /repos/acme/pipelines":
			deleted = true
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	_, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{
		Owner: "acme", Name: "pipelines", DefaultBranch: "main",
		Files: []gitprovider.RepositoryFile{{Path: "pipelines/README.md", Content: "safe"}},
	})

	require.ErrorContains(t, err, "ambiguous create")
	require.False(t, deleted)
}

func TestClient_BootstrapRepositoryPreservesFailureWhenCleanupFails(t *testing.T) {
	t.Parallel()

	getCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/pipelines":
			getCalls++
			if getCalls == 1 {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeJSON(t, w, map[string]any{"id": 42, "full_name": "acme/pipelines", "private": true})
		case "GET /user":
			writeJSON(t, w, map[string]any{"login": "alice"})
		case "POST /orgs/acme/repos":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			writeJSON(t, w, map[string]any{"id": 42, "name": "pipelines", "full_name": "acme/pipelines", "description": body["description"], "private": true})
		case "PUT /repos/acme/pipelines/contents/pipelines/README.md":
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(t, w, map[string]any{"message": "file failed"})
		case "DELETE /repos/acme/pipelines":
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(t, w, map[string]any{"message": "cleanup failed"})
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	_, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{
		Owner: "acme", Name: "pipelines", DefaultBranch: "main",
		Files: []gitprovider.RepositoryFile{{Path: "pipelines/README.md", Content: "safe"}},
	})

	require.ErrorContains(t, err, "file failed")
	require.ErrorContains(t, err, "cleanup failed")
}

func TestClient_ValidateRepositoryRequiresPushPermission(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"permissions": map[string]any{"pull": true, "push": false}})
	}))
	t.Cleanup(server.Close)

	err := newClient(t, server.URL).ValidateRepository(transportsecurity.WithPlaintext(t.Context()), "main")

	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot write")
}

func TestClient_ValidateRepositoryUsesInstallationContentsPermission(t *testing.T) {
	t.Parallel()

	write := "write"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/pipelines":
			writeJSON(t, w, map[string]any{"permissions": map[string]any{"pull": true}})
		case "/repos/acme/pipelines/branches/main":
			writeJSON(t, w, map[string]any{"name": "main"})
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	client, err := githubprovider.New(transportsecurity.WithPlaintext(context.Background()), gitprovider.Config{BaseURL: server.URL, TimeoutSeconds: 5, AllowPrivateHosts: true}, gitprovider.Credentials{Token: "ghs_installation", GitHubContentsPermission: &write}, githubRepoRef())
	require.NoError(t, err)
	require.NoError(t, client.ValidateRepository(transportsecurity.WithPlaintext(t.Context()), "main"))
}

func TestClient_ValidateRepositoryRejectsNonWriteInstallationContentsPermission(t *testing.T) {
	t.Parallel()

	for _, permission := range []string{"", "read", "admin"} {
		t.Run(permission, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/repos/acme/pipelines", r.URL.Path)
				writeJSON(t, w, map[string]any{"permissions": map[string]any{"push": true}})
			}))
			t.Cleanup(server.Close)

			client, err := githubprovider.New(transportsecurity.WithPlaintext(context.Background()), gitprovider.Config{BaseURL: server.URL, TimeoutSeconds: 5, AllowPrivateHosts: true}, gitprovider.Credentials{Token: "ghs_installation", GitHubContentsPermission: &permission}, githubRepoRef())
			require.NoError(t, err)
			err = client.ValidateRepository(transportsecurity.WithPlaintext(t.Context()), "main")
			require.ErrorContains(t, err, "installation cannot write")
		})
	}
}

func TestClient_ValidateRepositoryAcceptsProtectedSelectedBranch(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/pipelines":
			writeJSON(t, w, map[string]any{"permissions": map[string]any{"push": true}})
		case "/repos/acme/pipelines/branches/main":
			writeJSON(t, w, map[string]any{"name": "main", "protected": true})
		default:
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	require.NoError(t, newClient(t, server.URL).ValidateRepository(transportsecurity.WithPlaintext(t.Context()), "main"))
}

func TestClient_BootstrapRepositoryReportsNamingConflictWithoutMutation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/repos/acme/pipelines", r.URL.Path)
		writeJSON(t, w, map[string]any{"name": "pipelines"})
	}))
	t.Cleanup(server.Close)

	_, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{
		Owner: "acme", Name: "pipelines", DefaultBranch: "main",
		Files: []gitprovider.RepositoryFile{{Path: "pipelines/README.md", Content: "safe"}},
	})

	require.ErrorContains(t, err, "already exists")
}

func TestClient_BootstrapRepositoryResumesOnlyExactRootTemplate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, mode string
		wantErr    bool
	}{
		{name: "exact nested template", mode: "100644"},
		{name: "symlink is refused", mode: "120000", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /repos/acme/pipelines":
					writeJSON(t, w, map[string]any{"id": 42, "full_name": "Acme/Pipelines", "private": true, "description": "Git-first Braidplane pipeline definitions [setup-123]", "clone_url": "https://github.com/acme/pipelines.git"})
				case "GET /repos/acme/pipelines/branches/main":
					writeJSON(t, w, map[string]any{"name": "main", "commit": map[string]any{"sha": "commit-sha"}})
				case "GET /repos/acme/pipelines/git/commits/commit-sha":
					writeJSON(t, w, map[string]any{"sha": "commit-sha", "message": "Bootstrap Braidplane pipeline repository", "parents": []any{}, "tree": map[string]any{"sha": "tree-sha"}})
				case "GET /repos/acme/pipelines/git/trees/tree-sha":
					writeJSON(t, w, map[string]any{"tree": []map[string]any{{"path": "nested", "type": "tree", "mode": "040000", "sha": "directory-sha"}, {"path": "nested/README.md", "type": "blob", "mode": tc.mode, "sha": "blob-sha"}}})
				case "GET /repos/acme/pipelines/git/blobs/blob-sha":
					writeJSON(t, w, map[string]any{"content": "c2FmZQ==", "encoding": "base64"})
				default:
					writes++
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			t.Cleanup(server.Close)
			got, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme", Name: "pipelines", DefaultBranch: "main", Marker: "setup-123", Files: []gitprovider.RepositoryFile{{Path: "nested/README.md", Content: "safe"}}})
			if tc.wantErr {
				require.Error(t, err)
				require.NotNil(t, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, "commit-sha", got.HeadSHA)
			}
			require.Zero(t, writes)
		})
	}
}

func TestClient_BootstrapRepositoryRejectsExpectedReplacementBeforeMutation(t *testing.T) {
	t.Parallel()
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/repos/acme/pipelines" {
			writeJSON(t, w, map[string]any{"id": 43, "full_name": "acme/pipelines", "private": true, "description": "Git-first Braidplane pipeline definitions [setup-123]"})
			return
		}
		writes++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	_, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme", Name: "pipelines", DefaultBranch: "main", Marker: "setup-123", ExpectedRepositoryID: "42"})
	require.Error(t, err)
	require.Zero(t, writes)
}

func TestClient_BootstrapRepositoryRefusesMissingRequestedBranchInNonemptyOwnedRepository(t *testing.T) {
	t.Parallel()
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/acme/pipelines":
			writeJSON(t, w, map[string]any{"id": 42, "full_name": "acme/pipelines", "private": true, "description": "Git-first Braidplane pipeline definitions [setup-123]"})
		case "GET /repos/acme/pipelines/branches/main":
			w.WriteHeader(http.StatusNotFound)
			writeJSON(t, w, map[string]any{"message": "branch not found"})
		case "GET /repos/acme/pipelines/branches":
			writeJSON(t, w, []map[string]any{{"name": "user-work"}})
		default:
			writes++
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	got, err := newClient(t, server.URL).BootstrapRepository(transportsecurity.WithPlaintext(t.Context()), gitprovider.RepositoryBootstrapRequest{Owner: "acme", Name: "pipelines", DefaultBranch: "main", Marker: "setup-123", Files: []gitprovider.RepositoryFile{{Path: "pipelines/README.md", Content: "safe"}}})
	require.ErrorContains(t, err, "contains branches")
	require.NotNil(t, got)
	require.Zero(t, writes)
}

func newClient(t *testing.T, baseURL string) *githubprovider.Client {
	t.Helper()

	client, err := githubprovider.New(
		transportsecurity.WithPlaintext(context.Background()),
		gitprovider.Config{BaseURL: baseURL, TimeoutSeconds: 5, AllowPrivateHosts: true},
		gitprovider.Credentials{Token: "ghp_secret"},
		githubRepoRef(),
	)
	require.NoError(t, err)
	return client
}

func githubRepoRef() gitprovider.RepositoryRef {
	return gitprovider.RepositoryRef{
		Provider:    repostore.ProviderGitHub,
		Owner:       "acme",
		Name:        "pipelines",
		ProjectPath: "acme/pipelines",
		FullName:    "acme/pipelines",
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(value))
}
