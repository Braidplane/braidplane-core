package bigquery

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

type stubProvider struct {
	testConnectionFunc func(context.Context, TestConnectionRequest) error
	listDatasetsFunc   func(context.Context, ListDatasetsRequest) ([]Dataset, error)
	listTablesFunc     func(context.Context, ListTablesRequest) ([]Table, error)
	getTableFunc       func(context.Context, GetTableRequest) (*TableMetadata, error)
	queryFunc          func(context.Context, QueryRequest) (*QueryResult, error)
	insertRowsFunc     func(context.Context, InsertRowsRequest) (*InsertRowsResult, error)
	closeFunc          func() error
}

func (s *stubProvider) TestConnection(ctx context.Context, req TestConnectionRequest) error {
	if s.testConnectionFunc == nil {
		return assert.AnError
	}
	return s.testConnectionFunc(ctx, req)
}

func (s *stubProvider) ListDatasets(ctx context.Context, req ListDatasetsRequest) ([]Dataset, error) {
	if s.listDatasetsFunc == nil {
		return nil, assert.AnError
	}
	return s.listDatasetsFunc(ctx, req)
}

func (s *stubProvider) ListTables(ctx context.Context, req ListTablesRequest) ([]Table, error) {
	if s.listTablesFunc == nil {
		return nil, assert.AnError
	}
	return s.listTablesFunc(ctx, req)
}

func (s *stubProvider) GetTable(ctx context.Context, req GetTableRequest) (*TableMetadata, error) {
	if s.getTableFunc == nil {
		return nil, assert.AnError
	}
	return s.getTableFunc(ctx, req)
}

func (s *stubProvider) Query(ctx context.Context, req QueryRequest) (*QueryResult, error) {
	if s.queryFunc == nil {
		return nil, assert.AnError
	}
	return s.queryFunc(ctx, req)
}

func (s *stubProvider) InsertRows(ctx context.Context, req InsertRowsRequest) (*InsertRowsResult, error) {
	if s.insertRowsFunc == nil {
		return nil, assert.AnError
	}
	return s.insertRowsFunc(ctx, req)
}

func (s *stubProvider) Close() error {
	if s.closeFunc == nil {
		return nil
	}
	return s.closeFunc()
}

func TestNewRejectsMissingServiceAccountJSON(t *testing.T) {
	t.Parallel()

	conn, err := New(
		context.Background(),
		Config{ProjectID: "analytics-prod", Location: "US", TimeoutSeconds: 5},
		Credentials{},
	)

	require.Nil(t, conn)
	require.Error(t, err)
	require.ErrorIs(t, err, xerror.InvalidCredentials())
}

func TestConnectorTestConnectionUsesConfiguredProject(t *testing.T) {
	t.Parallel()

	conn := newWithClient(
		Config{ProjectID: "analytics-prod", Location: "US"},
		&stubProvider{
			testConnectionFunc: func(_ context.Context, req TestConnectionRequest) error {
				assert.Equal(t, "analytics-prod", req.ProjectID)
				assert.Equal(t, "US", req.Location)
				return nil
			},
		},
	)

	require.NoError(t, conn.TestConnection(context.Background()))
}

func TestConnectorIntrospectSchemaMapsDatasetsTablesAndColumnModes(t *testing.T) {
	t.Parallel()

	conn := newWithClient(
		Config{ProjectID: "analytics-prod", Location: "US"},
		&stubProvider{
			listDatasetsFunc: func(_ context.Context, req ListDatasetsRequest) ([]Dataset, error) {
				assert.Equal(t, "analytics-prod", req.ProjectID)
				return []Dataset{{ID: "analytics"}}, nil
			},
			listTablesFunc: func(_ context.Context, req ListTablesRequest) ([]Table, error) {
				assert.Equal(t, "analytics", req.DatasetID)
				return []Table{{DatasetID: "analytics", TableID: "orders"}}, nil
			},
			getTableFunc: func(_ context.Context, req GetTableRequest) (*TableMetadata, error) {
				assert.Equal(t, "orders", req.TableID)
				return &TableMetadata{
					DatasetID: "analytics",
					TableID:   "orders",
					Fields: []Field{
						{Name: "order_id", Type: "STRING", Mode: "REQUIRED"},
						{Name: "total_amount", Type: "NUMERIC", Mode: "NULLABLE"},
						{Name: "line_items", Type: "RECORD", Mode: "REPEATED"},
					},
				}, nil
			},
		},
	)

	schema, err := conn.IntrospectSchema(context.Background())
	require.NoError(t, err)
	require.NotNil(t, schema)
	require.Len(t, schema.Tables, 1)
	assert.Equal(t, "analytics.orders", schema.Tables[0].Name)
	require.Len(t, schema.Tables[0].Columns, 3)
	assert.Equal(t, Column{Name: "order_id", Type: "STRING", Nullable: false}, schema.Tables[0].Columns[0])
	assert.Equal(t, Column{Name: "total_amount", Type: "NUMERIC(38,9)", Nullable: true}, schema.Tables[0].Columns[1])
	assert.Equal(t, Column{Name: "line_items", Type: "RECORD", Nullable: true}, schema.Tables[0].Columns[2])
}

func TestColumnsFromFieldsPreservesBigQueryDecimalDeclarations(t *testing.T) {
	t.Parallel()

	columns := columnsFromFields([]Field{
		{Name: "amount", Type: "NUMERIC", Precision: "18", Scale: "2"},
		{Name: "wide_amount", Type: "BIGNUMERIC"},
	})

	require.Equal(t, []Column{
		{Name: "amount", Type: "NUMERIC(18,2)", Nullable: true},
		{Name: "wide_amount", Type: "BIGNUMERIC", Nullable: true},
	}, columns)
}

func TestConnectorExecuteQueryBindsParametersAndNormalizesRows(t *testing.T) {
	t.Parallel()

	conn := newWithClient(
		Config{
			ProjectID:          "analytics-prod",
			Location:           "EU",
			MaximumBytesBilled: 500_000,
		},
		&stubProvider{
			queryFunc: func(_ context.Context, req QueryRequest) (*QueryResult, error) {
				assert.Equal(t, "analytics-prod", req.ProjectID)
				assert.Equal(t, "EU", req.Location)
				assert.Equal(t, int64(500_000), req.MaximumBytesBilled)
				assert.Equal(t, 26*time.Second, req.Timeout)
				assert.Equal(t,
					"SELECT order_id, total_amount, active, order_date FROM `analytics.orders` WHERE region = @region AND total_amount >= @min_total AND active = @active AND order_date = @order_date",
					req.SQL,
				)
				assert.NotContains(t, req.SQL, "north-america")
				assert.Equal(t, []QueryParameter{
					{Name: "region", Type: "STRING", Value: "north-america"},
					{Name: "min_total", Type: "INT64", Value: int64(100)},
					{Name: "active", Type: "BOOL", Value: true},
					{Name: "order_date", Type: "DATE", Value: "2026-06-08"},
				}, req.Parameters)

				return &QueryResult{
					Schema: []Field{
						{Name: "order_id", Type: "STRING"},
						{Name: "total_amount", Type: "NUMERIC"},
						{Name: "active", Type: "BOOL"},
						{Name: "order_date", Type: "DATE"},
						{Name: "loaded_at", Type: "TIMESTAMP"},
					},
					Rows: []map[string]any{
						{
							"order_id":     "o-100",
							"total_amount": "1234567890.123456789",
							"active":       "true",
							"order_date":   "2026-06-08",
							"loaded_at":    "2026-06-08T10:11:12Z",
						},
					},
				}, nil
			},
		},
	)

	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{
			"type": "bigquery",
			"operation": "query",
			"sql": "SELECT order_id, total_amount, active, order_date FROM `+"`analytics.orders`"+` WHERE region = {{region}} AND total_amount >= {{min_total}} AND active = {{active}} AND order_date = {{order_date}}",
			"timeout_seconds": 26
		}`),
		map[string]types.TypedParam{
			"region":     {Value: "north-america", DataType: "text"},
			"min_total":  {Value: "100", DataType: "integer"},
			"active":     {Value: true, DataType: "boolean"},
			"order_date": {Value: "2026-06-08", DataType: "date"},
		},
	)

	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.Equal(t, result.ExecutionResultTypeSQL, execResult.Type())
	require.Equal(t, "bigquery", execResult.VendorType())
	require.NoError(t, execResult.Error())

	sqlResult, ok := execResult.SQL()
	require.True(t, ok)
	require.NotNil(t, sqlResult)
	assert.Equal(t, []string{"order_id", "total_amount", "active", "order_date", "loaded_at"}, sqlResult.ColumnNames)
	assert.Equal(t, 1, sqlResult.RowCount)
	require.Len(t, sqlResult.Rows, 1)
	assert.Equal(t, "o-100", sqlResult.Rows[0]["order_id"])
	assert.Equal(t, "1234567890.123456789", sqlResult.Rows[0]["total_amount"])
	assert.Equal(t, true, sqlResult.Rows[0]["active"])
	assert.Equal(t, "2026-06-08", sqlResult.Rows[0]["order_date"])
	assert.Equal(t, "2026-06-08T10:11:12Z", sqlResult.Rows[0]["loaded_at"])
}

func TestConnectorExecuteQueryReturnsRowLimitViolationAsExecutionResult(t *testing.T) {
	t.Parallel()

	conn := newWithClient(
		Config{ProjectID: "analytics-prod", Location: "US"},
		&stubProvider{
			queryFunc: func(_ context.Context, req QueryRequest) (*QueryResult, error) {
				assert.Equal(t, 2, req.MaxResultRows)
				return &QueryResult{
					Schema: []Field{{Name: "id", Type: "STRING"}},
					Rows: []map[string]any{
						{"id": "one"},
						{"id": "two"},
					},
				}, nil
			},
		},
	)

	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{"type":"bigquery","operation":"query","sql":"SELECT id FROM `+"`analytics.orders`"+`"}`),
		map[string]types.TypedParam{},
		types.ExecutionLimits{MaxResultRows: 1},
	)

	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.Error(t, execResult.Error())
	require.ErrorIs(t, execResult.Error(), xerror.ExecutionRowLimitExceeded())
}

func TestConnectorExecuteQueryReturnsResponseSizeViolationAsExecutionResult(t *testing.T) {
	t.Parallel()

	conn := newWithClient(
		Config{ProjectID: "analytics-prod", Location: "US"},
		&stubProvider{
			queryFunc: func(context.Context, QueryRequest) (*QueryResult, error) {
				return &QueryResult{
					Schema: []Field{{Name: "payload", Type: "STRING"}},
					Rows: []map[string]any{
						{"payload": "this normalized result is too large"},
					},
				}, nil
			},
		},
	)

	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{"type":"bigquery","operation":"query","sql":"SELECT payload FROM `+"`analytics.orders`"+`"}`),
		map[string]types.TypedParam{},
		types.ExecutionLimits{MaxResponseBytes: 16},
	)

	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.Error(t, execResult.Error())
	require.ErrorIs(t, execResult.Error(), xerror.ExecutionResponseSizeExceeded())
}

func TestConnectorExecuteQueryMapsCancellationToTimeoutResult(t *testing.T) {
	t.Parallel()

	conn := newWithClient(
		Config{ProjectID: "analytics-prod", Location: "US"},
		&stubProvider{
			queryFunc: func(context.Context, QueryRequest) (*QueryResult, error) {
				return nil, xerror.Timeout(xerror.DataSource).Detail("BigQuery job canceled")
			},
		},
	)

	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{"type":"bigquery","operation":"query","sql":"SELECT 1"}`),
		map[string]types.TypedParam{},
	)

	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.Error(t, execResult.Error())
	require.ErrorIs(t, execResult.Error(), xerror.Timeout(xerror.DataSource))
}

func TestConnectorExecuteInsertRowsConsumesPipelineInputBinding(t *testing.T) {
	t.Parallel()

	conn := newWithClient(
		Config{ProjectID: "analytics-prod", Location: "US"},
		&stubProvider{
			insertRowsFunc: func(_ context.Context, req InsertRowsRequest) (*InsertRowsResult, error) {
				assert.Equal(t, "analytics-prod", req.ProjectID)
				assert.Equal(t, "analytics", req.DatasetID)
				assert.Equal(t, "orders", req.TableID)
				assert.Equal(t, []map[string]any{
					{"order_id": "o-100", "total_amount": "123.45"},
					{"order_id": "o-101", "total_amount": "67.89"},
				}, req.Rows)
				assert.Equal(t, "run-abc", req.InsertIDPrefix)
				return &InsertRowsResult{InsertedRows: 2}, nil
			},
		},
	)

	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{
			"type": "bigquery",
			"operation": "insert_rows",
			"dataset": "analytics",
			"table": "orders",
			"rows": "{{input}}",
			"insert_id_prefix": "{{runtime.run.slug}}"
		}`),
		map[string]types.TypedParam{
			"input": {
				Value: []map[string]any{
					{"order_id": "o-100", "total_amount": "123.45"},
					{"order_id": "o-101", "total_amount": "67.89"},
				},
				DataType: "rows",
			},
			"runtime.run.slug": {Value: "run-abc", DataType: "text"},
		},
	)

	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.NoError(t, execResult.Error())
	sqlResult, ok := execResult.SQL()
	require.True(t, ok)
	require.NotNil(t, sqlResult)
	assert.Equal(t, 2, sqlResult.RowCount)
	assert.Empty(t, sqlResult.Rows)
}

func TestConnectorExecuteInsertRowsRejectsNonRowsInput(t *testing.T) {
	t.Parallel()

	conn := newWithClient(Config{ProjectID: "analytics-prod", Location: "US"}, &stubProvider{})

	execResult, err := conn.Execute(
		context.Background(),
		json.RawMessage(`{
			"type": "bigquery",
			"operation": "insert_rows",
			"dataset": "analytics",
			"table": "orders",
			"rows": "{{input}}"
		}`),
		map[string]types.TypedParam{
			"input": {Value: "not rows", DataType: "text"},
		},
	)

	require.NoError(t, err)
	require.NotNil(t, execResult)
	require.Error(t, execResult.Error())
	require.ErrorIs(t, execResult.Error(), xerror.InvalidInput(xerror.Query))
}
