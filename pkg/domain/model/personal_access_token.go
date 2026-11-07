package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type NewPersonalAccessToken struct {
	Name                          string    `validate:"required"`
	Slug                          slug.Slug `validate:"omitempty"`
	Description                   *string
	ExpiresAt                     *time.Time
	CanManagePersonalAccessTokens bool
}

// NullableStringUpdate carries both field presence and nullable string value for partial updates.
type NullableStringUpdate struct {
	Present bool
	Value   *string
}

// NullableTimeUpdate carries both field presence and nullable time value for partial updates.
type NullableTimeUpdate struct {
	Present bool
	Value   *time.Time
}

type UpdatePersonalAccessToken struct {
	Name                          string `validate:"required"`
	Description                   NullableStringUpdate
	ExpiresAt                     NullableTimeUpdate
	CanManagePersonalAccessTokens *bool
}

type PersonalAccessToken struct {
	Slug                          slug.Slug
	Name                          string
	Description                   *string
	TokenPrefix                   string
	UserSlug                      slug.Slug
	CanManagePersonalAccessTokens bool
	ExpiresAt                     *time.Time
	RevokedAt                     *time.Time
	LastUsedAt                    *time.Time
	Timestamps
}

type PersonalAccessTokenWithToken struct {
	PersonalAccessToken
	Token string
}

type UserPrincipal struct {
	UserSlug                      slug.Slug
	PersonalAccessTokenSlug       slug.Slug
	CanManagePersonalAccessTokens bool
}
