package queue

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestNew_NilPoolReturnsError(t *testing.T) {
	t.Parallel()

	client, err := New(nil, nil)
	require.Nil(t, client)
	require.ErrorContains(t, err, "queue database pool is required")
}

func TestNewEnqueueOnly_StartAndStopAreNoOpsWithoutWorkers(t *testing.T) {
	t.Parallel()

	pool, err := pgxpool.New(context.Background(), "postgres://postgres:postgres@127.0.0.1:1/braidplane?sslmode=disable")
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	client, err := NewEnqueueOnly(pool)
	require.NoError(t, err)
	require.False(t, client.willWork)

	require.NoError(t, client.Start(context.Background()))
	require.False(t, client.started)

	require.NoError(t, client.Stop(context.Background()))
	require.False(t, client.started)
}

func TestNew_WithQueueConfigMarksClientAsWorkerCapable(t *testing.T) {
	t.Parallel()

	pool := newTestQueuePool(t)

	client, err := New(pool, &Config{
		Queues: map[string]river.QueueConfig{
			"default": {MaxWorkers: 1},
		},
		Workers: river.NewWorkers(),
	})
	require.NoError(t, err)
	require.True(t, client.willWork)
}

func TestNewWorkerHost_WithoutRegistrarsRemainsEnqueueOnly(t *testing.T) {
	t.Parallel()

	pool := newTestQueuePool(t)

	client, err := NewWorkerHost(pool, &WorkerHostConfig{})
	require.NoError(t, err)
	require.False(t, client.willWork)
}

func TestNewWorkerHost_WithRegistrarsUsesDefaultQueueConfig(t *testing.T) {
	t.Parallel()

	pool := newTestQueuePool(t)

	client, err := NewWorkerHost(pool, &WorkerHostConfig{
		Registrars: []WorkerRegistrar{
			testWorkerRegistrar{register: func(workers *river.Workers) error {
				river.AddWorker(workers, &testQueueWorker{})
				return nil
			}},
		},
	})
	require.NoError(t, err)
	require.True(t, client.willWork)
}

func TestNewWorkerHost_WithPeriodicJobsOnlyMarksClientAsWorkerCapable(t *testing.T) {
	t.Parallel()

	pool := newTestQueuePool(t)

	client, err := NewWorkerHost(pool, &WorkerHostConfig{
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(
				river.PeriodicInterval(time.Hour),
				func() (river.JobArgs, *river.InsertOpts) {
					return testJobArgs{}, nil
				},
				nil,
			),
		},
	})
	require.NoError(t, err)
	require.True(t, client.willWork)
}

func TestNewWorkerHost_UsesPeriodicJobsFromRegistrarWhenNotExplicitlyConfigured(t *testing.T) {
	t.Parallel()

	pool := newTestQueuePool(t)

	client, err := NewWorkerHost(pool, &WorkerHostConfig{
		Registrars: []WorkerRegistrar{
			testPeriodicJobRegistrar{
				testWorkerRegistrar: testWorkerRegistrar{register: func(workers *river.Workers) error {
					river.AddWorker(workers, &testQueueWorker{})
					return nil
				}},
				jobs: []*river.PeriodicJob{
					river.NewPeriodicJob(
						river.PeriodicInterval(time.Hour),
						func() (river.JobArgs, *river.InsertOpts) {
							return testJobArgs{}, nil
						},
						nil,
					),
				},
			},
		},
	})
	require.NoError(t, err)
	require.True(t, client.willWork)
}

func TestNewWorkerHost_RegistrarErrorReturnsError(t *testing.T) {
	t.Parallel()

	pool := newTestQueuePool(t)

	client, err := NewWorkerHost(pool, &WorkerHostConfig{
		Registrars: []WorkerRegistrar{
			testWorkerRegistrar{register: func(*river.Workers) error {
				return xerror.Unexpected().Detail("register boom")
			}},
		},
	})
	require.Nil(t, client)
	require.ErrorContains(t, err, "register River worker")
	require.ErrorContains(t, err, "register boom")
}

func TestEnqueueTx_NilTxReturnsError(t *testing.T) {
	t.Parallel()

	client := &Client{}

	_, _, err := client.enqueueTx(context.Background(), nil, EnqueueRequest{Args: testJobArgs{}})
	require.ErrorContains(t, err, "queue transaction is required")
}

func TestToRiverInsertOpts_QueueBehavior(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		override string
		expected string
	}{
		{
			name:     "QueueOverride",
			override: "explicit",
			expected: "explicit",
		},
		{
			name:     "JobDefault",
			override: "",
			expected: "from_args",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			opts := toRiverInsertOpts(testJobArgs{}, test.override)

			require.NotNil(t, opts)
			require.Equal(t, test.expected, opts.Queue)
			require.True(t, opts.UniqueOpts.ByArgs)
		})
	}
}

func TestWorkerLivenessSnapshotReportsConfiguredQueues(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	client := &Client{
		started: true,
		queues: map[string]river.QueueConfig{
			"pipeline_retry":    {MaxWorkers: 4},
			"pipeline_schedule": {MaxWorkers: 2},
		},
		heartbeatAt: now.Add(-time.Minute),
	}

	snapshot := client.WorkerLivenessSnapshot(now)

	require.Equal(t, []WorkerLivenessMetric{
		{
			Queue:            "pipeline_retry",
			ActiveWorkers:    4,
			MaxWorkers:       4,
			LastHeartbeatAge: time.Minute,
		},
		{
			Queue:            "pipeline_schedule",
			ActiveWorkers:    2,
			MaxWorkers:       2,
			LastHeartbeatAge: time.Minute,
		},
	}, snapshot)
	require.Equal(t, now.Add(-time.Minute), client.heartbeatAt)
}

func TestWorkerLivenessSnapshotReportsStoppedQueuesAsMissing(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	client := &Client{
		started:     false,
		heartbeatAt: now.Add(-5 * time.Minute),
		queues: map[string]river.QueueConfig{
			"pipeline_retry": {MaxWorkers: 4},
		},
	}

	snapshot := client.WorkerLivenessSnapshot(now)

	require.Equal(t, []WorkerLivenessMetric{
		{
			Queue:            "pipeline_retry",
			ActiveWorkers:    0,
			MaxWorkers:       4,
			LastHeartbeatAge: 5 * time.Minute,
		},
	}, snapshot)
}

type testJobArgs struct{}

func (testJobArgs) Kind() string {
	return "queue_test_job"
}

func (testJobArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: "from_args",
		UniqueOpts: river.UniqueOpts{
			ByArgs: true,
		},
	}
}

type testQueueWorker struct {
	river.WorkerDefaults[testJobArgs]
}

func (w *testQueueWorker) Work(context.Context, *river.Job[testJobArgs]) error {
	return nil
}

type testWorkerRegistrar struct {
	register func(*river.Workers) error
}

func (r testWorkerRegistrar) Register(workers *river.Workers) error {
	return r.register(workers)
}

type testPeriodicJobRegistrar struct {
	testWorkerRegistrar
	jobs []*river.PeriodicJob
}

func (r testPeriodicJobRegistrar) PeriodicJobs() ([]*river.PeriodicJob, error) {
	return r.jobs, nil
}

func newTestQueuePool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.New(
		context.Background(),
		"postgres://postgres:postgres@127.0.0.1:1/braidplane?sslmode=disable",
	)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return pool
}
