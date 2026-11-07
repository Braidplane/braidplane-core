package views

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestEnvironmentRiskSummaryIncludesProtectedConventionalTargets(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"Protected production target. Treat mutations as production-impacting runtime changes.",
		EnvironmentRiskSummary("prod", true),
	)
	require.Equal(
		t,
		"Protected staging target for release checks, drift review, and binding validation.",
		EnvironmentRiskSummary("stg", true),
	)
	require.Equal(
		t,
		"Protected development target for local iteration and sandbox data.",
		EnvironmentRiskSummary("dev", true),
	)
	require.Equal(t, "Production target. Treat mutations as production-impacting runtime changes.", EnvironmentRiskSummary("prod", false))
}

func TestProjectEnvironmentsPageContentRendersProtectedCatalogSignals(t *testing.T) {
	t.Parallel()

	catalog := []api.Environment{
		{
			Slug:      "prod",
			Name:      "Production",
			Color:     api.EnvironmentColorBlue,
			Protected: true,
		},
	}
	environments := []api.ProjectEnvironment{
		{
			EnvironmentSlug:      "prod",
			EnvironmentName:      "Production",
			EnvironmentColor:     api.EnvironmentColorBlue,
			EnvironmentProtected: true,
			IsDefault:            true,
		},
	}

	var out bytes.Buffer
	require.NoError(t, ProjectEnvironmentsPageContent(
		"acme",
		"ops",
		"prod",
		"prod",
		environments,
		catalog,
		nil,
		nil,
		false,
		false,
		NewOrganizationPermissions(),
	).Render(context.Background(), &out))

	html := out.String()
	require.Equal(t, 1, strings.Count(html, `>protected</span>`))
	require.Contains(t, html, "Protected; changes require confirmation")
	require.Contains(t, html, "Protected production target. Treat mutations as production-impacting runtime changes.")
}

func TestEnvironmentPreviewsKeepConfigurationOnFullPages(t *testing.T) {
	t.Parallel()
	catalog := []api.Environment{{Slug: "prod", Name: "Production", Protected: true}}
	enabled := []api.ProjectEnvironment{{EnvironmentSlug: "prod", IsDefault: true}}
	refs := []api.DataSourceRef{{Slug: "source"}}
	var out bytes.Buffer
	require.NoError(t, OrganizationEnvironmentDetailPanel("acme", catalog, "prod", []api.RuntimeVariable{{Value: "private-runtime-value"}}, false, nil, false, DefaultEnvironmentCreateFormData(), NewOrganizationPermissions()).Render(context.Background(), &out))
	html := out.String()
	require.Contains(t, html, "Organization runtime target")
	require.NotContains(t, html, "<form")
	require.NotContains(t, html, "private-runtime-value")
	require.Contains(t, html, "list_return=")

	for _, unavailable := range []bool{false, true} {
		out.Reset()
		require.NoError(t, ProjectEnvironmentQuickLook("acme", "ops", "dev", "prod", enabled, catalog, refs, nil, unavailable).Render(context.Background(), &out))
		html = out.String()
		require.NotContains(t, html, "<form")
		require.NotContains(t, html, "<table")
		require.Contains(t, html, ">Default</span>")
		require.Contains(t, html, "section=configuration")
		if unavailable {
			require.Contains(t, html, "Readiness unavailable")
			require.NotContains(t, html, "1 missing bindings")
		} else {
			require.Contains(t, html, "1 missing bindings")
		}
	}
}

func TestEnvironmentBindingWarningsIncludeInactiveSources(t *testing.T) {
	t.Parallel()
	refs := []api.DataSourceRef{{Slug: "source"}}
	environment := api.ProjectEnvironment{EnvironmentSlug: "dev"}
	bindings := map[string][]api.DataSourceBinding{"dev": {{DataSourceRefSlug: "source", DataSourceActive: false}}}
	require.Equal(t, 1, inactiveEnvironmentBindingCount(refs, environment, bindings))
	require.Zero(t, MissingBindingCount(refs, []api.ProjectEnvironment{environment}, bindings))
}
