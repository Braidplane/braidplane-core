package views

import (
	"bytes"
	"context"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/a-h/templ"
	"github.com/stretchr/testify/require"
)

func TestEntityQuickLooksKeepLongLabelsAndStatesReadOnly(t *testing.T) {
	name := strings.Repeat("LongEntityName", 20)
	problem := strings.Repeat("Connection unavailable. ", 20)
	pipeline := &api.Pipeline{Name: name, Slug: "daily", SyncStatus: api.Synced}
	source := &api.DataSource{Name: name, Slug: "warehouse", Type: api.DataSourceTypePostgres, TestError: &problem}
	permissions := NewOrganizationPermissions("datasources:update", "datasources:delete", "pipelines:update")
	for _, tt := range []struct {
		name      string
		component templ.Component
		want      string
	}{
		{"pipeline populated", PipelineDetailPanelSlot("acme", "ops", "dev", PipelineSelectedDetail{Slug: "daily", Pipeline: pipeline, ValidationUnavailable: true, LatestRunUnavailable: true, DeploymentsUnavailable: true}, permissions), name},
		{"pipeline unavailable", PipelineDetailPanelSlot("acme", "ops", "dev", PipelineSelectedDetail{ErrorSummary: "Pipeline unavailable", ErrorDetails: problem}, permissions), problem},
		{"pipeline empty", PipelineDetailPanelSlot("acme", "ops", "dev", PipelineSelectedDetail{}, permissions), "Select a pipeline"},
		{"source populated", DataSourceDetailPanelSlot("acme", "dev", source, DefaultDataSourceSchemaPanelData(), nil, DataSourceUpdateFormData{}, permissions, "", ""), name},
		{"source restricted", DataSourceDetailPanelSlot("acme", "dev", nil, DefaultDataSourceSchemaPanelData(), nil, DataSourceUpdateFormData{}, OrganizationPermissions{}, "Permission required", "error"), "Permission required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, tt.component.Render(context.Background(), &out))
			html := out.String()
			require.Contains(t, html, tt.want)
			for _, forbidden := range []string{"<form", "<input", "<textarea", "<table", "truncate"} {
				require.NotContains(t, html, forbidden)
			}
			if strings.Contains(tt.name, "populated") {
				require.Contains(t, html, "Open full page")
				require.LessOrEqual(t, strings.Count(html, "<dt"), 6)
			}
		})
	}
}

func TestEntitySectionAttentionBadges(t *testing.T) {
	problem := "connection refused"
	for _, failed := range []bool{false, true} {
		source := &api.DataSource{Name: "Warehouse", Slug: "warehouse", Type: api.DataSourceTypePostgres}
		if failed {
			source.TestError = &problem
		}
		var out bytes.Buffer
		require.NoError(t, DataSourceDetailPanelContent("acme", "dev", source, DefaultDataSourceSchemaPanelData(), nil, DataSourceUpdateFormData{}, OrganizationPermissions{}, DataSourceOperationsPanelData{}).Render(context.Background(), &out))
		require.Equal(t, failed, strings.Contains(out.String(), `title="Connection test failed"`))
		if failed {
			require.Contains(t, out.String(), `<span aria-hidden="true">!</span>`)
			require.Contains(t, out.String(), `<span class="sr-only">Connection test failed</span>`)
		}
	}

	var step api.PipelineStep
	require.NoError(t, step.FromPipelineQueryStep(api.PipelineQueryStep{Slug: "load", Query: api.PipelineQueryRef{DataSourceRef: "orders", OperationType: api.PipelineQueryRefOperationTypeSql}}))
	pipeline := &api.Pipeline{Name: "Orders", Slug: "orders", Spec: api.PipelineSpec{Steps: []api.PipelineStep{step}}}
	bindings := PipelineBindingContext{Environments: []api.ProjectEnvironment{{EnvironmentSlug: "dev"}}}
	require.Equal(t, "Missing, invalid or unavailable bindings", PipelineBindingAttentionLabel(pipeline, bindings))
	require.Equal(t, "Runtime environment required for bindings", PipelineBindingAttentionLabel(pipeline, PipelineBindingContext{}))
	require.Equal(t, "Binding data unavailable", PipelineBindingAttentionLabel(pipeline, PipelineBindingContext{BindingsUnavailable: true}))
	require.Empty(t, PipelineBindingAttentionLabel(&api.Pipeline{}, PipelineBindingContext{}))

	var out bytes.Buffer
	require.NoError(t, PipelineDetailPageContent("acme", "ops", "dev", "orders", pipeline, nil, false, PipelineRunPreview{}, nil, false, bindings, PipelineUpdateFormData{}, OrganizationPermissions{}).Render(context.Background(), &out))
	require.Contains(t, out.String(), `section=deployments`)
	require.Contains(t, out.String(), `title="Missing, invalid or unavailable bindings"`)
	require.NotContains(t, out.String(), `id="pipeline-bindings"`)
	bindings.DataSourceRefs = []api.DataSourceRef{{Slug: "orders", RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource}}}
	bindings.Bindings = map[string][]api.DataSourceBinding{"dev": {{DataSourceRefSlug: "orders", DataSourceSlug: "warehouse", DataSourceType: api.DataSourceTypePostgres, DataSourceActive: true}}}
	require.Empty(t, PipelineBindingAttentionLabel(pipeline, bindings))
}

func TestDataSourceSectionsSeparateSettingsAndRespectPermissions(t *testing.T) {
	source := &api.DataSource{Name: "Warehouse", Slug: "warehouse", Type: api.DataSourceTypePostgres}
	for _, tt := range []struct {
		section      string
		canUpdate    bool
		wantSettings bool
	}{{"overview", true, false}, {"settings", true, true}, {"settings", false, false}, {"schema", false, false}, {"operations", false, false}, {"unknown", true, false}} {
		var permissions OrganizationPermissions
		if tt.canUpdate {
			permissions = NewOrganizationPermissions("datasources:update")
		}
		ctx := WithEntityPageURL(context.Background(), "/orgs/acme/data-sources/warehouse?section="+tt.section)
		var out bytes.Buffer
		require.NoError(t, DataSourceDetailPanelContent("acme", "dev", source, DefaultDataSourceSchemaPanelData(), nil, DataSourceUpdateFormData{Type: "postgres"}, permissions, DataSourceOperationsPanelData{}).Render(ctx, &out))
		require.Equal(t, tt.wantSettings, strings.Contains(out.String(), "Update connection settings"))
		require.Equal(t, tt.section == "schema", strings.Contains(out.String(), "Schema browser"))
		require.NotContains(t, out.String(), "#data-source-")
	}
}
