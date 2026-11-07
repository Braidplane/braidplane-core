package pipelinegit

import (
	"bytes"
	"context"
	"encoding/hex"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
	gitlabprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/gitlab"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var ErrProviderProposalUnsupported = xerror.New(
	xerror.KindInvalidInput,
	"PROVIDER_PROPOSAL_UNSUPPORTED",
	"provider proposal is unsupported",
)

var ErrProposalBranchHeadConflict = xerror.New(
	xerror.KindAlreadyExists,
	"PROPOSAL_BRANCH_HEAD_CONFLICT",
	"proposal branch head changed",
)

var ErrProposalAuthorityInvalid = xerror.New(
	xerror.KindUnauthorized,
	"PROPOSAL_AUTHORITY_INVALID",
	"provider authority is unavailable, revoked, or does not match the repository",
)

var ErrCreationTargetExists = xerror.New(
	xerror.KindAlreadyExists,
	"PIPELINE_CREATION_TARGET_EXISTS",
	"pipeline creation cannot replace an existing repository file",
)

const (
	providerGitHub = "github"
	providerGitLab = "gitlab"
)

// ProposalExecutor applies a pipeline draft to a branch and opens or updates a provider PR/MR.
type ProposalExecutor interface {
	PublishProposal(ctx context.Context, op ProposalOperation) (*ProposalResult, error)
	RefreshProposal(ctx context.Context, op ProposalRefreshOperation) (*ProposalResult, error)
}

// ProposalOperation describes one outbound Git-authored draft proposal publish.
type ProposalOperation struct {
	CreateOnly       bool
	OrganizationSlug slug.Slug
	DataSourceSlug   slug.Slug
	PipelineSlug     slug.Slug
	BaseRef          *string
	BaseCommitSHA    *string
	ExpectedHeadSHA  *string
	BranchName       string
	Path             string
	Content          []byte
	CommitMessage    string
	ProposalTitle    string
	ProposalBody     string
	Authority        model.PipelineProposalAuthorityPayload
}

// ProposalRefreshOperation identifies one provider proposal whose last-known state should be refreshed.
type ProposalRefreshOperation struct {
	OrganizationSlug slug.Slug
	DataSourceSlug   slug.Slug
	ProposalID       string
}

// ProposalResult contains safe provider metadata for persistence.
type ProposalResult struct {
	Provider      string
	ProposalID    string
	ProposalURL   string
	ProposalState string
	BranchName    string
	BranchHeadSHA string
}

// ValidatePublication keeps branch-only results distinct from provider review objects.
func (r *ProposalResult) ValidatePublication(authority model.PipelineProposalAuthorityPayload, branch string) error {
	if r == nil {
		return xerror.Unexpected().Detail("pipeline Git proposal executor returned nil result")
	}
	if authority.PublicationMode != model.PipelineProposalModeRepositoryCredential {
		if r.Provider == "git" {
			return ErrProposalAuthorityInvalid.Detail("plain Git result requires repository credential authority")
		}
		return nil
	}
	_, hashErr := hex.DecodeString(r.BranchHeadSHA)
	if r.Provider != "git" || r.BranchName != branch || branch == "" ||
		r.ProposalID != "" || r.ProposalURL != "" || r.ProposalState != "" || hashErr != nil ||
		(len(r.BranchHeadSHA) != 40 && len(r.BranchHeadSHA) != 64) {
		return xerror.Unexpected().With(hashErr).Detail("plain Git publication must return the exact proposal branch and commit without a provider review object")
	}
	return nil
}

// ProposalCompletion carries safe proposal metadata plus draft identity for persistence.
type ProposalCompletion struct {
	CreationSlug     slug.Slug
	OrganizationSlug slug.Slug
	ProjectSlug      slug.Slug
	PipelineSlug     slug.Slug
	MemberSlug       slug.Slug
	Provider         string
	ProposalID       string
	ProposalURL      string
	ProposalState    string
	BranchName       string
	BranchHeadSHA    string
	DraftSpecHash    string
	PublishAttemptID string
	Authority        model.PipelineProposalAuthorityPayload
}

// ProposalFailure identifies a member-scoped proposal attempt without retaining private provider diagnostics.
type ProposalFailure struct {
	CreationSlug     slug.Slug
	OrganizationSlug slug.Slug
	ProjectSlug      slug.Slug
	PipelineSlug     slug.Slug
	MemberSlug       slug.Slug
	PublishAttemptID string
	FailureCode      string
	FailureMessage   string
}

// ProposalRefreshCompletion carries safe provider state refreshed by exact proposal ID.
type ProposalRefreshCompletion struct {
	OrganizationSlug slug.Slug
	ProjectSlug      slug.Slug
	PipelineSlug     slug.Slug
	MemberSlug       slug.Slug
	Provider         string
	ProposalID       string
	ProposalURL      string
	ProposalState    string
	BranchName       string
	BranchHeadSHA    string
	RefreshAttemptID string
}

// ProposalRefreshFailure identifies an exact refresh without changing proposal lifecycle state.
type ProposalRefreshFailure struct {
	OrganizationSlug slug.Slug
	ProjectSlug      slug.Slug
	PipelineSlug     slug.Slug
	MemberSlug       slug.Slug
	ProposalID       string
	RefreshAttemptID string
	FailureCode      string
	FailureMessage   string
}

// ProposalBranchHeadConflictError reports only safe commit identities for an externally changed proposal branch.
type ProposalBranchHeadConflictError struct {
	Expected string
	Actual   string
}

func (e *ProposalBranchHeadConflictError) Error() string {
	return ErrProposalBranchHeadConflict.Detailf("expected %s, found %s", e.Expected, e.Actual).Error()
}

func (e *ProposalBranchHeadConflictError) Unwrap() error { return ErrProposalBranchHeadConflict }

// ProviderProposalRequest is the provider-native PR/MR upsert request.
type ProviderProposalRequest struct {
	BaseRef       string
	BaseCommitSHA string
	BranchName    string
	Title         string
	Body          string
}

type proposalProvider interface {
	UpsertProposal(ctx context.Context, req ProviderProposalRequest) (*ProposalResult, error)
	GetProposal(ctx context.Context, proposalID string) (*ProposalResult, error)
}

type proposalExecutor struct {
	gitAuthResolver DataSourceGitAuthResolver
	remoteGit       repostore.RemoteGit
	provider        proposalProvider
	resolveHead     func(context.Context, string) *string
}

// NewProposalExecutor creates the production executor for provider-native proposal publish.
func NewProposalExecutor(gitAuthResolver DataSourceGitAuthResolver) (ProposalExecutor, error) {
	if gitAuthResolver == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("git auth resolver is required"),
		)
	}

	return &proposalExecutor{
		gitAuthResolver: gitAuthResolver,
		remoteGit:       repostore.NewRemoteGit(),
	}, nil
}

func (e *proposalExecutor) PublishProposal(ctx context.Context, op ProposalOperation) (*ProposalResult, error) {
	if err := validateProposalAuthority(op.Authority); err != nil {
		return nil, err
	}
	auth, err := e.gitAuthResolver.ResolveGitAuth(ctx, op.OrganizationSlug, op.DataSourceSlug)
	if err != nil {
		return nil, xerror.Wrapf("resolve git datasource auth: %w", err)
	}
	if auth.Cleanup != nil {
		defer auth.Cleanup()
	}
	if auth.GitHubProposalAuthority != nil {
		current := auth.GitHubProposalAuthority.Authority.Payload()
		current.ID = op.Authority.ID
		if current != op.Authority {
			return nil, ErrProposalAuthorityInvalid.Detail("GitHub App publishing identity changed")
		}
	} else if op.Authority.AuthoritySource == "github_app_installation" {
		return nil, ErrProposalAuthorityInvalid.Detail("GitHub App publishing authority is unavailable")
	}
	plainGit := op.Authority.PublicationMode == model.PipelineProposalModeRepositoryCredential
	var provider proposalProvider
	if plainGit {
		if auth.Provider != "git" || !auth.GitWritable || auth.GitHubProposalAuthority != nil ||
			repostore.NormalizeRemoteURL(auth.RepoURL) != op.Authority.GitRepository ||
			auth.GitConfigurationHash != op.Authority.GitConfigurationHash ||
			auth.GitDataSourceSlug != op.Authority.GitDataSourceSlug || op.DataSourceSlug.String() != op.Authority.GitDataSourceSlug ||
			auth.GitCredentialSlug != op.Authority.GitCredentialSlug || auth.GitCredentialRevision != op.Authority.GitCredentialRevision {
			return nil, ErrProposalAuthorityInvalid.Detail("Git repository credential binding changed")
		}
	} else {
		provider, err = e.resolveProvider(ctx, auth.Provider, auth.RepoURL, auth.Env)
		if err != nil {
			return nil, err
		}
		normalizedRepository := repostore.NormalizeRemoteURL(auth.RepoURL)
		providerHost, providerRepository, found := strings.Cut(normalizedRepository, "/")
		if auth.GitHubProposalAuthority != nil {
			providerRepository = auth.GitHubProposalAuthority.Authority.ProviderRepositoryID
		}
		if !found || !strings.EqualFold(providerHost, op.Authority.ProviderHost) ||
			providerRepository != op.Authority.ProviderRepositoryID {
			return nil, ErrProposalAuthorityInvalid.Detail("repository binding changed")
		}
	}

	baseBranch := normalizeBranchName(firstNonEmptyString(op.BaseRef, &auth.DefaultBranch))
	if baseBranch == "" {
		return nil, xerror.InvalidRequired(xerror.RepoFile).Detail("proposal base ref is required")
	}
	branchName := strings.TrimSpace(op.BranchName)
	if branchName == "" {
		return nil, xerror.InvalidRequired(xerror.RepoFile).Detail("proposal branch name is required")
	}
	if plainGit && (op.Authority.GitBaseRef != "refs/heads/"+baseBranch || !strings.HasPrefix(branchName, "braidplane/") || branchName == baseBranch) {
		return nil, ErrProposalAuthorityInvalid.Detail("plain Git proposals must use a separate Braidplane proposal branch")
	}

	if err := e.remoteGit.CheckRemote(ctx, auth.RepoURL, auth.Env); err != nil {
		return nil, xerror.Wrapf("git datasource pre-flight check: %w", err)
	}

	repoStore, repoRoot, cleanup, branchExisted, err := e.cloneProposalSource(
		ctx, auth.RepoURL, branchName, baseBranch, auth.Env,
	)
	if err != nil {
		return nil, xerror.Wrapf("clone git datasource: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	current, err := repoStore.ReadFile(ctx, op.Path, 0)
	if err != nil && !xerror.Is(err, repostore.ErrFileNotFound) {
		return nil, xerror.Wrapf("read proposal file: %w", err)
	}
	changed := err != nil || !bytes.Equal(current, op.Content)
	if op.CreateOnly && err == nil && (!branchExisted || changed) {
		return nil, ErrCreationTargetExists
	}
	currentHead := e.head(ctx, repoRoot)
	expectedHead := strings.TrimSpace(valueOrEmpty(op.ExpectedHeadSHA))
	if expectedHead == "" && branchExisted && changed {
		expectedHead = strings.TrimSpace(valueOrEmpty(op.BaseCommitSHA))
	}
	if changed && expectedHead != "" &&
		(currentHead == nil || !strings.EqualFold(expectedHead, strings.TrimSpace(*currentHead))) {
		actual := "unknown"
		if currentHead != nil {
			actual = strings.TrimSpace(*currentHead)
		}
		return nil, &ProposalBranchHeadConflictError{Expected: expectedHead, Actual: actual}
	}
	if changed {
		if err := repoStore.WriteFile(ctx, op.Path, op.Content, op.CommitMessage); err != nil {
			return nil, xerror.Wrapf("write proposal file: %w", err)
		}
		if err := e.remoteGit.PushRemote(
			ctx,
			repoRoot,
			auth.RepoURL,
			auth.Env,
			repostore.PushOptions{Refspec: "HEAD:refs/heads/" + branchName},
		); err != nil {
			return nil, xerror.Wrapf("push proposal branch: %w", err)
		}
	}

	if plainGit {
		head := e.head(ctx, repoRoot)
		if head == nil || strings.TrimSpace(*head) == "" {
			return nil, xerror.Unexpected().Detail("pushed Git proposal commit is unavailable")
		}
		return &ProposalResult{Provider: "git", BranchName: branchName, BranchHeadSHA: strings.TrimSpace(*head)}, nil
	}

	result, err := provider.UpsertProposal(ctx, ProviderProposalRequest{
		BaseRef:       "refs/heads/" + baseBranch,
		BaseCommitSHA: strings.TrimSpace(valueOrEmpty(op.BaseCommitSHA)),
		BranchName:    branchName,
		Title:         op.ProposalTitle,
		Body:          op.ProposalBody,
	})
	if err != nil {
		return nil, sanitizeProviderError(err)
	}
	if result == nil {
		return nil, xerror.Unexpected().Detail("provider proposal publish returned nil result")
	}
	if result.BranchName == "" {
		result.BranchName = branchName
	}
	// Provider proposal responses can lag a successful Git push.
	if head := e.head(ctx, repoRoot); head != nil {
		result.BranchHeadSHA = strings.TrimSpace(*head)
	}
	return result, nil
}

func validateProposalAuthority(authority model.PipelineProposalAuthorityPayload) error {
	if authority.PublicationMode == model.PipelineProposalModeRepositoryCredential {
		if authority.ID <= 0 || authority.ValidateForPersistence() != nil {
			return ErrProposalAuthorityInvalid.Detail("Git repository credential authority is incomplete")
		}
		return nil
	}

	if authority.ID <= 0 ||
		strings.TrimSpace(authority.ProviderHost) == "" ||
		strings.TrimSpace(authority.ProviderAccountID) == "" ||
		strings.TrimSpace(authority.ProviderRepositoryID) == "" ||
		strings.TrimSpace(authority.ExecutingPrincipalID) == "" ||
		strings.TrimSpace(authority.ExecutingPrincipalDisplay) == "" ||
		strings.TrimSpace(authority.AuthoritySource) == "" {
		return ErrProposalAuthorityInvalid.Detail("provider authority binding is incomplete")
	}
	if authority.PublicationMode != model.PipelineProposalModeDelegatedUser &&
		authority.PublicationMode != model.PipelineProposalModeOrganizationService {
		return ErrProposalAuthorityInvalid.Detail("provider authority publication mode is unsupported")
	}
	return nil
}

func (e *proposalExecutor) RefreshProposal(ctx context.Context, op ProposalRefreshOperation) (*ProposalResult, error) {
	proposalID := strings.TrimSpace(op.ProposalID)
	if proposalID == "" {
		return nil, xerror.InvalidRequired(xerror.RepoFile).Detail("proposal ID is required")
	}
	auth, err := e.gitAuthResolver.ResolveGitAuth(ctx, op.OrganizationSlug, op.DataSourceSlug)
	if err != nil {
		return nil, xerror.Wrapf("resolve git datasource auth: %w", err)
	}
	if auth.Cleanup != nil {
		defer auth.Cleanup()
	}
	provider, err := e.resolveProvider(ctx, auth.Provider, auth.RepoURL, auth.Env)
	if err != nil {
		return nil, err
	}
	result, err := provider.GetProposal(ctx, proposalID)
	if err != nil {
		return nil, sanitizeProviderError(err)
	}
	if result == nil {
		return nil, xerror.Unexpected().Detail("provider proposal refresh returned nil result")
	}
	return result, nil
}

func (e *proposalExecutor) head(ctx context.Context, repoRoot string) *string {
	if e.resolveHead != nil {
		return e.resolveHead(ctx, repoRoot)
	}
	return repostore.ResolveGitMetadata(ctx, repoRoot).LastCommit
}

func (e *proposalExecutor) cloneProposalSource(
	ctx context.Context,
	repoURL, branchName, baseBranch string,
	env []string,
) (repostore.RepoStore, string, func(), bool, error) {
	repoStore, repoRoot, cleanup, err := e.remoteGit.CloneRemote(ctx, repoURL, branchName, env)
	if err == nil {
		return repoStore, repoRoot, cleanup, true, nil
	}
	repoStore, repoRoot, cleanup, err = e.remoteGit.CloneRemote(ctx, repoURL, baseBranch, env)
	return repoStore, repoRoot, cleanup, false, err
}

func (e *proposalExecutor) resolveProvider(
	ctx context.Context,
	provider, repoURL string,
	env []string,
) (proposalProvider, error) {
	if e.provider != nil {
		return e.provider, nil
	}
	switch firstNonEmptyString(&provider, ptrString(string(repostore.DetectProvider(repoURL)))) {
	case providerGitHub:
		token := tokenFromEnv(env, "GITHUB_TOKEN")
		if token == "" {
			return nil, xerror.ExecutionUnavailable().Detail("GitHub token is required for proposal publish")
		}
		return newGitHubProposalProvider(ctx, repoURL, token)
	case providerGitLab:
		token := tokenFromEnv(env, "GITLAB_TOKEN")
		if token == "" {
			return nil, xerror.ExecutionUnavailable().Detail("GitLab token is required for proposal publish")
		}
		return newGitLabProposalProvider(ctx, repoURL, token)
	default:
		return nil, ErrProviderProposalUnsupported.Detail(
			"generic git remotes can push branches but cannot create pull or merge requests",
		)
	}
}

type providerProposalAdapter struct {
	client gitprovider.ProposalClient
}

// newGitHubProposalProvider builds the GitHub proposal adapter for publish orchestration.
func newGitHubProposalProvider(
	ctx context.Context,
	repoURL, token string,
) (*providerProposalAdapter, error) {
	repo, err := gitprovider.RepositoryRefFromRemoteURL(repostore.ProviderGitHub, repoURL)
	if err != nil {
		return nil, err
	}
	client, err := githubprovider.New(
		ctx,
		gitprovider.Config{},
		gitprovider.Credentials{Token: token},
		repo,
	)
	if err != nil {
		return nil, err
	}
	return &providerProposalAdapter{client: client}, nil
}

// newGitLabProposalProvider builds the GitLab proposal adapter for publish orchestration.
func newGitLabProposalProvider(
	ctx context.Context,
	repoURL, token string,
) (*providerProposalAdapter, error) {
	repo, err := gitprovider.RepositoryRefFromRemoteURL(repostore.ProviderGitLab, repoURL)
	if err != nil {
		return nil, err
	}
	client, err := gitlabprovider.New(
		ctx,
		gitprovider.Config{},
		gitprovider.Credentials{Token: token},
		repo,
	)
	if err != nil {
		return nil, err
	}
	return &providerProposalAdapter{client: client}, nil
}

// UpsertProposal updates an open provider proposal or creates one when none exists.
func (p *providerProposalAdapter) UpsertProposal(
	ctx context.Context,
	req ProviderProposalRequest,
) (*ProposalResult, error) {
	existing, err := p.client.ListOpenProposals(ctx, gitprovider.ProposalListRequest{
		SourceBranch: req.BranchName,
		TargetBranch: req.BaseRef,
	})
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		updated, err := p.client.UpdateProposal(ctx, gitprovider.ProposalUpdateRequest{
			ProposalID: existing[0].ID,
			Title:      req.Title,
			Body:       req.Body,
		})
		if err != nil {
			return nil, err
		}
		return proposalResultFromRef(updated)
	}

	created, err := p.client.CreateProposal(ctx, gitprovider.ProposalRequest{
		SourceBranch: req.BranchName,
		TargetBranch: req.BaseRef,
		Title:        req.Title,
		Body:         req.Body,
	})
	if err != nil {
		return nil, err
	}
	return proposalResultFromRef(created)
}

func (p *providerProposalAdapter) GetProposal(ctx context.Context, proposalID string) (*ProposalResult, error) {
	ref, err := p.client.GetProposal(ctx, gitprovider.ProposalGetRequest{ProposalID: proposalID})
	if err != nil {
		return nil, err
	}
	return proposalResultFromRef(ref)
}

func proposalResultFromRef(ref *gitprovider.ProposalRef) (*ProposalResult, error) {
	if ref == nil {
		return nil, xerror.Unexpected().Detail("git provider returned nil proposal result")
	}
	return &ProposalResult{
		Provider:      string(ref.Provider),
		ProposalID:    ref.ID,
		ProposalURL:   ref.URL,
		ProposalState: string(ref.State),
		BranchName:    ref.SourceBranch,
		BranchHeadSHA: ref.HeadSHA,
	}, nil
}

func tokenFromEnv(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func normalizeBranchName(ref string) string {
	branch := strings.TrimSpace(ref)
	return strings.TrimPrefix(branch, "refs/heads/")
}

func firstNonEmptyString(values ...*string) string {
	for _, value := range values {
		if value == nil {
			continue
		}
		if trimmed := strings.TrimSpace(*value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func ptrString(value string) *string {
	return &value
}

func sanitizeProviderError(err error) error {
	return gitprovider.RedactProviderError(err, "")
}
