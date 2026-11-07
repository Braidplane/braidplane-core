//go:generate -command enumer go tool -modfile=../../tools/go.mod enumer
//go:generate enumer -type=SyncStatus -json -text -linecomment -output=sync_status_enumer.go
package pipelines

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var (
	ErrPipelineReferenceNotFound         = xerror.NotFound(xerror.PipelineReference)
	ErrPipelineDraftAttemptStale         = xerror.InvalidInput(xerror.PipelineReference).Detail("pipeline draft attempt is stale")
	ErrPipelineProposalAuthorityNotFound = xerror.NotFound(xerror.PipelineReference).
						Detail("active pipeline proposal authority not found")
	ErrPipelineDeploymentConflict = xerror.New(xerror.KindAlreadyExists, "PIPELINE_DEPLOYMENT_CONFLICT", "pipeline deployment conflict")
	ErrPipelineDeploymentNotFound = xerror.NotFound(xerror.PipelineDeployment)
	ErrPipelineVersionNotFound    = xerror.NotFound(xerror.PipelineVersion)
	ErrPipelineVersionAmbiguous   = xerror.InvalidInput(xerror.PipelineVersion)
)

// PipelineReference holds indexed pipeline identity metadata.
type PipelineReference struct {
	Slug             slug.Slug
	Name             string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	CurrentVersionID *int64
	HasDraft         bool
}

// UpsertParams contains fields for creating or updating a pipeline reference.
type UpsertParams struct {
	Slug slug.Slug
	Name string
}

// SyncStatus describes whether a pipeline's DB spec is in sync with its git source.
type SyncStatus int

const (
	SyncStatusUnknown SyncStatus = iota // unknown
	SyncStatusSynced                    // synced
	SyncStatusDrifted                   // drifted
)

// PipelineSpec holds the DB-canonical pipeline spec and ownership lifecycle metadata.
type PipelineSpec struct {
	Slug                   slug.Slug
	Name                   string
	HasDraft               bool
	SpecJSONB              []byte
	SpecHash               string
	CurrentVersionID       *int64
	CurrentVersionSpecHash *string
	SyncStatus             SyncStatus
	SourcePath             *string
	SourceRef              *string
	LastAppliedCommit      *string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// UpsertSpecParams contains fields for upserting a canonical pipeline spec.
type UpsertSpecParams struct {
	Slug              slug.Slug
	Name              string
	SpecJSONB         []byte
	SpecHash          string
	SyncStatus        SyncStatus
	SourcePath        *string
	SourceRef         *string
	LastAppliedCommit *string
}

// PipelineVersion stores immutable pipeline revision metadata.
type PipelineVersion struct {
	SourcePath        *string
	ID                int64
	PipelineSlug      slug.Slug
	GitCommitSHA      *string
	CreatedAt         time.Time
	SpecJSONB         []byte
	SpecHash          string
	ResolvedContracts []byte
}

// PipelineDeployment stores the deployed immutable version for one project environment.
type PipelineDeployment struct {
	Revision                 int64
	CurrentResolutionDigest  *string
	PreviousResolutionDigest *string
	ID                       int64
	ProjectSlug              slug.Slug
	EnvironmentSlug          slug.Slug
	PipelineSlug             slug.Slug
	CurrentVersionID         int64
	CurrentVersionSpecHash   string
	PreviousVersionID        *int64
	PreviousVersionSpecHash  *string
	DeployedByActorType      string
	DeployedByActorSlug      *string
	DeployedAt               time.Time
	UpdatedAt                time.Time
}

// DeployParams contains fields for pointing an environment at a pipeline version.
type DeployParams struct {
	ExpectedRevision        *int64
	ResolutionDigest        *string
	PipelineSlug            slug.Slug
	VersionID               int64
	ExpectedCurrentSpecHash *string
	DeployedByActorType     string
	DeployedByActorSlug     *slug.Slug
}

// PipelineDraft stores mutable draft pipeline state.
type PipelineDraft struct {
	PipelineSlug              slug.Slug
	BasedOnVersionID          *int64
	BasedOnSpecHash           *string
	CurrentVersionID          *int64
	SpecJSONB                 []byte
	DraftState                *string
	BaseRef                   *string
	BaseCommitSHA             *string
	BranchName                *string
	BranchHeadSHA             *string
	ProposalProvider          *string
	ProposalID                *string
	ProposalURL               *string
	ProposalState             *string
	LastProposedDraftHash     *string
	QueuedAt                  *time.Time
	ProposalUpdatedAt         *time.Time
	FailedAt                  *time.Time
	PublishAttemptID          *string
	RefreshAttemptID          *string
	FailureCode               *string
	FailureMessage            *string
	ProposalAuthorityID       *int64
	PublicationMode           *string
	ProviderHost              *string
	ProviderAccountID         *string
	ProviderRepositoryID      *string
	ProviderCredentialID      *string
	ExecutingPrincipalID      *string
	ExecutingPrincipalDisplay *string
	AuthoritySource           *string
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// ProposalAuthority is the active provider identity selected for one project repository.
type ProposalAuthority struct {
	ID                        int64
	PublicationMode           string
	ProviderHost              string
	ProviderAccountID         string
	ProviderRepositoryID      string
	ProviderCredentialID      string
	ExecutingPrincipalID      string
	ExecutingPrincipalDisplay string
	AuthoritySource           string
	GitConfigurationHash      string
	GitRepository             string
	GitBaseRef                string
	GitDataSourceSlug         string
	GitCredentialSlug         string
	GitCredentialRevision     string
}

// UpsertDraftParams contains fields for creating or updating a pipeline draft.
type UpsertDraftParams struct {
	PipelineSlug     slug.Slug
	BasedOnVersionID *int64
	SpecJSONB        []byte
	DraftState       string
}

// QueueDraftPublishParams contains proposal continuity fields for Git-authored draft publish.
type QueueDraftPublishParams struct {
	PipelineSlug     slug.Slug
	MemberSlug       slug.Slug
	DraftState       string
	BaseRef          *string
	BaseCommitSHA    *string
	BranchName       string
	PublishAttemptID string
	Authority        ProposalAuthority
}

// CompleteDraftPublishParams contains safe provider metadata returned by proposal execution.
type CompleteDraftPublishParams struct {
	PipelineSlug          slug.Slug
	MemberSlug            slug.Slug
	DraftState            string
	BranchName            string
	BranchHeadSHA         string
	ProposalProvider      string
	ProposalID            string
	ProposalURL           string
	ProposalState         string
	LastProposedDraftHash string
	PublishAttemptID      string
}

// FailDraftPublishParams identifies a member proposal whose latest provider attempt failed.
type FailDraftPublishParams struct {
	PipelineSlug     slug.Slug
	MemberSlug       slug.Slug
	DraftState       string
	PublishAttemptID string
	FailureCode      string
	FailureMessage   string
}

// QueueDraftProposalRefreshParams identifies one exact provider refresh attempt.
type QueueDraftProposalRefreshParams struct {
	PipelineSlug     slug.Slug
	MemberSlug       slug.Slug
	ProposalID       string
	RefreshAttemptID string
}

// RefreshDraftProposalParams contains safe state read back for one exact provider proposal.
type RefreshDraftProposalParams struct {
	PipelineSlug     slug.Slug
	MemberSlug       slug.Slug
	DraftState       string
	BranchName       *string
	BranchHeadSHA    *string
	ProposalProvider *string
	ProposalID       *string
	ProposalURL      *string
	ProposalState    *string
	RefreshAttemptID string
}

// FailDraftProposalRefreshParams records safe diagnostics without changing proposal lifecycle state.
type FailDraftProposalRefreshParams struct {
	PipelineSlug     slug.Slug
	MemberSlug       slug.Slug
	ProposalID       string
	RefreshAttemptID string
	FailureCode      string
	FailureMessage   string
}

// CreateVersionParams contains fields for creating a pipeline version.
type CreateVersionParams struct {
	SourcePath        *string
	PipelineSlug      slug.Slug
	GitCommitSHA      *string
	SpecJSONB         []byte
	SpecHash          string
	ResolvedContracts []byte
}
