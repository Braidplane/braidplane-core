package pipelinesvc

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

const (
	maxPipelineDeploymentEntitySlugLength = 100
	pipelineDeploymentEntityHashBytes     = 8
	maxPipelineRunEntitySlugLength        = 100
	pipelineRunEntityHashBytes            = 8
)

type pipelineEvent struct {
	securityAudit bool
	projectSlug   slug.Slug
	pipelineSlug  slug.Slug
	action        model.EventAction
	metadata      map[string]any
}

func (e pipelineEvent) EventRecord() (eventrecorder.Record, error) {
	metadata := map[string]any{
		"project_slug": e.projectSlug.String(),
	}
	for key, value := range e.metadata {
		metadata[key] = value
	}

	record := eventrecorder.Record{
		SlugPrefix: "evt-pipeline-" + slug.Slug(e.action.String()),
		EntityType: model.EventEntityTypePipeline,
		EntitySlug: e.pipelineSlug,
		Action:     e.action,
		Metadata:   metadata,
	}
	if e.securityAudit || e.action == model.EventActionDelete || e.action == model.EventActionPurge {
		record.EventClass = model.EventClassSecurityAudit
		record.SchemaVersion = model.CurrentEventSchemaVersion
	}
	return record, nil
}

type pipelineDeploymentEvent struct {
	projectSlug             slug.Slug
	environmentSlug         slug.Slug
	pipelineSlug            slug.Slug
	currentVersionSpecHash  string
	previousVersionSpecHash *string
	sourceEnvironmentSlug   *slug.Slug
	protectedEnvironment    bool
	proofReceiptSlug        *slug.Slug
}

func (e pipelineDeploymentEvent) EventRecord() (eventrecorder.Record, error) {
	entitySlug, err := pipelineDeploymentEntitySlug(e.projectSlug, e.environmentSlug, e.pipelineSlug)
	if err != nil {
		return eventrecorder.Record{}, err
	}

	metadata := map[string]any{
		"project_slug":                       e.projectSlug.String(),
		"environment_slug":                   e.environmentSlug.String(),
		"pipeline_slug":                      e.pipelineSlug.String(),
		"source_pipeline_version_spec_hash":  e.currentVersionSpecHash,
		"current_pipeline_version_spec_hash": e.currentVersionSpecHash,
	}
	if e.previousVersionSpecHash != nil && *e.previousVersionSpecHash != "" {
		metadata["previous_pipeline_version_spec_hash"] = *e.previousVersionSpecHash
	}
	if e.sourceEnvironmentSlug != nil && *e.sourceEnvironmentSlug != "" {
		metadata["source_environment_slug"] = e.sourceEnvironmentSlug.String()
	}
	if e.protectedEnvironment {
		metadata["protected_environment_confirmed"] = true
	}
	if e.proofReceiptSlug != nil {
		metadata["proof_receipt_slug"] = e.proofReceiptSlug.String()
	}

	return eventrecorder.Record{
		SlugPrefix:    "evt-pipeline-deployment-deploy",
		EventClass:    model.EventClassSecurityAudit,
		EntityType:    model.EventEntityTypePipelineDeployment,
		EntitySlug:    entitySlug,
		Action:        model.EventActionDeploy,
		SchemaVersion: model.CurrentEventSchemaVersion,
		Metadata:      metadata,
	}, nil
}

func pipelineDeploymentEntitySlug(
	projectSlug, environmentSlug, pipelineSlug slug.Slug,
) (slug.Slug, error) {
	base := projectSlug.String() + "-" + environmentSlug.String() + "-" + pipelineSlug.String()
	sum := sha256.Sum256([]byte(base))
	suffix := hex.EncodeToString(sum[:pipelineDeploymentEntityHashBytes])

	maxBaseLength := maxPipelineDeploymentEntitySlugLength - len(suffix) - 1
	if len(base) > maxBaseLength {
		base = strings.TrimRight(base[:maxBaseLength], "-")
	}

	entitySlug, err := slug.New(base + "-" + suffix)
	if err != nil {
		return "", xerror.Wrapf("build pipeline deployment entity slug: %w", err)
	}
	return entitySlug, nil
}

type pipelineRunExecutedEvent struct {
	replayOfRunSlug         *string
	projectSlug             slug.Slug
	environmentSlug         slug.Slug
	pipelineSlug            slug.Slug
	runSlug                 slug.Slug
	versionSpecHash         string
	status                  string
	durationMs              int64
	stepCount               int32
	lastStepSlug            string
	sourceType              string
	sourceSummary           string
	sourceEntrypoint        string
	sourceParameterKeys     []string
	scheduleTriggerSlug     slug.Slug
	scheduleTriggerRevision int64
	scheduleCron            string
	scheduleTimezone        string
	scheduleScheduledFireAt time.Time
	scheduleActualFireAt    time.Time
	scheduleMisfireResult   string
	requestID               *string
	traceID                 *string
	protectedEnvironment    bool
}

func (e pipelineRunExecutedEvent) EventRecord() (eventrecorder.Record, error) {
	entitySlug, err := pipelineRunEntitySlug(e.projectSlug, e.environmentSlug, e.runSlug)
	if err != nil {
		return eventrecorder.Record{}, err
	}

	metadata := map[string]any{
		"project_slug":                e.projectSlug.String(),
		"environment_slug":            e.environmentSlug.String(),
		"pipeline_slug":               e.pipelineSlug.String(),
		"pipeline_run_slug":           e.runSlug.String(),
		"pipeline_version_spec_hash":  e.versionSpecHash,
		"pipeline_run_status":         e.status,
		"pipeline_run_duration_ms":    e.durationMs,
		"pipeline_run_step_count":     e.stepCount,
		"pipeline_run_last_step_slug": e.lastStepSlug,
	}
	if e.sourceType != "" {
		metadata["pipeline_run_source_type"] = e.sourceType
	}
	if e.sourceSummary != "" {
		metadata["pipeline_run_source_summary"] = e.sourceSummary
	}
	if e.sourceEntrypoint != "" {
		metadata["pipeline_run_source_entrypoint"] = e.sourceEntrypoint
	}
	if len(e.sourceParameterKeys) > 0 {
		metadata["pipeline_run_source_parameter_keys"] = e.sourceParameterKeys
	}
	if e.scheduleTriggerSlug != "" {
		metadata["schedule_trigger_slug"] = e.scheduleTriggerSlug.String()
		metadata["schedule_trigger_revision"] = e.scheduleTriggerRevision
		metadata["schedule_cron"] = e.scheduleCron
		metadata["schedule_timezone"] = e.scheduleTimezone
		metadata["schedule_scheduled_fire_at"] = e.scheduleScheduledFireAt
		metadata["schedule_actual_fire_at"] = e.scheduleActualFireAt
		metadata["schedule_misfire_result"] = e.scheduleMisfireResult
	}
	if e.requestID != nil && *e.requestID != "" {
		metadata["request_id"] = *e.requestID
	}
	if e.traceID != nil && *e.traceID != "" {
		metadata["trace_id"] = *e.traceID
	}
	if e.replayOfRunSlug != nil {
		metadata["replay_of_run_slug"] = *e.replayOfRunSlug
		metadata["recovery_input_mode"] = "pinned"
	}
	if e.protectedEnvironment {
		metadata["protected_environment_confirmed"] = true
	}

	eventClass := model.EventClassActivity
	if e.replayOfRunSlug != nil {
		eventClass = model.EventClassSecurityAudit
	}
	return eventrecorder.Record{
		SlugPrefix: "evt-pipeline-run-execute",
		EventClass: eventClass,
		EntityType: model.EventEntityTypePipelineRun,
		EntitySlug: entitySlug,
		Action:     model.EventActionExecute,
		Metadata:   metadata,
	}, nil
}

func pipelineRunEntitySlug(
	projectSlug, environmentSlug, runSlug slug.Slug,
) (slug.Slug, error) {
	base := projectSlug.String() + "-" + environmentSlug.String() + "-" + runSlug.String()
	sum := sha256.Sum256([]byte(base))
	suffix := hex.EncodeToString(sum[:pipelineRunEntityHashBytes])

	maxBaseLength := maxPipelineRunEntitySlugLength - len(suffix) - 1
	if len(base) > maxBaseLength {
		base = strings.TrimRight(base[:maxBaseLength], "-")
	}

	entitySlug, err := slug.New(base + "-" + suffix)
	if err != nil {
		return "", xerror.Wrapf("build pipeline run entity slug: %w", err)
	}
	return entitySlug, nil
}
