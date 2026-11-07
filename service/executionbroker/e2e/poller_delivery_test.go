package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
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
	api, err := executionbroker.New(txm, cipher, executionbroker.NewPollerDispatcher(), 10*time.Second, nil, nil, testutil.FakeOperationAdmission{}, filestream.Profile{})
	require.NoError(t, err)
	entered := make(chan struct{})
	release := make(chan struct{})
	type received struct {
		kind                model.DataSourceType
		config, credentials []byte
	}
	delivered := make(chan received, 1)
	executor, err := executionbroker.New(txm, cipher, executionbroker.NewPollerDispatcher(), 10*time.Second, func(_ context.Context, kind model.DataSourceType, config, credentials []byte) (connector.Connector, error) {
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
	}, nil, testutil.FakeOperationAdmission{}, filestream.Profile{})
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
	close(release)
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("synchronous caller did not receive executor result")
	}
	require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM execution_requests WHERE organization_id=$1`, organization).Scan(&state))
	require.Equal(t, "succeeded", state)
}
