package roles

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateParams struct {
	OrgSlug   slug.Slug
	Slug      slug.Slug
	Name      string
	IsDefault bool
}

type UpdateParams struct {
	OrgSlug slug.Slug
	Slug    slug.Slug
	Name    string
}

type DeleteParams struct {
	OrgSlug slug.Slug
	Slug    slug.Slug
}

type RestoreParams struct {
	OrgSlug slug.Slug
	Slug    slug.Slug
}

type Role struct {
	Slug      slug.Slug
	Name      string
	IsDefault bool
	DeletedAt *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

type UserPermission struct {
	Resource string
	Action   string
}
