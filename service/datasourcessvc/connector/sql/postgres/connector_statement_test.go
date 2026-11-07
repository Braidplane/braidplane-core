package postgres

import (
	stdsql "database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/decimal"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
)

func TestPrepareStatementBindsNativeInputRelation(t *testing.T) {
	t.Parallel()

	queryData, err := json.Marshal(map[string]any{
		"type": "postgres",
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
	require.Contains(t, stmt, `WITH input AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS src(`)
	require.Contains(t, stmt, `"id" bigint`)
	require.Contains(t, stmt, `"email" text`)
	require.Contains(t, stmt, `SELECT $2, id, email FROM input`)
	require.Len(t, args, 2)

	rowsJSON, ok := args[0].(string)
	require.True(t, ok)
	require.JSONEq(t, `[{"email":"alice@example.com","id":1}]`, rowsJSON)
	require.Equal(t, "acme", args[1])
}

func TestPrepareStatementNeverInterpolatesSelectorLikeValues(t *testing.T) {
	t.Parallel()
	value := `orders; DROP TABLE orders; --`
	for _, statement := range []string{"SELECT * FROM orders WHERE id = {{value}}", "SELECT * FROM {{value}}"} {
		raw, err := json.Marshal(map[string]string{"sql": statement})
		require.NoError(t, err)
		prepared, args, err := prepareStatement(raw, map[string]any{"value": value})
		require.NoError(t, err)
		require.NotContains(t, prepared, value)
		require.Contains(t, prepared, "$1")
		require.Equal(t, []any{value}, args)
	}
}

func TestPostgresInputColumnTypeUsesNumericForExactDecimal(t *testing.T) {
	t.Parallel()

	amount, err := decimal.Parse("12345678901234567890.123456789")
	require.NoError(t, err)
	require.Equal(t, "numeric", postgresInputColumnType("amount", []map[string]any{{"amount": amount}}))
	require.Equal(t, "numeric", postgresInputColumnType("amount", []map[string]any{{"amount": json.Number("12.34")}}))
}

func TestPostgresColumnTypePreservesNumericDeclaration(t *testing.T) {
	t.Parallel()

	require.Equal(t, "numeric(18,2)", postgresColumnType("numeric", stdsql.NullInt64{Int64: 18, Valid: true}, stdsql.NullInt64{Int64: 2, Valid: true}))
	require.Equal(t, "numeric", postgresColumnType("numeric", stdsql.NullInt64{}, stdsql.NullInt64{}))
}

func TestPrepareStatementRejectsPipelineRowsWithoutInputRelation(t *testing.T) {
	t.Parallel()

	queryData, err := json.Marshal(map[string]any{
		"type": "postgres",
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
		"type": "postgres",
		"sql": `SELECT id, updated_at
FROM users
WHERE ({{runtime.incremental.lower_bound.value}}::timestamptz IS NULL OR updated_at > {{runtime.incremental.lower_bound.value}}::timestamptz)
	AND ({{runtime.incremental.upper_bound.value}}::timestamptz IS NULL OR updated_at <= {{runtime.incremental.upper_bound.value}}::timestamptz)`,
	})
	require.NoError(t, err)

	stmt, args, err := prepareStatement(queryData, map[string]any{
		"runtime.incremental.lower_bound.value": "2026-05-24T10:00:00Z",
		"runtime.incremental.upper_bound.value": "2026-05-24T11:00:00Z",
	})

	require.NoError(t, err)
	require.Contains(t, stmt, "$1::timestamptz IS NULL OR updated_at > $1::timestamptz")
	require.Contains(t, stmt, "$2::timestamptz IS NULL OR updated_at <= $2::timestamptz")
	require.Equal(
		t,
		[]any{
			"2026-05-24T10:00:00Z",
			"2026-05-24T11:00:00Z",
		},
		args,
	)
}

func TestPrepareStatementBindsNativeInputRelationFromJSONSourcedRows(t *testing.T) {
	t.Parallel()

	// Rows pre-normalized from a JSON payload at sink runtime are routed through
	// the same native input relation path as row payloads. The connector only
	// sees []map[string]any in InternalInputRows regardless of origin.
	queryData, err := json.Marshal(map[string]any{
		"type": "postgres",
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
	require.Contains(t, stmt, `WITH input AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS src(`)
	require.Contains(t, stmt, `"amount" double precision`)
	require.Contains(t, stmt, `"id" bigint`)
	require.Contains(t, stmt, `SELECT id, amount FROM input`)
	require.Len(t, args, 1)

	rowsJSON, ok := args[0].(string)
	require.True(t, ok)
	require.JSONEq(t, `[{"id":1,"amount":99.5},{"id":2,"amount":49.0}]`, rowsJSON)
}

func TestPrepareStatementBindsNativeInputRelationFromCSVSourcedRows(t *testing.T) {
	t.Parallel()

	// Rows pre-normalized from a CSV payload at sink runtime always carry string
	// values (CSV has no type information). The connector maps them to text columns.
	queryData, err := json.Marshal(map[string]any{
		"type": "postgres",
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
	require.Contains(t, stmt, `WITH input AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS src(`)
	require.Contains(t, stmt, `"id" text`)
	require.Contains(t, stmt, `"status" text`)
	require.Contains(t, stmt, `SELECT id, status FROM input`)
	require.Len(t, args, 1)

	rowsJSON, ok := args[0].(string)
	require.True(t, ok)
	require.JSONEq(t, `[{"id":"1","status":"paid"},{"id":"2","status":"pending"}]`, rowsJSON)
}

func TestPrepareStatementWithArrowAccessorsOnNestedJSONColumn(t *testing.T) {
	t.Parallel()

	// A row column that holds a nested map is typed as jsonb in the CTE so that
	// the query can use the -> and ->> arrow accessors in the SELECT clause.
	queryData, err := json.Marshal(map[string]any{
		"type": "postgres",
		"sql": `INSERT INTO events (id, source, event_type)
			SELECT id, meta->>'source', meta->>'type' FROM input`,
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
	require.Contains(t, stmt, `WITH input AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS src(`)
	// map[string]any columns are typed as jsonb, enabling -> / ->> access.
	require.Contains(t, stmt, `"id" bigint`)
	require.Contains(t, stmt, `"meta" jsonb`)
	// Arrow accessors in the original query are preserved as-is.
	require.Contains(t, stmt, `meta->>'source'`)
	require.Contains(t, stmt, `meta->>'type'`)
	require.Len(t, args, 1)

	rowsJSON, ok := args[0].(string)
	require.True(t, ok)
	require.JSONEq(
		t,
		`[{"id":1,"meta":{"source":"api","type":"click"}},{"id":2,"meta":{"source":"web","type":"view"}}]`,
		rowsJSON,
	)
}

func TestPostgresDecimalColumnTypeIsIndependentOfRowOrder(t *testing.T) {
	require.Equal(t, postgresInputColumnTypeBigInt, postgresInputColumnType("amount", []map[string]any{{"amount": int64(1)}, {"amount": json.Number("2")}}))
	value, err := decimal.Parse("1.25")
	require.NoError(t, err)
	for _, rows := range [][]map[string]any{
		{{"amount": int64(1)}, {"amount": value}},
		{{"amount": value}, {"amount": int64(1)}},
		{{"amount": int64(1)}, {"amount": json.Number("1.25")}},
	} {
		require.Equal(t, "numeric", postgresInputColumnType("amount", rows))
	}
}
