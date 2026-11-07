package views

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
)

func TestNotificationsPageContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		render func(t *testing.T) string
		assert func(t *testing.T, body string)
	}{
		{
			name: "LabelsBadgesAndSkipsRunDetailWithoutEnvironment",
			render: func(t *testing.T) string {
				t.Helper()

				now := time.Date(2026, time.June, 30, 10, 15, 0, 0, time.UTC)
				queuedAt := now.Add(time.Minute)
				var out bytes.Buffer

				err := NotificationsPageContent(
					"acme-hq",
					[]api.NotificationRecord{{
						Slug:             "notif-001",
						NotificationType: api.NotificationTypePipelineRunFailed,
						Severity:         api.NotificationSeverityCritical,
						LifecycleState:   api.NotificationLifecycleStateResolved,
						Summary:          "Pipeline users-sync failed",
						SourceEntityType: api.EventEntityTypePipelineRun,
						SourceAction:     api.EventActionExecute,
						SourceRefs: map[string]string{
							"project_slug":  "ops",
							"run_slug":      "run-001",
							"pipeline_slug": "users-sync",
						},
						SafeMetadata: map[string]any{
							"summary": "Pipeline users-sync failed",
						},
						OccurrenceCount:  1,
						FirstSeenAt:      now.Add(-time.Hour),
						LastSeenAt:       now,
						LastOccurrenceAt: now,
						CreatedAt:        now.Add(-time.Hour),
						UpdatedAt:        now,
						DispatchStatuses: []api.NotificationDispatchSummary{{
							DestinationSlug: "ops-email",
							Status:          api.NotificationDispatchStatusQueued,
							QueuedAt:        &queuedAt,
						}},
						ResolvedAt: &now,
					}},
					map[string][]api.AlertDeliveryAttempt{
						"notif-001": {{
							Slug:                     "alert-delivery-001",
							NotificationRecordSlug:   "notif-001",
							AlertPolicySlug:          ptr.Ptr(api.NullableSlug("critical-pipeline-email")),
							DestinationSlug:          ptr.Ptr(api.NullableSlug("ops-email")),
							DestinationName:          ptr.Ptr("Operations Email"),
							DestinationType:          api.AlertDestinationTypeEmail,
							Status:                   api.AlertDeliveryAttemptStatusSent,
							ProviderAttempt:          1,
							ProviderResponseMetadata: map[string]any{},
							QueuedAt:                 now,
							SentAt:                   &queuedAt,
							CreatedAt:                now,
							UpdatedAt:                queuedAt,
						}},
					},
					false,
					"",
					"",
					NotificationFilters{State: NotificationFilterStateResolved},
					DefaultOrganizationPermissions(),
					"",
					"",
				).Render(context.Background(), &out)
				require.NoError(t, err)

				return out.String()
			},
			assert: func(t *testing.T, body string) {
				t.Helper()

				require.Contains(t, body, "Critical")
				require.Contains(t, body, "Resolved")
				require.Contains(t, body, "2026-06-30T10:15:00Z")
				require.Contains(t, body, "ops-email: Queued")
				require.Contains(t, body, "Alert deliveries")
				require.Contains(t, body, "Operations Email")
				require.Contains(t, body, "Sent")
				require.Contains(t, body, "run failed")
				require.NotContains(t, body, "Run detail")
				require.NotContains(t, body, `notif-001/acknowledge`)
				require.NotContains(t, body, `notif-001/resolve`)
				require.NotContains(t, body, "/orgs/acme-hq/projects/ops/runs/run-001")
				require.NotContains(t, body, "/orgs/acme-hq/projects/ops/pipelines/users-sync")
			},
		},
		{
			name: "ShowsActionsForOpenAndAcknowledgedNotifications",
			render: func(t *testing.T) string {
				t.Helper()

				now := time.Date(2026, time.June, 30, 10, 15, 0, 0, time.UTC)
				var out bytes.Buffer

				err := NotificationsPageContent(
					"acme-hq",
					[]api.NotificationRecord{
						{
							Slug:             "notif-open",
							NotificationType: api.NotificationTypePipelineRunFailed,
							Severity:         api.NotificationSeverityCritical,
							LifecycleState:   api.NotificationLifecycleStateOpen,
							Summary:          "Open failure",
							SourceEntityType: api.EventEntityTypePipelineRun,
							SourceAction:     api.EventActionExecute,
							SourceRefs:       map[string]string{},
							SafeMetadata:     map[string]any{},
							OccurrenceCount:  1,
							FirstSeenAt:      now.Add(-time.Hour),
							LastSeenAt:       now,
							LastOccurrenceAt: now,
							CreatedAt:        now.Add(-time.Hour),
							UpdatedAt:        now,
						},
						{
							Slug:             "notif-ack",
							NotificationType: api.NotificationTypePipelineRunFailed,
							Severity:         api.NotificationSeverityWarning,
							LifecycleState:   api.NotificationLifecycleStateAcknowledged,
							Summary:          "Acknowledged failure",
							SourceEntityType: api.EventEntityTypePipelineRun,
							SourceAction:     api.EventActionExecute,
							SourceRefs:       map[string]string{},
							SafeMetadata:     map[string]any{},
							OccurrenceCount:  1,
							FirstSeenAt:      now.Add(-time.Hour),
							LastSeenAt:       now,
							LastOccurrenceAt: now,
							AcknowledgedAt:   &now,
							CreatedAt:        now.Add(-time.Hour),
							UpdatedAt:        now,
						},
					},
					map[string][]api.AlertDeliveryAttempt{},
					false,
					"",
					"",
					NotificationFilters{State: NotificationFilterStateActive, Severity: "critical"},
					NewOrganizationPermissions("notifications:update"),
					"",
					"",
				).Render(context.Background(), &out)
				require.NoError(t, err)

				return out.String()
			},
			assert: func(t *testing.T, body string) {
				t.Helper()

				require.Contains(t, body, `notif-open/acknowledge`)
				require.Contains(t, body, `notif-open/resolve`)
				require.NotContains(t, body, `notif-ack/acknowledge`)
				require.Contains(t, body, `notif-ack/resolve`)
				require.Contains(t, body, `name="severity" value="critical"`)
				require.Contains(t, body, "2026-06-30T10:15:00Z")
			},
		},
		{
			name: "HidesActionsWithoutUpdatePermission",
			render: func(t *testing.T) string {
				t.Helper()

				now := time.Date(2026, time.June, 30, 10, 15, 0, 0, time.UTC)
				var out bytes.Buffer

				err := NotificationsPageContent(
					"acme-hq",
					[]api.NotificationRecord{{
						Slug:             "notif-open",
						NotificationType: api.NotificationTypePipelineRunFailed,
						Severity:         api.NotificationSeverityCritical,
						LifecycleState:   api.NotificationLifecycleStateOpen,
						Summary:          "Open failure",
						SourceEntityType: api.EventEntityTypePipelineRun,
						SourceAction:     api.EventActionExecute,
						SourceRefs:       map[string]string{},
						SafeMetadata:     map[string]any{},
						OccurrenceCount:  1,
						FirstSeenAt:      now.Add(-time.Hour),
						LastSeenAt:       now,
						LastOccurrenceAt: now,
						CreatedAt:        now.Add(-time.Hour),
						UpdatedAt:        now,
					}},
					map[string][]api.AlertDeliveryAttempt{},
					false,
					"",
					"",
					NotificationFilters{State: NotificationFilterStateActive},
					DefaultOrganizationPermissions(),
					"",
					"",
				).Render(context.Background(), &out)
				require.NoError(t, err)

				return out.String()
			},
			assert: func(t *testing.T, body string) {
				t.Helper()

				require.NotContains(t, body, `notif-open/acknowledge`)
				require.NotContains(t, body, `notif-open/resolve`)
				require.NotContains(t, body, ">Acknowledge</button>")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tc.assert(t, tc.render(t))
		})
	}
}

func TestNotificationsPageContent_EmptyStates(t *testing.T) {
	t.Parallel()
	for _, state := range []string{NotificationFilterStateActive, NotificationFilterStateResolved} {
		for _, extra := range []string{"", "type", "severity", "both"} {
			t.Run(state+"/"+extra, func(t *testing.T) {
				t.Parallel()
				filters := NotificationFilters{State: state}
				if extra == "type" || extra == "both" {
					filters.NotificationType = string(api.NotificationTypePipelineRetryExhausted)
				}
				if extra == "severity" || extra == "both" {
					filters.Severity = "critical"
				}
				var out bytes.Buffer
				require.NoError(t, NotificationsPageContent("acme-hq", nil, nil, false, "", "", filters, DefaultOrganizationPermissions(), "", "").Render(context.Background(), &out))
				body := out.String()
				if extra == "" {
					require.Contains(t, body, "No "+state+" notifications")
					require.NotContains(t, body, "No notifications match these filters")
					require.NotContains(t, body, ">Clear filters</a>")
					require.NotContains(t, body, "filters active")
					if state == NotificationFilterStateResolved {
						require.Contains(t, body, "manually resolve")
					}
				} else {
					require.Contains(t, body, "No notifications match these filters")
					require.Contains(t, body, "filters active")
					require.NotContains(t, body, "No "+state+" notifications")
				}
				resetHref := "/orgs/acme-hq/notifications"
				if state == NotificationFilterStateResolved {
					resetHref += "?state=resolved"
				}
				require.Contains(t, body, `href="`+resetHref+`" hx-get="`+resetHref+`" hx-target="#notifications-workspace" hx-select="#notifications-workspace" hx-swap="outerHTML show:none focus-scroll:false" hx-push-url="true" data-preserve-scroll>Reset</a>`)
				if extra != "" {
					require.Contains(t, body, `href="`+resetHref+`" hx-get="`+resetHref+`" hx-target="#notifications-workspace" hx-select="#notifications-workspace" hx-swap="outerHTML show:none focus-scroll:false" hx-push-url="true" data-preserve-scroll>Clear filters</a>`)
				}
			})
		}
	}
}

func TestSuccessfulRunPolicyOptionAndInformationalLifecycle(t *testing.T) {
	form := DefaultAlertPolicyFormData()
	form.NotificationType = string(api.NotificationTypePipelineRunSucceeded)
	var out bytes.Buffer
	require.NoError(t, AlertPolicySelectorFieldsBody("acme", "selectors", form, AlertPolicyDynamicOptions{}).Render(t.Context(), &out))
	require.Contains(t, out.String(), `value="pipeline.run_succeeded" selected`)
	require.Contains(t, out.String(), "Pipeline run succeeded")
	record := api.NotificationRecord{NotificationType: api.NotificationTypePipelineRunSucceeded, LifecycleState: api.NotificationLifecycleStateResolved}
	require.False(t, notificationCanAcknowledge(record))
	require.False(t, notificationCanResolve(record))
}

func TestNotificationDeliveryEvidenceStates(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, diagnostics, want, absent string
	}{
		{"missing", "", "No delivery evidence recorded", "Delivery evidence is unavailable"},
		{"denied", "Access denied", "Delivery evidence is unavailable", "No delivery evidence recorded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := NotificationsPageContent("acme", []api.NotificationRecord{{Slug: "notice", Summary: "Retries exhausted"}}, nil, false, "", "", NotificationFilters{}, DefaultOrganizationPermissions(), "", tc.diagnostics).Render(context.Background(), &out)
			require.NoError(t, err)
			require.Contains(t, out.String(), tc.want)
			require.NotContains(t, out.String(), tc.absent)
			require.Contains(t, out.String(), "Neither repairs the source nor retries delivery")
		})
	}
	require.Contains(t, notificationDeliveryAttemptStatusLabel(api.AlertDeliveryAttemptStatusSent), "provider acknowledged")
	require.Equal(t, "Provider attempt 2", notificationProviderAttemptLabel(2))
}

func TestNotificationEvidenceLinksCheckIndependentPermissions(t *testing.T) {
	t.Parallel()
	notification := api.NotificationRecord{
		SourceEntityType: api.EventEntityTypePipelineRun,
		SourceAction:     api.EventActionExecute,
		SourceRefs:       map[string]string{"project_slug": "orders", "environment_slug": "staging", "run_slug": "historical-run", "pipeline_slug": "sync", "trigger_slug": "daily"},
	}
	require.Empty(t, notificationEvidenceLinks("acme", notification, DefaultOrganizationPermissions()))
	links := notificationEvidenceLinks("acme", notification, NewOrganizationPermissions("pipelines:read"))
	require.Len(t, links, 2)
	require.Contains(t, links[0].Href, "historical-run")
	require.Contains(t, links[0].Href, "staging")
	links = notificationEvidenceLinks("acme", notification, NewOrganizationPermissions("events:read"))
	require.Len(t, links, 1)
	require.Equal(t, "Activity", links[0].Label)
	links = notificationEvidenceLinks("acme", notification, NewOrganizationPermissions("pipeline_triggers:read"))
	require.Len(t, links, 1)
	require.Equal(t, "Schedule", links[0].Label)
	notification.SourceEntityType = api.EventEntityTypePipeline
	notification.SourceAction = api.EventActionValidate
	notification.SourceRefs = map[string]string{"project_slug": "orders", "environment_slug": "staging", "pipeline_slug": "sync", "validation_result_slug": "validation-original"}
	require.Empty(t, notificationEvidenceLinks("acme", notification, DefaultOrganizationPermissions()))
	links = notificationEvidenceLinks("acme", notification, NewOrganizationPermissions("pipelines:read"))
	require.Len(t, links, 2)
	require.Equal(t, "Validation history", links[1].Label)
	require.Contains(t, links[1].Href, "/pipelines/sync?environment_slug=staging&section=deployments#pipeline-validation")
	notification.NotificationType = api.NotificationTypePipelineFreshnessSLABreached
	notification.SourceRefs = map[string]string{"project_slug": "orders", "environment_slug": "staging", "pipeline_slug": "sync"}
	require.Empty(t, notificationEvidenceLinks("acme", notification, DefaultOrganizationPermissions()))
	links = notificationEvidenceLinks("acme", notification, NewOrganizationPermissions("pipelines:read"))
	require.Equal(t, "Recency", links[0].Label)
	require.Equal(t, "/orgs/acme/projects/orders/pipelines/sync/environments/staging/recency", links[0].Href)
	require.Contains(t, notificationConfigureAlertsHref("acme", notification, NotificationFilters{}), "pipeline_slug=sync")
}

func TestNotificationJourneyPreservesIdentityAndIndependentlyGatesConfiguration(t *testing.T) {
	t.Parallel()
	notification := api.NotificationRecord{Slug: "notice-original", NotificationType: api.NotificationTypePipelineRetryExhausted, SourceRefs: map[string]string{"project_slug": "orders", "environment_slug": "staging", "run_slug": "original-run"}}
	filters := NotificationFilters{State: NotificationFilterStateResolved, Severity: "warning", Cursor: "older-records", Direction: "next"}
	href := notificationConfigureAlertsHref("acme", notification, filters)
	require.Contains(t, href, "project_slug=orders")
	require.Contains(t, href, "environment_slug=staging")
	require.Contains(t, NotificationReturnHref("acme", notification, filters), "cursor=older-records")
	require.Contains(t, NotificationReturnHref("acme", notification, filters), "#notification-notice-original")
	for _, permissions := range []OrganizationPermissions{DefaultOrganizationPermissions(), NewOrganizationPermissions("alert_policies:read"), NewOrganizationPermissions("alert_destinations:read")} {
		var out bytes.Buffer
		err := NotificationsPageContent("acme", []api.NotificationRecord{notification}, map[string][]api.AlertDeliveryAttempt{"notice-original": {{AlertPolicySlug: ptr.Ptr(api.NullableSlug("historical-policy")), DestinationSlug: ptr.Ptr(api.NullableSlug("historical-target")), Status: api.AlertDeliveryAttemptStatusSent}}}, false, "", "", filters, permissions, "", "").Render(context.Background(), &out)
		require.NoError(t, err)
		require.Equal(t, permissions.Can("alert_policies", "read"), strings.Contains(out.String(), ">Recorded policy: historical-policy</a>"))
		require.Equal(t, permissions.Can("alert_destinations", "read"), strings.Contains(out.String(), ">Recorded destination: historical-target</a>"))
		require.NotContains(t, out.String(), "recipients")
	}
	require.True(t, NotificationTypeAvailable(string(api.NotificationTypePipelineRunFailed)))
	require.False(t, NotificationTypeAvailable(string(api.NotificationTypePipelineFreshnessSLABreached)))
	require.True(t, NotificationTypeAvailable(string(api.NotificationTypePipelineRunSucceeded)))
}

func TestProducerBackedNotificationSelectorsShareCapabilities(t *testing.T) {
	t.Parallel()
	for _, kind := range model.ProducerBackedNotificationTypes() {
		form := DefaultAlertPolicyFormData()
		form.NotificationType = kind.String()
		var out bytes.Buffer
		ctx := WithOperationalCapabilities(t.Context(), &api.PipelineOperationalCapabilities{SuccessfulRunRecency: true})
		require.NoError(t, AlertPolicySelectorFieldsBody("acme", "selectors", form, AlertPolicyDynamicOptions{}).Render(ctx, &out))
		require.Contains(t, out.String(), `value="`+kind.String()+`" selected`)
		require.Equal(t, 1, strings.Count(out.String(), `value="pipeline.freshness_sla_breached"`))
	}
}
