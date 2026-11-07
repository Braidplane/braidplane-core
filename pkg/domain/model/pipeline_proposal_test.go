package model

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPipelineProposalAuthorityValidatesPersistentIdentity(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, (PipelineProposalAuthority{}).ValidateForPersistence(), "provider account ID is required")

	authority := PipelineProposalAuthority{
		ProviderAccountID:         "account-1",
		ExecutingPrincipalDisplay: "alice@example.com",
	}

	require.ErrorContains(t, authority.ValidateForPersistence(), "must not be an email address")
}

func TestPlainGitAuthorityHasRepositoryCredentialsWithoutProviderIdentity(t *testing.T) {
	authority := PipelineProposalAuthority{PublicationMode: PipelineProposalModeRepositoryCredential, ExecutingPrincipalDisplay: "Configured Git connection", AuthoritySource: "git_data_source_credential", GitConfigurationHash: "configuration-one", GitRepository: "git.example.test/acme/pipelines", GitBaseRef: "refs/heads/main", GitDataSourceSlug: "git-source", GitCredentialSlug: "ssh-key", GitCredentialRevision: "revision-one"}
	require.NoError(t, authority.ValidateForPersistence())
	for _, change := range []func(*PipelineProposalAuthority){func(a *PipelineProposalAuthority) { a.GitCredentialRevision = "" }, func(a *PipelineProposalAuthority) { a.ProviderAccountID = "pretend-account" }, func(a *PipelineProposalAuthority) { a.ProviderCredentialID = "pretend-installation" }, func(a *PipelineProposalAuthority) { a.ExecutingPrincipalID = "pretend-user" }, func(a *PipelineProposalAuthority) { a.ExecutingPrincipalDisplay = "alice@example.com" }} {
		changed := authority
		change(&changed)
		require.Error(t, changed.ValidateForPersistence())
	}
}
