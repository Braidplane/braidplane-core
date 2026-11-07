package contractplan

import (
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/stretchr/testify/require"
)

func TestOperationPromisesDefaultAndConjoinWithoutChangingAuthoredIdentity(t *testing.T) {
	in := testInput(t)
	original, originalID, err := Compile(in)
	require.NoError(t, err)
	var content OperationContent
	require.NoError(t, json.Unmarshal(in.Operations[0].Content, &content))
	content.OutputContract = json.RawMessage(`{"name":"operation","representation":"rows","fields":[{"name":"id","type":"number","required":true}]}`)
	in.Operations[0].Content, err = json.Marshal(content)
	require.NoError(t, err)
	encoded, id, err := Compile(in)
	require.NoError(t, err)
	require.NotEqual(t, originalID, id)
	old, err := Read(original, originalID)
	require.NoError(t, err)
	plan, err := Read(encoded, id)
	require.NoError(t, err)
	require.Equal(t, old.AuthoredSpecHash, plan.AuthoredSpecHash)
	runtime, err := plan.RuntimePipeline()
	require.NoError(t, err)
	require.Equal(t, "operation", runtime.Steps[0].Query.OutputContract.Name)
	var authored pipelinespec.Pipeline
	require.NoError(t, json.Unmarshal(plan.Authored, &authored))
	require.Nil(t, authored.Steps[0].Query.OutputContract)
	var stronger payload.Contract
	require.NoError(t, json.Unmarshal([]byte(`{"name":"step","representation":"rows","fields":[{"name":"id","type":"integer","required":true},{"name":"amount","type":"number","required":true}]}`), &stronger))
	authored.Steps[0].Query.OutputContract = &stronger
	in.Authored, err = json.Marshal(authored)
	require.NoError(t, err)
	encoded, id, err = Compile(in)
	require.NoError(t, err)
	plan, err = Read(encoded, id)
	require.NoError(t, err)
	runtime, err = plan.RuntimePipeline()
	require.NoError(t, err)
	require.Equal(t, "step", runtime.Steps[0].Query.OutputContract.Name)
	_, operationOutput, err := OperationContracts(plan.Operations[0])
	require.NoError(t, err)
	require.Len(t, operationOutput.Fields, 1)
	// Replacing the same field with an incompatible step promise is rejected.
	require.NoError(t, json.Unmarshal([]byte(`{"name":"step","fields":[{"name":"id","type":"string","required":true}]}`), authored.Steps[0].Query.OutputContract))
	in.Authored, err = json.Marshal(authored)
	require.NoError(t, err)
	_, _, err = Compile(in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "post_query")
}

func TestLegacyCompiledPlanReadDoesNotReinterpretOperationObligations(t *testing.T) {
	in := testInput(t)
	raw, id, err := Compile(in)
	require.NoError(t, err)
	plan, err := Read(raw, id)
	require.NoError(t, err)
	plan.FormatVersion, plan.CompilerID = 1, "braidplane/contract-plan/v1"
	raw, err = json.Marshal(plan)
	require.NoError(t, err)
	raw, err = canonical(raw)
	require.NoError(t, err)
	legacy, err := Read(raw, digest(raw))
	require.NoError(t, err)
	require.Equal(t, 1, legacy.FormatVersion)
	_, err = legacy.RuntimePipeline()
	require.ErrorIs(t, err, ErrInvalid)
}
