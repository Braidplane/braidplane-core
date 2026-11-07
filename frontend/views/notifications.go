package views

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

const (
	NotificationFilterStateActive   = "active"
	NotificationFilterStateResolved = "resolved"

	notificationBadgeError          = "badge badge-error"
	notificationBadgeErrorOutline   = "badge badge-error badge-outline"
	notificationBadgeInfo           = "badge badge-info"
	notificationBadgeInfoOutline    = "badge badge-info badge-outline"
	notificationBadgeOutline        = "badge badge-outline"
	notificationBadgeSuccessOutline = "badge badge-success badge-outline"
	notificationBadgeWarning        = "badge badge-warning"
	notificationBadgeWarningOutline = "badge badge-warning badge-outline"
)

type NotificationFilters struct {
	Cursor           string
	Direction        string
	State            string
	NotificationType string
	Severity         string
}

type notificationEvidenceLink struct {
	Label string
	Href  string
}

type notificationFilterOption struct {
	Label string
	Value string
}

type notificationLifecycleEvidenceRow struct {
	Label     string
	Timestamp string
}

// NormalizeNotificationFilters coerces tab and filter values into supported display states.
func NormalizeNotificationFilters(filters NotificationFilters) NotificationFilters {
	switch strings.TrimSpace(filters.State) {
	case NotificationFilterStateResolved:
		filters.State = NotificationFilterStateResolved
	default:
		filters.State = NotificationFilterStateActive
	}
	filters.NotificationType = strings.TrimSpace(filters.NotificationType)
	filters.Severity = strings.TrimSpace(filters.Severity)
	return filters
}

// NotificationsListHref builds the route-backed notifications URL for handlers and templates.
func NotificationsListHref(orgSlug, cursor, direction string, filters NotificationFilters) string {
	return buildNotificationsListHref(orgSlug, cursor, direction, filters)
}

func buildNotificationsListHref(
	orgSlug string,
	cursor string,
	direction string,
	filters NotificationFilters,
) string {
	filters = NormalizeNotificationFilters(filters)
	values := url.Values{}
	if strings.TrimSpace(cursor) != "" {
		values.Set("cursor", cursor)
	}
	if strings.TrimSpace(direction) != "" {
		values.Set("direction", direction)
	}
	if filters.State == NotificationFilterStateResolved {
		values.Set("state", filters.State)
	}
	if filters.NotificationType != "" {
		values.Set("notification_type", filters.NotificationType)
	}
	if filters.Severity != "" {
		values.Set("severity", filters.Severity)
	}

	base := "/orgs/" + strings.TrimSpace(orgSlug) + "/notifications"
	encoded := values.Encode()
	if encoded == "" {
		return base
	}
	return base + "?" + encoded
}

// notificationActionHref builds an SSR action route for one notification lifecycle transition.
func notificationActionHref(orgSlug string, notification api.NotificationRecord, action string) string {
	return "/orgs/" + strings.TrimSpace(orgSlug) +
		"/notifications/" + url.PathEscape(notification.Slug) +
		"/" + url.PathEscape(strings.TrimSpace(action))
}

// notificationCanAcknowledge reports whether the operator should see the acknowledge action.
func notificationCanAcknowledge(notification api.NotificationRecord) bool {
	return notification.LifecycleState == api.NotificationLifecycleStateOpen
}

// notificationCanResolve reports whether the operator should see the resolve action.
func notificationCanResolve(notification api.NotificationRecord) bool {
	return notification.LifecycleState == api.NotificationLifecycleStateOpen ||
		notification.LifecycleState == api.NotificationLifecycleStateAcknowledged
}

// allNotificationTypeOptions keeps labels for stored records even when planned
// producers are hidden from create/filter selectors.
func allNotificationTypeOptions() []notificationFilterOption {
	return []notificationFilterOption{
		{Label: "Pipeline run succeeded", Value: string(api.NotificationTypePipelineRunSucceeded)},
		{Label: "Pipeline run failed", Value: string(api.NotificationTypePipelineRunFailed)},
		{Label: "Pipeline retry exhausted", Value: string(api.NotificationTypePipelineRetryExhausted)},
		{Label: "Scheduled run delayed", Value: string(api.NotificationTypePipelineScheduledRunDelayed)},
		{Label: "Scheduled run missing", Value: string(api.NotificationTypePipelineScheduledRunMissing)},
		{Label: "Successful-run recency breached", Value: string(api.NotificationTypePipelineFreshnessSLABreached)},
		{Label: "Pipeline contract failure", Value: string(api.NotificationTypePipelineContractFailure)},
	}
}

func notificationTypeOptions(includeRecency bool) []notificationFilterOption {
	options := []notificationFilterOption{}
	types := model.ProducerBackedNotificationTypes()
	for _, supported := range types {
		if supported == model.NotificationTypePipelineFreshnessSLABreached && !includeRecency {
			continue
		}
		for _, option := range allNotificationTypeOptions() {
			if option.Value == supported.String() {
				options = append(options, option)
			}
		}
	}
	return options
}

func notificationSeverityOptions() []notificationFilterOption {
	return []notificationFilterOption{
		{Label: "Critical", Value: "critical"},
		{Label: "Warning", Value: "warning"},
		{Label: "Info", Value: "info"},
	}
}

func notificationFiltersActive(filters NotificationFilters) bool {
	filters = NormalizeNotificationFilters(filters)
	return filters.NotificationType != "" ||
		filters.Severity != ""
}

func notificationStateTabClass(filters NotificationFilters, state string) string {
	filters = NormalizeNotificationFilters(filters)
	if filters.State == state {
		return tabActiveClass
	}
	return tabClass
}

func notificationSeverityBadgeClass(severity api.NotificationSeverity) string {
	switch severity {
	case api.NotificationSeverityCritical:
		return notificationBadgeError
	case api.NotificationSeverityWarning:
		return notificationBadgeWarning
	case api.NotificationSeverityInfo:
		return notificationBadgeInfo
	default:
		return notificationBadgeOutline
	}
}

func notificationLifecycleBadgeClass(state api.NotificationLifecycleState) string {
	switch state {
	case api.NotificationLifecycleStateResolved:
		return notificationBadgeSuccessOutline
	case api.NotificationLifecycleStateAcknowledged:
		return notificationBadgeWarningOutline
	case api.NotificationLifecycleStateOpen:
		return notificationBadgeErrorOutline
	default:
		return notificationBadgeOutline
	}
}

func notificationDispatchBadgeClass(status api.NotificationDispatchStatus) string {
	switch status {
	case api.NotificationDispatchStatusQueued:
		return notificationBadgeInfoOutline
	default:
		return notificationBadgeOutline
	}
}

func notificationDeliveryAttemptBadgeClass(status api.AlertDeliveryAttemptStatus) string {
	switch status {
	case api.AlertDeliveryAttemptStatusSent:
		return notificationBadgeSuccessOutline
	case api.AlertDeliveryAttemptStatusFailed:
		return notificationBadgeErrorOutline
	case api.AlertDeliveryAttemptStatusQueued:
		return notificationBadgeInfoOutline
	default:
		return notificationBadgeOutline
	}
}

func notificationTypeLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	for _, option := range allNotificationTypeOptions() {
		if value == option.Value {
			return option.Label
		}
	}
	value = strings.TrimPrefix(value, "pipeline.")
	value = strings.ReplaceAll(value, "_", " ")
	value = strings.ReplaceAll(value, ".", " ")
	return value
}

func notificationSeverityLabel(severity api.NotificationSeverity) string {
	return notificationCapitalizedLabel(string(severity))
}

func notificationLifecycleLabel(state api.NotificationLifecycleState) string {
	return notificationCapitalizedLabel(string(state))
}

func notificationDispatchStatusLabel(status api.NotificationDispatchStatus) string {
	return notificationCapitalizedLabel(string(status))
}

func notificationDeliveryAttemptStatusLabel(status api.AlertDeliveryAttemptStatus) string {
	if status == api.AlertDeliveryAttemptStatusSent {
		return "Sent (provider acknowledged)"
	}
	return notificationCapitalizedLabel(string(status))
}

func notificationDeliveryAttemptDestinationNameLabel(attempt api.AlertDeliveryAttempt) string {
	if attempt.DestinationName == nil || strings.TrimSpace(*attempt.DestinationName) == "" {
		return "-"
	}
	return *attempt.DestinationName
}

func notificationDeliveryAttemptDestinationSlugLabel(attempt api.AlertDeliveryAttempt) string {
	return notificationNullableSlugLabel(attempt.DestinationSlug)
}

func notificationCapitalizedLabel(value string) string {
	label := notificationTypeLabel(value)
	if label == "-" {
		return label
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

func notificationSourceLabel(notification api.NotificationRecord) string {
	entity := notificationTypeLabel(string(notification.SourceEntityType))
	action := notificationTypeLabel(string(notification.SourceAction))
	if entity == "-" && action == "-" {
		return "-"
	}
	if action == "-" {
		return entity
	}
	if entity == "-" {
		return action
	}
	return entity + " / " + action
}

func notificationNullableSlugLabel(value *api.NullableSlug) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "-"
	}
	return *value
}

// notificationOptionalTimestampLabel renders nullable lifecycle timestamps.
func notificationOptionalTimestampLabel(value *api.Timestamp) string {
	if value == nil || value.IsZero() {
		return "-"
	}
	return notificationTimestamp(*value)
}

// notificationLifecycleEvidenceRows returns display rows for acknowledged and resolved evidence.
func notificationLifecycleEvidenceRows(notification api.NotificationRecord) []notificationLifecycleEvidenceRow {
	rows := []notificationLifecycleEvidenceRow{}
	if notification.AcknowledgedAt != nil {
		rows = append(rows, notificationLifecycleEvidenceRow{
			Label:     "Acknowledged",
			Timestamp: notificationOptionalTimestampLabel(notification.AcknowledgedAt),
		})
	}
	if notification.ResolvedAt != nil {
		rows = append(rows, notificationLifecycleEvidenceRow{
			Label:     "Resolved",
			Timestamp: notificationOptionalTimestampLabel(notification.ResolvedAt),
		})
	}
	return rows
}

func notificationTimestamp(ts api.Timestamp) string {
	if ts.IsZero() {
		return "-"
	}
	return ts.UTC().Format(time.RFC3339)
}

func notificationOccurrenceLabel(count int) string {
	if count == 1 {
		return "1 occurrence"
	}
	return fmt.Sprintf("%d occurrences", count)
}

func notificationDeliveryAttemptCountLabel(count int) string {
	if count == 1 {
		return "1 recent attempt"
	}
	return fmt.Sprintf("%d recent attempts", count)
}

func notificationDeliveryAttemptTimestampLabel(attempt api.AlertDeliveryAttempt) string {
	if attempt.SentAt != nil && !attempt.SentAt.IsZero() {
		return "sent " + notificationTimestamp(*attempt.SentAt)
	}
	if attempt.FailedAt != nil && !attempt.FailedAt.IsZero() {
		return "failed " + notificationTimestamp(*attempt.FailedAt)
	}
	return "queued " + notificationTimestamp(attempt.QueuedAt)
}

func notificationMetadataJSON(metadata map[string]interface{}) string {
	if len(metadata) == 0 {
		return "{}"
	}
	payload, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func notificationSourceRefsJSON(refs map[string]string) string {
	if len(refs) == 0 {
		return "{}"
	}
	payload, err := json.MarshalIndent(refs, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func notificationEvidenceLinks(orgSlug string, notification api.NotificationRecord, permissions OrganizationPermissions) []notificationEvidenceLink {
	refs := notification.SourceRefs
	links := make([]notificationEvidenceLink, 0, 3)

	projectSlug := firstNotificationRef(refs, "project_slug", "project")
	environmentSlug := firstNotificationRef(refs, "environment_slug", "environment")
	runSlug := firstNotificationRef(refs, "run_slug", "pipeline_run_slug")
	pipelineSlug := firstNotificationRef(refs, "pipeline_slug", "pipeline")
	triggerSlug := firstNotificationRef(refs, "trigger_slug", "schedule_slug", "pipeline_trigger_slug")

	if permissions.Can("pipelines", "read") && projectSlug != "" && environmentSlug != "" && runSlug != "" {
		links = append(links, notificationEvidenceLink{
			Label: "Run detail",
			Href:  buildEnvironmentRunDetailHref(orgSlug, projectSlug, environmentSlug, runSlug),
		})
	}
	if permissions.Can("pipelines", "read") && projectSlug != "" && pipelineSlug != "" {
		if notification.NotificationType == api.NotificationTypePipelineFreshnessSLABreached && environmentSlug != "" {
			links = append(links, notificationEvidenceLink{
				Label: "Recency",
				Href:  RecencyHref(orgSlug, projectSlug, environmentSlug, pipelineSlug),
			})
		}
		links = append(links, notificationEvidenceLink{
			Label: "Pipeline",
			Href:  pipelineDetailHrefWithEnvironment(orgSlug, projectSlug, environmentSlug, pipelineSlug),
		})
		if firstNotificationRef(refs, "validation_result_slug") != "" && environmentSlug != "" {
			links = append(links, notificationEvidenceLink{
				Label: "Validation history",
				Href:  pipelineDetailHrefWithEnvironment(orgSlug, projectSlug, environmentSlug, pipelineSlug) + "&section=deployments#pipeline-validation",
			})
		}
	}
	if permissions.Can("pipeline_triggers", "read") && projectSlug != "" && triggerSlug != "" {
		links = append(links, notificationEvidenceLink{
			Label: "Schedule",
			Href:  scheduleSelectionHrefWithFilters(orgSlug, projectSlug, environmentSlug, triggerSlug, PipelineScheduleFilters{}),
		})
	}
	if permissions.Can("events", "read") && (notification.SourceEntityType != "" || notification.SourceAction != "") {
		links = append(links, notificationEvidenceLink{
			Label: "Activity",
			Href:  notificationActivityHref(orgSlug, notification),
		})
	}
	return links
}

func firstNotificationRef(refs map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(refs[key]); value != "" {
			return value
		}
	}
	return ""
}

func notificationProviderAttemptLabel(attempt int) string {
	return fmt.Sprintf("Provider attempt %d", attempt)
}

func NotificationReturnHref(org string, notification api.NotificationRecord, filters NotificationFilters) string {
	return NotificationsListHref(org, filters.Cursor, filters.Direction, filters) + "#notification-" + notification.Slug
}

func notificationConfigureAlertsHref(org string, notification api.NotificationRecord, filters NotificationFilters) string {
	values := url.Values{"tab": {string(AlertTabPolicies)}, "notification_return": {NotificationReturnHref(org, notification, filters)}}
	values.Set("notification_type", string(notification.NotificationType))
	if project := firstNotificationRef(notification.SourceRefs, "project_slug", "project"); project != "" {
		values.Set("project_slug", project)
		if environment := firstNotificationRef(notification.SourceRefs, "environment_slug", "environment"); environment != "" {
			values.Set("environment_slug", environment)
		}
		if notification.NotificationType == api.NotificationTypePipelineFreshnessSLABreached {
			values.Set("pipeline_slug", firstNotificationRef(notification.SourceRefs, "pipeline_slug", "pipeline"))
		}
	}
	return "/orgs/" + org + "/alerts?" + values.Encode()
}

func notificationSourceReturnHref(href, returnTo string) string {
	parsed, err := url.Parse(href)
	if err != nil {
		return href
	}
	values := parsed.Query()
	values.Set("notification_return", returnTo)
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func notificationActivityHref(org string, notification api.NotificationRecord) string {
	return buildEventsListHref(org, "", "", EventFilters{
		EntityType: string(notification.SourceEntityType),
		Action:     string(notification.SourceAction),
		EntitySlug: firstNotificationRef(notification.SourceRefs, "run_slug", "pipeline_run_slug"),
	})
}

func NotificationTypeAvailable(value string) bool {
	for _, option := range notificationTypeOptions(false) {
		if option.Value == value {
			return true
		}
	}
	return false
}
