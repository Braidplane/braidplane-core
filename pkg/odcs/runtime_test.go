package odcs_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/stretchr/testify/require"
)

func TestConvertedFlatFixtureAtQueryOutput(t *testing.T) {
	document, err := os.ReadFile("testdata/supported.json")
	require.NoError(t, err)
	converted, err := odcs.Convert(document, "order")
	require.NoError(t, err)
	require.Empty(t, converted.Diagnostics)
	require.NotNil(t, converted.Shape)
	converted.Shape.Representation = model.ContractRepresentationRows

	for _, tc := range []struct {
		name, field string
		value       any
		rule        string
	}{
		{name: "valid batch"},
		{name: "optional null", field: "email"},
		{name: "optional type without rules", field: "metadata", value: "opaque"},
		{name: "exact integer enum", field: "id", value: json.Number("9007199254740995"), rule: "enum"},
		{name: "string enum", field: "state", value: "unknown", rule: "enum"},
		{name: "lower bound", field: "total", value: json.Number("-1"), rule: "min"},
		{name: "upper bound", field: "total", value: json.Number("101"), rule: "max"},
		{name: "email format", field: "email", value: "wrong", rule: "format"},
		{name: "UUID format", field: "external_id", value: "wrong", rule: "format"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := map[string]any{
				"id": json.Number("9007199254740993"), "enabled": true, "state": "new",
				"total": json.Number("100"), "email": "orders@example.test",
				"external_id": "f8655bc4-f3c6-4551-adeb-c9f2fb80d45d",
				"metadata":    map[string]any{}, "labels": []any{},
			}
			if tc.field != "" {
				row[tc.field] = tc.value
			}
			output := result.NewSQLResult("postgres", &result.SQLResult{
				Rows: []map[string]any{row}, RowCount: 1,
				ColumnNames: []string{"id", "enabled", "state", "total", "email", "external_id", "metadata", "labels"},
			}, 0, nil)
			err := pipelinerun.ValidateQueryOutputContractAtRuntime(output, converted.Shape)
			if tc.rule == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, pipelinerun.ErrQueryOutputContractSchemaMismatch)
			var failure *payload.ContractFieldValidationError
			require.ErrorAs(t, err, &failure)
			require.Equal(t, tc.field, failure.FieldName)
			require.Equal(t, tc.rule, failure.Rule.String())
		})
	}
}
