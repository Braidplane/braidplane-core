package pipelinesvc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
)

// PipelineStore provides runtime access to pipeline specs materialized from Git.
type PipelineStore interface {
	ResolvePipelineContracts(context.Context, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*pipelinespec.Pipeline, []model.PinnedODCSPort, error)
	ResolveProposalAuthority(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
	) (model.PipelineProposalAuthority, error)

	// FindSpec returns the full canonical spec record including Git provenance.
	// Returns ErrPipelineNotFound if no spec exists.
	FindSpec(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) (*pipelinesrepo.PipelineSpec, error)

	// FindSpecForUpdate returns the full canonical spec record locked in the current unit of work.
	// Returns ErrPipelineNotFound if no spec exists.
	FindSpecForUpdate(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) (*pipelinesrepo.PipelineSpec, error)

	// Get returns the parsed pipeline spec from the canonical DB store.
	// Returns ErrPipelineNotFound if no spec exists.
	Get(ctx context.Context, orgSlug, projectSlug, pipelineSlug slug.Slug) (*pipelinespec.Pipeline, error)

	// List returns canonical spec records for one page of pipelines in an org.
	List(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		req model.ListRequest,
	) ([]*pipelinesrepo.PipelineSpec, error)

	// Exists reports whether a canonical pipeline spec exists for the given slugs.
	Exists(ctx context.Context, orgSlug, projectSlug, pipelineSlug slug.Slug) (bool, error)

	// ListVersions returns persisted versions for one pipeline, newest first.
	ListVersions(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) ([]*pipelinesrepo.PipelineVersion, error)

	// FindVersionBySpecHash returns a persisted version snapshot by spec hash.
	FindVersionBySpecHash(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		specHash string,
	) (*pipelinesrepo.PipelineVersion, error)

	// FindVersionBySpecHashPrefix returns a persisted version snapshot by unique spec hash prefix.
	FindVersionBySpecHashPrefix(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		specHashPrefix string,
	) (*pipelinesrepo.PipelineVersion, error)

	// Deploy points one project environment at an existing immutable version.
	Deploy(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		params pipelinesrepo.DeployParams,
	) (*pipelinesrepo.PipelineDeployment, error)

	// FindDeployment returns the current deployment pointer for one environment and pipeline.
	FindDeployment(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	) (*pipelinesrepo.PipelineDeployment, error)

	// ListDeployments returns deployed pipeline pointers for one project environment.
	ListDeployments(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) ([]*pipelinesrepo.PipelineDeployment, error)
	ProjectEnvironmentEnabled(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) (bool, error)
	ProjectEnvironmentProtection(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) (*model.ProjectEnvironmentProtection, error)

	// FindDraft returns mutable draft metadata for a pipeline.
	FindDraft(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	) (*pipelinesrepo.PipelineDraft, error)
	// SaveDraft upserts a mutable draft for a pipeline.
	SaveDraft(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		memberSlug slug.Slug,
		pipeline *pipelinespec.Pipeline,
		basedOnSpecHash *string,
	) error

	// PrepareGitDraftPublish records queued publish continuity for one Git-authored
	// draft inside the current unit of work and returns the outbound git write request.
	PrepareGitDraftPublish(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
		fallbackBaseRef *string,
		expectedSpecHash *string,
		force bool,
	) (*GitDraftPublishRequest, error)
	PrepareGitProposalRefresh(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	) (*GitProposalRefreshRequest, error)
	CompleteProposal(ctx context.Context, completed pipelinegit.ProposalCompletion) error
	CompleteProposalRefresh(ctx context.Context, completed pipelinegit.ProposalRefreshCompletion) error
	FailProposal(ctx context.Context, failed pipelinegit.ProposalFailure) error
	FailProposalRefresh(ctx context.Context, failed pipelinegit.ProposalRefreshFailure) error

	// DeleteDraft removes a mutable draft for a pipeline.
	DeleteDraft(ctx context.Context, orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug) error

	// ReconcileFromGit scans Git-backed pipeline files and synchronizes DB-canonical
	// specs/references. It performs inbound Git-to-DB reconciliation only,
	// without outbound DB-to-Git side effects.
	ReconcileFromGit(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
		repoStore repostore.RepoStore,
		params ReconcileFromGitParams,
	) (*ReconcileFromGitResult, error)
}

// ReconcileFromGitParams controls a single org-scoped reconcile run.
//
// SourceRepo and SourceRef record reconcile provenance for audit, drift reporting, and replay.
type ReconcileFromGitParams struct {
	PipelineDir          string
	PipelineSlugs        []slug.Slug
	SourceDataSourceSlug *slug.Slug
	SourceRepo           *string
	SourceRef            *string
	LastAppliedCommit    *string
	DryRun               bool
}

// ReconcileFromGitResult captures the effect of one reconcile run.
type ReconcileFromGitResult struct {
	Scanned               int
	Upserted              int
	Synced                int
	Deleted               int
	Skipped               int
	ResetDrifted          int
	Failed                int
	SyncedPipelines       []ReconcileFromGitSynced
	DeletedPipelines      []ReconcileFromGitDeleted
	SkippedPipelines      []ReconcileFromGitSkipped
	ResetDriftedPipelines []ReconcileFromGitReset
	Failures              []ReconcileFromGitFailure
}

const (
	ReconcileFromGitSkippedReasonFilteredOut = "filtered_out"
	maxReconcilePipelineSpecBytes            = 10 * 1024 * 1024
)

// ReconcileFromGitSynced captures one git spec synchronized into DB-canonical storage.
type ReconcileFromGitSynced struct {
	PipelineSlug string
	SourcePath   string
	SpecHash     string
}

// ReconcileFromGitDeleted captures one stale Git-authored DB pipeline removed by reconcile.
type ReconcileFromGitDeleted struct {
	PipelineSlug       string
	PreviousSourcePath *string
}

// ReconcileFromGitSkipped captures one pipeline intentionally skipped by reconcile.
type ReconcileFromGitSkipped struct {
	PipelineSlug string
	SourcePath   *string
	Reason       string
}

// ReconcileFromGitReset captures one git reconcile that replaced a drifted DB spec.
type ReconcileFromGitReset struct {
	PipelineSlug     string
	SourcePath       string
	PreviousSpecHash string
	AppliedSpecHash  string
}

// ReconcileFromGitFailure captures one pipeline-level failure during reconcile.
type ReconcileFromGitFailure struct {
	PipelinePath string
	PipelineSlug string
	Stage        string
	Message      string
}

// GitDraftPublishRequest is the queued outbound git write for a Git-authored draft publish.
type GitDraftPublishRequest struct {
	ProjectSlug      slug.Slug
	PipelineSlug     slug.Slug
	MemberSlug       slug.Slug
	BaseRef          *string
	BaseCommitSHA    *string
	ExpectedHeadSHA  *string
	BranchName       string
	SourcePath       string
	Content          []byte
	CommitMessage    string
	ProposalTitle    string
	ProposalBody     string
	DraftSpecHash    string
	PublishAttemptID string
	Authority        model.PipelineProposalAuthority
}

// GitProposalRefreshRequest identifies one exact provider proposal for an idempotent state refresh.
type GitProposalRefreshRequest struct {
	ProjectSlug      slug.Slug
	PipelineSlug     slug.Slug
	MemberSlug       slug.Slug
	ProposalID       string
	RefreshAttemptID string
}

// GitProposalMetadata is safe provider proposal metadata returned by worker execution.
type GitProposalMetadata struct {
	Provider         string
	ProposalID       string
	ProposalURL      string
	ProposalState    string
	BranchName       string
	BranchHeadSHA    string
	DraftSpecHash    string
	PublishAttemptID string
}

type pipelineStore struct {
	dbtx postgres.DBTX
	txm  postgres.TransactionManager
	repo pipelinesrepo.Repository
	odcs odcscontracts.Repository
}

// NewPipelineStore creates a PipelineStore backed by the canonical DB spec table.
func NewPipelineStore(
	dbtx postgres.DBTX,
	txm postgres.TransactionManager,
	repo pipelinesrepo.Repository,
	odcs odcscontracts.Repository,
) (PipelineStore, error) {
	if dbtx == nil {
		return nil, xerror.Unexpected().Detail("pipelinesvc: dbtx is required for PipelineStore")
	}
	if txm == nil {
		return nil, xerror.Unexpected().Detail("pipelinesvc: txm is required for PipelineStore")
	}
	if repo == nil {
		return nil, xerror.Unexpected().Detail(
			"pipelinesvc: pipeline reference repository is required for PipelineStore",
		)
	}
	if odcs == nil {
		return nil, xerror.Unexpected().Detail("pipelinesvc: ODCS repository is required for PipelineStore")
	}
	return &pipelineStore{dbtx: dbtx, txm: txm, repo: repo, odcs: odcs}, nil
}

func (s *pipelineStore) FindSpec(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineSpec, error) {
	spec, err := s.repo.FindSpec(ctx, s.dbtx, orgSlug, projectSlug, pipelineSlug)
	if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
		return nil, ErrPipelineNotFound.With(err)
	}
	return spec, err
}

func (s *pipelineStore) FindSpecForUpdate(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineSpec, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*pipelinesrepo.PipelineSpec, error) {
		spec, err := s.repo.FindSpecForUpdate(ctx, tx, orgSlug, projectSlug, pipelineSlug)
		if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return nil, ErrPipelineNotFound.With(err)
		}
		return spec, err
	})
}

func (s *pipelineStore) ResolveProposalAuthority(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
) (model.PipelineProposalAuthority, error) {
	authority, err := s.resolveProposalAuthority(ctx, s.dbtx, orgSlug, projectSlug)
	if err != nil {
		return model.PipelineProposalAuthority{}, err
	}
	return proposalAuthorityFromRepository(authority), nil
}

func proposalAuthorityFromRepository(authority *pipelinesrepo.ProposalAuthority) model.PipelineProposalAuthority {
	return model.PipelineProposalAuthority{
		ID:                        authority.ID,
		PublicationMode:           authority.PublicationMode,
		ProviderHost:              authority.ProviderHost,
		ProviderAccountID:         authority.ProviderAccountID,
		ProviderRepositoryID:      authority.ProviderRepositoryID,
		ProviderCredentialID:      authority.ProviderCredentialID,
		ExecutingPrincipalID:      authority.ExecutingPrincipalID,
		ExecutingPrincipalDisplay: authority.ExecutingPrincipalDisplay,
		AuthoritySource:           authority.AuthoritySource,
		GitConfigurationHash:      authority.GitConfigurationHash,
		GitRepository:             authority.GitRepository,
		GitBaseRef:                authority.GitBaseRef,
		GitDataSourceSlug:         authority.GitDataSourceSlug,
		GitCredentialSlug:         authority.GitCredentialSlug,
		GitCredentialRevision:     authority.GitCredentialRevision,
	}
}

func (s *pipelineStore) Get(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (*pipelinespec.Pipeline, error) {
	spec, err := s.repo.FindSpec(ctx, s.dbtx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return nil, ErrPipelineNotFound.With(err)
		}
		return nil, err
	}

	return pipelineFromJSON(spec.SpecJSONB)
}

func (s *pipelineStore) FindDraft(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) (*pipelinesrepo.PipelineDraft, error) {
	if memberSlug == "" {
		return nil, ErrPipelineDraftNotFound
	}

	draft, err := s.repo.FindDraft(ctx, s.dbtx, orgSlug, projectSlug, pipelineSlug, memberSlug)
	if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
		return nil, ErrPipelineDraftNotFound.With(err)
	}

	return draft, err
}

func pipelineSpecMatchesCurrentCanonical(
	specJSON []byte,
	specHash string,
	existingSpec *pipelinesrepo.PipelineSpec,
) bool {
	if existingSpec == nil {
		return false
	}
	if len(specJSON) > 0 &&
		len(existingSpec.SpecJSONB) > 0 &&
		bytes.Equal(specJSON, existingSpec.SpecJSONB) {
		return true
	}
	if specHash != "" && specHash == existingSpec.SpecHash {
		return true
	}
	if specHash != "" &&
		existingSpec.CurrentVersionSpecHash != nil &&
		specHash == *existingSpec.CurrentVersionSpecHash {
		return true
	}
	return false
}

func (s *pipelineStore) SaveDraft(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	memberSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	basedOnSpecHash *string,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return saveDraft(s, ctx, tx, orgSlug, projectSlug, memberSlug, pipeline, basedOnSpecHash)
	})
}

func saveDraft(
	s *pipelineStore,
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	memberSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	basedOnSpecHash *string,
) error {
	if memberSlug == "" {
		return xerror.InvalidRequired(xerror.Member)
	}

	if pipeline == nil {
		return xerror.InvalidRequired(xerror.Pipeline)
	}

	specJSON, err := json.Marshal(pipeline)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	existingSpec, findErr := s.repo.FindSpec(ctx, tx, orgSlug, projectSlug, pipeline.Metadata.Slug)
	if findErr != nil {
		if xerror.Is(findErr, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return ErrPipelineNotFound.With(findErr)
		}
		return xerror.Wrapf(
			"find pipeline spec %s before save draft: %w",
			pipeline.Metadata.Slug,
			findErr,
		)
	}

	var resolvedBasedOnVersionID *int64
	// An explicit client baseline means "save only if I'm still editing the
	// version I started from". If the draft already matches the latest
	// canonical spec, rebase the draft to the current version instead of
	// surfacing a version-only conflict.
	if basedOnSpecHash != nil {
		if pipelineSpecHashesMatch(existingSpec.CurrentVersionSpecHash, basedOnSpecHash) {
			resolvedBasedOnVersionID = existingSpec.CurrentVersionID
		} else {
			specHash := fmt.Sprintf("%x", sha256.Sum256(specJSON))
			if pipelineSpecMatchesCurrentCanonical(specJSON, specHash, existingSpec) {
				resolvedBasedOnVersionID = existingSpec.CurrentVersionID
			} else {
				return &PipelineDraftConflictError{
					PipelineSlug:     pipeline.Metadata.Slug,
					ExpectedSpecHash: basedOnSpecHash,
					CurrentSpecHash:  existingSpec.CurrentVersionSpecHash,
					DraftPreserved:   false,
					CanOverwrite:     false,
				}
			}
		}
	}
	if basedOnSpecHash != nil && resolvedBasedOnVersionID == nil {
		specHash := fmt.Sprintf("%x", sha256.Sum256(specJSON))
		if pipelineSpecMatchesCurrentCanonical(specJSON, specHash, existingSpec) {
			resolvedBasedOnVersionID = existingSpec.CurrentVersionID
		} else {
			return &PipelineDraftConflictError{
				PipelineSlug:     pipeline.Metadata.Slug,
				ExpectedSpecHash: basedOnSpecHash,
				CurrentSpecHash:  existingSpec.CurrentVersionSpecHash,
				DraftPreserved:   false,
				CanOverwrite:     false,
			}
		}
	}

	if resolvedBasedOnVersionID == nil {
		existingDraft, draftErr := s.repo.FindDraft(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			pipeline.Metadata.Slug,
			memberSlug,
		)
		if draftErr == nil {
			resolvedBasedOnVersionID = existingDraft.BasedOnVersionID
		} else if !xerror.Is(draftErr, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return xerror.Wrapf(
				"find existing pipeline draft %s before save draft: %w",
				pipeline.Metadata.Slug,
				draftErr,
			)
		}

		if resolvedBasedOnVersionID == nil {
			resolvedBasedOnVersionID = existingSpec.CurrentVersionID
		}
	}

	_, err = s.repo.UpsertDraft(ctx, tx, orgSlug, projectSlug, memberSlug, pipelinesrepo.UpsertDraftParams{
		PipelineSlug:     pipeline.Metadata.Slug,
		BasedOnVersionID: resolvedBasedOnVersionID,
		SpecJSONB:        specJSON,
		DraftState:       pipelineDraftStateEditing,
	})
	if err != nil {
		return xerror.Wrapf("upsert pipeline draft %s: %w", pipeline.Metadata.Slug, err)
	}

	if err := s.repo.SetHasDraft(ctx, tx, orgSlug, projectSlug, pipeline.Metadata.Slug, true); err != nil {
		return xerror.Wrapf(
			"set has_draft=true for pipeline %s: %w",
			pipeline.Metadata.Slug,
			err,
		)
	}

	return nil
}

func (s *pipelineStore) DeleteDraft(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return deleteDraft(s, ctx, tx, orgSlug, projectSlug, pipelineSlug, memberSlug)
	})
}

func deleteDraft(
	s *pipelineStore,
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) error {
	if err := s.repo.DeleteDraft(ctx, tx, orgSlug, projectSlug, pipelineSlug, memberSlug); err != nil {
		if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return ErrPipelineDraftNotFound.With(err)
		}
		return xerror.Wrapf("delete pipeline draft %s: %w", pipelineSlug, err)
	}

	hasAnyDraft := false
	if _, err := s.repo.FindAnyDraft(ctx, tx, orgSlug, projectSlug, pipelineSlug); err != nil {
		if !xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return xerror.Wrapf("find remaining drafts for pipeline %s: %w", pipelineSlug, err)
		}
	} else {
		hasAnyDraft = true
	}

	if err := s.repo.SetHasDraft(ctx, tx, orgSlug, projectSlug, pipelineSlug, hasAnyDraft); err != nil {
		if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return ErrPipelineNotFound.With(err)
		}
		return xerror.Wrapf("set has_draft for pipeline %s: %w", pipelineSlug, err)
	}

	return nil
}

func (s *pipelineStore) PrepareGitDraftPublish(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	fallbackBaseRef *string,
	expectedSpecHash *string,
	force bool,
) (*GitDraftPublishRequest, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*GitDraftPublishRequest, error) {
		return prepareGitDraftPublish(s, ctx, tx, orgSlug, projectSlug, pipelineSlug, memberSlug,
			fallbackBaseRef, expectedSpecHash, force)
	})
}

func prepareGitDraftPublish(
	s *pipelineStore,
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	fallbackBaseRef *string,
	expectedSpecHash *string,
	force bool,
) (*GitDraftPublishRequest, error) {
	existingSpec, err := s.repo.FindSpecForUpdate(ctx, tx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return nil, ErrPipelineNotFound.With(err)
		}
		return nil, xerror.Wrapf("find pipeline spec %s before queue publish: %w", pipelineSlug, err)
	}

	if existingSpec.SourcePath == nil || strings.TrimSpace(*existingSpec.SourcePath) == "" {
		return nil, ErrPipelineGitSourceRequired.Detailf(
			"Git-authored pipeline %s has no source path",
			pipelineSlug,
		)
	}

	draft, err := s.repo.FindDraft(ctx, tx, orgSlug, projectSlug, pipelineSlug, memberSlug)
	if err != nil {
		if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return nil, ErrPipelineDraftNotFound.With(err)
		}
		return nil, xerror.Wrapf("find pipeline draft %s before queue publish: %w", pipelineSlug, err)
	}

	effectiveVersionID := draft.BasedOnVersionID
	effectiveSpecHash := draft.BasedOnSpecHash
	if expectedSpecHash != nil {
		effectiveSpecHash = expectedSpecHash
		if pipelineSpecHashesMatch(existingSpec.CurrentVersionSpecHash, expectedSpecHash) {
			effectiveVersionID = existingSpec.CurrentVersionID
		}
	}

	if expectedSpecHash != nil &&
		!force &&
		!pipelineSpecHashesMatch(existingSpec.CurrentVersionSpecHash, expectedSpecHash) {
		return nil, &PipelineDraftConflictError{
			PipelineSlug:          pipelineSlug,
			ExpectedSpecHash:      expectedSpecHash,
			CurrentSpecHash:       existingSpec.CurrentVersionSpecHash,
			DraftBasedOnSpecHash:  draft.BasedOnSpecHash,
			DraftBasedOnVersionID: draft.BasedOnVersionID,
			DraftPreserved:        true,
			CanOverwrite:          true,
		}
	}
	if !force && !pipelineVersionIDsMatch(existingSpec.CurrentVersionID, effectiveVersionID) {
		return nil, &PipelineDraftConflictError{
			PipelineSlug:          pipelineSlug,
			ExpectedSpecHash:      effectiveSpecHash,
			CurrentSpecHash:       existingSpec.CurrentVersionSpecHash,
			DraftBasedOnSpecHash:  draft.BasedOnSpecHash,
			DraftBasedOnVersionID: draft.BasedOnVersionID,
			DraftPreserved:        true,
			CanOverwrite:          true,
		}
	}

	pipeline, err := pipelinevalidate.Load(pipelineSlug.String()+".pipeline.json", draft.SpecJSONB)
	if err != nil {
		return nil, xerror.Wrapf("validate pipeline draft %s before queue publish: %w", pipelineSlug, err)
	}
	if pipeline.Metadata.Slug != pipelineSlug {
		return nil, xerror.InvalidInput(xerror.Pipeline).Detailf(
			"draft slug mismatch: %s != %s",
			pipeline.Metadata.Slug,
			pipelineSlug,
		)
	}

	content, err := renderPipelineSource(strings.TrimSpace(*existingSpec.SourcePath), pipeline)
	if err != nil {
		return nil, xerror.Wrapf("render pipeline draft %s before queue publish: %w", pipelineSlug, err)
	}

	baseRef := firstNonEmptyStringPtr(draft.BaseRef, existingSpec.SourceRef, fallbackBaseRef)
	if baseRef == nil {
		return nil, ErrPipelineGitSourceRequired.Detailf(
			"Git-authored pipeline %s has no base ref for publish continuity",
			pipelineSlug,
		)
	}
	baseCommitSHA := firstNonEmptyStringPtr(draft.BaseCommitSHA, existingSpec.LastAppliedCommit)
	draftSpecHash := fmt.Sprintf("%x", sha256.Sum256(draft.SpecJSONB))
	publishAttemptID := uuid.New().String()
	if draft.DraftState != nil && *draft.DraftState == pipelineDraftStatePublishQueued &&
		draft.PublishAttemptID != nil && strings.TrimSpace(*draft.PublishAttemptID) != "" {
		publishAttemptID = strings.TrimSpace(*draft.PublishAttemptID)
	}
	// A new draft must not collide with a previous merged proposal's retained branch.
	proposalBranch := draft.BranchName
	if draft.DraftState != nil && *draft.DraftState == pipelineDraftStatePublishFailed &&
		(draft.BranchHeadSHA == nil || strings.TrimSpace(*draft.BranchHeadSHA) == "") {
		proposalBranch = nil
	}
	branchName := firstNonEmptyStringValue(
		proposalBranch,
		proposalBranchName(projectSlug, pipelineSlug, memberSlug)+"-"+publishAttemptID,
	)
	authority, err := s.resolveProposalAuthority(ctx, tx, orgSlug, projectSlug)
	if err != nil {
		return nil, err
	}
	if draft.ProposalAuthorityID != nil && *draft.ProposalAuthorityID != authority.ID {
		return nil, ErrPipelineProposalAuthorityDenied.Detail(
			"provider authority changed; restart the draft before publishing with a different identity",
		)
	}
	boundAuthority := proposalAuthorityFromRepository(authority)

	if err := s.repo.QueueDraftPublish(ctx, tx, orgSlug, projectSlug, pipelinesrepo.QueueDraftPublishParams{
		PipelineSlug:     pipelineSlug,
		MemberSlug:       memberSlug,
		DraftState:       pipelineDraftStatePublishQueued,
		BaseRef:          baseRef,
		BaseCommitSHA:    baseCommitSHA,
		BranchName:       branchName,
		PublishAttemptID: publishAttemptID,
		Authority:        *authority,
	}); err != nil {
		return nil, xerror.Wrapf("queue pipeline draft publish state %s: %w", pipelineSlug, err)
	}

	return &GitDraftPublishRequest{
		ProjectSlug:      projectSlug,
		PipelineSlug:     pipelineSlug,
		MemberSlug:       memberSlug,
		BaseRef:          baseRef,
		BaseCommitSHA:    baseCommitSHA,
		ExpectedHeadSHA:  draft.BranchHeadSHA,
		BranchName:       branchName,
		SourcePath:       strings.TrimSpace(*existingSpec.SourcePath),
		Content:          content,
		CommitMessage:    "propose pipeline " + pipelineSlug.String(),
		ProposalTitle:    "Propose pipeline " + pipelineSlug.String(),
		ProposalBody:     "Pipeline draft proposed by Braidplane.",
		DraftSpecHash:    draftSpecHash,
		PublishAttemptID: publishAttemptID,
		Authority:        boundAuthority,
	}, nil
}

func (s *pipelineStore) CompleteProposal(
	ctx context.Context,
	completed pipelinegit.ProposalCompletion,
) error {
	result := pipelinegit.ProposalResult{Provider: completed.Provider, ProposalID: completed.ProposalID, ProposalURL: completed.ProposalURL, ProposalState: completed.ProposalState, BranchName: completed.BranchName, BranchHeadSHA: completed.BranchHeadSHA}
	if err := result.ValidatePublication(completed.Authority, completed.BranchName); err != nil {
		return err
	}

	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return s.completeGitDraftPublish(
			ctx,
			tx,
			completed.OrganizationSlug,
			completed.ProjectSlug,
			completed.PipelineSlug,
			completed.MemberSlug,
			GitProposalMetadata{
				Provider:         completed.Provider,
				ProposalID:       completed.ProposalID,
				ProposalURL:      completed.ProposalURL,
				ProposalState:    completed.ProposalState,
				BranchName:       completed.BranchName,
				BranchHeadSHA:    completed.BranchHeadSHA,
				DraftSpecHash:    completed.DraftSpecHash,
				PublishAttemptID: completed.PublishAttemptID,
			},
		)
	})
}

func (s *pipelineStore) PrepareGitProposalRefresh(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) (*GitProposalRefreshRequest, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*GitProposalRefreshRequest, error) {
		return prepareGitProposalRefresh(s, ctx, tx, orgSlug, projectSlug, pipelineSlug, memberSlug)
	})
}

func prepareGitProposalRefresh(
	s *pipelineStore,
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) (*GitProposalRefreshRequest, error) {
	draft, err := s.repo.FindDraft(ctx, tx, orgSlug, projectSlug, pipelineSlug, memberSlug)
	if err != nil {
		if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return nil, ErrPipelineDraftNotFound.With(err)
		}
		return nil, xerror.Wrapf("find pipeline draft %s before proposal refresh: %w", pipelineSlug, err)
	}
	if draft.ProposalID == nil || strings.TrimSpace(*draft.ProposalID) == "" {
		return nil, xerror.InvalidInput(xerror.Pipeline).Detail("pipeline draft has no proposal to refresh")
	}
	refreshAttemptID := uuid.New().String()
	if err := s.repo.QueueDraftProposalRefresh(ctx, tx, orgSlug, projectSlug, pipelinesrepo.QueueDraftProposalRefreshParams{
		PipelineSlug: pipelineSlug, MemberSlug: memberSlug,
		ProposalID: strings.TrimSpace(*draft.ProposalID), RefreshAttemptID: refreshAttemptID,
	}); err != nil {
		if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return nil, ErrPipelineDraftNotFound.With(err)
		}
		return nil, xerror.Wrapf("queue pipeline draft proposal refresh %s: %w", pipelineSlug, err)
	}
	return &GitProposalRefreshRequest{
		ProjectSlug:      projectSlug,
		PipelineSlug:     pipelineSlug,
		MemberSlug:       memberSlug,
		ProposalID:       strings.TrimSpace(*draft.ProposalID),
		RefreshAttemptID: refreshAttemptID,
	}, nil
}

func (s *pipelineStore) CompleteProposalRefresh(
	ctx context.Context,
	completed pipelinegit.ProposalRefreshCompletion,
) error {
	draftState := pipelineDraftStateProposalOpen
	if completed.ProposalState == "closed" || completed.ProposalState == "merged" {
		draftState = pipelineDraftStateProposalClosed
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.repo.RefreshDraftProposal(ctx, tx, completed.OrganizationSlug, completed.ProjectSlug,
			pipelinesrepo.RefreshDraftProposalParams{
				PipelineSlug:     completed.PipelineSlug,
				MemberSlug:       completed.MemberSlug,
				DraftState:       draftState,
				BranchName:       ptrString(completed.BranchName),
				BranchHeadSHA:    ptrString(completed.BranchHeadSHA),
				ProposalProvider: ptrString(completed.Provider),
				ProposalID:       ptrString(completed.ProposalID),
				ProposalURL:      ptrString(completed.ProposalURL),
				ProposalState:    ptrString(completed.ProposalState),
				RefreshAttemptID: completed.RefreshAttemptID,
			}); err != nil {
			if xerror.Is(err, pipelinesrepo.ErrPipelineDraftAttemptStale) {
				return nil
			}
			if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
				return ErrPipelineDraftNotFound.With(err)
			}
			return xerror.Wrapf("refresh pipeline draft proposal %s: %w", completed.PipelineSlug, err)
		}
		return nil
	})
}

func (s *pipelineStore) FailProposalRefresh(
	ctx context.Context,
	failed pipelinegit.ProposalRefreshFailure,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		err := s.repo.FailDraftProposalRefresh(ctx, tx, failed.OrganizationSlug, failed.ProjectSlug,
			pipelinesrepo.FailDraftProposalRefreshParams{
				PipelineSlug: failed.PipelineSlug, MemberSlug: failed.MemberSlug,
				ProposalID: failed.ProposalID, RefreshAttemptID: failed.RefreshAttemptID,
				FailureCode: failed.FailureCode, FailureMessage: failed.FailureMessage,
			})
		if xerror.Is(err, pipelinesrepo.ErrPipelineDraftAttemptStale) {
			return nil
		}
		if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return ErrPipelineDraftNotFound.With(err)
		}
		if err != nil {
			return xerror.Wrapf("fail pipeline draft proposal refresh %s: %w", failed.PipelineSlug, err)
		}
		return nil
	})
}

func (s *pipelineStore) FailProposal(
	ctx context.Context,
	failed pipelinegit.ProposalFailure,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.repo.FailDraftPublish(
			ctx,
			tx,
			failed.OrganizationSlug,
			failed.ProjectSlug,
			pipelinesrepo.FailDraftPublishParams{
				PipelineSlug:     failed.PipelineSlug,
				MemberSlug:       failed.MemberSlug,
				DraftState:       pipelineDraftStatePublishFailed,
				PublishAttemptID: failed.PublishAttemptID,
				FailureCode:      failed.FailureCode,
				FailureMessage:   failed.FailureMessage,
			},
		); err != nil {
			if xerror.Is(err, pipelinesrepo.ErrPipelineDraftAttemptStale) {
				return nil
			}
			if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
				return ErrPipelineDraftNotFound.With(err)
			}
			return xerror.Wrapf("fail pipeline draft publish %s: %w", failed.PipelineSlug, err)
		}
		return nil
	})
}

func pipelineVersionIDsMatch(currentVersionID, expectedVersionID *int64) bool {
	if currentVersionID == nil || expectedVersionID == nil {
		return currentVersionID == nil && expectedVersionID == nil
	}

	return *currentVersionID == *expectedVersionID
}

func pipelineSpecHashesMatch(currentSpecHash, expectedSpecHash *string) bool {
	if currentSpecHash == nil || expectedSpecHash == nil {
		return currentSpecHash == nil && expectedSpecHash == nil
	}

	return *currentSpecHash == *expectedSpecHash
}

func formatOptionalSpecHash(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}

const (
	pipelineDraftStateEditing        = "editing"
	pipelineDraftStatePublishQueued  = "publish_queued"
	pipelineDraftStatePublishFailed  = "publish_failed"
	pipelineDraftStateProposalOpen   = "proposal_open"
	pipelineDraftStateProposalClosed = "proposal_closed"
)

func firstNonEmptyStringPtr(values ...*string) *string {
	for _, value := range values {
		if value == nil {
			continue
		}
		trimmed := strings.TrimSpace(*value)
		if trimmed == "" {
			continue
		}
		return &trimmed
	}

	return nil
}

func firstNonEmptyStringValue(existing *string, fallback string) string {
	if existing != nil {
		if trimmed := strings.TrimSpace(*existing); trimmed != "" {
			return trimmed
		}
	}

	return strings.TrimSpace(fallback)
}

func proposalBranchName(projectSlug, pipelineSlug, memberSlug slug.Slug) string {
	return strings.Join(
		[]string{
			"braidplane",
			projectSlug.String(),
			pipelineSlug.String(),
			memberSlug.String(),
		},
		"/",
	)
}

func (s *pipelineStore) List(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	req model.ListRequest,
) ([]*pipelinesrepo.PipelineSpec, error) {
	return s.repo.ListSpecs(ctx, s.dbtx, orgSlug, projectSlug, req)
}

func (s *pipelineStore) Exists(ctx context.Context, orgSlug, projectSlug, pipelineSlug slug.Slug) (bool, error) {
	_, err := s.repo.FindSpec(ctx, s.dbtx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *pipelineStore) ListVersions(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) ([]*pipelinesrepo.PipelineVersion, error) {
	return s.repo.ListVersions(ctx, s.dbtx, orgSlug, projectSlug, pipelineSlug)
}

func (s *pipelineStore) FindVersionBySpecHash(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	specHash string,
) (*pipelinesrepo.PipelineVersion, error) {
	return s.repo.FindVersionBySpecHash(ctx, s.dbtx, orgSlug, projectSlug, pipelineSlug, specHash)
}

func (s *pipelineStore) FindVersionBySpecHashPrefix(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	specHashPrefix string,
) (*pipelinesrepo.PipelineVersion, error) {
	return s.repo.FindVersionBySpecHashPrefix(
		ctx,
		s.dbtx,
		orgSlug,
		projectSlug,
		pipelineSlug,
		specHashPrefix,
	)
}

func (s *pipelineStore) Deploy(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params pipelinesrepo.DeployParams,
) (*pipelinesrepo.PipelineDeployment, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*pipelinesrepo.PipelineDeployment, error) {
		deployment, err := s.repo.Deploy(ctx, tx, orgSlug, projectSlug, environmentSlug, params)
		if xerror.Is(err, pipelinesrepo.ErrPipelineDeploymentNotFound) {
			return nil, ErrPipelineDeploymentNotFound.With(err)
		}
		return deployment, err
	})
}

func (s *pipelineStore) FindDeployment(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
) (*pipelinesrepo.PipelineDeployment, error) {
	deployment, err := s.repo.FindDeployment(
		ctx,
		s.dbtx,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
	)
	if xerror.Is(err, pipelinesrepo.ErrPipelineDeploymentNotFound) {
		return nil, ErrPipelineDeploymentNotFound.With(err)
	}
	return deployment, err
}

func (s *pipelineStore) ListDeployments(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) ([]*pipelinesrepo.PipelineDeployment, error) {
	return s.repo.ListDeployments(ctx, s.dbtx, orgSlug, projectSlug, environmentSlug)
}

func (s *pipelineStore) ProjectEnvironmentEnabled(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (bool, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (bool, error) {
		return s.repo.ProjectEnvironmentEnabled(ctx, tx, orgSlug, projectSlug, environmentSlug)
	})
}

func (s *pipelineStore) ProjectEnvironmentProtection(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (*model.ProjectEnvironmentProtection, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*model.ProjectEnvironmentProtection, error) {
		protection, err := s.repo.FindProjectEnvironmentProtection(ctx, tx, orgSlug, projectSlug, environmentSlug)
		if err != nil && xerror.Is(err, xerror.NotFound(xerror.ProjectEnvironment)) {
			return nil, ErrProjectEnvironmentNotFound.With(err)
		}
		return protection, err
	})
}

func (s *pipelineStore) ReconcileFromGit(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	repoStore repostore.RepoStore,
	params ReconcileFromGitParams,
) (*ReconcileFromGitResult, error) {
	if repoStore == nil {
		return nil, xerror.Unexpected().Detail("reconcile from git requires repo store")
	}
	if !params.DryRun {
		if firstNonEmptyStringPtr(params.SourceRef) == nil {
			return nil, ErrPipelineGitSourceRequired.Detail("pipeline source ref is required for reconciliation")
		}
		if firstNonEmptyStringPtr(params.LastAppliedCommit) == nil {
			return nil, ErrPipelineGitSourceRequired.Detail("immutable pipeline source commit is required for reconciliation")
		}
	}

	dir := params.PipelineDir
	if strings.TrimSpace(dir) == "" {
		dir = "pipelines"
	}

	entries, err := repoStore.ListFiles(ctx, dir)
	if err != nil {
		return nil, xerror.Wrapf("list git pipeline files: %w", err)
	}

	result := &ReconcileFromGitResult{}
	pipelineFilter := reconcilePipelineFilter(params.PipelineSlugs)

	// Phase 1: read and parse files outside the transaction so that file-level
	// failures (read, parse, marshal) are collected without touching the DB.
	type parsedPipeline struct {
		path       string
		pipeline   *pipelinespec.Pipeline
		specJSON   []byte
		hash       string
		sourcePath string
		pins       []byte
	}

	var toProcess []parsedPipeline

	for _, entry := range entries {
		if !isPipelineSpecFile(entry.Path) {
			continue
		}

		result.Scanned++
		pipelinePath := entry.Path
		pipelineSlug := ""

		data, err := repoStore.ReadFile(ctx, entry.Path, maxReconcilePipelineSpecBytes)
		if err != nil {
			result.addFailure(pipelinePath, pipelineSlug, "read", err)
			continue
		}

		pipeline, err := pipelinevalidate.Load(entry.Path, data)
		if err != nil {
			result.addFailure(pipelinePath, pipelineSlug, "load", err)
			continue
		}
		pipelineSlug = pipeline.Metadata.Slug.String()

		if len(pipelineFilter) > 0 {
			if _, ok := pipelineFilter[pipeline.Metadata.Slug]; !ok {
				sourcePath := entry.Path
				result.addSkipped(
					pipeline.Metadata.Slug.String(),
					&sourcePath,
					ReconcileFromGitSkippedReasonFilteredOut,
				)
				continue
			}
		}

		resolved, pins, resolveErr := s.resolvePipelineContracts(ctx, orgSlug, projectSlug, pipeline, true)
		if resolveErr == nil {
			resolveErr = pipelinevalidate.Validate(resolved)
		}
		if resolveErr == nil {
			resolveErr = pipelinevalidate.ValidateStepInputCompatibility(resolved)
		}
		if resolveErr != nil {
			result.addFailure(pipelinePath, pipelineSlug, "contract", resolveErr)
			continue
		}
		pinJSON, err := json.Marshal(pins)
		if err != nil {
			result.addFailure(pipelinePath, pipelineSlug, "marshal", err)
			continue
		}

		specJSON, err := json.Marshal(pipeline)
		if err != nil {
			result.addFailure(pipelinePath, pipelineSlug, "marshal", err)
			continue
		}

		hash := fmt.Sprintf("%x", sha256.Sum256(specJSON))
		toProcess = append(toProcess, parsedPipeline{
			path:       entry.Path,
			pipeline:   pipeline,
			specJSON:   specJSON,
			hash:       hash,
			sourcePath: entry.Path,
			pins:       pinJSON,
		})
	}

	// Phase 2: all DB writes in a single atomic transaction.
	// Any DB error aborts the whole reconcile and rolls back all changes.
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		discovered := make(map[slug.Slug]struct{}, len(toProcess))

		for _, p := range toProcess {
			discovered[p.pipeline.Metadata.Slug] = struct{}{}

			findSpec := s.repo.FindSpecForUpdate
			if params.DryRun {
				findSpec = s.repo.FindSpec
			}

			existingSpec, findErr := findSpec(ctx, tx, orgSlug, projectSlug, p.pipeline.Metadata.Slug)
			if findErr != nil &&
				!xerror.Is(findErr, pipelinesrepo.ErrPipelineReferenceNotFound) {
				return xerror.Wrapf(
					"find spec %s before git reconcile: %w",
					p.pipeline.Metadata.Slug,
					findErr,
				)
			}
			if xerror.Is(findErr, pipelinesrepo.ErrPipelineReferenceNotFound) {
				_, deleted, deletedErr := s.repo.FindSpecDeletedAtForUpdate(
					ctx,
					tx,
					orgSlug,
					projectSlug,
					p.pipeline.Metadata.Slug,
				)
				if deletedErr != nil && !xerror.Is(deletedErr, pipelinesrepo.ErrPipelineReferenceNotFound) {
					return xerror.Wrapf(
						"find deleted spec %s before git reconcile: %w",
						p.pipeline.Metadata.Slug,
						deletedErr,
					)
				}
				if deleted && !params.DryRun {
					if restoreErr := s.repo.RestoreSpec(
						ctx,
						tx,
						orgSlug,
						projectSlug,
						p.pipeline.Metadata.Slug,
					); restoreErr != nil {
						return xerror.Wrapf(
							"rematerialize pipeline %s from authoritative Git source: %w",
							p.pipeline.Metadata.Slug,
							restoreErr,
						)
					}
				}
			}

			if reconcileResetDriftedSpec(existingSpec, p.hash) {
				result.ResetDrifted++
				result.ResetDriftedPipelines = append(
					result.ResetDriftedPipelines,
					ReconcileFromGitReset{
						PipelineSlug:     p.pipeline.Metadata.Slug.String(),
						SourcePath:       p.sourcePath,
						PreviousSpecHash: existingSpec.SpecHash,
						AppliedSpecHash:  p.hash,
					},
				)
			}

			result.addSynced(p.pipeline.Metadata.Slug.String(), p.sourcePath, p.hash)

			if params.DryRun {
				continue
			}

			_, upsertErr := s.repo.UpsertSpec(ctx, tx, orgSlug, projectSlug, pipelinesrepo.UpsertSpecParams{
				Slug:              p.pipeline.Metadata.Slug,
				Name:              p.pipeline.Metadata.Name,
				SpecJSONB:         p.specJSON,
				SpecHash:          p.hash,
				SyncStatus:        pipelinesrepo.SyncStatusSynced,
				SourcePath:        &p.sourcePath,
				SourceRef:         params.SourceRef,
				LastAppliedCommit: params.LastAppliedCommit,
			})
			if upsertErr != nil {
				return xerror.Wrapf("upsert spec %s: %w", p.pipeline.Metadata.Slug, upsertErr)
			}

			_, upsertErr = s.repo.Upsert(ctx, tx, orgSlug, projectSlug, pipelinesrepo.UpsertParams{
				Slug: p.pipeline.Metadata.Slug,
				Name: p.pipeline.Metadata.Name,
			})
			if upsertErr != nil {
				return xerror.Wrapf("upsert reference %s: %w", p.pipeline.Metadata.Slug, upsertErr)
			}

			versionID, versionErr := s.ensureCurrentVersionForGitPipeline(
				ctx,
				tx,
				orgSlug,
				projectSlug,
				p.pipeline.Metadata.Slug,
				p.specJSON,
				p.hash,
				p.pins,
				p.sourcePath,
				params.LastAppliedCommit,
			)
			if versionErr != nil {
				return xerror.Wrapf(
					"set current version %s: %w",
					p.pipeline.Metadata.Slug,
					versionErr,
				)
			}

			if versionID <= 0 {
				return xerror.Unexpected().Detailf(
					"pipeline version ID must be positive for %s",
					p.pipeline.Metadata.Slug,
				)
			}

			result.Upserted++

		}

		existing, listErr := s.repo.ListSpecs(ctx, tx, orgSlug, projectSlug, model.ListRequest{})
		if listErr != nil {
			return xerror.Wrapf("list existing pipeline specs for reconcile: %w", listErr)
		}
		if result.Failed > 0 {
			return nil
		}

		for _, spec := range existing {
			if _, ok := discovered[spec.Slug]; ok {
				continue
			}

			if len(pipelineFilter) > 0 {
				continue
			}

			result.addDeleted(spec)

			if params.DryRun {
				continue
			}

			slog.Info("pipeline orphan removed",
				slog.String("org_slug", orgSlug.String()),
				slog.String("pipeline_slug", spec.Slug.String()),
			)

			if delErr := s.repo.DeleteSpec(ctx, tx, orgSlug, projectSlug, spec.Slug, ""); delErr != nil {
				if !xerror.Is(delErr, pipelinesrepo.ErrPipelineReferenceNotFound) {
					return xerror.Wrapf("delete spec %s: %w", spec.Slug, delErr)
				}
			}

			if delErr := s.repo.Delete(ctx, tx, orgSlug, projectSlug, spec.Slug); delErr != nil {
				if !xerror.Is(delErr, pipelinesrepo.ErrPipelineReferenceNotFound) {
					return xerror.Wrapf("delete reference %s: %w", spec.Slug, delErr)
				}
			}
		}

		return nil
	})
	if err != nil {
		result.addFailure("", "", "transaction", err)
		return result, err
	}

	return result, nil
}

func (s *pipelineStore) resolveProposalAuthority(
	ctx context.Context,
	dbtx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
) (*pipelinesrepo.ProposalAuthority, error) {
	authority, err := s.repo.FindProposalAuthority(ctx, dbtx, orgSlug, projectSlug)
	if err != nil {
		if xerror.Is(err, pipelinesrepo.ErrPipelineProposalAuthorityNotFound) {
			return nil, ErrPipelineProposalAuthorityDenied.With(err).Detail("save the project repository settings to enable plain Git proposals, or connect an active repository-scoped provider identity")
		}
		return nil, xerror.Wrapf("resolve pipeline proposal authority: %w", err)
	}
	if authority.PublicationMode != model.PipelineProposalModeDelegatedUser &&
		authority.PublicationMode != model.PipelineProposalModeOrganizationService &&
		authority.PublicationMode != model.PipelineProposalModeRepositoryCredential {
		return nil, ErrPipelineProposalAuthorityDenied.Detail(
			"unsupported publication mode",
		)
	}
	bound := proposalAuthorityFromRepository(authority)
	if bound.ValidateForPersistence() != nil {
		return nil, ErrPipelineProposalAuthorityDenied.Detail(
			"provider executing principal display is unavailable or unsafe",
		)
	}
	return authority, nil
}

func reconcileResetDriftedSpec(existingSpec *pipelinesrepo.PipelineSpec, appliedHash string) bool {
	if existingSpec == nil {
		return false
	}
	if existingSpec.SyncStatus != pipelinesrepo.SyncStatusDrifted {
		return false
	}
	return existingSpec.SpecHash != appliedHash
}

func reconcilePipelineFilter(pipelineSlugs []slug.Slug) map[slug.Slug]struct{} {
	if len(pipelineSlugs) == 0 {
		return nil
	}

	filter := make(map[slug.Slug]struct{}, len(pipelineSlugs))
	for _, pipelineSlug := range pipelineSlugs {
		filter[pipelineSlug] = struct{}{}
	}
	return filter
}

func (r *ReconcileFromGitResult) addSynced(pipelineSlug, sourcePath, specHash string) {
	r.Synced++
	r.SyncedPipelines = append(r.SyncedPipelines, ReconcileFromGitSynced{
		PipelineSlug: pipelineSlug,
		SourcePath:   sourcePath,
		SpecHash:     specHash,
	})
}

func (r *ReconcileFromGitResult) addDeleted(spec *pipelinesrepo.PipelineSpec) {
	r.Deleted++
	r.DeletedPipelines = append(r.DeletedPipelines, ReconcileFromGitDeleted{
		PipelineSlug:       spec.Slug.String(),
		PreviousSourcePath: spec.SourcePath,
	})
}

func (r *ReconcileFromGitResult) addSkipped(
	pipelineSlug string,
	sourcePath *string,
	reason string,
) {
	r.Skipped++
	r.SkippedPipelines = append(r.SkippedPipelines, ReconcileFromGitSkipped{
		PipelineSlug: pipelineSlug,
		SourcePath:   sourcePath,
		Reason:       reason,
	})
}

func (r *ReconcileFromGitResult) addFailure(
	pipelinePath, pipelineSlug, stage string,
	err error,
) {
	r.Failed++
	r.Failures = append(r.Failures, ReconcileFromGitFailure{
		PipelinePath: pipelinePath,
		PipelineSlug: pipelineSlug,
		Stage:        stage,
		Message:      err.Error(),
	})
}

func isPipelineSpecFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml", ".json":
		return true
	default:
		return false
	}
}

func (s *pipelineStore) completeGitDraftPublish(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	metadata GitProposalMetadata,
) error {
	draftState := pipelineDraftStateProposalOpen
	if metadata.Provider == "git" {
		draftState = "branch_pushed"
	}
	if err := s.repo.CompleteDraftPublish(ctx, tx, orgSlug, projectSlug, pipelinesrepo.CompleteDraftPublishParams{
		PipelineSlug:          pipelineSlug,
		MemberSlug:            memberSlug,
		DraftState:            draftState,
		BranchName:            metadata.BranchName,
		BranchHeadSHA:         metadata.BranchHeadSHA,
		ProposalProvider:      metadata.Provider,
		ProposalID:            metadata.ProposalID,
		ProposalURL:           metadata.ProposalURL,
		ProposalState:         metadata.ProposalState,
		LastProposedDraftHash: metadata.DraftSpecHash,
		PublishAttemptID:      metadata.PublishAttemptID,
	}); err != nil {
		if xerror.Is(err, pipelinesrepo.ErrPipelineDraftAttemptStale) {
			return nil
		}
		if xerror.Is(err, pipelinesrepo.ErrPipelineReferenceNotFound) {
			return ErrPipelineDraftNotFound.With(err)
		}
		return xerror.Wrapf("complete pipeline draft publish %s: %w", pipelineSlug, err)
	}

	return nil
}

func (s *pipelineStore) ensureCurrentVersionForGitPipeline(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	specJSON []byte,
	specHash string,
	pinJSON []byte,
	sourcePath string,
	commit *string,
) (int64, error) {
	var version *pipelinesrepo.PipelineVersion
	var err error
	var normalizedCommit *string

	version, err = s.repo.FindVersionBySpecHash(ctx, tx, orgSlug, projectSlug, pipelineSlug, specHash)
	if err != nil && !xerror.Is(err, pipelinesrepo.ErrPipelineVersionNotFound) {
		return 0, err
	}

	if commit != nil {
		trimmedCommit := strings.TrimSpace(*commit)
		if trimmedCommit != "" {
			normalizedCommit = &trimmedCommit
		}
	}

	if version == nil && normalizedCommit != nil {
		version, err = s.repo.FindVersionByCommitSHA(
			ctx,
			tx,
			orgSlug,
			projectSlug,
			pipelineSlug,
			*normalizedCommit,
		)
		if err != nil && !xerror.Is(err, pipelinesrepo.ErrPipelineVersionNotFound) {
			return 0, err
		}
	}

	if version == nil {
		version, err = s.repo.CreateVersion(ctx, tx, orgSlug, projectSlug, pipelinesrepo.CreateVersionParams{
			SourcePath:        &sourcePath,
			PipelineSlug:      pipelineSlug,
			GitCommitSHA:      normalizedCommit,
			SpecJSONB:         specJSON,
			SpecHash:          specHash,
			ResolvedContracts: pinJSON,
		})
		if err != nil {
			return 0, err
		}
	}

	if err := s.repo.SetCurrentVersion(ctx, tx, orgSlug, projectSlug, pipelineSlug, version.ID); err != nil {
		return 0, err
	}

	return version.ID, nil
}
