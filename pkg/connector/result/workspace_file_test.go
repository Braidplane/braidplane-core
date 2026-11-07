package result

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWorkspaceFileResultRoundTripAndClone(t *testing.T) {
	input := NewWorkspaceFileResult(&FileResult{
		ResourceID: "stable-file-id", Name: "crm.json", MIMEType: "application/json",
		Rows: []map[string]any{{"exact": int64(9007199254740993)}}, Content: []byte("private body"),
	}, 17, nil)
	encoded, err := json.Marshal(input)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private body")

	var decoded ExecutionResult
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	file, ok := decoded.File()
	require.True(t, ok)
	require.Equal(t, "stable-file-id", file.ResourceID)
	require.Equal(t, "crm.json", file.Name)
	require.Equal(t, "application/json", file.MIMEType)
	require.EqualValues(t, int64(9007199254740993), file.Rows[0]["exact"])

	cloned := input.Clone()
	clonedFile, ok := cloned.File()
	require.True(t, ok)
	clonedFile.Rows[0]["exact"] = int64(1)
	clonedFile.Content[0] = 'P'
	original, ok := input.File()
	require.True(t, ok)
	require.Equal(t, int64(9007199254740993), original.Rows[0]["exact"])
	require.Equal(t, byte('p'), original.Content[0])
}
