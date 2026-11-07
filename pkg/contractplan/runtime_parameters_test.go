package contractplan

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompileRejectsEncodedUnknownRuntimeReference(t *testing.T) {
	in := testInput(t)
	data, err := json.Marshal(map[string]any{"statement": `{"sql":"SELECT \u007b\u007bruntime.unknown\u007d\u007d"}`})
	require.NoError(t, err)
	in.Operations[0].Content, err = json.Marshal(OperationContent{Data: data})
	require.NoError(t, err)
	_, _, err = Compile(in)
	require.ErrorContains(t, err, "unknown runtime parameter runtime.unknown")
}

func TestCompileValidatesOperationRuntimeParametersWithoutRewritingHistory(t *testing.T) {
	for _, expression := range []string{"{{runtime.unknown}}", "{{runtime.sink.replay_identity}}", "{{runtime.incremental.lower_bound.cursor}}", "{{runtime.run.date | add_days:-1}}"} {
		in := testInput(t)
		content, err := json.Marshal(OperationContent{Data: json.RawMessage(`{"statement":"SELECT ` + expression + `"}`)})
		require.NoError(t, err)
		in.Operations[0].Content = content
		_, _, err = Compile(in)
		require.ErrorContains(t, err, "runtime date expression", expression)
	}
	in := testInput(t)
	in.Operations[0].Content = json.RawMessage(`{"data":{"statement":"SELECT {{runtime.run.date}}"},"parameters":[]}`)
	raw, id, err := Compile(in)
	require.NoError(t, err)
	plan, err := Read(raw, id)
	require.NoError(t, err)
	// A historical definition is inspection evidence, even if new authoring rejects its syntax.
	plan.Operations[0].Content = json.RawMessage(`{"data":{"statement":"SELECT {{runtime.run_date}}"},"parameters":[]}`)
	plan.Operations[0].Digest = digest(plan.Operations[0].Content)
	historical, err := json.Marshal(plan)
	require.NoError(t, err)
	historical, err = canonical(historical)
	require.NoError(t, err)
	retained, err := Read(historical, digest(historical))
	require.NoError(t, err)
	require.JSONEq(t, string(plan.Operations[0].Content), string(retained.Operations[0].Content))
}
