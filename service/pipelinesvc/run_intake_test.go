package pipelinesvc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/stretchr/testify/require"
)

func TestTriggerIntakeRecordsOnlyAvailableFactsAndStartsOnce(t *testing.T) {
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var row *pipelineruns.PipelineRun
	starts := 0
	repo := &fakeRunRepository{
		createRunFn: func(_ context.Context, _ postgres.DBTX, org, project, env slug.Slug, p *pipelineruns.CreateRunParams) (*pipelineruns.PipelineRun, error) {
			require.Equal(t, slug.Slug("acme"), org)
			require.Equal(t, slug.Slug("etl"), project)
			require.Equal(t, slug.Slug("staging"), env)
			if row != nil {
				return nil, pipelineruns.ErrRunAlreadyExists
			}
			require.Zero(t, p.PipelineVersionID)
			require.EqualValues(t, 42, *p.WorkerJobID)
			row = &pipelineruns.PipelineRun{PipelineSlug: p.PipelineSlug, RunSlug: p.RunSlug, Source: p.Source, Status: p.Status, StartedAt: p.StartedAt, Timeline: p.Timeline, WorkerJobID: p.WorkerJobID}
			return row, nil
		},
		findRunBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) (*pipelineruns.PipelineRun, error) {
			return row, nil
		},
		markRunRunningFn: func(_ context.Context, _ postgres.DBTX, _, _, _, _ slug.Slug, _ string, at time.Time, raw json.RawMessage) (*pipelineruns.PipelineRun, error) {
			starts++
			row.Status = "running"
			row.Timeline = raw
			row.StartedAt = at
			return row, nil
		},
	}
	svc := &service{runRepo: repo, txm: &recordingPipelineGitTxManager{}}
	source := pipelinerun.RunSourceSummary{Type: model.PipelineRunSourceTypeSchedule}
	require.NoError(t, svc.AcceptQueuedRun(t.Context(), "acme", "etl", "staging", "orders", "attempt", source, at, 42))
	var timeline pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal(row.Timeline, &timeline))
	require.Len(t, timeline.Events, 1)
	require.Equal(t, "run_queued", timeline.Events[0].Type)
	require.Zero(t, timeline.Revision)
	require.NoError(t, svc.AcceptQueuedRun(t.Context(), "acme", "etl", "staging", "orders", "attempt", source, at.Add(time.Hour), 42))
	require.Error(t, svc.AcceptQueuedRun(t.Context(), "acme", "etl", "staging", "orders", "attempt", source, at, 43))
	require.NoError(t, svc.StartQueuedRun(t.Context(), "acme", "etl", "staging", "attempt", at.Add(time.Second)))
	require.NoError(t, svc.StartQueuedRun(t.Context(), "acme", "etl", "staging", "attempt", at.Add(2*time.Second)))
	require.Equal(t, 1, starts)
	require.NoError(t, json.Unmarshal(row.Timeline, &timeline))
	require.Len(t, timeline.Events, 2)
	require.Equal(t, at, timeline.Events[0].At)
	require.Equal(t, "run_started", timeline.Events[1].Type)
}

func TestWorkerReconciliationRetainsEvidenceAndNeverDispatchesEffects(t *testing.T) {
	now := time.Now().UTC()
	prior := pipelinerun.RunTimeline{RunSlug: "attempt", Revision: 4, CompiledPlanDigest: "compiled", Events: []pipelinerun.TimelineEvent{{At: now.Add(-time.Minute), Type: "step_started", Step: "sink", Status: "running"}}}
	raw, err := json.Marshal(prior)
	require.NoError(t, err)
	replayOf, replayFrom := "root-run", "fetch"
	row := &pipelineruns.PipelineRun{ReplayOfRunSlug: &replayOf, ReplayFromStep: &replayFrom, Incremental: pipelinerun.IncrementalRunSummary{Mode: "backfill", CheckpointStatus: "skipped", BackfillSeriesSlug: "retained-backfill"}, IncrementalDiagnostics: emptyPipelineRunDiagnostics(), PipelineSlug: "orders", RunSlug: "attempt", Status: "running", StartedAt: now.Add(-time.Minute), Timeline: raw, StepCount: 3, StepSlug: "sink"}
	writes := 0
	repo := &fakeRunRepository{
		listInterruptedFn: func(_ context.Context, _ postgres.DBTX, cutoff time.Time) ([]pipelineruns.InterruptedRun, error) {
			require.Equal(t, now.Add(-pipelinerun.WorkerObservationStaleAfter), cutoff)
			return []pipelineruns.InterruptedRun{{OrgSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "staging", RunSlug: "attempt", JobState: "retryable"}}, nil
		},
		findRunBySlugFn: func(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) (*pipelineruns.PipelineRun, error) {
			return row, nil
		},
		completeRunFn: func(_ context.Context, _ postgres.DBTX, _, _, _ slug.Slug, _ string, p *pipelineruns.CompleteRunParams) (*pipelineruns.PipelineRun, error) {
			writes++
			var timeline pipelinerun.RunTimeline
			require.NoError(t, json.Unmarshal(p.Timeline, &timeline))
			require.EqualValues(t, 5, timeline.Revision)
			require.Equal(t, "compiled", timeline.CompiledPlanDigest)
			require.Equal(t, prior.Events[0], timeline.Events[0])
			require.Len(t, timeline.Events, 2)
			require.True(t, timeline.Failure.OutcomeUncertain)
			require.True(t, timeline.Failure.AutomaticRetryProhibited)
			require.Equal(t, pipelinerun.FailureReasonCodeWorkerOwnershipLost, timeline.Failure.ReasonCode)
			require.Equal(t, "failed", p.Status)
			require.EqualValues(t, 3, p.StepCount)
			require.Equal(t, row.Incremental, p.Incremental)
			diagnostics, err := json.Marshal(row.IncrementalDiagnostics)
			require.NoError(t, err)
			require.JSONEq(t, string(diagnostics), string(p.IncrementalDiagnostics))
			require.Equal(t, row.ReplayOfRunSlug, p.ReplayOfRunSlug)
			require.Equal(t, row.ReplayFromStep, p.ReplayFromStep)
			return &pipelineruns.PipelineRun{RunSlug: "attempt", PipelineSlug: "orders", Status: p.Status, Timeline: p.Timeline}, nil
		},
	}
	svc := &service{runRepo: repo, txm: &recordingPipelineGitTxManager{}, executionClock: fixedClock{at: now}, eventProducer: eventtest.NewProducer(&fakePipelineEventRecorder{})}
	require.NoError(t, svc.ReconcileInterruptedRuns(t.Context(), now))
	require.Equal(t, 1, writes)
	// No runtime dependency is provided: reconciliation must never dispatch connectors.
}
