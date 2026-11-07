package proofconfig

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

var (
	ErrSourceNotFound = xerror.New(xerror.KindNotFound, "PROOF_DATA_SOURCE_NOT_FOUND", "Git data source not found")
	ErrTargetConflict = xerror.New(xerror.KindAlreadyExists, "PROOF_TARGET_CONFLICT", "Proof target changed; reload and retry")
)

type Source struct {
	MappingsCurrent                                                                               bool
	Slug                                                                                          slug.Slug
	Name, Provider, RepositoryID, ConnectionID                                                    string
	Enabled, PublishChecks, Active, CredentialActive, Managed, RepositoryAccessible, WebhookReady bool
}

type Target struct {
	Project, Environment slug.Slug
	Ref, Path            string
}

type TargetContext struct {
	Project, Environment slug.Slug
	Ref, Directory       string
}

type Repository struct{}

func New() *Repository { return &Repository{} }

func (*Repository) List(ctx context.Context, db postgres.DBTX, org, ds slug.Slug) ([]Source, error) {
	rows, err := dbgen.New(db).ListDataSourceProofConfigurations(ctx, dbgen.ListDataSourceProofConfigurationsParams{OrgSlug: org, DataSourceSlug: ds.String()})
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]Source, 0, len(rows))
	for _, row := range rows {
		source := Source{
			Slug: row.Slug, Name: row.Name, Provider: row.Type, RepositoryID: row.RepositoryID,
			Enabled: row.ProofEnabled, PublishChecks: row.ProofChecksEnabled, Active: row.IsActive,
			CredentialActive: row.CredentialStatus == "active" && !row.RevokedAt.Valid,
			Managed:          row.RepositoryID != "" && (row.Type == "github" || row.Type == "gitlab" && row.CredentialKind == "oauth2_grant"),
			ConnectionID:     row.InstallationID, RepositoryAccessible: row.GithubConnected && row.GithubRepositoryAccess,
			WebhookReady: row.GitlabWebhookReady, MappingsCurrent: row.MappingsCurrent.Bool,
		}
		if row.Type == "gitlab" {
			source.ConnectionID = row.Slug.String()
			source.RepositoryAccessible = source.CredentialActive
		}
		result = append(result, source)
	}
	return result, nil
}

func (*Repository) Targets(ctx context.Context, db postgres.DBTX, org, ds slug.Slug) ([]Target, error) {
	rows, err := dbgen.New(db).ListDataSourceProofTargets(ctx, dbgen.ListDataSourceProofTargetsParams{OrgSlug: org, DataSourceSlug: ds})
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]Target, 0, len(rows))
	for _, row := range rows {
		result = append(result, Target{Project: row.ProjectSlug, Environment: row.EnvironmentSlug, Ref: row.TargetRef, Path: row.PipelinePath})
	}
	return result, nil
}

func (*Repository) Contexts(ctx context.Context, db postgres.DBTX, org, ds slug.Slug) ([]TargetContext, error) {
	rows, err := dbgen.New(db).ListDataSourceProofTargetContexts(ctx, dbgen.ListDataSourceProofTargetContextsParams{OrgSlug: org, DataSourceSlug: ds})
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]TargetContext, 0, len(rows))
	for _, row := range rows {
		result = append(result, TargetContext{Project: row.ProjectSlug, Environment: row.EnvironmentSlug, Ref: row.TargetRef, Directory: row.PipelineDirectory.String})
	}
	return result, nil
}

func (*Repository) Lock(ctx context.Context, db postgres.DBTX, org, ds slug.Slug) error {
	_, err := dbgen.New(db).LockDataSourceForUpdate(ctx, dbgen.LockDataSourceForUpdateParams{OrgSlug: org, DsSlug: ds})
	return mapError(err)
}

func (*Repository) SetControls(ctx context.Context, db postgres.DBTX, org, ds slug.Slug, enabled, checks bool) error {
	_, err := dbgen.New(db).SetDataSourceProofControls(ctx, dbgen.SetDataSourceProofControlsParams{OrgSlug: org, DataSourceSlug: ds, Enabled: enabled, PublishChecks: checks})
	return mapError(err)
}

func (*Repository) RetireTargets(ctx context.Context, db postgres.DBTX, org, ds slug.Slug) error {
	err := dbgen.New(db).RetireDataSourceProofTargets(ctx, dbgen.RetireDataSourceProofTargetsParams{OrgSlug: org, DataSourceSlug: ds})
	return mapError(err)
}

func (*Repository) CreateTarget(ctx context.Context, db postgres.DBTX, org slug.Slug, source Source, target Target) error {
	_, err := dbgen.New(db).CreateProofChangeCandidateMapping(ctx, dbgen.CreateProofChangeCandidateMappingParams{
		OrgSlug: org, ProjectSlug: target.Project, EnvironmentSlug: target.Environment, ProviderConnectionSlug: source.Slug,
		Provider: source.Provider, ProviderConnectionID: source.ConnectionID, RepositoryID: source.RepositoryID, TargetRef: target.Ref, PipelinePath: target.Path,
	})
	return mapError(err)
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if xerror.Is(err, pgx.ErrNoRows) {
		return ErrSourceNotFound.With(err)
	}
	var constraint *pgconn.PgError
	if xerror.As(err, &constraint) && (constraint.Code == "23503" || constraint.Code == "23505") {
		return ErrTargetConflict.With(err)
	}
	return xerror.Unexpected().With(err)
}
