package rolessvc

import (
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/postgres/roles"
)

func toRole(r *roles.Role) *model.OrgRole {
	return &model.OrgRole{
		Slug:      r.Slug,
		Name:      r.Name,
		IsDefault: r.IsDefault,
		DeletedAt: r.DeletedAt,
		Timestamps: model.Timestamps{
			CreatedAt: r.CreatedAt,
			UpdatedAt: r.UpdatedAt,
		},
	}
}

func toRoleList(rs []*roles.Role) []model.OrgRole {
	result := make([]model.OrgRole, len(rs))
	for i, r := range rs {
		result[i] = *toRole(r)
	}
	return result
}
