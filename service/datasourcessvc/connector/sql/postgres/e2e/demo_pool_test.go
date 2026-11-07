package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/postgres"
	"github.com/stretchr/testify/require"
)

func TestPrivatePolicyGrantReauthorizesLiveConnector(t *testing.T) {
	ctx := connector.WithDataSourceIdentity(transportsecurity.WithPlaintext(t.Context()), "test-org", "test-source")
	addr, err := netip.ParseAddrPort(fmt.Sprintf("127.0.0.1:%d", connectorPort()))
	require.NoError(t, err)
	var allowed atomic.Bool
	allowed.Store(true)
	var authorizations atomic.Int32
	policy := func(_ context.Context, org, source slug.Slug, kind model.DataSourceType, _, _ []byte) (connector.PrivatePostgresEndpoint, error) {
		require.Equal(t, slug.Slug("test-org"), org)
		require.Equal(t, slug.Slug("test-source"), source)
		require.Equal(t, model.DataSourceTypePostgres, kind)
		return connector.PrivatePostgresEndpoint{Host: "localhost", Address: addr, Authorize: func(context.Context) error {
			authorizations.Add(1)
			if !allowed.Load() {
				return xerror.ValidationFailed().Detail("fixture grant revoked")
			}
			return nil
		}}, nil
	}
	config, err := json.Marshal(map[string]any{"host": "localhost", "port": connectorPort(), "database": "braidplane", "ssl_mode": "disable"})
	require.NoError(t, err)
	credentials, err := json.Marshal(map[string]string{"username": "postgres", "password": "postgres"})
	require.NoError(t, err)
	factory := connector.NewConnectorWithPrivatePostgresPolicy(connector.NewConnector, policy)
	conn, err := factory(ctx, model.DataSourceTypePostgres, config, credentials)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	require.NoError(t, conn.TestConnection(ctx))
	query := json.RawMessage(`{"type":"postgres","sql":"SELECT 1"}`)
	result, err := conn.Execute(ctx, query, nil)
	require.NoError(t, err)
	require.NoError(t, result.Error())
	require.EqualValues(t, 2, authorizations.Load())
	allowed.Store(false)
	require.ErrorContains(t, conn.TestConnection(ctx), "fixture grant revoked")
	result, err = conn.Execute(ctx, query, nil)
	require.NoError(t, err)
	require.ErrorContains(t, result.Error(), "fixture grant revoked")
	require.EqualValues(t, 4, authorizations.Load())
}

func TestDemoPoolReleasesIdleFixtureConnections(t *testing.T) {
	fixtureCtx := transportsecurity.WithPlaintext(t.Context())
	admin, err := postgres.New(fixtureCtx, getTestConfig())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	for _, guarded := range []bool{false, true} {
		name, wantIdle := "ordinary source retains idle connection", 1
		if guarded {
			name, wantIdle = "demo source releases idle connection", 0
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(fixtureCtx)
			defer cancel()
			role, password := "demo_pool_"+strings.ToLower(rand.Text()), rand.Text()
			_, err := executeSQL(ctx, admin, fmt.Sprintf("CREATE ROLE %s LOGIN CONNECTION LIMIT 1 PASSWORD '%s'", role, password), nil)
			require.NoError(t, err)
			t.Cleanup(func() {
				_, err := executeSQL(fixtureCtx, admin, "DROP ROLE "+role, nil)
				require.NoError(t, err)
			})
			config := getTestConfig()
			config.MaxConns = 1
			config.User, config.Password = role, password
			if guarded {
				config.Authorize = func(context.Context) error { return nil }
			}
			conn, err := postgres.New(ctx, config)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, conn.Close()) })
			require.Equal(t, wantIdle, conn.SQLPoolStats().Idle, "startup ping")
			for range 5 {
				require.NoError(t, conn.TestConnection(ctx))
				require.Equal(t, wantIdle, conn.SQLPoolStats().Idle, "saved connection test")
				_, err = executeSQL(ctx, conn, "SELECT 1", nil)
				require.NoError(t, err)
				require.Equal(t, wantIdle, conn.SQLPoolStats().Idle, "completed provider operation")
			}
		})
	}
}
