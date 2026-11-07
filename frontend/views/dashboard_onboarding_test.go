package views

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDashboardOnboardingChecklistCanBeDismissed(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	require.NoError(t, DashboardOnboardingChecklist(DashboardData{}, "acme").Render(context.Background(), &out))

	require.Contains(t, out.String(), `data-dashboard-onboarding`)
	require.Contains(t, out.String(), `data-dashboard-onboarding-scope="acme"`)
	require.Contains(t, out.String(), `data-dashboard-onboarding-dismiss`)
	require.Contains(t, out.String(), "Ask a workspace admin for access to the remaining setup steps.")
}

func TestDashboardOnboardingStepsUseStateAndPermissions(t *testing.T) {
	t.Parallel()

	data := DashboardData{
		Permissions: NewOrganizationPermissions("*:*"),
		Onboarding: DashboardOnboarding{
			HasProject:       true,
			ProjectsHref:     "/organizations/acme/projects",
			ProjectSetupHref: "/organizations/acme/projects?project_slug=demo",
			DataSourcesHref:  "/orgs/acme/data-sources",
			QueriesHref:      "/orgs/acme/data-sources/warehouse/queries/read-users",
			PipelinesHref:    "/orgs/acme/projects/first/pipelines?environment_slug=dev",
		},
	}
	steps := DashboardOnboardingSteps(data)
	require.False(t, steps[1].Done)
	require.True(t, steps[0].Current)
	require.Equal(t, "/orgs/acme/data-sources", steps[0].Href)
	require.False(t, DashboardOnboardingComplete(data))

	data.Permissions = DefaultOrganizationPermissions()
	steps = DashboardOnboardingSteps(data)
	require.Empty(t, steps[0].Href)
	require.Equal(t, "Ask a workspace admin", steps[0].Action)
	require.Empty(t, steps[2].Href)
	require.Empty(t, steps[3].Href)
	require.Equal(t, "https://docs.example.test/getting-started/first-result/", DashboardFirstResultTutorialHref(AuthenticatedShellData{DocumentationURL: " https://docs.example.test/ "}))

	data.Permissions = NewOrganizationPermissions("*:*")
	data.Onboarding.HasSuccessfulConnection = true
	data.ProjectScope.HasPipelineBackend = true
	steps = DashboardOnboardingSteps(data)
	require.True(t, steps[1].Current)
	data.Onboarding.HasSuccessfulResult = true
	steps = DashboardOnboardingSteps(data)
	require.True(t, steps[3].Current)
	require.Equal(t, "Open pipelines", steps[3].Action)
	require.Equal(t, data.Onboarding.PipelinesHref, steps[3].Href)
	var out bytes.Buffer
	require.NoError(t, DashboardOnboardingChecklist(data, "acme").Render(context.Background(), &out))
	require.Contains(t, out.String(), `href="/orgs/acme/projects/first/pipelines?environment_slug=dev"`)
	require.Contains(t, out.String(), "Sync a workflow from Git")
	require.Contains(t, out.String(), ">View query</a>")
	require.NotContains(t, out.String(), "/builder")

	data.Onboarding.HasSuccessfulPipelineRun = true
	require.True(t, DashboardOnboardingComplete(data))
}

func TestDashboardOnboardingStartsWithConnectionWithoutProject(t *testing.T) {
	t.Parallel()
	data := DashboardData{
		Permissions: NewOrganizationPermissions("datasources:create"),
		Onboarding:  DashboardOnboarding{DataSourcesHref: "/orgs/acme/data-sources"},
	}
	steps := DashboardOnboardingSteps(data)
	require.Equal(t, "Connect a system", steps[0].Label)
	require.True(t, steps[0].Current)
	require.Equal(t, data.Onboarding.DataSourcesHref, steps[0].Href)
	require.Empty(t, steps[1].Href)
}
