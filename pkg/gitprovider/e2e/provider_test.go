package provider_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
	gitlabprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/gitlab"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
)

func mockBaseURL() string {
	return depsports.GitProviderMockBaseURL()
}

func resetMock(t *testing.T, ctx context.Context) {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mockBaseURL()+"/reset", nil)
	require.NoError(t, err)

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// Shares a mutable mock service instance across tests.
func TestGitHubProposalClient_UpsertAgainstMockProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resetMock(t, ctx)

	client, err := githubprovider.New(
		transportsecurity.WithPlaintext(ctx),
		gitprovider.Config{BaseURL: mockBaseURL(), TimeoutSeconds: 5, AllowPrivateHosts: true},
		gitprovider.Credentials{Token: "ghp_mock"},
		gitprovider.RepositoryRef{
			Provider:    repostore.ProviderGitHub,
			Owner:       "acme",
			Name:        "pipelines",
			ProjectPath: "acme/pipelines",
			FullName:    "acme/pipelines",
		},
	)
	require.NoError(t, err)

	created, err := client.CreateProposal(ctx, gitprovider.ProposalRequest{
		SourceBranch: "braidplane/default/daily-sync/member-a",
		TargetBranch: "main",
		Title:        "Propose daily sync",
		Body:         "Pipeline draft proposed by Braidplane.",
	})
	require.NoError(t, err)
	require.Equal(t, repostore.ProviderGitHub, created.Provider)
	require.Equal(t, gitprovider.ProposalStateOpen, created.State)
	require.Equal(t, "github-sha-1", created.HeadSHA)

	listed, err := client.ListOpenProposals(ctx, gitprovider.ProposalListRequest{
		SourceBranch: "braidplane/default/daily-sync/member-a",
		TargetBranch: "refs/heads/main",
	})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, created.ID, listed[0].ID)

	updated, err := client.UpdateProposal(ctx, gitprovider.ProposalUpdateRequest{
		ProposalID: created.ID,
		Title:      "Propose daily sync",
		Body:       "Updated body",
	})
	require.NoError(t, err)
	require.Equal(t, "github-updated-1", updated.HeadSHA)
}

// Shares a mutable mock service instance across tests.
func TestGitLabProposalClient_UpsertAgainstMockProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resetMock(t, ctx)

	client, err := gitlabprovider.New(
		transportsecurity.WithPlaintext(ctx),
		gitprovider.Config{BaseURL: mockBaseURL() + "/api/v4", TimeoutSeconds: 5, AllowPrivateHosts: true},
		gitprovider.Credentials{Token: "glpat-mock"},
		gitprovider.RepositoryRef{
			Provider:    repostore.ProviderGitLab,
			Owner:       "acme/platform",
			Name:        "pipelines",
			ProjectPath: "acme/platform/pipelines",
			FullName:    "acme/platform/pipelines",
		},
	)
	require.NoError(t, err)

	created, err := client.CreateProposal(ctx, gitprovider.ProposalRequest{
		SourceBranch: "braidplane/default/daily-sync/member-a",
		TargetBranch: "main",
		Title:        "Propose daily sync",
		Body:         "Pipeline draft proposed by Braidplane.",
	})
	require.NoError(t, err)
	require.Equal(t, repostore.ProviderGitLab, created.Provider)
	require.Equal(t, gitprovider.ProposalStateOpen, created.State)
	require.Equal(t, "gitlab-sha-1", created.HeadSHA)

	listed, err := client.ListOpenProposals(ctx, gitprovider.ProposalListRequest{
		SourceBranch: "braidplane/default/daily-sync/member-a",
		TargetBranch: "refs/heads/main",
	})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, created.ID, listed[0].ID)

	updated, err := client.UpdateProposal(ctx, gitprovider.ProposalUpdateRequest{
		ProposalID: created.ID,
		Title:      "Propose daily sync",
		Body:       "Updated body",
	})
	require.NoError(t, err)
	require.Equal(t, "gitlab-updated-1", updated.HeadSHA)
}
