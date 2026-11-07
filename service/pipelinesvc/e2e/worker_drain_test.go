package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/worker/consumer"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

func TestPipelineRunWorkerGracefulDrainPreservesCheckpointAndQueuedRun(t *testing.T) { // Real River/Postgres pipeline execution E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres pipeline worker graceful-drain E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openE2EPool(t)
	orgSlug := must.Slug(fmt.Sprintf("worker-drain-checkpoint-org-%d", time.Now().UnixNano()))
	memberSlug := createIncrementalCheckpointServiceOrgProject(t, pool, orgSlug)
	pipeline := incrementalCheckpointServicePipeline(must.Slug("worker-drain-users"))
	queueName := fmt.Sprintf("pipeline_run_drain_%d", time.Now().UnixNano())

	enqueueOnly, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	runProducer, err := workerproducer.NewPipelineRunProducerForQueue(queue.NewEnqueuer(enqueueOnly), pipelineruns.New(), queueName)
	require.NoError(t, err)

	sinkEntered := make(chan struct{})
	releaseSink := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSink) }) }
	t.Cleanup(release)

	executor := &incrementalCheckpointServiceQueryExecutor{
		sourceObservations: []json.RawMessage{
			json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`),
			json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`),
		},
	}
	var gateOnce sync.Once
	executor.sinkBarrier = func() {
		gateOnce.Do(func() {
			close(sinkEntered)
			<-releaseSink
		})
	}
	svc, store := newIncrementalCheckpointService(t, pool, executor, eventtest.NewProducer(nil), runProducer)
	createAndDeployIncrementalCheckpointPipeline(t, ctx, svc, store, orgSlug, memberSlug, pipeline)

	firstHost := newPipelineRunDrainHost(t, pool, queueName, svc)
	require.NoError(t, firstHost.Start(ctx))
	stopDone := make(chan struct{})
	var stopErr error
	stopStarted := false
	t.Cleanup(func() {
		release()
		if stopStarted {
			select {
			case <-stopDone:
				require.NoError(t, stopErr)
			case <-time.After(5 * time.Second):
				t.Error("graceful pipeline River stop did not finish during cleanup")
			}
			return
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		require.NoError(t, firstHost.Stop(stopCtx))
	})

	actor := model.UserEventActor(memberSlug)
	firstSlug := must.Slug("drain-first")
	first, err := svc.StartRun(ctx, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, &firstSlug, nil, actor, e2eRunSource(actor), nil)
	require.NoError(t, err)
	require.Equal(t, firstSlug.String(), first.Metadata.RunSlug)
	select {
	case <-sinkEntered:
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "first pipeline run did not reach the sink gate")
	}

	secondSlug := must.Slug("drain-second")
	second, err := svc.StartRun(ctx, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, &secondSlug, nil, actor, e2eRunSource(actor), nil)
	require.NoError(t, err)
	require.Equal(t, "queued", second.Metadata.Status)
	firstJobID := workerDrainRunJobID(t, ctx, pool, orgSlug, firstSlug)
	secondJobID := workerDrainRunJobID(t, ctx, pool, orgSlug, secondSlug)
	require.NotEqual(t, firstJobID, secondJobID)

	stopStarted = true
	go func() {
		stopErr = firstHost.Stop(context.Background())
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("graceful pipeline River stop returned while the sink was gated")
	case <-time.After(200 * time.Millisecond):
	}
	secondBeforeRelease := waitForWorkerDrainRiverJob(t, ctx, pool, secondJobID, "available")
	require.Equal(t, 0, secondBeforeRelease.attempt)

	release()
	select {
	case <-stopDone:
		require.NoError(t, stopErr)
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "graceful pipeline River stop did not finish after sink release")
	}

	firstCompleted := waitForWorkerDrainRiverJob(t, ctx, pool, firstJobID, "completed")
	require.Equal(t, 1, firstCompleted.attempt)
	firstRun := waitForWorkerDrainRun(t, ctx, svc, orgSlug, firstSlug)
	require.Equal(t, "succeeded", firstRun.Metadata.Status)
	require.Equal(t, firstSlug, firstRun.Metadata.Source.RootRunSlug)
	require.Equal(t, 1, firstRun.Metadata.Source.AttemptNumber)
	firstUpper, firstStatus := currentIncrementalCheckpoint(t, pool, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug)
	require.Equal(t, "committed", firstStatus)
	require.JSONEq(t, `{"value":"2026-05-24T10:00:00Z"}`, string(firstUpper))

	replacementHost := newPipelineRunDrainHost(t, pool, queueName, svc)
	require.NoError(t, replacementHost.Start(ctx))
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		require.NoError(t, replacementHost.Stop(stopCtx))
	})

	secondCompleted := waitForWorkerDrainRiverJob(t, ctx, pool, secondJobID, "completed")
	require.Equal(t, 1, secondCompleted.attempt)
	require.Equal(t, firstJobID, workerDrainRunJobID(t, ctx, pool, orgSlug, firstSlug))
	require.Equal(t, secondJobID, workerDrainRunJobID(t, ctx, pool, orgSlug, secondSlug))
	secondRun := waitForWorkerDrainRun(t, ctx, svc, orgSlug, secondSlug)
	require.Equal(t, "succeeded", secondRun.Metadata.Status)
	require.Equal(t, secondSlug, secondRun.Metadata.Source.RootRunSlug)
	require.Equal(t, 1, secondRun.Metadata.Source.AttemptNumber)
	upper, status := currentIncrementalCheckpoint(t, pool, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug)
	require.Equal(t, "committed", status)
	require.JSONEq(t, `{"value":"2026-05-24T11:00:00Z"}`, string(upper))
	require.Equal(t, 1, countIncrementalCheckpointsByStatus(t, pool, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, "superseded"))

	executor.mu.Lock()
	checkpointInputs := append([]*pipelinerun.IncrementalCheckpointInput(nil), executor.checkpointInputs...)
	sinkCalls := executor.sinkCalls
	executor.mu.Unlock()
	require.Len(t, checkpointInputs, 2)
	require.Nil(t, checkpointInputs[0].LowerBound)
	require.JSONEq(t, string(firstUpper), string(checkpointInputs[1].LowerBound))
	require.Equal(t, 2, sinkCalls)
}

type pipelineRunDrainRegistrar struct{ service pipelinesvc.Service }

func (r pipelineRunDrainRegistrar) Register(workers *river.Workers) error {
	return river.AddWorkerSafely(workers, consumer.NewPipelineRunExecutionWorker(r.service, slog.New(slog.DiscardHandler)))
}

func newPipelineRunDrainHost(t *testing.T, pool *pgxpool.Pool, queueName string, service pipelinesvc.Service) *queue.Client {
	t.Helper()

	host, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Logger: slog.New(slog.DiscardHandler),
		Queues: map[string]river.QueueConfig{
			queueName: {MaxWorkers: 1},
		},
		Registrars: []queue.WorkerRegistrar{pipelineRunDrainRegistrar{service: service}},
	})
	require.NoError(t, err)
	return host
}

type workerDrainRiverJob struct {
	state   string
	attempt int
}

func workerDrainRunJobID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgSlug, runSlug slug.Slug) int64 {
	t.Helper()

	var jobID int64
	err := pool.QueryRow(ctx, `
		SELECT run.worker_job_id
		FROM pipeline_runs run
		JOIN projects project ON project.id = run.project_id
		JOIN organizations organization ON organization.id = project.organization_id
		WHERE organization.slug = $1 AND run.run_slug = $2
	`, orgSlug.String(), runSlug.String()).Scan(&jobID)
	require.NoError(t, err)
	return jobID
}

func waitForWorkerDrainRiverJob(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jobID int64, wantState string) workerDrainRiverJob {
	t.Helper()

	waitCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		var job workerDrainRiverJob
		err := pool.QueryRow(waitCtx, `SELECT state::text, attempt::int FROM river_job WHERE id = $1`, jobID).Scan(&job.state, &job.attempt)
		require.NoError(t, err)
		if job.state == wantState {
			return job
		}
		select {
		case <-waitCtx.Done():
			t.Fatalf("timed out waiting for River job %d to reach %q; last state=%q attempt=%d", jobID, wantState, job.state, job.attempt)
		case <-ticker.C:
		}
	}
}

func waitForWorkerDrainRun(t *testing.T, ctx context.Context, service pipelinesvc.Service, orgSlug, runSlug slug.Slug) *pipelinesvc.PipelineRunDetail {
	t.Helper()

	waitCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		run, err := service.GetRun(waitCtx, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, runSlug.String())
		require.NoError(t, err)
		if run.Metadata.Status == "succeeded" {
			return run
		}
		select {
		case <-waitCtx.Done():
			t.Fatalf("timed out waiting for pipeline run %s to succeed; last status=%q", runSlug, run.Metadata.Status)
		case <-ticker.C:
		}
	}
}
