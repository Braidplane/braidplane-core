package views

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShellSelectOptionLabel_TruncatesLongOrganizationNames(t *testing.T) {
	t.Parallel()

	label := ShellSelectOptionLabel("Org", "Fake Org 20260620-200240-261062000")

	require.Len(t, []rune(label), 26)
	require.True(t, strings.HasPrefix(label, "Org Fake Org"))
	require.True(t, strings.HasSuffix(label, "..."))
	require.Equal(t, "Org Short", ShellSelectOptionLabel("Org", "Short"))
}

func TestShellOrganizationSettingsHref_TargetsOrganizationDetail(t *testing.T) {
	t.Parallel()

	require.Equal(t, "/organizations/acme-hq", ShellOrganizationSettingsHref(" acme-hq "))
	require.Equal(t, "/organizations", ShellOrganizationSettingsHref(" "))
}

func TestShellOrganizationSwitchHref_PreservesReturnTo(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"/organizations/acme-hq/switch?return_to=%2Forgs%2Facme-hq%2Fqueries%3Fds%3Dprod-db",
		ShellOrganizationSwitchHref("acme-hq", "/orgs/acme-hq/queries?ds=prod-db"),
	)
	require.Equal(t, "/organizations/acme-hq/switch?return_to=%2Fdashboard", ShellOrganizationSwitchHref("acme-hq", ""))
	require.Equal(t, "/organizations", ShellOrganizationSwitchHref("", "/dashboard"))
}

func TestShellTopbarControlsKeepOrganizationPositionStable(t *testing.T) {
	t.Parallel()

	want := "flex min-w-0 max-w-full flex-wrap items-start gap-3 xl:justify-end"
	require.Equal(t, "min-w-0 w-full max-w-full sm:w-fit xl:justify-self-end", ShellTopbarControlsClass(AuthenticatedShellData{CurrentPath: "/dashboard"}))
	require.Equal(t, want, ShellTopbarControlsClass(AuthenticatedShellData{CurrentPath: "/orgs/acme/data-sources"}))
}

func TestShellDetailTitleKeepsTheActiveTaskNavigation(t *testing.T) {
	t.Parallel()
	items := []ShellNavItem{{Label: "Queries", IsActive: true}}
	require.Equal(t, "Query details", ShellTopbarTitle("/orgs/acme/data-sources/source/queries/query", items, "Query details"))
	require.Equal(t, "Queries", ShellTopbarTitle("/orgs/acme/queries", items, ""))
}

func TestDashboardAppendQuery_EscapesInternalLinks(t *testing.T) {
	t.Parallel()

	got := dashboardAppendQuery("/orgs/acme/projects/default/runs?environment_slug=dev", "status", "failed & queued")

	require.Equal(t, "/orgs/acme/projects/default/runs?environment_slug=dev&status=failed+%26+queued", got)
	require.Equal(t, "/dashboard", dashboardAppendQuery("/dashboard", "", "ignored"))
	require.Equal(t, "https://example.test/dashboard", dashboardAppendQuery("https://example.test/dashboard", "status", "failed"))
}
