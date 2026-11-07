package deploymentresolutions

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
	"github.com/jackc/pgx/v5"
)

var ErrNotFound = xerror.NotFound(xerror.PipelineDeployment).Detail("deployment resolution was not found")

type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

func (*Repository) Create(ctx context.Context, db postgres.DBTX, org, project, environment, pipeline slug.Slug, compiled, digest string, materialization []byte) ([]byte, error) {
	row, err := dbgen.New(db).CreatePipelineDeploymentResolution(ctx, dbgen.CreatePipelineDeploymentResolutionParams{
		OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, PipelineSlug: pipeline.String(), CompiledDigest: compiled, Digest: digest, Materialization: materialization,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return row.Materialization, nil
}

func (*Repository) Find(ctx context.Context, db postgres.DBTX, org, project, environment, pipeline slug.Slug, digest string) ([]byte, error) {
	row, err := dbgen.New(db).FindPipelineDeploymentResolution(ctx, dbgen.FindPipelineDeploymentResolutionParams{
		OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, PipelineSlug: pipeline.String(), Digest: digest,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return row.Materialization, nil
}

func mapError(err error) error {
	if xerror.Is(err, pgx.ErrNoRows) {
		return ErrNotFound.With(err)
	}
	return xerror.Unexpected().With(err)
}
