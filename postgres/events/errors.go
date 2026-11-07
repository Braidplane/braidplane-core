package events

import "github.com/Braidplane/braidplane-core/pkg/xerror"

var (
	ErrNotFound             = xerror.NotFound(xerror.Event)
	ErrSlugTaken            = xerror.AlreadyExists(xerror.Event)
	ErrOrganizationNotFound = xerror.NotFound(xerror.Organization)
)
