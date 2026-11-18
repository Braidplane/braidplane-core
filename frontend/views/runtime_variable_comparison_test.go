package views

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestRuntimeVariableComparisonRendersSourcesAndBlockers(t *testing.T) {
	t.Parallel()
	comparison := api.RuntimeVariableComparison{
		Environments: []string{"dev", "stg", "prod"},
		Rows: []api.RuntimeVariableComparisonRow{
			{Key: "batch_size", Cells: []api.RuntimeVariableComparisonCell{
				{Source: api.RuntimeVariableSourceOverride, Variable: &api.RuntimeVariable{Value: "100"}},
				{Source: api.RuntimeVariableSourceDefault, Variable: &api.RuntimeVariable{Value: "200"}},
				{Source: api.RuntimeVariableSourceOverride, Variable: &api.RuntimeVariable{Value: "500"}},
			}},
			{Key: "region", Used: true, Definition: &api.RuntimeVariableDefinition{ValueType: api.RuntimeVariableValueTypeText}, Cells: []api.RuntimeVariableComparisonCell{
				{Source: api.RuntimeVariableSourceMissing, Required: true, Blocked: true},
				{Source: api.RuntimeVariableSourceOverride, Required: true, TypeMismatch: true, Blocked: true, Variable: &api.RuntimeVariable{ValueType: api.RuntimeVariableValueTypeInteger, Value: "1"}},
				{Source: api.RuntimeVariableSourceMissing, Required: true, Blocked: true},
			}},
			{Key: "optional", Definition: &api.RuntimeVariableDefinition{ValueType: api.RuntimeVariableValueTypeText}, Cells: []api.RuntimeVariableComparisonCell{
				{Source: api.RuntimeVariableSourceMissing},
				{Source: api.RuntimeVariableSourceMissing},
				{Source: api.RuntimeVariableSourceMissing},
			}},
		},
	}
	var html strings.Builder
	require.NoError(t, runtimeVariableComparisonContent(RuntimeVariableComparisonView{
		Organization: "acme", Comparison: &comparison, Pipeline: "orders",
	}).Render(t.Context(), &html))
	require.Contains(t, html.String(), "Required includes keys used")
	require.Contains(t, html.String(), "Missing required value")
	require.Contains(t, html.String(), "Type mismatch")
	require.Contains(t, html.String(), "Default")
	require.Contains(t, html.String(), "Override")
	require.Contains(t, html.String(), ">500<")
	require.Contains(t, html.String(), ">1<")
	require.Equal(t, 2, strings.Count(html.String(), "Missing required value"))
	require.Equal(t, 3, strings.Count(html.String(), ">Missing<"))
	require.Equal(t, 3, strings.Count(html.String(), "badge-error"))
}
