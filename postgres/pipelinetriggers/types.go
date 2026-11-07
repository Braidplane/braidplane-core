package pipelinetriggers

import (
	"fmt"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

// PipelineTrigger is the repository representation of a persisted trigger definition.
type PipelineTrigger struct {
	Slug             slug.Slug
	Name             string
	Type             model.PipelineTriggerType
	Enabled          bool
	Revision         int64
	EnvironmentSlug  slug.Slug
	PipelineSlug     slug.Slug
	VersionPolicy    model.PipelineTriggerVersionPolicy
	RuntimeInputKeys []string
	RetryPolicy      model.PipelineTriggerRetryPolicy
	ScheduleConfig   *model.PipelineTriggerScheduleConfig
	CompletionConfig *model.PipelineTriggerCompletionConfig
	WebhookConfig    *model.PipelineTriggerWebhookConfig
	CreatedBy        model.EventActor
	UpdatedBy        model.EventActor
	DisabledAt       *time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// CreatePipelineTriggerParams contains validated persistence input for a trigger.
type CreatePipelineTriggerParams struct {
	Slug             slug.Slug
	Name             string
	Type             model.PipelineTriggerType
	Enabled          bool
	PipelineSlug     slug.Slug
	VersionPolicy    model.PipelineTriggerVersionPolicy
	RuntimeInputKeys []string
	RetryPolicy      model.PipelineTriggerRetryPolicy
	ScheduleConfig   *model.PipelineTriggerScheduleConfig
	CompletionConfig *model.PipelineTriggerCompletionConfig
	WebhookConfig    *model.PipelineTriggerWebhookConfig
	Actor            model.EventActor
}

// UpdatePipelineTriggerParams contains validated full replacement input for a trigger.
type UpdatePipelineTriggerParams struct {
	Name             string
	Type             model.PipelineTriggerType
	Enabled          bool
	PipelineSlug     slug.Slug
	VersionPolicy    model.PipelineTriggerVersionPolicy
	RuntimeInputKeys []string
	RetryPolicy      model.PipelineTriggerRetryPolicy
	ScheduleConfig   *model.PipelineTriggerScheduleConfig
	CompletionConfig *model.PipelineTriggerCompletionConfig
	WebhookConfig    *model.PipelineTriggerWebhookConfig
	Actor            model.EventActor
}

// DueScheduleTrigger is the scheduler-facing projection of one enabled schedule trigger.
type DueScheduleTrigger struct {
	OrgSlug             slug.Slug
	ProjectSlug         slug.Slug
	EnvironmentSlug     slug.Slug
	TriggerSlug         slug.Slug
	TriggerRevision     int64
	Enabled             bool
	PipelineSlug        slug.Slug
	VersionPolicy       model.PipelineTriggerVersionPolicy
	RuntimeInputKeys    []string
	RetryPolicy         model.PipelineTriggerRetryPolicy
	ScheduleConfig      model.PipelineTriggerScheduleConfig
	TriggerCreatedAt    time.Time
	LastScheduledFireAt *time.Time
}

// ScheduleFireClaim contains the idempotency key for one scheduled trigger fire.
type ScheduleFireClaim struct {
	OrgSlug         slug.Slug
	ProjectSlug     slug.Slug
	EnvironmentSlug slug.Slug
	TriggerSlug     slug.Slug
	TriggerRevision int64
	ScheduledFireAt time.Time
	ActualFireAt    time.Time
	MisfireResult   model.PipelineTriggerScheduleMisfireResult
}

// FireSlug returns the deterministic external slug for a scheduled fire claim.
func (c ScheduleFireClaim) FireSlug() slug.Slug {
	const maxSlugLength = 100

	timestamp := c.ScheduledFireAt.UTC().Format("20060102t150405z")
	prefix := "fire-"
	separator := "-"
	triggerSlug := c.TriggerSlug.String()
	maxTriggerLength := maxSlugLength - len(prefix) - len(separator) - len(timestamp)
	if len(triggerSlug) > maxTriggerLength {
		triggerSlug = strings.TrimRight(triggerSlug[:maxTriggerLength], "-")
	}
	return slug.Slug(fmt.Sprintf("%s%s%s%s", prefix, triggerSlug, separator, timestamp))
}

// ScheduleFireClaimResult reports whether this dispatcher claimed the fire.
type ScheduleFireClaimResult struct {
	Claimed  bool
	FireSlug slug.Slug
}

// ScheduleFireExecutionRef durably identifies one claimed scheduled fire.
type ScheduleFireExecutionRef struct {
	OrgSlug         slug.Slug
	ProjectSlug     slug.Slug
	EnvironmentSlug slug.Slug
	TriggerSlug     slug.Slug
	TriggerRevision int64
	ScheduledFireAt time.Time
}

// ClaimedScheduleFire contains the trigger snapshot needed to execute one fire.
type ClaimedScheduleFire struct {
	Resuming bool
	Trigger  DueScheduleTrigger
	Claim    ScheduleFireClaim
	FireSlug slug.Slug
}

// ScheduleFireCompletion stores the terminal diagnostic state for a claimed fire.
type ScheduleFireCompletion struct {
	OrgSlug         slug.Slug
	ProjectSlug     slug.Slug
	EnvironmentSlug slug.Slug
	TriggerSlug     slug.Slug
	TriggerRevision int64
	ScheduledFireAt time.Time
	Status          string
	RunSlug         *string
	SourceEventSlug *slug.Slug
	ErrorSummary    *string
}

// ScheduleFireDiagnostic is the safe read projection for schedule-fire visibility surfaces.
type ScheduleFireDiagnostic struct {
	FireSlug        slug.Slug
	TriggerSlug     slug.Slug
	TriggerRevision int64
	PipelineSlug    slug.Slug
	ScheduledFireAt time.Time
	ActualFireAt    time.Time
	Status          model.PipelineScheduleFireStatus
	MisfireResult   model.PipelineTriggerScheduleMisfireResult
	RunSlug         *slug.Slug
	SourceEventSlug *slug.Slug
	ErrorSummary    *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// TriggerRetryAttemptRecord contains the durable retry decision to persist.
type TriggerRetryAttemptRecord struct {
	OrgSlug                 slug.Slug
	ProjectSlug             slug.Slug
	EnvironmentSlug         slug.Slug
	TriggerSlug             slug.Slug
	TriggerRevision         int64
	Status                  model.PipelineTriggerRetryAttemptStatus
	DecisionReason          string
	PipelineSlug            slug.Slug
	VersionPolicy           model.PipelineTriggerVersionPolicy
	PipelineVersionSpecHash string
	RuntimeInputKeys        []string
	RetryPolicy             model.PipelineTriggerRetryPolicy
	Source                  pipelinerun.RunSourceSummary
	RootRunSlug             slug.Slug
	RetryOfRunSlug          slug.Slug
	RunSlug                 slug.Slug
	AttemptNumber           int
	FailureKind             model.PipelineTriggerRetryFailureKind
	SourceEventSlug         *slug.Slug
	ScheduledAt             time.Time
}

// TriggerRetryAttempt is the persisted retry attempt projection used by workers.
type TriggerRetryAttempt struct {
	AttemptSlug             slug.Slug
	Status                  model.PipelineTriggerRetryAttemptStatus
	DecisionReason          string
	OrgSlug                 slug.Slug
	ProjectSlug             slug.Slug
	EnvironmentSlug         slug.Slug
	TriggerSlug             slug.Slug
	TriggerRevision         int64
	PipelineSlug            slug.Slug
	VersionPolicy           model.PipelineTriggerVersionPolicy
	PipelineVersionSpecHash string
	RuntimeInputKeys        []string
	RetryPolicy             model.PipelineTriggerRetryPolicy
	Source                  pipelinerun.RunSourceSummary
	RootRunSlug             slug.Slug
	RetryOfRunSlug          slug.Slug
	RunSlug                 slug.Slug
	AttemptNumber           int
	FailureKind             model.PipelineTriggerRetryFailureKind
	ScheduledAt             time.Time
	StartedAt               *time.Time
	CompletedAt             *time.Time
	ErrorSummary            *string
	CreatedAt               time.Time
}

// TriggerRetryAttemptDiagnostic is the safe read projection for retry visibility surfaces.
type TriggerRetryAttemptDiagnostic struct {
	AttemptSlug     slug.Slug
	Status          model.PipelineTriggerRetryAttemptStatus
	DecisionReason  *string
	TriggerSlug     slug.Slug
	TriggerRevision int64
	PipelineSlug    slug.Slug
	RootRunSlug     slug.Slug
	RetryOfRunSlug  slug.Slug
	RunSlug         slug.Slug
	AttemptNumber   int
	FailureKind     *model.PipelineTriggerRetryFailureKind
	SourceEventSlug *slug.Slug
	ScheduledAt     time.Time
	EnqueuedAt      *time.Time
	StartedAt       *time.Time
	CompletedAt     *time.Time
	ErrorSummary    *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// PendingRunFinalization retains only the exact queue identity of a terminal
// attempt whose source owner has not committed its completion/retry decision.
type PendingRunFinalization struct {
	OrgSlug, ProjectSlug, EnvironmentSlug, TriggerSlug slug.Slug
	TriggerRevision                                    int64
	ScheduledAt                                        time.Time
	RetryAttemptSlug, ReceiverID, DeliveryID           string
	AttemptNumber                                      int
}

// DelayedScheduleFire is safe scope and timing evidence from an unfinished fire.
type DelayedScheduleFire struct {
	OrgSlug, ProjectSlug, EnvironmentSlug, TriggerSlug, PipelineSlug slug.Slug
	TriggerRevision                                                  int64
	ScheduledFireAt, ActualFireAt                                    time.Time
}
