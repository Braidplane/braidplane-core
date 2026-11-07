package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	workerhostsvc "github.com/Braidplane/braidplane-core/service/worker"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type heartbeatExecutionProcessor struct{}

func (heartbeatExecutionProcessor) Process(
	context.Context,
	uuid.UUID,
) (executiondelivery.ProcessResult, error) {
	return executiondelivery.ProcessResult{}, nil
}

func TestRiverQueueHeartbeatReportsWorkerLiveness(t *testing.T) { // Shared real database.
	if testing.Short() {
		t.Skip("skipping real-Postgres River heartbeat test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "River queue heartbeat")
	now := time.Now().UTC()
	staleQueue := fmt.Sprintf("river_metrics_stale_%d", now.UnixNano())

	registrar, err := workerhostsvc.NewExecutionRegistrar(heartbeatExecutionProcessor{}, 1)
	require.NoError(t, err)
	client, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Logger:     slog.New(slog.DiscardHandler),
		Queues:     registrar.Queues(),
		Registrars: []queue.WorkerRegistrar{registrar},
	})
	require.NoError(t, err)
	require.NoError(t, client.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelStop()
		require.NoError(t, client.Stop(stopCtx))
	})

	_, err = pool.Exec(ctx, `
		INSERT INTO river_queue (name, updated_at)
		VALUES ($1, $2::timestamptz - INTERVAL '16 minutes')
	`, staleQueue, now)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(
			context.Background(),
			`DELETE FROM river_queue WHERE name = ANY($1::text[])`,
			[]string{staleQueue},
		)
		require.NoError(t, cleanupErr)
	})

	provider := metrics.NewRiverQueueHealthPostgresProvider(
		pool,
		metrics.RiverQueueHealthPostgresConfig{
			AllowedQueues: []string{jobs.ExecutionQueue, staleQueue},
			Now:           func() time.Time { return now },
		},
	)
	workers := provider(ctx).Workers
	require.Len(t, workers, 2)

	byQueue := make(map[string]metrics.RiverWorkerMetric, len(workers))
	for _, worker := range workers {
		byQueue[worker.Queue] = worker
	}
	require.Equal(t, int64(1), byQueue[jobs.ExecutionQueue].ActiveWorkers)
	require.Equal(t, int64(1), byQueue[jobs.ExecutionQueue].MaxWorkers)
	require.Less(t, byQueue[jobs.ExecutionQueue].LastHeartbeatAge, time.Minute)
	require.Zero(t, byQueue[staleQueue].ActiveWorkers)
	require.Equal(t, int64(1), byQueue[staleQueue].MaxWorkers)
	require.Equal(t, 16*time.Minute, byQueue[staleQueue].LastHeartbeatAge)
}
