package datasourcessvc

import (
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/githubappsvc"
	"github.com/stretchr/testify/require"
)

func TestGitHubProposalAuthorityRequiresVerifiedWritableIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*repods.DataSource, *githubappsvc.InstallationToken)
	}{
		{"ReadOnly", func(ds *repods.DataSource, _ *githubappsvc.InstallationToken) { ds.ReadOnly = true }},
		{"Inactive", func(ds *repods.DataSource, _ *githubappsvc.InstallationToken) { ds.IsActive = false }},
		{"Revoked", func(ds *repods.DataSource, _ *githubappsvc.InstallationToken) { ds.Credential.Status = "revoked" }},
		{"RevokedTimestamp", func(ds *repods.DataSource, _ *githubappsvc.InstallationToken) {
			now := time.Now()
			ds.Credential.RevokedAt = &now
		}},
		{"ContentsReadOnly", func(_ *repods.DataSource, token *githubappsvc.InstallationToken) { token.ContentsPermission = "read" }},
		{"PullRequestsReadOnly", func(_ *repods.DataSource, token *githubappsvc.InstallationToken) {
			token.PullRequestsPermission = "read"
		}},
		{"Expired", func(_ *repods.DataSource, token *githubappsvc.InstallationToken) {
			token.ExpiresAt = time.Now().Add(-time.Second)
		}},
		{"MissingPrincipal", func(_ *repods.DataSource, token *githubappsvc.InstallationToken) { token.PrincipalID = 0 }},
		{"MissingInstallation", func(_ *repods.DataSource, token *githubappsvc.InstallationToken) { token.InstallationID = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ds := &repods.DataSource{
				Type: model.DataSourceTypeGithub, IsActive: true,
				Config:     []byte(`{"repository":{"id":"42"}}`),
				Credential: &repods.CredentialMetadata{Slug: "credential", Status: "active"},
			}
			token := &githubappsvc.InstallationToken{
				Token: "ephemeral", ExpiresAt: time.Now().Add(time.Hour),
				ContentsPermission: "write", PullRequestsPermission: "write", InstallationID: 9, AccountID: 2,
				PrincipalID: 77, PrincipalLogin: "braidplane[bot]",
			}
			authority := gitHubProposalAuthority(ds, token)
			require.Equal(t, &GitHubProposalAuthority{CredentialSlug: "credential", Authority: model.PipelineProposalAuthority{
				PublicationMode: "organization_service", ProviderHost: "github.com", ProviderAccountID: "2",
				ProviderRepositoryID: "42", ProviderCredentialID: "9", ExecutingPrincipalID: "77",
				ExecutingPrincipalDisplay: "braidplane[bot]", AuthoritySource: "github_app_installation",
			}}, authority)
			tc.change(ds, token)
			require.Nil(t, gitHubProposalAuthority(ds, token))
		})
	}
}
