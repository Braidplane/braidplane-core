package views

import (
	"bytes"
	"context"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestPipelineBindingAttentionUsesSelectedEnvironment(t *testing.T) {
	t.Parallel()
	rows := []DataSourceBindingRow{
		{PipelineSlug: "one", RefSlug: "shared", Cells: []DataSourceBindingCell{{EnvironmentSlug: "dev", State: "missing"}, {EnvironmentSlug: "prod", State: "bound"}}},
		{PipelineSlug: "two", RefSlug: "shared", Cells: []DataSourceBindingCell{{EnvironmentSlug: "prod", State: "invalid"}}},
		{PipelineSlug: "two", RefSlug: "other", Cells: []DataSourceBindingCell{{EnvironmentSlug: "prod", State: "unavailable"}}},
	}
	attention := pipelineBindingAttentionRows(rows, "prod")
	require.Len(t, attention, 2)
	require.Equal(t, "two", attention[0].PipelineSlug)
	require.Len(t, attention[0].Cells, 1)
	require.Len(t, rows[0].Cells, 2)
	require.NotEqual(t, pipelineBindingCellID(rows[0], rows[0].Cells[1]), pipelineBindingCellID(rows[1], rows[1].Cells[0]))
}

func TestDataSourceBindingLabelsKeepConnectionAndLogicalIdentity(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ name, slug, want string }{
		{"", "blank-source", "blank-source"},
		{"Shared", "first", "Shared (first)"},
		{"Shared", "second", "Shared (second)"},
		{strings.Repeat("界", 65), "long-source", "long-source"},
		{"short", "short", "short"},
	} {
		require.Equal(t, tt.want, dataSourceBindingConnectionLabel(tt.name, tt.slug))
		label := dataSourceBindingOptionLabel(DataSourceBindingOption{Name: tt.name, Slug: tt.slug, Type: "postgres", IsActive: true})
		require.Contains(t, label, tt.slug)
	}
	row := DataSourceBindingRow{PipelineSlug: "pipeline", RefSlug: "orders-db", RefName: "Orders", RefAvailable: true}
	cell := DataSourceBindingCell{EnvironmentSlug: "prod", HasBinding: true, Binding: api.DataSourceBinding{DataSourceSlug: "blank-source", DataSourceActive: true}}
	var out bytes.Buffer
	require.NoError(t, DataSourceBindingCellControl("acme", "ops", "pipeline", "prod", row, cell, false, OrganizationPermissions{}, false, false).Render(context.Background(), &out))
	require.Contains(t, out.String(), "blank-source")
	require.Contains(t, out.String(), `aria-label="Data source for orders-db in prod"`)
}

func TestSelectedVersionOperationsOverrideCurrentRefTypeAssumptions(t *testing.T) {
	t.Parallel()
	// Current V2 declares an HTTP ref; selected V1 still requires SQL with that same logical identity.
	row := DataSourceBindingRow{DataSourceType: "http", RefAvailable: true, OperationTypes: []string{"sql"}, RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource}}
	source := api.DataSource{Type: api.DataSourceTypeHttp, IsActive: true}
	compatible, reason := dataSourceBindingOptionCompatibility(row, source)
	require.False(t, compatible)
	require.Equal(t, "operation mismatch", reason)
	require.Equal(t, []string{"bound data source does not support the pipeline operations"}, dataSourceBindingInvalidReasons(row, api.DataSourceBinding{DataSourceType: api.DataSourceTypeHttp, DataSourceActive: true}))
}

func TestPipelineBindingAttentionDistinguishesMissingContextFromNoRequirements(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		environment string
		pipeline    *api.Pipeline
		want        string
	}{
		{"prod", nil, "Pipeline requirements are unavailable."},
		{"", &api.Pipeline{}, "Select a runtime target to see its binding attention."},
		{"prod", &api.Pipeline{}, "No data-source requirements in this pipeline."},
	} {
		var out bytes.Buffer
		require.NoError(t, PipelineBindingAttentionSummary(tt.environment, tt.pipeline, PipelineBindingContext{Environments: []api.ProjectEnvironment{{EnvironmentSlug: "prod"}}}).Render(context.Background(), &out))
		require.Contains(t, out.String(), tt.want)
		require.NotContains(t, out.String(), "not enabled for the project")
		if tt.pipeline == nil {
			require.NotContains(t, out.String(), "No data-source requirements")
		}
	}
}
