package proofcandidatesvc

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider/providerclient"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	proofrepo "github.com/Braidplane/braidplane-core/postgres/proofcandidates"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

type GitAuthResolver interface {
	ResolveGitAuth(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitDataSourceAuth, error)
}

type exactSourceVerifier struct {
	auth        GitAuthResolver
	remote      repostore.RemoteGit
	newProvider func(context.Context, repostore.Provider, string, []string) (gitprovider.ProposalReader, error)
	head        func(context.Context, string) *string
}

// NewExactSourceVerifier keeps source verification provider-backed so local
// repository state cannot substitute for the provider's current change head.
func NewExactSourceVerifier(auth GitAuthResolver) (SourceVerifier, error) {
	if auth == nil {
		return nil, xerror.ExecutionUnavailable().Detail("Git data source auth resolver is required")
	}
	v := &exactSourceVerifier{
		auth: auth, remote: repostore.NewRemoteGit(), newProvider: providerclient.NewProposalReader,
		head: func(ctx context.Context, root string) *string {
			return repostore.ResolveGitMetadata(ctx, root).LastCommit
		},
	}
	return v, nil
}

func (v *exactSourceVerifier) VerifySource(
	ctx context.Context,
	mapping proofrepo.Mapping,
	change gitprovider.VerifiedChange,
) ([]byte, error) {
	auth, err := v.auth.ResolveGitAuth(ctx, mapping.OrganizationSlug, mapping.ProviderConnectionSlug)
	if err != nil {
		return nil, gitprovider.ErrChangePermissionLost.With(err)
	}
	if auth.Cleanup != nil {
		defer auth.Cleanup()
	}
	provider := repostore.Provider(strings.TrimSpace(mapping.Provider))
	if strings.TrimSpace(auth.Provider) != string(provider) {
		return nil, gitprovider.ErrChangePermissionLost.Detail("mapped Git provider connection changed")
	}
	client, err := v.newProvider(ctx, provider, auth.RepoURL, auth.Env)
	if err != nil {
		return nil, changeProviderError(err)
	}
	proposal, err := client.GetProposal(ctx, gitprovider.ProposalGetRequest{ProposalID: change.ChangeRequestID})
	if err != nil {
		return nil, changeProviderError(err)
	}
	if proposal == nil {
		return nil, gitprovider.ErrChangeUnavailable.Detail("Git provider returned no change")
	}
	if proposal.State != gitprovider.ProposalStateOpen || proposal.Closed || proposal.Merged {
		return nil, gitprovider.ErrChangeClosed
	}
	if proposal.Provider != provider || proposal.RepositoryID != change.RepositoryID || proposal.ID != change.ChangeRequestID ||
		"refs/heads/"+gitprovider.NormalizeBranchName(proposal.SourceBranch) != change.SourceRef ||
		"refs/heads/"+gitprovider.NormalizeBranchName(proposal.TargetBranch) != change.TargetRef {
		return nil, gitprovider.ErrChangeStale.Detail("Git change identity no longer matches the provider")
	}
	if !strings.EqualFold(strings.TrimSpace(proposal.HeadSHA), change.HeadSHA) {
		return nil, gitprovider.ErrChangeStale
	}

	store, root, cleanup, err := v.remote.CloneRemote(
		ctx, auth.RepoURL, gitprovider.NormalizeBranchName(change.SourceRef), auth.Env,
	)
	if err != nil {
		return nil, changeCloneError(err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	head := v.head(ctx, root)
	if head == nil || !strings.EqualFold(strings.TrimSpace(*head), change.HeadSHA) {
		return nil, gitprovider.ErrChangeStale
	}
	source, err := store.ReadFile(ctx, change.PipelinePath, maxSourceBytes+1)
	if err != nil {
		if xerror.Is(err, repostore.ErrFileNotFound) {
			return nil, gitprovider.ErrChangePathMissing
		}
		return nil, gitprovider.ErrChangeUnavailable.With(err)
	}
	return source, nil
}

func changeCloneError(err error) error {
	if xerror.Is(err, repostore.ErrRemoteRefNotFound) {
		return gitprovider.ErrChangeRefDeleted.With(err)
	}
	var typed *xerror.Error
	if !xerror.As(err, &typed) {
		return gitprovider.ErrChangeUnavailable.With(err)
	}
	switch typed.Kind() {
	case xerror.KindUnauthorized, xerror.KindForbidden:
		return gitprovider.ErrChangePermissionLost.With(err)
	default:
		return gitprovider.ErrChangeUnavailable.With(err)
	}
}

func changeProviderError(err error) error {
	var typed *xerror.Error
	if !xerror.As(err, &typed) {
		return gitprovider.ErrChangeUnavailable.With(err)
	}
	switch typed.Kind() {
	case xerror.KindUnauthorized, xerror.KindForbidden:
		return gitprovider.ErrChangePermissionLost.With(err)
	case xerror.KindNotFound, xerror.KindInvalidInput:
		return gitprovider.ErrChangeUnreachable.With(err)
	default:
		return gitprovider.ErrChangeUnavailable.With(err)
	}
}
