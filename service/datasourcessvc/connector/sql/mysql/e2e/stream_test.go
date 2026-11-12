package mysql_test

import (
	"encoding/json"
	"io"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/testutil/filestreamfixture"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql/mysql"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

func TestMySQLRetainedSQLStream(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable database fixture")
	}
	store := filestreamfixture.New()
	ctx := filestream.WithStore(transportsecurity.WithPlaintext(t.Context()), store)
	connector, err := mysql.New(ctx, getTestConfig())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connector.Close()) })
	limits := types.ExecutionLimits{MaxResultRows: 1, MaxResponseBytes: 1024, MaxFileBytes: 4096}
	got, err := connector.Execute(ctx, json.RawMessage(`{"type":"mysql","format":"file","sql":"SELECT CAST(9007199254740993 AS SIGNED) AS id, CAST(123456789.123456789 AS DECIMAL(18,9)) AS amount, NULL AS nullable, CAST('bytes' AS BINARY) AS bytes"}`), nil, limits)
	require.NoError(t, err)
	require.NoError(t, got.Error())
	output, ok := got.SQL()
	require.True(t, ok)
	require.Empty(t, output.Rows)
	require.Equal(t, 1, output.RowCount)
	body, err := store.Open(ctx, *output.Reference, limits.MaxFileBytes)
	require.NoError(t, err)
	defer func() { require.NoError(t, body.Close()) }()
	raw, err := io.ReadAll(body)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"id":9007199254740993`)
	require.JSONEq(t, `{"column_names":["id","amount","nullable","bytes"],"rows":[{"id":9007199254740993,"amount":"123456789.123456789","nullable":null,"bytes":"bytes"}],"row_count":1}`, string(raw))
	for _, statement := range []string{
		`SELECT 1 AS id UNION ALL SELECT 2`,
		`SELECT 1 AS duplicate, 2 AS duplicate`,
		`CREATE TABLE forbidden_stream_write(id int)`,
	} {
		query, err := json.Marshal(map[string]string{"type": "mysql", "format": "file", "sql": statement})
		require.NoError(t, err)
		got, err := connector.Execute(ctx, query, nil, limits)
		require.NoError(t, err)
		require.Error(t, got.Error())
		output, _ := got.SQL()
		require.Nil(t, output)
	}
}
