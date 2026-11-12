package views

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

const (
	scheduleStateEnabled = "enabled"
	scheduleStatePaused  = "paused"
	scheduleUnknownLabel = "unknown"
)

// PipelineScheduleFilters captures schedule list filters preserved through HTMX links.
type PipelineScheduleFilters struct {
	PipelineSlug string
	Enabled      string
}

// PipelineScheduleSelectedDetail carries the right-panel schedule configuration and fire history.
type PipelineScheduleSelectedDetail struct {
	CompletionPreview                    *api.PipelineDeployment
	CompletionPreviewError               string
	Slug                                 string
	Trigger                              *api.PipelineTrigger
	Fires                                []api.PipelineScheduleFire
	FiresHasMore                         bool
	FiresNextCursor                      string
	FiresPrevCursor                      string
	FireCursor                           string
	FireDirection                        string
	FireHistoryUnavailable               bool
	FireHistoryErrorSummary              string
	FireHistoryErrorDetails              string
	FireHistoryErrorDetailsSanitized     bool
	Deliveries                           []api.PipelineWebhookDelivery
	DeliveriesHasMore                    bool
	DeliveriesNextCursor                 string
	DeliveriesPrevCursor                 string
	DeliveryHistoryUnavailable           bool
	DeliveryHistoryErrorSummary          string
	DeliveryHistoryErrorDetails          string
	DeliveryHistoryErrorDetailsSanitized bool
	DeliveryActionSuccess                string
	DeliveryActionError                  string
	ErrorStatus                          int
	ErrorSummary                         string
	ErrorDetails                         string
	ErrorDetailsSanitized                bool
}

func webhookDeliveryStatusBadgeClass(status api.PipelineWebhookDeliveryStatus) string {
	switch status {
	case api.PipelineWebhookDeliveryStatusSucceeded:
		return "badge badge-success"
	case api.PipelineWebhookDeliveryStatusFailed, api.PipelineWebhookDeliveryStatusRejected:
		return "badge badge-error"
	case api.PipelineWebhookDeliveryStatusRunning:
		return "badge badge-info"
	default:
		return "badge badge-warning"
	}
}

func webhookDeliveryRunHref(orgSlug, projectSlug, environmentSlug string, delivery api.PipelineWebhookDelivery) string {
	if delivery.LinkedRunSlug == nil || strings.TrimSpace(*delivery.LinkedRunSlug) == "" {
		return ""
	}
	return buildEnvironmentRunDetailHref(orgSlug, projectSlug, environmentSlug, *delivery.LinkedRunSlug)
}

func webhookDeliveryFailureLabel(delivery api.PipelineWebhookDelivery) string {
	kind := ""
	if delivery.FailureKind != nil {
		kind = strings.TrimSpace(string(*delivery.FailureKind))
	}
	reason := ""
	if delivery.FailureReason != nil {
		reason = strings.TrimSpace(*delivery.FailureReason)
	}
	if kind != "" && reason != "" {
		return kind + " / " + reason
	}
	if kind != "" {
		return kind
	}
	if reason != "" {
		return reason
	}
	return "—"
}

func webhookDeliveryInputsLabel(delivery api.PipelineWebhookDelivery) string {
	if len(delivery.MappedInputNames) == 0 {
		return "none"
	}
	return strings.Join(delivery.MappedInputNames, ", ")
}

func webhookDeliveryReplayAction(orgSlug, projectSlug, triggerSlug, deliveryID string) string {
	return "/orgs/" + url.PathEscape(orgSlug) + "/projects/" + url.PathEscape(projectSlug) +
		"/schedules/" + url.PathEscape(triggerSlug) + "/deliveries/" + url.PathEscape(deliveryID) + "/replay"
}

func scheduleFiltersActive(filters PipelineScheduleFilters) bool {
	return strings.TrimSpace(filters.PipelineSlug) != "" || strings.TrimSpace(filters.Enabled) != ""
}

func scheduleFilterChipLabel(filters PipelineScheduleFilters) string {
	parts := make([]string, 0, 2)
	if pipelineSlug := strings.TrimSpace(filters.PipelineSlug); pipelineSlug != "" {
		parts = append(parts, "pipeline "+pipelineSlug)
	}
	switch strings.TrimSpace(filters.Enabled) {
	case scheduleStateEnabled:
		parts = append(parts, scheduleStateEnabled)
	case scheduleStatePaused:
		parts = append(parts, scheduleStatePaused)
	}
	if len(parts) == 0 {
		return "filters active"
	}
	return strings.Join(parts, " / ")
}

// FilterPipelineScheduleTriggers applies SSR-only schedule filters to trigger definitions.
func FilterPipelineScheduleTriggers(triggers []api.PipelineTrigger, filters PipelineScheduleFilters) []api.PipelineTrigger {
	pipelineSlug := strings.TrimSpace(filters.PipelineSlug)
	enabledFilter := strings.TrimSpace(filters.Enabled)
	if pipelineSlug == "" && enabledFilter == "" {
		return triggers
	}

	result := make([]api.PipelineTrigger, 0, len(triggers))
	for _, trigger := range triggers {
		if pipelineSlug != "" && trigger.PipelineSlug != pipelineSlug {
			continue
		}
		if enabledFilter == scheduleStateEnabled && !trigger.Enabled {
			continue
		}
		if enabledFilter == scheduleStatePaused && trigger.Enabled {
			continue
		}
		result = append(result, trigger)
	}
	return result
}

func buildEnvironmentProjectSchedulesListHref(
	orgSlug string,
	projectSlug string,
	environmentSlug string,
	filters PipelineScheduleFilters,
) string {
	values := scheduleFilterQueryValues(environmentSlug, "", filters)
	base := projectSchedulesBaseHref(orgSlug, projectSlug)
	encoded := values.Encode()
	if encoded == "" {
		return base
	}
	return base + "?" + encoded
}

func scheduleSelectionHrefWithFilters(
	orgSlug string,
	projectSlug string,
	environmentSlug string,
	triggerSlug string,
	filters PipelineScheduleFilters,
) string {
	values := scheduleFilterQueryValues(environmentSlug, triggerSlug, filters)
	base := projectSchedulesBaseHref(orgSlug, projectSlug)
	encoded := values.Encode()
	if encoded == "" {
		return base
	}
	return base + "?" + encoded
}

func scheduleFilterQueryValues(environmentSlug, triggerSlug string, filters PipelineScheduleFilters) url.Values {
	values := url.Values{}
	if strings.TrimSpace(environmentSlug) != "" {
		values.Set("environment_slug", strings.TrimSpace(environmentSlug))
	}
	if strings.TrimSpace(triggerSlug) != "" {
		values.Set("trigger_slug", strings.TrimSpace(triggerSlug))
	}
	if strings.TrimSpace(filters.PipelineSlug) != "" {
		values.Set("pipeline_slug", strings.TrimSpace(filters.PipelineSlug))
	}
	if strings.TrimSpace(filters.Enabled) != "" {
		values.Set("enabled", strings.TrimSpace(filters.Enabled))
	}
	return values
}

func projectSchedulesBaseHref(orgSlug, projectSlug string) string {
	trimmedProjectSlug := strings.TrimSpace(projectSlug)
	if trimmedProjectSlug == "" {
		return "/organizations/" + strings.TrimSpace(orgSlug) + "/projects"
	}
	return "/orgs/" + strings.TrimSpace(orgSlug) + "/projects/" + trimmedProjectSlug + "/schedules"
}

// ProjectSchedulesListHrefWithEnvironment returns the public project schedules URL.
func ProjectSchedulesListHrefWithEnvironment(orgSlug, projectSlug, environmentSlug string) string {
	return buildEnvironmentProjectSchedulesListHref(orgSlug, projectSlug, environmentSlug, PipelineScheduleFilters{})
}

func scheduleTriggerListItemClass(triggerSlug, selectedTriggerSlug string) string {
	base := "bg-base-100"
	if strings.TrimSpace(triggerSlug) == strings.TrimSpace(selectedTriggerSlug) {
		return base + " border-primary/60 bg-primary/10"
	}
	return base + " border-base-300 hover:border-primary/40"
}

func scheduleEnabledBadgeClass(enabled bool) string {
	if enabled {
		return eventBadgeSuccess
	}
	return badgeWarningOutline
}

func scheduleEnabledLabel(enabled bool) string {
	if enabled {
		return scheduleStateEnabled
	}
	return scheduleStatePaused
}

func scheduleVersionPolicyLabel(policy api.PipelineTriggerResolvedVersionPolicy) string {
	mode := strings.TrimSpace(string(policy.Mode))
	if mode == "" {
		return scheduleUnknownLabel
	}
	if policy.PinnedVersionHash == nil || strings.TrimSpace(*policy.PinnedVersionHash) == "" {
		return mode
	}
	return mode + " / " + shortHash(*policy.PinnedVersionHash)
}

func scheduleRuntimeInputKeysLabel(keys []string) string {
	if len(keys) == 0 {
		return "No runtime input keys"
	}
	return strings.Join(keys, ", ")
}

func scheduleRetryPolicyLabel(policy api.PipelineTriggerRetryPolicy) string {
	if !policy.Enabled {
		return "Retries disabled"
	}
	return strconv.Itoa(policy.MaxAttempts) + " attempts / " + string(policy.BackoffStrategy)
}

func scheduleRetryTimingLabel(policy api.PipelineTriggerRetryPolicy) string {
	if !policy.Enabled {
		return "No retry timing"
	}
	parts := []string{
		"initial " + strconv.Itoa(policy.InitialDelaySeconds) + "s",
		"max " + strconv.Itoa(policy.MaxDelaySeconds) + "s",
	}
	if policy.MaxElapsedSeconds != nil {
		parts = append(parts, "elapsed "+strconv.Itoa(*policy.MaxElapsedSeconds)+"s")
	}
	return strings.Join(parts, " / ")
}

func scheduleRetryFailureKindsLabel(kinds []api.PipelineTriggerRetryFailureKind) string {
	if len(kinds) == 0 {
		return "No retryable failure kinds"
	}
	labels := make([]string, 0, len(kinds))
	for _, kind := range kinds {
		labels = append(labels, string(kind))
	}
	return strings.Join(labels, ", ")
}

func scheduleFireStatusBadgeClass(status api.PipelineScheduleFireStatus) string {
	switch status {
	case api.PipelineScheduleFireStatusSucceeded:
		return eventBadgeSuccess
	case api.PipelineScheduleFireStatusFailed:
		return eventBadgeError
	case api.PipelineScheduleFireStatusSkipped:
		return badgeGhostOutline
	case api.PipelineScheduleFireStatusClaimed, api.PipelineScheduleFireStatusRunning:
		return badgeWarningOutline
	default:
		return eventBadgeOutline
	}
}

func scheduleMisfireBadgeClass(result api.PipelineScheduleFireMisfireResult) string {
	switch result {
	case api.PipelineScheduleFireMisfireResultOnTime:
		return eventBadgeSuccess
	case api.PipelineScheduleFireMisfireResultSkipped:
		return badgeGhostOutline
	case api.PipelineScheduleFireMisfireResultFireOnce:
		return badgeWarningOutline
	default:
		return eventBadgeOutline
	}
}

func scheduleFireRunLabel(fire api.PipelineScheduleFire) string {
	if fire.RunSlug != nil && strings.TrimSpace(*fire.RunSlug) != "" {
		return strings.TrimSpace(*fire.RunSlug)
	}
	if fire.Status == api.PipelineScheduleFireStatusSkipped {
		return "Skipped fire"
	}
	if scheduleFireHasMissingRunLink(fire) {
		return "Missing run link"
	}
	return "No run yet"
}

func scheduleFireSourceEventLabel(fire api.PipelineScheduleFire) string {
	if fire.SourceEventSlug == nil || strings.TrimSpace(*fire.SourceEventSlug) == "" {
		return "No source event"
	}
	return strings.TrimSpace(*fire.SourceEventSlug)
}

func scheduleFireErrorLabel(fire api.PipelineScheduleFire) string {
	if fire.ErrorSummary == nil || strings.TrimSpace(*fire.ErrorSummary) == "" {
		return "-"
	}
	return strings.TrimSpace(*fire.ErrorSummary)
}

func scheduleFireHasMissingRunLink(fire api.PipelineScheduleFire) bool {
	if fire.RunSlug != nil && strings.TrimSpace(*fire.RunSlug) != "" {
		return false
	}
	return fire.Status == api.PipelineScheduleFireStatusSucceeded || fire.Status == api.PipelineScheduleFireStatusFailed
}

func scheduleFireRunHref(orgSlug, projectSlug, environmentSlug string, fire api.PipelineScheduleFire) string {
	if fire.RunSlug == nil || strings.TrimSpace(*fire.RunSlug) == "" {
		return ""
	}
	return buildEnvironmentRunDetailHref(orgSlug, projectSlug, environmentSlug, strings.TrimSpace(*fire.RunSlug))
}

func schedulePipelineHref(orgSlug, projectSlug, environmentSlug string, trigger api.PipelineTrigger) string {
	href := pipelineDetailHref(orgSlug, projectSlug, trigger.PipelineSlug)
	if strings.TrimSpace(environmentSlug) == "" {
		return href
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return href
	}
	values := parsed.Query()
	values.Set("environment_slug", strings.TrimSpace(environmentSlug))
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func shortHash(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= 12 {
		return trimmed
	}
	return trimmed[:12]
}

func scheduleDetailHref(orgSlug, projectSlug, environmentSlug, triggerSlug string) string {
	return projectSchedulesBaseHref(orgSlug, projectSlug) + "/" + url.PathEscape(triggerSlug) + "?" + url.Values{"environment_slug": {environmentSlug}}.Encode()
}

func scheduleDetailSection(ctx context.Context) string {
	if entityPageURL(ctx).Query().Get("section") == "history" {
		return "history"
	}
	return "configuration"
}

func scheduleHistoryHref(ctx context.Context, orgSlug, projectSlug, environmentSlug, triggerSlug, cursor, direction string, _ PipelineScheduleFilters) string {
	u, _ := url.Parse(entitySectionHref(ctx, scheduleDetailHref(orgSlug, projectSlug, environmentSlug, triggerSlug), "history"))
	values := u.Query()
	values.Set("fire_cursor", cursor)
	values.Set("fire_direction", direction)
	u.RawQuery = values.Encode()
	return u.String()
}
