package datasourcessvc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/snowflakewif"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

// minimalConnector implements connector.Connector without any introspection interface.
// Used to verify that adapters return an error when the connector lacks the expected capability.
type minimalConnector struct{}

func (minimalConnector) Execute(
	_ context.Context,
	_ json.RawMessage,
	_ map[string]types.TypedParam,
	_ ...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	return &result.ExecutionResult{}, nil
}
func (minimalConnector) TestConnection(_ context.Context) error { return nil }
func (minimalConnector) Close() error                           { return nil }

var _ connector.Connector = minimalConnector{}

func requireOrganizationSlugInConnectorContext(t *testing.T, ctx context.Context, want slug.Slug) {
	t.Helper()

	var config map[string]any
	factory := connector.NewConnectorWithSnowflakeDefaults(
		func(_ context.Context, _ model.DataSourceType, configJSON, _ []byte) (connector.Connector, error) {
			require.NoError(t, json.Unmarshal(configJSON, &config))
			return minimalConnector{}, nil
		},
		connector.SnowflakeDefaults{
			WIFTokenFile:        "/var/run/braidplane/snowflake/identity.jwt",
			WIFOrganizationSlug: want,
		},
	)

	_, err := factory(ctx, model.DataSourceTypeSnowflake, []byte(`{}`), nil)
	require.NoError(t, err)
	require.Equal(t, "/var/run/braidplane/snowflake/identity.jwt", config["_wif_token_file"])
}

func requireCustomerIdentityInConnectorContext(t *testing.T, ctx context.Context) {
	t.Helper()
	called := false
	factory := connector.NewConnectorWithSnowflakeDefaults(func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		called = true
		return minimalConnector{}, nil
	}, connector.SnowflakeDefaults{WIFSocketPath: "/unused.sock", WIFResolve: func(_ context.Context, org, ds slug.Slug, account string) (snowflakewif.Identity, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Equal(t, slug.Slug("warehouse"), ds)
		require.Equal(t, "one", account)
		return snowflakewif.NewIdentity("https://issuer.test", org, ds, account)
	}})
	_, err := factory(ctx, model.DataSourceTypeSnowflake, []byte(`{"account":"one"}`), []byte(`{"auth_type":"workload_identity"}`))
	require.NoError(t, err)
	require.True(t, called, "authorized connection identity must reach the connector factory")
}

func TestLocalConnectionTestExecutorPassesOrganizationSlugToFactory(t *testing.T) {
	t.Parallel()

	want := slug.Slug("acme")
	factoryErr := xerror.Unexpected().Detail("factory failed")
	var factoryCtx context.Context
	executor := localConnectionTestExecutor{factory: func(
		ctx context.Context,
		_ model.DataSourceType,
		_, _ []byte,
	) (connector.Connector, error) {
		factoryCtx = ctx
		return nil, factoryErr
	}}

	err := executor.TestConnection(t.Context(), ConnectionTestRequest{
		OrganizationSlug: want,
		DataSourceSlug:   "warehouse",
		DataSourceType:   model.DataSourceTypeSnowflake,
	})

	require.ErrorIs(t, err, ErrConnectionFailed)
	require.ErrorIs(t, err, factoryErr)
	requireOrganizationSlugInConnectorContext(t, factoryCtx, want)
	requireCustomerIdentityInConnectorContext(t, factoryCtx)
}

func TestExecuteSchemaIntrospectionPassesOrganizationSlugToFactory(t *testing.T) {
	t.Parallel()

	want := slug.Slug("acme")
	factoryErr := xerror.Unexpected().Detail("factory failed")
	var factoryCtx context.Context
	_, err := ExecuteSchemaIntrospection(t.Context(), func(
		ctx context.Context,
		_ model.DataSourceType,
		_, _ []byte,
	) (connector.Connector, error) {
		factoryCtx = ctx
		return nil, factoryErr
	}, SchemaIntrospectionRequest{
		OrganizationSlug: want,
		DataSourceSlug:   "warehouse",
		DataSourceType:   model.DataSourceTypeSnowflake,
		MaxResultBytes:   1024,
		MaxResultItems:   10,
	})

	require.ErrorIs(t, err, ErrConnectionFailed)
	require.ErrorIs(t, err, factoryErr)
	requireOrganizationSlugInConnectorContext(t, factoryCtx, want)
	requireCustomerIdentityInConnectorContext(t, factoryCtx)
}

func TestSnowflakeAdapterConvertsSQLSchema(t *testing.T) {
	t.Parallel()

	conn := testutil.NewFakeSQLConnector()
	conn.IntrospectSchemaFn = func(context.Context) (*sqlconnector.Schema, error) {
		return &sqlconnector.Schema{Tables: []sqlconnector.Table{{
			Name:    "PUBLIC.ORDERS",
			Columns: []sqlconnector.Column{{Name: "ID", Type: "NUMBER", Nullable: false}},
		}}}, nil
	}
	adapter, err := adapterForType(model.DataSourceTypeSnowflake)
	require.NoError(t, err)

	result, err := adapter.introspect(context.Background(), conn, model.DataSourceTypeSnowflake)

	require.NoError(t, err)
	require.Equal(t, Schema[SQLSchemaPayload]{
		SchemaTypeValue: SchemaTypeSQL,
		Type:            model.DataSourceTypeSnowflake.String(),
		Payload: SQLSchemaPayload{Tables: []SchemaTable{{
			Name:    "PUBLIC.ORDERS",
			Columns: []SchemaColumn{{Name: "ID", Type: "NUMBER", Nullable: false}},
		}}},
	}, result)
}

type boundedHTTPIntrospector struct {
	minimalConnector
	operations []httpconnector.Operation
}

func (c boundedHTTPIntrospector) IntrospectSchema(context.Context) (*httpconnector.HTTPSchema, error) {
	return &httpconnector.HTTPSchema{Operations: c.operations}, nil
}

func TestExecuteSchemaIntrospectionRejectsOversizedWorkerResults(t *testing.T) {
	t.Parallel()

	factory := func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		return boundedHTTPIntrospector{operations: []httpconnector.Operation{
			{OperationID: "one", Path: "/one", Method: "GET", Description: "first"},
			{OperationID: "two", Path: "/two", Method: "GET", Description: "second"},
		}}, nil
	}

	_, err := ExecuteSchemaIntrospection(context.Background(), factory, SchemaIntrospectionRequest{
		DataSourceType: model.DataSourceTypeHTTP, MaxResultBytes: 1 << 20, MaxResultItems: 1,
	})
	require.ErrorContains(t, err, "schema result exceeds configured item limit")

	_, err = ExecuteSchemaIntrospection(context.Background(), factory, SchemaIntrospectionRequest{
		DataSourceType: model.DataSourceTypeHTTP, MaxResultBytes: 1, MaxResultItems: 10,
	})
	require.ErrorContains(t, err, "schema result exceeds configured byte limit")
}

func TestExecuteSchemaIntrospectionRejectsInvalidBudgetBeforeConnectorConstruction(t *testing.T) {
	t.Parallel()

	constructed := false
	factory := func(context.Context, model.DataSourceType, []byte, []byte) (connector.Connector, error) {
		constructed = true
		return minimalConnector{}, nil
	}

	_, err := ExecuteSchemaIntrospection(context.Background(), factory, SchemaIntrospectionRequest{
		DataSourceType: model.DataSourceTypeHTTP, MaxResultBytes: 0, MaxResultItems: 10,
	})
	require.ErrorContains(t, err, "byte limit")
	require.False(t, constructed)
}

func TestAdapterForType_UnsupportedIntrospector(t *testing.T) {
	t.Parallel()

	// Each data source type should select an adapter that rejects a connector
	// which does not implement the expected introspection interface.
	dsTypes := []model.DataSourceType{
		model.DataSourceTypePostgres,
		model.DataSourceTypeMySQL,
		model.DataSourceTypeSnowflake,
		model.DataSourceTypeMongoDB,
		model.DataSourceTypeHTTP,
		model.DataSourceTypeHubSpot,
		model.DataSourceTypeFTP,
		model.DataSourceTypeGit,
		model.DataSourceTypeGithub,
		model.DataSourceTypeGitlab,
	}

	for _, dsType := range dsTypes {
		t.Run(dsType.String(), func(t *testing.T) {
			t.Parallel()

			adapter, err := adapterForType(dsType)
			require.NoError(t, err)

			_, err = adapter.introspect(context.Background(), minimalConnector{}, dsType)

			require.Error(t, err)
			require.ErrorIs(t, err, ErrSchemaIntrospectionUnsupported)
			require.ErrorContains(t, err, "data source schema introspection unsupported")
		})
	}
}

func TestAdapterForType_InvalidType(t *testing.T) {
	t.Parallel()

	_, err := adapterForType(model.DataSourceTypeUnknown)

	require.ErrorIs(t, err, ErrInvalidDataSourceType)
}
