package run

import (
	"time"

	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"

	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// RunMetadata captures stable run information for local execution output.
type RunMetadata struct {
	DeploymentID               *int64                `json:"-"`
	DeploymentRevision         *int64                `json:"-"`
	DeploymentResolutionDigest string                `json:"deployment_resolution_digest,omitempty"`
	ProjectSlug                slug.Slug             `json:"project_slug"`
	EnvironmentSlug            slug.Slug             `json:"environment_slug"`
	RunSlug                    string                `json:"run_slug"`
	Source                     RunSourceSummary      `json:"source"`
	Status                     string                `json:"status"`
	DurationMs                 int64                 `json:"duration_ms"`
	StepCount                  int                   `json:"step_count"`
	PipelineVersionID          int64                 `json:"pipeline_version_id"`
	FailedStep                 *slug.Slug            `json:"failed_step,omitempty"`
	ReplayOfRunSlug            *string               `json:"replay_of_run_slug,omitempty"`
	ReplayFromStep             *string               `json:"replay_from_step,omitempty"`
	Incremental                IncrementalRunSummary `json:"incremental"`
}

// RunSourceSummary stores safe immutable context explaining why a run started.
type RunSourceSummary struct {
	Type            model.PipelineRunSourceType       `json:"type"       validate:"required,runsourcetype"`
	Summary         string                            `json:"summary"    validate:"required"`
	EnvironmentSlug slug.Slug                         `json:"environment_slug,omitempty"`
	Entrypoint      model.PipelineRunSourceEntrypoint `json:"entrypoint" validate:"required,runsourceentrypoint"`
	ActorType       model.EventActorType              `json:"actor_type" validate:"required,eventactortype"`
	ActorSlug       *slug.Slug                        `json:"actor_slug,omitempty"`
	RequestID       *string                           `json:"request_id,omitempty"`
	TraceID         *string                           `json:"trace_id,omitempty"`
	ParameterKeys   []string                          `json:"parameter_keys"`
	SourceEventSlug *slug.Slug                        `json:"source_event_slug,omitempty"`
	AttemptNumber   int                               `json:"attempt_number"`
	RootRunSlug     slug.Slug                         `json:"root_run_slug,omitempty"`
	RetryOfRunSlug  *slug.Slug                        `json:"retry_of_run_slug,omitempty"`
	Schedule        *RunSourceScheduleSummary         `json:"schedule,omitempty"`
	Webhook         *RunSourceWebhookSummary          `json:"webhook,omitempty"`
}

// RunSourceWebhookSummary stores safe webhook delivery context without payload values.
type RunSourceWebhookSummary struct {
	DeliveryRecordID   int64     `json:"-"`
	TriggerSlug        slug.Slug `json:"trigger_slug"        validate:"required"`
	TriggerRevision    int64     `json:"trigger_revision"    validate:"required,min=1"`
	DeliveryID         string    `json:"delivery_id"         validate:"required,max=128"`
	PayloadDigest      string    `json:"payload_digest"      validate:"required,len=64,hexadecimal"`
	PayloadSizeBytes   int64     `json:"payload_size_bytes"  validate:"min=0"`
	ReceivedAt         time.Time `json:"received_at"         validate:"required"`
	VerificationStatus string    `json:"verification_status" validate:"required,eq=verified"`
}

// NormalizeAttemptSource fills the run-attempt linkage required for persisted source summaries.
func NormalizeAttemptSource(source RunSourceSummary, runSlug slug.Slug) RunSourceSummary {
	if source.AttemptNumber <= 0 {
		source.AttemptNumber = 1
	}
	if source.RootRunSlug == "" && source.AttemptNumber == 1 {
		source.RootRunSlug = runSlug
	}
	if source.AttemptNumber == 1 {
		source.RetryOfRunSlug = nil
	} else if source.RetryOfRunSlug == nil && source.RootRunSlug != "" {
		retryOfRunSlug := source.RootRunSlug
		source.RetryOfRunSlug = &retryOfRunSlug
	}
	return source
}

// RunSourceScheduleSummary stores safe schedule trigger context for scheduled runs.
type RunSourceScheduleSummary struct {
	TriggerSlug     slug.Slug                                  `json:"trigger_slug"      validate:"required"`
	TriggerRevision int64                                      `json:"trigger_revision"  validate:"required,min=1"`
	Cron            string                                     `json:"cron"              validate:"required"`
	Timezone        string                                     `json:"timezone"          validate:"required"`
	ScheduledFireAt time.Time                                  `json:"scheduled_fire_at" validate:"required"`
	ActualFireAt    time.Time                                  `json:"actual_fire_at"    validate:"required"`
	MisfireResult   model.PipelineTriggerScheduleMisfireResult `json:"misfire_result"    validate:"required,schedulemisfireresult"`
}

// RunResult contains run metadata, timeline, and per-step execution results.
type RunResult struct {
	Metadata                         RunMetadata                                `json:"metadata"`
	Timeline                         RunTimeline                                `json:"timeline"`
	StepResults                      map[slug.Slug]*StepResult                  `json:"step_results,omitempty"`
	EdgeDiagnostics                  []pipelinevalidate.EdgeCompatibilityResult `json:"edge_diagnostics,omitempty"`
	IncrementalCheckpointTransitions []IncrementalCheckpointTransition          `json:"incremental_checkpoint_transitions,omitempty"`
	IncrementalDiagnostics           IncrementalRunDiagnostics                  `json:"incremental_diagnostics"`
}

// RunTimeline contains a structured event stream and sanitized step snapshots.
type RunTimeline struct {
	SinkReplayIdentity         string                            `json:"sink_replay_identity,omitempty"`
	CompiledPlanDigest         string                            `json:"compiled_plan_digest,omitempty"`
	WorkerObservedAt           *time.Time                        `json:"worker_observed_at,omitempty"`
	Revision                   int64                             `json:"revision,omitempty"`
	ExecutionContext           *pipelineruntime.ExecutionContext `json:"execution_context,omitempty"`
	StructuralChecks           []StructuralCheck                 `json:"structural_checks,omitempty"`
	DeploymentResolutionDigest string                            `json:"deployment_resolution_digest,omitempty"`
	Admissions                 []deploymentplan.Admission        `json:"admissions,omitempty"`
	RunSlug                    string                            `json:"run_slug"`
	PipelineSlug               slug.Slug                         `json:"pipeline_slug"`
	ProjectSlug                slug.Slug                         `json:"project_slug"`
	EnvironmentSlug            slug.Slug                         `json:"environment_slug"`
	Status                     string                            `json:"status"`
	ReplayOfRunSlug            *string                           `json:"replay_of_run_slug,omitempty"`
	Events                     []TimelineEvent                   `json:"events"`
	Snapshots                  []StepSnapshot                    `json:"snapshots"`
	Failure                    *FailureContext                   `json:"failure,omitempty"`
}

// ReplayCheckpoint stores pinned input needed to reproduce a run from a step boundary.
type ReplayCheckpoint struct {
	DeploymentPlan             []byte `json:"deployment_plan,omitempty"`
	DeploymentResolutionDigest string `json:"deployment_resolution_digest,omitempty"`
	CompiledPlan               []byte `json:"compiled_plan,omitempty"`
	CompiledPlanDigest         string `json:"compiled_plan_digest,omitempty"`
	RunSlug                    string
	OrgSlug                    slug.Slug
	ProjectSlug                slug.Slug
	EnvironmentSlug            slug.Slug
	StepSlug                   slug.Slug
	Pipeline                   *pipelinespec.Pipeline
	Results                    map[slug.Slug]*StepResult
	IncrementalSourceWindows   map[slug.Slug]IncrementalSourceWindow
}

// TimelineEvent captures important moments in a pipeline run lifecycle.
type TimelineEvent struct {
	At     time.Time `json:"at"`
	Type   string    `json:"type"`
	Step   slug.Slug `json:"step,omitempty"`
	Status string    `json:"status"`
}

// StepSnapshot stores sanitized input/output metadata for a step.
type StepSnapshot struct {
	Step   slug.Slug           `json:"step"`
	Kind   string              `json:"kind,omitempty"`
	Input  StepInputSnapshot   `json:"input"`
	Output *StepOutputSnapshot `json:"output,omitempty"`
}

// StepInputSnapshot stores sanitized step inputs without parameter values.
type StepInputSnapshot struct {
	OrganizationSlug    slug.Slug          `json:"organization_slug"`
	ProjectSlug         slug.Slug          `json:"project_slug"`
	EnvironmentSlug     slug.Slug          `json:"environment_slug"`
	DataSourceSlug      slug.Slug          `json:"data_source_slug"`
	OperationType       string             `json:"operation_type,omitempty"`
	OperationRef        slug.Slug          `json:"operation_ref,omitempty"`
	UsesInline          bool               `json:"uses_inline,omitempty"`
	ParameterKeys       []string           `json:"parameter_keys"`
	EventInput          *EventInputSummary `json:"event_input,omitempty"`
	SinkReplayStrategy  string             `json:"sink_replay_strategy,omitempty"`
	SinkReplayMethod    string             `json:"sink_replay_method,omitempty"`
	SinkReplayDecision  string             `json:"sink_replay_decision,omitempty"`
	SinkDeliveryOutcome string             `json:"sink_delivery_outcome,omitempty"`
	SinkEffectState     SinkEffectState    `json:"sink_effect_state,omitempty"`
}

// StepOutputSnapshot stores connector-agnostic execution metadata with typed result details.
type StepOutputSnapshot struct {
	ResultType    string             `json:"result_type"`
	VendorType    string             `json:"vendor_type"`
	DurationMs    int64              `json:"duration_ms"`
	OperationType string             `json:"operation_type,omitempty"`
	Details       *StepOutputDetails `json:"details,omitempty"`
}

// FailureContext points to the failing step and a concise error message.
type FailureContext struct {
	OutcomeUncertain         bool                       `json:"outcome_uncertain,omitempty"`
	AutomaticRetryProhibited bool                       `json:"automatic_retry_prohibited,omitempty"`
	Step                     slug.Slug                  `json:"step"`
	Error                    string                     `json:"error"`
	At                       time.Time                  `json:"at"`
	Kind                     string                     `json:"kind,omitempty"`
	Side                     string                     `json:"side,omitempty"`
	ReasonCode               FailureReasonCode          `json:"reason_code,omitempty"`
	DataSourceSlug           string                     `json:"data_source_slug,omitempty"`
	ActionHint               string                     `json:"action_hint,omitempty"`
	Contract                 *ContractFailureDiagnostic `json:"contract,omitempty"`
}

// ContractFailureDiagnostic is a sanitized contract-validation failure summary.
type ContractFailureDiagnostic struct {
	ContractName   string                          `json:"contract_name"`
	FieldName      string                          `json:"field_name"`
	Rule           string                          `json:"rule"`
	ViolationCount int                             `json:"violation_count"`
	ReasonCode     pipelinevalidate.ContractReason `json:"reason_code"`
}

// IncrementalRunSummary is the operator-facing run-level incremental state.
type IncrementalRunSummary struct {
	Mode                string `json:"mode"`
	CheckpointStatus    string `json:"checkpoint_status"`
	SourceStepSlug      string `json:"source_step_slug,omitempty"`
	BlockedReason       string `json:"blocked_reason,omitempty"`
	ReplaySafetyStatus  string `json:"replay_safety_status,omitempty"`
	ReplayBlockedReason string `json:"replay_blocked_reason,omitempty"`
	BackfillSeriesSlug  string `json:"backfill_series_slug,omitempty"`
	BackfillChunkIndex  int    `json:"backfill_chunk_index,omitempty"`
	BackfillChunkCount  int    `json:"backfill_chunk_count,omitempty"`
}

// IncrementalRunDiagnostics contains sanitized per-source checkpoint diagnostics.
type IncrementalRunDiagnostics struct {
	Sources      []IncrementalSourceDiagnostic `json:"sources"`
	ReplaySafety IncrementalReplaySafety       `json:"replay_safety"`
}

// IncrementalSourceDiagnostic summarizes a single incremental source step.
type IncrementalSourceDiagnostic struct {
	SourceStepSlug     string                             `json:"source_step_slug"`
	CheckpointKind     string                             `json:"checkpoint_kind"`
	PreviousCheckpoint IncrementalCheckpointSummary       `json:"previous_checkpoint"`
	Window             IncrementalCheckpointWindowSummary `json:"window"`
	ObservedCheckpoint IncrementalCheckpointSummary       `json:"observed_checkpoint"`
	CommitResult       IncrementalCheckpointCommitResult  `json:"commit_result"`
}

// IncrementalCheckpointSummary is a compact redacted checkpoint display value.
type IncrementalCheckpointSummary struct {
	Summary         string         `json:"summary"`
	DisplayMetadata map[string]any `json:"display_metadata,omitempty"`
	RawValueExposed bool           `json:"raw_value_exposed"`
}

// IncrementalCheckpointWindowSummary summarizes the effective source read window.
type IncrementalCheckpointWindowSummary struct {
	Summary         string         `json:"summary"`
	LowerBound      string         `json:"lower_bound,omitempty"`
	UpperBound      string         `json:"upper_bound,omitempty"`
	DisplayMetadata map[string]any `json:"display_metadata,omitempty"`
	RawValueExposed bool           `json:"raw_value_exposed"`
}

// IncrementalCheckpointCommitResult describes checkpoint promotion for one source.
type IncrementalCheckpointCommitResult struct {
	Status        string `json:"status"`
	BlockedByStep string `json:"blocked_by_step,omitempty"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

// IncrementalReplaySafety exposes why an automatic replay is or is not blocked.
type IncrementalReplaySafety struct {
	Status        string `json:"status"`
	BlockedSink   string `json:"blocked_sink,omitempty"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}
