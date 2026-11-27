package pipelineruns

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

var (
	ErrExecutionPaused          = xerror.New(xerror.KindAlreadyExists, "PIPELINE_EXECUTION_PAUSED", "Pipeline execution is paused")
	ErrExecutionControlRevision = xerror.New(xerror.KindAlreadyExists, "PIPELINE_EXECUTION_CONTROL_REVISION_CHANGED", "Pipeline execution state changed. Reload its status before trying again.")
)

func mapControlWriteError(err error) error {
	var pgErr *pgconn.PgError
	if xerror.As(err, &pgErr) && pgErr.Code == "40001" {
		return ErrExecutionControlRevision.With(err)
	}
	return xerror.Unexpected().With(err)
}

func mapRunWriteError(err error) error {
	var pgErr *pgconn.PgError
	if xerror.As(err, &pgErr) {
		switch pgErr.Code {
		case "BP278":
			return ErrExecutionPaused.With(err)
		case "BP279":
			return ErrExecutionControlRevision.With(err)
		}
	}
	return err
}

type ExecutionScope struct {
	Org, Project, Environment, Pipeline slug.Slug
}

type ExecutionControl struct {
	Paused         bool
	Revision       int64
	ChangedAt      *time.Time
	ChangedByType  *string
	ChangedBySlug  *string
	QueuedCount    int64
	ActiveCount    int64
	FinishedCount  int64
	UncertainCount int64
	Attempts       []ExecutionControlAttempt
}

type ExecutionControlAttempt struct {
	RunSlug             string     `json:"run_slug"`
	SourceType          string     `json:"source_type"`
	Phase               string     `json:"phase"`
	Status              string     `json:"status"`
	StartedAt           time.Time  `json:"started_at"`
	EndedAt             *time.Time `json:"ended_at"`
	LastObservedAt      *time.Time `json:"last_observed_at"`
	HasWorkerJob        bool       `json:"has_worker_job"`
	KnownCompletedWrite bool       `json:"known_completed_write"`
	OutcomeUncertain    bool       `json:"outcome_uncertain"`
}

func EnsureExecutionControl(ctx context.Context, db postgres.DBTX, scope ExecutionScope) error {
	err := dbgen.New(db).EnsurePipelineExecutionControl(ctx, dbgen.EnsurePipelineExecutionControlParams{
		OrgSlug: scope.Org, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment, PipelineSlug: scope.Pipeline,
	})
	if err != nil {
		return mapControlWriteError(err)
	}
	return nil
}

func SetExecutionControl(ctx context.Context, db postgres.DBTX, scope ExecutionScope, expectedRevision int64, paused bool, actor model.EventActor) (bool, error) {
	n, err := dbgen.New(db).SetPipelineExecutionControl(ctx, dbgen.SetPipelineExecutionControlParams{
		OrgSlug: scope.Org, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment, PipelineSlug: scope.Pipeline,
		ExpectedRevision: expectedRevision, Paused: paused, ActorType: postgres.ToText(actor.Type.String()),
		ActorSlug: postgres.OptionalSlugToText(actor.Slug),
	})
	if err != nil {
		return false, mapControlWriteError(err)
	}
	return n == 1, nil
}

func InspectExecutionControl(ctx context.Context, db postgres.DBTX, scope ExecutionScope) (*ExecutionControl, error) {
	row, err := dbgen.New(db).InspectPipelineExecutionControl(ctx, dbgen.InspectPipelineExecutionControlParams{
		OrgSlug: scope.Org, ProjectSlug: scope.Project, EnvironmentSlug: scope.Environment, PipelineSlug: scope.Pipeline,
	})
	if xerror.Is(err, pgx.ErrNoRows) {
		return nil, xerror.NotFound(xerror.Pipeline).With(err)
	}
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	control := &ExecutionControl{
		Paused: row.Paused, Revision: row.Revision, ChangedAt: postgres.FromNullableTimestamptz(row.ChangedAt),
		ChangedByType: postgres.FromNullableText(row.ChangedByType), ChangedBySlug: postgres.FromNullableText(row.ChangedBySlug),
		QueuedCount: row.QueuedCount, ActiveCount: row.ActiveCount, FinishedCount: row.FinishedCount, UncertainCount: row.UncertainCount,
	}
	if err := json.Unmarshal(row.Attempts, &control.Attempts); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return control, nil
}
