package connector

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestPrivatePostgresPolicyDoesNotReuseEarlierGrant(t *testing.T) {
	t.Parallel()
	endpoint := PrivatePostgresEndpoint{Address: netip.MustParseAddrPort("172.20.0.8:5432")}
	baseCalled := false
	base := func(ctx context.Context, _ model.DataSourceType, _, _ []byte) (Connector, error) {
		baseCalled = true
		grant, ok := ctx.Value(policyPostgresContextKey{}).(PrivatePostgresEndpoint)
		require.True(t, ok)
		require.False(t, grant.Address.IsValid())
		return noopTestConnector{}, nil
	}
	policy := func(ctx context.Context, org, source slug.Slug, _ model.DataSourceType, _, _ []byte) (PrivatePostgresEndpoint, error) {
		require.Equal(t, slug.Slug("org"), org)
		require.Equal(t, slug.Slug("source"), source)
		grant := ctx.Value(policyPostgresContextKey{}).(PrivatePostgresEndpoint)
		require.False(t, grant.Address.IsValid())
		return PrivatePostgresEndpoint{}, nil
	}
	ctx := context.WithValue(WithDataSourceIdentity(t.Context(), "org", "source"), policyPostgresContextKey{}, endpoint)
	_, err := NewConnectorWithPrivatePostgresPolicy(base, policy)(ctx, model.DataSourceTypePostgres, nil, nil)
	require.NoError(t, err)
	require.True(t, baseCalled)
	_, err = NewConnectorWithPrivatePostgresPolicy(base, nil)(ctx, model.DataSourceTypePostgres, nil, nil)
	require.Error(t, err)
	_, err = NewConnectorWithPrivatePostgresPolicy(base, func(context.Context, slug.Slug, slug.Slug, model.DataSourceType, []byte, []byte) (PrivatePostgresEndpoint, error) {
		return endpoint, nil
	})(ctx, model.DataSourceTypePostgres, nil, nil)
	require.ErrorContains(t, err, "requires authorization")
}

func TestPrivatePostgresScope(t *testing.T) {
	t.Parallel()
	endpoint := PrivatePostgresEndpoint{OrganizationSlug: slug.FromString("fixture-org"), Host: "fixture-db", Address: netip.MustParseAddrPort("172.20.0.8:5432")}
	for _, tt := range []struct {
		name, org, host string
		port            int
		kind            model.DataSourceType
		allowed         bool
	}{
		{"match", "fixture-org", "fixture-db", 5432, model.DataSourceTypePostgres, true},
		{"other tenant", "another-org", "fixture-db", 5432, model.DataSourceTypePostgres, false},
		{"missing tenant", "", "fixture-db", 5432, model.DataSourceTypePostgres, false},
		{"other host", "fixture-org", "application-db", 5432, model.DataSourceTypePostgres, false},
		{"literal bypass", "fixture-org", "172.20.0.8", 5432, model.DataSourceTypePostgres, false},
		{"other port", "fixture-org", "fixture-db", 5433, model.DataSourceTypePostgres, false},
		{"other connector", "fixture-org", "fixture-db", 5432, model.DataSourceTypeMySQL, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := json.Marshal(map[string]any{"host": tt.host, "port": tt.port, "allow_private_hosts": false})
			require.NoError(t, err)
			factory := NewConnectorWithPrivatePostgres(func(ctx context.Context, _ model.DataSourceType, got, _ []byte) (Connector, error) {
				require.Equal(t, cfg, got, "runtime exception must not be persisted in source configuration")
				granted, _ := ctx.Value(privatePostgresContextKey{}).(PrivatePostgresEndpoint)
				require.Equal(t, tt.allowed, granted.Address.IsValid())
				return noopTestConnector{}, nil
			}, endpoint)
			// Even a reused context must not retain a prior call's exception.
			ctx := context.WithValue(context.Background(), privatePostgresContextKey{}, endpoint)
			ctx = WithOrganizationSlug(ctx, slug.FromString(tt.org))
			_, err = factory(ctx, tt.kind, cfg, []byte(`{}`))
			require.NoError(t, err)
		})
	}
}

func TestPrivatePostgresPinsSocket(t *testing.T) {
	t.Parallel()
	endpoint := PrivatePostgresEndpoint{Address: netip.MustParseAddrPort("172.20.0.8:5432")}
	for _, tt := range []struct {
		name, network, address string
		allowed                bool
	}{
		{"match", "tcp", "172.20.0.8:5432", true},
		{"DNS changed to other private service", "tcp", "172.20.0.9:5432", false},
		{"DNS changed to public service", "tcp", "8.8.8.8:5432", false},
		{"metadata", "tcp", "169.254.169.254:5432", false},
		{"loopback", "tcp", "127.0.0.1:5432", false},
		{"port", "tcp", "172.20.0.8:5433", false},
		{"unresolved host", "tcp", "fixture-db:5432", false},
		{"Unix socket", "unix", "172.20.0.8:5432", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			called := false
			_, err := endpoint.dial(context.Background(), tt.network, tt.address, func(_ context.Context, network, address string) (net.Conn, error) {
				called = true
				require.Equal(t, "tcp", network)
				require.Equal(t, endpoint.Address.String(), address)
				client, server := net.Pipe()
				t.Cleanup(func() { require.NoError(t, client.Close()); require.NoError(t, server.Close()) })
				return client, nil
			})
			require.Equal(t, tt.allowed, called)
			if tt.allowed {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestPrivatePostgresPooledExecutionRemainsTenantScoped(t *testing.T) {
	t.Parallel()
	endpoint := PrivatePostgresEndpoint{OrganizationSlug: slug.FromString("fixture-org"), Host: "fixture-db", Address: netip.MustParseAddrPort("172.20.0.8:5432")}
	var allowed []bool
	factory := NewConnectorWithPrivatePostgres(func(ctx context.Context, _ model.DataSourceType, _, _ []byte) (Connector, error) {
		grant, _ := ctx.Value(privatePostgresContextKey{}).(PrivatePostgresEndpoint)
		allowed = append(allowed, grant.Address.IsValid())
		return &fakeRuntimeConnector{}, nil
	}, endpoint)
	manager, err := NewManager(factory, RuntimePoolSettings{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	for _, org := range []string{"fixture-org", "other-org", "fixture-org"} {
		identity := testRuntimeIdentity("config-1", "credential-1")
		identity.OrganizationSlug = slug.FromString(org)
		_, release, err := manager.Acquire(t.Context(), identity, []byte(`{"host":"fixture-db","port":5432}`), []byte(`{}`))
		require.NoError(t, err)
		require.NoError(t, release())
	}
	require.Equal(t, []bool{true, false}, allowed, "cached private connection must never be leased by another tenant")
}

func TestPrivatePostgresDisabled(t *testing.T) {
	t.Parallel()
	factory := NewConnectorWithPrivatePostgres(func(ctx context.Context, _ model.DataSourceType, _, _ []byte) (Connector, error) {
		grant, _ := ctx.Value(privatePostgresContextKey{}).(PrivatePostgresEndpoint)
		require.False(t, grant.Address.IsValid())
		return noopTestConnector{}, nil
	}, PrivatePostgresEndpoint{})
	_, err := factory(WithOrganizationSlug(t.Context(), slug.FromString("fixture-org")), model.DataSourceTypePostgres, []byte(`{"host":"fixture-db","port":5432}`), nil)
	require.NoError(t, err)
}

func TestPrivatePostgresPreservesMalformedConfigError(t *testing.T) {
	t.Parallel()
	endpoint := PrivatePostgresEndpoint{OrganizationSlug: slug.FromString("fixture-org"), Host: "fixture-db", Address: netip.MustParseAddrPort("172.20.0.8:5432")}
	factory := NewConnectorWithPrivatePostgres(NewConnector, endpoint)
	for _, org := range []string{"fixture-org", "other-org"} {
		t.Run(org, func(t *testing.T) {
			t.Parallel()
			_, err := factory(WithOrganizationSlug(t.Context(), slug.FromString(org)), model.DataSourceTypePostgres, []byte(`{"port":`), []byte(`{}`))
			require.ErrorIs(t, err, xerror.ValidationFailed())
		})
	}
}

func TestPrivatePostgresGrantRequiresExactSourceIdentity(t *testing.T) {
	endpoint := PrivatePostgresEndpoint{OrganizationSlug: "customer", SourceSlug: "warehouse", Host: "warehouse.internal", Address: netip.MustParseAddrPort("10.20.0.8:5432")}
	for _, tc := range []struct {
		org, source string
		allowed     bool
	}{
		{"customer", "warehouse", true},
		{"customer", "other", false},
		{"other", "warehouse", false},
		{"customer", "", false},
	} {
		factory := NewConnectorWithPrivatePostgres(func(ctx context.Context, _ model.DataSourceType, _, _ []byte) (Connector, error) {
			grant, _ := ctx.Value(privatePostgresContextKey{}).(PrivatePostgresEndpoint)
			require.Equal(t, tc.allowed, grant.Address.IsValid())
			return noopTestConnector{}, nil
		}, endpoint)
		ctx := WithDataSourceIdentity(t.Context(), slug.FromString(tc.org), slug.FromString(tc.source))
		_, err := factory(ctx, model.DataSourceTypePostgres, []byte(`{"host":"warehouse.internal","port":5432}`), nil)
		require.NoError(t, err)
	}
}
