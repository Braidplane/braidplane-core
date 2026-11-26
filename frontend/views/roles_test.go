package views

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestRolesForListViewSplitsActiveAndArchivedRoles(t *testing.T) {
	t.Parallel()

	deletedAt := time.Date(2026, 6, 28, 11, 30, 0, 0, time.UTC)
	active := api.OrgRole{Slug: "active", Name: "Active"}
	archived := api.OrgRole{Slug: "archived", Name: "Archived", DeletedAt: &deletedAt}
	roles := []api.OrgRole{active, archived}

	require.Nil(t, RolesForListView(nil, RoleListViewActive))
	require.Equal(t, []api.OrgRole{active}, RolesForListView(roles, RoleListViewActive))
	require.Equal(t, []api.OrgRole{active}, RolesForListView(roles, "unknown"))
	require.Equal(t, []api.OrgRole{archived}, RolesForListView(roles, RoleListViewArchived))
}

func TestRolePermissionGroupsExpandSelectedWildcards(t *testing.T) {
	t.Parallel()

	catalog := []api.RolePermissionOption{
		{Resource: "*", Action: "*"},
		{Resource: "events", Action: "*"},
		{Resource: "events", Action: "read"},
		{Resource: "pipelines", Action: "*"},
		{Resource: "pipelines", Action: "execute"},
		{Resource: "pipelines", Action: "read"},
	}
	groups, wildcards := rolePermissionGroups(catalog, []string{"pipelines:*", "pipelines:read", "legacy:read"})
	require.Equal(t, []string{"pipelines:*"}, wildcards)
	require.Len(t, groups, 3)
	require.False(t, groups[0].Choices[0].Checked)
	require.True(t, groups[1].Choices[0].Checked)
	require.False(t, groups[1].Choices[0].Explicit)
	require.True(t, groups[1].Choices[1].Explicit)
	require.Empty(t, groups[2].Resource)
	require.Equal(t, "legacy:read", groups[2].Choices[0].Value)

	groups, wildcards = rolePermissionGroups(catalog, []string{"*:*"})
	require.Equal(t, []string{"*:*"}, wildcards)
	for _, group := range groups {
		for _, choice := range group.Choices {
			require.True(t, choice.Checked)
		}
	}
}
