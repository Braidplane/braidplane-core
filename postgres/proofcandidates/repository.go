package proofcandidates

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type Repository interface {
	LockSource(context.Context, postgres.DBTX, slug.Slug, slug.Slug) error
	CreateMapping(context.Context, postgres.DBTX, CreateMappingParams) (int64, error)
	ResolveMappings(context.Context, postgres.DBTX, MappingLookup) ([]Mapping, error)
	ResolveWebhookMappings(context.Context, postgres.DBTX, WebhookMappingLookup) ([]Mapping, error)
	FindByKey(context.Context, postgres.DBTX, slug.Slug, string) (*Candidate, error)
	FindByID(context.Context, postgres.DBTX, slug.Slug, int64) (*Candidate, error)
	FindLatest(context.Context, postgres.DBTX, slug.Slug, int64, string) (*Candidate, error)
	CloseLatest(context.Context, postgres.DBTX, slug.Slug, int64, string, string) (*Candidate, error)
	ReopenByKey(context.Context, postgres.DBTX, slug.Slug, string) (*Candidate, error)
	Create(context.Context, postgres.DBTX, slug.Slug, CreateCandidateParams) (*Candidate, error)
}

func (*repository) LockSource(ctx context.Context, db postgres.DBTX, org, ds slug.Slug) error {
	_, err := dbgen.New(db).LockDataSourceForUpdate(ctx, dbgen.LockDataSourceForUpdateParams{OrgSlug: org, DsSlug: ds})
	if xerror.Is(err, pgx.ErrNoRows) {
		return ErrMappingTargetNotFound.With(err)
	}
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

type DeliveryClaimer interface {
	ClaimWebhookDelivery(context.Context, postgres.DBTX, string, string, string) (bool, error)
}

var ErrWebhookDeliveryDigestMismatch = xerror.New(xerror.KindInvalidInput, "PROOF_WEBHOOK_DELIVERY_DIGEST_MISMATCH", "proof webhook delivery identity was reused with different bytes")

func NewDeliveryClaimer() DeliveryClaimer { return &repository{} }

func (*repository) ClaimWebhookDelivery(ctx context.Context, db postgres.DBTX, provider, deliveryID, payloadSHA256 string) (bool, error) {
	queries := dbgen.New(db)
	_, err := queries.ClaimProofWebhookDelivery(ctx, dbgen.ClaimProofWebhookDeliveryParams{
		Provider: provider, DeliveryID: deliveryID, PayloadSha256: payloadSHA256,
	})
	if err == nil {
		return true, nil
	}
	if !xerror.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	stored, err := queries.GetProofWebhookDeliveryDigest(ctx, dbgen.GetProofWebhookDeliveryDigestParams{Provider: provider, DeliveryID: deliveryID})
	if err != nil {
		return false, err
	}
	if stored != payloadSHA256 {
		return false, ErrWebhookDeliveryDigestMismatch
	}
	return false, nil
}

func (*repository) ResolveWebhookMappings(ctx context.Context, db postgres.DBTX, lookup WebhookMappingLookup) ([]Mapping, error) {
	rows, err := dbgen.New(db).ResolveProofChangeWebhookMappings(ctx, dbgen.ResolveProofChangeWebhookMappingsParams{
		Provider: lookup.Provider, ProviderConnectionID: lookup.ProviderConnectionID,
		RepositoryID: lookup.RepositoryID, TargetRef: lookup.TargetRef,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Mapping, len(rows))
	for i, row := range rows {
		out[i] = mapping(row.ID, row.OrganizationSlug, row.ProjectSlug, row.EnvironmentSlug,
			row.ProviderConnectionSlug, row.Provider, row.ProviderConnectionID, row.RepositoryID,
			row.TargetRef, row.PipelinePath)
	}
	return out, nil
}

func (*repository) FindByKey(ctx context.Context, db postgres.DBTX, orgSlug slug.Slug, candidateKey string) (*Candidate, error) {
	row, err := dbgen.New(db).FindProofChangeCandidateByKey(ctx, dbgen.FindProofChangeCandidateByKeyParams{
		OrgSlug: orgSlug, CandidateKey: candidateKey,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrCandidateNotFound.With(err)
		}
		return nil, err
	}
	return candidate(row), nil
}

func (*repository) FindByID(ctx context.Context, db postgres.DBTX, orgSlug slug.Slug, candidateID int64) (*Candidate, error) {
	row, err := dbgen.New(db).FindProofChangeCandidateByID(ctx, dbgen.FindProofChangeCandidateByIDParams{
		OrgSlug: orgSlug, CandidateID: candidateID,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrCandidateNotFound.With(err)
		}
		return nil, err
	}
	return candidate(row), nil
}

type repository struct{}

func New() Repository { return &repository{} }

func (*repository) CreateMapping(ctx context.Context, db postgres.DBTX, params CreateMappingParams) (int64, error) {
	id, err := dbgen.New(db).CreateProofChangeCandidateMapping(ctx, dbgen.CreateProofChangeCandidateMappingParams{
		OrgSlug: params.OrganizationSlug, ProjectSlug: params.ProjectSlug,
		EnvironmentSlug: params.EnvironmentSlug, ProviderConnectionSlug: params.ProviderConnectionSlug,
		Provider: params.Provider, ProviderConnectionID: params.ProviderConnectionID,
		RepositoryID: params.RepositoryID, TargetRef: params.TargetRef, PipelinePath: params.PipelinePath,
	})
	if xerror.Is(err, pgx.ErrNoRows) {
		return 0, ErrMappingTargetNotFound.With(err)
	}
	return id, err
}

func (*repository) ResolveMappings(ctx context.Context, db postgres.DBTX, lookup MappingLookup) ([]Mapping, error) {
	rows, err := dbgen.New(db).ResolveProofChangeCandidateMappings(ctx, dbgen.ResolveProofChangeCandidateMappingsParams{
		MappingID: lookup.MappingID, OrgSlug: lookup.OrganizationSlug, Provider: lookup.Provider, ProviderConnectionID: lookup.ProviderConnectionID,
		RepositoryID: lookup.RepositoryID, TargetRef: lookup.TargetRef, PipelinePath: lookup.PipelinePath,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Mapping, len(rows))
	for i, row := range rows {
		out[i] = mapping(row.ID, row.OrganizationSlug, row.ProjectSlug, row.EnvironmentSlug,
			row.ProviderConnectionSlug, row.Provider, row.ProviderConnectionID, row.RepositoryID,
			row.TargetRef, row.PipelinePath)
	}
	return out, nil
}

func mapping(id int64, organizationSlug, projectSlug, environmentSlug, providerConnectionSlug slug.Slug, provider, providerConnectionID, repositoryID, targetRef, pipelinePath string) Mapping {
	return Mapping{
		ID: id, OrganizationSlug: organizationSlug, ProjectSlug: projectSlug,
		EnvironmentSlug: environmentSlug, ProviderConnectionSlug: providerConnectionSlug,
		Provider: provider, ProviderConnectionID: providerConnectionID, RepositoryID: repositoryID,
		TargetRef: targetRef, PipelinePath: pipelinePath,
	}
}

func (*repository) FindLatest(ctx context.Context, db postgres.DBTX, orgSlug slug.Slug, mappingID int64, changeRequestID string) (*Candidate, error) {
	row, err := dbgen.New(db).FindLatestProofChangeCandidate(ctx, dbgen.FindLatestProofChangeCandidateParams{
		OrgSlug: orgSlug, MappingID: mappingID, ChangeRequestID: changeRequestID,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrCandidateNotFound.With(err)
		}
		return nil, err
	}
	return candidate(row), nil
}

func (*repository) CloseLatest(ctx context.Context, db postgres.DBTX, orgSlug slug.Slug, mappingID int64, changeRequestID, headSHA string) (*Candidate, error) {
	row, err := dbgen.New(db).CloseLatestProofChangeCandidate(ctx, dbgen.CloseLatestProofChangeCandidateParams{
		OrgSlug: orgSlug, MappingID: mappingID, ChangeRequestID: changeRequestID, HeadSha: headSHA,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrCandidateNotFound.With(err)
		}
		return nil, err
	}
	return candidate(row), nil
}

func (*repository) ReopenByKey(ctx context.Context, db postgres.DBTX, orgSlug slug.Slug, candidateKey string) (*Candidate, error) {
	row, err := dbgen.New(db).ReopenProofChangeCandidateByKey(ctx, dbgen.ReopenProofChangeCandidateByKeyParams{
		OrgSlug: orgSlug, CandidateKey: candidateKey,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrCandidateNotFound.With(err)
		}
		return nil, err
	}
	return candidate(row), nil
}

func (*repository) Create(ctx context.Context, db postgres.DBTX, orgSlug slug.Slug, params CreateCandidateParams) (*Candidate, error) {
	row, err := dbgen.New(db).CreateProofChangeCandidate(ctx, dbgen.CreateProofChangeCandidateParams{
		OrgSlug: orgSlug, MappingID: params.MappingID, CandidateSlug: params.Slug,
		CandidateKey: params.CandidateKey, ChangeRequestID: params.ChangeRequestID,
		SourceRef: params.SourceRef, HeadSha: params.HeadSHA, SourceSha256: params.SourceSHA256,
		SpecHash: params.SpecHash, CanonicalSpec: params.CanonicalSpec,
		SupersedesCandidateID: nullableInt8(params.SupersedesCandidateID),
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, ErrMappingTargetNotFound.With(err)
		}
		return nil, err
	}
	return candidate(row), nil
}

func candidate(row dbgen.ProofChangeCandidate) *Candidate {
	var supersedes *int64
	if row.SupersedesCandidateID.Valid {
		value := row.SupersedesCandidateID.Int64
		supersedes = &value
	}
	var closedAt *time.Time
	if row.ClosedAt.Valid {
		value := row.ClosedAt.Time
		closedAt = &value
	}
	return &Candidate{
		ID: row.ID, MappingID: row.MappingID, Slug: row.Slug,
		CandidateKey: row.CandidateKey, ChangeRequestID: row.ChangeRequestID,
		SourceRef: row.SourceRef, HeadSHA: row.HeadSha, SourceSHA256: row.SourceSha256,
		SpecHash: row.SpecHash, CanonicalSpec: append([]byte(nil), row.CanonicalSpec...),
		SupersedesCandidateID: supersedes, ClosedAt: closedAt, CreatedAt: row.CreatedAt.Time,
	}
}

func nullableInt8(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}
