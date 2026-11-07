package checkpoints

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	internalpg "github.com/Braidplane/braidplane-core/postgres/dbgen"
)

type repository struct {
	db postgres.DBTX
}

const checkpointPipelineFormatVersion = 1

type checkpointPipelineEnvelope struct {
	FormatVersion int                               `json:"format_version"`
	Pipeline      json.RawMessage                   `json:"pipeline"`
	LiteralValues []pipelineruntime.LiteralLocation `json:"literal_values,omitempty"`
}

// New returns a postgres-backed TimelineStore.
func New(db postgres.DBTX) pipelinerun.TimelineStore {
	return &repository{db: db}
}

// Get retrieves a run timeline by run slug from the pipeline_runs table.
func (r *repository) Get(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
) (*pipelinerun.RunTimeline, error) {
	q := internalpg.New(r.db)
	timelineJSON, err := q.GetRunTimeline(ctx, internalpg.GetRunTimelineParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
		RunSlug:         runSlug,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, pipelinerun.ErrTimelineNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	var timeline pipelinerun.RunTimeline
	if err := json.Unmarshal(timelineJSON, &timeline); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return &timeline, nil
}

// SaveCheckpoint persists a replay checkpoint for the given run slug.
func (r *repository) SaveCheckpoint(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
	checkpoint pipelinerun.ReplayCheckpoint,
) error {
	pipelineJSON, err := marshalCheckpointPipeline(checkpoint.Pipeline)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	resultsJSON, err := json.Marshal(checkpoint.Results)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	sourceWindowsJSON, err := json.Marshal(checkpoint.IncrementalSourceWindows)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	q := internalpg.New(r.db)
	_, err = q.SaveCheckpoint(ctx, internalpg.SaveCheckpointParams{
		OrgSlug:                    orgSlug,
		ProjectSlug:                projectSlug,
		EnvironmentSlug:            environmentSlug,
		RunSlug:                    runSlug,
		StepSlug:                   checkpoint.StepSlug.String(),
		PipelineSpec:               pipelineJSON,
		CompiledPlan:               checkpoint.CompiledPlan,
		CompiledPlanDigest:         pgtype.Text{String: checkpoint.CompiledPlanDigest, Valid: checkpoint.CompiledPlanDigest != ""},
		DeploymentPlan:             checkpoint.DeploymentPlan,
		DeploymentResolutionDigest: pgtype.Text{String: checkpoint.DeploymentResolutionDigest, Valid: checkpoint.DeploymentResolutionDigest != ""},
		StepResults:                resultsJSON,
		IncrementalSourceWindows:   sourceWindowsJSON,
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return nil
}

// GetCheckpoint retrieves a previously saved replay checkpoint by run slug.
func (r *repository) GetCheckpoint(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
) (*pipelinerun.ReplayCheckpoint, error) {
	q := internalpg.New(r.db)
	row, err := q.GetCheckpoint(ctx, internalpg.GetCheckpointParams{
		OrgSlug:         orgSlug,
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
		RunSlug:         runSlug,
	})
	if err != nil {
		if xerror.Is(err, pgx.ErrNoRows) {
			return nil, pipelinerun.ErrReplayCheckpointNotFound.With(err)
		}
		return nil, xerror.Unexpected().With(err)
	}

	pipeline, err := unmarshalCheckpointPipeline(row.PipelineSpec)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	var results map[slug.Slug]*pipelinerun.StepResult
	if err := json.Unmarshal(row.StepResults, &results); err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	var sourceWindows map[slug.Slug]pipelinerun.IncrementalSourceWindow
	if len(row.IncrementalSourceWindows) > 0 {
		if err := json.Unmarshal(row.IncrementalSourceWindows, &sourceWindows); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
	}

	stepSlug, err := slug.New(row.StepSlug)
	if err != nil {
		return nil, xerror.Wrapf("invalid checkpoint step slug %q: %w", row.StepSlug, err)
	}

	return &pipelinerun.ReplayCheckpoint{
		RunSlug:                    row.RunSlug,
		OrgSlug:                    orgSlug,
		ProjectSlug:                projectSlug,
		EnvironmentSlug:            environmentSlug,
		StepSlug:                   stepSlug,
		Pipeline:                   pipeline,
		CompiledPlan:               row.CompiledPlan,
		CompiledPlanDigest:         row.CompiledPlanDigest.String,
		DeploymentPlan:             row.DeploymentPlan,
		DeploymentResolutionDigest: row.DeploymentResolutionDigest.String,
		Results:                    results,
		IncrementalSourceWindows:   sourceWindows,
	}, nil
}

func marshalCheckpointPipeline(pipeline *pipelinespec.Pipeline) ([]byte, error) {
	raw, err := json.Marshal(pipeline)
	if err != nil {
		return nil, err
	}
	return json.Marshal(checkpointPipelineEnvelope{
		FormatVersion: checkpointPipelineFormatVersion,
		Pipeline:      raw,
		LiteralValues: pipelineruntime.LiteralLocations(pipeline),
	})
}

func unmarshalCheckpointPipeline(raw []byte) (*pipelinespec.Pipeline, error) {
	var envelope checkpointPipelineEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Pipeline == nil {
		return pipelinespec.Load("pipeline.json", raw)
	}
	if envelope.FormatVersion != checkpointPipelineFormatVersion {
		return nil, xerror.Unexpected().Detail("unsupported checkpoint pipeline format")
	}
	pipeline, err := pipelinespec.Load("pipeline.json", envelope.Pipeline)
	if err != nil {
		return nil, err
	}
	if !pipelineruntime.RestoreLiteralLocations(pipeline, envelope.LiteralValues) {
		return nil, xerror.Unexpected().Detail("restore checkpoint literal values")
	}
	return pipeline, nil
}

func (r *repository) ObserveWorker(ctx context.Context, org, project, environment slug.Slug, run string, at time.Time) error {
	rows, err := internalpg.New(r.db).ObservePipelineRunWorker(ctx, internalpg.ObservePipelineRunWorkerParams{
		OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, RunSlug: run,
		ObservedAt: postgres.ToTimestamptz(at),
	})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if rows == 0 {
		return pipelinerun.ErrTimelineNotFound
	}
	return nil
}
