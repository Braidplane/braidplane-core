package pipelinetriggers

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

// Repository provides SQL-backed persistence for environment-scoped pipeline triggers.
type Repository interface {
	ListCompletionRunFinalizationsForSystem(context.Context, postgres.DBTX) ([]CompletionDispatchRef, error)
	FenceCompletionRetryDecision(context.Context, postgres.DBTX, slug.Slug, int) (time.Time, error)
	SaveCompletionRetryExecution(context.Context, postgres.DBTX, slug.Slug, int, slug.Slug, []byte) error
	DeferCompletionRunFinalization(context.Context, postgres.DBTX, slug.Slug, int, slug.Slug, string) error
	ListPendingCompletionsForSystem(context.Context, postgres.DBTX) ([]CompletionDispatchRef, error)
	ListCompletionRepairsForSystem(context.Context, postgres.DBTX) ([]CompletionDispatchRef, error)
	AcceptCompletionSnapshot(context.Context, postgres.DBTX, slug.Slug, int, slug.Slug, []byte) error
	DeferCompletionSnapshot(context.Context, postgres.DBTX, slug.Slug, int, string) error
	CompleteCompletionSnapshotsForSystem(context.Context, postgres.DBTX) error
	LoadCompletionSnapshot(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, int) (*model.PipelineCompletionSnapshot, error)
	RefuseCompletionSnapshot(context.Context, postgres.DBTX, slug.Slug, int, string) error
	LockPipelineTriggerTopology(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (bool, error)
	ListPipelineCompletionEdges(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) ([]CompletionEdge, error)
	ReconcileLogicalRunFinalizationsForSystem(context.Context, postgres.DBTX) error
	ListPendingRunFinalizationsForSystem(context.Context, postgres.DBTX) ([]PendingRunFinalization, error)
	ProjectEnvironmentEnabled(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) (bool, error)
	PipelineExists(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) (bool, error)
	ResolvePinnedVersionHash(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		specHash string,
	) (string, error)
	DeploymentExists(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	) (bool, error)
	CreatePipelineTrigger(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		params CreatePipelineTriggerParams,
	) (*PipelineTrigger, error)
	FindPipelineTriggerBySlug(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	) (*PipelineTrigger, error)
	ListPipelineTriggers(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		req model.ListRequest,
	) ([]*PipelineTrigger, error)
	UpdatePipelineTrigger(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
		params UpdatePipelineTriggerParams,
	) (*PipelineTrigger, error)
	DeletePipelineTrigger(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	) error
	ListDelayedScheduleFiresForSystem(context.Context, postgres.DBTX, time.Time) ([]DelayedScheduleFire, error)
	ListDueScheduleTriggersForSystem(
		ctx context.Context,
		tx postgres.DBTX,
	) ([]DueScheduleTrigger, error)
	ClaimScheduleFire(
		ctx context.Context,
		tx postgres.DBTX,
		claim ScheduleFireClaim,
	) (ScheduleFireClaimResult, error)
	LoadClaimedScheduleFire(
		ctx context.Context,
		tx postgres.DBTX,
		ref ScheduleFireExecutionRef,
	) (ClaimedScheduleFire, error)
	MarkScheduleFireRunning(
		ctx context.Context,
		tx postgres.DBTX,
		ref ScheduleFireExecutionRef,
	) error
	CompleteScheduleFire(
		ctx context.Context,
		tx postgres.DBTX,
		completion ScheduleFireCompletion,
	) error
	ListScheduleFiresForTrigger(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
		req model.ListRequest,
	) ([]ScheduleFireDiagnostic, error)
	RecordTriggerRetryAttempt(
		ctx context.Context,
		tx postgres.DBTX,
		record TriggerRetryAttemptRecord,
	) (TriggerRetryAttempt, error)
	ListTriggerRetryAttemptsForRun(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, runSlug slug.Slug,
	) ([]TriggerRetryAttemptDiagnostic, error)
	LoadTriggerRetryAttemptForExecution(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, attemptSlug slug.Slug,
	) (TriggerRetryAttempt, error)
	MarkTriggerRetryAttemptRunning(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, attemptSlug slug.Slug,
	) error
	CompleteTriggerRetryAttempt(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug, projectSlug, environmentSlug, attemptSlug slug.Slug,
		status model.PipelineTriggerRetryAttemptStatus,
		sourceEventSlug *slug.Slug,
		errorSummary *string,
	) error
}

// NewRepository creates the SQLC-backed pipeline trigger repository.
func NewRepository() Repository {
	return &repository{}
}
