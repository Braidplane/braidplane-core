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
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/executionbroker"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
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

	_, err = pool.Exec(ctx, `
        WITH c AS (
            INSERT INTO credentials (organization_id, slug, kind, credential_type, status, encrypted_payload)
            SELECT id, 'broker-credential', 'static_secret', 'postgres', 'active', decode('66697874757265', 'hex')
            FROM organizations WHERE slug = $1 RETURNING id, organization_id
        )
        INSERT INTO data_sources (organization_id, credential_id, name, slug, type, config, is_active)
        SELECT organization_id, id, 'Broker fixture', 'broker-source', 'postgres', '{}'::jsonb, true FROM c`, org)
	require.NoError(t, err)

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

	for _, failDispatch := range []bool{false, true} {
		t.Run(fmt.Sprintf("credential audit with dispatch failure %t", failDispatch), func(t *testing.T) {
			ctx := reqctx.WithAuditActor(ctx, model.UserEventActor("broker-operator"))
			dispatcher := &capturingExecutionDispatcher{delegate: riverDispatcher}
			if failDispatch {
				dispatcher.afterEnqueue = xerror.Unexpected().Detail("synthetic dispatch failure")
			}
			broker, err := executionbroker.New(postgres.NewTransactionManager(pool), cipher, dispatcher, brokerAtomicityRequestTimeout, nil, nil, testutil.FakeOperationAdmission{})
			require.NoError(t, err)
			const marker = "synthetic-broker-audit-credential"
			_, err = broker.ExecuteConnector(ctx, queriessvc.ConnectorExecutionRequest{
				OrganizationSlug: org, DataSourceSlug: "broker-source", DataSourceType: model.DataSourceTypePostgres,
				CredentialReference: "broker-credential/2026-06-01T12:00:00Z", CredentialsJSON: []byte(marker),
				ConcurrencyLimit: 1, ConcurrencyQueueLimit: 1, ConcurrencyQueueTimeout: time.Second,
			})
			require.NotEqual(t, uuid.Nil, dispatcher.requestID)
			requestID := dispatcher.requestID.String()
			var count int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM events e
                JOIN organizations o ON o.id = e.organization_id WHERE o.slug = $1 AND e.request_id = $2`,
				org, requestID).Scan(&count))
			if failDispatch {
				require.ErrorIs(t, err, dispatcher.afterEnqueue)
				assertCounts(t, dispatcher.requestID, 0, 0)
				require.Zero(t, count)
				return
			}
			t.Cleanup(func() { cleanupRequest(t, dispatcher.requestID) })
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assertCounts(t, dispatcher.requestID, 1, 1)
			require.Equal(t, 1, count)
			var version, metadata string
			require.NoError(t, pool.QueryRow(ctx, `SELECT metadata->>'version', metadata::text FROM events
                WHERE request_id = $1 AND event_class = 'security_audit' AND entity_slug = 'broker-source'
                  AND action = 'read' AND actor_slug = 'broker-operator' AND metadata->>'outcome' = 'enveloped'`,
				requestID).Scan(&version, &metadata))
			require.Len(t, version, 64)
			require.NotContains(t, metadata, marker)
		})
	}

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
