package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	requestrepo "github.com/Braidplane/braidplane-core/postgres/executionrequests"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/Braidplane/braidplane-core/service/worker/producer"
)

func TestProductionExecutionDispatcherSharesEnvelopeTransaction(t *testing.T) { // Shared real database.
	if testing.Short() {
		t.Skip("skipping real-Postgres production execution dispatcher test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "production execution dispatcher atomicity")
	txm := postgres.NewTransactionManager(pool)
	repo := requestrepo.New()
	org := slug.Slug(fmt.Sprintf("execution-dispatcher-atomicity-%d", time.Now().UnixNano()))
	_, err := pool.Exec(ctx, `INSERT INTO organizations (slug, name) VALUES ($1, $1)`, org)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, org)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, org)
		require.NoError(t, cleanupErr)
	})

	queueClient, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	dispatcher, err := producer.NewExecutionDispatcher(queue.NewEnqueuer(queueClient))
	require.NoError(t, err)

	dispatch := func(t *testing.T, requestID uuid.UUID, afterDispatch error) error {
		t.Helper()
		deadline := time.Now().Add(time.Minute)
		return txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
			if err := repo.Create(ctx, tx, requestrepo.Request{
				ID:                requestID,
				OrganizationSlug:  org.String(),
				Kind:              "connection_test",
				RequestCiphertext: []byte("encrypted"),
				DeadlineAt:        deadline,
			}, deadline.Add(time.Hour)); err != nil {
				return err
			}
			if err := dispatcher.Dispatch(ctx, tx, requestID); err != nil {
				return err
			}
			return afterDispatch
		})
	}
	assertCounts := func(t *testing.T, requestID uuid.UUID, expected int) {
		t.Helper()
		var envelopeCount, jobCount int
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT
				(SELECT count(*) FROM execution_requests WHERE id = $1),
				(SELECT count(*) FROM river_job
				 WHERE queue = $2
				   AND kind = $3
				   AND args = jsonb_build_object('request_id', $4::text))
		`, requestID, jobs.ExecutionQueue, jobs.ExecutionRequestArgs{}.Kind(), requestID.String()).
			Scan(&envelopeCount, &jobCount))
		require.Equal(t, expected, envelopeCount)
		require.Equal(t, expected, jobCount)
	}

	t.Run("commit persists envelope and production job", func(t *testing.T) { // Shared transaction fixture.
		requestID := uuid.New()
		require.NoError(t, dispatch(t, requestID, nil))
		t.Cleanup(func() {
			_, cleanupErr := pool.Exec(
				ctx,
				`DELETE FROM river_job WHERE kind = $1 AND args->>'request_id' = $2`,
				jobs.ExecutionRequestArgs{}.Kind(),
				requestID.String(),
			)
			require.NoError(t, cleanupErr)
			_, cleanupErr = pool.Exec(ctx, `DELETE FROM execution_requests WHERE id = $1`, requestID)
			require.NoError(t, cleanupErr)
		})
		assertCounts(t, requestID, 1)
	})

	t.Run("failure rolls back envelope and production job", func(t *testing.T) { // Shared transaction fixture.
		requestID := uuid.New()
		dispatchErr := xerror.Unexpected().Detail("fail after production dispatch")
		require.ErrorIs(t, dispatch(t, requestID, dispatchErr), dispatchErr)
		assertCounts(t, requestID, 0)
	})
}
