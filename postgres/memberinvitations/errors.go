package memberinvitations

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	ErrNotFound       = xerror.NotFound(xerror.MemberInvitation)
	ErrAlreadyPending = xerror.AlreadyExists(xerror.MemberInvitation)
	ErrTokenConflict  = xerror.AlreadyExists(xerror.MemberInvitation)
)
