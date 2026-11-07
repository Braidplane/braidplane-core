package views

import (
	"bytes"
	"context"
	"net/url"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestProjectReadOnlyPreviewAndReadiness(t *testing.T) {
	project := api.Project{Name: strings.Repeat("long project name ", 12), Slug: "ops"}
	for _, tc := range []struct {
		name    string
		context ProjectOperationalContext
		want    string
	}{
		{"no target", ProjectOperationalContext{}, "No runtime environments enabled"},
		{"unavailable", ProjectOperationalContext{EnvironmentsUnavailable: true}, "enablement is unavailable"},
		{"default missing", ProjectOperationalContext{Environments: []api.ProjectEnvironment{{EnvironmentSlug: "dev"}}}, "Default runtime environment missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, OrganizationProjectDetailPanelSlot(api.Organization{Slug: "acme"}, []api.Project{project}, "ops", ProjectPipelineBackendControls{}, tc.context, OrganizationPermissions{}).Render(context.Background(), &out))
			html := out.String()
			require.Contains(t, html, tc.want)
			require.Contains(t, html, "Git backend not attached")
			require.Contains(t, html, "project update permission")
			require.Contains(t, html, "Open full page")
			for _, forbidden := range []string{"<form", "<input", "<table", "truncate"} {
				require.NotContains(t, html, forbidden)
			}
			require.LessOrEqual(t, strings.Count(html, "<dt"), 6)
		})
	}
	require.Equal(t, "Logical binding readiness is unavailable.", projectEnvironmentProblem(ProjectOperationalContext{Environments: []api.ProjectEnvironment{{EnvironmentSlug: "dev", IsDefault: true}}, BindingsUnavailable: true}))
	require.Equal(t, "Repository validation unavailable", projectRepositoryLabel(api.Project{PipelineBackend: &api.ProjectPipelineBackend{}}))
	sha := "abc123"
	require.Equal(t, "Repository validated at an exact commit", projectRepositoryLabel(api.Project{PipelineBackend: &api.ProjectPipelineBackend{FirstCommitSha: &sha}}))
}

func TestProjectRepositoryChoiceAndRestrictedControls(t *testing.T) {
	org := api.Organization{Slug: "acme"}
	project := api.Project{Slug: "ops", Name: "Operations"}
	for _, tc := range []struct{ query, present, absent string }{
		{"section=repository", "Use existing repository", "name=\"mode\""},
		{"section=repository&repository_path=existing", "Save backend", "Create and connect"},
		{"section=repository&repository_path=create", "Create and connect", "Save backend"},
		{"section=settings", "Delete project", "Save backend"},
	} {
		t.Run(tc.query, func(t *testing.T) {
			ctx := WithEntityPageURL(context.Background(), "/organizations/acme/projects/ops?"+tc.query)
			var out bytes.Buffer
			require.NoError(t, OrganizationProjectFullPageContent(org, project, ProjectPipelineBackendControls{PipelineDir: "pipelines", CreationBranch: "main"}, ProjectOperationalContext{}, NewOrganizationPermissions("projects:update", "organizations:update", "datasources:create", "projects:delete"), "", "", "").Render(ctx, &out))
			require.Contains(t, out.String(), tc.present)
			require.NotContains(t, out.String(), tc.absent)
			out.Reset()
			require.NoError(t, OrganizationProjectFullPageContent(org, project, ProjectPipelineBackendControls{}, ProjectOperationalContext{}, OrganizationPermissions{}, "", "", "").Render(ctx, &out))
			require.Contains(t, out.String(), "View only")
			require.NotContains(t, out.String(), "<form")
		})
	}
}

func TestProjectNavigationPreservesScopedContext(t *testing.T) {
	list := "/organizations/acme/projects?cursor=a%2Bb&environment_slug=prod&project_slug=ops"
	ctx := WithEntityPageURL(context.Background(), list)
	detail := projectDetailHref(ctx, "acme", "ops")
	detailContext := WithEntityPageURL(context.Background(), detail)
	require.Equal(t, list, entityBackHref(detailContext, "/organizations/acme/projects"))
	for _, section := range []string{"overview", "repository", "pipelines", "environments", "settings"} {
		href := projectSectionHref(detailContext, "acme", "ops", section)
		parsed, err := url.Parse(href)
		require.NoError(t, err)
		require.Equal(t, "prod", parsed.Query().Get("environment_slug"))
		require.Equal(t, list, parsed.Query().Get("list_return"))
		require.Equal(t, section, projectSection(WithEntityPageURL(context.Background(), href)))
	}
	require.Equal(t, "overview", projectSection(WithEntityPageURL(context.Background(), "/?section=unknown")))
	require.Empty(t, projectRepositoryPath(WithEntityPageURL(context.Background(), "/?repository_path=unknown")))
}

func TestProjectDataContractsNavigation(t *testing.T) {
	for _, canRead := range []bool{true, false} {
		permissions := OrganizationPermissions{}
		if canRead {
			permissions = NewOrganizationPermissions("projects:read")
		}
		var out bytes.Buffer
		require.NoError(t, OrganizationProjectDetailPanel(api.Organization{Slug: "acme"}, api.Project{Slug: "ops"}, ProjectPipelineBackendControls{}, ProjectOperationalContext{}, permissions).Render(context.Background(), &out))
		require.Equal(t, canRead, strings.Contains(out.String(), `href="/orgs/acme/projects/ops/contracts"`))
	}
}

func TestProjectDeletionReturnIsScoped(t *testing.T) {
	for _, tc := range []struct{ returnTo, want string }{
		{"/organizations/acme/projects?cursor=a%2Bb&environment_slug=prod&project_slug=ops", "/organizations/acme/projects?cursor=a%2Bb&environment_slug=prod"},
		{"https://evil.test/organizations/acme/projects", "/organizations/acme/projects"},
		{"/organizations/other/projects", "/organizations/acme/projects"},
	} {
		ctx := WithEntityPageURL(context.Background(), "/delete?list_return="+url.QueryEscape(tc.returnTo))
		require.Equal(t, tc.want, ProjectDeletedListHref(ctx, "acme"))
	}
}

func TestProjectRepositoryStickyValuesAndRuntimeLinks(t *testing.T) {
	controls := ProjectPipelineBackendControls{PipelineDir: "custom-path", SelectedDataSource: "git-source", CreationBranch: "release", DataSourceOptions: []ProjectPipelineBackendDataSourceOption{{Slug: "git-source", Name: "Git source", Type: projectPipelineBackendSourceGitHub}}}
	org := api.Organization{Slug: "acme"}
	project := api.Project{Slug: "ops"}
	for _, path := range []string{"existing", "create"} {
		var out bytes.Buffer
		ctx := WithEntityPageURL(context.Background(), "/organizations/acme/projects/ops?section=repository&repository_path="+path)
		require.NoError(t, OrganizationProjectDetailPanel(org, project, controls, ProjectOperationalContext{}, NewOrganizationPermissions("projects:update", "organizations:update", "datasources:create")).Render(ctx, &out))
		require.Contains(t, out.String(), `value="custom-path"`)
		if path == "create" {
			require.Contains(t, out.String(), `name="mode" value="managed_create"`)
			require.NotContains(t, out.String(), `name="data_source_slug"`)
		} else {
			require.Contains(t, out.String(), `value="git-source" selected`)
		}
	}
	var out bytes.Buffer
	ctx := WithEntityPageURL(context.Background(), "/organizations/acme/projects/ops?section=pipelines&environment_slug=prod")
	require.NoError(t, OrganizationProjectPipelinesSummary(org, project, ProjectOperationalContext{Pipelines: []api.PipelineRef{{Slug: "daily"}}}).Render(ctx, &out))
	require.Contains(t, out.String(), `/pipelines?environment_slug=prod`)
	require.Contains(t, out.String(), `/pipelines/daily?environment_slug=prod`)
}

func TestManagedProjectRepositoryFormProtectsSavedTargetAndStopsOnUnavailableProgress(t *testing.T) {
	t.Parallel()
	org, project := api.Organization{Slug: "acme"}, api.Project{Slug: "ops"}
	permissions := NewOrganizationPermissions("projects:update", "organizations:update", "datasources:create")
	branch, directory, failure := "release", "definitions/pipelines", "repository_bootstrap_failed"
	setup := &api.ProjectRepositorySetup{Request: api.CreateProjectRepositoryRequest{Provider: api.CreateProjectRepositoryRequestProviderGitlab, Owner: "acme/platform", RepositoryName: "pipelines", TrackedBranch: &branch, PipelineDir: &directory}, FailureCode: &failure}

	var out bytes.Buffer
	ctx := WithEntityPageURL(context.Background(), "/organizations/acme/projects/ops?section=repository&repository_path=create")
	require.NoError(t, OrganizationProjectDetailPanel(org, project, ProjectPipelineBackendControls{RepositorySetup: setup, RepositoryProvider: "gitlab", ProviderOwner: "acme/platform", RepositoryName: "pipelines", CreationBranch: branch, PipelineDir: directory}, ProjectOperationalContext{}, permissions).Render(ctx, &out))
	html := out.String()
	require.Contains(t, html, "Resume repository setup")
	require.Contains(t, html, `value="acme/platform" required maxlength="255" readonly`)
	require.Contains(t, html, `value="pipelines" required maxlength="100" readonly`)
	require.Contains(t, html, "Resume setup")

	out.Reset()
	require.NoError(t, OrganizationProjectDetailPanel(org, project, ProjectPipelineBackendControls{RepositorySetupLoadError: "Setup progress could not be loaded."}, ProjectOperationalContext{}, permissions).Render(ctx, &out))
	require.Contains(t, out.String(), "Setup progress unavailable")
	require.NotContains(t, out.String(), `name="mode" value="managed_create"`)

	out.Reset()
	require.NoError(t, OrganizationProjectDetailPanel(org, api.Project{Slug: "ops", PipelineBackend: &api.ProjectPipelineBackend{}}, ProjectPipelineBackendControls{}, ProjectOperationalContext{}, permissions).Render(ctx, &out))
	require.Contains(t, out.String(), "Repository already connected")
	require.NotContains(t, out.String(), `name="mode" value="managed_create"`)

	out.Reset()
	require.NoError(t, OrganizationProjectDetailPanel(org, project, ProjectPipelineBackendControls{}, ProjectOperationalContext{}, NewOrganizationPermissions("projects:read")).Render(ctx, &out))
	require.Contains(t, out.String(), "View only")
	require.NotContains(t, out.String(), `name="mode" value="managed_create"`)
}

func TestManagedRepositoryRetestGuidancePreservesEnvironmentAndReadPermissions(t *testing.T) {
	t.Parallel()
	failure := "source_test_required"
	setup := &api.ProjectRepositorySetup{DataSourceSlug: "repo-saved", FailureCode: &failure}
	ctx := WithEntityPageURL(context.Background(), "/organizations/acme/projects/ops?section=repository&repository_path=create&environment_slug=dev")
	for _, canRead := range []bool{true, false} {
		permissions := NewOrganizationPermissions("projects:update", "organizations:update", "datasources:create")
		if canRead {
			permissions = NewOrganizationPermissions("projects:update", "organizations:update", "datasources:create", "datasources:read", "datasources:update")
		}
		var out bytes.Buffer
		require.NoError(t, ManagedProjectRepositoryForm(api.Organization{Slug: "acme"}, api.Project{Slug: "ops"}, ProjectPipelineBackendControls{RepositorySetup: setup}, permissions, true).Render(ctx, &out))
		html := out.String()
		require.Contains(t, html, "Test the saved repository connection successfully")
		if canRead {
			require.Contains(t, html, `/orgs/acme/data-sources/repo-saved?environment_slug=dev`)
			require.Contains(t, html, "Review connection")
			require.NotContains(t, html, "Ask an organization admin to test")
		} else {
			require.NotContains(t, html, "Review connection")
			require.Contains(t, html, "Ask an organization admin to test")
		}
		require.NotContains(t, html, `/test-connection`)
	}
}

func TestProjectEnvironmentRestrictedControls(t *testing.T) {
	var out bytes.Buffer
	ctx := WithEntityPageURL(context.Background(), "/organizations/acme/projects/ops?section=environments")
	state := ProjectOperationalContext{Environments: []api.ProjectEnvironment{{EnvironmentSlug: "prod", EnvironmentProtected: true, IsDefault: true}}, Catalog: []api.Environment{{Slug: "dev"}}}
	require.NoError(t, OrganizationProjectFullPageContent(api.Organization{Slug: "acme"}, api.Project{Slug: "ops"}, ProjectPipelineBackendControls{}, state, OrganizationPermissions{}, "", "", "").Render(ctx, &out))
	require.Contains(t, out.String(), "View only")
	require.Contains(t, out.String(), "protected")
	require.NotContains(t, out.String(), "<form")
	require.NotContains(t, out.String(), ">Disable<")
}
