package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type NewAPIKey struct {
	Name             string    `validate:"required"`
	Slug             slug.Slug `validate:"omitempty"`
	Description      *string
	Role             slug.Slug `validate:"required"`
	ExpiresAt        *time.Time
	CanManageAPIKeys bool
}

type UpdateAPIKey struct {
	Name             string `validate:"required"`
	Description      *string
	ExpiresAt        *time.Time
	CanManageAPIKeys *bool
}

type APIKey struct {
	Slug              slug.Slug
	Name              string
	Description       *string
	TokenPrefix       string
	Role              slug.Slug
	CanManageAPIKeys  bool
	CreatedByUserSlug *slug.Slug
	ExpiresAt         *time.Time
	RevokedAt         *time.Time
	LastUsedAt        *time.Time
	Timestamps
}

type APIKeyWithToken struct {
	APIKey
	Token string
}

type APIKeyPrincipal struct {
	Slug             slug.Slug
	OrgSlug          slug.Slug
	Role             slug.Slug
	CanManageAPIKeys bool
}
