package gitlabproofwebhooks

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

var (
	ErrNotFound        = xerror.NotFound(xerror.DataSource).Detail("GitLab Proof webhook binding was not found")
	ErrSetupInProgress = xerror.AlreadyExists(xerror.DataSource).Detail("GitLab Proof webhook setup is already in progress")
)

type Binding struct {
	Organization          slug.Slug
	DataSource            slug.Slug
	ProjectID             int64
	HookID                int64
	Status                string
	RouteKeyHash          string
	EncryptedSigningToken []byte
}

type Repository interface {
	GetByDataSource(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (Binding, error)
	GetByRoute(context.Context, postgres.DBTX, string) (Binding, error)
	ClaimSetup(context.Context, postgres.DBTX, Binding) error
	Activate(context.Context, postgres.DBTX, Binding) error
	FailSetup(context.Context, postgres.DBTX, Binding) error
}

type repository struct{}

func New() Repository { return &repository{} }

func (*repository) GetByDataSource(ctx context.Context, db postgres.DBTX, org, dataSource slug.Slug) (Binding, error) {
	row, err := dbgen.New(db).GetGitLabProofWebhookByDataSource(ctx, dbgen.GetGitLabProofWebhookByDataSourceParams{OrganizationSlug: org, DataSourceSlug: dataSource})
	if xerror.Is(err, pgx.ErrNoRows) {
		return Binding{}, ErrNotFound.With(err)
	}
	if err != nil {
		return Binding{}, err
	}
	return Binding{Organization: row.OrganizationSlug, DataSource: row.DataSourceSlug, ProjectID: row.ProjectID, HookID: row.HookID, Status: row.Status, RouteKeyHash: row.RouteKeyHash, EncryptedSigningToken: row.EncryptedSigningToken}, nil
}

func (*repository) GetByRoute(ctx context.Context, db postgres.DBTX, routeKeyHash string) (Binding, error) {
	row, err := dbgen.New(db).GetGitLabProofWebhookByRoute(ctx, routeKeyHash)
	if xerror.Is(err, pgx.ErrNoRows) {
		return Binding{}, ErrNotFound.With(err)
	}
	if err != nil {
		return Binding{}, err
	}
	return Binding{Organization: row.OrganizationSlug, DataSource: row.DataSourceSlug, ProjectID: row.ProjectID, HookID: row.HookID, Status: row.Status, RouteKeyHash: row.RouteKeyHash, EncryptedSigningToken: row.EncryptedSigningToken}, nil
}

func (*repository) ClaimSetup(ctx context.Context, db postgres.DBTX, binding Binding) error {
	_, err := dbgen.New(db).ClaimGitLabProofWebhookSetup(ctx, dbgen.ClaimGitLabProofWebhookSetupParams{
		OrganizationSlug: binding.Organization, DataSourceSlug: binding.DataSource,
		ProjectID: binding.ProjectID, HookID: binding.HookID, RouteKeyHash: binding.RouteKeyHash,
		EncryptedSigningToken: binding.EncryptedSigningToken,
	})
	if xerror.Is(err, pgx.ErrNoRows) {
		return ErrSetupInProgress.With(err)
	}
	return err
}

func (*repository) Activate(ctx context.Context, db postgres.DBTX, binding Binding) error {
	_, err := dbgen.New(db).ActivateGitLabProofWebhook(ctx, dbgen.ActivateGitLabProofWebhookParams{
		HookID: binding.HookID, OrganizationSlug: binding.Organization,
		DataSourceSlug: binding.DataSource, RouteKeyHash: binding.RouteKeyHash,
	})
	if xerror.Is(err, pgx.ErrNoRows) {
		return ErrNotFound.With(err)
	}
	return err
}

func (*repository) FailSetup(ctx context.Context, db postgres.DBTX, binding Binding) error {
	return dbgen.New(db).FailGitLabProofWebhookSetup(ctx, dbgen.FailGitLabProofWebhookSetupParams{
		OrganizationSlug: binding.Organization, DataSourceSlug: binding.DataSource,
		RouteKeyHash: binding.RouteKeyHash,
	})
}
