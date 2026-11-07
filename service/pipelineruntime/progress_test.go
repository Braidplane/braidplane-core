package pipelineruntime

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/testutil"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/stretchr/testify/require"
)

func progressTestRequest(t *testing.T) RunRequest {
	t.Helper()
	req := checkTestRequest(t)
	req.Pipeline.Steps = append(req.Pipeline.Steps[:1], req.Pipeline.Steps[2])
	req.Pipeline.Steps[1].Sink.Input.FromStep = "fetch"
	return req
}

func progressTestRunner(t *testing.T, store pipelinerun.RunStore, sink SinkRuntime) *Runner {
	t.Helper()
	runner := checkTestRunner(t, store, sink)
	runner.dataSourceService = compositionDataSourceService{schema: datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{Type: "postgres", Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{Name: "orders", Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "integer"}}}}}}}
	return runner
}

func TestProgressIsDurableDuringStepWithoutStructuralChecks(t *testing.T) {
	store, saved := durableCheckStore(t, nil)
	sinkCalls := 0
	runner := progressTestRunner(t, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
		sinkCalls++
		var durable pipelinerun.RunTimeline
		require.NoError(t, json.Unmarshal((*saved)[len(*saved)-1], &durable))
		require.Empty(t, durable.StructuralChecks)
		require.Equal(t, "running", durable.Status)
		require.Equal(t, "step_started", durable.Events[len(durable.Events)-1].Type)
		require.Equal(t, "deliver", durable.Events[len(durable.Events)-1].Step.String())
		require.Equal(t, "step_succeeded", durable.Events[len(durable.Events)-2].Type)
		require.Equal(t, "fetch", durable.Events[len(durable.Events)-2].Step.String())
		return nil
	}})
	run, err := runner.Run(t.Context(), progressTestRequest(t))
	require.NoError(t, err)
	require.Equal(t, 1, sinkCalls)
	require.Equal(t, "succeeded", run.Metadata.Status)
	for i, raw := range *saved {
		var durable pipelinerun.RunTimeline
		require.NoError(t, json.Unmarshal(raw, &durable))
		require.EqualValues(t, i+1, durable.Revision)
	}
	// Redelivery cannot repeat the completed attempt, even with no contracts.
	_, err = runner.Run(t.Context(), progressTestRequest(t))
	require.ErrorIs(t, err, errAttemptInterrupted)
	require.Equal(t, 1, sinkCalls)
}

func TestProgressPersistenceFailureStopsBeforeSink(t *testing.T) {
	store, saved := durableCheckStore(t, func(run *pipelinerun.RunResult) bool {
		events := run.Timeline.Events
		last := events[len(events)-1]
		return last.Type == "step_started" && last.Step == "deliver"
	})
	sinkCalls := 0
	runner := progressTestRunner(t, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error { sinkCalls++; return nil }})
	run, err := runner.Run(t.Context(), progressTestRequest(t))
	require.ErrorIs(t, err, errCheckPersistence)
	require.Nil(t, run)
	require.Zero(t, sinkCalls)
	var durable pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal((*saved)[len(*saved)-1], &durable))
	require.Equal(t, "failed", durable.Status)
	require.Equal(t, "run_completed", durable.Events[len(durable.Events)-1].Type)
}

func TestProgressPersistenceFailureAfterSinkIsUncertain(t *testing.T) {
	failed := false
	store, saved := durableCheckStore(t, func(run *pipelinerun.RunResult) bool {
		events := run.Timeline.Events
		last := events[len(events)-1]
		if !failed && last.Type == "step_succeeded" && last.Step == "deliver" {
			failed = true
			return true
		}
		return false
	})
	sinkCalls := 0
	runner := progressTestRunner(t, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error { sinkCalls++; return nil }})
	run, err := runner.Run(t.Context(), progressTestRequest(t))
	require.ErrorIs(t, err, errCheckPersistence)
	require.Nil(t, run)
	require.Equal(t, 1, sinkCalls)
	var durable pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal((*saved)[len(*saved)-1], &durable))
	require.Equal(t, "failed", durable.Status)
	require.True(t, durable.Failure.OutcomeUncertain)
	require.Equal(t, "run_completed", durable.Events[len(durable.Events)-1].Type)
}

func TestProgressPreservesAcceptedAndWorkerStartedFacts(t *testing.T) {
	req := progressTestRequest(t)
	store, saved := durableCheckStore(t, nil)
	accepted := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	workerStarted := accepted.Add(4 * time.Second)
	store.timelines = map[string]pipelinerun.RunTimeline{req.RunSlug: {
		RunSlug: req.RunSlug, Status: "running",
		Events: []pipelinerun.TimelineEvent{{At: accepted, Type: "run_queued", Status: "queued"}, {At: workerStarted, Type: "run_started", Status: "running"}},
	}}
	runner := progressTestRunner(t, store, &fakeSinkRuntime{})
	run, err := runner.Run(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, "run_queued", run.Timeline.Events[0].Type)
	require.Equal(t, accepted, run.Timeline.Events[0].At)
	require.Equal(t, "run_started", run.Timeline.Events[1].Type)
	require.Equal(t, workerStarted, run.Timeline.Events[1].At)
	outcome, err := newRunOutcome(req.Pipeline, run)
	require.NoError(t, err)
	require.Equal(t, workerStarted, outcome.Metadata.StartedAt)

	require.Equal(t, "step_started", run.Timeline.Events[2].Type)
	var first pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal((*saved)[0], &first))
	require.EqualValues(t, 1, first.Revision)
	require.Len(t, first.Events, 2)
}

func TestWorkerObservationFailureCancelsExecutionAndStopsReporter(t *testing.T) {
	for _, writeErr := range []error{xerror.Unexpected(), pipelinerun.ErrTimelineNotFound} {
		t.Run(writeErr.Error(), func(t *testing.T) {
			type observation struct {
				org, project, environment slug.Slug
				run                       string
				at                        time.Time
			}
			reported := make(chan observation, 16)
			var calls atomic.Int64
			store := &fakeRunStore{observeWorkerFn: func(_ context.Context, org, project, environment slug.Slug, run string, at time.Time) error {
				calls.Add(1)
				reported <- observation{org, project, environment, run, at}
				return writeErr
			}}
			runner := progressTestRunner(t, store, &fakeSinkRuntime{})
			runner.clock = testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC).Add(time.Duration(calls.Load()) * 5 * time.Second)
			}}
			ctx, stop := runner.observeWorker(t.Context(), progressTestRequest(t), time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), time.Millisecond)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("execution did not stop after losing durable worker observations")
			}
			require.ErrorIs(t, context.Cause(ctx), errCheckPersistence)
			require.ErrorIs(t, stop(), errCheckPersistence)
			if xerror.Is(writeErr, pipelinerun.ErrTimelineNotFound) {
				require.EqualValues(t, 1, calls.Load())
			} else {
				require.EqualValues(t, 3, calls.Load())
			}
			observed := <-reported
			require.Equal(t, slug.Slug("acme"), observed.org)
			require.Equal(t, slug.Slug("etl"), observed.project)
			require.Equal(t, slug.Slug("staging"), observed.environment)
			require.Equal(t, "checks-test", observed.run)
			require.False(t, observed.at.IsZero())
		})
	}
}

func TestWorkerObservationRecoversWithinStaleWindow(t *testing.T) {
	var calls atomic.Int64
	recovered := make(chan struct{}, 1)
	store := &fakeRunStore{observeWorkerFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, string, time.Time) error {
		call := calls.Add(1)
		if call == 1 {
			return xerror.Unexpected()
		}
		if call == 2 {
			recovered <- struct{}{}
		}
		return nil
	}}
	runner := progressTestRunner(t, store, &fakeSinkRuntime{})
	runner.clock = testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC).Add(time.Duration(calls.Load()) * 5 * time.Second)
	}}
	ctx, stop := runner.observeWorker(t.Context(), progressTestRequest(t), time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), time.Millisecond)
	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("worker observation did not recover")
	}
	require.NoError(t, ctx.Err())
	require.NoError(t, stop())
}
