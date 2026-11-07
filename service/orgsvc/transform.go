package orgsvc

import (
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
)

func (s *service) toOrganization(org *organizations.Organization) *model.Organization {
	return &model.Organization{
		Name:     org.Name,
		Slug:     org.Slug,
		Settings: org.Settings,
		Timestamps: model.Timestamps{
			CreatedAt: org.CreatedAt,
			UpdatedAt: org.UpdatedAt,
		},
	}
}

func (s *service) toOrganizationWithRole(org *organizations.OrganizationWithRole) model.OrganizationWithRole {
	return model.OrganizationWithRole{
		Organization: model.Organization{
			Name:     org.Name,
			Slug:     org.Slug,
			Settings: org.Settings,
			Timestamps: model.Timestamps{
				CreatedAt: org.CreatedAt,
				UpdatedAt: org.UpdatedAt,
			},
		},
		MemberRole: org.MemberRole,
	}
}
