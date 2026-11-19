package apischema

import (
	"encoding/json"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

func compileExport(t *testing.T, raw []byte) *jsonschema.Schema {
	t.Helper()
	var document any
	require.NoError(t, json.Unmarshal(raw, &document))
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	const id = "urn:core:mcp:test"
	require.NoError(t, c.AddResource(id, document))
	schema, err := c.Compile(id)
	require.NoError(t, err)
	return schema
}

func TestMCPEnvironmentContracts(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(Source())
	require.NoError(t, err)
	output := map[string]any{
		"name": "Demo", "slug": "demo", "color": "blue", "protected": false,
		"description": nil, "delete_reason": nil, "deleted_at": nil,
		"created_at": "2026-01-02T03:04:05Z", "updated_at": "2026-01-02T03:04:05Z",
	}
	for _, tc := range []struct {
		name    string
		samples []map[string]any
	}{
		{"NewEnvironment", []map[string]any{
			{"name": "Demo", "color": "blue"},
			{"name": "", "color": "blue"},
			{"color": "blue"},
			{"name": "Demo", "color": "other"},
			{"name": "Demo", "color": "blue", "slug": "Invalid!"},
			{"name": "Demo", "color": "blue", "protected": nil},
			{"name": "Demo", "color": "blue", "unknown": true},
		}},
		{"Environment", []map[string]any{output, {"name": "Demo"}}},
	} {
		raw, err := MCPObjectSchema(tc.name)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "components/schemas/")
		require.NotContains(t, string(raw), "NewUser")
		schema := compileExport(t, raw)
		for i, sample := range tc.samples {
			valid := i == 0
			require.Equal(t, valid, doc.Components.Schemas[tc.name].Value.VisitJSON(sample) == nil)
			require.Equal(t, valid, schema.Validate(sample) == nil, "%s sample %d", tc.name, i)
		}
	}
}

func TestMCPCompositionAndReferenceBoundaries(t *testing.T) {
	root := map[string]any{"type": "object", "properties": map[string]any{"choice": map[string]any{"$ref": "#/components/schemas/Choice"}}}
	components := map[string]map[string]any{
		"Root": root,
		"Choice": {"nullable": true, "oneOf": []any{
			map[string]any{"type": "integer", "minimum": 1, "exclusiveMinimum": true},
			map[string]any{"type": "string", "enum": []any{"yes"}},
		}},
	}
	raw, err := exportObject(components, "Root")
	require.NoError(t, err)
	schema := compileExport(t, raw)
	for _, v := range []any{nil, 2, "yes"} {
		require.NoError(t, schema.Validate(map[string]any{"choice": v}))
	}
	for _, v := range []any{1, "no", false} {
		require.Error(t, schema.Validate(map[string]any{"choice": v}))
	}
	for _, bad := range []map[string]any{
		{"x-internal": true},
		{"writeOnly": true},
		{"readOnly": true},
		{"discriminator": map[string]any{}},
		{"$ref": "https://example.invalid/private"},
		{"$ref": "#/components/schemas/Missing"},
	} {
		components["Choice"] = bad
		raw, err := exportObject(components, "Root")
		require.Error(t, err)
		require.Empty(t, raw)
	}
}

func TestMCPExportTracksReferencedConstraints(t *testing.T) {
	components := map[string]map[string]any{
		"Root": {"type": "object", "properties": map[string]any{
			"items": map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/Item"}},
			"child": map[string]any{"$ref": "#/components/schemas/Root"},
		}},
		"Item": {"type": "string", "maxLength": 4},
	}
	value := map[string]any{"child": map[string]any{"items": []any{"demo"}}}
	raw, err := exportObject(components, "Root")
	require.NoError(t, err)
	require.NoError(t, compileExport(t, raw).Validate(value))
	components["Item"]["maxLength"] = 3
	raw, err = exportObject(components, "Root")
	require.NoError(t, err)
	require.Error(t, compileExport(t, raw).Validate(value))
}
