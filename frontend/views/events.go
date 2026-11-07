package views

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

const (
	eventBadgeOutline = "badge badge-outline"
	eventBadgeSuccess = badgeSuccessOutline
	eventBadgeError   = "badge badge-error badge-outline"
	eventBadgeInfo    = "badge badge-info badge-outline"
)

const (
	EventClassGeneric       = "generic"
	EventClassSecurityAudit = "security_audit"
)

type EventFilters struct {
	EventClass     string
	ActorType      string
	ActorSlug      string
	EntityType     string
	EntitySlug     string
	Action         string
	RequestID      string
	OccurredFrom   string
	OccurredBefore string
}

type eventFilterOption struct {
	Label string
	Value string
}

func eventActorTypeOptions() []eventFilterOption {
	return []eventFilterOption{
		{Label: "User", Value: "user"},
		{Label: "API key", Value: "api_key"},
		{Label: "System", Value: "system"},
		{Label: "External", Value: "external"},
	}
}

func eventEntityTypeOptions() []eventFilterOption {
	return []eventFilterOption{
		{Label: "Organization", Value: "organization"},
		{Label: "Member", Value: "member"},
		{Label: "Member invitation", Value: "member_invitation"},
		{Label: "Role", Value: "role"},
		{Label: "API key", Value: "api_key"},
		{Label: "Data source", Value: "data_source"},
		{Label: "Data source ref", Value: "data_source_ref"},
		{Label: "Data source binding", Value: "data_source_binding"},
		{Label: "Project", Value: "project"},
		{Label: "Environment", Value: "environment"},
		{Label: "Project environment", Value: "project_environment"},
		{Label: "Query", Value: "query"},
		{Label: "Query execution", Value: "query_execution"},
		{Label: "Pipeline", Value: "pipeline"},
		{Label: "Pipeline deployment", Value: "pipeline_deployment"},
		{Label: "Pipeline version", Value: "pipeline_version"},
		{Label: "Pipeline run", Value: "pipeline_run"},
		{Label: "Pipeline checkpoint", Value: "pipeline_checkpoint"},
		{Label: "Pipeline trigger", Value: "pipeline_trigger"},
		{Label: "Notification", Value: "notification"},
	}
}

func eventActionOptions() []eventFilterOption {
	return []eventFilterOption{
		{Label: "Create", Value: "create"},
		{Label: "Update", Value: "update"},
		{Label: "Delete", Value: "delete"},
		{Label: "Restore", Value: "restore"},
		{Label: "Purge", Value: "purge"},
		{Label: "Execute", Value: "execute"},
		{Label: "Refresh", Value: "refresh"},
		{Label: "Test connection", Value: "test_connection"},
		{Label: "Rotate", Value: "rotate"},
		{Label: "Revoke", Value: "revoke"},
		{Label: "Reconcile", Value: "reconcile"},
		{Label: "Deploy", Value: "deploy"},
		{Label: "Publish", Value: "publish"},
		{Label: "Enable", Value: "enable"},
		{Label: "Disable", Value: "disable"},
		{Label: "Accept", Value: "accept"},
		{Label: "Decline", Value: "decline"},
		{Label: "Expire", Value: "expire"},
		{Label: "Auto accept", Value: "auto_accept"},
		{Label: "Acknowledge", Value: "acknowledge"},
		{Label: "Resolve", Value: "resolve"},
	}
}

func buildEventsListHref(
	orgSlug string,
	cursor string,
	direction string,
	filters EventFilters,
) string {
	values := url.Values{}
	if strings.TrimSpace(cursor) != "" {
		values.Set("cursor", cursor)
	}
	if strings.TrimSpace(direction) != "" {
		values.Set("direction", direction)
	}
	if strings.TrimSpace(filters.ActorType) != "" {
		values.Set("actor_type", filters.ActorType)
	}
	if strings.TrimSpace(filters.ActorSlug) != "" {
		values.Set("actor_slug", filters.ActorSlug)
	}
	if strings.TrimSpace(filters.EntityType) != "" {
		values.Set("entity_type", filters.EntityType)
	}
	if strings.TrimSpace(filters.EntitySlug) != "" {
		values.Set("entity_slug", filters.EntitySlug)
	}
	if strings.TrimSpace(filters.Action) != "" {
		values.Set("action", filters.Action)
	}
	if filters.EventClass == EventClassSecurityAudit {
		values.Set("event_class", filters.EventClass)
		if filters.RequestID != "" {
			values.Set("request_id", filters.RequestID)
		}
		if filters.OccurredFrom != "" {
			values.Set("occurred_from", filters.OccurredFrom)
		}
		if filters.OccurredBefore != "" {
			values.Set("occurred_before", filters.OccurredBefore)
		}
	}

	base := "/orgs/" + strings.TrimSpace(orgSlug) + "/events"
	encoded := values.Encode()
	if encoded == "" {
		return base
	}

	return base + "?" + encoded
}

func buildSecurityAuditExportHref(orgSlug string, filters EventFilters) string {
	href, err := url.Parse(buildEventsListHref(orgSlug, "", "", filters))
	base := "/orgs/" + strings.TrimSpace(orgSlug) + "/events/export"
	if err != nil {
		return base
	}
	values := href.Query()
	values.Del("event_class")
	if encoded := values.Encode(); encoded != "" {
		return base + "?" + encoded
	}
	return base
}

func formatEventTimestamp(ts api.Timestamp) string {
	if ts.IsZero() {
		return "-"
	}

	return ts.UTC().Format(time.RFC3339)
}

func eventSlugLabel(slug *api.NullableSlug) string {
	if slug == nil || strings.TrimSpace(*slug) == "" {
		return "-"
	}

	return *slug
}

func eventTypeLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}

	return strings.ReplaceAll(value, "_", " ")
}

func eventMetadataJSON(metadata map[string]interface{}) string {
	metadata = displaySafeEventMetadata(metadata)
	if len(metadata) == 0 {
		return "{}"
	}

	payload, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return "{}"
	}

	return string(payload)
}

var prohibitedEventMetadataKey = regexp.MustCompile(
	`(?i)(credential|password|passwd|secret|token|cookie|session|authorization|header|payload|body|content|email|private.?key|parameter_values|provider_response|query_result|runtime_input)`,
)

func displaySafeEventMetadata(metadata map[string]interface{}) map[string]interface{} {
	safe := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		if prohibitedEventMetadataKey.MatchString(key) {
			continue
		}
		safe[key] = displaySafeEventMetadataValue(value)
	}
	return safe
}

func displaySafeEventMetadataValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return displaySafeEventMetadata(typed)
	case []interface{}:
		items := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			items = append(items, displaySafeEventMetadataValue(item))
		}
		return items
	default:
		return value
	}
}

func eventRequestIDLabel(requestID *string) string {
	if requestID == nil || strings.TrimSpace(*requestID) == "" {
		return "-"
	}
	return *requestID
}

func eventActionBadgeClass(action api.EventAction) string {
	switch action {
	case api.EventActionCreate, api.EventActionRestore, api.EventActionEnable, api.EventActionPublish:
		return badgeSuccessOutline
	case api.EventActionDelete, api.EventActionPurge, api.EventActionRevoke, api.EventActionDisable:
		return eventBadgeError
	case api.EventActionExecute, api.EventActionRefresh, api.EventActionReconcile, api.EventActionTestConnection:
		return eventBadgeInfo
	default:
		return eventBadgeOutline
	}
}
