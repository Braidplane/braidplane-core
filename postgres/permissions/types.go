package permissions

import "github.com/Braidplane/braidplane-core/pkg/slug"

type CreateParams struct {
	Resource string
	Action   string
}

type Permission struct {
	Resource string
	Action   string
}

type AddToRoleParams struct {
	OrgSlug  slug.Slug
	RoleSlug slug.Slug
	Resource string
	Action   string
}

type RemoveFromRoleParams struct {
	OrgSlug  slug.Slug
	RoleSlug slug.Slug
	Resource string
	Action   string
}

type ClearRolePermissionsParams struct {
	OrgSlug  slug.Slug
	RoleSlug slug.Slug
}
