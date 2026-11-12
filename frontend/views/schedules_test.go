package views

import (
	"bytes"
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestPipelineWebhookDeliveryHistoryRendersSafeDiagnosticsAndReplay(t *testing.T) {
	t.Parallel()

	failureKind := api.PipelineWebhookDeliveryFailureKind("platform")
	failureReason := "delivery_failed"
	receivedAt := time.Date(2026, time.August, 27, 10, 30, 0, 0, time.UTC)
	var out bytes.Buffer
	require.NoError(t, PipelineWebhookDeliveryHistory("acme", "etl", "prod", PipelineScheduleFilters{}, PipelineScheduleSelectedDetail{
		Slug:                 "webhook",
		DeliveriesHasMore:    true,
		DeliveriesNextCursor: "next-cursor",
		Deliveries: []api.PipelineWebhookDelivery{{
			DeliveryId:       "delivery-1",
			PipelineSlug:     "sync-users",
			TriggerSlug:      "webhook",
			ReceivedAt:       receivedAt,
			UpdatedAt:        receivedAt,
			Status:           api.PipelineWebhookDeliveryStatus("failed"),
			MappedInputNames: []string{"customer_id"},
			FailureKind:      &failureKind,
			FailureReason:    &failureReason,
			ReplayAvailable:  true,
		}},
	}).Render(context.Background(), &out))

	html := out.String()
	require.Contains(t, html, "delivery-1")
	require.Contains(t, html, "customer_id")
	require.Contains(t, html, "platform / delivery_failed")
	require.Contains(t, html, ">Replay</button>")
	require.Contains(t, html, `/orgs/acme/projects/etl/schedules/webhook/deliveries/delivery-1/replay`)
	require.Contains(t, html, `hx-get="/orgs/acme/projects/etl/schedules/webhook?environment_slug=prod&amp;fire_cursor=next-cursor&amp;fire_direction=next&amp;section=history"`)
	require.Contains(t, html, `disabled>Previous</button>`)
}

func TestPipelineWebhookDeliveryHistoryOnlyRendersSanitizedErrorDetails(t *testing.T) {
	t.Parallel()

	selected := PipelineScheduleSelectedDetail{
		DeliveryHistoryUnavailable:  true,
		DeliveryHistoryErrorSummary: "Webhook delivery history is unavailable.",
		DeliveryHistoryErrorDetails: "safe detail",
	}
	var out bytes.Buffer
	require.NoError(t, PipelineWebhookDeliveryHistory("acme", "etl", "prod", PipelineScheduleFilters{}, selected).Render(context.Background(), &out))
	require.NotContains(t, out.String(), "safe detail")

	selected.DeliveryHistoryErrorDetailsSanitized = true
	out.Reset()
	require.NoError(t, PipelineWebhookDeliveryHistory("acme", "etl", "prod", PipelineScheduleFilters{}, selected).Render(context.Background(), &out))
	require.Contains(t, out.String(), "safe detail")
}

func TestScheduleSelectionHrefPreservesEnvironmentAndFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		orgSlug         string
		projectSlug     string
		environmentSlug string
		triggerSlug     string
		filters         PipelineScheduleFilters
		wantPath        string
		wantQuery       map[string]string
	}{
		{
			name:            "PreservesEnvironmentAndFilters",
			orgSlug:         " acme ",
			projectSlug:     " ops ",
			environmentSlug: " prod ",
			triggerSlug:     " nightly-users ",
			filters: PipelineScheduleFilters{
				PipelineSlug: " sync-users ",
				Enabled:      "enabled",
			},
			wantPath: "/orgs/acme/projects/ops/schedules",
			wantQuery: map[string]string{
				"environment_slug": "prod",
				"trigger_slug":     "nightly-users",
				"pipeline_slug":    "sync-users",
				"enabled":          "enabled",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			href := scheduleSelectionHrefWithFilters(tt.orgSlug, tt.projectSlug, tt.environmentSlug, tt.triggerSlug, tt.filters)
			parsed, err := url.Parse(href)
			require.NoError(t, err)
			require.Equal(t, tt.wantPath, parsed.Path)
			for key, want := range tt.wantQuery {
				require.Equal(t, want, parsed.Query().Get(key))
			}
		})
	}
}

func TestFilterPipelineScheduleTriggers(t *testing.T) {
	t.Parallel()

	triggers := []api.PipelineTrigger{
		{Slug: "nightly-users", PipelineSlug: "sync-users", Enabled: true},
		{Slug: "paused-orders", PipelineSlug: "sync-orders", Enabled: false},
	}
	tests := []struct {
		name      string
		filters   PipelineScheduleFilters
		wantSlugs []api.Slug
	}{
		{
			name:      "FiltersPausedPipeline",
			filters:   PipelineScheduleFilters{PipelineSlug: "sync-orders", Enabled: "paused"},
			wantSlugs: []api.Slug{"paused-orders"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			filtered := FilterPipelineScheduleTriggers(triggers, tt.filters)
			require.Len(t, filtered, len(tt.wantSlugs))
			for i, wantSlug := range tt.wantSlugs {
				require.Equal(t, wantSlug, filtered[i].Slug)
			}
		})
	}
}

func TestScheduleFireStateLabels(t *testing.T) {
	t.Parallel()

	runSlug := api.NullableSlug("run-nightly-users")
	tests := []struct {
		name            string
		fire            api.PipelineScheduleFire
		wantMissingLink bool
		wantLabel       string
	}{
		{
			name:            "FailedMissingRunLink",
			fire:            api.PipelineScheduleFire{Status: api.PipelineScheduleFireStatusFailed},
			wantMissingLink: true,
			wantLabel:       "Missing run link",
		},
		{
			name:            "SkippedFire",
			fire:            api.PipelineScheduleFire{Status: api.PipelineScheduleFireStatusSkipped},
			wantMissingLink: false,
			wantLabel:       "Skipped fire",
		},
		{
			name:            "LinkedRun",
			fire:            api.PipelineScheduleFire{Status: api.PipelineScheduleFireStatusSucceeded, RunSlug: &runSlug},
			wantMissingLink: false,
			wantLabel:       "run-nightly-users",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.wantMissingLink, scheduleFireHasMissingRunLink(tt.fire))
			require.Equal(t, tt.wantLabel, scheduleFireRunLabel(tt.fire))
		})
	}
}

func TestTriggerViewsRenderSeparately(t *testing.T) {
	selected := PipelineScheduleSelectedDetail{Slug: "nightly", Trigger: &api.PipelineTrigger{Slug: "nightly", Name: "Nightly", Type: api.PipelineTriggerTypeSchedule, PipelineSlug: "sync"}}
	for _, section := range []string{"configuration", "history"} {
		t.Run(section, func(t *testing.T) {
			var out bytes.Buffer
			ctx := WithEntityPageURL(context.Background(), "/orgs/acme/projects/etl/schedules/nightly?environment_slug=prod&section="+section+"&list_return=%2Forgs%2Facme%2Fprojects%2Fetl%2Fschedules%3Fenabled%3Dpaused%26environment_slug%3Dprod%26trigger_slug%3Dnightly")
			require.NoError(t, PipelineScheduleDetailContent("acme", "etl", "prod", selected).Render(ctx, &out))
			html := out.String()
			require.Contains(t, html, "Trigger sections")
			require.Contains(t, html, "enabled=paused")
			if section == "configuration" {
				require.Contains(t, html, "Version policy")
				require.NotContains(t, html, "Scheduled-fire history")
			} else {
				require.Contains(t, html, "Scheduled-fire history")
				require.NotContains(t, html, "Version policy")
			}
		})
	}
	var out bytes.Buffer
	require.NoError(t, PipelineScheduleQuickLook("acme", "etl", "prod", PipelineScheduleFilters{Enabled: "paused"}, selected).Render(context.Background(), &out))
	require.Contains(t, out.String(), "Open full page")
	require.Contains(t, out.String(), "data-entity-preview")
	require.NotContains(t, out.String(), "Version policy")
	require.NotContains(t, out.String(), "Scheduled-fire history")
}

func TestTriggerHistoryLinksRetainScopeAndRestrictedReplay(t *testing.T) {
	t.Parallel()
	ctx := WithEntityPageURL(context.Background(), "/orgs/acme/projects/etl/schedules/webhook?list_return=%2Forgs%2Facme%2Fprojects%2Fetl%2Fschedules%3Fenabled%3Dpaused")
	href := scheduleHistoryHref(ctx, "acme", "etl", "prod", "webhook", "cursor+/=", "next", PipelineScheduleFilters{})
	parsed, err := url.Parse(href)
	require.NoError(t, err)
	require.Equal(t, "/orgs/acme/projects/etl/schedules/webhook", parsed.Path)
	require.Equal(t, "history", parsed.Query().Get("section"))
	require.Equal(t, "prod", parsed.Query().Get("environment_slug"))
	require.Equal(t, "cursor+/=", parsed.Query().Get("fire_cursor"))
	require.Equal(t, "/orgs/acme/projects/etl/schedules?enabled=paused", parsed.Query().Get("list_return"))
	reason := "Permission required"
	selected := PipelineScheduleSelectedDetail{Slug: "webhook", Deliveries: []api.PipelineWebhookDelivery{{DeliveryId: "failed", Status: api.PipelineWebhookDeliveryStatusFailed, ReplayUnavailableReason: &reason}}}
	var out bytes.Buffer
	require.NoError(t, PipelineWebhookDeliveryHistory("acme", "etl", "prod", PipelineScheduleFilters{}, selected).Render(ctx, &out))
	require.Contains(t, out.String(), reason)
	require.NotContains(t, out.String(), "<form")
}

func TestScheduleRetryTimingLabelMarksLegacyJitterUnsupported(t *testing.T) {
	t.Parallel()
	require.Equal(t, "initial 2s / max 30s / jitter unsupported", scheduleRetryTimingLabel(api.PipelineTriggerRetryPolicy{
		Enabled: true, InitialDelaySeconds: 2, MaxDelaySeconds: 30, Jitter: true,
	}))
}
