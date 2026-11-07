package result

import (
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestCheckResponseSizeMatchesJSONEnvelope(t *testing.T) {
	t.Parallel()
	for _, rows := range [][]map[string]any{nil, {}, {{"text": "<\u2028\xff", "binary": []byte{0xff, 0}}}, {{"id": 1}, {"id": 2}}} {
		for _, r := range []*ExecutionResult{
			NewSQLResult("postgres", &SQLResult{Rows: rows, ColumnNames: []string{"text"}, RowCount: len(rows)}, 12, nil),
			NewMongoDBResult(&MongoDBResult{Documents: rows, OperationType: "find"}, 12, nil),
		} {
			encoded, err := json.Marshal(r)
			require.NoError(t, err)
			require.NoError(t, r.CheckResponseSize(int64(len(encoded))))
			require.ErrorIs(t, r.CheckResponseSize(int64(len(encoded)-1)), xerror.ExecutionResponseSizeExceeded())
			unchanged, err := json.Marshal(r)
			require.NoError(t, err)
			require.Equal(t, encoded, unchanged)
		}
	}
}
