package pipelinesvc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/stretchr/testify/require"
)

func TestRunProgressPagesAndClassifiesOnlyRecordedLiveness(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	observed := now.Add(-5 * time.Second)
	timeline := pipelinerun.RunTimeline{CompiledPlanDigest: "sha256:compiled", Revision: 7, WorkerObservedAt: &observed}
	for i := range 205 {
		timeline.Events = append(timeline.Events, pipelinerun.TimelineEvent{Type: "step_started", Step: "fetch", At: observed.Add(time.Duration(i) * time.Millisecond)})
	}
	deploymentDigest := "sha256:deployment"
	record := &pipelineruns.PipelineRun{DeploymentResolutionDigest: &deploymentDigest, RunSlug: "attempt-2", PipelineVersionSpecHash: "immutable-version", Status: "running", Source: pipelinerun.RunSourceSummary{RootRunSlug: "root", AttemptNumber: 2}}
	first, err := projectRunProgress(record, timeline, 0, 100, now)
	require.NoError(t, err)
	require.Len(t, first.Events, 100)
	require.True(t, first.HasMore)
	require.EqualValues(t, 100, first.NextAfterEvent)
	require.EqualValues(t, 7, first.Revision)
	require.Equal(t, "sha256:compiled", *first.CompiledPlanDigest)
	require.Equal(t, deploymentDigest, *first.DeploymentResolutionDigest)
	require.Equal(t, "immutable-version", first.Metadata.PipelineVersionSpecHash)
	require.Equal(t, slug.Slug("root"), first.Metadata.Source.RootRunSlug)
	require.Equal(t, 2, first.Metadata.Source.AttemptNumber)
	require.Equal(t, "recent", first.WorkerLiveness.String())
	last, err := projectRunProgress(record, timeline, 200, 100, now)
	require.NoError(t, err)
	require.Len(t, last.Events, 5)
	require.False(t, last.HasMore)
	require.EqualValues(t, 205, last.NextAfterEvent)
	require.Equal(t, timeline.Events[204].At, *last.ObservedAt)
	_, err = projectRunProgress(record, timeline, 206, 100, now)
	require.Error(t, err)
	for _, tc := range []struct {
		status string
		at     *time.Time
		now    time.Time
		want   string
	}{
		{"running", &observed, now.Add(time.Minute), "stale"},
		{"running", nil, now, "unavailable"},
		{"queued", &observed, now, "unavailable"},
		{"running", &now, observed, "unavailable"},
		{"failed", &observed, now, "terminal"},
		{"succeeded", nil, now, "terminal"},
	} {
		record.Status = tc.status
		timeline.WorkerObservedAt = tc.at
		got, err := projectRunProgress(record, timeline, 205, 100, tc.now)
		require.NoError(t, err)
		require.Equal(t, tc.want, got.WorkerLiveness.String())
		require.Empty(t, got.Events)
	}
}

func TestRunProgressValidatesBoundsAndKeepsExactReadScope(t *testing.T) {
	calls := 0
	svc := &service{executionClock: fixedClock{at: time.Now()}, runRepo: &fakeRunRepository{findRunBySlugFn: func(_ context.Context, _ postgres.DBTX, org, project, env slug.Slug, run string) (*pipelineruns.PipelineRun, error) {
		calls++
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("ops"), project)
		require.Equal(t, slug.Slug("staging"), env)
		require.Equal(t, "exact-attempt", run)
		return &pipelineruns.PipelineRun{Timeline: json.RawMessage(`{"events":[]}`)}, nil
	}}}
	for _, bounds := range [][2]int64{{-1, 100}, {0, 0}, {0, 101}} {
		_, err := svc.GetRunProgress(t.Context(), "acme", "ops", "staging", "exact-attempt", bounds[0], int(bounds[1]))
		require.Error(t, err)
	}
	require.Zero(t, calls)
	_, err := svc.GetRunProgress(t.Context(), "acme", "ops", "staging", "exact-attempt", 0, 100)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}

func TestRunProgressSeparatesPendingAndInterruptedSteps(t *testing.T) {
	now := time.Now().UTC()
	record := &pipelineruns.PipelineRun{Status: "running", StepCount: 5}
	timeline := pipelinerun.RunTimeline{Events: []pipelinerun.TimelineEvent{
		{Type: "step_reused", Step: "read", Status: "succeeded"},
		{Type: "step_succeeded", Step: "normalize", Status: "succeeded"},
		{Type: "checkpoint_committed", Step: "read", Status: "committed"},
		{Type: "step_started", Step: "deliver", Status: "running"},
	}}
	progress, err := projectRunProgress(record, timeline, 0, 100, now)
	require.NoError(t, err)
	require.Equal(t, 2, progress.CompletedSteps)
	require.Equal(t, 2, progress.PendingSteps)
	require.Equal(t, "deliver", *progress.CurrentStep)
	require.Zero(t, progress.InterruptedSteps)
	record.Status = "failed"
	progress, err = projectRunProgress(record, timeline, 0, 100, now)
	require.NoError(t, err)
	require.Nil(t, progress.CurrentStep)
	require.Zero(t, progress.FailedSteps, "run failure does not invent a step-finished failure")
	require.Equal(t, 1, progress.InterruptedSteps)
	require.Equal(t, 2, progress.PendingSteps)
}
