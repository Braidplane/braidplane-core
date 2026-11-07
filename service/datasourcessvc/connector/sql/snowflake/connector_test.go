package snowflake

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	snowflakeclient "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/snowflake/client"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestPrepareStatementBindsValuesWithoutInterpolation(t *testing.T) {
	t.Parallel()

	statement, args, err := prepareStatement(
		json.RawMessage(`{"type":"snowflake","sql":"SELECT * FROM orders WHERE region = {{region}} AND id = {{id}}"}`),
		map[string]any{"region": "EU' OR 1=1 --", "id": int64(42)},
	)

	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM orders WHERE region = ? AND id = ?", statement)
	require.Equal(t, []any{"EU' OR 1=1 --", int64(42)}, args)
	require.NotContains(t, statement, "OR 1=1")
}

func TestBindNamedParamsPreservesInterleavedOccurrenceOrder(t *testing.T) {
	t.Parallel()

	statement, args, err := bindNamedParams(
		"SELECT {{a}}, {{b}}, {{a}}",
		map[string]any{"a": "first", "b": "second"},
	)

	require.NoError(t, err)
	require.Equal(t, "SELECT ?, ?, ?", statement)
	require.Equal(t, []any{"first", "second", "first"}, args)
}

func TestPrepareStatementBuildsNativeInputRelationForSinkRows(t *testing.T) {
	t.Parallel()

	statement, args, err := prepareStatement(
		json.RawMessage(`{"type":"snowflake","sql":"INSERT INTO orders SELECT * FROM input"}`),
		map[string]any{
			sinkbinding.InternalInputRows:    []map[string]any{{"id": int64(42), "region": "EU"}},
			sinkbinding.InternalInputColumns: []string{"id", "region"},
		},
	)

	require.NoError(t, err)
	require.Equal(t, `INSERT INTO orders WITH input AS (SELECT value:"id"::NUMBER AS "id", value:"region"::STRING AS "region" FROM TABLE(FLATTEN(INPUT => PARSE_JSON(?)))) SELECT * FROM input`, statement)
	require.Len(t, args, 1)
	require.JSONEq(t, `[{"id":42,"region":"EU"}]`, args[0].(string))
}

func TestPrepareStatementPlacesInputRelationAfterQuotedSelectIdentifier(t *testing.T) {
	t.Parallel()

	statement, args, err := prepareStatement(
		json.RawMessage(`{"type":"snowflake","sql":"INSERT INTO orders (\"select\", id) SELECT \"select\", id FROM input"}`),
		map[string]any{
			sinkbinding.InternalInputRows:    []map[string]any{{"select": "value", "id": int64(42)}},
			sinkbinding.InternalInputColumns: []string{"select", "id"},
		},
	)

	require.NoError(t, err)
	require.Equal(t, `INSERT INTO orders ("select", id) WITH input AS (SELECT value:"select"::STRING AS "select", value:"id"::NUMBER AS "id" FROM TABLE(FLATTEN(INPUT => PARSE_JSON(?)))) SELECT "select", id FROM input`, statement)
	require.Len(t, args, 1)
}

func TestPrepareStatementTypesInputRelationForDuplicatePrevention(t *testing.T) {
	t.Parallel()

	statement, _, err := prepareStatement(
		json.RawMessage(`{"type":"snowflake","sql":"INSERT INTO sink_rows (ID, LABEL) SELECT source.\"ID\", source.\"LABEL\" FROM input AS source WHERE NOT EXISTS (SELECT 1 FROM sink_rows AS existing WHERE existing.ID = source.\"ID\" AND existing.LABEL = source.\"LABEL\")"}`),
		map[string]any{
			sinkbinding.InternalInputRows:    []map[string]any{{"ID": int64(2), "LABEL": "second"}},
			sinkbinding.InternalInputColumns: []string{"ID", "LABEL"},
		},
	)

	require.NoError(t, err)
	require.Contains(t, statement, `value:"ID"::NUMBER AS "ID"`)
	require.Contains(t, statement, `value:"LABEL"::STRING AS "LABEL"`)
	require.Contains(t, statement, `existing.LABEL = source."LABEL"`)
}

func TestValidateSnowflakeInputPayloadLimits(t *testing.T) {
	t.Parallel()

	t.Run("Uncompressed", func(t *testing.T) {
		t.Parallel()

		err := validateSnowflakeInputPayloadLimits([]byte("four"), 1_024, 3)

		require.ErrorIs(t, err, xerror.InvalidInput(xerror.Query))
		require.ErrorContains(t, err, "4 uncompressed bytes; maximum is 3 bytes")
	})

	t.Run("Compressed", func(t *testing.T) {
		t.Parallel()

		err := validateSnowflakeInputPayloadLimits([]byte("payload"), 1, 1_024)

		require.ErrorIs(t, err, xerror.InvalidInput(xerror.Query))
		require.ErrorContains(t, err, "compresses to")
		require.ErrorContains(t, err, "maximum is 1 bytes")
	})

	t.Run("WithinLimits", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, validateSnowflakeInputPayloadLimits([]byte("payload"), 1_024, 1_024))
	})
}

func TestPrepareStatementBindsNamedValuesAfterNativeInputRows(t *testing.T) {
	t.Parallel()

	statement, args, err := prepareStatement(
		json.RawMessage(`{"type":"snowflake","sql":"INSERT INTO orders (tenant, id) SELECT {{tenant}}, id FROM input"}`),
		map[string]any{
			sinkbinding.InternalInputRows:    []map[string]any{{"id": int64(42)}},
			sinkbinding.InternalInputColumns: []string{"id"},
			"tenant":                         "acme",
		},
	)

	require.NoError(t, err)
	require.Contains(t, statement, "INSERT INTO orders (tenant, id) WITH input AS")
	require.Contains(t, statement, "SELECT ?, id FROM input")
	require.Len(t, args, 2)
	require.JSONEq(t, `[{"id":42}]`, args[0].(string))
	require.Equal(t, "acme", args[1])
}

func TestPrepareStatementRejectsNonInsertPipelineSink(t *testing.T) {
	t.Parallel()

	_, _, err := prepareStatement(
		json.RawMessage(`{"type":"snowflake","sql":"SELECT * FROM input"}`),
		map[string]any{
			sinkbinding.InternalInputRows:    []map[string]any{{"id": int64(42)}},
			sinkbinding.InternalInputColumns: []string{"id"},
		},
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "must be an INSERT operation")
}

func TestPrepareStatementRejectsInputWithoutColumns(t *testing.T) {
	t.Parallel()

	_, _, err := prepareStatement(
		json.RawMessage(`{"type":"snowflake","sql":"INSERT INTO orders SELECT * FROM input"}`),
		map[string]any{sinkbinding.InternalInputRows: []map[string]any{}},
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "require at least one column")
}

func TestPrepareStatementRejectsPipelineRowsWithoutInputRelation(t *testing.T) {
	t.Parallel()

	_, _, err := prepareStatement(
		json.RawMessage(`{"type":"snowflake","sql":"INSERT INTO orders (id) VALUES (1)"}`),
		map[string]any{sinkbinding.InternalInputRows: []map[string]any{{"id": int64(42)}}},
	)

	require.ErrorIs(t, err, xerror.InvalidInput(xerror.Query))
	require.ErrorContains(t, err, "must select rows from input")
}

func TestPrepareStatementRejectsMissingParameter(t *testing.T) {
	t.Parallel()

	_, _, err := prepareStatement(
		json.RawMessage(`{"type":"snowflake","sql":"SELECT {{missing}}"}`),
		map[string]any{},
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), `missing required parameter "missing"`)
}

func TestCapabilitiesDeclareWarehouseSourceAndSinkPrimitives(t *testing.T) {
	t.Parallel()

	capabilities := (&Connector{}).GetCapabilities()
	require.True(t, capabilities.SupportsSchemaIntrospection)
	require.False(t, capabilities.SupportsTransactions)
	require.False(t, capabilities.SupportsPagination)
}

func TestConnectorTypesMapExplicitlyToClientTypes(t *testing.T) {
	t.Parallel()

	config := Config{
		Account: "acme", Warehouse: "WH", Database: "DB", Schema: "PUBLIC",
		Role: "READER", ConnectTimeout: 17, WIFTokenFile: "/run/identity.jwt",
	}
	credentials := Credentials{AuthType: AuthTypePassword, Username: "svc", Password: "secret"}

	require.Equal(t, snowflakeclient.Config{
		Account: "acme", Warehouse: "WH", Database: "DB", Schema: "PUBLIC",
		Role: "READER", ConnectTimeout: 17, WIFTokenFile: "/run/identity.jwt",
	}, config.clientConfig())
	require.Equal(t, snowflakeclient.Credentials{
		AuthType: snowflakeclient.AuthTypePassword, Username: "svc", Password: "secret",
	}, credentials.clientCredentials())
}

func TestExecuteUsesConnectorClock(t *testing.T) {
	t.Parallel()

	clockCalls := 0
	connector := &Connector{
		client: fakeProviderClient{},
		clock: fakeClock{now: func() time.Time {
			clockCalls++
			return time.Now()
		}},
	}

	_, err := connector.Execute(
		context.Background(),
		json.RawMessage(`{"type":"snowflake","sql":"SELECT 1"}`),
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, 1, clockCalls)
}

func TestExecuteNormalizesOrDiscardsProviderRowsAtResponseLimit(t *testing.T) {
	t.Parallel()
	for _, maxBytes := range []int64{0, 16} {
		connector := newTestConnector(fakeProviderClient{
			result: snowflakeclient.Result{
				Rows:        []map[string]any{{"payload": []byte("normalized")}},
				ColumnNames: []string{"payload"},
				RowCount:    1,
			},
		})
		execResult, err := connector.Execute(
			context.Background(),
			json.RawMessage(`{"type":"snowflake","sql":"SELECT payload FROM events"}`),
			nil,
			types.ExecutionLimits{MaxResponseBytes: maxBytes},
		)
		require.NoError(t, err)
		sqlResult, ok := execResult.SQL()
		require.True(t, ok)
		if maxBytes > 0 {
			require.ErrorIs(t, execResult.Error(), xerror.ExecutionResponseSizeExceeded())
			require.Empty(t, sqlResult.Rows)
		} else {
			require.NoError(t, execResult.Error())
			require.Equal(t, "normalized", sqlResult.Rows[0]["payload"])
		}
	}
}

func TestExecutePreservesProviderFailureBeforeResponseSizeEnforcement(t *testing.T) {
	t.Parallel()

	providerErr := xerror.Unavailable(xerror.DataSource).Detail("provider unavailable")
	connector := newTestConnector(fakeProviderClient{
		result: snowflakeclient.Result{
			Rows: []map[string]any{{"payload": "larger than the configured response limit"}},
		},
		err: providerErr,
	})

	execResult, err := connector.Execute(
		context.Background(),
		json.RawMessage(`{"type":"snowflake","sql":"SELECT payload FROM events"}`),
		nil,
		types.ExecutionLimits{MaxResponseBytes: 1},
	)

	require.NoError(t, err)
	require.ErrorIs(t, execResult.Error(), providerErr)
	require.NotErrorIs(t, execResult.Error(), xerror.ExecutionResponseSizeExceeded())
}

func TestIntrospectSchemaMapsVisibleProviderColumns(t *testing.T) {
	t.Parallel()

	connector := newTestConnector(fakeProviderClient{columns: []snowflakeclient.Column{
		{Schema: "PUBLIC", Table: "ORDERS", Name: "ID", DataType: "NUMBER", IsNullable: "NO"},
		{Schema: "PUBLIC", Table: "ORDERS", Name: "NOTE", DataType: "TEXT", IsNullable: "YES"},
	}})

	schema, err := connector.IntrospectSchema(context.Background())

	require.NoError(t, err)
	require.Len(t, schema.Tables, 1)
	require.Equal(t, "PUBLIC.ORDERS", schema.Tables[0].Name)
	require.Equal(t, "ID", schema.Tables[0].Columns[0].Name)
	require.False(t, schema.Tables[0].Columns[0].Nullable)
	require.True(t, schema.Tables[0].Columns[1].Nullable)
}

func TestIntrospectSchemaPreservesProviderFailure(t *testing.T) {
	t.Parallel()

	providerErr := xerror.Unavailable(xerror.DataSource).Detail("provider unavailable")
	connector := newTestConnector(fakeProviderClient{err: providerErr})

	schema, err := connector.IntrospectSchema(context.Background())

	require.Nil(t, schema)
	require.Same(t, providerErr, err)
}

func TestExecuteUsesExecContextForPipelineSinkInput(t *testing.T) {
	t.Parallel()

	queryFlag := true
	connector := newTestConnector(&fakeProviderClient{queryFlag: &queryFlag})
	execResult, err := connector.Execute(
		context.Background(),
		json.RawMessage(`{"type":"snowflake","sql":"INSERT INTO orders SELECT * FROM input"}`),
		map[string]types.TypedParam{
			sinkbinding.InternalInputRows: {
				Value: []map[string]any{{"id": int64(42)}},
			},
			sinkbinding.InternalInputColumns: {
				Value: []string{"id"},
			},
		},
	)

	require.NoError(t, err)
	require.NoError(t, execResult.Error())
	require.False(t, queryFlag)
}

func newTestConnector(client providerClient) *Connector {
	return &Connector{client: client, clock: clock.New()}
}

type fakeClock struct {
	now func() time.Time
}

func (f fakeClock) Now() time.Time {
	return f.now()
}

type fakeProviderClient struct {
	result    snowflakeclient.Result
	columns   []snowflakeclient.Column
	err       error
	queryFlag *bool
}

func (f fakeProviderClient) Close() error { return nil }

func (f fakeProviderClient) TestConnection(context.Context) error { return f.err }

func (f fakeProviderClient) Execute(
	_ context.Context,
	_ string,
	_ []any,
	query bool,
	_ snowflakeclient.RowScanner,
) (snowflakeclient.Result, error) {
	if f.queryFlag != nil {
		*f.queryFlag = query
	}
	return f.result, f.err
}

func (f fakeProviderClient) ListColumns(context.Context) ([]snowflakeclient.Column, error) {
	return f.columns, f.err
}
