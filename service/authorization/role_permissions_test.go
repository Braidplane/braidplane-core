package authorization

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/open-policy-agent/opa/v1/rego"
	"github.com/stretchr/testify/require"
)

func TestRolePermissionCatalogMatchesTenantPolicy(t *testing.T) {
	t.Parallel()
	query := rego.New(
		rego.Query("some resource, actions in data.braidplane.authz.tenant_actions; some action in actions"),
		rego.Module("authz.rego", authzModule),
	)
	results, err := query.Eval(t.Context())
	require.NoError(t, err)
	want := map[model.RolePermission]bool{{Resource: "*", Action: "*"}: true}
	for _, row := range results {
		resource, action := row.Bindings["resource"].(string), row.Bindings["action"].(string)
		want[model.RolePermission{Resource: resource, Action: action}] = true
		want[model.RolePermission{Resource: resource, Action: "*"}] = true
	}
	require.Greater(t, len(want), 4)
	got := make(map[model.RolePermission]bool)
	for _, option := range RolePermissionCatalog() {
		require.NotEmpty(t, option.ResourceLabel)
		require.NotEmpty(t, option.ActionLabel)
		require.True(t, IsGrantableRolePermission(option.RolePermission))
		require.False(t, got[option.RolePermission], "duplicate option: %v", option)
		got[option.RolePermission] = true
	}
	require.Equal(t, want, got)
}
