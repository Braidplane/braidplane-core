package pipelines

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

// Repository provides pipeline reference and canonical spec persistence operations.
type Repository interface {
	Upsert(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
		params UpsertParams,
	) (*PipelineReference, error)
	FindBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) (*PipelineReference, error)
	List(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug slug.Slug) ([]*PipelineReference, error)
	Delete(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, pipelineSlug slug.Slug) error
	SetHasDraft(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		hasDraft bool,
	) error

	UpsertDraft(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
		memberSlug slug.Slug,
		params UpsertDraftParams,
	) (*PipelineDraft, error)
	FindDraft(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	) (*PipelineDraft, error)
	FindAnyDraft(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) (*PipelineDraft, error)
	FindProposalAuthority(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
	) (*ProposalAuthority, error)
	QueueDraftPublish(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
		params QueueDraftPublishParams,
	) error
	CompleteDraftPublish(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
		params CompleteDraftPublishParams,
	) error
	FailDraftPublish(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
		params FailDraftPublishParams,
	) error
	QueueDraftProposalRefresh(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
		params QueueDraftProposalRefreshParams,
	) error
	RefreshDraftProposal(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
		params RefreshDraftProposalParams,
	) error
	FailDraftProposalRefresh(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
		params FailDraftProposalRefreshParams,
	) error
	DeleteDraft(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	) error

	// Canonical spec storage (P9-1).
	UpsertSpec(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
		params UpsertSpecParams,
	) (*PipelineSpec, error)
	FindSpec(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) (*PipelineSpec, error)
	FindSpecForUpdate(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) (*PipelineSpec, error)
	ListSpecs(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
		req model.ListRequest,
	) ([]*PipelineSpec, error)
	DeleteSpec(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, pipelineSlug, deletedBy slug.Slug) error
	RestoreSpec(ctx context.Context, tx postgres.DBTX, orgSlug, projectSlug, pipelineSlug slug.Slug) error
	// FindSpecDeletedAtForUpdate exposes source-removal tombstone state to reconciliation.
	FindSpecDeletedAtForUpdate(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) (time.Time, bool, error)
	CreateVersion(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug slug.Slug,
		params CreateVersionParams,
	) (*PipelineVersion, error)
	FindVersionBySpecHash(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		specHash string,
	) (*PipelineVersion, error)
	FindVersionBySpecHashPrefix(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		specHashPrefix string,
	) (*PipelineVersion, error)
	FindVersionByCommitSHA(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		commitSHA string,
	) (*PipelineVersion, error)
	FindVersionByID(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		versionID int64,
	) (*PipelineVersion, error)
	ListVersions(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) ([]*PipelineVersion, error)
	SetCurrentVersion(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		versionID int64,
	) error
	Deploy(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		params DeployParams,
	) (*PipelineDeployment, error)
	FindDeployment(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	) (*PipelineDeployment, error)
	ListDeployments(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) ([]*PipelineDeployment, error)
	ProjectEnvironmentEnabled(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) (bool, error)
	FindProjectEnvironmentProtection(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) (*model.ProjectEnvironmentProtection, error)
}

type repository struct{}

// New creates a new pipeline reference repository.
func New() Repository {
	return &repository{}
}

func (r *repository) Upsert(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	params UpsertParams,
) (*PipelineReference, error) {
	queries := dbgen.New(tx)
	row, err := queries.UpsertPipelineReference(ctx, dbgen.UpsertPipelineReferenceParams{
		OrgSlug:     orgSlug,
		ProjectSlug: projectSlug,
		Slug:        params.Slug,
		Name:        params.Name,
	})
	if err != nil {
		return nil, err
	}
	return toReference(row), nil
}

func (r *repository) FindBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (*PipelineReference, error) {
	queries := dbgen.New(tx)
	row, err := queries.FindPipelineReferenceBySlug(ctx, dbgen.FindPipelineReferenceBySlugParams{
		OrgSlug:     orgSlug,
		ProjectSlug: projectSlug,
		Slug:        pipelineSlug,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrPipelineReferenceNotFound.With(err)
		}
		return nil, err
	}
	return toReference(row), nil
}

func (r *repository) List(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
) ([]*PipelineReference, error) {
	queries := dbgen.New(tx)
	rows, err := queries.ListPipelineReferencesByProject(
		ctx,
		dbgen.ListPipelineReferencesByProjectParams{
			OrgSlug:     orgSlug,
			ProjectSlug: projectSlug,
		},
	)
	if err != nil {
		return nil, err
	}
	refs := make([]*PipelineReference, len(rows))
	for i, row := range rows {
		refs[i] = toReference(row)
	}
	return refs, nil
}

func (r *repository) Delete(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) error {
	queries := dbgen.New(tx)
	result, err := queries.DeletePipelineReference(ctx, dbgen.DeletePipelineReferenceParams{
		OrgSlug:     orgSlug,
		ProjectSlug: projectSlug,
		Slug:        pipelineSlug,
	})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPipelineReferenceNotFound
	}
	return nil
}

func (r *repository) SetHasDraft(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	hasDraft bool,
) error {
	queries := dbgen.New(tx)
	result, err := queries.SetPipelineReferenceHasDraft(
		ctx,
		dbgen.SetPipelineReferenceHasDraftParams{
			HasDraft:     hasDraft,
			OrgSlug:      orgSlug,
			ProjectSlug:  projectSlug,
			PipelineSlug: pipelineSlug,
		},
	)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPipelineReferenceNotFound
	}

	return nil
}

func (r *repository) UpsertDraft(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	memberSlug slug.Slug,
	params UpsertDraftParams,
) (*PipelineDraft, error) {
	queries := dbgen.New(tx)
	row, err := queries.UpsertPipelineDraft(ctx, dbgen.UpsertPipelineDraftParams{
		OrgSlug:          orgSlug,
		ProjectSlug:      projectSlug,
		PipelineSlug:     string(params.PipelineSlug),
		MemberSlug:       memberSlug,
		BasedOnVersionID: nullableInt8(params.BasedOnVersionID),
		SpecJsonb:        params.SpecJSONB,
		DraftState:       textOrNull(&params.DraftState),
	})
	if err != nil {
		return nil, err
	}

	return toDraft(row), nil
}

func (r *repository) FindDraft(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) (*PipelineDraft, error) {
	queries := dbgen.New(tx)
	row, err := queries.FindPipelineDraftBySlug(ctx, dbgen.FindPipelineDraftBySlugParams{
		OrgSlug:      orgSlug,
		ProjectSlug:  projectSlug,
		PipelineSlug: string(pipelineSlug),
		MemberSlug:   memberSlug,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrPipelineReferenceNotFound.With(err)
		}
		return nil, err
	}

	return toDraftFromFindRow(row), nil
}

func (r *repository) FindAnyDraft(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (*PipelineDraft, error) {
	queries := dbgen.New(tx)
	row, err := queries.FindAnyPipelineDraftBySlug(ctx, dbgen.FindAnyPipelineDraftBySlugParams{
		OrgSlug:      orgSlug,
		ProjectSlug:  projectSlug,
		PipelineSlug: string(pipelineSlug),
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrPipelineReferenceNotFound.With(err)
		}
		return nil, err
	}

	return toDraftFromAnyRow(row), nil
}

func (r *repository) FindProposalAuthority(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
) (*ProposalAuthority, error) {
	row, err := dbgen.New(tx).FindPipelineProposalAuthority(
		ctx,
		dbgen.FindPipelineProposalAuthorityParams{OrgSlug: orgSlug, ProjectSlug: projectSlug},
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrPipelineProposalAuthorityNotFound.With(err)
		}
		return nil, err
	}
	authority := &ProposalAuthority{
		ID:                        int64(row.ID),
		PublicationMode:           row.PublicationMode,
		ProviderHost:              row.ProviderHost,
		ProviderAccountID:         row.ProviderAccountID,
		ProviderRepositoryID:      row.ProviderRepositoryID,
		ProviderCredentialID:      row.ProviderCredentialID,
		ExecutingPrincipalID:      row.ExecutingPrincipalID,
		ExecutingPrincipalDisplay: row.ExecutingPrincipalDisplay,
		AuthoritySource:           row.AuthoritySource,
	}
	if authority.PublicationMode == model.PipelineProposalModeRepositoryCredential {
		authority.GitConfigurationHash = model.GitConfigurationHash(row.GitConfiguration)
		authority.GitRepository = row.GitRepository
		authority.GitBaseRef = "refs/heads/" + row.GitBaseBranch
		authority.GitDataSourceSlug = row.GitDataSourceSlug.String()
		authority.GitCredentialSlug = row.GitCredentialSlug.String()
		if row.GitCredentialUpdatedAt.Valid {
			authority.GitCredentialRevision = row.GitCredentialUpdatedAt.Time.UTC().Format(time.RFC3339Nano)
		}
	}
	return authority, nil
}

func (r *repository) QueueDraftPublish(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	params QueueDraftPublishParams,
) error {
	queries := dbgen.New(tx)
	result, err := queries.QueuePipelineDraftPublish(ctx, dbgen.QueuePipelineDraftPublishParams{
		OrgSlug:                   orgSlug,
		ProjectSlug:               projectSlug,
		PipelineSlug:              string(params.PipelineSlug),
		MemberSlug:                params.MemberSlug,
		DraftState:                textOrNull(&params.DraftState),
		BaseRef:                   textOrNull(params.BaseRef),
		BaseCommitSha:             textOrNull(params.BaseCommitSHA),
		BranchName:                textOrNull(&params.BranchName),
		PublishAttemptID:          textOrNull(&params.PublishAttemptID),
		ProposalAuthorityID:       pgtype.Int8{Int64: params.Authority.ID, Valid: true},
		PublicationMode:           textOrNull(&params.Authority.PublicationMode),
		ProviderHost:              textOrNull(&params.Authority.ProviderHost),
		ProviderAccountID:         textOrNull(&params.Authority.ProviderAccountID),
		ProviderRepositoryID:      textOrNull(&params.Authority.ProviderRepositoryID),
		ProviderCredentialID:      textOrNull(&params.Authority.ProviderCredentialID),
		ExecutingPrincipalID:      textOrNull(&params.Authority.ExecutingPrincipalID),
		ExecutingPrincipalDisplay: textOrNull(&params.Authority.ExecutingPrincipalDisplay),
		AuthoritySource:           textOrNull(&params.Authority.AuthoritySource),
	})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPipelineReferenceNotFound
	}

	return nil
}

func (r *repository) CompleteDraftPublish(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	params CompleteDraftPublishParams,
) error {
	queries := dbgen.New(tx)
	result, err := queries.CompletePipelineDraftPublish(ctx, dbgen.CompletePipelineDraftPublishParams{
		OrgSlug:               textOrNullString(orgSlug.String()),
		ProjectSlug:           textOrNullString(projectSlug.String()),
		PipelineSlug:          textOrNullString(params.PipelineSlug.String()),
		MemberSlug:            textOrNullString(params.MemberSlug.String()),
		DraftState:            textOrNull(&params.DraftState),
		BranchName:            textOrNull(&params.BranchName),
		BranchHeadSha:         textOrNull(&params.BranchHeadSHA),
		ProposalProvider:      textOrNull(&params.ProposalProvider),
		ProposalID:            pgtype.Text{String: params.ProposalID, Valid: params.ProposalID != ""},
		ProposalUrl:           pgtype.Text{String: params.ProposalURL, Valid: params.ProposalURL != ""},
		ProposalState:         pgtype.Text{String: params.ProposalState, Valid: params.ProposalState != ""},
		LastProposedDraftHash: textOrNull(&params.LastProposedDraftHash),
		PublishAttemptID:      textOrNull(&params.PublishAttemptID),
	})
	if err != nil {
		return err
	}
	if !result.DraftExists {
		return ErrPipelineReferenceNotFound
	}
	if !result.Updated {
		return ErrPipelineDraftAttemptStale
	}

	return nil
}

func (r *repository) FailDraftPublish(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	params FailDraftPublishParams,
) error {
	queries := dbgen.New(tx)
	result, err := queries.FailPipelineDraftPublish(ctx, dbgen.FailPipelineDraftPublishParams{
		OrgSlug:          orgSlug,
		ProjectSlug:      projectSlug,
		PipelineSlug:     string(params.PipelineSlug),
		MemberSlug:       params.MemberSlug,
		DraftState:       textOrNull(&params.DraftState),
		PublishAttemptID: textOrNull(&params.PublishAttemptID),
		FailureCode:      textOrNull(&params.FailureCode),
		FailureMessage:   textOrNull(&params.FailureMessage),
	})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPipelineDraftAttemptStale
	}

	return nil
}

func (r *repository) RefreshDraftProposal(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	params RefreshDraftProposalParams,
) error {
	queries := dbgen.New(tx)
	result, err := queries.RefreshPipelineDraftProposal(ctx, dbgen.RefreshPipelineDraftProposalParams{
		OrgSlug:          textOrNullString(orgSlug.String()),
		ProjectSlug:      textOrNullString(projectSlug.String()),
		PipelineSlug:     textOrNullString(params.PipelineSlug.String()),
		MemberSlug:       textOrNullString(params.MemberSlug.String()),
		DraftState:       textOrNull(&params.DraftState),
		BranchName:       textOrNull(params.BranchName),
		BranchHeadSha:    textOrNull(params.BranchHeadSHA),
		ProposalProvider: textOrNull(params.ProposalProvider),
		ProposalID:       textOrNull(params.ProposalID),
		ProposalUrl:      textOrNull(params.ProposalURL),
		ProposalState:    textOrNull(params.ProposalState),
		RefreshAttemptID: textOrNull(&params.RefreshAttemptID),
	})
	if err != nil {
		return err
	}
	if !result.DraftExists {
		return ErrPipelineReferenceNotFound
	}
	if !result.Updated {
		return ErrPipelineDraftAttemptStale
	}
	return nil
}

func (r *repository) QueueDraftProposalRefresh(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	params QueueDraftProposalRefreshParams,
) error {
	queries := dbgen.New(tx)
	result, err := queries.QueuePipelineDraftProposalRefresh(ctx, dbgen.QueuePipelineDraftProposalRefreshParams{
		OrgSlug:          orgSlug,
		ProjectSlug:      projectSlug,
		PipelineSlug:     string(params.PipelineSlug),
		MemberSlug:       params.MemberSlug,
		ProposalID:       textOrNull(&params.ProposalID),
		RefreshAttemptID: textOrNull(&params.RefreshAttemptID),
	})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPipelineReferenceNotFound
	}
	return nil
}

func (r *repository) FailDraftProposalRefresh(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	params FailDraftProposalRefreshParams,
) error {
	queries := dbgen.New(tx)
	result, err := queries.FailPipelineDraftProposalRefresh(ctx, dbgen.FailPipelineDraftProposalRefreshParams{
		OrgSlug:          textOrNullString(orgSlug.String()),
		ProjectSlug:      textOrNullString(projectSlug.String()),
		PipelineSlug:     textOrNullString(params.PipelineSlug.String()),
		MemberSlug:       textOrNullString(params.MemberSlug.String()),
		ProposalID:       textOrNull(&params.ProposalID),
		RefreshAttemptID: textOrNull(&params.RefreshAttemptID),
		FailureCode:      textOrNull(&params.FailureCode),
		FailureMessage:   textOrNull(&params.FailureMessage),
	})
	if err != nil {
		return err
	}
	if !result.DraftExists {
		return ErrPipelineReferenceNotFound
	}
	if !result.Updated {
		return ErrPipelineDraftAttemptStale
	}
	return nil
}

func (r *repository) DeleteDraft(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) error {
	queries := dbgen.New(tx)
	result, err := queries.DeletePipelineDraft(ctx, dbgen.DeletePipelineDraftParams{
		OrgSlug:      orgSlug,
		ProjectSlug:  projectSlug,
		PipelineSlug: string(pipelineSlug),
		MemberSlug:   memberSlug,
	})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPipelineReferenceNotFound
	}

	return nil
}

func (r *repository) UpsertSpec(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	params UpsertSpecParams,
) (*PipelineSpec, error) {
	queries := dbgen.New(tx)
	row, err := queries.UpsertPipelineSpec(ctx, dbgen.UpsertPipelineSpecParams{
		OrgSlug:           orgSlug,
		ProjectSlug:       projectSlug,
		Slug:              params.Slug,
		Name:              params.Name,
		SpecJsonb:         params.SpecJSONB,
		SpecHash:          params.SpecHash,
		SyncStatus:        params.SyncStatus.String(),
		SourcePath:        stringValue(params.SourcePath),
		SourceRef:         stringValue(params.SourceRef),
		LastAppliedCommit: stringValue(params.LastAppliedCommit),
	})
	if err != nil {
		return nil, err
	}

	spec, err := toSpecFromUpsertRow(row)
	if err != nil {
		return nil, err
	}

	return spec, nil
}

func (r *repository) FindSpec(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (*PipelineSpec, error) {
	queries := dbgen.New(tx)
	row, err := queries.FindPipelineSpecBySlug(ctx, dbgen.FindPipelineSpecBySlugParams{
		OrgSlug:     orgSlug,
		ProjectSlug: projectSlug,
		Slug:        pipelineSlug,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrPipelineReferenceNotFound.With(err)
		}
		return nil, err
	}

	spec, err := toSpecFromFindRow(row)
	if err != nil {
		return nil, err
	}

	return spec, nil
}

func (r *repository) FindSpecForUpdate(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (*PipelineSpec, error) {
	queries := dbgen.New(tx)
	row, err := queries.FindPipelineSpecBySlugForUpdate(
		ctx,
		dbgen.FindPipelineSpecBySlugForUpdateParams{
			OrgSlug:     orgSlug,
			ProjectSlug: projectSlug,
			Slug:        pipelineSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrPipelineReferenceNotFound.With(err)
		}
		return nil, err
	}

	spec, err := toSpecFromFindForUpdateRow(row)
	if err != nil {
		return nil, err
	}

	return spec, nil
}

func (r *repository) ListSpecs(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	req model.ListRequest,
) ([]*PipelineSpec, error) {
	queries := dbgen.New(tx)
	order := req.Order
	if order != model.ListOrderAsc && order != model.ListOrderDesc {
		order = model.ListOrderDesc
	}

	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(
		req.Cursor,
		model.ListSortCreatedAt,
	)
	if err != nil {
		return nil, err
	}

	rows, err := queries.ListPipelineSpecsByProject(ctx, dbgen.ListPipelineSpecsByProjectParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		CursorSlug:      cursorSlug,
		OrderDir:        order,
		CursorTimestamp: cursorTimestamp,
		LimitCount:      int64(req.Limit),
	})
	if err != nil {
		return nil, err
	}
	specs := make([]*PipelineSpec, len(rows))
	for i, row := range rows {
		spec, parseErr := toSpecFromListRow(row)
		if parseErr != nil {
			return nil, parseErr
		}
		specs[i] = spec
	}
	return specs, nil
}

func (r *repository) DeleteSpec(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, deletedBy slug.Slug,
) error {
	queries := dbgen.New(tx)
	result, err := queries.DeletePipelineSpec(ctx, dbgen.DeletePipelineSpecParams{
		OrgSlug:       orgSlug,
		ProjectSlug:   projectSlug,
		Slug:          pipelineSlug,
		DeletedBySlug: deletedBy,
		DeleteReason:  pgtype.Text{Valid: false},
	})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPipelineReferenceNotFound
	}
	return nil
}

func (r *repository) RestoreSpec(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) error {
	queries := dbgen.New(tx)
	result, err := queries.RestorePipelineSpec(ctx, dbgen.RestorePipelineSpecParams{
		OrgSlug:     orgSlug,
		ProjectSlug: projectSlug,
		Slug:        pipelineSlug,
	})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPipelineReferenceNotFound
	}
	return nil
}

func (r *repository) FindSpecDeletedAtForUpdate(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) (time.Time, bool, error) {
	queries := dbgen.New(tx)
	deletedAt, err := queries.FindPipelineSpecDeletedAtForUpdate(
		ctx,
		dbgen.FindPipelineSpecDeletedAtForUpdateParams{
			OrgSlug:     orgSlug,
			ProjectSlug: projectSlug,
			Slug:        pipelineSlug,
		},
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, ErrPipelineReferenceNotFound.With(err)
		}
		return time.Time{}, false, err
	}
	if !deletedAt.Valid {
		return time.Time{}, false, nil
	}

	return deletedAt.Time, true, nil
}

func (r *repository) CreateVersion(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
	params CreateVersionParams,
) (*PipelineVersion, error) {
	queries := dbgen.New(tx)
	row, err := queries.CreatePipelineVersion(ctx, dbgen.CreatePipelineVersionParams{
		SourcePath:        textOrNull(params.SourcePath),
		OrgSlug:           orgSlug,
		ProjectSlug:       projectSlug,
		PipelineSlug:      string(params.PipelineSlug),
		GitCommitSha:      textOrNull(params.GitCommitSHA),
		SpecJsonb:         params.SpecJSONB,
		SpecHash:          params.SpecHash,
		ResolvedContracts: resolvedContractsJSON(params.ResolvedContracts),
	})
	if err != nil {
		return nil, err
	}
	return toVersion(row), nil
}

func (r *repository) FindVersionBySpecHash(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	specHash string,
) (*PipelineVersion, error) {
	queries := dbgen.New(tx)
	row, err := queries.FindPipelineVersionBySpecHash(
		ctx,
		dbgen.FindPipelineVersionBySpecHashParams{
			OrgSlug:      orgSlug,
			ProjectSlug:  projectSlug,
			PipelineSlug: string(pipelineSlug),
			SpecHash:     specHash,
		},
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrPipelineVersionNotFound.With(err)
		}
		return nil, err
	}
	return toVersion(row), nil
}

func (r *repository) FindVersionBySpecHashPrefix(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	specHashPrefix string,
) (*PipelineVersion, error) {
	queries := dbgen.New(tx)
	rows, err := queries.FindPipelineVersionsBySpecHashPrefix(
		ctx,
		dbgen.FindPipelineVersionsBySpecHashPrefixParams{
			OrgSlug:        orgSlug,
			ProjectSlug:    projectSlug,
			PipelineSlug:   string(pipelineSlug),
			SpecHashPrefix: specHashPrefix,
		},
	)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrPipelineVersionNotFound
	}
	if len(rows) > 1 {
		return nil, ErrPipelineVersionAmbiguous
	}
	return toVersion(rows[0]), nil
}

func (r *repository) FindVersionByCommitSHA(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	commitSHA string,
) (*PipelineVersion, error) {
	queries := dbgen.New(tx)
	row, err := queries.FindPipelineVersionByCommitSHA(
		ctx,
		dbgen.FindPipelineVersionByCommitSHAParams{
			OrgSlug:      orgSlug,
			ProjectSlug:  projectSlug,
			PipelineSlug: string(pipelineSlug),
			GitCommitSha: pgtype.Text{String: commitSHA, Valid: true},
		},
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrPipelineVersionNotFound.With(err)
		}
		return nil, err
	}
	return toVersion(row), nil
}

func (r *repository) FindVersionByID(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	versionID int64,
) (*PipelineVersion, error) {
	queries := dbgen.New(tx)
	row, err := queries.FindPipelineVersionByID(
		ctx,
		dbgen.FindPipelineVersionByIDParams{
			OrgSlug:      orgSlug,
			ProjectSlug:  projectSlug,
			PipelineSlug: string(pipelineSlug),
			VersionID:    int(versionID),
		},
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrPipelineVersionNotFound.With(err)
		}
		return nil, err
	}
	return toVersion(row), nil
}

func (r *repository) ListVersions(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) ([]*PipelineVersion, error) {
	queries := dbgen.New(tx)
	rows, err := queries.ListPipelineVersionsByPipeline(
		ctx,
		dbgen.ListPipelineVersionsByPipelineParams{
			OrgSlug:      orgSlug,
			ProjectSlug:  projectSlug,
			PipelineSlug: string(pipelineSlug),
		},
	)
	if err != nil {
		return nil, err
	}
	versions := make([]*PipelineVersion, len(rows))
	for i, row := range rows {
		versions[i] = toVersion(row)
	}
	return versions, nil
}

func (r *repository) SetCurrentVersion(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	versionID int64,
) error {
	queries := dbgen.New(tx)
	result, err := queries.SetCurrentPipelineVersion(ctx, dbgen.SetCurrentPipelineVersionParams{
		VersionID:    pgtype.Int8{Int64: versionID, Valid: true},
		OrgSlug:      pgtype.Text{String: string(orgSlug), Valid: true},
		ProjectSlug:  pgtype.Text{String: string(projectSlug), Valid: true},
		PipelineSlug: pgtype.Text{String: string(pipelineSlug), Valid: true},
	})
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrPipelineReferenceNotFound
	}
	return nil
}

func (r *repository) Deploy(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params DeployParams,
) (*PipelineDeployment, error) {
	row, err := dbgen.New(tx).UpsertPipelineDeployment(
		ctx,
		dbgen.UpsertPipelineDeploymentParams{
			OrgSlug:                 orgSlug,
			ProjectSlug:             projectSlug,
			EnvironmentSlug:         environmentSlug,
			VersionID:               int(params.VersionID),
			PipelineSlug:            params.PipelineSlug.String(),
			ExpectedCurrentSpecHash: textOrNull(params.ExpectedCurrentSpecHash),
			ExpectedRevision:        int64OrNull(params.ExpectedRevision),
			ResolutionDigest:        textOrNull(params.ResolutionDigest),
			DeployedByActorType:     params.DeployedByActorType,
			DeployedByActorSlug:     slugOrNull(params.DeployedByActorSlug),
		},
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if xerror.As(err, &pgErr) && pgErr.Code == "40001" {
			return nil, ErrPipelineDeploymentConflict.With(err)
		}
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrPipelineDeploymentNotFound.With(err)
		}
		return nil, err
	}
	return toDeploymentFromUpsertRow(row), nil
}

func (r *repository) FindDeployment(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
) (*PipelineDeployment, error) {
	row, err := dbgen.New(tx).FindPipelineDeployment(
		ctx,
		dbgen.FindPipelineDeploymentParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
			PipelineSlug:    pipelineSlug.String(),
		},
	)
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrPipelineDeploymentNotFound.With(err)
		}
		return nil, err
	}
	return toDeploymentFromFindRow(row), nil
}

func (r *repository) ListDeployments(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) ([]*PipelineDeployment, error) {
	rows, err := dbgen.New(tx).ListPipelineDeployments(
		ctx,
		dbgen.ListPipelineDeploymentsParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
		},
	)
	if err != nil {
		return nil, err
	}
	deployments := make([]*PipelineDeployment, len(rows))
	for i, row := range rows {
		deployments[i] = toDeploymentFromListRow(row)
	}
	return deployments, nil
}

func (r *repository) ProjectEnvironmentEnabled(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (bool, error) {
	return dbgen.New(tx).ProjectEnvironmentEnabled(
		ctx,
		dbgen.ProjectEnvironmentEnabledParams{
			OrgSlug:         orgSlug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
		},
	)
}

func (r *repository) FindProjectEnvironmentProtection(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) (*model.ProjectEnvironmentProtection, error) {
	row, err := dbgen.New(tx).FindProjectEnvironmentProtection(ctx, dbgen.FindProjectEnvironmentProtectionParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, xerror.NotFound(xerror.ProjectEnvironment).With(err)
		}
		return nil, err
	}
	return &model.ProjectEnvironmentProtection{
		EnvironmentSlug: row.EnvironmentSlug,
		Protected:       row.Protected,
	}, nil
}

func toReference(row dbgen.PipelineReference) *PipelineReference {
	return &PipelineReference{
		Slug:             row.Slug,
		Name:             row.Name,
		CreatedAt:        row.CreatedAt.Time,
		UpdatedAt:        row.UpdatedAt.Time,
		CurrentVersionID: nullableInt64(row.CurrentVersionID),
		HasDraft:         row.HasDraft,
	}
}

func toSpec(row dbgen.PipelineSpec) (*PipelineSpec, error) {
	syncStatus, err := SyncStatusString(row.SyncStatus)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &PipelineSpec{
		Slug:              row.Slug,
		Name:              row.Name,
		SpecJSONB:         row.SpecJsonb,
		SpecHash:          row.SpecHash,
		CurrentVersionID:  nullableInt64(row.CurrentVersionID),
		SyncStatus:        syncStatus,
		SourcePath:        stringPointer(row.SourcePath),
		SourceRef:         stringPointer(row.SourceRef),
		LastAppliedCommit: stringPointer(row.LastAppliedCommit),
		CreatedAt:         row.CreatedAt.Time,
		UpdatedAt:         row.UpdatedAt.Time,
	}, nil
}

func toSpecFromUpsertRow(row dbgen.UpsertPipelineSpecRow) (*PipelineSpec, error) {
	syncStatus, err := SyncStatusString(row.SyncStatus)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &PipelineSpec{
		Slug:              row.Slug,
		Name:              row.Name,
		SpecJSONB:         row.SpecJsonb,
		SpecHash:          row.SpecHash,
		CurrentVersionID:  nullableInt64(row.CurrentVersionID),
		SyncStatus:        syncStatus,
		SourcePath:        stringPointer(row.SourcePath),
		SourceRef:         stringPointer(row.SourceRef),
		LastAppliedCommit: stringPointer(row.LastAppliedCommit),
		CreatedAt:         row.CreatedAt.Time,
		UpdatedAt:         row.UpdatedAt.Time,
	}, nil
}

func toSpecFromFindRow(row dbgen.FindPipelineSpecBySlugRow) (*PipelineSpec, error) {
	syncStatus, err := SyncStatusString(row.SyncStatus)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &PipelineSpec{
		Slug:                   row.Slug,
		Name:                   row.Name,
		SpecJSONB:              row.SpecJsonb,
		SpecHash:               row.SpecHash,
		CurrentVersionID:       nullableInt64(row.CurrentVersionID),
		CurrentVersionSpecHash: nullableText(row.CurrentVersionSpecHash),
		SyncStatus:             syncStatus,
		SourcePath:             stringPointer(row.SourcePath),
		SourceRef:              stringPointer(row.SourceRef),
		LastAppliedCommit:      stringPointer(row.LastAppliedCommit),
		CreatedAt:              row.CreatedAt.Time,
		UpdatedAt:              row.UpdatedAt.Time,
	}, nil
}

func toSpecFromFindForUpdateRow(row dbgen.FindPipelineSpecBySlugForUpdateRow) (*PipelineSpec, error) {
	return toSpecFromFindRow(dbgen.FindPipelineSpecBySlugRow(row))
}

func toSpecFromListRow(row dbgen.ListPipelineSpecsByProjectRow) (*PipelineSpec, error) {
	syncStatus, err := SyncStatusString(row.SyncStatus)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return &PipelineSpec{
		Slug:                   row.Slug,
		Name:                   row.Name,
		SpecJSONB:              row.SpecJsonb,
		SpecHash:               row.SpecHash,
		CurrentVersionID:       nullableInt64(row.CurrentVersionID),
		CurrentVersionSpecHash: nullableText(row.CurrentVersionSpecHash),
		SyncStatus:             syncStatus,
		SourcePath:             stringPointer(row.SourcePath),
		SourceRef:              stringPointer(row.SourceRef),
		LastAppliedCommit:      stringPointer(row.LastAppliedCommit),
		CreatedAt:              row.CreatedAt.Time,
		UpdatedAt:              row.UpdatedAt.Time,
	}, nil
}

func textOrNull(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func textOrNullString(s string) pgtype.Text {
	return textOrNull(&s)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringPointer(value string) *string {
	return &value
}

func nullableText(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func nullableInt64(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func nullableTime(v pgtype.Timestamptz) *time.Time {
	if !v.Valid {
		return nil
	}
	return &v.Time
}

func nullableInt8(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}

	return pgtype.Int8{Int64: *v, Valid: true}
}

func slugOrNull(s *slug.Slug) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s.String(), Valid: true}
}

func toVersion(row dbgen.PipelineVersion) *PipelineVersion {
	return &PipelineVersion{
		SourcePath:        nullableText(row.SourcePath),
		ID:                int64(row.ID),
		PipelineSlug:      slug.Slug(row.PipelineSlug),
		GitCommitSHA:      nullableText(row.GitCommitSha),
		CreatedAt:         row.CreatedAt.Time,
		SpecJSONB:         row.SpecJsonb,
		SpecHash:          row.SpecHash,
		ResolvedContracts: row.ResolvedContracts,
	}
}

func resolvedContractsJSON(data []byte) []byte {
	if len(data) == 0 {
		return []byte("[]")
	}
	return data
}

func toDeploymentFromUpsertRow(row dbgen.UpsertPipelineDeploymentRow) *PipelineDeployment {
	return &PipelineDeployment{
		Revision:                 row.Revision,
		CurrentResolutionDigest:  nullableText(row.CurrentResolutionDigest),
		PreviousResolutionDigest: nullableText(row.PreviousResolutionDigest),
		ID:                       int64(row.ID),
		ProjectSlug:              slug.Slug(row.ProjectSlug),
		EnvironmentSlug:          slug.Slug(row.EnvironmentSlug),
		PipelineSlug:             slug.Slug(row.PipelineSlug),
		CurrentVersionID:         row.CurrentVersionID,
		CurrentVersionSpecHash:   row.CurrentVersionSpecHash,
		PreviousVersionID:        nullableInt64(row.PreviousVersionID),
		PreviousVersionSpecHash:  nullableText(row.PreviousVersionSpecHash),
		DeployedByActorType:      row.DeployedByActorType,
		DeployedByActorSlug:      nullableText(row.DeployedByActorSlug),
		DeployedAt:               row.DeployedAt.Time,
		UpdatedAt:                row.UpdatedAt.Time,
	}
}

func toDeploymentFromFindRow(row dbgen.FindPipelineDeploymentRow) *PipelineDeployment {
	return &PipelineDeployment{
		Revision:                 row.Revision,
		CurrentResolutionDigest:  nullableText(row.CurrentResolutionDigest),
		PreviousResolutionDigest: nullableText(row.PreviousResolutionDigest),
		ID:                       int64(row.ID),
		ProjectSlug:              row.ProjectSlug,
		EnvironmentSlug:          row.EnvironmentSlug,
		PipelineSlug:             slug.Slug(row.PipelineSlug),
		CurrentVersionID:         row.CurrentVersionID,
		CurrentVersionSpecHash:   row.CurrentVersionSpecHash,
		PreviousVersionID:        nullableInt64(row.PreviousVersionID),
		PreviousVersionSpecHash:  nullableText(row.PreviousVersionSpecHash),
		DeployedByActorType:      row.DeployedByActorType,
		DeployedByActorSlug:      nullableText(row.DeployedByActorSlug),
		DeployedAt:               row.DeployedAt.Time,
		UpdatedAt:                row.UpdatedAt.Time,
	}
}

func toDeploymentFromListRow(row dbgen.ListPipelineDeploymentsRow) *PipelineDeployment {
	return &PipelineDeployment{
		Revision:                 row.Revision,
		CurrentResolutionDigest:  nullableText(row.CurrentResolutionDigest),
		PreviousResolutionDigest: nullableText(row.PreviousResolutionDigest),
		ID:                       int64(row.ID),
		ProjectSlug:              row.ProjectSlug,
		EnvironmentSlug:          row.EnvironmentSlug,
		PipelineSlug:             slug.Slug(row.PipelineSlug),
		CurrentVersionID:         row.CurrentVersionID,
		CurrentVersionSpecHash:   row.CurrentVersionSpecHash,
		PreviousVersionID:        nullableInt64(row.PreviousVersionID),
		PreviousVersionSpecHash:  nullableText(row.PreviousVersionSpecHash),
		DeployedByActorType:      row.DeployedByActorType,
		DeployedByActorSlug:      nullableText(row.DeployedByActorSlug),
		DeployedAt:               row.DeployedAt.Time,
		UpdatedAt:                row.UpdatedAt.Time,
	}
}

func toDraft(row dbgen.PipelineDraft) *PipelineDraft {
	return &PipelineDraft{
		PipelineSlug:              slug.Slug(row.PipelineSlug),
		BasedOnVersionID:          nullableInt64(row.BasedOnVersionID),
		SpecJSONB:                 row.SpecJsonb,
		DraftState:                nullableText(row.DraftState),
		BaseRef:                   nullableText(row.BaseRef),
		BaseCommitSHA:             nullableText(row.BaseCommitSha),
		BranchName:                nullableText(row.BranchName),
		BranchHeadSHA:             nullableText(row.BranchHeadSha),
		ProposalProvider:          nullableText(row.ProposalProvider),
		ProposalID:                nullableText(row.ProposalID),
		ProposalURL:               nullableText(row.ProposalUrl),
		ProposalState:             nullableText(row.ProposalState),
		LastProposedDraftHash:     nullableText(row.LastProposedDraftHash),
		QueuedAt:                  nullableTime(row.QueuedAt),
		ProposalUpdatedAt:         nullableTime(row.ProposalUpdatedAt),
		FailedAt:                  nullableTime(row.FailedAt),
		PublishAttemptID:          nullableText(row.PublishAttemptID),
		RefreshAttemptID:          nullableText(row.RefreshAttemptID),
		FailureCode:               nullableText(row.FailureCode),
		FailureMessage:            nullableText(row.FailureMessage),
		ProposalAuthorityID:       nullableInt64(row.ProposalAuthorityID),
		PublicationMode:           nullableText(row.PublicationMode),
		ProviderHost:              nullableText(row.ProviderHost),
		ProviderAccountID:         nullableText(row.ProviderAccountID),
		ProviderRepositoryID:      nullableText(row.ProviderRepositoryID),
		ProviderCredentialID:      nullableText(row.ProviderCredentialID),
		ExecutingPrincipalID:      nullableText(row.ExecutingPrincipalID),
		ExecutingPrincipalDisplay: nullableText(row.ExecutingPrincipalDisplay),
		AuthoritySource:           nullableText(row.AuthoritySource),
		CreatedAt:                 row.CreatedAt.Time,
		UpdatedAt:                 row.UpdatedAt.Time,
	}
}

func toDraftFromFindRow(row dbgen.FindPipelineDraftBySlugRow) *PipelineDraft {
	return &PipelineDraft{
		PipelineSlug:              slug.Slug(row.PipelineSlug),
		BasedOnVersionID:          nullableInt64(row.BasedOnVersionID),
		BasedOnSpecHash:           nullableText(row.BasedOnSpecHash),
		CurrentVersionID:          nullableInt64(row.CurrentVersionID),
		SpecJSONB:                 row.SpecJsonb,
		DraftState:                nullableText(row.DraftState),
		BaseRef:                   nullableText(row.BaseRef),
		BaseCommitSHA:             nullableText(row.BaseCommitSha),
		BranchName:                nullableText(row.BranchName),
		BranchHeadSHA:             nullableText(row.BranchHeadSha),
		ProposalProvider:          nullableText(row.ProposalProvider),
		ProposalID:                nullableText(row.ProposalID),
		ProposalURL:               nullableText(row.ProposalUrl),
		ProposalState:             nullableText(row.ProposalState),
		LastProposedDraftHash:     nullableText(row.LastProposedDraftHash),
		QueuedAt:                  nullableTime(row.QueuedAt),
		ProposalUpdatedAt:         nullableTime(row.ProposalUpdatedAt),
		FailedAt:                  nullableTime(row.FailedAt),
		PublishAttemptID:          nullableText(row.PublishAttemptID),
		RefreshAttemptID:          nullableText(row.RefreshAttemptID),
		FailureCode:               nullableText(row.FailureCode),
		FailureMessage:            nullableText(row.FailureMessage),
		ProposalAuthorityID:       nullableInt64(row.ProposalAuthorityID),
		PublicationMode:           nullableText(row.PublicationMode),
		ProviderHost:              nullableText(row.ProviderHost),
		ProviderAccountID:         nullableText(row.ProviderAccountID),
		ProviderRepositoryID:      nullableText(row.ProviderRepositoryID),
		ProviderCredentialID:      nullableText(row.ProviderCredentialID),
		ExecutingPrincipalID:      nullableText(row.ExecutingPrincipalID),
		ExecutingPrincipalDisplay: nullableText(row.ExecutingPrincipalDisplay),
		AuthoritySource:           nullableText(row.AuthoritySource),
		CreatedAt:                 row.CreatedAt.Time,
		UpdatedAt:                 row.UpdatedAt.Time,
	}
}

func toDraftFromAnyRow(row dbgen.FindAnyPipelineDraftBySlugRow) *PipelineDraft {
	return &PipelineDraft{
		PipelineSlug:              slug.Slug(row.PipelineSlug),
		BasedOnVersionID:          nullableInt64(row.BasedOnVersionID),
		BasedOnSpecHash:           nullableText(row.BasedOnSpecHash),
		CurrentVersionID:          nullableInt64(row.CurrentVersionID),
		SpecJSONB:                 row.SpecJsonb,
		DraftState:                nullableText(row.DraftState),
		BaseRef:                   nullableText(row.BaseRef),
		BaseCommitSHA:             nullableText(row.BaseCommitSha),
		BranchName:                nullableText(row.BranchName),
		BranchHeadSHA:             nullableText(row.BranchHeadSha),
		ProposalProvider:          nullableText(row.ProposalProvider),
		ProposalID:                nullableText(row.ProposalID),
		ProposalURL:               nullableText(row.ProposalUrl),
		ProposalState:             nullableText(row.ProposalState),
		LastProposedDraftHash:     nullableText(row.LastProposedDraftHash),
		QueuedAt:                  nullableTime(row.QueuedAt),
		ProposalUpdatedAt:         nullableTime(row.ProposalUpdatedAt),
		FailedAt:                  nullableTime(row.FailedAt),
		PublishAttemptID:          nullableText(row.PublishAttemptID),
		RefreshAttemptID:          nullableText(row.RefreshAttemptID),
		FailureCode:               nullableText(row.FailureCode),
		FailureMessage:            nullableText(row.FailureMessage),
		ProposalAuthorityID:       nullableInt64(row.ProposalAuthorityID),
		PublicationMode:           nullableText(row.PublicationMode),
		ProviderHost:              nullableText(row.ProviderHost),
		ProviderAccountID:         nullableText(row.ProviderAccountID),
		ProviderRepositoryID:      nullableText(row.ProviderRepositoryID),
		ProviderCredentialID:      nullableText(row.ProviderCredentialID),
		ExecutingPrincipalID:      nullableText(row.ExecutingPrincipalID),
		ExecutingPrincipalDisplay: nullableText(row.ExecutingPrincipalDisplay),
		AuthoritySource:           nullableText(row.AuthoritySource),
		CreatedAt:                 row.CreatedAt.Time,
		UpdatedAt:                 row.UpdatedAt.Time,
	}
}

func int64OrNull(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}
