package pats

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateParams struct {
	UserSlug                      slug.Slug
	Slug                          slug.Slug
	Name                          string
	Description                   *string
	TokenHash                     string
	TokenPrefix                   string
	ExpiresAt                     *time.Time
	CanManagePersonalAccessTokens bool
}

type UpdateParams struct {
	UserSlug                         slug.Slug
	Slug                             slug.Slug
	Name                             string
	DescriptionSet                   bool
	Description                      *string
	ExpiresAtSet                     bool
	ExpiresAt                        *time.Time
	CanManagePersonalAccessTokensSet bool
	CanManagePersonalAccessTokens    bool
}

type RotateParams struct {
	UserSlug    slug.Slug
	Slug        slug.Slug
	TokenHash   string
	TokenPrefix string
}

type PersonalAccessToken struct {
	ID                            int
	Slug                          slug.Slug
	Name                          string
	Description                   *string
	TokenPrefix                   string
	UserSlug                      slug.Slug
	CanManagePersonalAccessTokens bool
	ExpiresAt                     *time.Time
	RevokedAt                     *time.Time
	LastUsedAt                    *time.Time
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
}

type Principal struct {
	TokenSlug                     slug.Slug
	UserSlug                      slug.Slug
	CanManagePersonalAccessTokens bool
}
