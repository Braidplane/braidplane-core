package views

import (
	"fmt"
	"net/url"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

const rolePermissionBlankRows = 3

const (
	RoleListViewActive   = "active"
	RoleListViewArchived = "archived"
)

const (
	roleLifecycleActiveBadge = "badge badge-success"
)

// RolePermissionFormData stores one editable permission row in role forms.
type RolePermissionFormData struct {
	Resource string
	Action   string
}

// RoleFormData stores sticky values for role create and update forms.
type RoleFormData struct {
	Name        string
	Slug        string
	Permissions []RolePermissionFormData
}

// RolePermissionOption describes a selectable role permission value.
type RolePermissionOption struct {
	Value string
	Label string
}

// DefaultRoleFormData returns an empty create-role form with editable permission rows.
func DefaultRoleFormData() RoleFormData {
	return RoleFormData{Permissions: RolePermissionRows(nil)}
}

// RoleCountLabel formats a compact role count for section badges.
func RoleCountLabel(count int) string {
	if count == 1 {
		return "1 role"
	}
	return fmt.Sprintf("%d roles", count)
}

// RolePermissionCountLabel formats the number of permissions on a role.
func RolePermissionCountLabel(count int) string {
	if count == 1 {
		return "1 permission"
	}
	return fmt.Sprintf("%d permissions", count)
}

// RolePermissionSummary returns a compact visible summary for dense role rows.
func RolePermissionSummary(role api.OrgRole) string {
	if len(role.Permissions) == 0 {
		return "No permissions"
	}
	if roleHasWildcardPermission(role) {
		return "All organization permissions"
	}

	parts := make([]string, 0, len(role.Permissions))
	for _, permission := range role.Permissions {
		parts = append(parts, rolePermissionLabel(permission))
	}
	if len(parts) <= 3 {
		return strings.Join(parts, ", ")
	}
	return strings.Join(parts[:3], ", ") + fmt.Sprintf(" and %d more", len(parts)-3)
}

// RolePermissionRows returns current permissions plus a few blanks for form editing.
func RolePermissionRows(permissions []api.Permission) []RolePermissionFormData {
	rows := make([]RolePermissionFormData, 0, len(permissions)+rolePermissionBlankRows)
	for _, permission := range permissions {
		rows = append(rows, RolePermissionFormData{
			Resource: strings.TrimSpace(permission.Resource),
			Action:   strings.TrimSpace(string(permission.Action)),
		})
	}
	for i := 0; i < rolePermissionBlankRows; i++ {
		rows = append(rows, RolePermissionFormData{})
	}
	return rows
}

// NormalizeRoleListView maps unknown role list view values back to active roles.
func NormalizeRoleListView(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case RoleListViewArchived, "deleted", "true":
		return RoleListViewArchived
	default:
		return RoleListViewActive
	}
}

// RoleListIncludesArchived reports whether the list API should include archived roles.
func RoleListIncludesArchived(view string) bool {
	return NormalizeRoleListView(view) == RoleListViewArchived
}

// RoleListOnlyArchived reports whether the list API should return archived roles only.
func RoleListOnlyArchived(view string) bool {
	return NormalizeRoleListView(view) == RoleListViewArchived
}

// RoleListViewHref builds the organization roles URL for the requested view.
func RoleListViewHref(orgSlug, view string) string {
	base := "/organizations/" + strings.TrimSpace(orgSlug) + "/roles"
	if NormalizeRoleListView(view) == RoleListViewActive {
		return base
	}

	values := url.Values{}
	values.Set("deleted", "true")
	return base + "?" + values.Encode()
}

// RoleListViewButtonClass returns the tab treatment for active and archived role lists.
func RoleListViewButtonClass(currentView, targetView string) string {
	base := "btn btn-sm"
	if NormalizeRoleListView(currentView) == NormalizeRoleListView(targetView) {
		return base + " btn-primary"
	}
	return base + " btn-outline"
}

// RoleListViewLabel returns a short display label for the current roles list view.
func RoleListViewLabel(view string) string {
	if NormalizeRoleListView(view) == RoleListViewArchived {
		return "Archived roles"
	}
	return "Active roles"
}

// RolesForListView filters role rows to the visible lifecycle list.
func RolesForListView(roles []api.OrgRole, view string) []api.OrgRole {
	if roles == nil {
		return nil
	}

	filtered := make([]api.OrgRole, 0, len(roles))
	archivedView := NormalizeRoleListView(view) == RoleListViewArchived
	for _, role := range roles {
		if RoleArchived(role) == archivedView {
			filtered = append(filtered, role)
		}
	}
	return filtered
}

// RoleResourceOptions returns common organization-scoped permission resources.
func RoleResourceOptions() []RolePermissionOption {
	return []RolePermissionOption{
		{Value: "*", Label: "All resources"},
		{Value: "organizations", Label: "Organizations"},
		{Value: "members", Label: "Members"},
		{Value: "roles", Label: "Roles"},
		{Value: "api_keys", Label: "API keys"},
		{Value: "projects", Label: "Projects"},
		{Value: "environments", Label: "Environments"},
		{Value: "datasources", Label: "Data sources"},
		{Value: "queries", Label: "Queries"},
		{Value: "pipelines", Label: "Pipelines"},
		{Value: "pipeline_triggers", Label: "Pipeline triggers"},
		{Value: "events", Label: "Events"},
		{Value: "notifications", Label: "Notifications"},
		{Value: "alert_policies", Label: "Alert policies"},
		{Value: "alert_destinations", Label: "Alert destinations"},
	}
}

// RoleActionOptions returns supported role permission actions.
func RoleActionOptions() []RolePermissionOption {
	return []RolePermissionOption{
		{Value: "*", Label: "All actions"},
		{Value: "create", Label: "Create"},
		{Value: "read", Label: "Read"},
		{Value: "update", Label: "Update"},
		{Value: "delete", Label: "Delete"},
		{Value: "restore", Label: "Restore"},
		{Value: "purge", Label: "Purge"},
		{Value: "execute", Label: "Execute"},
	}
}

// RoleEditPanelID builds the per-role edit panel identifier.
func RoleEditPanelID(role api.OrgRole) string {
	return "organization-role-" + role.Slug + "-edit"
}

// RoleDetailsPanelID builds the per-role permissions detail panel identifier.
func RoleDetailsPanelID(role api.OrgRole) string {
	return "organization-role-" + role.Slug + "-details"
}

// RoleArchiveModalID builds the protected role archive confirmation modal id.
func RoleArchiveModalID(role api.OrgRole) string {
	return "organization-role-" + role.Slug + "-archive"
}

// RoleMutable reports whether the role should expose unsafe mutation controls.
func RoleMutable(role api.OrgRole) bool {
	return !role.IsDefault && !RoleArchived(role)
}

// RoleRestorable reports whether the role can be restored from the archived view.
func RoleRestorable(role api.OrgRole) bool {
	return !role.IsDefault && RoleArchived(role)
}

// RoleArchived reports whether the backend has marked this role as archived.
func RoleArchived(role api.OrgRole) bool {
	return role.DeletedAt != nil
}

// RoleLifecycleLabel summarizes whether a role is active or archived.
func RoleLifecycleLabel(role api.OrgRole) string {
	if RoleArchived(role) {
		return "Archived"
	}
	return viewStatusActive
}

// RoleLifecycleBadgeClass maps a role lifecycle state to compact badge styling.
func RoleLifecycleBadgeClass(role api.OrgRole) string {
	if RoleArchived(role) {
		return eventBadgeOutline
	}
	return roleLifecycleActiveBadge
}

// RoleArchivedAtLabel formats the archived timestamp for dense role rows.
func RoleArchivedAtLabel(role api.OrgRole) string {
	if role.DeletedAt == nil {
		return ""
	}
	return role.DeletedAt.Format("2006-01-02 15:04 MST")
}

// RoleSystemBadgeClass returns a compact badge class for system and custom roles.
func RoleSystemBadgeClass(role api.OrgRole) string {
	if role.IsDefault {
		return viewBadgeWarning
	}
	return eventBadgeOutline
}

// RolePermissionLabel formats one backend permission tuple for display.
func RolePermissionLabel(permission api.Permission) string {
	return rolePermissionLabel(permission)
}

// rolePermissionLabel formats one backend permission tuple for display.
func rolePermissionLabel(permission api.Permission) string {
	resource := strings.TrimSpace(permission.Resource)
	action := strings.TrimSpace(string(permission.Action))
	if resource == "*" && action == "*" {
		return "*:*"
	}
	if resource == "" {
		resource = "unspecified"
	}
	if action == "" {
		action = "unspecified"
	}
	return resource + ":" + action
}

// roleHasWildcardPermission reports whether a role grants the all-access tuple.
func roleHasWildcardPermission(role api.OrgRole) bool {
	for _, permission := range role.Permissions {
		if strings.TrimSpace(permission.Resource) == "*" &&
			strings.TrimSpace(string(permission.Action)) == "*" {
			return true
		}
	}
	return false
}
