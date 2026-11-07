package views

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

type PipelineUpdateFormData struct {
	Name        string
	ConfirmSlug string
}

// PipelineCatalogFilters carries SSR filter values for the visible pipeline catalog.
type PipelineCatalogFilters struct {
	Query           string
	SyncStatus      string
	DraftState      string
	DeploymentState string
	RunStatus       string
	Cursor          string
	Direction       string
}

// PipelineCatalogRow joins pipeline metadata with deployment and latest-run display state.
type PipelineCatalogRow struct {
	Pipeline     api.PipelineRef
	Deployments  []PipelineDeploymentBadge
	LatestRun    PipelineLatestRunSummary
	HasLatestRun bool
}

// PipelineDeploymentBadge is the compact environment/version label rendered in catalog surfaces.
type PipelineDeploymentBadge struct {
	PipelineSlug     string
	EnvironmentSlug  string
	EnvironmentColor string
	ShortSpecHash    string
	FullSpecHash     string
	DeployedAt       string
}

// PipelineLatestRunSummary is the small run read model needed by pipeline catalog/detail surfaces.
type PipelineLatestRunSummary struct {
	EnvironmentSlug string
	RunSlug         string
	Status          api.PipelineRunMetadataStatus
	CreatedAt       api.Timestamp
	StartedAt       api.Timestamp
	DurationMs      int64
	ErrorSummary    *string
}

// PipelineSelectedDetail carries the right-panel pipeline detail state on the catalog page.
type PipelineSelectedDetail struct {
	Slug                   string
	Pipeline               *api.Pipeline
	Deployments            []PipelineDeploymentBadge
	DeploymentsUnavailable bool
	LatestRun              PipelineLatestRunSummary
	HasLatestRun           bool
	LatestRunUnavailable   bool
	ValidationResults      []api.PipelineValidationResultRecord
	ValidationUnavailable  bool
	BindingContext         PipelineBindingContext
	UpdateFormData         PipelineUpdateFormData
	ErrorSummary           string
	ErrorDetails           string
}

// PipelineValidationContextRow is one compact diagnostic context key/value row.
type PipelineValidationContextRow struct {
	Label string
	Value string
}

const (
	BuilderDraftPipelineSlug      = "draft-pipeline"
	pipelineCatalogFilterNone     = "none"
	pipelineDisplaySpecHashLength = 12
	pipelineValidationSourceAPI   = "api"
)

// DefaultPipelineCatalogFilters returns the unfiltered catalog state.
func DefaultPipelineCatalogFilters() PipelineCatalogFilters {
	return PipelineCatalogFilters{}
}

func DefaultPipelineUpdateFormData() PipelineUpdateFormData {
	return PipelineUpdateFormData{}
}

func PipelineUpdateFormDataFromPipeline(pipeline api.Pipeline) PipelineUpdateFormData {
	return PipelineUpdateFormData{Name: pipeline.Name}
}

// PipelineDetailHTMXTarget returns the stable fragment target for detail actions.
func PipelineDetailHTMXTarget(embedded bool) string {
	if embedded {
		return "#pipeline-detail-panel-slot"
	}
	return htmxMainContentSelector
}

// PipelineDetailHTMXSelect returns the response selector for detail action swaps.
func PipelineDetailHTMXSelect(embedded bool) string {
	return PipelineDetailHTMXTarget(embedded)
}

// PipelineDetailHTMXSwap returns a scroll-preserving replacement mode for detail actions.
func PipelineDetailHTMXSwap() string {
	return htmxScrollPreserveSwap
}

// runtimeTargetActionLabel appends explicit project runtime target copy to an action.
func runtimeTargetActionLabel(action, environmentSlug string) string {
	trimmedEnvironmentSlug := strings.TrimSpace(environmentSlug)
	if trimmedEnvironmentSlug == "" {
		return action
	}
	return action + " in " + trimmedEnvironmentSlug
}

// runtimeTargetBadgeLabel returns compact visible copy for a selected project runtime target.
func runtimeTargetBadgeLabel(environmentSlug string) string {
	trimmedEnvironmentSlug := strings.TrimSpace(environmentSlug)
	if trimmedEnvironmentSlug == "" {
		return "No runtime target"
	}
	return "Target: " + trimmedEnvironmentSlug
}

// PipelineCatalogRows joins and filters pipeline rows for the SSR catalog view.
func PipelineCatalogRows(pipelines []api.PipelineRef, deployments map[string][]PipelineDeploymentBadge, latestRuns map[string]PipelineLatestRunSummary, filters PipelineCatalogFilters) []PipelineCatalogRow {
	rows := make([]PipelineCatalogRow, 0, len(pipelines))
	for _, pipeline := range pipelines {
		row := PipelineCatalogRow{
			Pipeline:    pipeline,
			Deployments: pipelineDeploymentBadgesFor(deployments, pipeline.Slug),
		}
		if latestRun, ok := latestRuns[strings.TrimSpace(pipeline.Slug)]; ok {
			row.LatestRun = latestRun
			row.HasLatestRun = true
		}
		if pipelineCatalogRowMatches(row, filters) {
			rows = append(rows, row)
		}
	}
	return rows
}

// PipelineLatestRunsByPipeline keeps the newest run seen for each pipeline as the latest display row.
func PipelineLatestRunsByPipeline(runs []api.PipelineRunMetadata) map[string]PipelineLatestRunSummary {
	latestRuns := make(map[string]PipelineLatestRunSummary, len(runs))
	for _, run := range runs {
		pipelineSlug := strings.TrimSpace(run.PipelineSlug)
		if pipelineSlug == "" {
			continue
		}
		candidate := PipelineLatestRunSummary{
			EnvironmentSlug: run.EnvironmentSlug,
			RunSlug:         run.RunSlug,
			Status:          run.Status,
			CreatedAt:       run.CreatedAt,
			StartedAt:       run.StartedAt,
			DurationMs:      run.DurationMs,
			ErrorSummary:    run.ErrorSummary,
		}
		current, exists := latestRuns[pipelineSlug]
		if !exists || pipelineLatestRunIsNewer(candidate, current) {
			latestRuns[pipelineSlug] = candidate
		}
	}
	return latestRuns
}

// pipelineLatestRunIsNewer compares run metadata without depending on API result ordering.
func pipelineLatestRunIsNewer(candidate, current PipelineLatestRunSummary) bool {
	if candidate.CreatedAt.After(current.CreatedAt) {
		return true
	}
	if current.CreatedAt.After(candidate.CreatedAt) {
		return false
	}
	if candidate.StartedAt.After(current.StartedAt) {
		return true
	}
	if current.StartedAt.After(candidate.StartedAt) {
		return false
	}
	return strings.TrimSpace(candidate.RunSlug) > strings.TrimSpace(current.RunSlug)
}

// pipelineCatalogFiltersActive reports whether the SSR catalog is narrowed by any filter.
func pipelineCatalogFiltersActive(filters PipelineCatalogFilters) bool {
	return strings.TrimSpace(filters.Query) != "" ||
		strings.TrimSpace(filters.SyncStatus) != "" ||
		strings.TrimSpace(filters.DraftState) != "" ||
		strings.TrimSpace(filters.DeploymentState) != "" ||
		strings.TrimSpace(filters.RunStatus) != ""
}

// pipelineCatalogRowMatches applies all catalog filters to one joined pipeline row.
func pipelineCatalogRowMatches(row PipelineCatalogRow, filters PipelineCatalogFilters) bool {
	if query := strings.ToLower(strings.TrimSpace(filters.Query)); query != "" && !pipelineCatalogRowMatchesQuery(row, query) {
		return false
	}
	if syncStatus := strings.TrimSpace(filters.SyncStatus); syncStatus != "" && syncStatus != string(row.Pipeline.SyncStatus) {
		return false
	}
	if draftState := strings.TrimSpace(filters.DraftState); draftState != "" && !pipelineCatalogRowMatchesDraft(row, draftState) {
		return false
	}
	if deploymentState := strings.TrimSpace(filters.DeploymentState); deploymentState != "" && !pipelineCatalogRowMatchesDeployment(row, deploymentState) {
		return false
	}
	if runStatus := strings.TrimSpace(filters.RunStatus); runStatus != "" && !pipelineCatalogRowMatchesRun(row, runStatus) {
		return false
	}
	return true
}

// pipelineCatalogRowMatchesQuery searches operator-facing identity and provenance fields.
func pipelineCatalogRowMatchesQuery(row PipelineCatalogRow, query string) bool {
	fields := []string{
		row.Pipeline.Name,
		row.Pipeline.Slug,
		row.Pipeline.SourcePath,
		row.Pipeline.SourceRef,
		row.Pipeline.LastAppliedCommit,
		formatPipelineVersionSpecHash(row.Pipeline.CurrentVersionSpecHash),
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

// pipelineCatalogRowMatchesDraft applies the draft-state filter without inferring draft freshness.
func pipelineCatalogRowMatchesDraft(row PipelineCatalogRow, draftState string) bool {
	switch draftState {
	case "draft":
		return row.Pipeline.HasDraft
	case pipelineCatalogFilterNone:
		return !row.Pipeline.HasDraft
	default:
		return true
	}
}

// pipelineCatalogRowMatchesDeployment applies deployed/not-deployed filtering to badge state.
func pipelineCatalogRowMatchesDeployment(row PipelineCatalogRow, deploymentState string) bool {
	switch deploymentState {
	case "deployed":
		return len(row.Deployments) > 0
	case "not_deployed":
		return len(row.Deployments) == 0
	default:
		return true
	}
}

// pipelineCatalogRowMatchesRun applies the latest-run filter to current environment run state.
func pipelineCatalogRowMatchesRun(row PipelineCatalogRow, runStatus string) bool {
	switch runStatus {
	case string(api.PipelineRunMetadataStatusSucceeded), string(api.PipelineRunMetadataStatusFailed):
		return row.HasLatestRun && string(row.LatestRun.Status) == runStatus
	case pipelineCatalogFilterNone:
		return !row.HasLatestRun
	default:
		return true
	}
}

func PipelineBuilderDraftHref(orgSlug, projectSlug, pipelineName, pipelineSlug string) string {
	return PipelineBuilderDraftHrefWithEnvironment(orgSlug, projectSlug, "", pipelineName, pipelineSlug)
}

func PipelineBuilderDraftHrefWithEnvironment(orgSlug, projectSlug, environmentSlug, pipelineName, pipelineSlug string) string {
	values := url.Values{}
	values.Set("target_org_slug", orgSlug)
	values.Set("project_slug", strings.TrimSpace(projectSlug))
	values.Set("draft_name", strings.TrimSpace(pipelineName))
	values.Set("draft_slug", strings.TrimSpace(pipelineSlug))
	if strings.TrimSpace(environmentSlug) != "" {
		values.Set("target_environment_slug", strings.TrimSpace(environmentSlug))
	}
	return "/builder?" + values.Encode()
}

func pipelineBuilderHref(orgSlug, projectSlug, pipelineSlug string) string {
	values := url.Values{}
	values.Set("org_slug", strings.TrimSpace(orgSlug))
	values.Set("project_slug", strings.TrimSpace(projectSlug))
	values.Set("pipeline_slug", strings.TrimSpace(pipelineSlug))
	return "/builder?" + values.Encode()
}

func pipelineBuilderHrefWithEnvironment(orgSlug, projectSlug, environmentSlug, pipelineSlug string) string {
	values := url.Values{}
	values.Set("org_slug", strings.TrimSpace(orgSlug))
	values.Set("project_slug", strings.TrimSpace(projectSlug))
	values.Set("pipeline_slug", strings.TrimSpace(pipelineSlug))
	if strings.TrimSpace(environmentSlug) != "" {
		values.Set("target_environment_slug", strings.TrimSpace(environmentSlug))
	}
	return "/builder?" + values.Encode()
}

func pipelineRunsHrefWithProject(orgSlug, projectSlug, pipelineSlug string) string {
	trimmedProjectSlug := strings.TrimSpace(projectSlug)
	if trimmedProjectSlug == "" {
		return "/organizations/" + strings.TrimSpace(orgSlug) + "/projects"
	}

	base := "/orgs/" + strings.TrimSpace(orgSlug) + "/projects/" + trimmedProjectSlug + "/runs"
	trimmedPipelineSlug := strings.TrimSpace(pipelineSlug)
	if trimmedPipelineSlug == "" {
		return base
	}

	values := url.Values{}
	values.Set("pipeline_slug", trimmedPipelineSlug)
	return base + "?" + values.Encode()
}

func pipelineDetailHref(orgSlug, projectSlug, pipelineSlug string) string {
	trimmedOrgSlug := strings.TrimSpace(orgSlug)
	trimmedProjectSlug := strings.TrimSpace(projectSlug)
	trimmedPipelineSlug := strings.TrimSpace(pipelineSlug)
	if trimmedProjectSlug == "" {
		return "/organizations/" + trimmedOrgSlug + "/projects"
	}

	return "/orgs/" + trimmedOrgSlug + "/projects/" + trimmedProjectSlug + "/pipelines/" + trimmedPipelineSlug
}

func pipelineDetailHrefWithEnvironment(orgSlug, projectSlug, environmentSlug, pipelineSlug string) string {
	base := pipelineDetailHref(orgSlug, projectSlug, pipelineSlug)
	if strings.TrimSpace(projectSlug) == "" || strings.TrimSpace(environmentSlug) == "" {
		return base
	}

	values := url.Values{}
	values.Set("environment_slug", strings.TrimSpace(environmentSlug))
	return base + "?" + values.Encode()
}

func PipelineDetailHref(orgSlug, projectSlug, pipelineSlug string) string {
	return pipelineDetailHref(orgSlug, projectSlug, pipelineSlug)
}

func PipelineDetailHrefWithEnvironment(orgSlug, projectSlug, environmentSlug, pipelineSlug string) string {
	return pipelineDetailHrefWithEnvironment(orgSlug, projectSlug, environmentSlug, pipelineSlug)
}

func PipelineScheduleDetailHref(orgSlug, projectSlug, environmentSlug, triggerSlug string) string {
	return scheduleDetailHref(orgSlug, projectSlug, environmentSlug, triggerSlug)
}

// PipelineDeploymentDetailHrefWithEnvironment builds the read-only deployment inspection URL.
func PipelineDeploymentDetailHrefWithEnvironment(orgSlug, projectSlug, environmentSlug, pipelineSlug string) string {
	base := pipelineDetailHref(orgSlug, projectSlug, pipelineSlug)
	if strings.TrimSpace(projectSlug) == "" {
		return base
	}

	base += "/deployment"
	if strings.TrimSpace(environmentSlug) == "" {
		return base
	}

	values := url.Values{}
	values.Set("environment_slug", strings.TrimSpace(environmentSlug))
	return base + "?" + values.Encode()
}

func buildProjectPipelinesListHref(orgSlug, projectSlug, cursor, direction string) string {
	return buildEnvironmentProjectPipelinesListHref(orgSlug, projectSlug, "", cursor, direction)
}

func buildEnvironmentProjectPipelinesListHref(orgSlug, projectSlug, environmentSlug, cursor, direction string) string {
	trimmedProjectSlug := strings.TrimSpace(projectSlug)
	if trimmedProjectSlug == "" {
		return "/organizations/" + strings.TrimSpace(orgSlug) + "/projects"
	}

	values := url.Values{}
	if strings.TrimSpace(environmentSlug) != "" {
		values.Set("environment_slug", strings.TrimSpace(environmentSlug))
	}
	if strings.TrimSpace(cursor) != "" {
		values.Set("cursor", cursor)
	}
	if strings.TrimSpace(direction) != "" {
		values.Set("direction", direction)
	}

	encoded := values.Encode()
	base := "/orgs/" + strings.TrimSpace(orgSlug) + "/projects/" + trimmedProjectSlug + "/pipelines"
	if encoded == "" {
		return base
	}

	return base + "?" + encoded
}

// buildEnvironmentProjectPipelinesListHrefWithFilters preserves catalog filters across pagination links.
func buildEnvironmentProjectPipelinesListHrefWithFilters(
	orgSlug string,
	projectSlug string,
	environmentSlug string,
	cursor string,
	direction string,
	filters PipelineCatalogFilters,
) string {
	trimmedProjectSlug := strings.TrimSpace(projectSlug)
	if trimmedProjectSlug == "" {
		return "/organizations/" + strings.TrimSpace(orgSlug) + "/projects"
	}

	values := url.Values{}
	if strings.TrimSpace(environmentSlug) != "" {
		values.Set("environment_slug", strings.TrimSpace(environmentSlug))
	}
	if strings.TrimSpace(cursor) != "" {
		values.Set("cursor", cursor)
	}
	if strings.TrimSpace(direction) != "" {
		values.Set("direction", direction)
	}
	if query := strings.TrimSpace(filters.Query); query != "" {
		values.Set("q", query)
	}
	if syncStatus := strings.TrimSpace(filters.SyncStatus); syncStatus != "" {
		values.Set("sync_status", syncStatus)
	}
	if draftState := strings.TrimSpace(filters.DraftState); draftState != "" {
		values.Set("draft", draftState)
	}
	if deploymentState := strings.TrimSpace(filters.DeploymentState); deploymentState != "" {
		values.Set("deployment", deploymentState)
	}
	if runStatus := strings.TrimSpace(filters.RunStatus); runStatus != "" {
		values.Set("run_status", runStatus)
	}

	encoded := values.Encode()
	base := "/orgs/" + strings.TrimSpace(orgSlug) + "/projects/" + trimmedProjectSlug + "/pipelines"
	if encoded == "" {
		return base
	}

	return base + "?" + encoded
}

// pipelineSelectionHrefWithFilters builds the master-detail catalog URL for one selected pipeline.
func pipelineSelectionHrefWithFilters(
	orgSlug string,
	projectSlug string,
	environmentSlug string,
	pipelineSlug string,
	filters PipelineCatalogFilters,
) string {
	trimmedProjectSlug := strings.TrimSpace(projectSlug)
	if trimmedProjectSlug == "" {
		return "/organizations/" + strings.TrimSpace(orgSlug) + "/projects"
	}

	values := url.Values{}
	if trimmedEnvironmentSlug := strings.TrimSpace(environmentSlug); trimmedEnvironmentSlug != "" {
		values.Set("environment_slug", trimmedEnvironmentSlug)
	}
	if trimmedPipelineSlug := strings.TrimSpace(pipelineSlug); trimmedPipelineSlug != "" {
		values.Set("pipeline_slug", trimmedPipelineSlug)
	}
	if cursor := strings.TrimSpace(filters.Cursor); cursor != "" {
		values.Set("cursor", cursor)
	}
	if direction := strings.TrimSpace(filters.Direction); direction != "" {
		values.Set("direction", direction)
	}
	if query := strings.TrimSpace(filters.Query); query != "" {
		values.Set("q", query)
	}
	if syncStatus := strings.TrimSpace(filters.SyncStatus); syncStatus != "" {
		values.Set("sync_status", syncStatus)
	}
	if draftState := strings.TrimSpace(filters.DraftState); draftState != "" {
		values.Set("draft", draftState)
	}
	if deploymentState := strings.TrimSpace(filters.DeploymentState); deploymentState != "" {
		values.Set("deployment", deploymentState)
	}
	if runStatus := strings.TrimSpace(filters.RunStatus); runStatus != "" {
		values.Set("run_status", runStatus)
	}

	encoded := values.Encode()
	base := "/orgs/" + strings.TrimSpace(orgSlug) + "/projects/" + trimmedProjectSlug + "/pipelines"
	if encoded == "" {
		return base
	}

	return base + "?" + encoded
}

func ProjectPipelinesListHref(orgSlug, projectSlug, cursor, direction string) string {
	return buildProjectPipelinesListHref(orgSlug, projectSlug, cursor, direction)
}

func ProjectPipelinesListHrefWithEnvironment(orgSlug, projectSlug, environmentSlug, cursor, direction string) string {
	return buildEnvironmentProjectPipelinesListHref(orgSlug, projectSlug, environmentSlug, cursor, direction)
}

func pipelineActionHref(orgSlug, projectSlug, pipelineSlug, action string) string {
	return pipelineActionHrefWithEnvironment(orgSlug, projectSlug, "", pipelineSlug, action)
}

func pipelineActionHrefWithEnvironment(orgSlug, projectSlug, environmentSlug, pipelineSlug, action string) string {
	base := pipelineDetailHref(orgSlug, projectSlug, pipelineSlug)
	trimmedAction := strings.TrimSpace(action)
	if trimmedAction != "" {
		base += "/" + trimmedAction
	}

	trimmedEnvironmentSlug := strings.TrimSpace(environmentSlug)
	if strings.TrimSpace(projectSlug) == "" || trimmedEnvironmentSlug == "" {
		return base
	}

	values := url.Values{}
	values.Set("environment_slug", trimmedEnvironmentSlug)
	return base + "?" + values.Encode()
}

// PipelineCatalogItemClass returns clickable catalog row styling for selected and operational states.
func PipelineCatalogItemClass(row PipelineCatalogRow, selectedPipelineSlug string) string {
	base := "block rounded-lg border p-3 text-left transition hover:border-primary/55 hover:bg-primary/10 focus:outline-none focus:ring-2 focus:ring-primary/45"
	if row.Pipeline.Slug == strings.TrimSpace(selectedPipelineSlug) {
		return base + " border-primary/60 bg-primary/15"
	}
	if row.Pipeline.SyncStatus == api.Drifted {
		return base + " border-warning/35 bg-warning/10"
	}
	if row.Pipeline.HasDraft {
		return base + " border-info/35 bg-info/10"
	}
	if len(row.Deployments) == 0 {
		return base + " border-base-300 bg-base-200/35"
	}
	return base + " border-success/25 bg-base-100"
}

func pipelineSyncStatusBadgeClass(syncStatus api.PipelineSyncStatus) string {
	if syncStatus == api.Drifted {
		return badgeWarningOutline
	}
	return eventBadgeSuccess
}

// pipelineSyncStatusLabel renders sync state as stable display copy.
func pipelineSyncStatusLabel(syncStatus api.PipelineSyncStatus) string {
	switch syncStatus {
	case api.Drifted:
		return "drifted"
	case api.Synced:
		return "synced"
	default:
		return string(syncStatus)
	}
}

func pipelineDraftBadgeClass(hasDraft bool) string {
	if hasDraft {
		return badgePrimaryOutline
	}

	return badgeGhostOutline
}

// pipelineDraftBadgeLabel renders draft presence without inferring publish state.
func pipelineDraftBadgeLabel(hasDraft bool) string {
	if hasDraft {
		return "draft saved"
	}
	return "no draft"
}

// pipelineDetailDraftLabel adds detail-page freshness context when timestamps prove it.
func pipelineDetailDraftLabel(pipeline *api.Pipeline) string {
	if pipeline == nil || pipeline.Draft == nil {
		return "No draft"
	}
	if !pipeline.Draft.UpdatedAt.IsZero() && !pipeline.UpdatedAt.IsZero() && pipeline.Draft.UpdatedAt.After(pipeline.UpdatedAt) {
		return "Draft saved after canonical update"
	}
	return "Draft saved"
}

// pipelineDetailDraftDescription explains how draft state relates to the owning authoring path.
func pipelineDetailDraftDescription(pipeline *api.Pipeline) string {
	if pipeline == nil || pipeline.Draft == nil {
		return "No member draft is currently saved for this pipeline."
	}
	return "This UI draft is working state for a Git-authored pipeline. Publishing proposes a Git change; deployment remains separate."
}

// ProjectEnvironmentColorMap indexes enabled project environment colors by slug.
func ProjectEnvironmentColorMap(environments []api.ProjectEnvironment) map[string]string {
	colors := make(map[string]string, len(environments))
	for _, environment := range environments {
		colors[strings.TrimSpace(environment.EnvironmentSlug)] = string(environment.EnvironmentColor)
	}
	return colors
}

// PipelineDeploymentBadgesByPipeline groups environment deployment pointers by pipeline slug.
func PipelineDeploymentBadgesByPipeline(deployments []api.PipelineDeployment, environmentColors map[string]string) map[string][]PipelineDeploymentBadge {
	badges := make(map[string][]PipelineDeploymentBadge)
	for _, deployment := range deployments {
		pipelineSlug := deployment.PipelineSlug
		badges[pipelineSlug] = append(badges[pipelineSlug], PipelineDeploymentBadge{
			PipelineSlug:     deployment.PipelineSlug,
			EnvironmentSlug:  deployment.EnvironmentSlug,
			EnvironmentColor: environmentColors[strings.TrimSpace(deployment.EnvironmentSlug)],
			ShortSpecHash:    deployment.CurrentVersionShortSpecHash,
			FullSpecHash:     deployment.CurrentVersionSpecHash,
			DeployedAt:       formatPipelineTimestamp(deployment.DeployedAt),
		})
	}
	return badges
}

// pipelineDeploymentBadgesFor returns deployment badges for one pipeline slug.
func pipelineDeploymentBadgesFor(deployments map[string][]PipelineDeploymentBadge, pipelineSlug string) []PipelineDeploymentBadge {
	return deployments[strings.TrimSpace(pipelineSlug)]
}

// pipelineDeploymentStateLabel summarizes deployment pointer availability without run status.
func pipelineDeploymentStateLabel(badges []PipelineDeploymentBadge, unavailable bool) string {
	if unavailable {
		return "deployment metadata unavailable"
	}
	if len(badges) == 0 {
		return "not deployed"
	}
	if len(badges) == 1 {
		return "1 environment deployed"
	}
	return strconv.Itoa(len(badges)) + " environments deployed"
}

// pipelineDeploymentBadgeClass maps stored environment colors to compact badge treatments.
func pipelineDeploymentBadgeClass(environmentColor string) string {
	return environmentColorOutlineClass(environmentColor)
}

// pipelineDeploymentStateBadgeClass renders deployment pointer state without using run status colors.
func pipelineDeploymentStateBadgeClass(deployment *api.PipelineDeployment) string {
	if deployment == nil {
		return badgeGhostOutline
	}
	return badgeSuccessOutline
}

// pipelineDeploymentStateDetailLabel describes whether a deployment pointer exists for the selected target.
func pipelineDeploymentStateDetailLabel(deployment *api.PipelineDeployment) string {
	if deployment == nil {
		return "not deployed"
	}
	return "deployed pointer"
}

// pipelineDeploymentActorLabel renders deploy actor metadata without exposing any credential material.
func pipelineDeploymentActorLabel(deployment *api.PipelineDeployment) string {
	if deployment == nil {
		return "-"
	}

	actorType := strings.TrimSpace(string(deployment.DeployedByActorType))
	if actorType == "" {
		actorType = "actor"
	}
	if deployment.DeployedByActorSlug == nil || strings.TrimSpace(*deployment.DeployedByActorSlug) == "" {
		return actorType
	}
	return actorType + " " + strings.TrimSpace(*deployment.DeployedByActorSlug)
}

// pipelineDeploymentPreviousVersionLabel renders rollback context when a previous pointer is known.
func pipelineDeploymentPreviousVersionLabel(deployment *api.PipelineDeployment) string {
	if deployment == nil || deployment.PreviousVersionShortSpecHash == nil || strings.TrimSpace(*deployment.PreviousVersionShortSpecHash) == "" {
		return "No previous pointer recorded"
	}
	return strings.TrimSpace(*deployment.PreviousVersionShortSpecHash)
}

// pipelineDeploymentCanonicalComparisonLabel compares the deployed pointer with the current canonical pipeline version.
func pipelineDeploymentCanonicalComparisonLabel(pipeline *api.Pipeline, deployment *api.PipelineDeployment) string {
	if pipeline == nil || pipeline.CurrentVersionSpecHash == nil || strings.TrimSpace(*pipeline.CurrentVersionSpecHash) == "" {
		return "Canonical version unavailable"
	}
	if deployment == nil || strings.TrimSpace(deployment.CurrentVersionSpecHash) == "" {
		return "No deployed version selected"
	}
	if strings.TrimSpace(*pipeline.CurrentVersionSpecHash) == strings.TrimSpace(deployment.CurrentVersionSpecHash) {
		return "Matches current canonical version"
	}
	return "Differs from current canonical version"
}

// shortPipelineVersionSpecHash renders a compact canonical version label.
func shortPipelineVersionSpecHash(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "no version"
	}
	trimmed := strings.TrimSpace(*value)
	if len(trimmed) <= 12 {
		return trimmed
	}
	return trimmed[:12]
}

// pipelineVersionTitle returns the full canonical version hash for hover text.
func pipelineVersionTitle(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "No canonical version has been persisted yet."
	}
	return strings.TrimSpace(*value)
}

// pipelineLatestRunBadgeClass reuses run status styling for catalog latest-run state.
func pipelineLatestRunBadgeClass(status api.PipelineRunMetadataStatus) string {
	return runStatusBadgeClass(status)
}

// pipelineLatestRunStatusLabel renders latest-run availability and status.
func pipelineLatestRunStatusLabel(run PipelineLatestRunSummary, ok, unavailable bool) string {
	if unavailable {
		return "runs unavailable"
	}
	if !ok {
		return "no runs"
	}
	return string(run.Status)
}

// pipelineLatestRunTimingLabel renders latest-run start time and duration.
func pipelineLatestRunTimingLabel(run PipelineLatestRunSummary) string {
	if run.StartedAt.IsZero() {
		return "-"
	}
	return formatPipelineTimestamp(run.StartedAt) + " / " + runDurationLabel(run.DurationMs)
}

// pipelineLatestRunErrorLabel returns failure copy only when the API exposes a summary.
func pipelineLatestRunErrorLabel(run PipelineLatestRunSummary) string {
	if run.ErrorSummary == nil || strings.TrimSpace(*run.ErrorSummary) == "" {
		return ""
	}
	return strings.TrimSpace(*run.ErrorSummary)
}

// pipelineValidationLatest returns the newest validation result from the API-ordered history.
func pipelineValidationLatest(results []api.PipelineValidationResultRecord) *api.PipelineValidationResultRecord {
	if len(results) == 0 {
		return nil
	}
	return &results[0]
}

func pipelineValidationStatusBadgeClass(valid bool) string {
	if valid {
		return "badge badge-success badge-outline"
	}
	return "badge badge-error badge-outline"
}

// pipelineValidationDiagnosticSeverityBadgeClass renders diagnostic severity independently of validation pass/fail state.
func pipelineValidationDiagnosticSeverityBadgeClass(severity api.PipelineValidationDiagnosticSeverity) string {
	switch severity {
	case api.PipelineValidationDiagnosticSeverityError:
		return badgeErrorOutline
	case api.PipelineValidationDiagnosticSeverityWarning:
		return badgeWarningOutline
	default:
		return badgeGhostOutline
	}
}

func pipelineValidationStatusLabel(valid bool) string {
	if valid {
		return "passed"
	}
	return DataSourceListFilterFailed
}

func pipelineValidationSourceLabel(source api.PipelineValidationResultSource) string {
	switch strings.TrimSpace(string(source)) {
	case pipelineValidationSourceAPI:
		return "API validation"
	case "execute":
		return "execution preflight"
	case "revalidate":
		return "server revalidation"
	default:
		return "validation"
	}
}

func pipelineValidationSpecHashLabel(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "-"
	}
	trimmed := strings.TrimSpace(*value)
	if len(trimmed) <= pipelineDisplaySpecHashLength {
		return trimmed
	}
	return trimmed[:pipelineDisplaySpecHashLength]
}

func pipelineValidationNullableSlugLabel(value *api.NullableSlug) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return ""
	}
	return strings.TrimSpace(*value)
}

func pipelineValidationDiagnosticContextRows(diagnostic api.PipelineValidationDiagnostic) []PipelineValidationContextRow {
	rows := make([]PipelineValidationContextRow, 0, 8)
	add := func(label, value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		rows = append(rows, PipelineValidationContextRow{Label: label, Value: value})
	}

	add("Spec path", diagnostic.Field)
	add("Step", pipelineValidationNullableSlugLabel(diagnostic.StepSlug))
	add("Runtime target", pipelineValidationNullableSlugLabel(diagnostic.EnvironmentSlug))
	add("Logical ref", pipelineValidationNullableSlugLabel(diagnostic.DataSourceRefSlug))
	add("Spec data-source ref", pipelineValidationNullableSlugLabel(diagnostic.DataSourceRef))
	add("Resolved data source", pipelineValidationNullableSlugLabel(diagnostic.ResolvedDataSourceSlug))
	if diagnostic.ExpectedDataSourceType != nil {
		add("Expected type", string(*diagnostic.ExpectedDataSourceType))
	}
	if diagnostic.DataSourceType != nil {
		add("Actual type", string(*diagnostic.DataSourceType))
	}
	if diagnostic.RequiredCapability != nil {
		add("Required capability", string(*diagnostic.RequiredCapability))
	}
	return rows
}

func pipelineValidationDiagnosticSummary(diagnostics []api.PipelineValidationDiagnostic) string {
	if len(diagnostics) == 0 {
		return "No diagnostics returned."
	}
	code := strings.TrimSpace(diagnostics[0].Code)
	if code == "" {
		return "Diagnostic returned."
	}
	return code
}

func formatPipelineTimestamp(ts api.Timestamp) string {
	if ts.IsZero() {
		return "-"
	}

	return ts.UTC().Format(time.RFC3339)
}

func nullablePipelineField(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "-"
	}
	return *value
}

func formatPipelineVersionSpecHash(value *string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return "-"
	}

	return *value
}

func formatPipelineDraftUpdatedAt(draft *api.PipelineDraft) string {
	if draft == nil {
		return "-"
	}

	return formatPipelineTimestamp(draft.UpdatedAt)
}

// pipelineDetailSection accepts only the workspace's SSR destinations.
func pipelineDetailSection(ctx context.Context) string {
	switch section := entityPageURL(ctx).Query().Get("section"); section {
	case "workflow", "runs", "connections", "deployments", "git":
		return section
	default:
		return "overview"
	}
}

func pipelineWorkflowSummary(pipeline *api.Pipeline) string {
	projection := PipelineSpecProjectionFromPipeline(pipeline)
	if projection.TotalSteps == 0 {
		return "No steps configured"
	}
	return fmt.Sprintf("%d source · %d transform · %d sink steps", len(projection.SourceSteps), len(projection.TransformSteps), len(projection.SinkSteps))
}

func pipelineTargetDeployment(deployments []PipelineDeploymentBadge, environment string) []PipelineDeploymentBadge {
	for _, deployment := range deployments {
		if deployment.EnvironmentSlug == environment {
			return []PipelineDeploymentBadge{deployment}
		}
	}
	return nil
}
