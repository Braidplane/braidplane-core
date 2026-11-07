// Package repositorysetups persists resumable project repository setup state.
package repositorysetups

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

var (
	ErrNotFound  = xerror.NotFound(xerror.Project).Detail("project repository setup was not found")
	ErrBusy      = xerror.New(xerror.KindAlreadyExists, "PROJECT_REPOSITORY_SETUP_BUSY", "project repository setup is leased")
	ErrLeaseLost = xerror.New(xerror.KindAlreadyExists, "PROJECT_REPOSITORY_SETUP_LEASE_LOST", "project repository setup lease was lost")
)

type Row struct {
	Slug        slug.Slug
	Request     json.RawMessage
	Result      json.RawMessage
	Status      string
	FailureCode string
	AbandonedAt *time.Time
}

type Repository struct{}

func New() *Repository { return &Repository{} }

func (*Repository) Create(ctx context.Context, db postgres.DBTX, org, project, setupSlug slug.Slug, request json.RawMessage) (bool, error) {
	rows, err := dbgen.New(db).CreateProjectRepositorySetup(ctx, dbgen.CreateProjectRepositorySetupParams{
		SetupSlug: setupSlug, Request: request, OrgSlug: org, ProjectSlug: project,
	})
	if err != nil {
		return false, xerror.Unexpected().With(err)
	}
	return rows > 0, nil
}

func (*Repository) Get(ctx context.Context, db postgres.DBTX, org, project slug.Slug) (*Row, error) {
	row, err := dbgen.New(db).GetProjectRepositorySetup(ctx, dbgen.GetProjectRepositorySetupParams{OrgSlug: org, ProjectSlug: project})
	if xerror.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound.With(err)
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return setupRow(row.Slug, row.Request, row.Result, row.Status, row.FailureCode, row.AbandonedAt), nil
}

func (*Repository) Claim(ctx context.Context, db postgres.DBTX, org, project, setup slug.Slug, leaseOwner string) (*Row, error) {
	row, err := dbgen.New(db).ClaimProjectRepositorySetup(ctx, dbgen.ClaimProjectRepositorySetupParams{
		LeaseOwner: pgtype.Text{String: leaseOwner, Valid: true}, OrgSlug: org, ProjectSlug: project, SetupSlug: setup,
	})
	if xerror.Is(err, pgx.ErrNoRows) {
		return nil, ErrBusy.With(err)
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return setupRow(row.Slug, row.Request, row.Result, row.Status, row.FailureCode, row.AbandonedAt), nil
}

func (*Repository) Update(ctx context.Context, db postgres.DBTX, org, project slug.Slug, leaseOwner string, row *Row) error {
	rows, err := dbgen.New(db).UpdateProjectRepositorySetup(ctx, dbgen.UpdateProjectRepositorySetupParams{
		Result: row.Result, Status: row.Status, FailureCode: row.FailureCode,
		OrgSlug: org, ProjectSlug: project, LeaseOwner: pgtype.Text{String: leaseOwner, Valid: true},
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rows == 0 {
		return ErrLeaseLost
	}
	return nil
}

func (*Repository) Release(ctx context.Context, db postgres.DBTX, org, project slug.Slug, leaseOwner string) error {
	rows, err := dbgen.New(db).ReleaseProjectRepositorySetup(ctx, dbgen.ReleaseProjectRepositorySetupParams{
		OrgSlug: org, ProjectSlug: project, LeaseOwner: pgtype.Text{String: leaseOwner, Valid: true},
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rows == 0 {
		return ErrLeaseLost
	}
	return nil
}

func setupRow(setup slug.Slug, request, result []byte, status, failure string, abandoned pgtype.Timestamptz) *Row {
	row := &Row{Slug: setup, Request: cloneJSON(request), Result: cloneJSON(result), Status: status, FailureCode: failure}
	if abandoned.Valid {
		row.AbandonedAt = &abandoned.Time
	}
	return row
}

func cloneJSON(value []byte) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func (*Repository) ProjectForSetup(ctx context.Context, db postgres.DBTX, org, setup slug.Slug) (slug.Slug, error) {
	project, err := dbgen.New(db).FindProjectForRepositorySetup(ctx, dbgen.FindProjectForRepositorySetupParams{OrgSlug: org, SetupSlug: setup})
	if xerror.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound.With(err)
	}
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	return project, nil
}

func (*Repository) GetAttempt(ctx context.Context, db postgres.DBTX, org, project, setup slug.Slug) (*Row, error) {
	row, err := dbgen.New(db).GetProjectRepositorySetupAttempt(ctx, dbgen.GetProjectRepositorySetupAttemptParams{OrgSlug: org, ProjectSlug: project, SetupSlug: setup})
	if xerror.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound.With(err)
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return setupRow(row.Slug, row.Request, row.Result, row.Status, row.FailureCode, row.AbandonedAt), nil
}

func (*Repository) Abandon(ctx context.Context, db postgres.DBTX, org, project, setup slug.Slug, leaseOwner string) (*Row, error) {
	row, err := dbgen.New(db).AbandonProjectRepositorySetup(ctx, dbgen.AbandonProjectRepositorySetupParams{
		OrgSlug: org, ProjectSlug: project, SetupSlug: setup, LeaseOwner: pgtype.Text{String: leaseOwner, Valid: true},
	})
	if xerror.Is(err, pgx.ErrNoRows) {
		return nil, ErrLeaseLost.With(err)
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return setupRow(row.Slug, row.Request, row.Result, row.Status, row.FailureCode, row.AbandonedAt), nil
}
