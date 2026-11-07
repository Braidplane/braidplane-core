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
