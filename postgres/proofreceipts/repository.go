package proofreceipts

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type Repository interface {
	LockSource(context.Context, postgres.DBTX, slug.Slug, slug.Slug) error
	PublicationEnabled(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (bool, error)
	FindLatestForCandidate(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*Receipt, error)
	FindCandidateTarget(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*CandidateTarget, error)
	LockTarget(context.Context, postgres.DBTX, string) error
	CreateEvidence(context.Context, postgres.DBTX, *CandidateTarget, EvidenceParams) (*Evidence, error)
	FindLatest(context.Context, postgres.DBTX, *CandidateTarget, string) (*LatestReceipt, error)
	Create(context.Context, postgres.DBTX, *CandidateTarget, CreateReceiptParams) (*Receipt, error)
	FindBySlug(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*Receipt, error)
	List(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, model.ListRequest) ([]*Receipt, error)
	ListLifecycleLinksByReceipt(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) ([]LifecycleLink, error)
}

func (*repository) LockSource(ctx context.Context, db postgres.DBTX, org, ds slug.Slug) error {
	_, err := dbgen.New(db).LockDataSourceForUpdate(ctx, dbgen.LockDataSourceForUpdateParams{OrgSlug: org, DsSlug: ds})
	if xerror.Is(err, pgx.ErrNoRows) {
		return ErrCandidateNotFound.With(err)
	}
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (*repository) ListLifecycleLinksByReceipt(ctx context.Context, db postgres.DBTX, orgSlug, projectSlug, environmentSlug, receiptSlug slug.Slug) ([]LifecycleLink, error) {
	rows, err := dbgen.New(db).ListProofLifecycleLinksByReceipt(ctx, dbgen.ListProofLifecycleLinksByReceiptParams{
		OrgSlug: orgSlug, ProjectSlug: projectSlug, EnvironmentSlug: environmentSlug, ReceiptSlug: receiptSlug,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	links := make([]LifecycleLink, 0, len(rows))
	for _, row := range rows {
		link, err := lifecycleLink(row.Kind, row.Identifier, row.ReceiptSlug, row.SpecHash, row.BindingFingerprint, row.CapabilityFingerprint, row.LinkedAt.Time, row.RetainUntil.Time, row.Superseded)
		if err != nil {
			return nil, err
		}
		if row.PipelineSlug.Valid {
			value := slug.Slug(row.PipelineSlug.String)
			link.PipelineSlug = &value
		}
		if row.RunSlug.Valid {
			value := slug.Slug(row.RunSlug.String)
			link.RunSlug = &value
		}
		link.CurrentSpecHash = row.CurrentVersionSpecHash
		if link.Kind == model.ProofLifecycleLinkKindDeployment && link.PipelineSlug != nil && row.DeployedAt.Valid {
			link.CurrentIdentifier = link.PipelineSlug.String() + ":" + link.CurrentSpecHash + ":" + row.DeployedAt.Time.UTC().Format(time.RFC3339Nano)
		}
		links = append(links, *link)
	}
	return links, nil
}

type LifecycleRepository interface {
	FindBySlug(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*Receipt, error)
	LinkDeployment(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, LinkDeploymentParams) (*LifecycleLink, error)
	LinkRun(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, string, time.Time) (*LifecycleLink, error)
	FindDeploymentLink(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*LifecycleLink, error)
	FindRunLink(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*LifecycleLink, error)
}

type FullRepository interface {
	Repository
	LifecycleRepository
	MaintenanceRepository
	OperationalRepository
}

type repository struct{}

func New() FullRepository { return &repository{} }

func (*repository) FindLatestForCandidate(ctx context.Context, db postgres.DBTX, org, candidate slug.Slug) (*Receipt, error) {
	row, err := dbgen.New(db).FindLatestProofReceiptForCandidate(ctx, dbgen.FindLatestProofReceiptForCandidateParams{OrgSlug: org, CandidateSlug: candidate})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrReceiptNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return decodeReceipt(row.Receipt, row.Superseded)
}

func (*repository) FindCandidateTarget(ctx context.Context, db postgres.DBTX, orgSlug, candidateSlug slug.Slug) (*CandidateTarget, error) {
	row, err := dbgen.New(db).FindProofCandidateReceiptTarget(ctx, dbgen.FindProofCandidateReceiptTargetParams{
		OrgSlug: orgSlug, CandidateSlug: candidateSlug,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrCandidateNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	var supersedesCandidateID *int64
	if row.SupersedesCandidateID.Valid {
		value := row.SupersedesCandidateID.Int64
		supersedesCandidateID = &value
	}
	return &CandidateTarget{
		OrganizationID: row.OrganizationID, ProjectID: row.ProjectID,
		ProjectEnvironmentID: row.ProjectEnvironmentID, MappingID: row.MappingID, CandidateID: row.ID,
		SupersedesCandidateID: supersedesCandidateID,
		CandidateSlug:         row.Slug, OrganizationSlug: row.OrganizationSlug, ProjectSlug: row.ProjectSlug,
		EnvironmentSlug: row.EnvironmentSlug, ProviderConnectionSlug: row.ProviderConnectionSlug,
		RepositoryID: row.RepositoryID, ChangeRequestID: row.ChangeRequestID, HeadSHA: row.HeadSha,
		TargetRef: row.TargetRef, PipelinePath: row.PipelinePath, SpecHash: row.SpecHash,
		CanonicalSpec: append([]byte(nil), row.CanonicalSpec...), CreatedAt: row.CreatedAt.Time,
	}, nil
}

func (*repository) LockTarget(ctx context.Context, db postgres.DBTX, targetKey string) error {
	if err := dbgen.New(db).LockProofReceiptTarget(ctx, targetKey); err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func (*repository) CreateEvidence(ctx context.Context, db postgres.DBTX, target *CandidateTarget, params EvidenceParams) (*Evidence, error) {
	row, err := dbgen.New(db).CreateProofEvidence(ctx, dbgen.CreateProofEvidenceParams{
		OrganizationID: target.OrganizationID, ProjectID: target.ProjectID,
		ProjectEnvironmentID: target.ProjectEnvironmentID, MappingID: target.MappingID,
		CandidateID: target.CandidateID, SpecHash: target.SpecHash,
		BindingFingerprint: params.BindingFingerprint, CapabilityFingerprint: params.CapabilityFingerprint,
		Kind: params.Kind, Digest: params.Digest, SourceIdentifier: params.SourceIdentifier,
		ObservedAt: postgres.ToTimestamptz(params.ObservedAt), ExpiresAt: optionalTime(params.ExpiresAt),
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return evidence(row), nil
}

func (*repository) FindLatest(ctx context.Context, db postgres.DBTX, target *CandidateTarget, receiptKey string) (*LatestReceipt, error) {
	var supersedesCandidateID int64
	if target.SupersedesCandidateID != nil {
		supersedesCandidateID = *target.SupersedesCandidateID
	}
	row, err := dbgen.New(db).FindLatestProofReceiptForChange(ctx, dbgen.FindLatestProofReceiptForChangeParams{
		OrganizationID: target.OrganizationID, MappingID: target.MappingID,
		ChangeRequestID: target.ChangeRequestID, CandidateID: target.CandidateID,
		SupersedesCandidateID: supersedesCandidateID, ReceiptKey: receiptKey,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrReceiptNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return &LatestReceipt{ID: int64(row.ID), Slug: row.Slug}, nil
}

func (*repository) Create(ctx context.Context, db postgres.DBTX, target *CandidateTarget, params CreateReceiptParams) (*Receipt, error) {
	raw, err := json.Marshal(params.Receipt)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	var supersedes pgtype.Int8
	if params.Supersedes != nil {
		supersedes = pgtype.Int8{Int64: params.Supersedes.ID, Valid: true}
	}
	row, err := dbgen.New(db).CreateProofReceipt(ctx, dbgen.CreateProofReceiptParams{
		OrganizationID: target.OrganizationID, MappingID: target.MappingID,
		ReceiptKey: params.ReceiptKey,
		ProjectID:  target.ProjectID, ProjectEnvironmentID: target.ProjectEnvironmentID,
		CandidateID: target.CandidateID, EvidenceID: params.EvidenceID,
		ReceiptSlug: params.Receipt.ID, SpecHash: target.SpecHash,
		BindingFingerprint:    params.Receipt.Candidate.BindingFingerprint,
		CapabilityFingerprint: params.Receipt.Candidate.CapabilityFingerprint,
		CompilerVersion:       params.Receipt.Candidate.CompilerVersion,
		PolicyVersion:         params.Receipt.Candidate.PolicyVersion,
		AggregateVerdict:      params.Receipt.AggregateVerdict.String(), Receipt: raw,
		SupersedesReceiptID: supersedes,
		CreatedAt:           postgres.ToTimestamptz(params.Receipt.CreatedAt),
		RetainUntil:         postgres.ToTimestamptz(params.Receipt.RetainUntil),
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrReceiptNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return decodeReceipt(row.Receipt, row.Superseded)
}

func (*repository) FindBySlug(ctx context.Context, db postgres.DBTX, orgSlug, projectSlug, environmentSlug, receiptSlug slug.Slug) (*Receipt, error) {
	row, err := dbgen.New(db).FindProofReceiptBySlug(ctx, dbgen.FindProofReceiptBySlugParams{
		OrgSlug: orgSlug, ProjectSlug: projectSlug, EnvironmentSlug: environmentSlug, ReceiptSlug: receiptSlug,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrReceiptNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return decodeReceipt(row.Receipt, row.Superseded)
}

func (*repository) List(ctx context.Context, db postgres.DBTX, orgSlug, projectSlug, environmentSlug slug.Slug, req model.ListRequest) ([]*Receipt, error) {
	cursorSlug, cursorTimestamp, _, err := postgres.BuildCursorValues(req.Cursor, model.ListSortCreatedAt)
	if err != nil {
		return nil, err
	}
	rows, err := dbgen.New(db).ListProofReceipts(ctx, dbgen.ListProofReceiptsParams{
		OrgSlug: orgSlug, ProjectSlug: projectSlug, EnvironmentSlug: environmentSlug,
		CursorSlug: cursorSlug, CursorTimestamp: cursorTimestamp,
		OrderDir: pgtype.Text{String: req.Order, Valid: true}, LimitCount: int64(req.Limit),
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	receipts := make([]*Receipt, 0, len(rows))
	for _, row := range rows {
		receipt, err := decodeReceipt(row.Receipt, row.Superseded)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

func (*repository) LinkDeployment(ctx context.Context, db postgres.DBTX, orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug, params LinkDeploymentParams) (*LifecycleLink, error) {
	row, err := dbgen.New(db).CreateProofDeploymentLink(ctx, dbgen.CreateProofDeploymentLinkParams{
		OrgSlug: orgSlug, ProjectSlug: projectSlug, EnvironmentSlug: environmentSlug,
		PipelineSlug: pipelineSlug.String(), ReceiptSlug: params.ReceiptSlug,
		Identifier: params.Identifier, SpecHash: params.SpecHash,
		LinkedAt: postgres.ToTimestamptz(params.LinkedAt),
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrLifecycleLinkNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return lifecycleLink(row.Kind, row.Identifier, row.ReceiptSlug, row.SpecHash, row.BindingFingerprint, row.CapabilityFingerprint, row.LinkedAt.Time, row.RetainUntil.Time, row.Superseded)
}

func (*repository) LinkRun(ctx context.Context, db postgres.DBTX, orgSlug, projectSlug, environmentSlug, runSlug slug.Slug, deploymentID int64, deploymentIdentifier string, linkedAt time.Time) (*LifecycleLink, error) {
	row, err := dbgen.New(db).CreateProofRunLink(ctx, dbgen.CreateProofRunLinkParams{
		OrgSlug: orgSlug, ProjectSlug: projectSlug, EnvironmentSlug: environmentSlug,
		RunSlug: runSlug.String(), DeploymentID: pgtype.Int8{Int64: deploymentID, Valid: true},
		DeploymentIdentifier: deploymentIdentifier, Identifier: runSlug.String(), LinkedAt: postgres.ToTimestamptz(linkedAt),
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrLifecycleLinkNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return lifecycleLink(row.Kind, row.Identifier, row.ReceiptSlug, row.SpecHash, row.BindingFingerprint, row.CapabilityFingerprint, row.LinkedAt.Time, row.RetainUntil.Time, row.Superseded)
}

func (*repository) FindDeploymentLink(ctx context.Context, db postgres.DBTX, orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug) (*LifecycleLink, error) {
	row, err := dbgen.New(db).FindProofDeploymentLink(ctx, dbgen.FindProofDeploymentLinkParams{
		OrgSlug: orgSlug, ProjectSlug: projectSlug, EnvironmentSlug: environmentSlug,
		PipelineSlug: pipelineSlug.String(),
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrLifecycleLinkNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return lifecycleLink(row.Kind, row.Identifier, row.ReceiptSlug, row.SpecHash, row.BindingFingerprint, row.CapabilityFingerprint, row.LinkedAt.Time, row.RetainUntil.Time, row.Superseded)
}

func (*repository) FindRunLink(ctx context.Context, db postgres.DBTX, orgSlug, projectSlug, environmentSlug, runSlug slug.Slug) (*LifecycleLink, error) {
	row, err := dbgen.New(db).FindProofRunLink(ctx, dbgen.FindProofRunLinkParams{
		OrgSlug: orgSlug, ProjectSlug: projectSlug, EnvironmentSlug: environmentSlug, RunSlug: runSlug.String(),
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrLifecycleLinkNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}
	return lifecycleLink(row.Kind, row.Identifier, row.ReceiptSlug, row.SpecHash, row.BindingFingerprint, row.CapabilityFingerprint, row.LinkedAt.Time, row.RetainUntil.Time, row.Superseded)
}

func lifecycleLink(kind, identifier string, receiptSlug slug.Slug, specHash, bindingFingerprint, capabilityFingerprint string, linkedAt, retainUntil time.Time, superseded bool) (*LifecycleLink, error) {
	parsedKind, err := model.ProofLifecycleLinkKindString(kind)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return &LifecycleLink{Kind: parsedKind, ReceiptSlug: receiptSlug, Identifier: identifier, SpecHash: specHash, BindingFingerprint: bindingFingerprint, CapabilityFingerprint: capabilityFingerprint, LinkedAt: linkedAt, RetainUntil: retainUntil, Superseded: superseded}, nil
}

func evidence(row dbgen.CreateProofEvidenceRow) *Evidence {
	var expiresAt *time.Time
	if row.ExpiresAt.Valid {
		value := row.ExpiresAt.Time
		expiresAt = &value
	}
	return &Evidence{ID: int64(row.ID), Digest: row.Digest, ObservedAt: row.ObservedAt.Time, ExpiresAt: expiresAt}
}

func decodeReceipt(raw []byte, superseded bool) (*Receipt, error) {
	var value model.ProofReceipt
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if err := value.Validate(); err != nil {
		return nil, xerror.Wrapf("validate stored proof receipt: %w", err)
	}
	return &Receipt{Value: value, Superseded: superseded}, nil
}

func optionalTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return postgres.ToTimestamptz(*value)
}

func (*repository) PublicationEnabled(ctx context.Context, db postgres.DBTX, org, receipt slug.Slug) (bool, error) {
	enabled, err := dbgen.New(db).ProofReceiptPublicationEnabled(ctx, dbgen.ProofReceiptPublicationEnabledParams{OrgSlug: org, ReceiptSlug: receipt})
	if xerror.Is(err, pgx.ErrNoRows) {
		return false, ErrReceiptNotFound.With(err)
	}
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return enabled, nil
}
