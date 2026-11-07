package views

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestPipelineBindingRowsDerivesRequirementsFromSpec(t *testing.T) {
	t.Parallel()

	var queryStep api.PipelineStep
	require.NoError(t, queryStep.FromPipelineQueryStep(api.PipelineQueryStep{
		Slug: "load-orders",
		Query: api.PipelineQueryRef{
			DataSourceRef: "orders-db",
			OperationType: api.PipelineQueryRefOperationTypeSql,
		},
	}))
	var sinkStep api.PipelineStep
	require.NoError(t, sinkStep.FromPipelineSinkStep(api.PipelineSinkStep{
		Slug: "write-orders",
		Sink: api.PipelineSinkSpec{
			Input:         "load-orders",
			DataSourceRef: "orders-db",
			OperationType: api.PipelineSinkSpecOperationTypeSql,
		},
	}))
	pipeline := &api.Pipeline{
		Spec: api.PipelineSpec{
			Steps: []api.PipelineStep{queryStep, sinkStep},
		},
	}

	rows := PipelineBindingRows(pipeline, PipelineBindingContext{
		DataSourceRefs: []api.DataSourceRef{
			{
				Slug:                 "orders-db",
				Name:                 "Orders DB",
				DataSourceType:       testAPIDataSourceTypePtr(api.DataSourceTypePostgres),
				RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource},
			},
		},
		Environments: []api.ProjectEnvironment{{EnvironmentSlug: "prod"}},
	})

	require.Len(t, rows, 1)
	require.Equal(t, "orders-db", rows[0].RefSlug)
	require.ElementsMatch(t, []api.DataSourceRefCapability{
		api.DataSourceRefCapabilityPipelineSource,
		api.DataSourceRefCapabilityPipelineSink,
	}, rows[0].RequiredCapabilities)
	require.True(t, rows[0].CapabilityMismatch)
	require.Equal(t, bindingCellStateUnavailable, rows[0].Cells[0].State)
	require.Equal(t, "contract mismatch", rows[0].Cells[0].StateLabel)
}

func TestPipelineBindingRowsAllowsUntypedDataSourceRef(t *testing.T) {
	t.Parallel()

	pipeline := &api.Pipeline{
		Spec: api.PipelineSpec{
			Steps: []api.PipelineStep{
				testPipelineQueryStep(t, "load-orders", "orders-db"),
			},
		},
	}

	rows := PipelineBindingRows(pipeline, PipelineBindingContext{
		DataSourceRefs: []api.DataSourceRef{
			{
				Slug:                 "orders-db",
				Name:                 "Orders DB",
				DataSourceType:       nil,
				RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource},
			},
		},
		Environments: []api.ProjectEnvironment{{EnvironmentSlug: "prod"}},
	})

	require.Len(t, rows, 1)
	require.Equal(t, "orders-db", rows[0].RefSlug)
	require.Empty(t, rows[0].DataSourceType)
	require.True(t, rows[0].RefAvailable)
	require.False(t, rows[0].CapabilityMismatch)
	require.Len(t, rows[0].Cells, 1)
	require.Equal(t, bindingCellStateMissing, rows[0].Cells[0].State)
	require.Empty(t, rows[0].Cells[0].Options)

	var out bytes.Buffer
	require.NoError(t, PipelineDataSourceBindingsPanel(
		"acme",
		"default",
		"prod",
		pipeline,
		PipelineBindingContext{
			DataSourceRefs: []api.DataSourceRef{
				{
					Slug:                 "orders-db",
					Name:                 "Orders DB",
					DataSourceType:       nil,
					RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource},
				},
			},
			Environments: []api.ProjectEnvironment{{EnvironmentSlug: "prod"}},
		},
		OrganizationPermissions{},
		false,
	).Render(context.Background(), &out))

	body := out.String()
	require.Contains(t, body, "Orders DB")
	require.Contains(t, body, "missing")
	require.NotContains(t, body, "contract mismatch")
	require.NotContains(t, body, "ref unavailable")
}

func TestPipelineDataSourceBindingsPanelRendersNoRequirements(t *testing.T) {
	t.Parallel()

	transformName := api.Name("Normalize")
	var transformStep api.PipelineStep
	require.NoError(t, transformStep.FromPipelineTransformStep(api.PipelineTransformStep{
		Kind: api.PipelineTransformStepKind(api.PipelineStepKindTransform),
		Name: &transformName,
		Slug: "normalize",
		Transform: api.PipelineTransformSpec{
			Inputs:     map[string]api.Slug{"input": "upstream"},
			OutputKind: api.PipelineTransformSpecOutputKindRows,
			Sql:        "SELECT * FROM input",
		},
	}))
	pipeline := &api.Pipeline{Spec: api.PipelineSpec{Steps: []api.PipelineStep{transformStep}}}

	var out bytes.Buffer
	require.NoError(t, PipelineDataSourceBindingsPanel(
		"acme",
		"default",
		"dev",
		pipeline,
		PipelineBindingContext{},
		OrganizationPermissions{},
		false,
	).Render(context.Background(), &out))

	body := out.String()
	require.Contains(t, body, "Pipeline data-source requirements")
	require.Contains(t, body, "No data-source requirements in this pipeline.")
	require.NotContains(t, body, "Pipeline-scoped requirements are not available on this overview yet.")
	require.NotContains(t, body, "<form", "no-requirements state should not render binding forms")
}

func TestProjectBindingRowsUsesPipelineRequirementsOnly(t *testing.T) {
	t.Parallel()

	rows := ProjectBindingRows(ProjectOperationalContext{
		DataSourceRefs: []api.DataSourceRef{
			{
				Slug:                 "orders-db",
				Name:                 "Orders DB",
				DataSourceType:       testAPIDataSourceTypePtr(api.DataSourceTypePostgres),
				RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource},
			},
			{
				Slug:                 "warehouse-out",
				Name:                 "Warehouse Output",
				DataSourceType:       testAPIDataSourceTypePtr(api.DataSourceTypePostgres),
				RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSink},
			},
			{
				Slug:                 "unused-db",
				Name:                 "Unused DB",
				DataSourceType:       testAPIDataSourceTypePtr(api.DataSourceTypePostgres),
				RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource},
			},
		},
		PipelineDetails: []api.Pipeline{
			testPipelineWithBindingSteps(t, []string{"orders-db"}, []string{"warehouse-out"}),
		},
		Environments: []api.ProjectEnvironment{{EnvironmentSlug: "prod"}},
		Bindings:     map[string][]api.DataSourceBinding{},
	})

	require.Len(t, rows, 2)
	require.Equal(t, "orders-db", rows[0].RefSlug)
	require.Equal(t, "warehouse-out", rows[1].RefSlug)
	require.NotContains(t, []string{rows[0].RefSlug, rows[1].RefSlug}, "unused-db")
	require.ElementsMatch(t, []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource}, rows[0].RequiredCapabilities)
	require.ElementsMatch(t, []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSink}, rows[1].RequiredCapabilities)
}

func TestProjectBindingRowsMarksReadOnlySinkInvalid(t *testing.T) {
	t.Parallel()

	rows := ProjectBindingRows(ProjectOperationalContext{
		DataSourceRefs: []api.DataSourceRef{
			{
				Slug:                 "warehouse-out",
				Name:                 "Warehouse Output",
				DataSourceType:       testAPIDataSourceTypePtr(api.DataSourceTypePostgres),
				RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSink},
			},
		},
		PipelineDetails: []api.Pipeline{
			testPipelineWithBindingSteps(t, nil, []string{"warehouse-out"}),
		},
		Environments: []api.ProjectEnvironment{{EnvironmentSlug: "prod"}},
		Bindings: map[string][]api.DataSourceBinding{
			"prod": {
				{
					EnvironmentSlug:    "prod",
					DataSourceRefSlug:  "warehouse-out",
					DataSourceSlug:     "readonly-warehouse",
					DataSourceName:     "Readonly Warehouse",
					DataSourceType:     api.DataSourceTypePostgres,
					DataSourceActive:   true,
					DataSourceReadOnly: true,
					DataSourceRefName:  "Warehouse Output",
				},
			},
		},
		DataSources: []api.DataSource{
			{
				Slug:     "readonly-warehouse",
				Name:     "Readonly Warehouse",
				Type:     api.DataSourceTypePostgres,
				IsActive: true,
				ReadOnly: true,
			},
			{
				Slug:     "write-warehouse",
				Name:     "Writable Warehouse",
				Type:     api.DataSourceTypePostgres,
				IsActive: true,
			},
		},
		BindingUnavailableEnvironments: map[string]bool{},
	})

	require.Len(t, rows, 1)
	require.Len(t, rows[0].Cells, 1)
	cell := rows[0].Cells[0]
	require.Equal(t, bindingCellStateInvalid, cell.State)
	require.Contains(t, cell.InvalidReasons, "read-only data source cannot satisfy a pipeline sink")
	require.Len(t, cell.Options, 1)
	require.Equal(t, "write-warehouse", cell.Options[0].Slug)
	require.True(t, cell.Options[0].Compatible)
}

func TestProjectBindingRowsMarksEnvironmentUnavailable(t *testing.T) {
	t.Parallel()

	rows := ProjectBindingRows(ProjectOperationalContext{
		DataSourceRefs: []api.DataSourceRef{
			{
				Slug:                 "orders-db",
				Name:                 "Orders DB",
				DataSourceType:       testAPIDataSourceTypePtr(api.DataSourceTypePostgres),
				RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource},
			},
		},
		PipelineDetails: []api.Pipeline{
			testPipelineWithBindingSteps(t, []string{"orders-db"}, nil),
		},
		Environments: []api.ProjectEnvironment{{EnvironmentSlug: "prod"}},
		Bindings:     map[string][]api.DataSourceBinding{},
		BindingUnavailableEnvironments: map[string]bool{
			"prod": true,
		},
	})

	require.Len(t, rows, 1)
	require.Equal(t, bindingCellStateUnavailable, rows[0].Cells[0].State)
	require.Equal(t, "Binding state could not be loaded for this environment.", rows[0].Cells[0].StateDescription)
}

func TestProjectBindingRowsKeepsDuplicateRefSlugsPipelineScoped(t *testing.T) {
	t.Parallel()

	ordersPipeline := testPipelineWithBindingSteps(t, []string{"warehouse"}, nil)
	ordersPipeline.Slug = "orders-sync"
	customersPipeline := testPipelineWithBindingSteps(t, []string{"warehouse"}, nil)
	customersPipeline.Slug = "customers-sync"

	rows := ProjectBindingRows(ProjectOperationalContext{
		DataSourceRefs: []api.DataSourceRef{
			{
				PipelineSlug:         "orders-sync",
				Slug:                 "warehouse",
				Name:                 "Orders Warehouse",
				DataSourceType:       testAPIDataSourceTypePtr(api.DataSourceTypePostgres),
				RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource},
			},
			{
				PipelineSlug:         "customers-sync",
				Slug:                 "warehouse",
				Name:                 "Customers Warehouse",
				DataSourceType:       testAPIDataSourceTypePtr(api.DataSourceTypePostgres),
				RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource},
			},
		},
		PipelineDetails: []api.Pipeline{ordersPipeline, customersPipeline},
		Environments:    []api.ProjectEnvironment{{EnvironmentSlug: "prod"}},
		Bindings: map[string][]api.DataSourceBinding{
			"prod": {
				{
					PipelineSlug:       "customers-sync",
					EnvironmentSlug:    "prod",
					DataSourceRefSlug:  "warehouse",
					DataSourceRefName:  "Customers Warehouse",
					DataSourceSlug:     "customers-db",
					DataSourceName:     "Customers DB",
					DataSourceType:     api.DataSourceTypePostgres,
					DataSourceActive:   true,
					DataSourceReadOnly: false,
				},
			},
		},
		DataSources: []api.DataSource{
			{Slug: "customers-db", Name: "Customers DB", Type: api.DataSourceTypePostgres, IsActive: true},
		},
	})

	require.Len(t, rows, 2)
	require.Equal(t, "orders-sync", rows[0].PipelineSlug)
	require.Equal(t, "warehouse", rows[0].RefSlug)
	require.Equal(t, bindingCellStateMissing, rows[0].Cells[0].State)
	require.False(t, rows[0].Cells[0].HasBinding)

	require.Equal(t, "customers-sync", rows[1].PipelineSlug)
	require.Equal(t, "warehouse", rows[1].RefSlug)
	require.Equal(t, bindingCellStateBound, rows[1].Cells[0].State)
	require.True(t, rows[1].Cells[0].HasBinding)
	require.Equal(t, "customers-db", rows[1].Cells[0].Binding.DataSourceSlug)
}

func TestDataSourceBindingCellMatchesDuplicateRefByPipelineSlug(t *testing.T) {
	t.Parallel()

	environment := api.ProjectEnvironment{
		EnvironmentSlug: "prod",
		EnvironmentName: "Production",
	}
	bindings := []api.DataSourceBinding{
		{
			PipelineSlug:       "customers-sync",
			EnvironmentSlug:    "prod",
			DataSourceRefSlug:  "warehouse",
			DataSourceRefName:  "Warehouse",
			DataSourceSlug:     "customers-db",
			DataSourceName:     "Customers DB",
			DataSourceType:     api.DataSourceTypePostgres,
			DataSourceActive:   true,
			DataSourceReadOnly: false,
		},
	}
	dataSources := []api.DataSource{
		{Slug: "orders-db", Name: "Orders DB", Type: api.DataSourceTypePostgres, IsActive: true},
		{Slug: "customers-db", Name: "Customers DB", Type: api.DataSourceTypePostgres, IsActive: true},
	}

	for _, tt := range []struct {
		name               string
		rowPipelineSlug    string
		wantState          string
		wantHasBinding     bool
		wantDataSourceSlug string
	}{
		{
			name:            "OtherPipelineWithSameRefSlugStaysMissing",
			rowPipelineSlug: "orders-sync",
			wantState:       bindingCellStateMissing,
		},
		{
			name:               "OwningPipelineWithSameRefSlugIsBound",
			rowPipelineSlug:    "customers-sync",
			wantState:          bindingCellStateBound,
			wantHasBinding:     true,
			wantDataSourceSlug: "customers-db",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cell := dataSourceBindingCell(
				DataSourceBindingRow{
					PipelineSlug:         tt.rowPipelineSlug,
					RefSlug:              "warehouse",
					RefName:              "Warehouse",
					DataSourceType:       "postgres",
					RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource},
					RefAvailable:         true,
				},
				environment,
				bindings,
				false,
				dataSources,
			)

			require.Equal(t, tt.wantState, cell.State)
			require.Equal(t, tt.wantHasBinding, cell.HasBinding)
			require.Equal(t, tt.wantDataSourceSlug, cell.Binding.DataSourceSlug)
		})
	}
}

func TestDataSourceBindingHelpersPreserveActionContext(t *testing.T) {
	t.Parallel()

	rows := []DataSourceBindingRow{
		{Cells: []DataSourceBindingCell{{State: bindingCellStateBound}, {State: bindingCellStateMissing}}},
		{Cells: []DataSourceBindingCell{{State: bindingCellStateInvalid}}},
	}
	require.Equal(t, 2, ProjectBindingAttentionCount(rows))

	options := []DataSourceBindingOption{
		{Slug: "readonly", Compatible: false},
		{Slug: "warehouse", Compatible: true},
	}
	require.True(t, DataSourceBindingCanSubmit(options, false, true))
	require.False(t, DataSourceBindingCanSubmit(options, true, true))
	require.False(t, DataSourceBindingCanSubmit(options, false, false))
	require.False(t, DataSourceBindingCanSubmit([]DataSourceBindingOption{{Compatible: false}}, false, true))

	require.Equal(t, "Warehouse Output", DataSourceBindingRefDisplayName(DataSourceBindingRow{
		RefSlug: "warehouse-out",
		RefName: "Warehouse Output",
		Cells:   []DataSourceBindingCell{{HasBinding: true, Binding: api.DataSourceBinding{DataSourceName: "Writable Warehouse"}}},
	}))
	require.Empty(t, DataSourceBindingRefDisplayName(DataSourceBindingRow{
		RefSlug: "warehouse-out",
		RefName: "Writable Warehouse",
		Cells:   []DataSourceBindingCell{{HasBinding: true, Binding: api.DataSourceBinding{DataSourceName: "Writable Warehouse"}}},
	}))

	require.Equal(t, "binding-upsert-pipeline-ref-prod", DataSourceBindingFormID("binding", "upsert", "pipeline/ref", "prod"))
	require.Equal(t, "data-source-binding", DataSourceBindingFormID(" / "))
	require.Equal(t, "/orgs/acme/projects/default/environments", ProjectEnvironmentsHref("acme", "default"))
	require.Equal(t, badgeGhostSoft, DataSourceBindingStateBadgeClass("unknown"))

	projectUpsert := ProjectDataSourceBindingUpsertAction("acme", "default", "prod", "warehouse", true)
	require.Equal(t, "/organizations/acme/projects/default/environments/prod/data-source-bindings/warehouse/upsert?return_to=full", projectUpsert)
	require.Equal(
		t,
		"/organizations/acme/projects/default/environments/prod/data-source-bindings/warehouse/delete",
		ProjectDataSourceBindingDeleteAction("acme", "default", "prod", "warehouse", false),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/default/pipelines/users-sync/environments/prod/data-source-bindings/warehouse/upsert?environment_slug=dev&return_to=list",
		PipelineDataSourceBindingUpsertAction("acme", "default", "users-sync", "prod", "warehouse", "dev", true),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/default/pipelines/users-sync/environments/prod/data-source-bindings/warehouse/delete?environment_slug=dev",
		PipelineDataSourceBindingDeleteAction("acme", "default", "users-sync", "prod", "warehouse", "dev", false),
	)

	require.Equal(t, projectUpsert, DataSourceBindingUpsertAction("acme", "default", "", "prod", "warehouse", "", true, false))
	require.Equal(
		t,
		"/orgs/acme/projects/default/pipelines/users-sync/environments/prod/data-source-bindings/warehouse/delete?environment_slug=dev&return_to=list",
		DataSourceBindingDeleteAction("acme", "default", "users-sync", "prod", "warehouse", "dev", false, true),
	)
	require.Equal(t, "#organization-project-full-page", DataSourceBindingHTMXTarget("", true, false))
	require.Equal(t, "#organization-projects-workspace", DataSourceBindingHTMXSelect("", false, false))
	require.Equal(t, "#pipeline-catalog-workspace", DataSourceBindingHTMXTarget("users-sync", false, true))
	require.Equal(t, "body", DataSourceBindingHTMXTarget("users-sync", false, false))
}

func TestDataSourceBindingRefSummaryRendersDataSourceIconReference(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	require.NoError(t, DataSourceBindingRefSummary(DataSourceBindingRow{
		RefSlug:        "orders-db",
		RefName:        "Orders DB",
		DataSourceType: "postgres",
		RefAvailable:   true,
	}).Render(context.Background(), &out))

	body := out.String()
	requireDataSourceIconMarker(t, body, "postgres")
	require.Contains(t, body, `data-data-source-identity`)
	require.Contains(t, body, "Orders DB")
	require.Contains(t, body, "orders-db")
}

func TestEnvironmentBindingCellRendersBoundDataSourceIconReference(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	require.NoError(t, EnvironmentBindingCell(EnvironmentBindingMatrixCell{
		EnvironmentSlug: "prod",
		DataSourceSlug:  "warehouse-prod",
		DataSourceName:  "Warehouse Prod",
		DataSourceType:  "postgres",
		IsBound:         true,
		IsActive:        true,
	}).Render(context.Background(), &out))

	body := out.String()
	requireDataSourceIconMarker(t, body, "postgres")
	require.Contains(t, body, `data-data-source-identity`)
	require.Contains(t, body, "Warehouse Prod")
	require.Contains(t, body, "warehouse-prod")
}

func TestDataSourceBindingCellControlRendersBoundDataSourceIdentity(t *testing.T) {
	t.Parallel()

	body := renderDataSourceBindingCellControlForPermissions(t, NewOrganizationPermissions("data_sources:update", "data_sources:delete"))

	requireDataSourceIconMarker(t, body, "postgres")
	require.Contains(t, body, `data-data-source-identity`)
	require.Contains(t, body, "Warehouse Prod")
	require.Contains(t, body, "warehouse-prod")
}

func TestDataSourceBindingCellControlPermissionStates(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name               string
		permissions        OrganizationPermissions
		wantSelectDisabled bool
		wantUpdateDisabled bool
		wantRemoveDisabled bool
		wantReadOnlyCopy   bool
		wantRemovalCopy    bool
	}{
		{
			name:               "NoPermissionsDisablesUpdateAndRemove",
			permissions:        DefaultOrganizationPermissions(),
			wantSelectDisabled: true,
			wantUpdateDisabled: true,
			wantRemoveDisabled: true,
			wantReadOnlyCopy:   true,
			wantRemovalCopy:    true,
		},
		{
			name:               "UpdateOnlyEnablesUpdateAndDisablesRemove",
			permissions:        NewOrganizationPermissions("data_sources:update"),
			wantSelectDisabled: false,
			wantUpdateDisabled: false,
			wantRemoveDisabled: true,
			wantReadOnlyCopy:   false,
			wantRemovalCopy:    true,
		},
		{
			name:               "UpdateDeleteEnablesUpdateAndRemove",
			permissions:        NewOrganizationPermissions("data_sources:update", "data_sources:delete"),
			wantSelectDisabled: false,
			wantUpdateDisabled: false,
			wantRemoveDisabled: false,
			wantReadOnlyCopy:   false,
			wantRemovalCopy:    false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := renderDataSourceBindingCellControlForPermissions(t, tt.permissions)
			selectTag := htmlStartTagWithAttribute(t, body, "select", `name="data_source_slug"`)
			updateButton := htmlStartTagBeforeText(t, body, "button", "Update</button>")
			removeButton := htmlStartTagBeforeText(t, body, "button", "Remove</button>")

			require.Contains(t, body, ">Update</button>")
			require.Contains(t, body, ">Remove</button>")
			require.Equal(t, tt.wantSelectDisabled, htmlStartTagHasDisabledAttribute(selectTag))
			require.Equal(t, tt.wantUpdateDisabled, htmlStartTagHasDisabledAttribute(updateButton))
			require.Equal(t, tt.wantRemoveDisabled, htmlStartTagHasDisabledAttribute(removeButton))
			require.Equal(t, tt.wantReadOnlyCopy, strings.Contains(body, "Read-only for your role."))
			require.Equal(t, tt.wantRemovalCopy, strings.Contains(body, "Removal is disabled for your role."))
		})
	}
}

// renderDataSourceBindingCellControlForPermissions renders the same bound cell with varied permissions.
func renderDataSourceBindingCellControlForPermissions(t *testing.T, permissions OrganizationPermissions) string {
	t.Helper()

	row := DataSourceBindingRow{
		RefSlug:        "orders-db",
		RefName:        "Orders DB",
		DataSourceType: "postgres",
		RefAvailable:   true,
	}
	cell := DataSourceBindingCell{
		EnvironmentSlug: "prod",
		State:           bindingCellStateBound,
		StateLabel:      "bound",
		StateClass:      DataSourceBindingStateBadgeClass(bindingCellStateBound),
		HasBinding:      true,
		Binding:         api.DataSourceBinding{DataSourceName: "Warehouse Prod", DataSourceSlug: "warehouse-prod", DataSourceActive: true},
		Options:         []DataSourceBindingOption{{Slug: "warehouse-prod", Name: "Warehouse Prod", Type: "postgres", Label: "Warehouse Prod - postgres", Compatible: true, Selected: true}},
	}

	var out bytes.Buffer
	require.NoError(t, DataSourceBindingCellControl(
		"acme-hq",
		"default",
		"",
		"",
		row,
		cell,
		false,
		permissions,
		true,
		false,
	).Render(context.Background(), &out))

	return out.String()
}

// htmlStartTagWithAttribute finds the first opening tag that carries a stable identifying attribute.
func htmlStartTagWithAttribute(t *testing.T, body, tagName, attribute string) string {
	t.Helper()

	marker := "<" + tagName
	offset := 0
	for {
		index := strings.Index(body[offset:], marker)
		if index < 0 {
			require.Failf(t, "missing HTML tag", "expected <%s> with %q", tagName, attribute)
		}
		offset += index

		end := strings.Index(body[offset:], ">")
		require.NotEqual(t, -1, end)
		startTag := body[offset : offset+end+1]
		if strings.Contains(startTag, attribute) {
			return startTag
		}
		offset += len(marker)
	}
}

// htmlStartTagBeforeText finds the opening tag immediately associated with visible button text.
func htmlStartTagBeforeText(t *testing.T, body, tagName, text string) string {
	t.Helper()

	textIndex := strings.Index(body, text)
	require.NotEqual(t, -1, textIndex)
	start := strings.LastIndex(body[:textIndex], "<"+tagName)
	require.NotEqual(t, -1, start)
	end := strings.Index(body[start:], ">")
	require.NotEqual(t, -1, end)

	return body[start : start+end+1]
}

// htmlStartTagHasDisabledAttribute checks boolean or valued disabled attributes on one opening tag.
func htmlStartTagHasDisabledAttribute(startTag string) bool {
	return strings.Contains(startTag, " disabled") || strings.Contains(startTag, " disabled=")
}

func testPipelineWithBindingSteps(t *testing.T, queryRefs, sinkRefs []string) api.Pipeline {
	t.Helper()

	steps := make([]api.PipelineStep, 0, len(queryRefs)+len(sinkRefs))
	for _, ref := range queryRefs {
		steps = append(steps, testPipelineQueryStep(t, "query-"+ref, ref))
	}
	for _, ref := range sinkRefs {
		var step api.PipelineStep
		require.NoError(t, step.FromPipelineSinkStep(api.PipelineSinkStep{
			Slug: "sink-" + ref,
			Sink: api.PipelineSinkSpec{
				Input:         "upstream",
				DataSourceRef: ref,
				OperationType: api.PipelineSinkSpecOperationTypeSql,
			},
		}))
		steps = append(steps, step)
	}

	return api.Pipeline{
		Spec: api.PipelineSpec{Steps: steps},
	}
}

func testPipelineQueryStep(t *testing.T, stepSlug, refSlug string) api.PipelineStep {
	t.Helper()

	var step api.PipelineStep
	require.NoError(t, step.FromPipelineQueryStep(api.PipelineQueryStep{
		Slug: stepSlug,
		Query: api.PipelineQueryRef{
			DataSourceRef: refSlug,
			OperationType: api.PipelineQueryRefOperationTypeSql,
		},
	}))
	return step
}

func testAPIDataSourceTypePtr(value api.DataSourceType) *api.DataSourceType {
	return &value
}

func TestUntypedSQLBindingOptionsMatchConnectorOperations(t *testing.T) {
	t.Parallel()
	row := DataSourceBindingRow{RefAvailable: true, OperationTypes: []string{"sql"}, RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource}}
	for _, tc := range []struct {
		name   string
		source api.DataSource
		want   bool
	}{
		{"postgres", api.DataSource{Type: api.DataSourceTypePostgres, IsActive: true, ReadOnly: true}, true},
		{"mysql", api.DataSource{Type: api.DataSourceTypeMysql, IsActive: true}, true},
		{"s3", api.DataSource{Type: api.DataSourceTypeS3, IsActive: true}, false},
		{"bigquery", api.DataSource{Type: api.DataSourceTypeBigquery, IsActive: true}, false},
		{"http", api.DataSource{Type: api.DataSourceTypeHttp, IsActive: true}, false},
		{"unknown", api.DataSource{Type: api.DataSourceType("unknown"), IsActive: true}, false},
		{"inactive", api.DataSource{Type: api.DataSourceTypePostgres}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := dataSourceBindingOptionCompatibility(row, tc.source)
			require.Equal(t, tc.want, got)
		})
	}
	row.RequiredCapabilities = []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSink}
	got, _ := dataSourceBindingOptionCompatibility(row, api.DataSource{Type: api.DataSourceTypePostgres, IsActive: true, ReadOnly: true})
	require.False(t, got)
	row.RefAvailable = false
	got, _ = dataSourceBindingOptionCompatibility(row, api.DataSource{Type: api.DataSourceTypePostgres, IsActive: true})
	require.False(t, got)
}

func TestUntypedExistingSQLBindingRejectsIncompatibleConnector(t *testing.T) {
	t.Parallel()
	row := DataSourceBindingRow{RefAvailable: true, OperationTypes: []string{"sql"}, RequiredCapabilities: []api.DataSourceRefCapability{api.DataSourceRefCapabilityPipelineSource}}
	require.Empty(t, dataSourceBindingInvalidReasons(row, api.DataSourceBinding{DataSourceType: api.DataSourceTypePostgres, DataSourceActive: true, DataSourceReadOnly: true}))
	require.Equal(t, []string{"bound data source does not support the pipeline operations"}, dataSourceBindingInvalidReasons(row, api.DataSourceBinding{DataSourceType: api.DataSourceTypeS3, DataSourceActive: true}))
}
