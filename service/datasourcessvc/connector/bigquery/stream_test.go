package bigquery

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/testutil/filestreamfixture"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

func TestFileQueryPreservesTypesAcrossPagesAndDiscardsFailures(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "provider-error"}[fail], func(t *testing.T) {
			store := filestreamfixture.New()
			ctx := filestream.WithStore(t.Context(), store)
			provider := &stubProvider{queryPagesFunc: func(_ context.Context, request QueryRequest, consume func(*QueryResult) error) error {
				require.Equal(t, 2, request.MaxResultRows)
				require.Equal(t, "SELECT @id", request.SQL)
				for range 2 {
					if err := consume(&QueryResult{Schema: []Field{{Name: "id", Type: "INTEGER"}, {Name: "decimal", Type: "NUMERIC"}, {Name: "nullable", Type: "STRING"}}, Rows: []map[string]any{{"id": "9007199254740993", "decimal": "123456789.123456789", "nullable": nil}}}); err != nil {
						return err
					}
					if fail {
						return io.ErrUnexpectedEOF
					}
				}
				return nil
			}}
			conn := newWithClient(Config{ProjectID: "fixture"}, provider)
			got, err := conn.Execute(ctx, json.RawMessage(`{"type":"bigquery","sql":"SELECT {{id}}","format":"file"}`), map[string]types.TypedParam{"id": {Value: "value", DataType: "text"}}, types.ExecutionLimits{MaxResultRows: 2, MaxResponseBytes: 4096, MaxFileBytes: 8192})
			require.NoError(t, err)
			if fail {
				require.ErrorIs(t, got.Error(), io.ErrUnexpectedEOF)
				output, _ := got.SQL()
				require.Nil(t, output)
				return
			}
			require.NoError(t, got.Error())
			output, ok := got.SQL()
			require.True(t, ok)
			require.Empty(t, output.Rows)
			require.Equal(t, 2, output.RowCount)
			body, err := store.Open(ctx, *output.Reference, 8192)
			require.NoError(t, err)
			defer func() { require.NoError(t, body.Close()) }()
			raw, err := io.ReadAll(body)
			require.NoError(t, err)
			require.Contains(t, string(raw), `"id":9007199254740993`)
			require.JSONEq(t, `{"column_names":["id","decimal","nullable"],"rows":[{"id":9007199254740993,"decimal":"123456789.123456789","nullable":null},{"id":9007199254740993,"decimal":"123456789.123456789","nullable":null}],"row_count":2}`, string(raw))
			public, err := json.Marshal(got)
			require.NoError(t, err)
			require.NotContains(t, string(public), output.Reference.ID)
		})
	}
}
