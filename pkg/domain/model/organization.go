package model

import (
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type NewOrganization struct {
	Name string `validate:"required,min=1,max=100"`
	Slug slug.Slug
}

type UpdateOrganization struct {
	Name string `validate:"required,min=1,max=100"`
}

type Organization struct {
	Name     string
	Slug     slug.Slug
	Settings []byte
	Timestamps
}

type OrganizationWithRole struct {
	Organization
	MemberRole string
}
