package memberssvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
	"github.com/Braidplane/braidplane-core/postgres/members"
)

// Re-exported from repository layer — single source of truth.
var (
	ErrMemberNotFound      = members.ErrNotFound
	ErrMemberAlreadyExists = members.ErrAlreadyMember
	ErrInvitationNotFound  = memberinvitations.ErrNotFound
	ErrInvitationPending   = memberinvitations.ErrAlreadyPending
)

// Service-only errors.
var (
	ErrInvalidOrgSlug        = xerror.InvalidInput(xerror.Organization)
	ErrInvalidUserSlug       = xerror.InvalidInput(xerror.User)
	ErrInvalidRole           = xerror.InvalidInput(xerror.Member)
	ErrCannotRemoveLastAdmin = xerror.CannotRemoveOwner()
	ErrCannotDemoteLastAdmin = xerror.CannotDemoteOwner()
	ErrInvitationNotPending  = xerror.New(xerror.KindInvalidInput, "MEMBER_INVITATION_NOT_PENDING", "member invitation not pending")
	ErrInvitationExpired     = xerror.New(xerror.KindInvalidInput, "MEMBER_INVITATION_EXPIRED", "member invitation expired")
)
