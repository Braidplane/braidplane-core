package proofcandidatesvc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	proofrepo "github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

func TestExactSourceVerifierBindsOpenProviderHeadAndSource(t *testing.T) {
	t.Parallel()
	head := "0123456789abcdef0123456789abcdef01234567"
	change := validChange(head)
	v := &exactSourceVerifier{
		auth: fixedGitAuth{auth: &datasourcessvc.GitDataSourceAuth{
			Provider: "github", RepoURL: "https://github.com/acme/pipelines.git",
		}},
		remote: fixedRemoteGit{store: fixedRepoStore{content: []byte("pipeline")}},
		newProvider: func(context.Context, repostore.Provider, string, []string) (gitprovider.ProposalReader, error) {
			return fixedProposal{ref: &gitprovider.ProposalRef{
				Provider: repostore.ProviderGitHub, RepositoryID: "8", ID: "42", State: gitprovider.ProposalStateOpen,
				SourceBranch: "feature", TargetBranch: "main", HeadSHA: head,
			}}, nil
		},
		head: func(context.Context, string) *string { return &head },
	}

	source, err := v.VerifySource(t.Context(), validMapping(), change)

	require.NoError(t, err)
	require.Equal(t, []byte("pipeline"), source)
}

func TestExactSourceVerifierRejectsChangedProviderState(t *testing.T) {
	t.Parallel()
	head := "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name string
		ref  *gitprovider.ProposalRef
		want error
	}{
		{name: "closed", ref: &gitprovider.ProposalRef{State: gitprovider.ProposalStateClosed}, want: gitprovider.ErrChangeClosed},
		{name: "force pushed", ref: &gitprovider.ProposalRef{
			Provider: repostore.ProviderGitHub, RepositoryID: "8", ID: "42", State: gitprovider.ProposalStateOpen,
			SourceBranch: "feature", TargetBranch: "main", HeadSHA: "abcdef0123456789abcdef0123456789abcdef01",
		}, want: gitprovider.ErrChangeStale},
		{name: "repository changed", ref: &gitprovider.ProposalRef{
			Provider: repostore.ProviderGitHub, RepositoryID: "9", ID: "42", State: gitprovider.ProposalStateOpen,
			SourceBranch: "feature", TargetBranch: "main", HeadSHA: head,
		}, want: gitprovider.ErrChangeStale},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := &exactSourceVerifier{
				auth: fixedGitAuth{auth: &datasourcessvc.GitDataSourceAuth{Provider: "github"}},
				newProvider: func(context.Context, repostore.Provider, string, []string) (gitprovider.ProposalReader, error) {
					return fixedProposal{ref: tt.ref}, nil
				},
			}
			_, err := v.VerifySource(t.Context(), validMapping(), validChange(head))
			require.ErrorIs(t, err, tt.want)
		})
	}
}

func TestExactSourceVerifierClassifiesCloneFailures(t *testing.T) {
	t.Parallel()
	head := "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "missing ref", err: repostore.ErrRemoteRefNotFound, want: gitprovider.ErrChangeRefDeleted},
		{name: "other missing resource", err: xerror.NotFound(xerror.RepoFile), want: gitprovider.ErrChangeUnavailable},
		{name: "permission lost", err: xerror.Forbidden(xerror.RepoFile), want: gitprovider.ErrChangePermissionLost},
		{name: "provider unavailable", err: xerror.Unavailable(xerror.RepoFile), want: gitprovider.ErrChangeUnavailable},
		{name: "canceled", err: context.Canceled, want: gitprovider.ErrChangeUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := exactSourceVerifierForTest(head)
			v.remote = fixedRemoteGit{err: tt.err}
			_, err := v.VerifySource(t.Context(), validMapping(), validChange(head))
			require.ErrorIs(t, err, tt.want)
			require.ErrorIs(t, err, tt.err)
		})
	}
}

func exactSourceVerifierForTest(head string) *exactSourceVerifier {
	return &exactSourceVerifier{
		auth: fixedGitAuth{auth: &datasourcessvc.GitDataSourceAuth{Provider: "github"}},
		newProvider: func(context.Context, repostore.Provider, string, []string) (gitprovider.ProposalReader, error) {
			return fixedProposal{ref: &gitprovider.ProposalRef{
				Provider: repostore.ProviderGitHub, RepositoryID: "8", ID: "42", State: gitprovider.ProposalStateOpen,
				SourceBranch: "feature", TargetBranch: "main", HeadSHA: head,
			}}, nil
		},
	}
}

func validChange(head string) gitprovider.VerifiedChange {
	return gitprovider.VerifiedChange{
		Provider: repostore.ProviderGitHub, ProviderConnectionID: "7", RepositoryID: "8",
		ChangeRequestID: "42", HeadSHA: head, SourceRef: "refs/heads/feature",
		TargetRef: "refs/heads/main", PipelinePath: "pipelines/orders.pipeline.yaml",
	}
}

func validMapping() proofrepo.Mapping {
	return proofrepo.Mapping{
		OrganizationSlug: "acme", ProviderConnectionSlug: "github", Provider: "github",
	}
}

type fixedGitAuth struct {
	auth *datasourcessvc.GitDataSourceAuth
}

func (f fixedGitAuth) ResolveGitAuth(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitDataSourceAuth, error) {
	return f.auth, nil
}

type fixedProposal struct{ ref *gitprovider.ProposalRef }

func (f fixedProposal) GetProposal(context.Context, gitprovider.ProposalGetRequest) (*gitprovider.ProposalRef, error) {
	return f.ref, nil
}

type fixedRemoteGit struct {
	store repostore.RepoStore
	err   error
}

func (f fixedRemoteGit) CloneRemote(context.Context, string, string, []string) (repostore.RepoStore, string, func(), error) {
	return f.store, "repo", func() {}, f.err
}
func (fixedRemoteGit) CheckRemote(context.Context, string, []string) error { return nil }
func (fixedRemoteGit) ListRemoteBranches(context.Context, string, []string) ([]repostore.RemoteBranch, error) {
	return nil, nil
}

func (fixedRemoteGit) PushRemote(context.Context, string, string, []string, repostore.PushOptions) error {
	return xerror.Unexpected().Detail("unexpected push")
}

type fixedRepoStore struct{ content []byte }

func (f fixedRepoStore) ReadFile(context.Context, string, int64) ([]byte, error) {
	return f.content, nil
}

func (fixedRepoStore) WriteFile(context.Context, string, []byte, string) error {
	return xerror.Unexpected().Detail("unexpected write")
}

func (fixedRepoStore) DeleteFile(context.Context, string, string) error {
	return xerror.Unexpected().Detail("unexpected delete")
}

func (fixedRepoStore) ListFiles(context.Context, string) ([]repostore.FileEntry, error) {
	return nil, nil
}
func (fixedRepoStore) FileExists(context.Context, string) (bool, error) { return false, nil }
