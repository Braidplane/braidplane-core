package orgsvc

import (
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
)

// Re-exported from repository layer — single source of truth.
var (
	ErrOrganizationNotFound = organizations.ErrNotFound
	ErrSlugAlreadyExists    = organizations.ErrSlugAlreadyExists
)

// Service-only errors.
var (
	ErrInvalidUserSlug = xerror.InvalidInput(xerror.User)
	ErrInvalidOrgSlug  = xerror.InvalidInput(xerror.Organization)
	ErrDeletionBlocked = xerror.New(xerror.KindAlreadyExists, "ORGANIZATION_DELETION_BLOCKED", "remove projects and data sources, resolve outstanding processing, and wait for Snowflake workload identity cleanup to complete before retrying organization deletion")
)
