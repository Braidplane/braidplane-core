package datasourcessvc

import (
	"strconv"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/service/githubappsvc"
)

// GitHubProposalAuthority contains only verified, credential-safe binding metadata.
type GitHubProposalAuthority struct {
	CredentialSlug slug.Slug                       `json:"credential_slug"`
	Authority      model.PipelineProposalAuthority `json:"authority"`
}

func gitHubContentsPermission(token *githubappsvc.InstallationToken) *string {
	if token == nil {
		return nil
	}
	return &token.ContentsPermission
}

func gitHubProposalAuthority(ds *repods.DataSource, token *githubappsvc.InstallationToken) *GitHubProposalAuthority {
	config, managed := managedProviderGitConfig(ds)
	if !managed || ds.Type != model.DataSourceTypeGithub || !ds.IsActive || ds.ReadOnly ||
		ds.Credential == nil || ds.Credential.Status != "active" || ds.Credential.RevokedAt != nil ||
		token == nil || token.Token == "" || !token.ExpiresAt.After(time.Now()) ||
		token.ContentsPermission != "write" || token.PullRequestsPermission != "write" ||
		token.InstallationID <= 0 || token.AccountID <= 0 || token.PrincipalID <= 0 || token.PrincipalLogin == "" {
		return nil
	}
	return &GitHubProposalAuthority{
		CredentialSlug: ds.Credential.Slug,
		Authority: model.PipelineProposalAuthority{
			PublicationMode:           model.PipelineProposalModeOrganizationService,
			ProviderHost:              "github.com",
			ProviderAccountID:         strconv.FormatInt(token.AccountID, 10),
			ProviderRepositoryID:      config.Repository.ID,
			ProviderCredentialID:      strconv.FormatInt(token.InstallationID, 10),
			ExecutingPrincipalID:      strconv.FormatInt(token.PrincipalID, 10),
			ExecutingPrincipalDisplay: token.PrincipalLogin,
			AuthoritySource:           "github_app_installation",
		},
	}
}
