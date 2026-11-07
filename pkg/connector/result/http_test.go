package result

import (
	"encoding/json"
	"testing"

	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/stretchr/testify/require"
)

func TestHTTPResultRowCount(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		rows int
	}{
		{"array", ` [{"id":1},{"id":2}] `, 2},
		{"mixed array", `[1,"text",null,[1,2],{}]`, 5},
		{"empty array", `[]`, 0},
		{"object with nested array", `{"items":[1,2,3]}`, 1},
		{"string", `"text"`, 1},
		{"number", `42`, 1},
		{"boolean", `true`, 1},
		{"null", `null`, 0},
		{"absent body", ``, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &HTTPResult{Body: json.RawMessage(tc.body)}
			require.Equal(t, tc.rows, r.RowCount())
		})
	}
	file := &HTTPResult{Body: json.RawMessage(`[1,2]`), File: &filepayload.FilePayloadMetadata{}}
	require.Zero(t, file.RowCount())
}
