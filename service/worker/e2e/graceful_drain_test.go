package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/postgres"
)

func TestRiverGracefulDrainWaitsForRunningJobAndLeavesQueuedJobForReplacement(t *testing.T) { // Real River/Postgres graceful-drain E2E uses shared external state.
	if testing.Short() {
		t.Skip("skipping real-Postgres River graceful-drain E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openWorkerE2EPool(t)
	queueName := uniqueWorkerE2EQueue("graceful_drain")
	effectTable := fmt.Sprintf("worker_e2e_drain_effect_%d", time.Now().UnixNano())
	_, err := pool.Exec(ctx, "CREATE TABLE "+effectTable+" (job_id bigint PRIMARY KEY)")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+effectTable)
		require.NoError(t, cleanupErr)
	})

	releaseBusy := make(chan struct{})
	var releaseOnce sync.Once
	unblockBusy := func() { releaseOnce.Do(func() { close(releaseBusy) }) }
	t.Cleanup(unblockBusy)

	busyStarted := make(chan int64, 1)
	firstWorker := &riverDrainWorker{
		pool:        pool,
		effectTable: effectTable,
		started:     busyStarted,
		release:     releaseBusy,
	}
	firstClient := newRiverDrainClient(t, pool, queueName, firstWorker)
	require.NoError(t, firstClient.Start(ctx))

	stopDone := make(chan struct{})
	var stopErr error
	stopStarted := false
	t.Cleanup(func() {
		unblockBusy()
		if stopStarted {
			select {
			case <-stopDone:
				require.NoError(t, stopErr)
			case <-time.After(5 * time.Second):
				t.Error("graceful River stop did not finish during cleanup")
			}
			return
		}

		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		require.NoError(t, firstClient.Stop(stopCtx))
	})

	busy := enqueueRiverDrainJob(t, ctx, pool, firstClient, queueName, "busy", true)
	select {
	case startedID := <-busyStarted:
		require.Equal(t, busy.ID, startedID)
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "busy River job did not start")
	}
	queued := enqueueRiverDrainJob(t, ctx, pool, firstClient, queueName, "queued", false)

	stopStarted = true
	go func() {
		stopErr = firstClient.Stop(context.Background())
		close(stopDone)
	}()

	select {
	case <-stopDone:
		t.Fatal("graceful River stop returned while the busy job was gated")
	case <-time.After(200 * time.Millisecond):
	}

	queuedBeforeRelease := waitForRiverDrainJob(t, ctx, pool, queued.ID, "available")
	require.Equal(t, 0, queuedBeforeRelease.attempt)

	unblockBusy()
	select {
	case <-stopDone:
		require.NoError(t, stopErr)
	case <-ctx.Done():
		require.NoError(t, ctx.Err(), "graceful River stop did not finish after the busy job was released")
	}

	busyCompleted := waitForRiverDrainJob(t, ctx, pool, busy.ID, "completed")
	require.Equal(t, 1, busyCompleted.attempt)
	queuedAfterStop := waitForRiverDrainJob(t, ctx, pool, queued.ID, "available")
	require.Equal(t, 0, queuedAfterStop.attempt)

	replacementClient := newRiverDrainClient(t, pool, queueName, &riverDrainWorker{pool: pool, effectTable: effectTable})
	require.NoError(t, replacementClient.Start(ctx))
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		require.NoError(t, replacementClient.Stop(stopCtx))
	})

	queuedCompleted := waitForRiverDrainJob(t, ctx, pool, queued.ID, "completed")
	require.Equal(t, 1, queuedCompleted.attempt)

	var effects int
	err = pool.QueryRow(ctx, "SELECT count(*) FROM "+effectTable).Scan(&effects)
	require.NoError(t, err)
	require.Equal(t, 2, effects)
}

type riverDrainArgs struct {
	Name  string `json:"name"`
	Block bool   `json:"block"`
}

func (riverDrainArgs) Kind() string { return "worker_e2e_graceful_drain" }

func (riverDrainArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{} }

type riverDrainRegistrar struct{ worker *riverDrainWorker }

func (r riverDrainRegistrar) Register(workers *river.Workers) error {
	return river.AddWorkerSafely(workers, r.worker)
}

type riverDrainWorker struct {
	river.WorkerDefaults[riverDrainArgs]

	pool        *pgxpool.Pool
	effectTable string
	started     chan<- int64
	release     <-chan struct{}
}

func (w *riverDrainWorker) Work(ctx context.Context, job *river.Job[riverDrainArgs]) error {
	if job.Args.Block {
		select {
		case w.started <- job.ID:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-w.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	_, err := w.pool.Exec(ctx, "INSERT INTO "+w.effectTable+" (job_id) VALUES ($1)", job.ID)
	return err
}

func newRiverDrainClient(t *testing.T, pool *pgxpool.Pool, queueName string, worker *riverDrainWorker) *queue.Client {
	t.Helper()

	client, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Logger: slog.New(slog.DiscardHandler),
		Queues: map[string]river.QueueConfig{
			queueName: {MaxWorkers: 1},
		},
		Registrars: []queue.WorkerRegistrar{riverDrainRegistrar{worker: worker}},
	})
	require.NoError(t, err)
	return client
}

func enqueueRiverDrainJob(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	client *queue.Client,
	queueName string,
	name string,
	block bool,
) queue.QueuedJob {
	t.Helper()

	var queued queue.QueuedJob
	err := postgres.NewTransactionManager(pool).RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var enqueueErr error
		queued, enqueueErr = queue.NewEnqueuer(client).EnqueueTx(ctx, tx, queue.EnqueueRequest{
			Args:  riverDrainArgs{Name: name, Block: block},
			Queue: queueName,
		})
		return enqueueErr
	})
	require.NoError(t, err)
	require.Equal(t, "queued", queued.State)
	return queued
}

func waitForRiverDrainJob(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	id int64,
	wantState string,
) riverJobSnapshot {
	t.Helper()

	waitCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		var job riverJobSnapshot
		err := pool.QueryRow(waitCtx, `SELECT state::text, attempt::int, COALESCE(array_to_json(errors)::text, '[]') FROM river_job WHERE id = $1`, id).Scan(&job.state, &job.attempt, &job.errorsJSON)
		require.NoError(t, err)
		if job.state == wantState {
			return job
		}

		select {
		case <-waitCtx.Done():
			failRiverJobStateTimeout(t, "graceful-drain", fmt.Sprint(id), wantState, job)
		case <-ticker.C:
		}
	}
}
