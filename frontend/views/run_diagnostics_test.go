package views

import (
	"bytes"
	"strings"
	"testing"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/runlineage"
	"github.com/stretchr/testify/require"
)

func TestWebhookRunSourceRetainsSafeProvenance(t *testing.T) {
	t.Parallel()
	actor, event, retry := api.NullableSlug("external-actor"), api.NullableSlug("event-one"), api.NullableSlug("root-run")
	request, trace := "request-one", "trace-one"
	var source api.PipelineRunSourceSummary
	require.NoError(t, source.FromPipelineRunWebhookSourceSummary(api.PipelineRunWebhookSourceSummary{
		Type: "webhook", Summary: "Verified webhook", Entrypoint: "webhook", ActorType: "external", ActorSlug: &actor,
		SourceEventSlug: &event, RequestId: &request, TraceId: &trace, AttemptNumber: 2, RootRunSlug: "root-run", RetryOfRunSlug: &retry,
		ParameterKeys: []string{"event"}, Webhook: api.PipelineRunSourceWebhookSummary{
			TriggerSlug: "orders-hook", TriggerRevision: 3, DeliveryId: "delivery-one", VerificationStatus: "verified",
			PayloadDigest: strings.Repeat("a", 64), PayloadSizeBytes: 100,
		},
	}))
	view := apiRunSourceView(&source)
	require.Equal(t, "Verified webhook via webhook", runSourceLabel(view))
	require.Equal(t, "external external-actor", runSourceActorLabel(view))
	require.Equal(t, "event-one", runSourceEventLabel(view))
	require.Equal(t, "Request request-one / Trace trace-one", runSourceRequestLabel(view))
	require.Equal(t, "event", runSourceParameterKeysLabel(view))
	require.Equal(t, "attempt 2", runAttemptLabel(view))
	require.Equal(t, "Root root-run / retry of root-run", runRetryGroupLabel(view))
	require.True(t, runHasRetryContext(view))
	run := api.PipelineRunRecord{Metadata: api.PipelineRunMetadata{RunSlug: "webhook-run", PipelineSlug: "orders", Status: "succeeded", Source: &source}}
	var out bytes.Buffer
	require.NoError(t, PipelineRunDetailPageContent("acme", "ops", "dev", run, nil, false, "", runlineage.View{}, DefaultOrganizationPermissions(), RunCompletionInspection{}).Render(t.Context(), &out))
	for _, value := range []string{"Verified webhook via webhook", "external external-actor", "event-one", "orders-hook", "revision 3", "delivery-one", "verified", strings.Repeat("a", 64)} {
		require.Contains(t, out.String(), value)
	}
	require.NotContains(t, out.String(), "Source unavailable")
	require.NotContains(t, out.String(), "Actor unavailable")
}

func TestRunFailureLocationNeverInventsStep(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, step, kind, label string }{
		{"worker loss", "", "platform", "Platform failure"},
		{"unavailable step", "", "data_source", "Run failure"},
		{"step failure", "write", "data_source", "Failed at step write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := &api.PipelineRunFailureContext{Step: tc.step, Kind: api.PipelineRunFailureKind(tc.kind), Error: "safe summary"}
			run := api.PipelineRunRecord{Metadata: api.PipelineRunMetadata{RunSlug: "failed-run", PipelineSlug: "orders", Status: "failed"}, Timeline: api.PipelineRunTimeline{Failure: failure}}
			for _, section := range []string{"overview", "steps", "attempts"} {
				var out bytes.Buffer
				ctx := WithEntityPageURL(t.Context(), "/?section="+section)
				require.NoError(t, PipelineRunDetailPageContent("acme", "ops", "dev", run, nil, false, "", runlineage.View{}, DefaultOrganizationPermissions(), RunCompletionInspection{}).Render(ctx, &out))
				require.Contains(t, out.String(), tc.label)
				require.NotContains(t, out.String(), "failed at .")
				if tc.step == "" {
					require.NotContains(t, out.String(), "Inspect failing step")
				}
			}
		})
	}
}

func TestRunRecordedDurationIsQualifiedForActiveAndTerminalRuns(t *testing.T) {
	t.Parallel()
	for _, status := range []api.PipelineRunMetadataStatus{"queued", "running", "succeeded", "failed"} {
		run := api.PipelineRunRecord{Metadata: api.PipelineRunMetadata{RunSlug: "selected-run", PipelineSlug: "orders", Status: status, DurationMs: 11, PipelineVersionSpecHash: "executed-version"}}
		var out bytes.Buffer
		ctx := t.Context()
		if status == "running" {
			at := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
			progress := &api.PipelineRunProgress{Metadata: run.Metadata, ServerTime: at.Add(23 * time.Second), WorkerLiveness: "recent"}
			progress.Metadata.StartedAt = at
			ctx = WithRunProgress(ctx, RunProgressInspection{Evidence: progress})
		}
		require.NoError(t, PipelineRunDetailPageContent("acme", "ops", "dev", run, nil, false, "", runlineage.View{}, DefaultOrganizationPermissions(), RunCompletionInspection{}).Render(ctx, &out))
		if status == "running" {
			require.Contains(t, out.String(), "Elapsed 23.00 s")
		}
		require.Contains(t, out.String(), "Recorded duration 11 ms")
		require.Contains(t, out.String(), "executed-version")
		var preview bytes.Buffer
		require.NoError(t, PipelineRunDetailPanelSlot("acme", "ops", "dev", PipelineRunSelectedDetail{Slug: "selected-run", Run: &run}).Render(ctx, &preview))
		require.Contains(t, preview.String(), "Recorded duration")
		require.Contains(t, preview.String(), "11 ms")
	}
}
