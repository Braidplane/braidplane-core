package apikeys

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateParams struct {
	OrgSlug           slug.Slug
	Slug              slug.Slug
	Name              string
	Description       *string
	TokenHash         string
	TokenPrefix       string
	CreatedByUserSlug *slug.Slug
	RoleSlug          slug.Slug
	ExpiresAt         *time.Time
	CanManageAPIKeys  bool
}

type UpdateParams struct {
	OrgSlug             slug.Slug
	Slug                slug.Slug
	Name                string
	Description         *string
	ExpiresAt           *time.Time
	CanManageAPIKeysSet bool
	CanManageAPIKeys    bool
}

type RotateParams struct {
	OrgSlug     slug.Slug
	Slug        slug.Slug
	TokenHash   string
	TokenPrefix string
}

type APIKey struct {
	ID                int
	Slug              slug.Slug
	Name              string
	Description       *string
	TokenPrefix       string
	RoleSlug          slug.Slug
	CanManageAPIKeys  bool
	CreatedByUserSlug *slug.Slug
	ExpiresAt         *time.Time
	RevokedAt         *time.Time
	LastUsedAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Principal struct {
	Slug             slug.Slug
	OrgSlug          slug.Slug
	Role             slug.Slug
	CanManageAPIKeys bool
}
