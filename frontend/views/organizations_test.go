package views

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestOrganizationDetailTabsStartWithOverviewAndGitIntegrations(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	require.NoError(t, OrganizationDetailTabs("acme", "overview").Render(context.Background(), &out))
	require.Contains(t, out.String(), `/organizations/acme/connections`)
	require.NotContains(t, out.String(), `/organizations/acme/proof`)
	require.Less(t, strings.Index(out.String(), ">Overview</a>"), strings.Index(out.String(), ">Git integrations</a>"))
}

func TestGitIntegrationsKeepsGitHubLinkWhenGitLabIsUnavailable(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	permissions := NewOrganizationPermissions("organizations:read")
	require.NoError(t, ProviderConnectionsContent("acme", nil, nil, "", false, "GitLab connection could not be loaded. Reload to try again.", "", permissions).Render(t.Context(), &out))
	require.Contains(t, out.String(), "Manage GitHub connection")
	require.Contains(t, out.String(), "GitLab connection could not be loaded")
	require.NotContains(t, out.String(), "No GitLab account is connected")
	require.Contains(t, out.String(), "Proof assessments")
}

func TestProjectOperationalHelpers(t *testing.T) {
	t.Parallel()

	projectWithBackend := api.Project{
		Name: "Data pipelines",
		Slug: "data-pipelines",
		PipelineBackend: &api.ProjectPipelineBackend{
			DataSourceSlug: "pipeline-git",
			Repo:           "https://github.com/acme/pipelines.git",
			TrackedBranch:  "main",
		},
	}
	projectWithSourceOnlyBackend := api.Project{
		Name: "Ad hoc",
		Slug: "ad-hoc",
		PipelineBackend: &api.ProjectPipelineBackend{
			DataSourceSlug: "pipeline-git",
		},
	}

	tests := []struct {
		name   string
		assert func(t *testing.T)
	}{
		{
			name: "BackendListLabelPrefersRepo",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "https://github.com/acme/pipelines.git", projectPipelineBackendListLabel(projectWithBackend))
			},
		},
		{
			name: "BackendListLabelFallsBackToSource",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "backend pipeline-git", projectPipelineBackendListLabel(projectWithSourceOnlyBackend))
			},
		},
		{
			name: "BackendListLabelMissing",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "no Git backend", projectPipelineBackendListLabel(api.Project{}))
			},
		},
		{
			name: "FullPageBackendAction",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "/organizations/acme/projects/data-pipelines/settings/pipeline-backend?return_to=full", ProjectPipelineBackendFormAction(" acme ", " data-pipelines ", true))
			},
		},
		{
			name: "PortfolioBackendAction",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "/organizations/acme/projects/data-pipelines/settings/pipeline-backend", ProjectPipelineBackendFormAction(" acme ", " data-pipelines ", false))
			},
		},
		{
			name: "FullPageSourceChangeHref",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "/organizations/acme/projects/data-pipelines", ProjectPipelineBackendDataSourceChangeHref(" acme ", " data-pipelines ", true))
			},
		},
		{
			name: "PortfolioSourceChangeHref",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "/organizations/acme/projects?project_slug=data-pipelines", ProjectPipelineBackendDataSourceChangeHref(" acme ", " data-pipelines ", false))
			},
		},
		{
			name: "FullPageTarget",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "#organization-project-full-page", ProjectDetailHTMXTarget(true))
			},
		},
		{
			name: "PortfolioTarget",
			assert: func(t *testing.T) {
				t.Helper()
				require.Equal(t, "#organization-projects-workspace", ProjectDetailHTMXTarget(false))
			},
		},
		{
			name: "GitHubExternalLink",
			assert: func(t *testing.T) {
				t.Helper()
				link := ProjectPipelineBackendExternalURL(projectWithBackend, ProjectPipelineBackendControls{
					DataSourceOptions: []ProjectPipelineBackendDataSourceOption{
						{Slug: "pipeline-git", Type: "github", RepoURL: "git@github.com:acme/pipelines.git"},
					},
				})
				require.NotNil(t, link)
				require.Equal(t, "Open GitHub", link.Label)
				require.Equal(t, "https://github.com/acme/pipelines", link.Href)
			},
		},
		{
			name: "GitLabExternalLink",
			assert: func(t *testing.T) {
				t.Helper()
				link := ProjectPipelineBackendExternalURL(api.Project{
					PipelineBackend: &api.ProjectPipelineBackend{
						DataSourceSlug: "pipeline-gitlab",
						Repo:           "ssh://git@gitlab.example.com/acme/pipelines.git",
					},
				}, ProjectPipelineBackendControls{
					DataSourceOptions: []ProjectPipelineBackendDataSourceOption{
						{Slug: "pipeline-gitlab", Type: "gitlab"},
					},
				})
				require.NotNil(t, link)
				require.Equal(t, "Open GitLab", link.Label)
				require.Equal(t, "https://gitlab.example.com/acme/pipelines", link.Href)
			},
		},
		{
			name: "GenericGitExternalLinkHidden",
			assert: func(t *testing.T) {
				t.Helper()
				link := ProjectPipelineBackendExternalURL(api.Project{
					PipelineBackend: &api.ProjectPipelineBackend{
						DataSourceSlug: "pipeline-git",
						Repo:           "ssh://git.example.com/acme/pipelines.git",
					},
				}, ProjectPipelineBackendControls{
					DataSourceOptions: []ProjectPipelineBackendDataSourceOption{
						{Slug: "pipeline-git", Type: "git", RepoURL: "ssh://git.example.com/acme/pipelines.git"},
					},
				})
				require.Nil(t, link)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.assert(t)
		})
	}
}

func TestOrganizationRetentionRunHelpersSanitizeOutcomes(t *testing.T) {
	t.Parallel()

	failureCode := api.OrganizationRetentionRunFailureCodeMaintenanceFailed
	run := api.OrganizationRetentionRun{
		Status:      api.OrganizationRetentionRunStatusFailed,
		FailureCode: &failureCode,
		Outcome: api.OrganizationRetentionOutcome{
			QueryExecutionsDeleted:   1,
			ReplayCheckpointsDeleted: 2,
			PipelineRunsDeleted:      3,
			EventLogsDeleted:         4,
		},
	}

	require.Equal(t, int64(10), retentionRunDeletedTotal(run))
	require.Equal(t, "Failed", retentionRunStatusLabel(run.Status))
	require.Equal(t, "badge badge-error badge-outline", retentionRunStatusBadgeClass(run.Status))
	require.Equal(
		t,
		"Retention maintenance did not complete. Braidplane will try again automatically.",
		retentionRunFailureMessage(run),
	)
	require.NotContains(t, retentionRunFailureMessage(run), string(failureCode))

	invalidPolicy := api.OrganizationRetentionRunFailureCodeInvalidPolicy
	run.FailureCode = &invalidPolicy
	require.Contains(t, retentionRunFailureMessage(run), "saved retention policy")
	require.NotContains(t, retentionRunFailureMessage(run), string(invalidPolicy))
}

func TestProjectPipelineBackendGitURLHelpers(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"https://github.com/acme/pipelines",
		projectPipelineBackendWebURL("git@github.com:acme/pipelines.git"),
	)
	require.Equal(
		t,
		"https://github.com/acme/pipelines",
		projectPipelineBackendWebURL("git@github.com:acme/pipelines.git?token=redacted#readme"),
	)
	require.Equal(
		t,
		"https://github.com/acme/pipelines",
		projectPipelineBackendWebURL("https://github.com/acme/pipelines.git?token=redacted#readme"),
	)
	require.Equal(
		t,
		"https://github.com/acme/pipelines",
		projectPipelineBackendWebURL("https://alice:secret@github.com/acme/pipelines.git"),
	)
	require.Equal(
		t,
		"https://gitlab.example.com/acme/pipelines",
		projectPipelineBackendWebURL("ssh://git@gitlab.example.com/acme/pipelines.git"),
	)

	host, path, ok := parseSCPStyleGitURL("deploy@mirror@github.com:acme/pipelines.git")
	require.True(t, ok)
	require.Equal(t, "github.com", host)
	require.Equal(t, "acme/pipelines.git", path)

	host, path, ok = parseSCPStyleGitURL("git@example.com:team:repo.git")
	require.True(t, ok)
	require.Equal(t, "example.com", host)
	require.Equal(t, "team:repo.git", path)

	_, _, ok = parseSCPStyleGitURL("git@example.com:/")
	require.False(t, ok)
}

func TestProjectOperationalBindingCounts(t *testing.T) {
	t.Parallel()

	refs := []api.DataSourceRef{
		{Slug: "orders-db", Name: "Orders DB", DataSourceType: testAPIDataSourceTypePtr(api.DataSourceTypePostgres)},
		{Slug: "crm-api", Name: "CRM API", DataSourceType: testAPIDataSourceTypePtr(api.DataSourceTypeHubspot)},
	}
	environments := []api.ProjectEnvironment{
		{EnvironmentSlug: "dev", IsDefault: true},
		{EnvironmentSlug: "prod", EnvironmentProtected: true},
	}
	bindings := map[string][]api.DataSourceBinding{
		"dev": {
			{EnvironmentSlug: "dev", DataSourceRefSlug: "orders-db", DataSourceSlug: "warehouse"},
			{EnvironmentSlug: "dev", DataSourceRefSlug: "crm-api", DataSourceSlug: "crm"},
		},
		"prod": {
			{EnvironmentSlug: "prod", DataSourceRefSlug: "orders-db", DataSourceSlug: "warehouse-prod"},
		},
	}

	require.Equal(t, "dev", ProjectDefaultEnvironmentLabel(environments))
	require.Equal(t, 3, ProjectBoundBindingCount(refs, environments, bindings))
	require.Equal(t, 4, ProjectBindingCellTotalCount(refs, environments))
	require.Equal(t, 1, MissingBindingCount(refs, environments, bindings))
	require.Equal(t, 1, ProtectedProjectEnvironmentCount(environments))
}

func TestEnvironmentBindingMatrixRowsScopesBindingsByPipeline(t *testing.T) {
	t.Parallel()

	refs := []api.DataSourceRef{
		{PipelineSlug: "orders-sync", Slug: "warehouse", Name: "Orders Warehouse", DataSourceType: testAPIDataSourceTypePtr(api.DataSourceTypePostgres)},
		{PipelineSlug: "customers-sync", Slug: "warehouse", Name: "Customers Warehouse", DataSourceType: testAPIDataSourceTypePtr(api.DataSourceTypePostgres)},
	}
	environments := []api.ProjectEnvironment{{EnvironmentSlug: "prod"}}
	bindings := map[string][]api.DataSourceBinding{
		"prod": {
			{PipelineSlug: "customers-sync", EnvironmentSlug: "prod", DataSourceRefSlug: "warehouse", DataSourceSlug: "customer-warehouse-prod"},
		},
	}

	rows := EnvironmentBindingMatrixRows(refs, environments, bindings)

	require.Len(t, rows, 2)
	require.Equal(t, "orders-sync", rows[0].PipelineSlug)
	require.False(t, rows[0].Cells[0].IsBound)
	require.Equal(t, "customers-sync", rows[1].PipelineSlug)
	require.True(t, rows[1].Cells[0].IsBound)
	require.Equal(t, "customer-warehouse-prod", rows[1].Cells[0].DataSourceSlug)
}

func TestAvailableCatalogEnvironmentsByProtection(t *testing.T) {
	t.Parallel()

	catalog := []api.Environment{
		{Slug: "dev", Protected: false},
		{Slug: "stg", Protected: false},
		{Slug: "prod", Protected: true},
	}
	environments := []api.ProjectEnvironment{
		{EnvironmentSlug: "dev"},
	}

	unprotected := AvailableCatalogEnvironmentsByProtection(catalog, environments, false)
	protected := AvailableCatalogEnvironmentsByProtection(catalog, environments, true)

	require.Len(t, unprotected, 1)
	require.Equal(t, api.Slug("stg"), unprotected[0].Slug)
	require.Len(t, protected, 1)
	require.Equal(t, api.Slug("prod"), protected[0].Slug)
}
