package e2e

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/checkpoints"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/stretchr/testify/require"
)

func TestProgressRejectsDuplicateOutOfOrderAndConcurrentRevisions(t *testing.T) {
	pool := openPipelineRunsE2EPool(t)
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	org, project, env, pipeline := slug.Slug("progress-org"), slug.Slug("progress-project"), slug.Slug("staging"), slug.Slug("orders")
	seedPipelineRunSourceEventRun(t, pool, org, project, env, pipeline, "progress", at, "running")
	repo := pipelineruns.New()
	params := &pipelineruns.CompleteRunParams{
		PipelineSlug: pipeline.String(), Status: "running", StartedAt: at,
		Timeline: json.RawMessage(`{"revision":1,"compiled_plan_digest":"sha256:compiled","deployment_resolution_digest":"sha256:deployment","events":[{"type":"run_started"}]}`),
	}
	_, err := repo.CompleteRun(t.Context(), pool, org, project, env, "progress", params)
	require.NoError(t, err)
	observations := checkpoints.New(pool)
	observed := at.Add(5 * time.Second)
	require.NoError(t, observations.ObserveWorker(t.Context(), org, project, env, "progress", observed))
	require.NoError(t, observations.ObserveWorker(t.Context(), org, project, env, "progress", at))
	for _, scope := range [][3]slug.Slug{{"other", project, env}, {org, "other", env}, {org, project, "other"}} {
		require.ErrorIs(t, observations.ObserveWorker(t.Context(), scope[0], scope[1], scope[2], "progress", observed), pipelinerun.ErrTimelineNotFound)
	}
	for _, timeline := range []string{
		`{"revision":1,"compiled_plan_digest":"sha256:compiled","deployment_resolution_digest":"sha256:deployment","events":[{"type":"run_started"}]}`,
		`{"revision":3,"events":[{"type":"run_started"}]}`,
		`{"revision":2,"events":[]}`,
		`{"revision":2,"events":[{"type":"run_started"}]}`,
		`{"revision":2,"compiled_plan_digest":"sha256:changed","deployment_resolution_digest":"sha256:deployment","events":[{"type":"run_started"}]}`,
		`{"revision":2,"compiled_plan_digest":"sha256:compiled","deployment_resolution_digest":"sha256:changed","events":[{"type":"run_started"}]}`,
		`{"events":[{"type":"run_started"}]}`,
	} {
		params.Timeline = json.RawMessage(timeline)
		_, err := repo.CompleteRun(t.Context(), pool, org, project, env, "progress", params)
		require.ErrorIs(t, err, pipelineruns.ErrRunNotFound)
	}
	outcomes := make(chan error, 2)
	start := make(chan struct{})
	for _, step := range []string{"fetch", "other"} {
		candidate := *params
		candidate.Timeline = json.RawMessage(`{"revision":2,"compiled_plan_digest":"sha256:compiled","deployment_resolution_digest":"sha256:deployment","events":[{"type":"run_started"},{"type":"step_started","step":"` + step + `"}]}`)
		go func() {
			<-start
			_, err := repo.CompleteRun(t.Context(), pool, org, project, env, "progress", &candidate)
			outcomes <- err
		}()
	}
	close(start)
	successes := 0
	for range 2 {
		if err := <-outcomes; err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, pipelineruns.ErrRunNotFound)
		}
	}
	require.Equal(t, 1, successes)
	run, err := repo.FindRunBySlug(t.Context(), pool, org, project, env, "progress")
	require.NoError(t, err)
	var timeline map[string]any
	require.NoError(t, json.Unmarshal(run.Timeline, &timeline))
	var durable pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal(run.Timeline, &durable))
	require.True(t, observed.Equal(*durable.WorkerObservedAt), "boundary persistence and delayed heartbeats cannot regress the worker report")
	timeline["revision"] = 3
	events := timeline["events"].([]any)
	timeline["events"] = append(events, map[string]any{"type": "run_completed", "status": "failed"})
	params.Timeline, err = json.Marshal(timeline)
	require.NoError(t, err)
	params.Status = "failed"
	params.EndedAt = &at
	// An exact identity is still scoped to its organization, project and environment.
	for _, scope := range [][3]slug.Slug{{"other", project, env}, {org, "other", env}, {org, project, "other"}} {
		_, err := repo.CompleteRun(t.Context(), pool, scope[0], scope[1], scope[2], "progress", params)
		require.ErrorIs(t, err, pipelineruns.ErrRunNotFound)
	}
	_, err = repo.CompleteRun(t.Context(), pool, org, project, env, "progress", params)
	require.NoError(t, err)
	_, err = repo.CompleteRun(t.Context(), pool, org, project, env, "progress", params)
	require.ErrorIs(t, err, pipelineruns.ErrRunNotFound, "terminal transition is immutable")
	require.ErrorIs(t, observations.ObserveWorker(t.Context(), org, project, env, "progress", observed.Add(time.Hour)), pipelinerun.ErrTimelineNotFound, "terminal attempts cannot be revived by a late worker report")
}

func TestProgressIntakeBindsActualArtifactAndFencesWorkerRecovery(t *testing.T) {
	pool := openPipelineRunsE2EPool(t)
	ctx := t.Context()
	at := time.Now().UTC().Add(-time.Minute)
	org, project, env, pipeline := slug.Slug("intake-org"), slug.Slug("intake-project"), slug.Slug("staging"), slug.Slug("orders")
	seedPipelineRunSourceEventRun(t, pool, org, project, env, pipeline, "seed", at, "running")
	var versionID, jobID, otherJobID int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT pipeline_version_id FROM pipeline_runs WHERE run_slug='seed' AND project_id=(SELECT p.id FROM projects p JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND p.slug=$2)`, org, project).Scan(&versionID))
	for _, id := range []*int64{&jobID, &otherJobID} {
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO river_job(kind,queue,max_attempts,args) VALUES('progress_intake_test','progress_intake_test',1,'{}') RETURNING id`).Scan(id))
		job := *id
		t.Cleanup(func() {
			_, err := pool.Exec(context.Background(), `DELETE FROM river_job WHERE id=$1`, job)
			require.NoError(t, err)
		})
	}
	repo := pipelineruns.New()
	queued := json.RawMessage(`{"events":[{"type":"run_queued"}]}`)
	create := &pipelineruns.CreateRunParams{PipelineSlug: pipeline.String(), RunSlug: "intake", Status: "queued", StartedAt: at, WorkerJobID: &jobID, Timeline: queued}
	row, err := repo.CreateRun(ctx, pool, org, project, env, create)
	require.NoError(t, err)
	require.Zero(t, row.PipelineVersionID)
	require.Empty(t, row.PipelineVersionSpecHash)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	_, err = repo.CreateRun(ctx, tx, org, project, env, create)
	require.ErrorIs(t, err, pipelineruns.ErrRunAlreadyExists)
	retained, err := repo.FindRunBySlug(ctx, tx, org, project, env, "intake")
	require.NoError(t, err, "duplicate intake must leave its enqueue transaction usable")
	require.Equal(t, row.Timeline, retained.Timeline)
	require.NoError(t, tx.Rollback(ctx))

	require.NoError(t, repo.BindWorkerJob(ctx, pool, org, project, env, "intake", jobID))
	require.ErrorIs(t, repo.BindWorkerJob(ctx, pool, org, project, env, "intake", otherJobID), pipelineruns.ErrRunNotFound)
	refs, err := repo.ListInterruptedForSystem(ctx, pool, at.Add(30*time.Second))
	require.NoError(t, err)
	require.Empty(t, refs, "waiting in a queue is not an abandoned worker")
	running := json.RawMessage(`{"events":[{"type":"run_queued"},{"type":"run_started"}]}`)
	_, err = repo.MarkRunRunning(ctx, pool, org, project, env, pipeline, "intake", at, running)
	require.NoError(t, err)
	params := &pipelineruns.CompleteRunParams{PipelineSlug: pipeline.String(), PipelineVersionID: versionID, Status: "running", StartedAt: at, Timeline: json.RawMessage(`{"revision":1,"compiled_plan_digest":"actual-plan","events":[{"type":"run_queued"},{"type":"run_started"}]}`)}
	row, err = repo.CompleteRun(ctx, pool, org, project, env, "intake", params)
	require.NoError(t, err)
	require.Equal(t, versionID, row.PipelineVersionID)
	require.Equal(t, "sha256:source-event-version", row.PipelineVersionSpecHash)
	_, err = pool.Exec(ctx, `UPDATE river_job SET attempt=1 WHERE id=$1`, jobID)
	require.NoError(t, err)
	refs, err = repo.ListInterruptedForSystem(ctx, pool, at.Add(30*time.Second))
	require.NoError(t, err)
	require.Len(t, refs, 1)
	require.Equal(t, "intake", refs[0].RunSlug)
	require.NoError(t, checkpoints.New(pool).ObserveWorker(ctx, org, project, env, "intake", time.Now().UTC()))
	refs, err = repo.ListInterruptedForSystem(ctx, pool, time.Now().UTC().Add(-15*time.Second))
	require.NoError(t, err)
	require.Empty(t, refs, "recent worker observation prevents interrupted classification")
	params.Timeline = json.RawMessage(`{"revision":2,"compiled_plan_digest":"actual-plan","events":[{"type":"run_queued"},{"type":"run_started"}]}`)
	params.PipelineVersionID = versionID + 1000000
	_, err = repo.CompleteRun(ctx, pool, org, project, env, "intake", params)
	require.ErrorIs(t, err, pipelineruns.ErrRunNotFound, "admission cannot switch the executed artifact")
}

func TestProgressMultipleObserversDuringQuietExecution(t *testing.T) {
	pool := openPipelineRunsE2EPool(t)
	ctx := t.Context()
	at := time.Now().UTC()
	org, project, env, pipeline := slug.Slug("observers-org"), slug.Slug("observers-project"), slug.Slug("staging"), slug.Slug("orders")
	seedPipelineRunSourceEventRun(t, pool, org, project, env, pipeline, "observed", at, "running")
	repo := pipelineruns.New()
	params := &pipelineruns.CompleteRunParams{PipelineSlug: pipeline.String(), Status: "running", StartedAt: at, StepCount: 1, Timeline: json.RawMessage(`{"revision":1,"events":[{"type":"run_started"},{"type":"step_started","step":"quiet","status":"running"}]}`)}
	baselineAt := time.Now()
	_, err := repo.CompleteRun(ctx, pool, org, project, env, "observed", params)
	require.NoError(t, err)
	baseline := time.Since(baselineAt)
	observations := make(chan time.Duration, 1000)
	failures := make(chan error, 4)
	var group sync.WaitGroup
	observerCtx, stopObservers := context.WithCancel(ctx)
	defer stopObservers()
	for range 4 {
		group.Go(func() {
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-observerCtx.Done():
					failures <- nil
					return
				case <-ticker.C:
					start := time.Now()
					_, err := repo.FindRunBySlug(observerCtx, pool, org, project, env, "observed")
					if err != nil {
						if observerCtx.Err() != nil {
							failures <- nil
						} else {
							failures <- err
						}
						return
					}
					observations <- time.Since(start)
				}
			}
		})
	}
	heartbeatAt := time.Now()
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	heartbeat := time.NewTicker(time.Second)
	defer heartbeat.Stop()
quiet:
	for {
		select {
		case <-timer.C:
			break quiet
		case now := <-heartbeat.C:
			require.NoError(t, checkpoints.New(pool).ObserveWorker(ctx, org, project, env, "observed", now))
			heartbeatAt = now
		}
	}
	params.Timeline = json.RawMessage(`{"revision":2,"events":[{"type":"run_started"},{"type":"step_started","step":"quiet","status":"running"}]}`)
	concurrentAt := time.Now()
	_, err = repo.CompleteRun(ctx, pool, org, project, env, "observed", params)
	require.NoError(t, err)
	concurrentWrite := time.Since(concurrentAt)
	stopObservers()
	group.Wait()
	close(observations)
	for range 4 {
		require.NoError(t, <-failures)
	}
	row, err := repo.FindRunBySlug(ctx, pool, org, project, env, "observed")
	require.NoError(t, err)
	var timeline pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal(row.Timeline, &timeline))
	require.EqualValues(t, 2, timeline.Revision)
	require.Len(t, timeline.Events, 2, "quiet work and observers must not invent transitions")
	require.WithinDuration(t, heartbeatAt, *timeline.WorkerObservedAt, time.Millisecond)
	require.Equal(t, "running", row.Status, "disconnecting all observers must not stop execution")
	params.Status = "succeeded"
	ended := time.Now().UTC()
	params.EndedAt = &ended
	params.Timeline = json.RawMessage(`{"revision":3,"events":[{"type":"run_started"},{"type":"step_started","step":"quiet","status":"running"},{"type":"step_succeeded","step":"quiet","status":"succeeded"},{"type":"run_completed","status":"succeeded"}]}`)
	finalizeAt := time.Now()
	_, err = repo.CompleteRun(ctx, pool, org, project, env, "observed", params)
	require.NoError(t, err)
	finalize := time.Since(finalizeAt)
	var durations []time.Duration
	for duration := range observations {
		durations = append(durations, duration)
	}
	require.Greater(t, len(durations), 20)
	slices.Sort(durations)
	p95 := durations[(len(durations)-1)*95/100]
	require.Less(t, p95, 5*time.Second)
	t.Logf("local PostgreSQL persistence measurement: 4 observers at 200ms, quiet interval=6s, reads=%d, p95_read=%s, boundary_write_baseline=%s, boundary_write_with_observers=%s, final_write_after_observers=%s; excludes network/UI/provider execution", len(durations), p95, baseline, concurrentWrite, finalize)
}
