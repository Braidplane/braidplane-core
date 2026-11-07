package odcs

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/stretchr/testify/require"
)

func mustCoverageJSON(t *testing.T, result *ConversionResult) []byte {
	t.Helper()
	raw, err := json.Marshal(result.Coverage)
	require.NoError(t, err)
	return raw
}

func coverageAt(t *testing.T, result *ConversionResult, path string) model.ODCSDeclarationCoverage {
	t.Helper()
	for _, entry := range result.Coverage {
		if entry.Path == path {
			return entry
		}
	}
	t.Fatalf("missing coverage for %s", path)
	return model.ODCSDeclarationCoverage{}
}

func TestRichProjectionRetainsCompleteCoverageWithoutChangingEnforcement(t *testing.T) {
	rich, err := Convert(fixture(t, "rich"), "order")
	require.NoError(t, err)
	supported, err := Convert(fixture(t, "supported"), "order")
	require.NoError(t, err)
	require.Equal(t, supported.Shape, rich.Shape)
	require.Empty(t, rich.Diagnostics)
	require.True(t, rich.CoverageComplete)
	require.Equal(t, CompilerID, rich.CompilerID)
	for _, tc := range []struct{ path, state, applicability string }{
		{"$.tenant", "descriptive", "document"},
		{"$.slaProperties", "unsupported", "document"},
		{"$.context", "unsupported", "document"},
		{"$.schema[0].properties[0].logicalType", "descriptive", "not_selected"},
		{"$.schema[1].properties[0].logicalType", "enforced", "selected_port"},
		{"$.schema[1].properties[0].required", "enforced", "selected_port"},
		{"$.schema[1].properties[1].enum[0].value", "enforced", "selected_port"},
		{"$.schema[1].properties[1].logicalTypeOptions.minLength", "enforced", "selected_port"},
	} {
		entry := coverageAt(t, rich, tc.path)
		require.Equal(t, tc.state, entry.State, tc.path)
		require.Equal(t, tc.applicability, entry.Applicability, tc.path)
	}
	require.Error(t, payload.ValidateContractFieldValues(rich.Shape, []map[string]any{{"id": "wrong"}}))
	require.Error(t, payload.ValidateContractFieldValues(rich.Shape, []map[string]any{{"id": nil}}))
	require.Error(t, payload.ValidateContractFieldValues(rich.Shape, []map[string]any{{}}))
	unsupported, err := Convert(fixture(t, "rich"), "rich-object")
	require.NoError(t, err)
	require.Nil(t, unsupported.Shape)
	for _, entry := range unsupported.Coverage {
		require.NotEqual(t, "enforced", entry.State)
	}
}

func TestCoverageAccountsForEveryNodeInAdmittedConformanceCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/upstream-conformance.json")
	require.NoError(t, err)
	var cases []struct {
		Source   string
		Valid    bool
		Document json.RawMessage
	}
	require.NoError(t, json.Unmarshal(raw, &cases))
	cases = append(cases, struct {
		Source   string
		Valid    bool
		Document json.RawMessage
	}{"rich", true, fixture(t, "rich")})
	for _, tc := range cases {
		if !tc.Valid {
			continue
		}
		t.Run(tc.Source, func(t *testing.T) {
			var document map[string]any
			require.NoError(t, jsonvalue.Unmarshal(tc.Document, &document))
			var count func(any) int
			count = func(value any) int {
				n := 1
				switch value := value.(type) {
				case map[string]any:
					for _, child := range value {
						n += count(child)
					}
				case []any:
					for _, child := range value {
						n += count(child)
					}
				}
				return n
			}
			// Include unavailable selections and ID-less objects, not just executable examples.
			selections := []string{"absent"}
			for _, value := range document["schema"].([]any) {
				if id, ok := value.(map[string]any)["id"].(string); ok {
					selections = append(selections, id)
				}
			}
			for _, selection := range selections {
				result, err := Convert(tc.Document, selection)
				require.NoError(t, err)
				require.True(t, result.DocumentValid)
				require.True(t, result.CoverageComplete)
				require.Len(t, result.Coverage, count(document))
				seen := map[string]bool{}
				for _, entry := range result.Coverage {
					require.False(t, seen[entry.Path], entry.Path)
					seen[entry.Path] = true
					require.Contains(t, []string{"enforced", "descriptive", "unsupported", "unresolved"}, entry.State)
					require.NotEmpty(t, entry.Reason)
					require.NotEmpty(t, entry.Applicability)
					if entry.State == "enforced" {
						require.NotNil(t, result.Shape)
						require.NotEmpty(t, entry.RuntimeScope)
					}
				}
				canonical, err := json.Marshal(document)
				require.NoError(t, err)
				again, err := Convert(canonical, selection)
				require.NoError(t, err)
				require.Equal(t, result, again)
			}
		})
	}
}

func TestCoverageBudgetsFailWithoutPartialProjectionOrDocumentLoss(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"entry count", `[` + strings.Repeat(`"${X}",`, MaxCoverageEntries) + `"${X}"]`},
		{"encoded capacity", strings.Repeat(`{"authoritativeDefinitions":`, 45) + `[` + strings.Repeat(`"${X}",`, 10000) + `"${X}"]` + strings.Repeat(`}`, 45)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var document map[string]any
			require.NoError(t, jsonvalue.Unmarshal(fixture(t, "supported"), &document))
			var value any
			require.NoError(t, jsonvalue.Unmarshal([]byte(tc.value), &value))
			document["customProperties"] = []any{map[string]any{"property": "vendor", "value": value}}
			raw, err := json.Marshal(document)
			require.NoError(t, err)
			require.Less(t, len(raw), MaxDocumentBytes)
			revision, err := Record(raw, "orders")
			require.NoError(t, err)
			result, err := Convert(revision.Document, "order")
			require.NoError(t, err)
			require.True(t, result.DocumentValid)
			require.False(t, result.CoverageComplete)
			require.Nil(t, result.Coverage)
			require.Nil(t, result.Shape)
			requireDiagnostic(t, result, "$", "coverage_limit_exceeded")
			again, err := Record(revision.Document, "orders")
			require.NoError(t, err)
			require.Equal(t, revision, again)
		})
	}
}

func TestCoverageRetainsDiagnosticReasonsAndIndependentMetadata(t *testing.T) {
	absent, err := Convert(fixture(t, "supported"), "absent")
	require.NoError(t, err)
	for _, path := range []string{"$.schema", "$.schema[0]", "$.schema[0].properties[0].logicalType"} {
		entry := coverageAt(t, absent, path)
		require.Equal(t, "unresolved", entry.State)
		require.Equal(t, "object_not_found", entry.Reason)
		if path != "$.schema" {
			require.Equal(t, "not_selected", entry.Applicability)
		}
	}
	unsupported, err := Convert(fixture(t, "unsupported"), "order")
	require.NoError(t, err)
	require.Nil(t, unsupported.Shape)
	for _, key := range []string{"label", "description"} {
		entry := coverageAt(t, unsupported, "$.schema[0].properties[9].enum[0]."+key)
		require.Equal(t, "descriptive", entry.State)
		require.Equal(t, "retained_metadata", entry.Reason)
	}
}

func TestEmptyLogicalOptionsDoNotClaimAnEnforcedConstraint(t *testing.T) {
	raw := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","schema":[{"id":"order","name":"Orders","properties":[{"name":"id","logicalType":"string","logicalTypeOptions":{}}]}]}`
	result, err := Convert([]byte(raw), "order")
	require.NoError(t, err)
	require.NotNil(t, result.Shape)
	require.Nil(t, result.Shape.Fields[0].Validations)
	entry := coverageAt(t, result, "$.schema[0].properties[0].logicalTypeOptions")
	require.Equal(t, "descriptive", entry.State)
	require.Empty(t, entry.RuntimeScope)
}

func TestProjectionVariablesAndEnumAnnotations(t *testing.T) {
	base := `{"apiVersion":"v3.2.0","kind":"DataContract","id":"orders","version":"1","description":{"purpose":"${PURPOSE}"},"schema":[{"id":"order","name":"Orders","properties":[{"name":"id","logicalType":"string","enum":[{"value":"ok","label":"${LABEL}","description":"private-value"}]}]}],"customProperties":[{"property":"vendor","value":{"private-key-a":"private-value","private-key-b":"${OPAQUE}"}}]}`
	result, err := Convert([]byte(base), "order")
	require.NoError(t, err)
	require.NotNil(t, result.Shape)
	require.Equal(t, "descriptive", coverageAt(t, result, "$.schema[0].properties[0].enum[0].description").State)
	require.Equal(t, "unresolved", coverageAt(t, result, "$.schema[0].properties[0].enum[0].label").State)
	require.NotContains(t, string(mustCoverageJSON(t, result)), "private-")
	for _, replacement := range []struct{ old, new string }{
		{`"logicalType":"string"`, `"logicalType":"${TYPE}"`},
		{`"name":"id"`, `"name":"${FIELD}"`},
		{`"value":"ok"`, `"value":"${VALUE}"`},
	} {
		raw := strings.Replace(base, replacement.old, replacement.new, 1)
		converted, err := Convert([]byte(raw), "order")
		require.NoError(t, err)
		require.Nil(t, converted.Shape, fmt.Sprint(replacement))
		require.Contains(t, string(mustCoverageJSON(t, converted)), `"state":"unresolved"`)
	}
}
