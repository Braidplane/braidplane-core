package odcs

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/stretchr/testify/require"
)

func projectionInput(t *testing.T, properties string, objectExtra ...string) ProjectionInput {
	t.Helper()
	raw := json.RawMessage(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"dataset","version":"1","schema":[{"id":"row","name":"Row","logicalType":"object","properties":[` + properties + `]` + strings.Join(objectExtra, "") + `}]}`)
	record, err := Record(raw, "dataset")
	require.NoError(t, err)
	types := map[payload.StructuralKind]bool{}
	for _, kind := range payload.StructuralKindValues() {
		types[kind] = true
	}

	return ProjectionInput{
		Contract: "contract-a", Revision: "revision-a", ObjectID: "row", Digest: record.Digest, Document: raw,
		Mapping: ProjectionMapping{ID: "synthetic/json/v1", Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Types: types},
	}
}

func projectionCompile(t *testing.T, inputs ...ProjectionInput) *ProjectionResult {
	t.Helper()
	result, err := CompileProjection(ProjectionRequest{Inputs: inputs})
	require.NoError(t, err)
	return result
}

func projectionCode(t *testing.T, result *ProjectionResult, code string) {
	t.Helper()
	require.Nil(t, result.Plan)
	require.Empty(t, result.Digest)
	require.True(t, slices.ContainsFunc(result.Diagnostics, func(d ProjectionDiagnostic) bool { return d.Code == code }), "%+v", result.Diagnostics)
}

func TestProjectionRichDefaultsAndOriginalIdentity(t *testing.T) {
	input := projectionInput(t, `
		{"id":"v","name":"vector","logicalType":"vector","required":true,"logicalTypeOptions":{"dimensions":3}},
		{"id":"n","name":"child","logicalType":"object","properties":[{"id":"leaf","name":"a.b","logicalType":"integer","required":true}]},
		{"name":"items","logicalType":"array","items":{"logicalType":"string"},"logicalTypeOptions":{"minItems":1,"uniqueItems":true}},
		{"name":"map","logicalType":"map","map":{"key":{"logicalType":"string"},"value":{"logicalType":"boolean"}}},
		{"name":"instant","logicalType":"timestamp"},
		{"name":"date","logicalType":"date","logicalTypeOptions":{"format":"yyyy-MM-dd"}},
		{"name":"time","logicalType":"time","logicalTypeOptions":{"format":"HH:mm:ss"}},
		{"name":"local","logicalType":"timestamp","logicalTypeOptions":{"format":"yyyy-MM-ddTHH:mm:ss","timezone":false}}`)
	original := slices.Clone(input.Document)
	result := projectionCompile(t, input)
	require.Empty(t, result.Diagnostics)
	require.NotNil(t, result.Plan)
	require.Equal(t, original, input.Document)
	out := result.Plan.Inputs[0]
	require.Equal(t, input.Digest, out.Digest)
	require.Equal(t, input.Digest, out.ResolvedDigest)
	properties := out.Structure.Root.Properties
	require.Equal(t, 32, properties[0].Type.Items.Bits)
	require.Equal(t, 3, properties[0].Type.Dimension)
	require.Equal(t, "leaf", properties[1].Type.Properties[0].ID)
	require.Equal(t, 32, properties[1].Type.Properties[0].Type.Bits)
	require.Equal(t, payload.StructuralInstant, properties[4].Type.Kind)
	require.Equal(t, payload.StructuralDate, properties[5].Type.Kind)
	require.Equal(t, payload.StructuralLocalTime, properties[6].Type.Kind)
	require.Equal(t, payload.StructuralLocalDatetime, properties[7].Type.Kind)
	for _, expected := range []struct {
		path  string
		value any
	}{
		{"$.schema[0].properties[0].logicalTypeOptions.elementType", "float32"},
		{"$.schema[0].properties[4].logicalTypeOptions.defaultTimezone", "Etc/UTC"},
		{"$.schema[0].properties[4].logicalTypeOptions.format", "YYYY-MM-DDTHH:mm:ss.SSSZ"},
	} {
		require.True(t, slices.ContainsFunc(out.Defaults, func(d ProjectionDefault) bool {
			return d.Path == expected.path && d.Value == expected.value && strings.Contains(d.Source, "ODCS/v3.2.0")
		}))
	}
	// The new foundation does not make nested structure publicly executable.
	legacy, err := Convert(input.Document, input.ObjectID)
	require.NoError(t, err)
	require.Nil(t, legacy.Shape)
	require.Equal(t, result, projectionCompile(t, input))
}

func TestProjectionPhysicalCapabilitiesAndExactNumbers(t *testing.T) {
	input := projectionInput(t, `{"name":"amount","logicalType":"number","physicalType":"DECIMAL(20,2)","logicalTypeOptions":{"minimum":9007199254740993.01,"maximum":9007199254740993.02}}`)
	input.Mapping.Physical = map[string]PhysicalTypeMapping{"DECIMAL(20,2)": {LogicalType: "number", Kind: payload.StructuralDecimal, Precision: 20, Scale: 2}}
	result := projectionCompile(t, input)
	require.Empty(t, result.Diagnostics)
	field := result.Plan.Inputs[0].Structure.Root.Properties[0].Type
	require.Equal(t, payload.StructuralDecimal, field.Kind)
	require.Equal(t, "9007199254740993.01", field.Min.String())
	require.Equal(t, "DECIMAL(20,2)", result.Plan.Inputs[0].Declarations[1].PhysicalType)
	input.Mapping.Physical["DECIMAL(20,2)"] = PhysicalTypeMapping{LogicalType: "integer", Kind: payload.StructuralInteger, Bits: 32}
	projectionCode(t, projectionCompile(t, input), "physical_mapping_conflict")
	delete(input.Mapping.Physical, "DECIMAL(20,2)")
	projectionCode(t, projectionCompile(t, input), "physical_mapping_conflict")
	input = projectionInput(t, `{"name":"value","logicalType":"integer"}`)
	input.Mapping.Types[payload.StructuralInteger] = false
	projectionCode(t, projectionCompile(t, input), "capability_unsupported")
}

func TestProjectionUnsupportedSelectedDeclarationsFailClosed(t *testing.T) {
	for _, property := range []string{
		`{"name":"v","logicalType":"vector","logicalTypeOptions":{"dimensions":3,"elementType":"float16"}}`,
		`{"name":"v","logicalType":"vector","logicalTypeOptions":{"dimensions":3,"normalized":true}}`,
		`{"name":"n","logicalType":"integer","logicalTypeOptions":{"format":"u64"}}`,
		`{"name":"n","logicalType":"number"}`,
		`{"name":"n","logicalType":"integer","logicalTypeOptions":{"exclusiveMinimum":1}}`,
		`{"name":"s","logicalType":"string","logicalTypeOptions":{"pattern":"secret-pattern"}}`,
		`{"name":"t","logicalType":"timestamp","logicalTypeOptions":{"format":"custom-secret-format"}}`,
		`{"name":"o","logicalType":"object","logicalTypeOptions":{"maxProperties":2}}`,
		`{"name":"a","logicalType":"array"}`,
	} {
		input := projectionInput(t, property)
		result := projectionCompile(t, input)
		require.Nil(t, result.Plan, "%s", property)
		require.NotEmpty(t, result.Diagnostics)
		require.NotEmpty(t, result.Coverage[0])
		for _, diagnostic := range result.Diagnostics {
			require.Equal(t, 0, diagnostic.Input)
			require.NotEmpty(t, diagnostic.Phase)
			require.NotContains(t, diagnostic.Path+diagnostic.Message, "secret")
		}
		for _, entry := range result.Coverage[0] {
			require.NotEqual(t, "enforced", entry.State)
		}
	}
}

func TestProjectionVariablesSinglePassAndRevalidation(t *testing.T) {
	for _, tc := range []struct {
		value    string
		supplied bool
		want     string
	}{
		{"", false, "string"}, {"", true, "string"}, {"boolean", true, "boolean"},
	} {
		input := projectionInput(t, `{"name":"${NAME:-literal}","logicalType":"${TYPE:-string}"}`)
		input.Variables = map[string]ResolutionValue{}
		if tc.supplied {
			input.Variables["TYPE"] = ResolutionValue{Value: tc.value, Revision: "vars-1"}
		}
		result := projectionCompile(t, input)
		require.Empty(t, result.Diagnostics)
		out := result.Plan.Inputs[0]
		require.Equal(t, tc.want, out.Declarations[1].LogicalType)
		require.NotEqual(t, input.Digest, out.ResolvedDigest)
		require.Len(t, out.Resolutions, 2)
		require.Contains(t, string(input.Document), "${TYPE:-string}")
	}
	t.Setenv("TYPE", "string")
	for _, tc := range []struct{ token, value string }{
		{"${TYPE}", ""},
		{"${TYPE}", "${OTHER}"},
		{"${TYPE}", "{{runtime.secrets.x}}"},
		{"${TYPE", "string"},
		{"${TYPE:-${OTHER}}", ""},
		{"${TYPE:-a{b}", ""},
	} {
		input := projectionInput(t, fmt.Sprintf(`{"name":%q,"logicalType":"string"}`, tc.token))
		input.Variables = map[string]ResolutionValue{"TYPE": {Value: tc.value, Revision: "vars-1"}}
		require.Nil(t, projectionCompile(t, input).Plan, "%+v", tc)
	}
	invalidType := projectionInput(t, `{"name":"safe","logicalType":"${TYPE}"}`)
	invalidType.Variables = map[string]ResolutionValue{"TYPE": {Value: "vendor", Revision: "vars-1"}}
	projectionCode(t, projectionCompile(t, invalidType), "resolution_invalid")
	// String tokens cannot make integer/boolean positions valid at admission.
	input := projectionInput(t, `{"name":"value","logicalType":"string"}`)
	for _, property := range []string{`{"name":"value","logicalType":"string","required":"${REQUIRED}"}`, `{"name":"v","logicalType":"vector","logicalTypeOptions":{"dimensions":"${N}"}}`} {
		input.Document = json.RawMessage(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"dataset","version":"1","schema":[{"id":"row","name":"Row","properties":[` + property + `]}]}`)
		projectionCode(t, projectionCompile(t, input), "document_invalid")
	}
}

func TestProjectionRelationshipClosurePairingCyclesAndOrder(t *testing.T) {
	input := projectionInput(t, `{"id":"a","name":"a","logicalType":"integer"},{"id":"b","name":"b","logicalType":"string"}`, `,"relationships":[{"from":["schema/row/properties/b","schema/row/properties/a"],"to":["schema/row/properties/b","schema/row/properties/a"]},{"from":"schema/row/properties/a","to":"https://example.invalid/other.yaml#schema/row/properties/a"}]`)
	request := ProjectionRequest{Inputs: []ProjectionInput{input, input}, Relationships: []RelationshipSelection{
		{Input: 1, Target: 0, Path: "$.schema[0].relationships[0]"},
		{Input: 0, Target: 1, Path: "$.schema[0].relationships[0]"},
		{Input: 0, Target: 0, Path: "$.schema[0].relationships[1]"},
	}}
	result, err := CompileProjection(request)
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.Len(t, result.Plan.Relationships, 3)
	require.True(t, slices.ContainsFunc(result.Plan.Inputs[0].Defaults, func(d ProjectionDefault) bool {
		return d.Path == "$.schema[0].relationships[0].type" && d.Value == "foreignKey"
	}))
	pairs := result.Plan.Relationships[0].Pairs
	require.Equal(t, []string{"b"}, pairs[0].From.IDs)
	require.Equal(t, []string{"b"}, pairs[0].To.IDs)
	require.Equal(t, []string{"a"}, pairs[1].To.IDs)
	slices.Reverse(request.Relationships)
	other, err := CompileProjection(request)
	require.NoError(t, err)
	require.Equal(t, result.Digest, other.Digest)
	request.Relationships[0].Target = 99
	invalid, err := CompileProjection(request)
	require.NoError(t, err)
	projectionCode(t, invalid, "relationship_target_invalid")
	// Unmapped external declarations remain inert and do not fetch anything.
	require.NotNil(t, projectionCompile(t, input).Plan)
}

func TestProjectionRelationshipKeyFailures(t *testing.T) {
	for _, target := range []string{"schema/row/properties/missing", "schema/row/properties/b", "${UNRESOLVED}"} {
		input := projectionInput(t, `{"id":"a","name":"a","logicalType":"integer"},{"id":"b","name":"b","logicalType":"string"}`, fmt.Sprintf(`,"relationships":[{"from":"schema/row/properties/a","to":%q}]`, target))
		result, err := CompileProjection(ProjectionRequest{Inputs: []ProjectionInput{input}, Relationships: []RelationshipSelection{{Input: 0, Target: 0, Path: "$.schema[0].relationships[0]"}}})
		require.NoError(t, err)
		require.Nil(t, result.Plan)
		require.NotEmpty(t, result.Diagnostics)
	}
	input := projectionInput(t, `{"id":"a","name":"a","logicalType":"integer","relationships":[{"to":"schema/row/properties/a"}]}`)
	result, err := CompileProjection(ProjectionRequest{Inputs: []ProjectionInput{input}, Relationships: []RelationshipSelection{{Input: 0, Target: 0, Path: "$.schema[0].properties[0].relationships[0]"}}})
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.NotNil(t, result.Plan)
}

func TestProjectionLimitsAndIdentityFailures(t *testing.T) {
	input := projectionInput(t, `{"name":"v","logicalType":"string"}`)
	bad := input
	bad.Digest = "sha256:wrong"
	projectionCode(t, projectionCompile(t, bad), "identity_invalid")
	inputs := make([]ProjectionInput, MaxProjectionInputs+1)
	projectionCode(t, projectionCompile(t, inputs...), "projection_limit")
	input = projectionInput(t, `{"name":"${VALUE}","logicalType":"string"}`)
	input.Variables = map[string]ResolutionValue{"VALUE": {Value: strings.Repeat("x", MaxDocumentBytes), Revision: "vars-1"}}
	require.Nil(t, projectionCompile(t, input).Plan)
	nested := `{"name":"value","logicalType":"string"}`
	for range 33 {
		nested = `{"name":"parent","logicalType":"object","properties":[` + nested + `]}`
	}
	// Admission has a separate 64-container depth limit.
	input = projectionInput(t, `{"name":"v","logicalType":"string"}`)
	input.Document = json.RawMessage(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"dataset","version":"1","schema":[{"id":"row","name":"Row","properties":[` + nested + `]}]}`)
	projectionCode(t, projectionCompile(t, input), "document_invalid")
}

func TestProjectionBudgetsDoNotReturnPartialPlans(t *testing.T) {
	input := projectionInput(t, `{"name":"safe","logicalType":"string"}`)
	for _, properties := range []string{
		strings.TrimSuffix(strings.Repeat(`{"name":"p","logicalType":"string"},`, 9000), ","),
		strings.TrimSuffix(strings.Repeat(`{"name":"${NAME}","logicalType":"string"},`, 100), ","),
	} {
		input.Document = json.RawMessage(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"dataset","version":"1","schema":[{"id":"row","name":"Row","properties":[` + properties + `]}]}`)
		record, err := Record(input.Document, "dataset")
		require.NoError(t, err)
		input.Digest = record.Digest
		input.Variables = map[string]ResolutionValue{"NAME": {Value: strings.Repeat("x", 100_000), Revision: "vars-1"}}
		require.Nil(t, projectionCompile(t, input).Plan)
	}
	input = projectionInput(t, `{"name":"x","logicalType":"string"}`)
	result := &ProjectionResult{}
	work := payload.DefaultStructuralLimits().Work
	c := &projectionCompiler{input: &input, output: &ProjectedInput{}, result: result, work: &work}
	require.False(t, c.spend(1, "$.schema"))
	projectionCode(t, result, "projection_limit")
}

func TestProjectionTemporalFormatAndShorthandKeys(t *testing.T) {
	input := projectionInput(t, `{"name":"instant","logicalType":"timestamp","required":true}`)
	result := projectionCompile(t, input)
	require.Empty(t, result.Diagnostics)
	s := result.Plan.Inputs[0].Structure
	require.NoError(t, payload.ValidateStructuralValue(s, map[string]any{"instant": "2026-10-17T10:20:30.123Z"}, payload.DefaultStructuralLimits()))
	for _, timestamp := range []string{"2026-10-17T10:20:30Z", "2026-10-17T10:20:30.1Z", "2026-10-17T10:20:30.1234Z"} {
		require.Error(t, payload.ValidateStructuralValue(s, map[string]any{"instant": timestamp}, payload.DefaultStructuralLimits()))
	}
	input = projectionInput(t, `{"name":"key","logicalType":"string"}`, `,"relationships":[{"from":"Row.key","to":"Row.key"}]`)
	result, err := CompileProjection(ProjectionRequest{Inputs: []ProjectionInput{input}, Relationships: []RelationshipSelection{{Input: 0, Target: 0, Path: "$.schema[0].relationships[0]"}}})
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.Equal(t, "key", result.Plan.Relationships[0].Pairs[0].From.Path[0].Name)
	require.Equal(t, []string{""}, result.Plan.Relationships[0].Pairs[0].From.IDs)
}

func TestProjectionNestedCoverageIsTruthful(t *testing.T) {
	input := projectionInput(t, `{"name":"items","logicalType":"array","items":{"logicalType":"map","map":{"key":{"logicalType":"string"},"value":{"logicalType":"integer","description":"${DESCRIPTIVE}","quality":[{"type":"text","description":"not evaluated"}]}}}}`)
	result := projectionCompile(t, input)
	require.Empty(t, result.Diagnostics)
	states := map[string]string{}
	for _, entry := range result.Coverage[0] {
		states[entry.Path] = entry.State
	}
	base := "$.schema[0].properties[0].items.map"
	require.Equal(t, "validated", states[base+".key"])
	require.Equal(t, "validated", states[base+".value"])
	require.Equal(t, "unresolved", states[base+".value.description"])
	require.Equal(t, "unsupported", states[base+".value.quality"])
	legacy, err := Convert(input.Document, input.ObjectID)
	require.NoError(t, err)
	require.Nil(t, legacy.Shape)
}

func TestProjectionStructuralAndCapabilityLimits(t *testing.T) {
	input := projectionInput(t, `{"name":"value","logicalType":"string"}`)
	input.Mapping.Types[payload.StructuralKind(-1)] = true
	projectionCode(t, projectionCompile(t, input), "projection_limit")
	input.Mapping.Types = map[payload.StructuralKind]bool{payload.StructuralKind(-1): true}
	projectionCode(t, projectionCompile(t, input), "capability_unsupported")
	input = projectionInput(t, `{"name":"value","logicalType":"string"}`)
	input.Variables = map[string]ResolutionValue{"LARGE": {Value: strings.Repeat("x", MaxProjectionBytes+1)}}
	projectionCode(t, projectionCompile(t, input), "projection_limit")
	for _, property := range []string{
		strings.TrimSuffix(strings.Repeat(`{"name":"p","logicalType":"string"},`, 4100), ","),
		`{"name":"outer",` + strings.Repeat(`"logicalType":"array","items":{`, 34) + `"logicalType":"string"` + strings.Repeat(`}`, 35),
	} {
		input = projectionInput(t, property)
		projectionCode(t, projectionCompile(t, input), "projection_limit")
	}
}
