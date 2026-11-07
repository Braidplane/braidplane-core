package contractplan

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/stretchr/testify/require"
)

func TestSpecializationPinsExplicitInputsWithoutChangingTemplate(t *testing.T) {
	t.Parallel()
	in := testInput(t)
	in.Projection = testProjection(t)
	selected := &in.Projection.Inputs[0]
	document := strings.Replace(string(selected.Document), `"logicalType":"integer"`, `"logicalType":"${KIND:-integer}"`, 1)
	record, err := odcs.Record(json.RawMessage(document), "orders")
	require.NoError(t, err)
	selected.Document, selected.Digest = record.Document, record.Digest
	selected.Mapping.Types[payload.StructuralString] = true
	selected.Variables = map[string]odcs.ResolutionValue{"KIND": {Value: "integer", Revision: "template-v1"}}
	raw, id, err := Compile(in)
	require.NoError(t, err)
	target, targetID, err := Specialize(raw, id, map[string]odcs.ResolutionValue{"KIND": {Value: "string", Revision: "env-v2"}})
	require.NoError(t, err)
	require.NotEqual(t, id, targetID)
	template, err := Read(raw, id)
	require.NoError(t, err)
	executable, err := Read(target, targetID)
	require.NoError(t, err)
	require.Equal(t, template.AuthoredSpecHash, executable.AuthoredSpecHash)
	require.Equal(t, template.Source, executable.Source)
	require.Equal(t, "integer", template.ProjectionInputs.Inputs[0].Variables["KIND"].Value)
	require.Equal(t, "string", executable.ProjectionInputs.Inputs[0].Variables["KIND"].Value)
	require.Equal(t, "env-v2", executable.ProjectionInputs.Inputs[0].Variables["KIND"].Revision)
	require.NotEqual(t, template.ProjectionDigest, executable.ProjectionDigest)
	for name, values := range map[string]map[string]odcs.ResolutionValue{
		"undeclared":        {"OTHER": {Value: "string", Revision: "v2"}},
		"missing revision":  {"KIND": {Value: "string"}},
		"secret expression": {"KIND": {Value: "{{runtime.secrets.type}}", Revision: "v2"}},
		"nested expansion":  {"KIND": {Value: "${OTHER}", Revision: "v2"}},
		"unsupported type":  {"KIND": {Value: "unsupported", Revision: "v2"}},
	} {
		t.Run(name, func(t *testing.T) { _, _, err := Specialize(raw, id, values); require.Error(t, err) })
	}
}
