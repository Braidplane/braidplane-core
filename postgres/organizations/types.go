package organizations

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateParams struct {
	Name string
	Slug slug.Slug
}

type UpdateParams struct {
	Slug slug.Slug
	Name string
}

type UpdateSettingsParams struct {
	Slug     slug.Slug
	Settings []byte
}

type Organization struct {
	Name      string
	Slug      slug.Slug
	Settings  []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

type OrganizationWithRole struct {
	Organization
	MemberRole string
}
