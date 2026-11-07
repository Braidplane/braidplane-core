package gitlab_test

import (
	"context"
	"encoding/base64"
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
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestClient_ListOpenProposalsForNestedProject(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/v4/projects/acme%2Fplatform%2Fpipelines/merge_requests", r.URL.EscapedPath())
		assert.Equal(t, "glpat-secret", r.Header.Get("PRIVATE-TOKEN"))
		assert.Equal(t, "opened", r.URL.Query().Get("state"))
		assert.Equal(t, "braidplane/default/daily-sync/member-a", r.URL.Query().Get("source_branch"))
		assert.Equal(t, "main", r.URL.Query().Get("target_branch"))
		writeJSON(t, w, []map[string]any{{
			"iid":           7,
			"web_url":       "https://gitlab.com/acme/platform/pipelines/-/merge_requests/7",
			"state":         "opened",
			"sha":           "abc123",
			"source_branch": "braidplane/default/daily-sync/member-a",
			"target_branch": "main",
			"merged_at":     nil,
			"closed_at":     nil,
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
		Provider:     repostore.ProviderGitLab,
		Repository:   gitlabRepoRef(),
		ID:           "7",
		URL:          "https://gitlab.com/acme/platform/pipelines/-/merge_requests/7",
		SourceBranch: "braidplane/default/daily-sync/member-a",
		TargetBranch: "main",
		State:        gitprovider.ProposalStateOpen,
		HeadSHA:      "abc123",
		ProviderKind: gitprovider.ProposalKindMergeRequest,
		Closed:       false,
		Merged:       false,
	}}, got)
}

func TestOAuthClient_GetProposalUsesBearerAuthentication(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"oauth-access", "glpat-access"} {
		t.Run(token, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
				assert.Empty(t, r.Header.Get("PRIVATE-TOKEN"))
				assert.Equal(t, "/api/v4/projects/acme%2Fplatform%2Fpipelines/merge_requests/7", r.URL.EscapedPath())
				writeJSON(t, w, map[string]any{"iid": 7, "state": "opened", "target_project_id": 42, "sha": "abc123"})
			}))
			t.Cleanup(server.Close)
			client, err := gitlabprovider.NewOAuth(transportsecurity.WithPlaintext(t.Context()), gitprovider.Config{BaseURL: server.URL, AllowPrivateHosts: true}, gitprovider.Credentials{Token: token}, gitlabRepoRef())
			require.NoError(t, err)
			proposal, err := client.GetProposal(transportsecurity.WithPlaintext(t.Context()), gitprovider.ProposalGetRequest{ProposalID: "7"})
			require.NoError(t, err)
			require.Equal(t, "7", proposal.ID)
			require.Equal(t, "42", proposal.RepositoryID)
		})
	}
}

func TestClient_EnsureProofWebhook(t *testing.T) {
	t.Parallel()
	signingToken := "whsec_" + base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch requests.Add(1) {
		case 1:
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "/api/v4/projects/acme%2Fplatform%2Fpipelines", r.URL.EscapedPath())
			writeJSON(t, w, map[string]any{"id": 42, "path_with_namespace": "acme/platform/pipelines"})
		case 2:
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "/api/v4/projects/42/hooks", r.URL.EscapedPath())
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "https://app.example/api/webhooks/gitlab/merge-requests/route", body["url"])
			assert.Equal(t, signingToken, body["signing_token"])
			assert.Equal(t, true, body["merge_requests_events"])
			writeJSON(t, w, map[string]any{"id": 9, "project_id": 42})
		}
	}))
	t.Cleanup(server.Close)

	client := newClient(t, server.URL)
	projectID, err := client.ProjectID(transportsecurity.WithPlaintext(t.Context()))
	require.NoError(t, err)
	hook, err := client.EnsureProofWebhook(transportsecurity.WithPlaintext(t.Context()), projectID, "https://app.example/api/webhooks/gitlab/merge-requests/route", signingToken, 0)
	require.NoError(t, err)
	require.Equal(t, gitlabprovider.ProofWebhook{ProjectID: 42, HookID: 9}, hook)

	for _, tc := range []struct {
		callbackURL, signingToken string
		wantErr                   error
	}{
		{"invalid", signingToken, xerror.InvalidInput(xerror.DataSource)},
		{"https://app.example/hook", "whsec_token", xerror.InvalidCredentials()},
	} {
		_, err = client.EnsureProofWebhook(transportsecurity.WithPlaintext(t.Context()), projectID, tc.callbackURL, tc.signingToken, 0)
		require.True(t, xerror.Is(err, tc.wantErr))
		require.Equal(t, int32(2), requests.Load())
	}
}

func TestClient_CreateProposalForNestedProject(t *testing.T) {
	t.Parallel()

	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v4/projects/acme%2Fplatform%2Fpipelines/merge_requests", r.URL.EscapedPath())
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		writeJSON(t, w, map[string]any{
			"iid":           8,
			"web_url":       "https://gitlab.com/acme/platform/pipelines/-/merge_requests/8",
			"state":         "opened",
			"sha":           "def456",
			"source_branch": "braidplane/default/daily-sync/member-a",
			"target_branch": "main",
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
		"title":         "Propose daily sync",
		"source_branch": "braidplane/default/daily-sync/member-a",
		"target_branch": "main",
		"description":   "Pipeline draft proposed by Braidplane.",
	}, gotBody)
	require.Equal(t, &gitprovider.ProposalRef{
		Provider:     repostore.ProviderGitLab,
		Repository:   gitlabRepoRef(),
		ID:           "8",
		URL:          "https://gitlab.com/acme/platform/pipelines/-/merge_requests/8",
		SourceBranch: "braidplane/default/daily-sync/member-a",
		TargetBranch: "main",
		State:        gitprovider.ProposalStateOpen,
		HeadSHA:      "def456",
		ProviderKind: gitprovider.ProposalKindMergeRequest,
	}, got)
}

func TestClient_GetProposalIncludesClosedState(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v4/projects/acme%2Fplatform%2Fpipelines/merge_requests/7", r.URL.EscapedPath())
		writeJSON(t, w, map[string]any{
			"iid": 7, "web_url": "https://gitlab.com/acme/platform/pipelines/-/merge_requests/7", "state": "closed",
			"sha": "head-7", "source_branch": "bp/member", "target_branch": "main", "target_project_id": 8,
		})
	}))
	t.Cleanup(server.Close)

	got, err := newClient(t, server.URL).GetProposal(transportsecurity.WithPlaintext(context.Background()), gitprovider.ProposalGetRequest{ProposalID: "7"})

	require.NoError(t, err)
	require.Equal(t, gitprovider.ProposalStateClosed, got.State)
	require.Equal(t, "8", got.RepositoryID)
	require.Equal(t, "head-7", got.HeadSHA)
}

func TestClient_UpdateProposal(t *testing.T) {
	t.Parallel()

	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/api/v4/projects/acme%2Fplatform%2Fpipelines/merge_requests/7", r.URL.EscapedPath())
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		writeJSON(t, w, map[string]any{
			"iid":           7,
			"web_url":       "https://gitlab.com/acme/platform/pipelines/-/merge_requests/7",
			"state":         "opened",
			"sha":           "newsha",
			"source_branch": "braidplane/default/daily-sync/member-a",
			"target_branch": "main",
		})
	}))
	t.Cleanup(server.Close)

	client := newClient(t, server.URL)

	got, err := client.UpdateProposal(transportsecurity.WithPlaintext(context.Background()), gitprovider.ProposalUpdateRequest{
		ProposalID: "7",
		Title:      "Propose daily sync",
		Body:       "Updated proposal body.",
	})

	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"title":       "Propose daily sync",
		"description": "Updated proposal body.",
	}, gotBody)
	require.Equal(t, "7", got.ID)
	require.Equal(t, "newsha", got.HeadSHA)
}

func TestClient_RejectsNonPositiveProposalIDs(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requestCount.Add(1)
	}))
	t.Cleanup(server.Close)
	client := newClient(t, server.URL)
	for _, proposalID := range []string{"0", "-1"} {
		t.Run(proposalID, func(t *testing.T) {
			t.Parallel()
			_, err := client.GetProposal(transportsecurity.WithPlaintext(t.Context()), gitprovider.ProposalGetRequest{ProposalID: proposalID})
			require.Error(t, err)
			_, err = client.UpdateProposal(transportsecurity.WithPlaintext(t.Context()), gitprovider.ProposalUpdateRequest{ProposalID: proposalID})
			require.Error(t, err)
		})
	}
	require.Zero(t, requestCount.Load())
}

func TestClient_RedactsTokenFromProviderError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(t, w, map[string]any{"message": "bad token glpat-secret"})
	}))
	t.Cleanup(server.Close)

	client := newClient(t, server.URL)

	_, err := client.ListOpenProposals(transportsecurity.WithPlaintext(context.Background()), gitprovider.ProposalListRequest{
		SourceBranch: "braidplane/default/daily-sync/member-a",
		TargetBranch: "main",
	})

	require.Error(t, err)
	require.NotContains(t, err.Error(), "glpat-secret")
	require.Contains(t, err.Error(), "[REDACTED]")
}

func newClient(t *testing.T, baseURL string) *gitlabprovider.Client {
	t.Helper()

	client, err := gitlabprovider.New(
		transportsecurity.WithPlaintext(context.Background()),
		gitprovider.Config{BaseURL: baseURL + "/api/v4", TimeoutSeconds: 5, AllowPrivateHosts: true},
		gitprovider.Credentials{Token: "glpat-secret"},
		gitlabRepoRef(),
	)
	require.NoError(t, err)
	return client
}

func gitlabRepoRef() gitprovider.RepositoryRef {
	return gitprovider.RepositoryRef{
		Provider:    repostore.ProviderGitLab,
		Owner:       "acme/platform",
		Name:        "pipelines",
		ProjectPath: "acme/platform/pipelines",
		FullName:    "acme/platform/pipelines",
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(value))
}
