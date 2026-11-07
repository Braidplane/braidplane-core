package e2e

import (
	"encoding/json"
	"testing"
	"time"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/checkpoints"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/postgres/retention"
	"github.com/stretchr/testify/require"
)

func TestStructuralChecksRedeliveryAccessAndRunRetention(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local PostgreSQL")
	}
	ctx := t.Context()
	pool := openPipelineRunsE2EPool(t)
	org, project, env, pipeline, run := slug.Slug("check-history-org"), slug.Slug("check-history-project"), slug.Slug("staging"), slug.Slug("checks"), slug.Slug("attempt-one")
	started := time.Now().UTC().Add(-48 * time.Hour)
	pe := seedPipelineRunSourceEventRun(t, pool, org, project, env, pipeline, run, started, "running")
	initial := pipelinerun.RunTimeline{RunSlug: run.String(), PipelineSlug: pipeline, ProjectSlug: project, EnvironmentSlug: env, Status: "running", StructuralChecks: []pipelinerun.StructuralCheck{
		{ID: "already-durable", Status: pipelinerun.CheckStatusPassed, ReasonCode: "conformant"},
		{ID: "incomplete", Status: pipelinerun.CheckStatusUnavailable, ReasonCode: "evaluating"},
	}}
	raw, err := json.Marshal(initial)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET timeline=$1 WHERE project_environment_id=$2 AND run_slug=$3`, raw, pe, run)
	require.NoError(t, err)
	repo := pipelineruns.New()
	_, err = repo.MarkRunRunning(ctx, pool, org, project, env, pipeline, run.String(), time.Now(), json.RawMessage(`{"status":"running"}`))
	require.ErrorIs(t, err, pipelineruns.ErrRunNotFound, "redelivery cannot reclaim a running attempt")
	row, err := repo.FindRunBySlug(ctx, pool, org, project, env, run.String())
	require.NoError(t, err)
	require.JSONEq(t, string(raw), string(row.Timeline), "worker redelivery cannot erase prior evidence")
	store := checkpoints.New(pool)
	_, err = store.Get(ctx, "other-org", project, env, run.String())
	require.ErrorIs(t, err, pipelinerun.ErrTimelineNotFound)
	_, err = store.Get(ctx, org, "other-project", env, run.String())
	require.ErrorIs(t, err, pipelinerun.ErrTimelineNotFound)
	_, err = store.Get(ctx, org, project, "other-environment", run.String())
	require.ErrorIs(t, err, pipelinerun.ErrTimelineNotFound)
	prune := retention.New()
	deleted, err := prune.DeletePipelineRunsBefore(ctx, pool, org, time.Now())
	require.NoError(t, err)
	require.Zero(t, deleted, "active attempts are not pruned")
	terminal := started.Add(time.Minute)
	pipelinerun.FinalizeStructuralChecks(initial.StructuralChecks, terminal)
	initial.Status = "failed"
	raw, err = json.Marshal(initial)
	require.NoError(t, err)
	_, err = repo.CompleteRun(ctx, pool, org, project, env, run.String(), &pipelineruns.CompleteRunParams{PipelineSlug: pipeline.String(), Status: "failed", StartedAt: started, EndedAt: &terminal, Timeline: raw, IncrementalDiagnostics: json.RawMessage(`{"sources":[],"replay_safety":{"status":"not_applicable"}}`)})
	require.NoError(t, err)
	got, err := store.Get(ctx, org, project, env, run.String())
	require.NoError(t, err)
	require.Equal(t, pipelinerun.CheckStatusPassed, got.StructuralChecks[0].Status)
	require.Equal(t, pipelinerun.CheckStatusError, got.StructuralChecks[1].Status)
	deleted, err = prune.DeletePipelineRunsBefore(ctx, pool, "other-org", time.Now())
	require.NoError(t, err)
	require.Zero(t, deleted)
	deleted, err = prune.DeletePipelineRunsBefore(ctx, pool, org, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	_, err = store.Get(ctx, org, project, env, run.String())
	require.ErrorIs(t, err, pipelinerun.ErrTimelineNotFound, "expired checks remain a missing-evidence gap")
}
