package mysql

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/decimal"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
)

func TestPrepareStatementBindsNativeInputRelation(t *testing.T) {
	t.Parallel()

	queryData, err := json.Marshal(map[string]any{
		"type": "mysql",
		"sql": `INSERT INTO sink_users (org_slug, id, email)
SELECT {{org_slug}}, id, email FROM input`,
	})
	require.NoError(t, err)

	stmt, args, err := prepareStatement(queryData, map[string]any{
		sinkbinding.InternalInputRows: []map[string]any{{
			"id":    int64(1),
			"email": "alice@example.com",
		}},
		sinkbinding.InternalInputColumns: []string{"id", "email"},
		"org_slug":                       "acme",
	})
	require.NoError(t, err)
	require.Contains(
		t,
		stmt,
		"INSERT INTO sink_users (org_slug, id, email)\nWITH input AS (SELECT * FROM JSON_TABLE(CAST(? AS JSON), '$[*]' COLUMNS (",
	)
	require.Contains(t, stmt, "`id` BIGINT PATH '$.c0'")
	require.Contains(t, stmt, "`email` TEXT PATH '$.c1'")
	require.Contains(t, stmt, "SELECT ?, id, email FROM input")
	require.Len(t, args, 2)

	rowsJSON, ok := args[0].(string)
	require.True(t, ok)
	require.JSONEq(t, `[{"c0":1,"c1":"alice@example.com"}]`, rowsJSON)
	require.Equal(t, "acme", args[1])
}

func TestMySQLInputColumnTypeUsesExactDecimalBounds(t *testing.T) {
	t.Parallel()

	amount, err := decimal.Parse("12345678901234567890.123456789")
	require.NoError(t, err)
	types, err := mysqlInputColumnTypes([]map[string]any{{"amount": amount}}, []string{"amount"})
	require.NoError(t, err)
	require.Equal(t, "DECIMAL(29,9)", types["amount"])

	types, err = mysqlInputColumnTypes([]map[string]any{
		{"amount": json.Number("12.34")},
		{"amount": json.Number("0.0001")},
		{"amount": int64(123456)},
	}, []string{"amount"})
	require.NoError(t, err)
	require.Equal(t, "DECIMAL(10,4)", types["amount"])

	_, err = mysqlInputColumnTypes([]map[string]any{{"amount": json.Number("1e309")}}, []string{"amount"})
	require.Error(t, err)
	_, err = mysqlInputColumnTypes([]map[string]any{{"amount": json.Number("0.1234567890123456789012345678901")}}, []string{"amount"})
	require.Error(t, err)
}

func TestPrepareStatementRejectsPipelineRowsWithoutInputRelation(t *testing.T) {
	t.Parallel()

	queryData, err := json.Marshal(map[string]any{
		"type": "mysql",
		"sql":  "INSERT INTO sink_users (id) VALUES (1)",
	})
	require.NoError(t, err)

	_, _, err = prepareStatement(queryData, map[string]any{
		sinkbinding.InternalInputRows: []map[string]any{{"id": int64(1)}},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "must select rows from input")
}

func TestPrepareStatementBindsIncrementalWatermarkBounds(t *testing.T) {
	t.Parallel()

	queryData, err := json.Marshal(map[string]any{
		"type": "mysql",
		"sql": `SELECT id, updated_at
FROM users
WHERE ({{runtime.incremental.lower_bound.value}} IS NULL OR updated_at > {{runtime.incremental.lower_bound.value}})
	AND ({{runtime.incremental.upper_bound.value}} IS NULL OR updated_at <= {{runtime.incremental.upper_bound.value}})`,
	})
	require.NoError(t, err)

	stmt, args, err := prepareStatement(queryData, map[string]any{
		"runtime.incremental.lower_bound.value": "2026-05-24T10:00:00Z",
		"runtime.incremental.upper_bound.value": "2026-05-24T11:00:00Z",
	})

	require.NoError(t, err)
	require.Contains(t, stmt, "(? IS NULL OR updated_at > ?)")
	require.Contains(t, stmt, "(? IS NULL OR updated_at <= ?)")
	require.Equal(
		t,
		[]any{
			"2026-05-24T10:00:00Z",
			"2026-05-24T10:00:00Z",
			"2026-05-24T11:00:00Z",
			"2026-05-24T11:00:00Z",
		},
		args,
	)
}

func TestPrepareStatementBindsNativeInputRelationFromJSONSourcedRows(t *testing.T) {
	t.Parallel()

	// Rows pre-normalized from a JSON payload at sink runtime are routed through
	// the same native input relation path as row payloads.
	queryData, err := json.Marshal(map[string]any{
		"type": "mysql",
		"sql":  `INSERT INTO orders (id, amount) SELECT id, amount FROM input`,
	})
	require.NoError(t, err)

	stmt, args, err := prepareStatement(queryData, map[string]any{
		sinkbinding.InternalInputRows: []map[string]any{
			{"id": float64(1), "amount": 99.5},
			{"id": float64(2), "amount": 49.0},
		},
		sinkbinding.InternalInputColumns: []string{"amount", "id"},
	})
	require.NoError(t, err)
	require.Contains(
		t,
		stmt,
		"WITH input AS (SELECT * FROM JSON_TABLE(CAST(? AS JSON), '$[*]' COLUMNS (",
	)
	require.Contains(t, stmt, "`amount` DOUBLE PATH '$.c0'")
	require.Contains(t, stmt, "`id` BIGINT PATH '$.c1'")
	require.Contains(t, stmt, "SELECT id, amount FROM input")
	require.Len(t, args, 1)

	rowsJSON, ok := args[0].(string)
	require.True(t, ok)
	require.JSONEq(t, `[{"c0":99.5,"c1":1},{"c0":49,"c1":2}]`, rowsJSON)
}

func TestPrepareStatementBindsNativeInputRelationFromCSVSourcedRows(t *testing.T) {
	t.Parallel()

	// Rows pre-normalized from a CSV payload always carry string values;
	// the connector maps them to TEXT columns.
	queryData, err := json.Marshal(map[string]any{
		"type": "mysql",
		"sql":  `INSERT INTO orders (id, status) SELECT id, status FROM input`,
	})
	require.NoError(t, err)

	stmt, args, err := prepareStatement(queryData, map[string]any{
		sinkbinding.InternalInputRows: []map[string]any{
			{"id": "1", "status": "paid"},
			{"id": "2", "status": "pending"},
		},
		sinkbinding.InternalInputColumns: []string{"id", "status"},
	})
	require.NoError(t, err)
	require.Contains(
		t,
		stmt,
		"WITH input AS (SELECT * FROM JSON_TABLE(CAST(? AS JSON), '$[*]' COLUMNS (",
	)
	require.Contains(t, stmt, "`id` TEXT PATH '$.c0'")
	require.Contains(t, stmt, "`status` TEXT PATH '$.c1'")
	require.Contains(t, stmt, "SELECT id, status FROM input")
	require.Len(t, args, 1)

	rowsJSON, ok := args[0].(string)
	require.True(t, ok)
	require.JSONEq(t, `[{"c0":"1","c1":"paid"},{"c0":"2","c1":"pending"}]`, rowsJSON)
}

func TestPrepareStatementWithArrowAccessorsOnNestedJSONColumn(t *testing.T) {
	t.Parallel()

	// A row column that holds a nested map is typed as JSON in the CTE so that
	// the query can use the -> and ->> arrow accessors (or JSON_EXTRACT) in the
	// SELECT clause.
	queryData, err := json.Marshal(map[string]any{
		"type": "mysql",
		"sql": `INSERT INTO events (id, source, event_type)
			SELECT id, meta->>'$."source"', meta->>'$."type"' FROM input`,
	})
	require.NoError(t, err)

	stmt, args, err := prepareStatement(queryData, map[string]any{
		sinkbinding.InternalInputRows: []map[string]any{
			{"id": int64(1), "meta": map[string]any{"source": "api", "type": "click"}},
			{"id": int64(2), "meta": map[string]any{"source": "web", "type": "view"}},
		},
		sinkbinding.InternalInputColumns: []string{"id", "meta"},
	})
	require.NoError(t, err)
	require.Contains(
		t,
		stmt,
		"WITH input AS (SELECT * FROM JSON_TABLE(CAST(? AS JSON), '$[*]' COLUMNS (",
	)
	// map[string]any columns are typed as JSON, enabling -> / ->> access.
	require.Contains(t, stmt, "`id` BIGINT PATH '$.c0'")
	require.Contains(t, stmt, "`meta` JSON PATH '$.c1'")
	// Arrow accessors in the original query are preserved as-is.
	require.Contains(t, stmt, `meta->>'$."source"'`)
	require.Contains(t, stmt, `meta->>'$."type"'`)
	require.Len(t, args, 1)

	rowsJSON, ok := args[0].(string)
	require.True(t, ok)
	require.JSONEq(
		t,
		`[{"c0":1,"c1":{"source":"api","type":"click"}},{"c0":2,"c1":{"source":"web","type":"view"}}]`,
		rowsJSON,
	)
}

func TestPrependInputRelationCTE(t *testing.T) {
	t.Parallel()

	inputRelationSQL := "SELECT * FROM src"

	tests := []struct {
		name      string
		statement string
		want      string
	}{
		{
			name:      "WithSpaceAfterWith",
			statement: "WITH cte AS (SELECT 1) SELECT * FROM cte",
			want:      "WITH input AS (SELECT * FROM src), cte AS (SELECT 1) SELECT * FROM cte",
		},
		{
			name:      "WithNewlineAfterWith",
			statement: "WITH\ncte AS (SELECT 1) SELECT * FROM cte",
			want:      "WITH input AS (SELECT * FROM src), cte AS (SELECT 1) SELECT * FROM cte",
		},
		{
			name:      "WithRecursiveNewlineAfterKeyword",
			statement: "WITH RECURSIVE\ncte AS (SELECT 1) SELECT * FROM cte",
			want:      "WITH RECURSIVE input AS (SELECT * FROM src), cte AS (SELECT 1) SELECT * FROM cte",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := prependInputRelationCTE(tt.statement, inputRelationSQL)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestPrepareNativeInputKeepsSparseRowsCompact(t *testing.T) {
	columns := make([]string, 2000)
	for i := range columns {
		columns[i] = fmt.Sprintf("field_%d", i)
	}
	row := map[string]any{columns[0]: "first", columns[len(columns)-1]: nil}
	_, args, handled, err := prepareNativeInputRelationStatement("SELECT * FROM input", map[string]any{
		sinkbinding.InternalInputRows:    []map[string]any{row},
		sinkbinding.InternalInputColumns: columns,
	})
	require.NoError(t, err)
	require.True(t, handled)
	require.Less(t, len(args[0].(string)), 100, "missing cells must not expand into a dense row")
	require.Equal(t, map[string]any{columns[0]: "first", columns[len(columns)-1]: nil}, row)
}
