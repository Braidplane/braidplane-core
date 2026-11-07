package braidplane.authz

import future.keywords.if
import future.keywords.in

default allow := false

# Global role permissions (system-level roles)
# These apply when user is NOT in an organization context
role_permissions := {
	"super_admin": ["*:*"],
	"user": ["organizations:create", "organizations:read"],
}

# Get permissions based on context:
# - For org-scoped operations: use permissions loaded from database (input.user.permissions)
# - For system-level operations: use role_permissions from above
user_permissions := input.user.permissions if {
	input.org_scoped
}

user_permissions := role_permissions[input.user.role] if {
	not input.org_scoped
}

# Admin wildcard permission - grants access to everything
allow if {
    "*:*" in user_permissions
}

# Resource wildcard permission - grants access to all actions on a specific resource
allow if {
    resource_wildcard := concat(":", [input.resource, "*"])
    resource_wildcard in user_permissions
}

# Standard permission check - user has the specific permission
allow if {
    required_permission := concat(":", [input.resource, input.action])
    required_permission in user_permissions
}

# Special case: users can always read their own profile
allow if {
    not input.org_scoped
    input.resource == "profile"
    input.action == "read"
    input.resource_owner_slug == input.user.slug
}

# Special case: users can always update their own profile
allow if {
    not input.org_scoped
    input.resource == "profile"
    input.action == "update"
    input.resource_owner_slug == input.user.slug
}

# Account removal is limited to the current subject at the HTTP boundary.
allow if {
    not input.org_scoped
    input.resource == "profile"
    input.action == "delete"
    input.resource_owner_slug == input.user.slug
}
