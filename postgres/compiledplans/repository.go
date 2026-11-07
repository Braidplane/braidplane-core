package compiledplans

import (
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

var ErrNotFound = xerror.NotFound(xerror.Pipeline).Detail("compiled pipeline plan was not found")

type Snapshot struct {
	PipelineSlug     slug.Slug
	Digest           string
	AuthoredSpecHash string
	FormatVersion    int
	Materialization  []byte
	CreatedAt        time.Time
}

type CreateParams struct {
	PipelineSlug     slug.Slug
	Digest           string
	AuthoredSpecHash string
	FormatVersion    int
	Materialization  []byte
}

type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

// Create returns ErrNotFound when the scope is unavailable or the digest already
// exists. A following Find reads that exact identity without aborting the transaction.
func (*Repository) Create(ctx context.Context, db postgres.DBTX, org, project slug.Slug, params CreateParams) (*Snapshot, error) {
	formatVersion, err := formatVersion(params.FormatVersion)
	if err != nil {
		return nil, err
	}
	row, err := dbgen.New(db).CreatePipelineCompiledPlan(ctx, dbgen.CreatePipelineCompiledPlanParams{
		OrgSlug: org, ProjectSlug: project, PipelineSlug: params.PipelineSlug.String(), Digest: params.Digest,
		AuthoredSpecHash: params.AuthoredSpecHash, FormatVersion: formatVersion,
		Materialization: params.Materialization,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return snapshot(slug.Slug(row.PipelineSlug), row.Digest, row.AuthoredSpecHash, row.FormatVersion, row.Materialization, row.CreatedAt.Time), nil
}

func (*Repository) Find(ctx context.Context, db postgres.DBTX, org, project, pipeline slug.Slug, digest string) (*Snapshot, error) {
	row, err := dbgen.New(db).FindPipelineCompiledPlan(ctx, dbgen.FindPipelineCompiledPlanParams{
		OrgSlug: org, ProjectSlug: project, PipelineSlug: pipeline.String(), Digest: digest,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return snapshot(slug.Slug(row.PipelineSlug), row.Digest, row.AuthoredSpecHash, row.FormatVersion, row.Materialization, row.CreatedAt.Time), nil
}

func mapError(err error) error {
	if xerror.Is(err, pgx.ErrNoRows) {
		return ErrNotFound.With(err)
	}
	return xerror.Unexpected().With(err)
}

func formatVersion(value int) (int32, error) {
	if int64(value) > math.MaxInt32 || int64(value) < math.MinInt32 {
		return 0, xerror.InvalidInput(xerror.Pipeline).Detail("compiled plan format version is outside the database integer range")
	}
	return int32(value), nil
}

func snapshot(pipelineSlug slug.Slug, digest, authoredSpecHash string, formatVersion int32, materialization []byte, createdAt time.Time) *Snapshot {
	return &Snapshot{
		PipelineSlug: pipelineSlug, Digest: digest, AuthoredSpecHash: authoredSpecHash,
		FormatVersion: int(formatVersion), Materialization: materialization, CreatedAt: createdAt,
	}
}
