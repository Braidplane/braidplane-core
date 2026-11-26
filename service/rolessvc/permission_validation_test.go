package rolessvc

import (
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/stretchr/testify/require"
)

func TestRoleMutationsRejectUnsupportedPermissionsBeforeWriting(t *testing.T) {
	t.Parallel()
	svc := &service{}
	for _, permission := range []model.RolePermission{
		{Resource: "roles", Action: "execute"},
		{Resource: "profile", Action: "read"},
		{Resource: "*", Action: "read"},
		{Resource: "queries", Action: "write"},
	} {
		permissions := []model.RolePermission{permission}
		_, err := svc.CreateRoleWithPermissions(t.Context(), "acme", model.EventActor{}, model.NewRole{Name: "Reviewer"}, permissions)
		require.ErrorIs(t, err, ErrInvalidInput)
		err = svc.UpdateRoleWithPermissions(t.Context(), "acme", "reviewer", model.EventActor{}, model.UpdateRole{Name: "Reviewer"}, permissions)
		require.ErrorIs(t, err, ErrInvalidInput)
	}
}
