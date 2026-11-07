package views

import (
	"strconv"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

// MemberInviteFormData stores sticky values for the invite member form.
type MemberInviteFormData struct {
	Email string
	Role  string
}

// MemberRoleFormData keeps a failed role edit separate from the persisted role badge.
type MemberRoleFormData struct {
	UserSlug string
	Role     string
}

func (f MemberRoleFormData) RoleFor(member api.Member) string {
	if f.UserSlug == member.UserSlug && (f.Role == "admin" || f.Role == "viewer") {
		return f.Role
	}
	return member.Role
}

// OrganizationPermissions exposes effective org-scoped permissions to templates.
type OrganizationPermissions struct {
	entries map[string]struct{}
}

const (
	viewBadgeOutline = "badge badge-outline"
	viewBadgeWarning = "badge badge-warning badge-outline"
	viewBadgeSuccess = "badge badge-success badge-outline"
	viewBadgeError   = "badge badge-error badge-outline"
	viewBadgeNeutral = "badge badge-neutral badge-outline"
)

func DefaultMemberInviteFormData() MemberInviteFormData {
	return MemberInviteFormData{Role: "viewer"}
}

// MemberCountLabel formats compact table counters for member surfaces.
func MemberCountLabel(count int) string {
	return strconv.Itoa(count)
}

// OptionalAPISlug returns a readable placeholder when an API slug pointer is absent.
func OptionalAPISlug(value *api.Slug) string {
	if value == nil {
		return "Unknown"
	}
	return *value
}

// ShowMemberInvitationActions indicates whether an invitation can still be changed by admins.
func ShowMemberInvitationActions(status api.MemberInvitationStatus) bool {
	return string(status) == "pending"
}

// MemberInvitationStatusBadgeClass maps invitation lifecycle state to DaisyUI badge classes.
func MemberInvitationStatusBadgeClass(status api.MemberInvitationStatus) string {
	switch string(status) {
	case "pending":
		return viewBadgeWarning
	case "accepted":
		return viewBadgeSuccess
	case "expired", "revoked":
		return viewBadgeError
	case "declined":
		return viewBadgeNeutral
	default:
		return viewBadgeOutline
	}
}

func NewOrganizationPermissions(values ...string) OrganizationPermissions {
	entries := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		entries[trimmed] = struct{}{}
	}

	return OrganizationPermissions{entries: entries}
}

func DefaultOrganizationPermissions() OrganizationPermissions {
	return OrganizationPermissions{entries: map[string]struct{}{}}
}

func (p OrganizationPermissions) Can(resource, action string) bool {
	if len(p.entries) == 0 {
		return false
	}

	if _, ok := p.entries["*:*"]; ok {
		return true
	}

	resourceWildcard := strings.TrimSpace(resource) + ":*"
	if _, ok := p.entries[resourceWildcard]; ok {
		return true
	}

	permission := strings.TrimSpace(resource) + ":" + strings.TrimSpace(action)
	_, ok := p.entries[permission]
	return ok
}
