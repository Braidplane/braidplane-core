package bigquery_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	bigqueryconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/bigquery"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

// bigQueryMockBaseURL returns the worktree-aware BigQuery mock URL used by E2E tests.
func bigQueryMockBaseURL() string {
	return depsports.BigQueryMockBaseURL()
}

// testConfig builds connector config that routes both BigQuery REST and token traffic to the mock.
func testConfig() bigqueryconnector.Config {
	baseURL := bigQueryMockBaseURL()
	return bigqueryconnector.Config{
		ProjectID:          "analytics-prod",
		Location:           "US",
		DefaultDataset:     "analytics",
		TimeoutSeconds:     5,
		MaximumBytesBilled: 1000000,
		BaseURL:            baseURL,
		TokenURL:           baseURL + "/token",
		AllowPrivateHosts:  true,
	}
}

// testCredentials uses the locally generated test key at the mock boundary.
func testCredentials() bigqueryconnector.Credentials {
	data, err := os.ReadFile("../../../../../pkg/testutil/testdata/bigquery-service-account.json")
	if err != nil {
		panic(err)
	}
	return bigqueryconnector.Credentials{
		ServiceAccountJSON: string(data),
	}
}

// resetMock restores the shared BigQuery mock to its seeded default state.
func resetMock(t *testing.T, ctx context.Context) {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, bigQueryMockBaseURL()+"/reset", nil)
	require.NoError(t, err)

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// insertedRows reads rows appended through the mock insertAll endpoint.
func insertedRows(t *testing.T, ctx context.Context) []map[string]any {
	t.Helper()

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		bigQueryMockBaseURL()+"/inserted?dataset=analytics&table=order_exports",
		nil,
	)
	require.NoError(t, err)

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var payload struct {
		Rows []map[string]any `json:"rows"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	return payload.Rows
}

// executeSQL runs a BigQuery statement and returns the shared execution envelope.
func executeSQL(
	ctx context.Context,
	conn *bigqueryconnector.Connector,
	statement string,
	params map[string]types.TypedParam,
) (*result.ExecutionResult, error) {
	queryData := json.RawMessage(statement)
	return conn.Execute(ctx, queryData, params)
}

// Shares a mutable mock service instance across tests.
func TestConnector_TestConnectionAndIntrospectSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resetMock(t, ctx)
	conn, err := bigqueryconnector.New(transportsecurity.WithPlaintext(ctx), testConfig(), testCredentials())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	require.NoError(t, conn.TestConnection(ctx))

	schema, err := conn.IntrospectSchema(ctx)
	require.NoError(t, err)
	require.NotNil(t, schema)
	require.Len(t, schema.Tables, 2)
	assert.Equal(t, "analytics.order_exports", schema.Tables[0].Name)
	assert.Equal(t, "analytics.orders", schema.Tables[1].Name)

	columnNames := make([]string, 0, len(schema.Tables[1].Columns))
	for _, column := range schema.Tables[1].Columns {
		columnNames = append(columnNames, column.Name)
	}
	assert.Contains(t, columnNames, "order_id")
	assert.Contains(t, columnNames, "metadata")
}

// Shares a mutable mock service instance across tests.
func TestConnector_ExecuteQueryAndInsertRows(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping E2E test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resetMock(t, ctx)
	conn, err := bigqueryconnector.New(transportsecurity.WithPlaintext(ctx), testConfig(), testCredentials())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()

	queryResult, err := executeSQL(
		ctx,
		conn,
		`{
			"type": "bigquery",
			"operation": "query",
			"sql": "SELECT order_id, region, total_amount, active, item_count, metadata FROM `+"`analytics.orders`"+` WHERE region = {{region}}"
		}`,
		map[string]types.TypedParam{
			"region": {Value: "emea", DataType: "text"},
		},
	)
	require.NoError(t, err)
	require.NoError(t, queryResult.Error())

	sqlResult, ok := queryResult.SQL()
	require.True(t, ok)
	require.NotNil(t, sqlResult)
	require.Equal(t, 2, sqlResult.RowCount)
	require.Len(t, sqlResult.Rows, 2)
	assert.Equal(t, "o-100", sqlResult.Rows[0]["order_id"])
	assert.Equal(t, "emea", sqlResult.Rows[0]["region"])
	assert.Equal(t, "123.45", sqlResult.Rows[0]["total_amount"])
	assert.Equal(t, true, sqlResult.Rows[0]["active"])
	assert.Equal(t, int64(2), sqlResult.Rows[0]["item_count"])
	assert.Equal(t, map[string]any{"source": "fixture", "tier": "gold"}, sqlResult.Rows[0]["metadata"])

	insertResult, err := executeSQL(
		ctx,
		conn,
		`{
			"type": "bigquery",
			"operation": "insert_rows",
			"dataset": "analytics",
			"table": "order_exports",
			"rows": "{{input}}",
			"insert_id_prefix": "{{runtime.run.slug}}"
		}`,
		map[string]types.TypedParam{
			"input":            {Value: sqlResult.Rows, DataType: "rows"},
			"runtime.run.slug": {Value: "bigquery-e2e", DataType: "text"},
		},
	)
	require.NoError(t, err)
	require.NoError(t, insertResult.Error())

	inserted := insertedRows(t, ctx)
	require.Len(t, inserted, 2)
	assert.Equal(t, "o-100", inserted[0]["order_id"])
	assert.Equal(t, "o-102", inserted[1]["order_id"])
}
