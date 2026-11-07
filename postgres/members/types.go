package members

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateParams struct {
	OrganizationSlug slug.Slug
	UserSlug         slug.Slug
	RoleSlug         slug.Slug
}

type Member struct {
	UserSlug  slug.Slug
	UserEmail string
	RoleSlug  slug.Slug
	CreatedAt time.Time
	UpdatedAt time.Time
}
