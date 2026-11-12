package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/executionbroker"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

const atomicityRequestTimeout = 2 * time.Second

func TestRiverDeliveryDistinguishesCompletedAndLiveLeaseRequests(t *testing.T) { // Shared real database.
	if testing.Short() {
		t.Skip("skipping real-Postgres River delivery test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "River execution delivery dispositions")
	org := slug.Slug(fmt.Sprintf("execution-river-delivery-%d", time.Now().UnixNano()))
	var organizationID int
	require.NoError(t, pool.QueryRow(
		ctx,
		`INSERT INTO organizations (slug, name) VALUES ($1, $1) RETURNING id`,
		org,
	).Scan(&organizationID))
	t.Cleanup(func() {
		_, cleanupErr := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id = $1`, organizationID)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, organizationID)
		require.NoError(t, cleanupErr)
	})

	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	broker, err := executionbroker.New(postgres.NewTransactionManager(pool), cipher, executionbroker.NewPollerDispatcher(), atomicityRequestTimeout, func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (connector.Connector, error) {
		return nil, xerror.Unexpected().Detail("connector creation should not be reached")
	}, nil, testutil.FakeOperationAdmission{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, broker.Close()) })

	insertRequest := func(t *testing.T, state string, leaseExpiresAt *time.Time) uuid.UUID {
		t.Helper()
		id := uuid.New()
		_, insertErr := pool.Exec(ctx, `
			INSERT INTO execution_requests (
				id, organization_id, kind, state, request_ciphertext,
				deadline_at, lease_expires_at, expires_at
			) VALUES ($1, $2, 'connection_test', $3, $4, $5, $6, $7)
		`, id, organizationID, state, []byte("encrypted"), time.Now().Add(time.Minute),
			leaseExpiresAt, time.Now().Add(time.Hour))
		require.NoError(t, insertErr)
		return id
	}

	t.Run("completed request is already done", func(t *testing.T) { // Shared transaction fixture.
		id := insertRequest(t, "succeeded", nil)
		result, processErr := broker.Process(ctx, id)
		require.NoError(t, processErr)
		require.Equal(t, executiondelivery.ProcessDone, result.Disposition)
	})

	t.Run("missing request is already done", func(t *testing.T) { // Shared transaction fixture.
		result, processErr := broker.Process(ctx, uuid.New())
		require.NoError(t, processErr)
		require.Equal(t, executiondelivery.ProcessDone, result.Disposition)
	})

	t.Run("live lease is retried at lease expiry", func(t *testing.T) { // Shared transaction fixture.
		leaseExpiresAt := time.Now().Add(20 * time.Second)
		id := insertRequest(t, "running", &leaseExpiresAt)
		result, processErr := broker.Process(ctx, id)
		require.NoError(t, processErr)
		require.Equal(t, executiondelivery.ProcessRetryAt, result.Disposition)
		require.WithinDuration(t, leaseExpiresAt, result.RetryAt, time.Millisecond)
	})
}

func TestExecutionEntryPointsHonorCallerCancellation(t *testing.T) { // Shared real database.
	if testing.Short() {
		t.Skip("skipping real-Postgres execution cancellation test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "execution entry point cancellation")
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	_, err = executionbroker.New(nil, cipher, executionbroker.NewPollerDispatcher(), atomicityRequestTimeout, nil, nil, testutil.FakeOperationAdmission{})
	require.Error(t, err)
	_, err = executionbroker.New(postgres.NewTransactionManager(pool), cipher, executionbroker.NewPollerDispatcher(), 0, nil, nil, testutil.FakeOperationAdmission{})
	require.Error(t, err)
	broker, err := executionbroker.New(postgres.NewTransactionManager(pool), cipher, executionbroker.NewPollerDispatcher(), atomicityRequestTimeout, nil, nil, testutil.FakeOperationAdmission{})
	require.NoError(t, err)

	require.Error(t, broker.Start(ctx, 1))
	require.Error(t, broker.PreflightProcessor(ctx))
	require.Error(t, broker.StartProcessorObservability(ctx, 0))
	processorBroker, err := executionbroker.New(postgres.NewTransactionManager(pool), cipher, executionbroker.NewPollerDispatcher(), atomicityRequestTimeout, func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		return nil, xerror.Unexpected().Detail("connector factory should not be called during preflight")
	}, nil, testutil.FakeOperationAdmission{})
	require.NoError(t, err)
	require.NoError(t, processorBroker.PreflightProcessor(ctx))
	require.Error(t, processorBroker.Start(ctx, 0))

	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	require.NoError(t, processorBroker.StartProcessorObservability(cancelledCtx, 1))
	require.Eventually(t, func() bool {
		return cancelledCtx.Err() != nil
	}, time.Second, time.Millisecond)
	org := slug.Slug("cancelled-execution")
	assertCancelled := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
	}

	_, err = broker.ExecuteConnector(cancelledCtx, queriessvc.ConnectorExecutionRequest{OrganizationSlug: org})
	assertCancelled(t, err)
	_, err = broker.ListGitBranches(cancelledCtx, datasourcessvc.GitBranchRequest{OrganizationSlug: org})
	assertCancelled(t, err)
	_, err = broker.ExecuteGitSourceSnapshot(cancelledCtx, pipelinegit.RemoteSourceRequest{OrganizationSlug: org})
	assertCancelled(t, err)
	assertCancelled(t, broker.ExecuteGitWrite(cancelledCtx, pipelinegit.RemoteWriteRequest{
		Operation: pipelinegit.WriteFileOperation{OrganizationSlug: org},
	}))
	assertCancelled(t, broker.ExecuteGitDelete(cancelledCtx, pipelinegit.RemoteDeleteRequest{
		Operation: pipelinegit.DeleteFileOperation{OrganizationSlug: org},
	}))
	_, err = broker.ExecuteGitProposal(cancelledCtx, pipelinegit.RemoteProposalRequest{
		Operation: pipelinegit.ProposalOperation{OrganizationSlug: org},
	})
	assertCancelled(t, err)
	_, err = broker.IntrospectSchema(cancelledCtx, datasourcessvc.SchemaIntrospectionRequest{OrganizationSlug: org})
	assertCancelled(t, err)
	_, err = broker.ExecuteTransform(cancelledCtx, pipelineruntime.TransformExecutionRequest{OrganizationSlug: org})
	assertCancelled(t, err)
}
