//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=PipelineCompletionCondition -transform=snake -output=pipeline_completion_condition_string.go -text -json -linecomment
//go:generate enumer -type=PipelineTriggerType -transform=snake -output=pipeline_trigger_type_string.go -text -json -linecomment
//go:generate enumer -type=PipelineTriggerVersionPolicyMode -transform=snake -output=pipeline_trigger_version_policy_mode_string.go -text -json -linecomment
//go:generate enumer -type=PipelineTriggerBackoffStrategy -transform=snake -output=pipeline_trigger_backoff_strategy_string.go -text -json -linecomment
//go:generate enumer -type=PipelineTriggerRetryFailureKind -transform=snake -output=pipeline_trigger_retry_failure_kind_string.go -text -json -linecomment
//go:generate enumer -type=PipelineTriggerRetryAttemptStatus -transform=snake -output=pipeline_trigger_retry_attempt_status_string.go -text -json -linecomment
//go:generate enumer -type=PipelineScheduleFireStatus -transform=snake -output=pipeline_schedule_fire_status_string.go -text -json -linecomment
//go:generate enumer -type=PipelineTriggerScheduleMisfirePolicy,PipelineTriggerScheduleMisfireResult -transform=snake -output=pipeline_trigger_schedule_misfire_policy_string.go -text -json -linecomment
package model

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// PipelineTriggerType identifies how a persisted trigger requests a pipeline run.
type PipelineTriggerType int

const (
	PipelineTriggerTypeUnknown    PipelineTriggerType = iota // unknown
	PipelineTriggerTypeSchedule                              // schedule
	PipelineTriggerTypeWebhook                               // webhook
	PipelineTriggerTypeCompletion                            // completion
)

type PipelineCompletionCondition int

const (
	PipelineCompletionConditionUnknown      PipelineCompletionCondition = iota // unknown
	PipelineCompletionConditionSuccess                                         // success
	PipelineCompletionConditionFinalFailure                                    // final_failure
)

type PipelineTriggerCompletionConfig struct {
	UpstreamPipelineSlug  slug.Slug                   `json:"upstream_pipeline_slug" validate:"required"`
	Condition             PipelineCompletionCondition `json:"condition" validate:"required"`
	ExecutionGrantEnabled bool                        `json:"execution_grant_enabled"`
}

// PipelineTriggerVersionPolicyMode controls which deployed version a trigger uses.
type PipelineTriggerVersionPolicyMode int

const (
	PipelineTriggerVersionPolicyModeUnknown        PipelineTriggerVersionPolicyMode = iota // unknown
	PipelineTriggerVersionPolicyModeLatestDeployed                                         // latest_deployed
	PipelineTriggerVersionPolicyModePinned                                                 // pinned
)

// PipelineTriggerBackoffStrategy controls automatic retry delay behavior.
type PipelineTriggerBackoffStrategy int

const (
	PipelineTriggerBackoffStrategyUnknown     PipelineTriggerBackoffStrategy = iota // unknown
	PipelineTriggerBackoffStrategyNone                                              // none
	PipelineTriggerBackoffStrategyFixed                                             // fixed
	PipelineTriggerBackoffStrategyExponential                                       // exponential
)

// PipelineTriggerRetryFailureKind identifies run failure categories eligible for retry.
type PipelineTriggerRetryFailureKind int

const (
	PipelineTriggerRetryFailureKindUnknown    PipelineTriggerRetryFailureKind = iota // unknown
	PipelineTriggerRetryFailureKindDataSource                                        // data_source
	PipelineTriggerRetryFailureKindPlatform                                          // platform
)

// PipelineTriggerRetryAttemptStatus identifies the persisted retry decision or execution state.
type PipelineTriggerRetryAttemptStatus int

const (
	PipelineTriggerRetryAttemptStatusUnknown   PipelineTriggerRetryAttemptStatus = iota // unknown
	PipelineTriggerRetryAttemptStatusQueued                                             // queued
	PipelineTriggerRetryAttemptStatusSkipped                                            // skipped
	PipelineTriggerRetryAttemptStatusExhausted                                          // exhausted
	PipelineTriggerRetryAttemptStatusRunning                                            // running
	PipelineTriggerRetryAttemptStatusSucceeded                                          // succeeded
	PipelineTriggerRetryAttemptStatusFailed                                             // failed
)

// PipelineScheduleFireStatus identifies the persisted state of a scheduled fire.
type PipelineScheduleFireStatus int

const (
	PipelineScheduleFireStatusUnknown   PipelineScheduleFireStatus = iota // unknown
	PipelineScheduleFireStatusClaimed                                     // claimed
	PipelineScheduleFireStatusRunning                                     // running
	PipelineScheduleFireStatusSucceeded                                   // succeeded
	PipelineScheduleFireStatusFailed                                      // failed
	PipelineScheduleFireStatusSkipped                                     // skipped
)

// PipelineTriggerScheduleMisfirePolicy controls how late scheduled fires will be handled.
type PipelineTriggerScheduleMisfirePolicy int

const (
	PipelineTriggerScheduleMisfirePolicyUnknown  PipelineTriggerScheduleMisfirePolicy = iota // unknown
	PipelineTriggerScheduleMisfirePolicySkip                                                 // skip
	PipelineTriggerScheduleMisfirePolicyFireOnce                                             // fire_once
)

// PipelineTriggerScheduleMisfireResult records how a scheduled fire was handled.
type PipelineTriggerScheduleMisfireResult int

const (
	PipelineTriggerScheduleMisfireResultUnknown  PipelineTriggerScheduleMisfireResult = iota // unknown
	PipelineTriggerScheduleMisfireResultOnTime                                               // on_time
	PipelineTriggerScheduleMisfireResultSkipped                                              // skipped
	PipelineTriggerScheduleMisfireResultFireOnce                                             // fire_once
)

// PipelineTriggerVersionPolicy is the pipeline version selector stored on a trigger.
type PipelineTriggerVersionPolicy struct {
	Mode              PipelineTriggerVersionPolicyMode `validate:"required"`
	PinnedVersionHash *string
}

// PipelineTriggerRetryPolicy is the persisted automatic retry policy for a trigger.
type PipelineTriggerRetryPolicy struct {
	Enabled               bool
	MaxAttempts           int                               `validate:"min=1,max=10"`
	BackoffStrategy       PipelineTriggerBackoffStrategy    `validate:"required"`
	InitialDelaySeconds   int                               `validate:"min=0,max=86400"`
	MaxDelaySeconds       int                               `validate:"min=0,max=604800"`
	RetryableFailureKinds []PipelineTriggerRetryFailureKind `validate:"dive,required"`
	MaxElapsedSeconds     *int                              `validate:"omitempty,min=1,max=2592000"`
}

// PipelineTriggerScheduleConfig is the persisted schedule configuration.
type PipelineTriggerScheduleConfig struct {
	Cron          string                               `validate:"required,min=1,max=255"`
	Timezone      string                               `validate:"required,min=1,max=64"`
	MisfirePolicy PipelineTriggerScheduleMisfirePolicy `validate:"required"`
}

type PipelineWebhookInputMapping struct {
	JSONPointer     string `json:"json_pointer" validate:"max=500"`
	RuntimeInputKey string `json:"runtime_input_key" validate:"required,max=100"`
}

type PipelineTriggerWebhookConfig struct {
	ReceiverID           string
	VerificationStrategy string
	InputMappings        []PipelineWebhookInputMapping `validate:"max=100,dive"`
	EnabledKeyVersions   []int
	SecretConfigured     bool
}

type PipelineWebhookSecret struct {
	KeyVersion int
	Value      string
}

// NewPipelineTrigger is the service input for creating a persisted trigger resource.
type NewPipelineTrigger struct {
	Name             string `validate:"required,min=1,max=255"`
	Slug             slug.Slug
	Type             PipelineTriggerType `validate:"required"`
	Enabled          bool
	PipelineSlug     slug.Slug                    `validate:"required"`
	VersionPolicy    PipelineTriggerVersionPolicy `validate:"required"`
	RuntimeInputKeys []string
	RetryPolicy      PipelineTriggerRetryPolicy `validate:"required"`
	ScheduleConfig   *PipelineTriggerScheduleConfig
	CompletionConfig *PipelineTriggerCompletionConfig
	WebhookConfig    *PipelineTriggerWebhookConfig
}

// UpdatePipelineTrigger is the full service input for replacing a trigger resource.
type UpdatePipelineTrigger struct {
	Name             string              `validate:"required,min=1,max=255"`
	Type             PipelineTriggerType `validate:"required"`
	Enabled          bool
	PipelineSlug     slug.Slug                    `validate:"required"`
	VersionPolicy    PipelineTriggerVersionPolicy `validate:"required"`
	RuntimeInputKeys []string
	RetryPolicy      PipelineTriggerRetryPolicy `validate:"required"`
	ScheduleConfig   *PipelineTriggerScheduleConfig
	CompletionConfig *PipelineTriggerCompletionConfig
	WebhookConfig    *PipelineTriggerWebhookConfig
}

// PipelineTrigger is the safe resource representation returned from service and API calls.
type PipelineTrigger struct {
	Slug             slug.Slug
	Name             string
	Type             PipelineTriggerType
	Enabled          bool
	Revision         int64
	EnvironmentSlug  slug.Slug
	PipelineSlug     slug.Slug
	VersionPolicy    PipelineTriggerVersionPolicy
	RuntimeInputKeys []string
	RetryPolicy      PipelineTriggerRetryPolicy
	ScheduleConfig   *PipelineTriggerScheduleConfig
	CompletionConfig *PipelineTriggerCompletionConfig
	WebhookConfig    *PipelineTriggerWebhookConfig
	WebhookSecret    *PipelineWebhookSecret
	CreatedBy        EventActor
	UpdatedBy        EventActor
	DisabledAt       *time.Time
	Timestamps
}

// PipelineScheduleFire is an operator-safe diagnostic for one persisted scheduled fire.
type PipelineScheduleFire struct {
	FireSlug        slug.Slug
	TriggerSlug     slug.Slug
	TriggerRevision int64
	PipelineSlug    slug.Slug
	ScheduledFireAt time.Time
	ActualFireAt    time.Time
	Status          PipelineScheduleFireStatus
	MisfireResult   PipelineTriggerScheduleMisfireResult
	RunSlug         *slug.Slug
	SourceEventSlug *slug.Slug
	ErrorSummary    *string
	Timestamps
}

// PipelineTriggerRetryAttempt is an operator-safe diagnostic for one trigger retry decision or execution.
type PipelineTriggerRetryAttempt struct {
	AttemptSlug     slug.Slug
	Status          PipelineTriggerRetryAttemptStatus
	DecisionReason  *string
	TriggerSlug     slug.Slug
	TriggerRevision int64
	PipelineSlug    slug.Slug
	RootRunSlug     slug.Slug
	RetryOfRunSlug  slug.Slug
	RunSlug         slug.Slug
	AttemptNumber   int
	FailureKind     *PipelineTriggerRetryFailureKind
	SourceEventSlug *slug.Slug
	ScheduledAt     time.Time
	EnqueuedAt      *time.Time
	StartedAt       *time.Time
	CompletedAt     *time.Time
	ErrorSummary    *string
	Timestamps
}

// CompletionAuthoritySnapshot contains only verified database permission facts.
type CompletionAuthoritySnapshot struct {
	Valid       bool     `json:"valid"`
	Permissions []string `json:"permissions"`
}

// PipelineCompletionSnapshot is internal execution evidence, never a transport DTO.
// It carries no credential or runtime input values.
type PipelineCompletionSnapshot struct {
	ID                  int
	VersionID           int64
	DeploymentID        int64
	ChildRunSlug        slug.Slug
	AncestorPipelines   []string
	RetryPolicy         PipelineTriggerRetryPolicy
	TriggerSlug         slug.Slug
	TriggerRevision     int64
	PipelineSlug        slug.Slug
	RootRunSlug         slug.Slug
	FinalAttemptRunSlug slug.Slug
	VersionHash         string
	DeploymentRevision  int64
	ResolutionDigest    string
	Materialization     []byte
	CausalDepth         int
	State               string
	DecisionReason      string
	ClosureAuthority    CompletionAuthoritySnapshot
	CurrentAuthority    CompletionAuthoritySnapshot
}
