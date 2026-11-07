package odcs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".json")
	require.NoError(t, err)
	return raw
}

func TestConvertSupportedFixture(t *testing.T) {
	raw := fixture(t, "supported")
	result, err := Convert(raw, "order")
	require.NoError(t, err)
	require.Empty(t, result.Diagnostics)
	require.True(t, result.DocumentValid)
	require.Equal(t, PinnedVersion, result.ODCSVersion)
	recorded, err := Record(raw, "orders")
	require.NoError(t, err)
	require.Equal(t, recorded.Digest, result.Digest)
	require.Equal(t, "order", result.Shape.Name)
	require.Len(t, result.Shape.Fields, 8)
	fields := result.Shape.Fields
	require.Equal(t, model.ContractFieldTypeValueInteger, fields[0].Type)
	require.True(t, fields[0].Required)
	require.False(t, fields[0].Nullable)
	require.Equal(t, "Stable order identifier", fields[0].Description)
	require.Equal(t, []any{json.Number("9007199254740993"), json.Number("9007199254740994")}, fields[0].Validations.Enum)
	require.False(t, fields[1].Required)
	require.True(t, fields[1].Nullable)
	require.Equal(t, []any{"new", "paid"}, fields[1].Validations.Enum)
	require.Equal(t, 1, *fields[1].Validations.MinLength)
	require.Equal(t, 10, *fields[1].Validations.MaxLength)
	require.Equal(t, json.Number("0"), fields[2].Validations.Min)
	require.Equal(t, json.Number("100"), fields[2].Validations.Max)
	require.Equal(t, model.ContractFieldTypeValueBoolean, fields[3].Type)
	require.Equal(t, model.ContractFieldTypeValueObject, fields[4].Type)
	require.Equal(t, model.ContractFieldTypeValueArray, fields[5].Type)
	require.Equal(t, "email", fields[6].Validations.Format)
	require.Equal(t, "uuid", fields[7].Validations.Format)
	require.NoError(t, payload.ValidateContractFieldDeclarations(result.Shape))
	require.NotNil(t, payload.ValidateContractFieldValues(result.Shape, []map[string]any{{"id": json.Number("9007199254740995")}}))

	other, err := Convert(raw, "summary")
	require.NoError(t, err)
	require.Empty(t, other.Diagnostics)
	require.Len(t, other.Shape.Fields, 1)
	require.Equal(t, "count", other.Shape.Fields[0].Name)

	var document map[string]any
	require.NoError(t, jsonvalue.Unmarshal(raw, &document))
	reencoded, err := json.Marshal(document)
	require.NoError(t, err)
	reordered, err := Convert(reencoded, "order")
	require.NoError(t, err)
	require.Equal(t, result, reordered)
}

func TestConvertReportsUnsupportedFixtureWithoutPartialShape(t *testing.T) {
	raw := fixture(t, "unsupported")
	result, err := Convert(raw, "order")
	require.NoError(t, err)
	require.True(t, result.DocumentValid, "%+v", result.Diagnostics)
	require.Nil(t, result.Shape)
	for _, path := range []string{
		"$.schema[0].properties[0].logicalType", "$.schema[0].properties[0].map",
		"$.schema[0].properties[1].properties", "$.schema[0].properties[2].items",
		"$.schema[0].properties[4].logicalType", "$.schema[0].properties[5].logicalType",
		"$.schema[0].properties[6].logicalType", "$.schema[0].properties[7].logicalType",
		"$.schema[0].properties[8].logicalType", "$.schema[0].properties[10].logicalTypeOptions.pattern",
		"$.schema[0].properties[10].logicalTypeOptions.format", "$.schema[0].properties[11].logicalTypeOptions.exclusiveMinimum",
		"$.schema[0].properties[11].logicalTypeOptions.multipleOf", "$.schema[0].properties[11].logicalTypeOptions.format",
	} {
		requireDiagnostic(t, result, path, "")
	}
	for range 10 {
		repeated, err := Convert(raw, "order")
		require.NoError(t, err)
		require.Equal(t, result, repeated)
	}
}

func TestConvertInvalidDeclarationsAndSafeDiagnostics(t *testing.T) {
	result, err := Convert(fixture(t, "forbidden"), "order")
	require.NoError(t, err)
	require.False(t, result.DocumentValid)
	require.Nil(t, result.Shape)
	requireDiagnostic(t, result, "$.schema[0].properties[0].credentials", "schema_invalid")
	for _, diagnostic := range result.Diagnostics {
		require.NotContains(t, diagnostic.Path+diagnostic.Message, "private-value")
	}
}

func TestConvertPinnedVersionAndInvalidDeclarations(t *testing.T) {
	base := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","properties":[PROPERTY]}]}`
	cases := []struct {
		name, property, path, code string
	}{
		{"nonboolean required", `{"name":"id","logicalType":"integer","required":"private-value"}`, "$.schema[0].properties[0].required", "schema_invalid"},
		{"missing name", `{"logicalType":"integer"}`, "$.schema[0].properties[0]", "schema_invalid"},
		{"unknown type", `{"name":"id","logicalType":"private-value"}`, "$.schema[0].properties[0].logicalType", "schema_invalid"},
		{"primitive enum", `{"name":"id","logicalType":"integer","enum":[1]}`, "$.schema[0].properties[0].enum[0]", "schema_invalid"},
		{"empty enum", `{"name":"id","logicalType":"string","enum":[]}`, "$.schema[0].properties[0].enum", "schema_invalid"},
		{"incompatible enum", `{"name":"id","logicalType":"integer","enum":[{"value":"private-value"}]}`, "$", "document_invalid"},
		{"null enum", `{"name":"id","logicalType":"string","enum":[{"value":null}]}`, "$", "document_invalid"},
		{"collection enum", `{"name":"id","logicalType":"string","enum":[{"value":[]}]}`, "$.schema[0].properties[0].enum[0].value", "schema_invalid"},
		{"inconsistent lengths", `{"name":"id","logicalType":"string","logicalTypeOptions":{"minLength":2,"maxLength":1}}`, "$.schema[0].properties[0]", "mapping_invalid"},
		{"inconsistent bounds", `{"name":"id","logicalType":"number","logicalTypeOptions":{"minimum":2,"maximum":1}}`, "$.schema[0].properties[0]", "mapping_invalid"},
		{"rounded number enum", `{"name":"id","logicalType":"number","enum":[{"value":9007199254740993}]}`, "$.schema[0].properties[0].enum[0].value", "mapping_unsupported"},
		{"rounded lower bound", `{"name":"id","logicalType":"number","logicalTypeOptions":{"minimum":0.1}}`, "$.schema[0].properties[0].logicalTypeOptions.minimum", "mapping_unsupported"},
		{"rounded upper bound", `{"name":"id","logicalType":"number","logicalTypeOptions":{"maximum":9007199254740993}}`, "$.schema[0].properties[0].logicalTypeOptions.maximum", "mapping_unsupported"},
		{"invalid map", `{"name":"id","logicalType":"map"}`, "$.schema[0].properties[0]", "schema_invalid"},
		{"ambiguous name", `{"name":"id.value","logicalType":"string"}`, "$.schema[0].properties[0].name", "shape_ambiguous"},
		{"unknown declaration", `{"name":"id","logicalType":"string","private-value_credentials":"private-value"}`, "$.schema[0].properties[0].*", "schema_invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := Convert([]byte(strings.Replace(base, "PROPERTY", tc.property, 1)), "order")
			require.NoError(t, err)
			require.Nil(t, result.Shape)
			requireDiagnostic(t, result, tc.path, tc.code)
			for _, diagnostic := range result.Diagnostics {
				require.NotContains(t, diagnostic.Path+diagnostic.Message, "private-value")
			}
		})
	}
	for _, version := range []string{"v3.1.0", "v3.0.2", "v4.0.0"} {
		result, err := Convert([]byte(strings.Replace(string(fixture(t, "supported")), PinnedVersion, version, 1)), "order")
		require.NoError(t, err)
		require.False(t, result.DocumentValid)
		require.Nil(t, result.Shape)
		requireDiagnostic(t, result, "$.apiVersion", "version_unsupported")
	}
}

func TestConvertCatalogBoundaryAndSelection(t *testing.T) {
	for _, raw := range []string{"null", "[]", "{}", string([]byte{0xff}), strings.Repeat(" ", MaxDocumentBytes+1), string(fixture(t, "supported")) + " {}", `{"id":"a","id":"b"}`, strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65)} {
		result, err := Convert([]byte(raw), "order")
		require.NoError(t, err)
		require.False(t, result.DocumentValid)
		require.Nil(t, result.Shape)
		require.NotEmpty(t, result.Diagnostics)
	}
	for _, object := range []string{"", "latest", "Orders", "other"} {
		result, err := Convert(fixture(t, "supported"), object)
		require.NoError(t, err)
		require.True(t, result.DocumentValid)
		require.Nil(t, result.Shape)
		requireDiagnostic(t, result, "$.schema", "object_not_found")
	}
	for _, raw := range []string{
		`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders"}]}`,
		`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","properties":[{"name":"id","logicalType":"string"},{"name":"id","logicalType":"integer"}]}]}`,
	} {
		result, err := Convert([]byte(raw), "order")
		require.NoError(t, err)
		require.True(t, result.DocumentValid)
		require.Nil(t, result.Shape)
		require.NotEmpty(t, result.Diagnostics)
		require.Equal(t, "shape_ambiguous", result.Diagnostics[0].Code)
	}
}

func requireDiagnostic(t *testing.T, result *ConversionResult, path, code string) {
	t.Helper()
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Path == path && (code == "" || diagnostic.Code == code) {
			require.NotEmpty(t, diagnostic.Message)
			return
		}
	}
	t.Fatalf("missing diagnostic %s %s in %+v", code, path, result.Diagnostics)
}

func TestConvertExactNumberConstraints(t *testing.T) {
	for _, literal := range []string{"0.5", "1.25", "1e2", "9007199254740992"} {
		raw := []byte(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","properties":[{"name":"n","logicalType":"number","enum":[{"value":` + literal + `}]}]}]}`)
		result, err := Convert(raw, "order")
		require.NoError(t, err)
		require.Empty(t, result.Diagnostics)
		require.Equal(t, json.Number(literal), result.Shape.Fields[0].Validations.Enum[0])
	}
}

func TestSchemaDiagnosticNumericObjectKeysStayPrivate(t *testing.T) {
	raw := []byte(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","properties":[{"name":"id","logicalType":"string","1234567":"private-value"}]}]}`)
	result, err := Convert(raw, "order")
	require.NoError(t, err)
	require.Nil(t, result.Shape)
	requireDiagnostic(t, result, "$.schema[0].properties[0].*", "schema_invalid")
	for _, diagnostic := range result.Diagnostics {
		require.NotContains(t, diagnostic.Path+diagnostic.Message, "1234567")
		require.NotContains(t, diagnostic.Path+diagnostic.Message, "private-value")
	}
}

func TestConvertWholeNumberJSONNotation(t *testing.T) {
	for _, literal := range []string{"100", "100.0", "1e2"} {
		raw := []byte(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","properties":[{"name":"n","logicalType":"integer","enum":[{"value":` + literal + `}],"logicalTypeOptions":{"minimum":` + literal + `,"maximum":` + literal + `}},{"name":"s","logicalType":"string","logicalTypeOptions":{"minLength":` + literal + `,"maxLength":` + literal + `}}]}]}`)
		result, err := Convert(raw, "order")
		require.NoError(t, err)
		require.Empty(t, result.Diagnostics)
		require.Equal(t, []any{json.Number("100")}, result.Shape.Fields[0].Validations.Enum)
		require.Equal(t, json.Number("100"), result.Shape.Fields[0].Validations.Min)
		require.Equal(t, json.Number("100"), result.Shape.Fields[0].Validations.Max)
		require.Equal(t, 100, *result.Shape.Fields[1].Validations.MinLength)
		require.Equal(t, 100, *result.Shape.Fields[1].Validations.MaxLength)
	}
	for _, literal := range []string{"9223372036854775808", "0.5"} {
		raw := []byte(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","properties":[{"name":"n","logicalType":"integer","enum":[{"value":` + literal + `}]}]}]}`)
		result, err := Convert(raw, "order")
		require.NoError(t, err)
		if literal == "0.5" {
			requireDiagnostic(t, result, "$", "document_invalid")
		} else {
			requireDiagnostic(t, result, "$.schema[0].properties[0]", "mapping_invalid")
		}
		require.Nil(t, result.Shape)
	}
}

func TestConversionRetainsPropertyIdentityAndRejectsAmbiguousIDs(t *testing.T) {
	raw := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","properties":[{"id":"stable-id","name":"id","logicalType":"string"},{"name":"label","logicalType":"string"}]}]}`
	result, err := Convert([]byte(raw), "order")
	require.NoError(t, err)
	require.NotNil(t, result.Shape)
	require.Equal(t, []PropertyIdentity{{ID: "stable-id", Name: "id"}, {Name: "label"}}, result.Properties)
	duplicate, err := Convert([]byte(strings.Replace(raw, `{"name":"label"`, `{"id":"stable-id","name":"label"`, 1)), "order")
	require.NoError(t, err)
	require.Nil(t, duplicate.Shape)
	require.False(t, duplicate.DocumentValid)
	requireDiagnostic(t, duplicate, "$", "document_invalid")
}
