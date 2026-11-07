package e2e

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/stretchr/testify/require"
)

func TestPreviousSuccessSelection(t *testing.T) {
	pool := openPipelineRunsE2EPool(t)
	ctx := t.Context()
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	org, project, environment, pipeline := slug.Slug("prior-success-org"), slug.Slug("prior-project"), slug.Slug("prod"), slug.Slug("orders")
	scope := seedPipelineRunSourceEventRun(t, pool, org, project, environment, pipeline, "original", at.Add(-time.Hour), "succeeded")
	var version int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT pipeline_version_id FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='original'`, scope).Scan(&version))
	repo := pipelineruns.New()
	prior, err := repo.PreviousSuccess(ctx, pool, org, project, environment, pipeline, version, at)
	require.NoError(t, err)
	require.Equal(t, "original", prior.RunSlug)
	require.Equal(t, at.Add(-time.Hour), prior.FinishedAt)

	// Each immutable attempt is inserted independently.
	for index, tc := range []struct {
		name, status     string
		ended            time.Time
		replay, backfill *string
		attempt          int
		want             string
	}{
		{name: "failure", status: "failed", ended: at.Add(-time.Minute), attempt: 1, want: "original"},
		{name: "running", status: "running", ended: at.Add(-time.Minute), attempt: 1, want: "original"},
		{name: "future success", status: "succeeded", ended: at.Add(time.Minute), attempt: 1, want: "original"},
		{name: "equal cutoff", status: "succeeded", ended: at, attempt: 1, want: "original"},
		{name: "recovery", status: "succeeded", ended: at.Add(-time.Minute), replay: stringPointer("original"), attempt: 1, want: "original"},
		{name: "backfill", status: "succeeded", ended: at.Add(-time.Minute), backfill: stringPointer("backfill"), attempt: 1, want: "original"},
		{name: "successful retry", status: "succeeded", ended: at.Add(-time.Minute), replay: stringPointer("original"), attempt: 2, want: "candidate"},
		{name: "ordinary success", status: "succeeded", ended: at.Add(-time.Minute), attempt: 1, want: "candidate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := fmt.Sprintf("candidate-%d", index)
			_, err := pool.Exec(ctx, `INSERT INTO pipeline_runs (project_id, project_environment_id, pipeline_version_id, pipeline_version_spec_hash, api_version, pipeline_slug, run_slug, source_environment_slug, status, duration_ms, step_count, step_slug, started_at, ended_at, replay_of_run_slug, backfill_series_slug, source_attempt_number, timeline)
 SELECT project_id, project_environment_id, pipeline_version_id, pipeline_version_spec_hash, api_version, pipeline_slug, $2, source_environment_slug, $3, 10, 1, step_slug, $4, $4, $5, $6, $7, '{}'::jsonb FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='original'`, scope, candidate, tc.status, tc.ended, tc.replay, tc.backfill, tc.attempt)
			require.NoError(t, err)
			got, err := repo.PreviousSuccess(ctx, pool, org, project, environment, pipeline, version, at)
			require.NoError(t, err)
			want := tc.want
			if want == "candidate" {
				want = candidate
			}
			require.Equal(t, want, got.RunSlug)
		})
	}
	for _, tc := range []struct {
		name                                string
		org, project, environment, pipeline slug.Slug
		version                             int64
		before                              time.Time
	}{
		{"tenant", "other-org", project, environment, pipeline, version, at},
		{"project", org, "other-project", environment, pipeline, version, at},
		{"environment", org, project, "dev", pipeline, version, at},
		{"pipeline", org, project, environment, "other-pipeline", version, at},
		{"version", org, project, environment, pipeline, version + 999999, at},
		{"first run", org, project, environment, pipeline, version, at.Add(-2 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.PreviousSuccess(ctx, pool, tc.org, tc.project, tc.environment, tc.pipeline, tc.version, tc.before)
			require.True(t, xerror.Is(err, pipelineruns.ErrRunNotFound))
			require.Nil(t, got)
		})
	}
	// The same names in a second organization cannot see this organization's successes.
	otherScope := seedPipelineRunSourceEventRun(t, pool, "prior-other-org", project, environment, pipeline, "other", at, "failed")
	var otherVersion int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT pipeline_version_id FROM pipeline_runs WHERE project_environment_id=$1`, otherScope).Scan(&otherVersion))
	got, err := repo.PreviousSuccess(ctx, pool, "prior-other-org", project, environment, pipeline, otherVersion, at.Add(time.Hour))
	require.True(t, xerror.Is(err, pipelineruns.ErrRunNotFound))
	require.Nil(t, got)
}

func stringPointer(value string) *string { return &value }

func TestRunExecutionContextCannotBeReplaced(t *testing.T) {
	pool := openPipelineRunsE2EPool(t)
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	org, project, environment, pipeline := slug.Slug("context-pin-org"), slug.Slug("context-project"), slug.Slug("prod"), slug.Slug("orders")
	seedPipelineRunSourceEventRun(t, pool, org, project, environment, pipeline, "pinned", at, "running")
	repo := pipelineruns.New()
	params := &pipelineruns.CompleteRunParams{PipelineSlug: pipeline.String(), Status: "running", StartedAt: at, Timeline: json.RawMessage(`{"execution_context":{"at":"2026-10-22T12:00:00Z"}}`)}

	type outcome struct {
		timeline json.RawMessage
		err      error
	}
	outcomes := make(chan outcome, 2)
	start := make(chan struct{})
	for _, timeline := range []json.RawMessage{params.Timeline, json.RawMessage(`{"execution_context":{"at":"2026-10-22T13:00:00Z"}}`)} {
		candidate := *params
		candidate.Timeline = timeline
		go func() {
			<-start
			_, err := repo.CompleteRun(t.Context(), pool, org, project, environment, "pinned", &candidate)
			outcomes <- outcome{candidate.Timeline, err}
		}()
	}
	close(start)
	var selected json.RawMessage
	successes, conflicts := 0, 0
	for range 2 {
		result := <-outcomes
		if result.err == nil {
			successes++
			selected = result.timeline
		} else {
			require.True(t, xerror.Is(result.err, pipelineruns.ErrRunNotFound), "%v", result.err)
			conflicts++
		}
	}
	require.Equal(t, 1, successes, "one concurrent selection must win")
	require.Equal(t, 1, conflicts, "a conflicting selection must fail before dispatch")

	// A service-owned lifecycle refusal without context retains the original snapshot.
	params.Timeline = json.RawMessage(`{"status":"failed"}`)
	params.Status = "failed"
	_, err := repo.CompleteRun(t.Context(), pool, org, project, environment, "pinned", params)
	require.NoError(t, err)
	run, err := repo.FindRunBySlug(t.Context(), pool, org, project, environment, "pinned")
	require.NoError(t, err)
	var expected map[string]any
	require.NoError(t, json.Unmarshal(selected, &expected))
	expected["status"] = "failed"
	encoded, err := json.Marshal(expected)
	require.NoError(t, err)
	require.JSONEq(t, string(encoded), string(run.Timeline))
}
