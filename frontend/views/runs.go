package views

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/runlineage"
)

const (
	failureKindPlatform   = "platform"
	failureKindDataSource = "data_source"
	failureSideSink       = "sink"
	failureSideSource     = "source"
	failureSideTransform  = "transform"
	failureSideConnector  = "connector"
	runStatusPending      = "pending"
)

// PipelineRunSelectedDetail carries the right-panel run detail state on the runs page.
type PipelineRunSelectedDetail struct {
	Slug         string
	Run          *api.PipelineRunRecord
	ErrorSummary string
}

func runStatusBadgeClass(status api.PipelineRunMetadataStatus) string {
	switch strings.TrimSpace(string(status)) {
	case string(api.PipelineRunMetadataStatusSucceeded):
		return eventBadgeSuccess
	case string(api.PipelineRunMetadataStatusFailed):
		return eventBadgeError
	case "running", "in_progress":
		return eventBadgeInfo
	case "queued", runStatusPending:
		return badgeWarningOutline
	case "canceled", "cancelled":
		return badgeGhostOutline
	default:
		return eventBadgeOutline
	}
}

func formatRunTimestamp(ts api.Timestamp) string {
	if ts.IsZero() {
		return "-"
	}

	return ts.UTC().Format(time.RFC3339)
}

func formatNullableRunTimestamp(ts *api.NullableTimestamp) string {
	if ts == nil {
		return "-"
	}

	return formatRunTimestamp(*ts)
}

func runDurationLabel(durationMs int64) string {
	if durationMs < 1000 {
		return strconv.FormatInt(durationMs, 10) + " ms"
	}

	seconds := float64(durationMs) / 1000.0
	return strconv.FormatFloat(seconds, 'f', 2, 64) + " s"
}

func runStatusFilterLabel(statusFilter string) string {
	switch strings.TrimSpace(statusFilter) {
	case string(api.PipelineRunMetadataStatusFailed):
		return "Failed runs"
	case string(api.PipelineRunMetadataStatusSucceeded):
		return "Succeeded runs"
	default:
		return "Recent runs"
	}
}

func runFiltersActive(statusFilter, pipelineSlugFilter, modeFilter, backfillSeriesFilter string) bool {
	return strings.TrimSpace(statusFilter) != "" ||
		strings.TrimSpace(pipelineSlugFilter) != "" ||
		strings.TrimSpace(modeFilter) != "" ||
		strings.TrimSpace(backfillSeriesFilter) != ""
}

func runSourceLabel(view apiRunSourceViewData) string {
	summary := strings.TrimSpace(sourceSummary(view))
	if summary == "" {
		summary = "Source unavailable"
	}

	entrypoint := strings.TrimSpace(sourceEntrypoint(view))
	if entrypoint == "" {
		return summary
	}

	return summary + " via " + entrypoint
}

func runSourceActorLabel(view apiRunSourceViewData) string {
	actorType := strings.TrimSpace(view.actorType)
	if actorType == "" || actorType == "unknown" {
		return "Actor unavailable"
	}
	if view.actorSlug == nil || strings.TrimSpace(*view.actorSlug) == "" {
		return actorType
	}
	return actorType + " " + strings.TrimSpace(*view.actorSlug)
}

func runSourceRequestLabel(view apiRunSourceViewData) string {
	parts := make([]string, 0, 2)
	if view.requestID != nil && strings.TrimSpace(*view.requestID) != "" {
		parts = append(parts, "Request "+strings.TrimSpace(*view.requestID))
	}
	if view.traceID != nil && strings.TrimSpace(*view.traceID) != "" {
		parts = append(parts, "Trace "+strings.TrimSpace(*view.traceID))
	}
	if len(parts) == 0 {
		return "No request context"
	}
	return strings.Join(parts, " / ")
}

func runSourceParameterKeysLabel(view apiRunSourceViewData) string {
	if len(view.parameterKeys) == 0 {
		return "No supplied parameter names"
	}
	return strings.Join(view.parameterKeys, ", ")
}

func runSourceEventLabel(view apiRunSourceViewData) string {
	if view.sourceEventSlug == nil || strings.TrimSpace(*view.sourceEventSlug) == "" {
		return "No source event"
	}
	return strings.TrimSpace(*view.sourceEventSlug)
}

func runAttemptLabel(view apiRunSourceViewData) string {
	if view.attemptNumber == 0 {
		return "Attempt not recorded"
	}
	if view.attemptNumber <= 1 {
		return "attempt 1"
	}
	return "attempt " + strconv.Itoa(view.attemptNumber)
}

func runRetryGroupLabel(view apiRunSourceViewData) string {
	rootRunSlug := strings.TrimSpace(view.rootRunSlug)
	if rootRunSlug == "" {
		return "No retry group"
	}
	if view.retryOfRunSlug == nil || strings.TrimSpace(*view.retryOfRunSlug) == "" {
		return "Root run " + rootRunSlug
	}
	return "Root " + rootRunSlug + " / retry of " + strings.TrimSpace(*view.retryOfRunSlug)
}

func runHasRetryContext(view apiRunSourceViewData) bool {
	if view.attemptNumber > 1 {
		return true
	}
	return view.retryOfRunSlug != nil && strings.TrimSpace(*view.retryOfRunSlug) != ""
}

func runSourceTypeLabel(view apiRunSourceViewData) string {
	sourceType := strings.TrimSpace(view.sourceType)
	if sourceType == "" {
		return "source unavailable"
	}
	return sourceType
}

func runScheduleTriggerLabel(view apiRunSourceViewData) string {
	if view.schedule == nil {
		return "No schedule trigger"
	}
	return view.schedule.TriggerSlug + " rev " + strconv.FormatInt(view.schedule.TriggerRevision, 10)
}

func runScheduleCadenceLabel(view apiRunSourceViewData) string {
	if view.schedule == nil {
		return "No schedule cadence"
	}
	return strings.TrimSpace(view.schedule.Cron) + " / " + strings.TrimSpace(view.schedule.Timezone)
}

func runScheduleFireLabel(view apiRunSourceViewData) string {
	if view.schedule == nil {
		return "No schedule fire"
	}
	return "scheduled " + formatRunTimestamp(view.schedule.ScheduledFireAt) + " / actual " + formatRunTimestamp(view.schedule.ActualFireAt)
}

func runScheduleMisfireLabel(view apiRunSourceViewData) string {
	if view.schedule == nil || strings.TrimSpace(string(view.schedule.MisfireResult)) == "" {
		return "No misfire result"
	}
	return string(view.schedule.MisfireResult)
}

func sourceSummary(view apiRunSourceViewData) string {
	return view.summary
}

func sourceEntrypoint(view apiRunSourceViewData) string {
	return view.entrypoint
}

type apiRunSourceViewData struct {
	sourceType      string
	summary         string
	entrypoint      string
	actorType       string
	actorSlug       *api.NullableSlug
	requestID       *string
	traceID         *string
	parameterKeys   []string
	sourceEventSlug *api.NullableSlug
	attemptNumber   int
	rootRunSlug     string
	retryOfRunSlug  *api.NullableSlug
	schedule        *api.PipelineRunSourceScheduleSummary
	webhook         *api.PipelineRunSourceWebhookSummary
}

func apiRunSourceView(source *api.PipelineRunSourceSummary) apiRunSourceViewData {
	if source == nil {
		return apiRunSourceViewData{}
	}
	discriminator, err := source.Discriminator()
	if err != nil {
		return apiRunSourceViewData{}
	}
	switch discriminator {
	case "manual":
		manual, err := source.AsPipelineRunManualSourceSummary()
		if err != nil {
			return apiRunSourceViewData{}
		}
		return apiRunSourceViewData{
			sourceType:      string(manual.Type),
			summary:         manual.Summary,
			entrypoint:      string(manual.Entrypoint),
			actorType:       string(manual.ActorType),
			actorSlug:       manual.ActorSlug,
			requestID:       manual.RequestId,
			traceID:         manual.TraceId,
			parameterKeys:   manual.ParameterKeys,
			sourceEventSlug: manual.SourceEventSlug,
			attemptNumber:   manual.AttemptNumber,
			rootRunSlug:     manual.RootRunSlug,
			retryOfRunSlug:  manual.RetryOfRunSlug,
		}
	case "completion":
		completion, err := source.AsPipelineRunCompletionSourceSummary()
		if err != nil {
			return apiRunSourceViewData{}
		}
		return apiRunSourceViewData{
			sourceType:      string(completion.Type),
			summary:         completion.Summary,
			entrypoint:      string(completion.Entrypoint),
			actorType:       string(completion.ActorType),
			actorSlug:       completion.ActorSlug,
			requestID:       completion.RequestId,
			traceID:         completion.TraceId,
			parameterKeys:   completion.ParameterKeys,
			sourceEventSlug: completion.SourceEventSlug,
			attemptNumber:   completion.AttemptNumber,
			rootRunSlug:     completion.RootRunSlug,
			retryOfRunSlug:  completion.RetryOfRunSlug,
		}
	case "api":
		apiSource, err := source.AsPipelineRunAPISourceSummary()
		if err != nil {
			return apiRunSourceViewData{}
		}
		return apiRunSourceViewData{
			sourceType:      string(apiSource.Type),
			summary:         apiSource.Summary,
			entrypoint:      string(apiSource.Entrypoint),
			actorType:       string(apiSource.ActorType),
			actorSlug:       apiSource.ActorSlug,
			requestID:       apiSource.RequestId,
			traceID:         apiSource.TraceId,
			parameterKeys:   apiSource.ParameterKeys,
			sourceEventSlug: apiSource.SourceEventSlug,
			attemptNumber:   apiSource.AttemptNumber,
			rootRunSlug:     apiSource.RootRunSlug,
			retryOfRunSlug:  apiSource.RetryOfRunSlug,
		}
	case "webhook":
		webhook, err := source.AsPipelineRunWebhookSourceSummary()
		if err != nil {
			return apiRunSourceViewData{}
		}
		return apiRunSourceViewData{
			sourceType: string(webhook.Type), summary: webhook.Summary,
			entrypoint: string(webhook.Entrypoint), actorType: string(webhook.ActorType),
			actorSlug: webhook.ActorSlug, requestID: webhook.RequestId, traceID: webhook.TraceId,
			parameterKeys: webhook.ParameterKeys, sourceEventSlug: webhook.SourceEventSlug,
			attemptNumber: webhook.AttemptNumber, rootRunSlug: webhook.RootRunSlug,
			retryOfRunSlug: webhook.RetryOfRunSlug, webhook: &webhook.Webhook,
		}
	case "schedule":
		schedule, err := source.AsPipelineRunScheduleSourceSummary()
		if err != nil {
			return apiRunSourceViewData{}
		}
		return apiRunSourceViewData{
			sourceType:      string(schedule.Type),
			summary:         schedule.Summary,
			entrypoint:      string(schedule.Entrypoint),
			actorType:       string(schedule.ActorType),
			actorSlug:       schedule.ActorSlug,
			requestID:       schedule.RequestId,
			traceID:         schedule.TraceId,
			parameterKeys:   schedule.ParameterKeys,
			sourceEventSlug: schedule.SourceEventSlug,
			attemptNumber:   schedule.AttemptNumber,
			rootRunSlug:     schedule.RootRunSlug,
			retryOfRunSlug:  schedule.RetryOfRunSlug,
			schedule:        &schedule.Schedule,
		}
	default:
		return apiRunSourceViewData{}
	}
}

func runReplayContextLabel(replayOfRunSlug, replayFromStep *api.NullableSlug) string {
	if replayOfRunSlug == nil || strings.TrimSpace(*replayOfRunSlug) == "" {
		return "No replay context"
	}

	label := "Replay of " + strings.TrimSpace(*replayOfRunSlug)
	if replayFromStep != nil && strings.TrimSpace(*replayFromStep) != "" {
		label += " from " + strings.TrimSpace(*replayFromStep)
	}

	return label
}

func runHasReplayContext(replayOfRunSlug *api.NullableSlug, source apiRunSourceViewData) bool {
	// Automatic retries inherit an effect namespace without becoming retained-input replays.
	return !runHasRetryContext(source) && replayOfRunSlug != nil && strings.TrimSpace(*replayOfRunSlug) != ""
}

func runOperationLabel(snapshot *api.PipelineRunStepSnapshot) string {
	if snapshot == nil {
		return "-"
	}

	operationType := ""
	if snapshot.Input.OperationType != nil {
		operationType = strings.TrimSpace(*snapshot.Input.OperationType)
	}

	operationRef := ""
	if snapshot.Input.OperationRef != nil {
		operationRef = strings.TrimSpace(*snapshot.Input.OperationRef)
	}

	if operationRef == "" && snapshot.Input.UsesInline != nil && *snapshot.Input.UsesInline {
		operationRef = "inline"
	}

	switch {
	case operationType != "" && operationRef != "":
		return operationType + " / " + operationRef
	case operationType != "":
		return operationType
	case operationRef != "":
		return operationRef
	default:
		return "-"
	}
}

func runFailureDataSourceLabel(failure *api.PipelineRunFailureContext, snapshot *api.PipelineRunStepSnapshot) string {
	if failure != nil && failure.DataSourceSlug != nil && strings.TrimSpace(*failure.DataSourceSlug) != "" {
		return strings.TrimSpace(*failure.DataSourceSlug)
	}

	if dataSourceSlug := runSnapshotDataSourceSlug(snapshot); dataSourceSlug != "" {
		return dataSourceSlug
	}

	return "-"
}

// PipelineRunSinkOutputView carries safe final sink output metadata for run detail.
type PipelineRunSinkOutputView struct {
	Visible       bool
	Available     bool
	Step          string
	DataSource    string
	Operation     string
	LocationLabel string
	TargetPath    string
	FileMetadata  string
	RowCount      *int
}

func runSinkOutputView(snapshot *api.PipelineRunStepSnapshot) PipelineRunSinkOutputView {
	if snapshot == nil || runStepKind(snapshot) != failureSideSink {
		return PipelineRunSinkOutputView{}
	}

	if details, ok := runRowsOutputDetails(snapshot.Output); ok {
		return PipelineRunSinkOutputView{
			Visible: true, Available: true, Step: snapshot.Step,
			DataSource: runSinkDataSourceLabel(snapshot), Operation: runOperationLabel(snapshot),
			RowCount: &details.RowCount,
		}
	}
	if !runIsFileObjectSinkSnapshot(snapshot) {
		return PipelineRunSinkOutputView{}
	}

	targetPath := runSinkOutputTargetPath(snapshot)
	return PipelineRunSinkOutputView{
		Visible:       true,
		Available:     targetPath != "",
		Step:          snapshot.Step,
		DataSource:    runSinkDataSourceLabel(snapshot),
		Operation:     runOperationLabel(snapshot),
		LocationLabel: runSinkOutputLocationLabel(snapshot),
		TargetPath:    targetPath,
		FileMetadata:  runFileMetadataSummary(snapshot.Output),
	}
}

// runStepKind returns the captured step kind, treating a missing API value as
// unknown so older run snapshots do not render sink-only metadata.
func runStepKind(snapshot *api.PipelineRunStepSnapshot) string {
	if snapshot == nil || snapshot.Kind == nil {
		return ""
	}

	return strings.TrimSpace(string(*snapshot.Kind))
}

// runIsFileObjectSinkSnapshot reports whether the snapshot comes from a
// connector family that can return output path metadata for a sink write.
func runIsFileObjectSinkSnapshot(snapshot *api.PipelineRunStepSnapshot) bool {
	connector := runSinkOutputConnector(snapshot)
	return connector == "ftp" || connector == "git" || connector == "s3" || connector == "google_workspace" || connector == "workspace_file"
}

// runSinkOutputConnector returns the best connector identity for labeling sink
// metadata, preferring the execution output type over configured operation type.
func runSinkOutputConnector(snapshot *api.PipelineRunStepSnapshot) string {
	if snapshot == nil {
		return ""
	}
	if snapshot.Output != nil {
		if value := strings.ToLower(strings.TrimSpace(snapshot.Output.VendorType)); value != "" {
			return value
		}
		if value := strings.ToLower(strings.TrimSpace(snapshot.Output.ResultType)); value != "" {
			return value
		}
	}
	if snapshot.Input.OperationType != nil {
		return strings.ToLower(strings.TrimSpace(*snapshot.Input.OperationType))
	}
	return ""
}

// runSinkOutputLocationLabel returns the operator-facing label for the
// connector-specific location value, falling back to a generic label.
func runSinkOutputLocationLabel(snapshot *api.PipelineRunStepSnapshot) string {
	switch runSinkOutputConnector(snapshot) {
	case "s3":
		return "Object key"
	case "google_workspace", "workspace_file":
		return "File ID"
	case "git":
		return "Repository path"
	case "ftp":
		return "Path"
	default:
		return "Output location"
	}
}

// runSinkOutputTargetPath returns the captured object key or path, treating
// missing output metadata as unavailable instead of rendering a blank value.
func runSinkOutputTargetPath(snapshot *api.PipelineRunStepSnapshot) string {
	if snapshot == nil {
		return ""
	}
	details, ok := runFileWriteOutputDetails(snapshot.Output)
	if !ok {
		return ""
	}

	return strings.TrimSpace(details.TargetPath)
}

// runSinkDataSourceLabel returns the sink data source slug or a placeholder
// when older snapshots did not capture data source metadata.
func runSinkDataSourceLabel(snapshot *api.PipelineRunStepSnapshot) string {
	dataSourceSlug := runSnapshotDataSourceSlug(snapshot)
	if dataSourceSlug == "" {
		return "-"
	}

	return dataSourceSlug
}

func runSnapshotDataSourceSlug(snapshot *api.PipelineRunStepSnapshot) string {
	if snapshot == nil || snapshot.Input.DataSourceSlug == nil {
		return ""
	}
	return strings.TrimSpace(*snapshot.Input.DataSourceSlug)
}

// runFileMetadataSummary returns a compact metadata-only file summary without
// including bytes or raw object content.
func runFileMetadataSummary(output *api.PipelineRunStepOutputSnapshot) string {
	details, ok := runFileWriteOutputDetails(output)
	if !ok || details.File == nil {
		return ""
	}

	parts := make([]string, 0, 3)
	if details.File.Filename != nil && strings.TrimSpace(*details.File.Filename) != "" {
		parts = append(parts, strings.TrimSpace(*details.File.Filename))
	}
	if strings.TrimSpace(details.File.MimeType) != "" {
		parts = append(parts, strings.TrimSpace(details.File.MimeType))
	}
	if details.File.SizeBytes >= 0 {
		parts = append(parts, strconv.FormatInt(details.File.SizeBytes, 10)+" bytes")
	}
	return strings.Join(parts, " / ")
}

// runOutputRowCount returns row evidence from typed run-output details when present.
func runOutputRowCount(output *api.PipelineRunStepOutputSnapshot) *int {
	if rows, ok := runRowsOutputDetails(output); ok {
		rowCount := rows.RowCount
		return &rowCount
	}
	if fileRead, ok := runFileReadOutputDetails(output); ok {
		return fileRead.RowCount
	}
	return nil
}

// runRowsOutputDetails decodes row-shaped run-output details.
func runRowsOutputDetails(output *api.PipelineRunStepOutputSnapshot) (*api.PipelineRunRowsOutputDetails, bool) {
	if runOutputDetailsKind(output) != "rows" {
		return nil, false
	}
	details, err := output.Details.AsPipelineRunRowsOutputDetails()
	if err != nil {
		return nil, false
	}
	return &details, true
}

// runFileReadOutputDetails decodes file-read run-output details.
func runFileReadOutputDetails(output *api.PipelineRunStepOutputSnapshot) (*api.PipelineRunFileReadOutputDetails, bool) {
	if runOutputDetailsKind(output) != "file_read" {
		return nil, false
	}
	details, err := output.Details.AsPipelineRunFileReadOutputDetails()
	if err != nil {
		return nil, false
	}
	return &details, true
}

// runFileWriteOutputDetails decodes file-write run-output details.
func runFileWriteOutputDetails(output *api.PipelineRunStepOutputSnapshot) (*api.PipelineRunFileWriteOutputDetails, bool) {
	if runOutputDetailsKind(output) != "file_write" {
		return nil, false
	}
	details, err := output.Details.AsPipelineRunFileWriteOutputDetails()
	if err != nil {
		return nil, false
	}
	return &details, true
}

// runOutputDetailsKind returns the details discriminator for a run step output.
func runOutputDetailsKind(output *api.PipelineRunStepOutputSnapshot) string {
	if output == nil {
		return ""
	}
	kind, err := output.Details.Discriminator()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(kind)
}

func buildRunsListHrefWithProject(
	orgSlug string,
	projectSlug string,
	cursor string,
	direction string,
	statusFilter string,
	pipelineSlugFilter string,
	modeFilter string,
	backfillSeriesFilter string,
) string {
	values := url.Values{}
	if strings.TrimSpace(cursor) != "" {
		values.Set("cursor", cursor)
	}
	if strings.TrimSpace(direction) != "" {
		values.Set("direction", direction)
	}
	if strings.TrimSpace(statusFilter) != "" {
		values.Set("status", statusFilter)
	}
	if strings.TrimSpace(pipelineSlugFilter) != "" {
		values.Set("pipeline_slug", pipelineSlugFilter)
	}
	if strings.TrimSpace(modeFilter) != "" {
		values.Set("mode", modeFilter)
	}
	if strings.TrimSpace(backfillSeriesFilter) != "" {
		values.Set("backfill_series_slug", backfillSeriesFilter)
	}

	trimmedProjectSlug := strings.TrimSpace(projectSlug)
	if trimmedProjectSlug == "" {
		return "/organizations/" + strings.TrimSpace(orgSlug) + "/projects"
	}

	base := "/orgs/" + strings.TrimSpace(orgSlug) + "/projects/" + trimmedProjectSlug + "/runs"
	encoded := values.Encode()
	if encoded == "" {
		return base
	}

	return base + "?" + encoded
}

func buildEnvironmentRunsListHrefWithProject(
	orgSlug string,
	projectSlug string,
	environmentSlug string,
	cursor string,
	direction string,
	statusFilter string,
	pipelineSlugFilter string,
	extraFilters ...string,
) string {
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
	if strings.TrimSpace(statusFilter) != "" {
		values.Set("status", statusFilter)
	}
	if strings.TrimSpace(pipelineSlugFilter) != "" {
		values.Set("pipeline_slug", pipelineSlugFilter)
	}
	if len(extraFilters) > 0 && strings.TrimSpace(extraFilters[0]) != "" {
		values.Set("mode", extraFilters[0])
	}
	if len(extraFilters) > 1 && strings.TrimSpace(extraFilters[1]) != "" {
		values.Set("backfill_series_slug", extraFilters[1])
	}

	trimmedProjectSlug := strings.TrimSpace(projectSlug)
	if trimmedProjectSlug == "" {
		return "/organizations/" + strings.TrimSpace(orgSlug) + "/projects"
	}

	base := "/orgs/" + strings.TrimSpace(orgSlug) + "/projects/" + trimmedProjectSlug + "/runs"
	encoded := values.Encode()
	if encoded == "" {
		return base
	}

	return base + "?" + encoded
}

func runSelectionHrefWithFilters(
	orgSlug string,
	projectSlug string,
	environmentSlug string,
	runSlug string,
	cursor string,
	direction string,
	statusFilter string,
	pipelineSlugFilter string,
	modeFilter string,
	backfillSeriesFilter string,
) string {
	values := url.Values{}
	if strings.TrimSpace(environmentSlug) != "" {
		values.Set("environment_slug", strings.TrimSpace(environmentSlug))
	}
	if strings.TrimSpace(runSlug) != "" {
		values.Set("run_slug", strings.TrimSpace(runSlug))
	}
	if strings.TrimSpace(cursor) != "" {
		values.Set("cursor", strings.TrimSpace(cursor))
	}
	if strings.TrimSpace(direction) != "" {
		values.Set("direction", strings.TrimSpace(direction))
	}
	if strings.TrimSpace(statusFilter) != "" {
		values.Set("status", strings.TrimSpace(statusFilter))
	}
	if strings.TrimSpace(pipelineSlugFilter) != "" {
		values.Set("pipeline_slug", strings.TrimSpace(pipelineSlugFilter))
	}
	if strings.TrimSpace(modeFilter) != "" {
		values.Set("mode", strings.TrimSpace(modeFilter))
	}
	if strings.TrimSpace(backfillSeriesFilter) != "" {
		values.Set("backfill_series_slug", strings.TrimSpace(backfillSeriesFilter))
	}

	trimmedProjectSlug := strings.TrimSpace(projectSlug)
	if trimmedProjectSlug == "" {
		return "/organizations/" + strings.TrimSpace(orgSlug) + "/projects"
	}

	base := "/orgs/" + strings.TrimSpace(orgSlug) + "/projects/" + trimmedProjectSlug + "/runs"
	encoded := values.Encode()
	if encoded == "" {
		return base
	}

	return base + "?" + encoded
}

func buildEnvironmentRunDetailHref(orgSlug, projectSlug, environmentSlug, runSlug string) string {
	trimmedProjectSlug := strings.TrimSpace(projectSlug)
	if trimmedProjectSlug == "" {
		return "/organizations/" + strings.TrimSpace(orgSlug) + "/projects"
	}

	base := "/orgs/" + strings.TrimSpace(orgSlug) + "/projects/" + trimmedProjectSlug + "/runs/" + strings.TrimSpace(runSlug)
	if strings.TrimSpace(environmentSlug) == "" {
		return base
	}

	values := url.Values{}
	values.Set("environment_slug", strings.TrimSpace(environmentSlug))
	return base + "?" + values.Encode()
}

const (
	eventTypeStepStarted         = "step_started"
	eventTypeStepFailed          = "step_failed"
	eventTypeRunStarted          = "run_started"
	eventTypeRunCompleted        = "run_completed"
	eventTypeCheckpointObserved  = "checkpoint_observed"
	eventTypeCheckpointCommitted = "checkpoint_committed"
	eventTypeCheckpointBlocked   = "checkpoint_blocked"
	eventTypeCheckpointSkipped   = "checkpoint_skipped"
	statusFailed                 = "failed"
)

func runModeBadgeClass(mode api.PipelineRunMode) string {
	switch mode {
	case api.Backfill:
		return eventBadgeInfo
	case api.Incremental:
		return badgePrimaryOutline
	case api.Full:
		return "badge badge-ghost"
	default:
		return eventBadgeOutline
	}
}

func checkpointStatusBadgeClass(status api.PipelineRunCheckpointStatus) string {
	switch status {
	case api.PipelineRunCheckpointStatusCommitted:
		return eventBadgeSuccess
	case api.PipelineRunCheckpointStatusBlocked:
		return eventBadgeError
	case api.PipelineRunCheckpointStatusSkipped:
		return "badge badge-warning badge-outline"
	case api.PipelineRunCheckpointStatusUnchanged:
		return eventBadgeInfo
	default:
		return "badge badge-ghost"
	}
}

func runCheckpointStatusVisible(status api.PipelineRunCheckpointStatus) bool {
	trimmed := strings.TrimSpace(string(status))
	return trimmed != "" && status != api.PipelineRunCheckpointStatusNotApplicable
}

func runIncrementalDiagnosticsVisible(run api.PipelineRunRecord) bool {
	incremental := run.Metadata.Incremental
	return incremental.Mode != api.Full ||
		runCheckpointStatusVisible(incremental.CheckpointStatus) ||
		incremental.BlockedReason != "" ||
		incremental.ReplayBlockedReason != "" ||
		len(run.IncrementalDiagnostics.Sources) > 0
}

func pipelineRunListItemStateClass(runSlug, selectedRunSlug string) string {
	if strings.TrimSpace(runSlug) == strings.TrimSpace(selectedRunSlug) {
		return "border-primary/60 bg-primary/15 shadow-sm shadow-primary/10"
	}
	return "border-base-300 bg-base-100"
}

func retryAttemptStatusBadgeClass(status api.PipelineTriggerRetryAttemptStatus) string {
	switch status {
	case api.PipelineTriggerRetryAttemptStatusQueued, api.PipelineTriggerRetryAttemptStatusRunning:
		return "badge badge-warning badge-outline"
	case api.PipelineTriggerRetryAttemptStatusSucceeded:
		return eventBadgeSuccess
	case api.PipelineTriggerRetryAttemptStatusFailed, api.PipelineTriggerRetryAttemptStatusExhausted:
		return eventBadgeError
	case api.PipelineTriggerRetryAttemptStatusSkipped:
		return "badge badge-ghost badge-outline"
	default:
		return eventBadgeOutline
	}
}

func retryAttemptDecisionLabel(attempt api.PipelineRunTriggerRetryAttempt) string {
	if attempt.DecisionReason != nil && strings.TrimSpace(*attempt.DecisionReason) != "" {
		return strings.TrimSpace(*attempt.DecisionReason)
	}
	if attempt.ErrorSummary != nil && strings.TrimSpace(*attempt.ErrorSummary) != "" {
		return strings.TrimSpace(*attempt.ErrorSummary)
	}
	if attempt.FailureKind != nil && strings.TrimSpace(string(*attempt.FailureKind)) != "" {
		return string(*attempt.FailureKind)
	}
	return "No decision detail"
}

func runEventBadgeClass(eventType, status string) string {
	switch eventType {
	case eventTypeRunStarted:
		return "badge badge-info badge-sm badge-outline"
	case eventTypeRunCompleted:
		if status == statusFailed {
			return "badge badge-error badge-sm badge-outline"
		}
		return "badge badge-success badge-sm badge-outline"
	case eventTypeCheckpointObserved:
		return "badge badge-info badge-sm badge-outline"
	case eventTypeCheckpointCommitted:
		return "badge badge-success badge-sm badge-outline"
	case eventTypeCheckpointBlocked:
		return "badge badge-warning badge-sm badge-outline"
	case eventTypeCheckpointSkipped:
		return "badge badge-ghost badge-sm badge-outline"
	case eventTypeStepFailed:
		return "badge badge-error badge-sm badge-outline"
	default:
		return "badge badge-ghost badge-sm badge-outline"
	}
}

func runEventTypeLabel(eventType string) string {
	switch eventType {
	case eventTypeRunStarted:
		return "Run started"
	case eventTypeRunCompleted:
		return "Run completed"
	case eventTypeStepStarted:
		return "Step started"
	case eventTypeStepFailed:
		return "Step failed"
	case "step_succeeded":
		return "Step succeeded"
	case eventTypeCheckpointObserved:
		return "Checkpoint observed"
	case eventTypeCheckpointCommitted:
		return "Checkpoint committed"
	case eventTypeCheckpointBlocked:
		return "Checkpoint blocked"
	case eventTypeCheckpointSkipped:
		return "Checkpoint skipped"
	default:
		return eventType
	}
}

func runTimelineDotClass(eventType, status string) string {
	base := "h-3 w-3 rounded-full"
	switch eventType {
	case eventTypeRunStarted:
		return base + " bg-info"
	case eventTypeRunCompleted:
		if status == statusFailed {
			return base + " bg-error"
		}
		return base + " bg-success"
	case eventTypeStepFailed:
		return base + " bg-error"
	case eventTypeCheckpointObserved:
		return base + " bg-info"
	case eventTypeCheckpointCommitted:
		return base + " bg-success"
	case eventTypeCheckpointBlocked:
		return base + " bg-warning"
	case eventTypeCheckpointSkipped:
		return base + " bg-base-300"
	default:
		return base + " bg-base-300"
	}
}

func runStepSnapshot(run api.PipelineRunRecord, stepSlug string) *api.PipelineRunStepSnapshot {
	for i := range run.Timeline.Snapshots {
		if run.Timeline.Snapshots[i].Step == stepSlug {
			return &run.Timeline.Snapshots[i]
		}
	}
	return nil
}

func failureKindLabel(kind string) string {
	switch kind {
	case failureKindDataSource:
		return "Data source error"
	case failureKindPlatform:
		return "Platform error"
	default:
		return kind
	}
}

func failureKindBadgeClass(kind string) string {
	switch kind {
	case failureKindDataSource:
		return "badge badge-sm badge-warning"
	case failureKindPlatform:
		return "badge badge-sm badge-error"
	default:
		return "badge badge-sm badge-ghost"
	}
}

func failureSideLabel(side string) string {
	switch side {
	case failureSideSource:
		return "Source step"
	case failureSideSink:
		return "Sink step"
	case failureSideTransform:
		return "Transform step"
	case failureSideConnector:
		return "Connector"
	default:
		return side
	}
}

func runDetailSection(ctx context.Context) string {
	switch section := entityPageURL(ctx).Query().Get("section"); section {
	case "steps", "attempts", "completion", "connected", "definition":
		return section
	case "outputs":
		return "steps"
	case "outcome", "metadata", "overview":
		return "overview"
	default:
		if entityPageURL(ctx).Query().Get("lineage_step") != "" {
			return "steps"
		}
		return "overview"
	}
}

func runSectionHref(ctx context.Context, detailHref, section string) string {
	u, _ := url.Parse(entitySectionHref(ctx, detailHref, section))
	q := u.Query()
	for _, key := range []string{"connected_run", "lineage_step", "definition_input", "definition_port", "definition_view", "definition_scope", "spec_hash"} {
		if value := entityPageURL(ctx).Query().Get(key); value != "" {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func runSinkSnapshots(run api.PipelineRunRecord, lineage runlineage.View) []api.PipelineRunStepSnapshot {
	var sinks []api.PipelineRunStepSnapshot
	seen := make(map[string]bool)
	for _, snapshot := range run.Timeline.Snapshots {
		if snapshot.Kind != nil && *snapshot.Kind == "sink" {
			sinks = append(sinks, snapshot)
			seen[snapshot.Step] = true
		}
	}
	for _, node := range lineage.Nodes {
		if node.Kind == "sink" && !seen[node.Slug] {
			kind := api.PipelineStepKindSink
			snapshot := api.PipelineRunStepSnapshot{Step: node.Slug}
			if node.Snapshot != nil {
				snapshot = *node.Snapshot
			}
			snapshot.Kind = &kind
			sinks = append(sinks, snapshot)
		}
	}
	return sinks
}

func runSinkDeliveryLabel(input api.PipelineRunStepInputSnapshot) string {
	if input.SinkDeliveryOutcome == nil {
		return "Outcome unavailable"
	}
	switch *input.SinkDeliveryOutcome {
	case api.PipelineRunStepInputSnapshotSinkDeliveryOutcomeConfirmed:
		return "Acknowledged by sink"
	case api.PipelineRunStepInputSnapshotSinkDeliveryOutcomeFailed:
		return "Failed before dispatch"
	case api.PipelineRunStepInputSnapshotSinkDeliveryOutcomeUnknown:
		return "Unknown delivery"
	case api.PipelineRunStepInputSnapshotSinkDeliveryOutcomeNoOp:
		return "No-op: empty input"
	default:
		return "Outcome unavailable"
	}
}

func runSinkRecoveryHint(input api.PipelineRunStepInputSnapshot) string {
	if input.SinkDeliveryOutcome != nil {
		switch *input.SinkDeliveryOutcome {
		case api.PipelineRunStepInputSnapshotSinkDeliveryOutcomeConfirmed:
			if input.SinkReplayStrategy != nil && *input.SinkReplayStrategy == "unsafe" {
				return "Completed unsafe effect. Recovery is blocked; reconcile the provider outcome without starting a new run to bypass replay safety."
			}
			return "Completion is recorded. Recovery preserves the effect record and skips repeat-safe completed writes."
		case api.PipelineRunStepInputSnapshotSinkDeliveryOutcomeNoOp:
			return "No provider write was made. Review the empty-input policy if data was expected."
		case api.PipelineRunStepInputSnapshotSinkDeliveryOutcomeFailed:
			return "No dispatch began for this attempt. Correct the configuration before recovery."
		case api.PipelineRunStepInputSnapshotSinkDeliveryOutcomeUnknown:
			break
		}
	}
	if input.SinkReplayStrategy != nil {
		switch *input.SinkReplayStrategy {
		case api.PipelineRunStepInputSnapshotSinkReplayStrategyNaturallyIdempotent, api.PipelineRunStepInputSnapshotSinkReplayStrategyIdempotentWithKey:
			return "Recovery can repeat the same effect with pinned inputs. Verify the provider target and replay guarantee."
		case api.PipelineRunStepInputSnapshotSinkReplayStrategyUnsafe, api.PipelineRunStepInputSnapshotSinkReplayStrategyDeduplicatedByLedger:
			return "Automatic recovery is blocked after an unknown attempt. Reconcile the provider outcome; do not start a new run to bypass this refusal."
		}
	}
	return "Delivery evidence is unavailable. Verify the provider outcome before repeating this write."
}

func runFailureLocation(failure *api.PipelineRunFailureContext) string {
	if failure != nil && strings.TrimSpace(failure.Step) != "" {
		return "Failed at step " + failure.Step
	}
	if failure != nil && failure.Kind == "platform" {
		return "Platform failure"
	}
	return "Run failure"
}

func runFailureSummary(failure *api.PipelineRunFailureContext) string {
	if failure == nil {
		return ""
	}
	if contract := failure.Contract; contract != nil && strings.TrimSpace(contract.FieldName) != "" {
		return runContractFailureSummary(contract)
	}
	if failure.ReasonCode != nil && *failure.ReasonCode == api.SourceResultShapeMismatch &&
		strings.HasSuffix(strings.TrimSpace(failure.Error), "structure_type") {
		return "A source result value has the wrong type. The field and expected/actual types were not recorded."
	}
	if failure.ReasonCode != nil && *failure.ReasonCode == api.S3ObjectWriteFailed &&
		strings.TrimSpace(failure.Error) == "s3 object sink write failed" {
		return "S3 write failed. No provider error code or response was recorded."
	}
	if diagnostic := strings.TrimSpace(failure.Error); diagnostic != "" {
		return diagnostic
	}
	if failure.ReasonCode != nil {
		return strings.ReplaceAll(string(*failure.ReasonCode), "_", " ")
	}
	return failureKindLabel(string(failure.Kind)) + " · see technical diagnostic"
}

func runFailureActionHint(failure *api.PipelineRunFailureContext) string {
	if failure == nil {
		return ""
	}
	if failure.ReasonCode != nil {
		switch *failure.ReasonCode {
		case api.SourceResultShapeMismatch:
			if failure.Contract == nil || strings.TrimSpace(failure.Contract.FieldName) == "" {
				return "No field-level mismatch was recorded. Compare the saved query result fields and value types with the executed output contract."
			}
		case api.S3ObjectWriteFailed:
			if failure.ActionHint == nil || *failure.ActionHint == "The data source returned an unexpected error. Check the data source status and credentials." {
				return "Review the executed S3 write, saved query and connection. Reconcile the object at the provider before repeating the write; a failed request does not prove that nothing was written."
			}
		case api.WorkerOwnershipLost:
			if failure.ActionHint == nil {
				return "Wait for native queue rescue and terminal reconciliation. Recovery requires fenced ownership, complete retained inputs and repeat-safe sinks; otherwise reconcile provider effects before a new evaluation."
			}
		case api.DataSourceCredentialsFailed, api.DataSourcePermissionDenied, api.DataSourceConnectionFailed, api.DataSourceTimeout, api.ResourceScopeRejected, api.SinkContractMismatch,
			api.SourceQueryFailed, api.SourceQueryInvalid, api.SourceSchemaReferenceInvalid, api.TransformContractMismatch, api.TransformRuntimeFailed,
			api.EventInputRejected, api.DestinationConstraintViolation:
		}
	}
	if failure.ActionHint != nil {
		return *failure.ActionHint
	}
	return ""
}

type runTimezoneKey struct{}

// WithRunTimezone applies the authenticated user's configured display timezone.
func WithRunTimezone(ctx context.Context, timezone string) context.Context {
	location, err := time.LoadLocation(strings.TrimSpace(timezone))
	if err != nil || timezone == "" {
		location = time.UTC
	}
	return context.WithValue(ctx, runTimezoneKey{}, location)
}

func runTimelineTimestamp(ctx context.Context, at time.Time) string {
	if at.IsZero() {
		return "Time unavailable"
	}
	location, ok := ctx.Value(runTimezoneKey{}).(*time.Location)
	if !ok {
		location = time.UTC
	}
	return at.In(location).Format("02 Jan 2006, 15:04:05.000 MST (UTC-07:00)")
}

func runSinkBlocked(lineage runlineage.View, step string) bool {
	for _, node := range lineage.Nodes {
		if node.Slug == step {
			return node.Blocked && node.StartedAt == nil && node.Snapshot == nil && node.Failure == nil
		}
	}
	return false
}

func runContractFailureSummary(contract *api.PipelineRunContractFailureDiagnostic) string {
	if contract == nil || strings.TrimSpace(contract.FieldName) == "" {
		return "Contract mismatch; field and rule were not recorded"
	}
	summary := "Field " + contract.FieldName
	if rule := strings.TrimSpace(contract.Rule); rule != "" && rule != "unknown" {
		return summary + " · " + rule
	}
	return summary + " · contract mismatch; precise rule not recorded"
}
