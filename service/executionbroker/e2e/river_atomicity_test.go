package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/executionbroker"
)

const (
	brokerAtomicityRequestTimeout = 2 * time.Second
	executionQueue                = "execution"
	executionRequestKind          = "execution_request"
)

type executionRequestArgs struct {
	RequestID uuid.UUID `json:"request_id" river:"unique"`
}

func (executionRequestArgs) Kind() string {
	return executionRequestKind
}

func (executionRequestArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: executionQueue,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: rivertype.UniqueOptsByStateDefault(),
		},
	}
}

type riverExecutionDispatcher struct {
	enqueuer queue.Enqueuer
}

func (d riverExecutionDispatcher) Dispatch(
	ctx context.Context,
	tx postgres.DBTX,
	requestID uuid.UUID,
) error {
	_, err := d.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args:  executionRequestArgs{RequestID: requestID},
		Queue: executionQueue,
	})
	return err
}

type capturingExecutionDispatcher struct {
	delegate     executionbroker.Dispatcher
	requestID    uuid.UUID
	afterEnqueue error
}

func (d *capturingExecutionDispatcher) Dispatch(
	ctx context.Context,
	tx postgres.DBTX,
	requestID uuid.UUID,
) error {
	d.requestID = requestID
	if err := d.delegate.Dispatch(ctx, tx, requestID); err != nil {
		return err
	}
	return d.afterEnqueue
}

func TestRiverSubmissionCommitsEnvelopeAndJobAtomically(t *testing.T) { // Shared real database.
	if testing.Short() {
		t.Skip("skipping real-Postgres River submission test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "River execution submission atomicity")
	org := slug.Slug(fmt.Sprintf("execution-river-atomicity-%d", time.Now().UnixNano()))
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
	riverDispatcher := riverExecutionDispatcher{enqueuer: queue.NewEnqueuer(queueClient)}
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)

	checkCounts := func(requestID uuid.UUID, envelopes, expectedJobs int) error {
		var envelopeCount, jobCount int
		if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT count(*) FROM execution_requests WHERE id = $1),
				(SELECT count(*) FROM river_job
				 WHERE queue = $2 AND kind = $3 AND args->>'request_id' = $4)
		`, requestID, executionQueue, executionRequestKind, requestID.String()).
			Scan(&envelopeCount, &jobCount); err != nil {
			return err
		}
		if envelopeCount != envelopes || jobCount != expectedJobs {
			return fmt.Errorf(
				"unexpected execution counts: envelopes=%d jobs=%d",
				envelopeCount,
				jobCount,
			)
		}
		return nil
	}
	assertCounts := func(t *testing.T, requestID uuid.UUID, envelopes, expectedJobs int) {
		t.Helper()
		require.NoError(t, checkCounts(requestID, envelopes, expectedJobs))
	}
	cleanupRequest := func(t *testing.T, requestID uuid.UUID) {
		t.Helper()
		cleanupCtx, cancelCleanup := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelCleanup()
		_, err := postgres.InTx(cleanupCtx, postgres.NewTransactionManager(pool), func(
			ctx context.Context,
			tx postgres.DBTX,
		) (struct{}, error) {
			if _, err := tx.Exec(
				ctx,
				`DELETE FROM river_job WHERE kind = $1 AND args->>'request_id' = $2`,
				executionRequestKind,
				requestID.String(),
			); err != nil {
				return struct{}{}, err
			}
			_, err := tx.Exec(ctx, `DELETE FROM execution_requests WHERE id = $1`, requestID)
			return struct{}{}, err
		})
		require.NoError(t, err)
	}

	t.Run("commit exposes both envelope and job", func(t *testing.T) { // Shared transaction fixture.
		dispatcher := &capturingExecutionDispatcher{delegate: riverDispatcher}
		broker, brokerErr := executionbroker.New(postgres.NewTransactionManager(pool), cipher, dispatcher, brokerAtomicityRequestTimeout, nil, nil, testutil.FakeOperationAdmission{})
		require.NoError(t, brokerErr)

		err := broker.TestConnection(ctx, datasourcessvc.ConnectionTestRequest{OrganizationSlug: org})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.NotEqual(t, uuid.Nil, dispatcher.requestID)
		t.Cleanup(func() {
			cleanupRequest(t, dispatcher.requestID)
		})
		assertCounts(t, dispatcher.requestID, 1, 1)
	})

	t.Run("caller transaction cannot hide envelope and job", func(t *testing.T) { // Shared transaction fixture.
		dispatcher := &capturingExecutionDispatcher{delegate: riverDispatcher}
		txm := postgres.NewTransactionManager(pool)
		broker, brokerErr := executionbroker.New(txm, cipher, dispatcher, brokerAtomicityRequestTimeout, nil, nil, testutil.FakeOperationAdmission{})
		require.NoError(t, brokerErr)

		t.Cleanup(func() {
			if dispatcher.requestID != uuid.Nil {
				cleanupRequest(t, dispatcher.requestID)
			}
		})
		err := txm.RunInTx(ctx, func(txCtx context.Context, _ postgres.DBTX) error {
			testErr := broker.TestConnection(
				txCtx,
				datasourcessvc.ConnectionTestRequest{OrganizationSlug: org},
			)
			if !xerror.Is(testErr, context.DeadlineExceeded) {
				return fmt.Errorf("test connection: %w", testErr)
			}
			if dispatcher.requestID == uuid.Nil {
				return fmt.Errorf("execution request ID was not captured")
			}
			return checkCounts(dispatcher.requestID, 1, 1)
		})
		require.NoError(t, err)
	})

	t.Run("dispatch failure rolls back both envelope and job", func(t *testing.T) { // Shared transaction fixture.
		dispatchErr := xerror.Unexpected().Detail("dispatch failed after River insert")
		dispatcher := &capturingExecutionDispatcher{
			delegate:     riverDispatcher,
			afterEnqueue: dispatchErr,
		}
		broker, brokerErr := executionbroker.New(postgres.NewTransactionManager(pool), cipher, dispatcher, brokerAtomicityRequestTimeout, nil, nil, testutil.FakeOperationAdmission{})
		require.NoError(t, brokerErr)

		err := broker.TestConnection(ctx, datasourcessvc.ConnectionTestRequest{OrganizationSlug: org})
		require.ErrorIs(t, err, dispatchErr)
		require.NotEqual(t, uuid.Nil, dispatcher.requestID)
		assertCounts(t, dispatcher.requestID, 0, 0)
	})
}
