package bigquery

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestNormalizedResultBudgetPreservesExactBoundaryAndLastField(t *testing.T) {
	query := &QueryResult{
		Schema: []Field{{Name: "id", Type: "STRING"}, {Name: "id", Type: "INTEGER"}, {Name: "active", Type: "BOOLEAN"}, {Name: "json", Type: "JSON"}, {Name: "<escaped>\"\n", Type: "STRING"}},
		Rows:   []map[string]any{{"id": "123", "active": "true", "json": `{"value":"escaped\ntext"}`}, {"id": "456", "active": "false", "json": `null`}},
	}
	want := &result.SQLResult{
		ColumnNames: []string{"id", "id", "active", "json", "<escaped>\"\n"},
		RowCount:    2,
		Rows: []map[string]any{
			{"id": int64(123), "active": true, "json": map[string]any{"value": "escaped\ntext"}, "<escaped>\"\n": nil},
			{"id": int64(456), "active": false, "json": nil, "<escaped>\"\n": nil},
		},
	}
	encoded, err := json.Marshal(want)
	require.NoError(t, err)
	got, err := sqlResultFromQuery(query, int64(len(encoded)))
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Equal(t, "123", query.Rows[0]["id"], "normalization must not mutate provider-owned rows")
	got, err = sqlResultFromQuery(query, int64(len(encoded)-1))
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
	require.Nil(t, got)
}

func TestNormalizedResultBudgetKeepsEmptyResults(t *testing.T) {
	for _, query := range []*QueryResult{nil, {}} {
		got, err := sqlResultFromQuery(query, 1024)
		require.NoError(t, err)
		require.Equal(t, &result.SQLResult{Rows: []map[string]any{}, ColumnNames: []string{}}, got)
	}
}
