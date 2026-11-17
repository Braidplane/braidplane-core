package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/executionbroker"
)

func TestPollerConnectionTestPreservesSynchronousSemantics(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ctx = reqctx.WithAuditOperation(ctx, "synthetic-connection-test-request")
	pool := e2edb.OpenPostgresPool(t, "poller connection test")
	var organization int
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO organizations(slug,name) VALUES('poller-roundtrip','Poller') RETURNING id`).Scan(&organization))
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM execution_requests WHERE organization_id=$1`, organization)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id=$1`, organization)
		require.NoError(t, err)
	})
	cipher, err := crypto.NewAES256(make([]byte, 32))
	require.NoError(t, err)
	txm := postgres.NewTransactionManager(pool)
	api, err := executionbroker.New(txm, cipher, executionbroker.NewPollerDispatcher(), 10*time.Second, nil, nil, testutil.FakeOperationAdmission{})
	require.NoError(t, err)
	entered := make(chan struct{})
	release := make(chan struct{})
	type received struct {
		kind                model.DataSourceType
		config, credentials []byte
	}
	delivered := make(chan received, 1)
	const rejectedMarker = "synthetic-rejected-only"
	executor, err := executionbroker.New(txm, cipher, executionbroker.NewPollerDispatcher(), 10*time.Second, func(_ context.Context, kind model.DataSourceType, config, credentials []byte) (connector.Connector, error) {
		if string(credentials) == `{"password":"`+rejectedMarker+`"}` {
			return nil, xerror.InvalidCredentials().Detail(rejectedMarker)
		}
		delivered <- received{kind, append([]byte(nil), config...), append([]byte(nil), credentials...)}
		return &testutil.FakeSQLConnector{TestConnectionFn: func(ctx context.Context) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}, nil
	}, nil, testutil.FakeOperationAdmission{})
	require.NoError(t, err)
	defer func() { require.NoError(t, executor.Close()) }()
	require.NoError(t, executor.Start(ctx, 1))
	request := datasourcessvc.ConnectionTestRequest{OrganizationSlug: "poller-roundtrip", DataSourceType: model.DataSourceTypePostgres, ConfigJSON: []byte(`{"host":"synthetic"}`), CredentialsJSON: []byte(`{"password":"synthetic-only"}`)}
	finished := make(chan error, 1)
	go func() { finished <- api.TestConnection(ctx, request) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("executor did not receive persisted connection test")
	}
	payload := <-delivered
	require.Equal(t, request.DataSourceType, payload.kind)
	require.Equal(t, request.ConfigJSON, payload.config)
	require.Equal(t, request.CredentialsJSON, payload.credentials)
	select {
	case err := <-finished:
		t.Fatalf("synchronous caller returned before executor finished: %v", err)
	default:
	}
	var state string
	var encrypted bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT state,position('synthetic-only'::bytea IN request_ciphertext)=0 FROM execution_requests WHERE organization_id=$1`, organization).Scan(&state, &encrypted))
	require.Equal(t, "running", state)
	require.True(t, encrypted)
	var version, metadata, parentOperation string
	require.NoError(t, pool.QueryRow(ctx, `SELECT metadata->>'version', metadata::text, metadata->>'parent_operation_id' FROM events
        WHERE organization_id=$1 AND entity_type='organization' AND entity_slug='poller-roundtrip'
        AND event_class='security_audit' AND action='read' AND metadata->>'operation'='connection_test'
        AND metadata->>'outcome'='enveloped'`, organization).Scan(&version, &metadata, &parentOperation))
	require.Empty(t, version)
	require.Equal(t, reqctx.AuditOperation(ctx), parentOperation)
	require.NotContains(t, metadata, "synthetic-only")
	close(release)
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("synchronous caller did not receive executor result")
	}
	require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM execution_requests WHERE organization_id=$1`, organization).Scan(&state))
	require.Equal(t, "succeeded", state)

	request.CredentialsJSON = []byte(`{"password":"` + rejectedMarker + `"}`)
	err = api.TestConnection(ctx, request)
	require.ErrorIs(t, err, datasourcessvc.ErrConnectionFailed)
	require.NotContains(t, err.Error(), rejectedMarker)
	require.NotContains(t, xerror.PublicDetail(err), rejectedMarker)

	var resultCiphertext []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT result_ciphertext FROM execution_requests
        WHERE organization_id=$1 AND state='failed'`, organization).Scan(&resultCiphertext))
	result, err := cipher.Decrypt(resultCiphertext)
	require.NoError(t, err)
	defer clear(result)
	require.Contains(t, string(result), "connection_failed")
	require.NotContains(t, string(result), rejectedMarker)
}
