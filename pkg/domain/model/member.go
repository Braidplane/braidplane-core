package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type NewMember struct {
	UserSlug slug.Slug `validate:"required"`
	Role     slug.Slug `validate:"required"`
}

type UpdateMember struct {
	Role slug.Slug `validate:"required"`
}

type Member struct {
	UserSlug slug.Slug
	Email    string
	Role     slug.Slug
	Timestamps
}

//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=MemberInvitationStatus -transform=snake -output=member_invitation_status_string.go -text -json -linecomment

// MemberInvitationStatus describes the durable lifecycle state of an organization member invitation.
type MemberInvitationStatus int

const (
	MemberInvitationStatusUnknown  MemberInvitationStatus = iota // unknown
	MemberInvitationStatusPending                                // pending
	MemberInvitationStatusAccepted                               // accepted
	MemberInvitationStatusDeclined                               // declined
	MemberInvitationStatusRevoked                                // revoked
	MemberInvitationStatusExpired                                // expired
)

// NewMemberInvitation describes the admin request to invite an email address into an organization role.
type NewMemberInvitation struct {
	Email string    `validate:"required,email"`
	Role  slug.Slug `validate:"required"`
}

// MemberInvitation represents a pending or historical invitation separately from active membership.
type MemberInvitation struct {
	Slug              slug.Slug
	OrganizationSlug  slug.Slug
	Email             string
	Role              slug.Slug
	Status            MemberInvitationStatus
	InvitedByUserSlug *slug.Slug
	AcceptedUserSlug  *slug.Slug
	ExpiresAt         time.Time
	LastSentAt        time.Time
	AcceptedAt        *time.Time
	DeclinedAt        *time.Time
	RevokedAt         *time.Time
	ExpiredAt         *time.Time
	Timestamps
}

// MemberInvitationAcceptance represents the accepted invitation and resulting organization member.
type MemberInvitationAcceptance struct {
	Invitation MemberInvitation
	Member     Member
}

// MemberInvitationEmail carries the non-secret context needed to render an invitation email.
type MemberInvitationEmail struct {
	Email            string
	OrganizationName string
	InviterName      string
	InviterEmail     string
	Role             slug.Slug
	ExpiresAt        time.Time
	Locale           string
}
