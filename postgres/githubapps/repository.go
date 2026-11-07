package githubapps

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

var ErrNotFound = xerror.NotFound(xerror.Organization).Detail("GitHub App connection was not found")

type Intent struct {
	RepositorySetupSlug   slug.Slug
	RequestedAccountLogin string
	ID                    int
	OrganizationID        int32
	Organization          slug.Slug
	Actor                 string
	ExpiresAt             time.Time
	Consumed              bool
}

type Installation struct {
	ID                  int64
	OrganizationID      int32
	InstallationID      int64
	AccountID           int64
	AccountLogin        string
	AccountType         string
	RepositorySelection string
	SuspendedAt         *time.Time
	DeletedAt           *time.Time
}

type RepositoryBinding struct {
	ID         int64
	FullName   string
	OwnerLogin string
	Name       string
}

type VerifiedInstallation struct {
	InstallationID      int64
	AccountID           int64
	AccountLogin        string
	AccountType         string
	RepositorySelection string
	Repositories        []RepositoryBinding
}

type Repository interface {
	CreateIntent(context.Context, postgres.DBTX, slug.Slug, string, string, time.Time, slug.Slug, string) error
	GetIntent(context.Context, postgres.DBTX, string) (Intent, error)
	LockIntent(context.Context, postgres.DBTX, string) (Intent, error)
	ConsumeIntent(context.Context, postgres.DBTX, int) error
	UpsertInstallation(context.Context, postgres.DBTX, Intent, VerifiedInstallation) (int64, error)
	UpsertRepository(context.Context, postgres.DBTX, int32, int64, RepositoryBinding) error
	RemoveMissingRepositories(context.Context, postgres.DBTX, int32, int64, []int64) error
	GetInstallation(context.Context, postgres.DBTX, slug.Slug) (*Installation, error)
	ListRepositories(context.Context, postgres.DBTX, slug.Slug) ([]RepositoryBinding, error)
	Disconnect(context.Context, postgres.DBTX, slug.Slug) error
	OrganizationForInstallation(context.Context, postgres.DBTX, int64) (slug.Slug, error)
	Resolve(context.Context, postgres.DBTX, int64, int64) (slug.Slug, error)
	UpdateLifecycle(context.Context, postgres.DBTX, string, int64) error
	AddRepository(context.Context, postgres.DBTX, int64, RepositoryBinding) error
	RemoveRepository(context.Context, postgres.DBTX, int64, int64) error
}

type repository struct{}

func New() Repository { return &repository{} }

func (*repository) CreateIntent(ctx context.Context, db postgres.DBTX, org slug.Slug, actor, hash string, expires time.Time, setupSlug slug.Slug, requestedAccountLogin string) error {
	_, err := dbgen.New(db).CreateGitHubAppInstallIntent(ctx, dbgen.CreateGitHubAppInstallIntentParams{
		OrganizationSlug: org, ActorSlug: actor, StateHash: hash,
		RepositorySetupSlug:   pgtype.Text{String: setupSlug.String(), Valid: setupSlug != ""},
		RequestedAccountLogin: pgtype.Text{String: requestedAccountLogin, Valid: requestedAccountLogin != ""},
		ExpiresAt:             pgtype.Timestamptz{Time: expires, Valid: true},
	})
	if xerror.Is(err, pgx.ErrNoRows) {
		return ErrNotFound.With(err)
	}
	return err
}

func (*repository) LockIntent(ctx context.Context, db postgres.DBTX, hash string) (Intent, error) {
	row, err := dbgen.New(db).GetGitHubAppInstallIntentForUpdate(ctx, hash)
	if xerror.Is(err, pgx.ErrNoRows) {
		return Intent{}, ErrNotFound.With(err)
	}
	if err != nil {
		return Intent{}, err
	}
	return Intent{
		RepositorySetupSlug: slug.Slug(row.RepositorySetupSlug.String), RequestedAccountLogin: row.RequestedAccountLogin.String, ID: row.ID, OrganizationID: row.OrganizationID, Organization: row.OrganizationSlug,
		Actor: row.ActorSlug, ExpiresAt: row.ExpiresAt.Time, Consumed: row.ConsumedAt.Valid,
	}, nil
}

func (*repository) GetIntent(ctx context.Context, db postgres.DBTX, hash string) (Intent, error) {
	row, err := dbgen.New(db).GetGitHubAppInstallIntent(ctx, hash)
	if xerror.Is(err, pgx.ErrNoRows) {
		return Intent{}, ErrNotFound.With(err)
	}
	if err != nil {
		return Intent{}, err
	}
	return Intent{RepositorySetupSlug: slug.Slug(row.RepositorySetupSlug.String), RequestedAccountLogin: row.RequestedAccountLogin.String, ID: row.ID, OrganizationID: row.OrganizationID, Organization: row.OrganizationSlug, Actor: row.ActorSlug, ExpiresAt: row.ExpiresAt.Time, Consumed: row.ConsumedAt.Valid}, nil
}

func (*repository) ConsumeIntent(ctx context.Context, db postgres.DBTX, id int) error {
	_, err := dbgen.New(db).ConsumeGitHubAppInstallIntent(ctx, id)
	if xerror.Is(err, pgx.ErrNoRows) {
		return ErrNotFound.With(err)
	}
	return err
}

func (*repository) UpsertInstallation(ctx context.Context, db postgres.DBTX, intent Intent, verified VerifiedInstallation) (int64, error) {
	installation, err := dbgen.New(db).UpsertGitHubAppInstallation(ctx, dbgen.UpsertGitHubAppInstallationParams{
		OrganizationID: intent.OrganizationID, InstallationID: verified.InstallationID,
		AccountID: verified.AccountID, AccountLogin: verified.AccountLogin, AccountType: verified.AccountType,
		RepositorySelection: verified.RepositorySelection,
		AllowReplacement:    intent.RepositorySetupSlug == "" && intent.RequestedAccountLogin == "",
	})
	if xerror.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound.With(err).Detail("GitHub installation changed during authorization")
	}
	return int64(installation.ID), err
}

func (*repository) UpsertRepository(ctx context.Context, db postgres.DBTX, orgID int32, installationID int64, repo RepositoryBinding) error {
	return dbgen.New(db).UpsertGitHubAppRepository(ctx, dbgen.UpsertGitHubAppRepositoryParams{OrganizationID: orgID, InstallationID: installationID, RepositoryID: repo.ID, FullName: repo.FullName, OwnerLogin: repo.OwnerLogin, Name: repo.Name})
}

func (*repository) RemoveMissingRepositories(ctx context.Context, db postgres.DBTX, orgID int32, installationID int64, ids []int64) error {
	return dbgen.New(db).RemoveMissingGitHubAppRepositories(ctx, dbgen.RemoveMissingGitHubAppRepositoriesParams{OrganizationID: orgID, InstallationID: installationID, RepositoryIds: ids})
}

func (*repository) GetInstallation(ctx context.Context, db postgres.DBTX, org slug.Slug) (*Installation, error) {
	row, err := dbgen.New(db).GetGitHubAppInstallationByOrg(ctx, org)
	if xerror.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // No active binding is an expected read result.
	}
	if err != nil {
		return nil, err
	}
	return &Installation{ID: int64(row.ID), OrganizationID: row.OrganizationID, InstallationID: row.InstallationID, AccountID: row.AccountID, AccountLogin: row.AccountLogin, AccountType: row.AccountType, RepositorySelection: row.RepositorySelection, SuspendedAt: optionalTime(row.SuspendedAt), DeletedAt: optionalTime(row.DeletedAt)}, nil
}

func (*repository) ListRepositories(ctx context.Context, db postgres.DBTX, org slug.Slug) ([]RepositoryBinding, error) {
	repos, err := dbgen.New(db).ListGitHubAppRepositoriesByOrg(ctx, org)
	if err != nil {
		return nil, err
	}
	out := make([]RepositoryBinding, len(repos))
	for i, repo := range repos {
		out[i] = RepositoryBinding{ID: repo.RepositoryID, FullName: repo.FullName, OwnerLogin: repo.OwnerLogin, Name: repo.Name}
	}
	return out, nil
}

func (*repository) Disconnect(ctx context.Context, db postgres.DBTX, org slug.Slug) error {
	rows, err := dbgen.New(db).DisconnectGitHubAppInstallation(ctx, org)
	if err == nil && rows == 0 {
		return ErrNotFound
	}
	return err
}

func (*repository) OrganizationForInstallation(ctx context.Context, db postgres.DBTX, installationID int64) (slug.Slug, error) {
	row, err := dbgen.New(db).GetGitHubAppOrganizationByInstallation(ctx, installationID)
	if xerror.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound.With(err)
	}
	return row, err
}

func (*repository) Resolve(ctx context.Context, db postgres.DBTX, installationID, repositoryID int64) (slug.Slug, error) {
	row, err := dbgen.New(db).ResolveGitHubAppRepositoryBinding(ctx, dbgen.ResolveGitHubAppRepositoryBindingParams{
		ProviderInstallationID: installationID, RepositoryID: repositoryID,
	})
	if xerror.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound.With(err)
	}
	return row.OrganizationSlug, err
}

func (*repository) UpdateLifecycle(ctx context.Context, db postgres.DBTX, action string, installationID int64) error {
	rows, err := dbgen.New(db).UpdateGitHubAppInstallationLifecycle(ctx, dbgen.UpdateGitHubAppInstallationLifecycleParams{Action: action, ProviderInstallationID: installationID})
	if err == nil && rows == 0 {
		return ErrNotFound
	}
	return err
}

func (*repository) AddRepository(ctx context.Context, db postgres.DBTX, installationID int64, repo RepositoryBinding) error {
	rows, err := dbgen.New(db).AddGitHubAppRepositoryByProviderInstallation(ctx, dbgen.AddGitHubAppRepositoryByProviderInstallationParams{ProviderInstallationID: installationID, RepositoryID: repo.ID, FullName: repo.FullName, OwnerLogin: repo.OwnerLogin, Name: repo.Name})
	if err == nil && rows == 0 {
		return ErrNotFound
	}
	return err
}

func (*repository) RemoveRepository(ctx context.Context, db postgres.DBTX, installationID, repositoryID int64) error {
	rows, err := dbgen.New(db).RemoveGitHubAppRepositoryByProviderInstallation(ctx, dbgen.RemoveGitHubAppRepositoryByProviderInstallationParams{ProviderInstallationID: installationID, RepositoryID: repositoryID})
	if err == nil && rows == 0 {
		return ErrNotFound
	}
	return err
}

func optionalTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}
