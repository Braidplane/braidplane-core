package views

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/runlineage"
	"github.com/stretchr/testify/require"
)

func TestRunInspectionStatesAndScopedNavigation(t *testing.T) {
	list := "/orgs/acme/projects/ops/runs?environment_slug=prod&status=failed&pipeline_slug=daily&mode=backfill&backfill_series_slug=series&cursor=a%2Bb&direction=prev"
	ctx := WithEntityPageURL(context.Background(), list)
	selected := entityListHref(ctx, "/orgs/acme/projects/ops/runs?environment_slug=prod", "run_slug", "run-1")
	u, err := url.Parse(selected)
	require.NoError(t, err)
	require.Equal(t, "a+b", u.Query().Get("cursor"))
	for _, key := range []string{"status", "pipeline_slug", "mode", "backfill_series_slug", "direction"} {
		original, _ := url.Parse(list)
		require.Equal(t, original.Query().Get(key), u.Query().Get(key))
	}
	for _, status := range []api.PipelineRunMetadataStatus{"queued", "running", "succeeded", "failed", "canceled"} {
		t.Run(string(status), func(t *testing.T) {
			ctx := WithEntityPageURL(context.Background(), list)
			summary := strings.Repeat("safe error <script> ", 100)
			run := api.PipelineRunRecord{Metadata: api.PipelineRunMetadata{RunSlug: "run-1", PipelineSlug: "daily", Status: status, PipelineVersionSpecHash: "exact-recorded-hash"}}
			if status == "failed" {
				run.Metadata.ErrorSummary = &summary
				run.Timeline.Failure = &api.PipelineRunFailureContext{Step: "write-users", Error: summary}
			}
			var preview bytes.Buffer
			require.NoError(t, PipelineRunDetailPanelSlot("acme", "ops", "prod", PipelineRunSelectedDetail{Slug: "run-1", Run: &run}).Render(ctx, &preview))
			html := preview.String()
			for _, absent := range []string{"Execution timeline", "Run lineage", "Trigger retry attempts", "<form", "exact-recorded-hash", "<script>"} {
				require.NotContains(t, html, absent)
			}
			require.Equal(t, 5, strings.Count(html, "<dt"))
			detail := entityDetailHref("/orgs/acme/projects/ops/runs/run-1?environment_slug=prod", selected)
			detailCtx := WithEntityPageURL(ctx, detail+"&lineage_step=write-users")
			require.Equal(t, selected, entityBackHref(detailCtx, "/orgs/acme/projects/ops/runs"))
			for _, section := range []string{"overview", "outcome", "steps", "outputs", "attempts", "metadata", "unknown"} {
				sectionCtx := WithEntityPageURL(detailCtx, detail+"&section="+section)
				var full bytes.Buffer
				require.NoError(t, PipelineRunDetailPageContent("acme", "ops", "prod", run, nil, true, "Permission required", runlineage.Unavailable("Version restricted"), DefaultOrganizationPermissions(), RunCompletionInspection{}).Render(sectionCtx, &full))
				html = full.String()
				active := section
				switch section {
				case "outcome", "metadata", "unknown":
					active = "overview"
				case "outputs":
					active = "steps"
				}
				for _, other := range []string{"outcome", "steps", "outputs", "attempts", "metadata"} {
					present := (active == "overview" && (other == "outcome" || other == "metadata")) || (active == "steps" && (other == "steps" || other == "outputs")) || (active == "attempts" && other == "attempts")
					require.Equal(t, present, strings.Contains(html, `id="run-`+other+`"`), "section %s, anchor %s", section, other)
				}
				require.NotContains(t, html, "<script>")
				require.Contains(t, html, `aria-current="page"`)
				require.Contains(t, html, `list_return=`)
				switch active {
				case "overview":
					if status == "failed" {
						require.Contains(t, html, "Failed at step write-users")
					}
				case "steps":
					require.Contains(t, html, "No step snapshots recorded")
					require.Contains(t, html, "No events recorded")
					require.Contains(t, html, "exact-recorded-hash")
					require.Contains(t, html, "Version restricted")
					require.Contains(t, html, "Sink effects unavailable")
				case "attempts":
					require.Contains(t, html, "Permission required")
				}
			}
		})
	}
	closed, err := url.Parse(entityListHref(ctx, list, "run_slug", ""))
	require.NoError(t, err)
	require.Equal(t, "closed", closed.Query().Get("preview"))
	require.Equal(t, "a+b", closed.Query().Get("cursor"))
	for _, invalid := range []string{"https://evil.test" + list, "/orgs/other/projects/ops/runs", "/orgs/acme/projects/other/runs"} {
		require.Equal(t, "/orgs/acme/projects/ops/runs", entityBackHref(WithEntityPageURL(context.Background(), "/detail?list_return="+url.QueryEscape(invalid)), "/orgs/acme/projects/ops/runs"))
	}
}

func TestRunSectionLinksPreserveExactSelection(t *testing.T) {
	const detail = "/orgs/acme/projects/ops/runs/run-one?environment_slug=prod"
	ctx := WithEntityPageURL(t.Context(), detail+"&lineage_step=join&definition_input=orders&definition_port=input&definition_view=serialized&spec_hash=executed-v1&list_return="+url.QueryEscape("/orgs/acme/projects/ops/runs?status=failed")+"&recovery_error=discard")
	for _, section := range []string{"overview", "steps", "attempts"} {
		next, err := url.Parse(runSectionHref(ctx, detail, section))
		require.NoError(t, err)
		require.Equal(t, section, next.Query().Get("section"))
		for key, expected := range map[string]string{"environment_slug": "prod", "lineage_step": "join", "definition_input": "orders", "definition_port": "input", "definition_view": "serialized", "spec_hash": "executed-v1", "list_return": "/orgs/acme/projects/ops/runs?status=failed"} {
			require.Equal(t, expected, next.Query().Get(key))
		}
		require.Empty(t, next.Query().Get("recovery_error"))
	}
	require.Equal(t, "steps", runDetailSection(ctx))
}

func TestRunSinkEffectsKeepIndependentAndMissingEvidence(t *testing.T) {
	kind := api.PipelineStepKindSink
	acknowledged := api.PipelineRunStepInputSnapshotSinkDeliveryOutcomeConfirmed
	unknown := api.PipelineRunStepInputSnapshotSinkDeliveryOutcomeUnknown
	run := api.PipelineRunRecord{Metadata: api.PipelineRunMetadata{Status: "succeeded"}, Timeline: api.PipelineRunTimeline{Snapshots: []api.PipelineRunStepSnapshot{
		{Step: "warehouse", Kind: &kind, Input: api.PipelineRunStepInputSnapshot{SinkDeliveryOutcome: &acknowledged}},
		{Step: "notify", Kind: &kind, Input: api.PipelineRunStepInputSnapshot{SinkDeliveryOutcome: &unknown}},
	}}}
	lineage := runlineage.View{Available: true, Nodes: []runlineage.Node{{Slug: "warehouse", Kind: "sink"}, {Slug: "notify", Kind: "sink"}, {Slug: "archive", Kind: "sink"}}}
	var out bytes.Buffer
	require.NoError(t, PipelineRunSinkDeliveryPanel(run, lineage).Render(t.Context(), &out))
	for _, expected := range []string{"warehouse", "Acknowledged by sink", "notify", "Unknown delivery", "archive", "Outcome unavailable", "not independent delivery readback"} {
		require.Contains(t, out.String(), expected)
	}
	require.Len(t, run.Timeline.Snapshots, 2)
}

func TestRunInspectionRendersResourceScopeRejection(t *testing.T) {
	t.Parallel()
	var run api.PipelineRunRecord
	require.NoError(t, json.Unmarshal([]byte(`{"metadata":{"run_slug":"scope-rejection","pipeline_slug":"orders","status":"failed"},"timeline":{"failure":{"step":"fetch","kind":"data_source","side":"source","reason_code":"resource_scope_rejected","error":"resource scope or selector is unsupported or outside the permitted scope","action_hint":"Use an unambiguous relative object key within the pinned scope, without traversal or absolute paths. Keep the bucket and target settings fixed to the pinned scope."}}}`), &run))
	var output bytes.Buffer
	for _, section := range []string{"outcome", "steps"} {
		ctx := WithEntityPageURL(t.Context(), "/orgs/acme/projects/ops/runs/scope-rejection?environment_slug=prod&section="+section)
		require.NoError(t, PipelineRunDetailPageContent("acme", "ops", "prod", run, nil, false, "", runlineage.View{}, DefaultOrganizationPermissions(), RunCompletionInspection{}).Render(ctx, &output))
	}
	require.Contains(t, output.String(), "Failed at step fetch")
	require.Contains(t, output.String(), "resource_scope_rejected")
	require.Contains(t, output.String(), run.Timeline.Failure.Error)
	require.Contains(t, output.String(), *run.Timeline.Failure.ActionHint)
}

func TestLineageMissingSelectionRetainsRequestedIdentity(t *testing.T) {
	var out bytes.Buffer
	ctx := WithEntityPageURL(t.Context(), "/runs/historical?lineage_step=deleted-step&section=steps")
	require.NoError(t, PipelineRunLineageEvidence(runlineage.View{SelectedIndex: -1}).Render(ctx, &out))
	require.Contains(t, out.String(), "Step deleted-step is unavailable in this executed definition.")
}

func TestLineageExplainsFailedCheckWithoutInventingStepOutcome(t *testing.T) {
	check := &api.PipelineRunStructuralCheck{Status: "failed", ContractFailure: &api.PipelineRunContractFailureDiagnostic{FieldName: "customer_id", Rule: "unknown"}}
	lineage := runlineage.View{SelectedIndex: 0, Nodes: []runlineage.Node{{Slug: "read-orders", Kind: "query", Boundary: "output", Status: runlineage.StatusUnknown, FailedCheck: check}}}
	var out bytes.Buffer
	require.NoError(t, PipelineRunLineageEvidence(lineage).Render(t.Context(), &out))
	require.Contains(t, out.String(), "Recorded contract check failed")
	require.Contains(t, out.String(), "Field customer_id · contract mismatch; precise rule not recorded")
	require.Contains(t, out.String(), "No step-level failure diagnostic was recorded.")
	require.Contains(t, out.String(), "Evidence unavailable")
	require.NotContains(t, out.String(), "Rule unknown")
}

func TestFailureDiagnosticShowsRetainedCauseAndUsefulNextStep(t *testing.T) {
	for _, tc := range []struct {
		name, reason, diagnostic, expected, hint string
	}{
		{"structural type mismatch", "source_result_shape_mismatch", "operation output obligation (post_query): query output contract does not match cached schema structure_type", "The field and expected/actual types were not recorded", "Compare the saved query result fields and value types"},
		{"historical S3", "s3_object_write_failed", "s3 object sink write failed", "No provider error code or response was recorded", "Reconcile the object at the provider before repeating the write"},
		{"retained S3 cause", "s3_object_write_failed", "S3 AccessDenied: access to the requested operation was denied.", "S3 AccessDenied", "Reconcile the object at the provider before repeating the write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reason := api.PipelineRunFailureReasonCode(tc.reason)
			genericHint := "The data source returned an unexpected error. Check the data source status and credentials."
			failure := &api.PipelineRunFailureContext{ReasonCode: &reason, Error: tc.diagnostic, ActionHint: &genericHint}
			var out bytes.Buffer
			require.NoError(t, PipelineRunFailureDiagnostic(failure).Render(t.Context(), &out))
			visible := strings.SplitN(out.String(), "<details", 2)[0]
			require.Contains(t, visible, tc.expected)
			require.Contains(t, visible, tc.hint)
			require.NotContains(t, visible, genericHint)
			require.Contains(t, out.String(), tc.diagnostic)
		})
	}
}

func TestFailureActionsOpenRelevantExecutedAndCurrentResources(t *testing.T) {
	for _, tc := range []struct{ reason, boundary, label string }{
		{"source_result_shape_mismatch", "output", "Compare executed output contract"},
		{"s3_object_write_failed", "", "Review executed S3 write"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			reason := api.PipelineRunFailureReasonCode(tc.reason)
			ctx := WithRunStepActions(t.Context(), RunStepActions{DataSourceHref: "/current-connection", QueryHref: "/current-query"})
			lineage := runlineage.View{SelectedIndex: 0, Nodes: []runlineage.Node{{Slug: "failed-step", Href: "/runs/historical?environment_slug=dev&spec_hash=executed-v1", Boundary: tc.boundary, Failure: &api.PipelineRunFailureContext{ReasonCode: &reason}}}}
			var out bytes.Buffer
			require.NoError(t, PipelineRunLineageEvidence(lineage).Render(ctx, &out))
			require.Contains(t, out.String(), tc.label)
			require.Contains(t, out.String(), "section=definition")
			require.Contains(t, out.String(), "spec_hash=executed-v1")
			require.Contains(t, out.String(), "lineage_step=failed-step")
			require.Contains(t, out.String(), "Review current connection")
			require.Contains(t, out.String(), "Review current saved query")
		})
	}
}
