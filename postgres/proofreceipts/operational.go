package proofreceipts

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
	"github.com/jackc/pgx/v5"
)

type OperationalRun struct {
	Pipeline         slug.Slug
	Run              slug.Slug
	Attempt          int
	Status           string
	Timeline         json.RawMessage
	EndedAt          *time.Time
	SpecHash         string
	VersionCommit    string
	ResolutionDigest string
	Resolution       []byte
	Settings         json.RawMessage
}

type OperationalRepository interface {
	FindOperationalRun(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*OperationalRun, error)
}

func (*repository) FindOperationalRun(ctx context.Context, db postgres.DBTX, org, project, environment, run slug.Slug) (*OperationalRun, error) {
	row, err := dbgen.New(db).FindProofOperationalRun(ctx, dbgen.FindProofOperationalRunParams{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: run.String()})
	if xerror.Is(err, pgx.ErrNoRows) {
		return nil, ErrReceiptNotFound.With(err)
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return &OperationalRun{
		Pipeline: slug.Slug(row.PipelineSlug), Run: slug.Slug(row.RunSlug), Attempt: int(row.SourceAttemptNumber), Status: row.Status, Timeline: row.Timeline,
		EndedAt: postgres.FromNullableTimestamptz(row.EndedAt), SpecHash: row.SpecHash, VersionCommit: row.GitCommitSha.String, ResolutionDigest: row.ResolutionDigest, Resolution: row.Materialization, Settings: row.Settings,
	}, nil
}
