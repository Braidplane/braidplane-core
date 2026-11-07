package views

import (
	"fmt"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

// APIKeyCreateFormData stores sticky values for the API key create form.
type APIKeyCreateFormData struct {
	Name             string
	Slug             string
	Role             string
	Description      string
	ExpiresAt        string
	CanManageAPIKeys bool
}

func DefaultAPIKeyCreateFormData() APIKeyCreateFormData {
	return APIKeyCreateFormData{Role: "viewer"}
}

// APIKeyTokenNotice holds the one-time token shown after create or rotation.
type APIKeyTokenNotice struct {
	Action string
	Name   string
	Slug   string
	Token  string
}

func APIKeyStatusLabel(key api.APIKey) string {
	if key.RevokedAt != nil {
		return "Revoked"
	}
	if APIKeyExpired(key) {
		return viewStatusExpired
	}
	return viewStatusActive
}

func APIKeyStatusBadgeClass(key api.APIKey) string {
	if key.RevokedAt != nil {
		return "badge " + "badge-outline"
	}
	if APIKeyExpired(key) {
		return "badge badge-warning"
	}
	return "badge badge-success"
}

// APIKeyCountLabel formats a compact API-key count for section badges.
func APIKeyCountLabel(count int) string {
	if count == 1 {
		return "1 key"
	}
	return fmt.Sprintf("%d keys", count)
}

func APIKeyExpired(key api.APIKey) bool {
	return key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now())
}

func FormatOptionalAPIKeyTime(value *api.NullableTimestamp) string {
	if value == nil {
		return "Never"
	}
	return value.Format("2006-01-02 15:04 MST")
}

func FormatAPIKeyDateTimeLocal(value *api.NullableTimestamp) string {
	if value == nil {
		return ""
	}
	return value.Local().Format("2006-01-02T15:04")
}

// APIKeyDescriptionValue returns a stable empty-string fallback for optional descriptions in forms.
func APIKeyDescriptionValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// APIKeyEditPanelID builds the per-row metadata edit panel identifier.
func APIKeyEditPanelID(key api.APIKey) string {
	return "organization-api-key-" + key.Slug + "-edit"
}

// APIKeyRotateModalID builds the destructive rotation confirmation modal identifier.
func APIKeyRotateModalID(key api.APIKey) string {
	return "organization-api-key-" + key.Slug + "-rotate"
}

// APIKeyRevokeModalID builds the destructive revocation confirmation modal identifier.
func APIKeyRevokeModalID(key api.APIKey) string {
	return "organization-api-key-" + key.Slug + "-revoke"
}

// APIKeyDeleteModalID builds the destructive permanent-delete confirmation modal identifier.
func APIKeyDeleteModalID(key api.APIKey) string {
	return "organization-api-key-" + key.Slug + "-delete"
}
