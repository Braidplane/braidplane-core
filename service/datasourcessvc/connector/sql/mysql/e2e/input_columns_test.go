package mysql_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/mysql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestNativeInputColumnsRemainDataAcrossSQLModes(t *testing.T) {
	if testing.Short() {
		t.Skip("requires the owned MySQL fixture")
	}
	for _, mode := range []string{"", "NO_BACKSLASH_ESCAPES", "ANSI_QUOTES", "NO_BACKSLASH_ESCAPES,ANSI_QUOTES"} {
		t.Run("mode="+mode, func(t *testing.T) {
			conn, err := mysql.New(transportsecurity.WithPlaintext(t.Context()), getTestConfig())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, conn.Close()) })
			_, err = conn.ExecuteStatement(t.Context(), "SET SESSION sql_mode = ?", []any{mode})
			require.NoError(t, err)
			for _, name := range []string{"customer's", `back\slash`, `double"quote`, "tick`name", "tick\\`name", "line\nname", "żółć", "x' /* SQL-shaped */", "{{unexpected}}"} {
				t.Run(name, func(t *testing.T) {
					observed, err := executeSQL(t.Context(), conn, "SELECT @@SESSION.sql_mode AS mode", nil)
					require.NoError(t, err)
					if mode == "" {
						require.Empty(t, observed.Rows[0]["mode"])
					} else {
						for _, flag := range strings.Split(mode, ",") {
							require.Contains(t, observed.Rows[0]["mode"], flag)
						}
					}
					rows := []map[string]any{
						{name: "first", "nilled": nil, "amount": json.Number("9007199254740993.10"), "meta": map[string]any{"source": "fixture"}},
						{name: "second"},
					}
					params := map[string]any{
						sinkbinding.InternalInputRows:    rows,
						sinkbinding.InternalInputColumns: []string{name, "missing", "nilled", "amount", "meta"},
						"marker":                         "bound-value",
					}
					const statement = "SELECT input.*, {{marker}} AS bound_marker FROM input"
					got, err := executeSQL(t.Context(), conn, statement, params)
					require.NoError(t, err)
					require.Equal(t, []string{name, "missing", "nilled", "amount", "meta", "bound_marker"}, got.ColumnNames)
					require.Len(t, got.Rows, 2)
					require.Equal(t, "first", got.Rows[0][name])
					require.Equal(t, "second", got.Rows[1][name])
					require.Equal(t, "9007199254740993.10", got.Rows[0]["amount"])
					require.JSONEq(t, `{"source":"fixture"}`, got.Rows[0]["meta"].(string))
					for _, row := range got.Rows {
						require.Nil(t, row["missing"])
						require.Nil(t, row["nilled"])
						require.Equal(t, "bound-value", row["bound_marker"])
					}
					require.Nil(t, got.Rows[1]["amount"])
					require.Nil(t, got.Rows[1]["meta"])
					require.Equal(t, json.Number("9007199254740993.10"), rows[0]["amount"])
					readOnly, err := executeSQLWithLimits(t.Context(), conn, statement, params, types.ExecutionLimits{ReadOnly: true})
					require.NoError(t, err)
					require.Equal(t, got.Rows, readOnly.Rows)
				})
			}
		})
	}
}
