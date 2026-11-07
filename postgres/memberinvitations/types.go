package memberinvitations

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateParams struct {
	OrganizationSlug  slug.Slug
	InvitationSlug    slug.Slug
	Email             string
	RoleSlug          slug.Slug
	InvitedByUserSlug *slug.Slug
	TokenHash         string
	ExpiresAt         time.Time
}

type RefreshTokenParams struct {
	OrganizationSlug slug.Slug
	InvitationSlug   slug.Slug
	TokenHash        string
	ExpiresAt        time.Time
	LastSentAt       time.Time
}

type MarkAcceptedParams struct {
	OrganizationSlug slug.Slug
	InvitationSlug   slug.Slug
	AcceptedUserSlug slug.Slug
	AcceptedAt       time.Time
}

type MarkDeclinedParams struct {
	OrganizationSlug slug.Slug
	InvitationSlug   slug.Slug
	DeclinedAt       time.Time
}

type MarkRevokedParams struct {
	OrganizationSlug slug.Slug
	InvitationSlug   slug.Slug
	RevokedAt        time.Time
}

type MarkExpiredParams struct {
	OrganizationSlug slug.Slug
	InvitationSlug   slug.Slug
	ExpiredAt        time.Time
}

type Invitation struct {
	Slug              slug.Slug
	OrganizationSlug  slug.Slug
	Email             string
	RoleSlug          slug.Slug
	Status            model.MemberInvitationStatus
	InvitedByUserSlug *slug.Slug
	AcceptedUserSlug  *slug.Slug
	ExpiresAt         time.Time
	LastSentAt        time.Time
	AcceptedAt        *time.Time
	DeclinedAt        *time.Time
	RevokedAt         *time.Time
	ExpiredAt         *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
