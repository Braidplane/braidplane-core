package gitprovider

import (
	"context"
	"regexp"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var ErrRepositoryCleanupFailed = xerror.New(
	xerror.KindUnavailable,
	"REPOSITORY_CLEANUP_FAILED",
	"repository bootstrap cleanup failed",
)

// Config controls provider API client construction.
type Config struct {
	AllowPlaintext    bool   `json:"-"`
	BaseURL           string `json:"base_url,omitempty" validate:"omitempty,url"`
	TimeoutSeconds    int    `json:"timeout_seconds,omitempty" validate:"omitempty,min=1,max=300"`
	AllowPrivateHosts bool   `json:"allow_private_hosts,omitempty"`
}

// Credentials holds provider API authentication values.
type Credentials struct {
	Token                    string  `json:"token" validate:"required,min=1"`
	GitHubContentsPermission *string `json:"-"`
}

// RepositoryRef identifies a provider repository without credentials.
type RepositoryRef struct {
	Provider    repostore.Provider
	Owner       string
	Name        string
	ProjectPath string
	FullName    string
}

// RepositoryFile is one sanitized file in a provider-created repository's first commit.
type RepositoryFile struct {
	Path    string
	Content string
}

// RepositoryBootstrapRequest creates a private repository with an exact template.
type RepositoryBootstrapRequest struct {
	Owner         string
	Name          string
	DefaultBranch string
	Files         []RepositoryFile
	// Marker binds resumable creation to a durable, non-secret setup identity.
	Marker               string
	ExpectedRepositoryID string
}

// RepositoryBootstrapResult is safe provider provenance for a created repository.
type RepositoryBootstrapResult struct {
	Repository   RepositoryRef
	RepositoryID string
	RemoteURL    string
	HeadSHA      string
	OwnerKind    string
}

// RepositoryClient validates, creates, and cleans up provider repositories.
type RepositoryClient interface {
	ValidateRepository(ctx context.Context, branch string) error
	BootstrapRepository(ctx context.Context, req RepositoryBootstrapRequest) (*RepositoryBootstrapResult, error)
	DeleteRepository(ctx context.Context, repositoryID string) error
}

// ProposalState is a normalized provider proposal lifecycle state.
type ProposalState string

const (
	ProposalStateOpen   ProposalState = "open"
	ProposalStateClosed ProposalState = "closed"
	ProposalStateMerged ProposalState = "merged"
)

// ProposalListRequest selects provider proposals for one source and target branch pair.
type ProposalListRequest struct {
	SourceBranch string
	TargetBranch string
}

// ProposalGetRequest selects one provider proposal by its repository-local identifier.
type ProposalGetRequest struct {
	ProposalID string
}

// ProposalRequest creates a provider-native PR/MR proposal.
type ProposalRequest struct {
	SourceBranch string
	TargetBranch string
	Title        string
	Body         string
}

// ProposalUpdateRequest updates an existing provider-native PR/MR proposal.
type ProposalUpdateRequest struct {
	ProposalID string
	Title      string
	Body       string
}

// ProposalRef contains safe provider proposal metadata used by publish, refresh, and future webhooks.
type ProposalRef struct {
	Provider     repostore.Provider
	Repository   RepositoryRef
	RepositoryID string
	ID           string
	URL          string
	SourceBranch string
	TargetBranch string
	State        ProposalState
	HeadSHA      string
	ProviderKind ProposalKind
	Closed       bool
	Merged       bool
}

// ProposalReader separates read-only proposal verification from mutation so
// proof ingestion cannot acquire provider write authority.
type ProposalReader interface {
	GetProposal(ctx context.Context, req ProposalGetRequest) (*ProposalRef, error)
}

// ProposalClient performs provider-native proposal operations.
type ProposalClient interface {
	ProposalReader
	ListOpenProposals(ctx context.Context, req ProposalListRequest) ([]ProposalRef, error)
	CreateProposal(ctx context.Context, req ProposalRequest) (*ProposalRef, error)
	UpdateProposal(ctx context.Context, req ProposalUpdateRequest) (*ProposalRef, error)
}

// ProofCheck is the sanitized provider-visible projection of one immutable Proof receipt.
type ProofCheck struct {
	RepositoryID, ChangeRequestID            string
	HeadSHA, ExternalID, DetailsURL, Summary string
	Conclusion                               string
	// ExistingOnly invalidates historical evidence without creating checks on closed or changed candidates.
	ExistingOnly bool
}

// ProofCheckPublisher creates or updates the check identified by its exact head and external ID.
type ProofCheckPublisher interface {
	PublishProofCheck(context.Context, ProofCheck) error
}

type ProofCheckClient interface {
	ProposalReader
	ProofCheckPublisher
}

// ProposalKind identifies provider-native code review proposal objects.
type ProposalKind string

const (
	ProposalKindPullRequest  ProposalKind = "pull_request"
	ProposalKindMergeRequest ProposalKind = "merge_request"
)

var credentialPattern = regexp.MustCompile(`(gh[pousr]_[A-Za-z0-9_]+|glpat-[A-Za-z0-9_-]+)`)

// NormalizeBranchName strips provider-neutral refs/heads prefixes from branch names.
func NormalizeBranchName(ref string) string {
	return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
}

// RedactProviderError removes token-like values and the resolved token from provider errors.
func RedactProviderError(err error, token string) error {
	if err == nil {
		return nil
	}

	message := credentialPattern.ReplaceAllString(err.Error(), "[REDACTED]")
	if trimmedToken := strings.TrimSpace(token); trimmedToken != "" {
		message = strings.ReplaceAll(message, trimmedToken, "[REDACTED]")
	}

	return redactedProviderError{message: message, cause: err}
}

type redactedProviderError struct {
	message string
	cause   error
}

// Error returns a credential-redacted provider error message.
func (e redactedProviderError) Error() string {
	return e.message
}

// Unwrap returns the original provider error for errors.Is/errors.As matching.
func (e redactedProviderError) Unwrap() error {
	return e.cause
}
