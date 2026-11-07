package rolessvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/roles"
)

// Re-exported from repository layer — single source of truth.
var (
	ErrRoleNotFound            = roles.ErrNotFound
	ErrRoleSlugAlreadyExists   = roles.ErrSlugAlreadyExists
	ErrCannotDeleteDefaultRole = roles.ErrCannotDeleteSystemRole
	ErrUserNotMember           = roles.ErrUserNotMember
)

// Service-only errors.
var (
	ErrInvalidOrgSlug           = xerror.InvalidInput(xerror.Organization)
	ErrInvalidRoleSlug          = xerror.InvalidInput(xerror.Role)
	ErrCannotUpdateDefaultRole  = xerror.CannotUpdateDefault(xerror.Role)
	ErrPermissionNotFound       = xerror.NotFound(xerror.Permission)
	ErrPermissionAlreadyGranted = xerror.AlreadyExists(xerror.Permission)
	ErrInvalidInput             = xerror.ValidationFailed()
)
