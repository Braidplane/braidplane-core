package views

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func TestRunViewHelpersFormatStatusTimelineAndFailureContext(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"badge badge-success badge-outline",
		runStatusBadgeClass(api.PipelineRunMetadataStatusSucceeded),
	)
	require.Equal(
		t,
		"badge badge-error badge-outline",
		runStatusBadgeClass(api.PipelineRunMetadataStatusFailed),
	)
	require.Equal(t, "badge badge-info badge-outline", runStatusBadgeClass("running"))

	ts := time.Date(2026, 5, 14, 12, 30, 0, 0, time.FixedZone("CEST", 2*60*60))
	require.Equal(t, "2026-05-14T10:30:00Z", formatRunTimestamp(ts))
	require.Equal(t, "-", formatRunTimestamp(api.Timestamp{}))
	nullable := ts
	require.Equal(t, "2026-05-14T10:30:00Z", formatNullableRunTimestamp(&nullable))
	require.Equal(t, "-", formatNullableRunTimestamp(nil))

	require.Equal(t, "999 ms", runDurationLabel(999))
	require.Equal(t, "1.25 s", runDurationLabel(1250))

	require.Equal(t, "Run started", runEventTypeLabel(eventTypeRunStarted))
	require.Equal(t, "Run completed", runEventTypeLabel(eventTypeRunCompleted))
	require.Equal(t, "Step started", runEventTypeLabel(eventTypeStepStarted))
	require.Equal(t, "Step failed", runEventTypeLabel(eventTypeStepFailed))
	require.Equal(t, "custom", runEventTypeLabel("custom"))

	require.Equal(t, "badge badge-info badge-sm badge-outline", runEventBadgeClass(eventTypeRunStarted, ""))
	require.Equal(
		t,
		"badge badge-success badge-sm badge-outline",
		runEventBadgeClass(eventTypeRunCompleted, "succeeded"),
	)
	require.Equal(
		t,
		"badge badge-error badge-sm badge-outline",
		runEventBadgeClass(eventTypeRunCompleted, statusFailed),
	)
	require.Equal(t, "badge badge-error badge-sm badge-outline", runEventBadgeClass(eventTypeStepFailed, ""))
	require.Equal(t, "badge badge-ghost badge-sm badge-outline", runEventBadgeClass("custom", ""))

	require.Equal(t, "h-3 w-3 rounded-full bg-info", runTimelineDotClass(eventTypeRunStarted, ""))
	require.Equal(
		t,
		"h-3 w-3 rounded-full bg-success",
		runTimelineDotClass(eventTypeRunCompleted, "succeeded"),
	)
	require.Equal(
		t,
		"h-3 w-3 rounded-full bg-error",
		runTimelineDotClass(eventTypeRunCompleted, statusFailed),
	)
	require.Equal(t, "h-3 w-3 rounded-full bg-error", runTimelineDotClass(eventTypeStepFailed, ""))
	require.Equal(t, "h-3 w-3 rounded-full bg-base-300", runTimelineDotClass("custom", ""))

	require.Equal(t, "Data source error", failureKindLabel(failureKindDataSource))
	require.Equal(t, "Platform error", failureKindLabel(failureKindPlatform))
	require.Equal(t, "custom", failureKindLabel("custom"))
	require.Equal(t, "badge badge-sm badge-warning", failureKindBadgeClass(failureKindDataSource))
	require.Equal(t, "badge badge-sm badge-error", failureKindBadgeClass(failureKindPlatform))
	require.Equal(t, "badge badge-sm badge-ghost", failureKindBadgeClass("custom"))
	require.Equal(t, "Source step", failureSideLabel(failureSideSource))
	require.Equal(t, "Sink step", failureSideLabel(failureSideSink))
	require.Equal(t, "Transform step", failureSideLabel(failureSideTransform))
	require.Equal(t, "Connector", failureSideLabel(failureSideConnector))
	require.Equal(t, "custom", failureSideLabel("custom"))
}

func TestRunViewHelpersBuildLinksAndFindSnapshots(t *testing.T) {
	t.Parallel()

	require.Equal(
		t,
		"/orgs/acme/projects/ops/runs",
		buildRunsListHrefWithProject(" acme ", " ops ", "", "", "", "", "", ""),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/runs?backfill_series_slug=series-001&cursor=next&direction=after&mode=backfill&pipeline_slug=users-sync&status=failed",
		buildRunsListHrefWithProject(" acme ", " ops ", "next", "after", "failed", "users-sync", "backfill", "series-001"),
	)
	require.Equal(
		t,
		"/organizations/acme/projects",
		buildRunsListHrefWithProject(" acme ", "", "next", "after", "failed", "users-sync", "", ""),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/runs?environment_slug=prod",
		buildEnvironmentRunsListHrefWithProject(" acme ", " ops ", " prod ", "", "", "", ""),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/runs?cursor=next&direction=next&environment_slug=prod&pipeline_slug=users-sync&status=failed",
		buildEnvironmentRunsListHrefWithProject(" acme ", " ops ", "prod", "next", "next", "failed", "users-sync"),
	)
	require.Equal(
		t,
		"/organizations/acme/projects",
		buildEnvironmentRunsListHrefWithProject(" acme ", "", "prod", "next", "next", "failed", "users-sync"),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/pipelines?cursor=page-2&direction=next&environment_slug=prod",
		buildEnvironmentProjectPipelinesListHref(" acme ", " ops ", " prod ", "page-2", "next"),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/pipelines?cursor=page-2&deployment=deployed&direction=next&draft=draft&environment_slug=prod&q=orders&run_status=failed",
		buildEnvironmentProjectPipelinesListHrefWithFilters(" acme ", " ops ", " prod ", "page-2", "next", PipelineCatalogFilters{
			Query:           " orders ",
			DraftState:      "draft",
			DeploymentState: "deployed",
			RunStatus:       "failed",
		}),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/pipelines?cursor=page-2&deployment=deployed&direction=next&environment_slug=prod&pipeline_slug=users-sync&q=orders",
		pipelineSelectionHrefWithFilters(" acme ", " ops ", " prod ", " users-sync ", PipelineCatalogFilters{
			Query:           " orders ",
			DeploymentState: "deployed",
			Cursor:          " page-2 ",
			Direction:       "next",
		}),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/runs/run-001?environment_slug=prod",
		buildEnvironmentRunDetailHref(" acme ", " ops ", " prod ", " run-001 "),
	)
	require.Equal(
		t,
		"/organizations/acme/projects",
		buildEnvironmentRunDetailHref(" acme ", "", "prod", " run-001 "),
	)

	run := api.PipelineRunRecord{
		Timeline: api.PipelineRunTimeline{
			Snapshots: []api.PipelineRunStepSnapshot{
				{Step: "fetch-users"},
				{Step: "write-warehouse"},
			},
		},
	}
	require.Equal(t, "write-warehouse", runStepSnapshot(run, "write-warehouse").Step)
	require.Nil(t, runStepSnapshot(run, "missing-step"))
}

func TestPipelineViewHelpersFormatLinksBadgesAndNullableFields(t *testing.T) {
	t.Parallel()

	require.Equal(t, PipelineUpdateFormData{}, DefaultPipelineUpdateFormData())
	require.Equal(t, PipelineUpdateFormData{Name: "Users Sync"}, PipelineUpdateFormDataFromPipeline(api.Pipeline{
		Name: "Users Sync",
	}))

	require.Equal(
		t,
		"/builder?draft_name=Users+Sync&draft_slug=users-sync&project_slug=ops&target_org_slug=acme",
		PipelineBuilderDraftHref("acme", " ops ", " Users Sync ", " users-sync "),
	)
	require.Equal(
		t,
		"/builder?draft_name=Users+Sync&draft_slug=users-sync&project_slug=ops&target_environment_slug=prod&target_org_slug=acme",
		PipelineBuilderDraftHrefWithEnvironment("acme", " ops ", " prod ", " Users Sync ", " users-sync "),
	)
	require.Equal(
		t,
		"/builder?org_slug=acme&pipeline_slug=users-sync&project_slug=ops",
		pipelineBuilderHref(" acme ", " ops ", " users-sync "),
	)
	require.Equal(
		t,
		"/builder?org_slug=acme&pipeline_slug=users-sync&project_slug=ops&target_environment_slug=prod",
		pipelineBuilderHrefWithEnvironment(" acme ", " ops ", " prod ", " users-sync "),
	)
	require.Equal(
		t,
		"/organizations/acme/projects",
		PipelineDetailHref(" acme ", "", " users-sync "),
	)
	require.Equal(
		t,
		"/organizations/acme/projects",
		pipelineDetailHrefWithEnvironment(" acme ", "", " prod ", " users-sync "),
	)
	require.Equal(
		t,
		"/organizations/acme/projects",
		PipelineDeploymentDetailHrefWithEnvironment(" acme ", "", " prod ", " users-sync "),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/pipelines/users-sync?environment_slug=prod",
		pipelineDetailHrefWithEnvironment(" acme ", " ops ", " prod ", " users-sync "),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/pipelines/users-sync/deployment?environment_slug=prod",
		PipelineDeploymentDetailHrefWithEnvironment(" acme ", " ops ", " prod ", " users-sync "),
	)
	require.Equal(
		t,
		"/organizations/acme/projects",
		ProjectPipelinesListHref(" acme ", "", "next", "after"),
	)
	require.Equal(
		t,
		"/organizations/acme/projects",
		pipelineRunsHrefWithProject(" acme ", "", " users-sync "),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/runs",
		pipelineRunsHrefWithProject(" acme ", " ops ", ""),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/runs?pipeline_slug=users-sync",
		pipelineRunsHrefWithProject(" acme ", " ops ", " users-sync "),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/pipelines",
		ProjectPipelinesListHref(" acme ", " ops ", "", ""),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/pipelines?cursor=next&direction=after",
		ProjectPipelinesListHref(" acme ", " ops ", "next", "after"),
	)
	require.Equal(
		t,
		"/orgs/acme/projects/ops/pipelines/users-sync",
		pipelineActionHref(" acme ", " ops ", " users-sync ", ""),
	)
	require.Equal(t, "badge badge-warning badge-outline", pipelineSyncStatusBadgeClass(api.Drifted))
	require.Equal(t, "badge badge-success badge-outline", pipelineSyncStatusBadgeClass(api.Synced))
	require.Equal(t, "badge badge-primary badge-outline", pipelineDraftBadgeClass(true))
	require.Equal(t, "badge badge-ghost badge-outline", pipelineDraftBadgeClass(false))
	require.Equal(t, "drifted", pipelineSyncStatusLabel(api.Drifted))
	require.Equal(t, "synced", pipelineSyncStatusLabel(api.Synced))
	require.Equal(t, "draft saved", pipelineDraftBadgeLabel(true))
	require.Equal(t, "no draft", pipelineDraftBadgeLabel(false))
	require.Equal(t, "badge badge-error badge-outline", pipelineDeploymentBadgeClass(" red "))
	require.Equal(t, "badge badge-warning badge-outline", pipelineDeploymentBadgeClass("amber"))
	require.Equal(t, "badge badge-info badge-outline", pipelineDeploymentBadgeClass("blue"))
	require.Equal(t, "badge badge-success badge-outline", pipelineDeploymentBadgeClass("green"))
	require.Equal(t, "deployment metadata unavailable", pipelineDeploymentStateLabel(nil, true))
	require.Equal(t, "not deployed", pipelineDeploymentStateLabel(nil, false))
	require.Equal(t, "1 environment deployed", pipelineDeploymentStateLabel([]PipelineDeploymentBadge{{EnvironmentSlug: "prod"}}, false))
	ts := time.Date(2026, 5, 14, 12, 30, 0, 0, time.FixedZone("CEST", 2*60*60))
	deploymentsByPipeline := PipelineDeploymentBadgesByPipeline(
		[]api.PipelineDeployment{
			{
				PipelineSlug:                "users-sync",
				EnvironmentSlug:             "prod",
				CurrentVersionShortSpecHash: "abc1234",
				CurrentVersionSpecHash:      "abc1234def5678",
				DeployedAt:                  ts,
				DeployedByActorType:         api.EventActorTypeSystem,
				ProjectSlug:                 "ops",
				UpdatedAt:                   ts,
			},
		},
		map[string]string{"prod": "red"},
	)
	require.Equal(t, []PipelineDeploymentBadge{
		{
			PipelineSlug:     "users-sync",
			EnvironmentSlug:  "prod",
			EnvironmentColor: "red",
			ShortSpecHash:    "abc1234",
			FullSpecHash:     "abc1234def5678",
			DeployedAt:       "2026-05-14T10:30:00Z",
		},
	}, pipelineDeploymentBadgesFor(deploymentsByPipeline, " users-sync "))
	require.Nil(t, pipelineDeploymentBadgesFor(deploymentsByPipeline, "missing-pipeline"))

	unmappedDeploymentsByPipeline := PipelineDeploymentBadgesByPipeline(
		[]api.PipelineDeployment{
			{
				PipelineSlug:                "users-sync",
				EnvironmentSlug:             "retired-prod",
				CurrentVersionShortSpecHash: "abc1234",
				CurrentVersionSpecHash:      "abc1234def5678",
				DeployedAt:                  ts,
				DeployedByActorType:         api.EventActorTypeSystem,
				ProjectSlug:                 "ops",
				UpdatedAt:                   ts,
			},
		},
		map[string]string{"prod": "red"},
	)
	unmappedBadges := pipelineDeploymentBadgesFor(unmappedDeploymentsByPipeline, "users-sync")
	require.Len(t, unmappedBadges, 1)
	require.Equal(t, "retired-prod", unmappedBadges[0].EnvironmentSlug)
	require.Empty(t, unmappedBadges[0].EnvironmentColor)
	require.Equal(t, "badge badge-ghost badge-outline", pipelineDeploymentBadgeClass(unmappedBadges[0].EnvironmentColor))

	require.Equal(t, "2026-05-14T10:30:00Z", formatPipelineTimestamp(ts))
	require.Equal(t, "-", formatPipelineTimestamp(api.Timestamp{}))
	require.Equal(t, "-", nullablePipelineField(nil))
	blank := " "
	value := "value"
	require.Equal(t, "-", nullablePipelineField(&blank))
	require.Equal(t, "value", nullablePipelineField(&value))
	require.Equal(t, "-", formatPipelineVersionSpecHash(nil))
	require.Equal(t, "-", formatPipelineVersionSpecHash(&blank))
	require.Equal(t, "value", formatPipelineVersionSpecHash(&value))
	require.Equal(t, "-", formatPipelineDraftUpdatedAt(nil))
	require.Equal(t, "2026-05-14T10:30:00Z", formatPipelineDraftUpdatedAt(&api.PipelineDraft{
		UpdatedAt: ts,
	}))
	require.Equal(t, "no version", shortPipelineVersionSpecHash(nil))
	hash := "abc1234567890def"
	require.Equal(t, "abc123456789", shortPipelineVersionSpecHash(&hash))
	require.Equal(t, hash, pipelineVersionTitle(&hash))
	require.Equal(t, "No draft", pipelineDetailDraftLabel(&api.Pipeline{}))
	require.Equal(
		t,
		"Draft saved after canonical update",
		pipelineDetailDraftLabel(&api.Pipeline{UpdatedAt: ts, Draft: &api.PipelineDraft{UpdatedAt: ts.Add(time.Minute)}}),
	)
	require.Equal(
		t,
		"This UI draft is working state for a Git-authored pipeline. Publishing proposes a Git change; deployment remains separate.",
		pipelineDetailDraftDescription(&api.Pipeline{Draft: &api.PipelineDraft{}}),
	)

	latestRuns := PipelineLatestRunsByPipeline([]api.PipelineRunMetadata{
		{PipelineSlug: "users-sync", EnvironmentSlug: "prod", RunSlug: "run-001", Status: api.PipelineRunMetadataStatusFailed, CreatedAt: ts, StartedAt: ts, DurationMs: 1250},
		{PipelineSlug: "users-sync", EnvironmentSlug: "prod", RunSlug: "run-older", Status: api.PipelineRunMetadataStatusSucceeded, CreatedAt: ts.Add(-time.Hour), StartedAt: ts.Add(-time.Hour), DurationMs: 1000},
		{PipelineSlug: "users-sync", EnvironmentSlug: "prod", RunSlug: "run-newer", Status: api.PipelineRunMetadataStatusFailed, CreatedAt: ts.Add(time.Minute), StartedAt: ts.Add(time.Minute), DurationMs: 1500},
		{PipelineSlug: "orders-export", EnvironmentSlug: "prod", RunSlug: "run-100", Status: api.PipelineRunMetadataStatusSucceeded, CreatedAt: ts, StartedAt: ts, DurationMs: 500},
		{PipelineSlug: "orders-export", EnvironmentSlug: "prod", RunSlug: "run-101", Status: api.PipelineRunMetadataStatusSucceeded, CreatedAt: ts, StartedAt: ts, DurationMs: 600},
		{PipelineSlug: "orders-export", EnvironmentSlug: "prod", RunSlug: "run-created-latest", Status: api.PipelineRunMetadataStatusSucceeded, CreatedAt: ts.Add(time.Minute), StartedAt: ts.Add(-time.Hour), DurationMs: 700},
	})
	require.Equal(t, "run-newer", latestRuns["users-sync"].RunSlug)
	require.Equal(t, "run-created-latest", latestRuns["orders-export"].RunSlug)
	require.Equal(t, "failed", pipelineLatestRunStatusLabel(latestRuns["users-sync"], true, false))
	require.Equal(t, "2026-05-14T10:31:00Z / 1.50 s", pipelineLatestRunTimingLabel(latestRuns["users-sync"]))

	rows := PipelineCatalogRows(
		[]api.PipelineRef{
			{Slug: "users-sync", Name: "Users Sync", SyncStatus: api.Synced, HasDraft: true, CurrentVersionSpecHash: &hash},
			{Slug: "orders-export", Name: "Orders Export", SyncStatus: api.Drifted},
		},
		map[string][]PipelineDeploymentBadge{
			"users-sync": {
				{EnvironmentSlug: "prod", ShortSpecHash: "abc1234"},
			},
		},
		latestRuns,
		PipelineCatalogFilters{DraftState: "draft", DeploymentState: "deployed", RunStatus: "failed"},
	)
	require.Len(t, rows, 1)
	require.Equal(t, "users-sync", rows[0].Pipeline.Slug)
}

func TestPipelineValidationHistoryPanelRendersDiagnosticEvidence(t *testing.T) {
	t.Parallel()

	stepSlug := api.NullableSlug("fetch-users")
	dataSourceRefSlug := api.NullableSlug("orders-db")
	dataSourceRef := api.NullableSlug("orders-db")
	resolvedDataSourceSlug := api.NullableSlug("orders-prod")
	environmentSlug := api.NullableSlug("prod")
	dataSourceType := api.DataSourceTypePostgres
	requiredCapability := api.DataSourceRefCapabilityPipelineSource
	specHash := "6d7b6f5f6c8d9b77f7a4a6c8d8b9f2a1b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8"
	result := api.PipelineValidationResultRecord{
		ValidationSlug:  "validation-acme-users-sync-a1b2c3",
		ProjectSlug:     "default",
		EnvironmentSlug: "prod",
		PipelineSlug:    "acme-users-sync",
		Source:          api.PipelineValidationResultSourceRevalidate,
		Valid:           false,
		SpecHash:        &specHash,
		CreatedAt:       time.Date(2026, 6, 27, 14, 45, 0, 0, time.UTC),
		UpdatedAt:       time.Date(2026, 6, 27, 14, 45, 0, 0, time.UTC),
		Diagnostics: []api.PipelineValidationDiagnostic{{
			Severity:               api.PipelineValidationDiagnosticSeverityError,
			Code:                   "PIPELINE_DATA_SOURCE_REF_MISSING_BINDING",
			Field:                  "steps[0].query.data_source_ref",
			StepSlug:               &stepSlug,
			DataSourceRef:          &dataSourceRef,
			DataSourceRefSlug:      &dataSourceRefSlug,
			ResolvedDataSourceSlug: &resolvedDataSourceSlug,
			EnvironmentSlug:        &environmentSlug,
			DataSourceType:         &dataSourceType,
			RequiredCapability:     &requiredCapability,
			Message:                "logical data source ref orders-db has no binding for prod",
		}, {
			Severity: api.PipelineValidationDiagnosticSeverityWarning,
			Code:     "PIPELINE_CONTRACT_IMPACT_WARNING",
			Field:    "steps[1].transform.output_contract.email",
			Message:  "contract impact may affect downstream email projections",
		}},
	}
	older := result
	older.ValidationSlug = "validation-acme-users-sync-z9y8x7"
	older.CreatedAt = time.Date(2026, 6, 27, 14, 30, 0, 0, time.UTC)
	older.UpdatedAt = older.CreatedAt

	var out bytes.Buffer
	require.NoError(t, PipelineValidationHistoryPanel(
		"acme",
		"default",
		"prod",
		&api.Pipeline{Slug: "acme-users-sync", Name: "ACME Users Sync"},
		[]api.PipelineValidationResultRecord{result, older},
		false,
		NewOrganizationPermissions("pipelines:update"),
		true,
	).Render(context.Background(), &out))

	html := out.String()
	for _, expected := range []string{
		"Validation evidence",
		"validation-acme-users-sync-a1b2c3",
		"validation-acme-users-sync-z9y8x7",
		"Inspect",
		"server revalidation",
		"No runtime run was created for this validation failure.",
		"PIPELINE_DATA_SOURCE_REF_MISSING_BINDING",
		"PIPELINE_CONTRACT_IMPACT_WARNING",
		"steps[0].query.data_source_ref",
		"fetch-users",
		"orders-db",
		"orders-prod",
		"pipeline_source",
		"/orgs/acme/projects/default/pipelines/acme-users-sync/validation/revalidate?environment_slug=prod",
		"/builder?org_slug=acme&amp;pipeline_slug=acme-users-sync&amp;project_slug=default&amp;target_environment_slug=prod",
		"/orgs/acme/projects/default/environments",
	} {
		require.Contains(t, html, expected)
	}
	require.Contains(t, html, `class="badge badge-warning badge-outline">warning`)
	require.NotContains(t, html, `class="badge badge-success badge-outline">warning`)
	require.NotContains(t, html, "name=\"return_to\"", "embedded revalidation should keep the current HTMX URL instead of a default catalog URL")
	require.NotContains(t, html, "run_slug", "validation evidence must not render a runtime run record")
}

func TestPipelineValidationHistoryPanelHidesRevalidateWithoutUpdatePermission(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	require.NoError(t, PipelineValidationHistoryPanel(
		"acme",
		"default",
		"prod",
		&api.Pipeline{Slug: "acme-users-sync", Name: "ACME Users Sync"},
		nil,
		false,
		DefaultOrganizationPermissions(),
		false,
	).Render(context.Background(), &out))

	html := out.String()
	require.Contains(t, html, "Validation evidence")
	require.NotContains(t, html, "validation/revalidate")
	require.NotContains(t, html, "Revalidate")
}
