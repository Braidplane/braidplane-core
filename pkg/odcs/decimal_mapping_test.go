package odcs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/stretchr/testify/require"
)

func decimalDocument(property, mappings string) []byte {
	return []byte(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"offers","version":"1","customProperties":[{"vendor":"braidplane","property":"decimalPhysicalTypes","value":` + mappings + `}],"schema":[{"id":"offer","name":"Offer","properties":[` + property + `]}]}`)
}

func TestConvertRevisionOwnedDecimals(t *testing.T) {
	raw := decimalDocument(`{"name":"quoted_price","logicalType":"number","physicalType":"DECIMAL(18,2)","logicalTypeOptions":{"minimum":0.1},"enum":[{"value":1.20}]},{"name":"unit_price","logicalType":"number","physicalType":"DECIMAL(18,4)","logicalTypeOptions":{"maximum":9999.9999}}`, `{"DECIMAL(18,2)":{"precision":18,"scale":2},"DECIMAL(18,4)":{"precision":18,"scale":4}}`)
	result, err := Convert(raw, "offer")
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.Equal(t, DecimalCompilerID, result.CompilerID)
	quoted, unit := result.Shape.Fields[0], result.Shape.Fields[1]
	require.Equal(t, model.ContractFieldTypeValueDecimal, quoted.Type)
	require.Equal(t, 18, *quoted.Precision)
	require.Equal(t, 2, *quoted.Scale)
	require.Equal(t, json.Number("0.1"), quoted.Validations.Min)
	require.Equal(t, []any{json.Number("1.20")}, quoted.Validations.Enum)
	require.Equal(t, model.ContractFieldTypeValueDecimal, unit.Type)
	require.Equal(t, 4, *unit.Scale)
	for _, path := range []string{"$.schema[0].properties[0].physicalType", "$.customProperties[0].value.*[0]"} {
		found := false
		for _, coverage := range result.Coverage {
			if coverage.Path == path {
				require.Equal(t, "enforced", coverage.State)
				found = true
			}
		}
		require.True(t, found, path)
	}
	// Other vendors retain opaque metadata and the unchanged legacy conversion.
	legacy, err := Convert([]byte(strings.Replace(string(raw), `"vendor":"braidplane"`, `"vendor":"other"`, 1)), "offer")
	require.NoError(t, err)
	require.Nil(t, legacy.Shape) // 0.1 cannot become an exact float bound.
	require.Equal(t, CompilerID, legacy.CompilerID)
}

func TestConvertDecimalDeclarationsRejectInvalidOrLossyShapes(t *testing.T) {
	for _, tc := range []struct{ name, property, mappings, code string }{
		{"precision loss", `{"logicalTypeOptions":{"minimum":0.001},"name":"price","logicalType":"number","physicalType":"PRICE"}`, `{"PRICE":{"precision":18,"scale":2}}`, "mapping_invalid"},
		{"missing mapping", `{"name":"price","logicalType":"number","physicalType":"UNKNOWN"}`, `{"PRICE":{"precision":18,"scale":2}}`, "decimal_mapping_missing"},
		{"missing physical", `{"name":"price","logicalType":"number"}`, `{"PRICE":{"precision":18,"scale":2}}`, "decimal_mapping_missing"},
		{"conflicting type", `{"name":"price","logicalType":"string","physicalType":"PRICE"}`, `{"PRICE":{"precision":18,"scale":2}}`, "decimal_mapping_conflict"},
		{"conflicting format", `{"name":"price","logicalType":"number","physicalType":"PRICE","logicalTypeOptions":{"format":"f64"}}`, `{"PRICE":{"precision":18,"scale":2}}`, "decimal_mapping_conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Convert(decimalDocument(tc.property, tc.mappings), "offer")
			require.NoError(t, err)
			require.Nil(t, result.Shape)
			require.Contains(t, diagnosticCodes(result), tc.code)
		})
	}
	for _, mappings := range []string{`null`, `{}`, `[]`, `{"PRICE":{}}`, `{"PRICE":{"precision":18}}`, `{"PRICE":{"scale":2}}`, `{"PRICE":{"precision":18,"scale":19}}`, `{"PRICE":{"precision":39,"scale":2}}`, `{"PRICE":{"precision":18,"scale":2,"extra":true}}`, `{"private-${value}":{"precision":18,"scale":2}}`, `{"PRICE":{"precision":18,"scale":null}}`} {
		t.Run(mappings, func(t *testing.T) {
			result, err := Convert(decimalDocument(`{"name":"price","logicalType":"number","physicalType":"PRICE"}`, mappings), "offer")
			require.NoError(t, err)
			require.Nil(t, result.Shape)
			requireDiagnostic(t, result, "$.customProperties[0].value", "decimal_mapping_invalid")
			for _, diagnostic := range result.Diagnostics {
				require.NotContains(t, diagnostic.Path+diagnostic.Message, "private")
			}
		})
	}
}

func diagnosticCodes(result *ConversionResult) []string {
	codes := []string{}
	for _, diagnostic := range result.Diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	return codes
}

func TestDecimalDefinitionSharedBySchemaObjects(t *testing.T) {
	raw := decimalDocument(`{"name":"price","logicalType":"number","physicalType":"PRICE"}`, `{"PRICE":{"precision":18,"scale":2},"UNIT":{"precision":18,"scale":4}}`)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	doc["schema"] = append(doc["schema"].([]any), map[string]any{"id": "other", "name": "Other", "properties": []any{map[string]any{"name": "price", "logicalType": "number", "physicalType": "PRICE"}}})
	raw, err := json.Marshal(doc)
	require.NoError(t, err)
	first, err := Convert(raw, "offer")
	require.NoError(t, err)
	second, err := Convert(raw, "other")
	require.NoError(t, err)
	require.Empty(t, first.Diagnostics)
	require.Empty(t, second.Diagnostics)
	require.Equal(t, first.Shape.Fields, second.Shape.Fields)
	require.Equal(t, first.Digest, second.Digest)
	changed, err := Convert([]byte(strings.Replace(string(raw), `"scale":2`, `"scale":3`, 1)), "offer")
	require.NoError(t, err)
	require.Empty(t, changed.Diagnostics)
	require.NotEqual(t, first.Digest, changed.Digest)
	require.Equal(t, 3, *changed.Shape.Fields[0].Scale)
	doc["customProperties"] = append(doc["customProperties"].([]any), doc["customProperties"].([]any)[0])
	raw, err = json.Marshal(doc)
	require.NoError(t, err)
	duplicate, err := Convert(raw, "offer")
	require.NoError(t, err)
	require.Nil(t, duplicate.Shape)
	requireDiagnostic(t, duplicate, "$.customProperties[1].value", "decimal_mapping_invalid")
}
