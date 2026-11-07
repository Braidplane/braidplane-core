package pipelineschedulesvc

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

const (
	initialScheduleLookback = 24 * time.Hour
	maxScheduleLookback     = 32 * 24 * time.Hour
	scheduleOnTimeGrace     = time.Minute

	scheduleFireStatusSkipped   = "skipped"
	scheduleFireStatusSucceeded = "succeeded"
	scheduleFireStatusFailed    = "failed"
)

type (
	DueScheduleTrigger        = pipelinetriggers.DueScheduleTrigger
	ScheduleFireClaim         = pipelinetriggers.ScheduleFireClaim
	ScheduleFireClaimResult   = pipelinetriggers.ScheduleFireClaimResult
	ScheduleFireExecutionRef  = pipelinetriggers.ScheduleFireExecutionRef
	ClaimedScheduleFire       = pipelinetriggers.ClaimedScheduleFire
	ScheduleFireCompletion    = pipelinetriggers.ScheduleFireCompletion
	TriggerRetryAttempt       = pipelinetriggers.TriggerRetryAttempt
	TriggerRetryAttemptRecord = pipelinetriggers.TriggerRetryAttemptRecord
)

// DispatchResult summarizes one scheduler scan for worker diagnostics and tests.
type DispatchResult struct {
	Scanned         int
	Claimed         int
	Enqueued        int
	SkippedDisabled int
	SkippedMisfire  int
	AlreadyClaimed  int
}

// Repository defines the persistence operations required by schedule dispatch.
type Repository interface {
	ReconcileLogicalRunFinalizationsForSystem(context.Context, postgres.DBTX) error
	ListPendingRunFinalizationsForSystem(context.Context, postgres.DBTX) ([]pipelinetriggers.PendingRunFinalization, error)
	ListDelayedScheduleFiresForSystem(context.Context, postgres.DBTX, time.Time) ([]pipelinetriggers.DelayedScheduleFire, error)
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
	RecordTriggerRetryAttempt(
		ctx context.Context,
		tx postgres.DBTX,
		record TriggerRetryAttemptRecord,
	) (TriggerRetryAttempt, error)
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

// ScheduleFireProducer enqueues one claimed schedule fire into River.
type ScheduleFireProducer interface {
	EnqueueRunFinalization(context.Context, postgres.DBTX, queue.JobArgs) (queue.QueuedJob, error)
	EnqueueScheduleFireExecution(
		ctx context.Context,
		tx postgres.DBTX,
		args jobs.PipelineScheduleFireExecutionArgs,
	) (queue.QueuedJob, error)
}

// PipelineRetryProducer enqueues one trigger retry attempt into River.
type PipelineRetryProducer interface {
	EnqueueTriggerRetryExecution(
		ctx context.Context,
		tx postgres.DBTX,
		args jobs.PipelineTriggerRetryExecutionArgs,
	) (queue.QueuedJob, error)
}

// NotificationProducer creates generic notification candidates for scheduler observations.
type NotificationProducer = notificationsvc.CandidateProducer

// PipelineExecutor is the existing pipeline run entrypoint used by the scheduler.
type PipelineExecutor interface {
	FailUnadmittedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error
	ReconcileInterruptedRuns(context.Context, time.Time) error
	AcceptQueuedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, slug.Slug, pipelinerun.RunSourceSummary, time.Time, int64) error
	StartQueuedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, time.Time) error
	GetRun(context.Context, slug.Slug, slug.Slug, slug.Slug, string) (*pipelinesvc.PipelineRunDetail, error)
	Execute(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
		runSlug *slug.Slug,
		protectedEnvironmentConfirmation *slug.Slug,
		actor model.EventActor,
		source pipelinerun.RunSourceSummary,
		runtimeInput map[string]any,
	) (*pipelinesvc.PipelineRunDetail, error)
	ExecuteVersion(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
		specHash string,
		runSlug *slug.Slug,
		protectedEnvironmentConfirmation *slug.Slug,
		actor model.EventActor,
		source pipelinerun.RunSourceSummary,
		runtimeInput map[string]any,
	) (*pipelinesvc.PipelineRunDetail, error)
}

// Service dispatches persisted schedule triggers into pipeline runs.
type Service struct {
	txm           postgres.TransactionManager
	repo          Repository
	executor      PipelineExecutor
	producer      ScheduleFireProducer
	retryProducer PipelineRetryProducer
	notifier      NotificationProducer
}

// Option customizes optional scheduler integrations.
type Option func(*Service)

// WithNotificationProducer emits generic notification candidates for terminal retry decisions.
func WithNotificationProducer(producer NotificationProducer) Option {
	return func(s *Service) {
		s.notifier = producer
	}
}

type claimedScheduleFire struct {
	trigger DueScheduleTrigger
	claim   ScheduleFireClaim
	result  ScheduleFireClaimResult
}

type scheduleDecision struct {
	scheduledFireAt time.Time
	misfireResult   model.PipelineTriggerScheduleMisfireResult
	shouldExecute   bool
}

// New creates a schedule dispatcher using the repository and existing pipeline executor.
// Pass nil retryProducer only for callers that do not enqueue trigger retry attempts.
func New(
	txm postgres.TransactionManager,
	repo Repository,
	executor PipelineExecutor,
	producer ScheduleFireProducer,
	retryProducer PipelineRetryProducer,
	opts ...Option,
) *Service {
	s := &Service{txm: txm, repo: repo, executor: executor, producer: producer, retryProducer: retryProducer}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// DispatchDueSchedules claims due schedule fires and enqueues one execution job per claimed fire.
func (s *Service) DispatchDueSchedules(ctx context.Context, now time.Time) (DispatchResult, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var result DispatchResult
	reconcileErr := s.executor.ReconcileInterruptedRuns(ctx, now)
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.repo.ReconcileLogicalRunFinalizationsForSystem(ctx, tx); err != nil {
			return err
		}
		pending, err := s.repo.ListPendingRunFinalizationsForSystem(ctx, tx)
		if err != nil {
			return err
		}
		for _, item := range pending {
			var args queue.JobArgs
			switch {
			case item.ReceiverID != "":
				args = jobs.PipelineWebhookDeliveryArgs{FinalizeOnly: true, ReceiverID: item.ReceiverID, DeliveryID: item.DeliveryID, RunAttempt: item.AttemptNumber}
			case item.RetryAttemptSlug != "":
				args = jobs.PipelineTriggerRetryExecutionArgs{FinalizeOnly: true, OrganizationSlug: item.OrgSlug, ProjectSlug: item.ProjectSlug, EnvironmentSlug: item.EnvironmentSlug, RetryAttemptSlug: slug.FromString(item.RetryAttemptSlug)}
			default:
				args = jobs.PipelineScheduleFireExecutionArgs{FinalizeOnly: true, OrganizationSlug: item.OrgSlug, ProjectSlug: item.ProjectSlug, EnvironmentSlug: item.EnvironmentSlug, TriggerSlug: item.TriggerSlug, TriggerRevision: item.TriggerRevision, ScheduledFireAt: item.ScheduledAt}
			}
			if _, err := s.producer.EnqueueRunFinalization(ctx, tx, args); err != nil {
				return err
			}
		}
		if s.notifier != nil {
			fires, err := s.repo.ListDelayedScheduleFiresForSystem(ctx, tx, now.Add(-scheduleOnTimeGrace))
			if err != nil {
				return err
			}
			for _, fire := range fires {
				if err := s.notifier.EnqueueCandidate(ctx, tx, scheduleNotificationCandidate(fire, model.NotificationTypePipelineScheduledRunDelayed)); err != nil {
					return err
				}
			}
		}
		triggers, err := s.repo.ListDueScheduleTriggersForSystem(ctx, tx)
		if err != nil {
			return err
		}
		result.Scanned = len(triggers)
		for _, trigger := range triggers {
			claim, ok, err := s.claimDueTrigger(ctx, tx, trigger, now, &result)
			if err != nil {
				return err
			}
			if ok && claim.result.Claimed && claim.claim.MisfireResult != model.PipelineTriggerScheduleMisfireResultSkipped {
				queued, err := s.producer.EnqueueScheduleFireExecution(ctx, tx, jobs.PipelineScheduleFireExecutionArgs{
					OrganizationSlug: claim.claim.OrgSlug,
					ProjectSlug:      claim.claim.ProjectSlug,
					EnvironmentSlug:  claim.claim.EnvironmentSlug,
					TriggerSlug:      claim.claim.TriggerSlug,
					TriggerRevision:  claim.claim.TriggerRevision,
					ScheduledFireAt:  claim.claim.ScheduledFireAt,
				})
				if err != nil {
					return err
				}
				if err := s.executor.AcceptQueuedRun(ctx, claim.claim.OrgSlug, claim.claim.ProjectSlug, claim.claim.EnvironmentSlug, claim.trigger.PipelineSlug, claim.result.FireSlug, s.runSource(claim.trigger, claim.claim, claim.result.FireSlug), claim.claim.ActualFireAt, queued.ID); err != nil {
					return err
				}

				result.Enqueued++
			}
		}
		return nil
	})
	return result, xerror.Join(err, reconcileErr)
}

// ExecuteClaimedScheduleFire executes one already-claimed schedule fire and records completion.
func (s *Service) ExecuteClaimedScheduleFire(
	ctx context.Context,
	args jobs.PipelineScheduleFireExecutionArgs,
) error {
	var fire ClaimedScheduleFire
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		loaded, err := s.repo.LoadClaimedScheduleFire(ctx, db, ScheduleFireExecutionRef{
			OrgSlug:         args.OrganizationSlug,
			ProjectSlug:     args.ProjectSlug,
			EnvironmentSlug: args.EnvironmentSlug,
			TriggerSlug:     args.TriggerSlug,
			TriggerRevision: args.TriggerRevision,
			ScheduledFireAt: args.ScheduledFireAt,
		})
		if err != nil {
			return err
		}
		fire = loaded
		return nil
	})
	if err != nil {
		if xerror.Is(err, pipelinetriggers.ErrPipelineTriggerNotFound) {
			return nil
		}
		return err
	}

	if args.FinalizeOnly && !fire.Resuming {
		return nil
	}
	if !fire.Resuming {
		if err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
			err := s.repo.MarkScheduleFireRunning(ctx, tx, ScheduleFireExecutionRef{
				OrgSlug:         fire.Claim.OrgSlug,
				ProjectSlug:     fire.Claim.ProjectSlug,
				EnvironmentSlug: fire.Claim.EnvironmentSlug,
				TriggerSlug:     fire.Claim.TriggerSlug,
				TriggerRevision: fire.Claim.TriggerRevision,
				ScheduledFireAt: fire.Claim.ScheduledFireAt,
			})
			if err != nil {
				return err
			}
			return s.executor.StartQueuedRun(ctx, fire.Claim.OrgSlug, fire.Claim.ProjectSlug, fire.Claim.EnvironmentSlug, fire.FireSlug, time.Now().UTC())
		}); err != nil {
			if xerror.Is(err, pipelinetriggers.ErrPipelineTriggerNotFound) {
				return nil
			}
			return err
		}
	}
	var run *pipelinesvc.PipelineRunDetail
	var runErr error
	if fire.Resuming {
		run, runErr = s.loadTerminalAttempt(ctx, fire.Claim.OrgSlug, fire.Claim.ProjectSlug, fire.Claim.EnvironmentSlug, fire.FireSlug.String())
		if runErr != nil {
			return runErr
		}
	} else {
		run, runErr = s.executeClaimedFire(ctx, fire)
	}
	var retryNotification notificationsvc.CreateCandidateParams
	var shouldNotifyRetry bool
	if err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.completeExecutedFireTx(ctx, tx, fire, run, runErr); err != nil {
			return err
		}
		if input, ok := retryDecisionInputForFailedRun(fire, run); ok {
			input.ExecutionError = runErr
			candidate, shouldNotify, err := s.recordRetryDecisionInTx(ctx, tx, input)
			if err != nil {
				return err
			}
			retryNotification = candidate
			shouldNotifyRetry = shouldNotify
		}
		return nil
	}); err != nil {
		return err
	}
	s.createRetryDecisionNotification(ctx, retryNotification, shouldNotifyRetry)
	if runErr != nil && run == nil {
		return runErr
	}
	return nil
}

// ExecuteTriggerRetryAttempt executes one queued retry attempt and records the terminal result.
func (s *Service) ExecuteTriggerRetryAttempt(
	ctx context.Context,
	args jobs.PipelineTriggerRetryExecutionArgs,
) error {
	var attempt TriggerRetryAttempt
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		loaded, err := s.repo.LoadTriggerRetryAttemptForExecution(
			ctx,
			db,
			args.OrganizationSlug,
			args.ProjectSlug,
			args.EnvironmentSlug,
			args.RetryAttemptSlug,
		)
		if err != nil {
			return err
		}
		attempt = loaded
		return nil
	})
	if err != nil {
		if xerror.Is(err, pipelinetriggers.ErrPipelineTriggerNotFound) {
			return nil
		}
		return err
	}

	if args.FinalizeOnly && attempt.Status != model.PipelineTriggerRetryAttemptStatusRunning {
		return nil
	}
	if attempt.Status != model.PipelineTriggerRetryAttemptStatusRunning {
		if err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
			err := s.repo.MarkTriggerRetryAttemptRunning(
				ctx,
				tx,
				attempt.OrgSlug,
				attempt.ProjectSlug,
				attempt.EnvironmentSlug,
				attempt.AttemptSlug,
			)
			if err != nil {
				return err
			}
			return s.executor.StartQueuedRun(ctx, attempt.OrgSlug, attempt.ProjectSlug, attempt.EnvironmentSlug, attempt.RunSlug, time.Now().UTC())
		}); err != nil {
			if xerror.Is(err, pipelinetriggers.ErrPipelineTriggerNotFound) {
				return nil
			}
			return err
		}
	}
	var run *pipelinesvc.PipelineRunDetail
	var runErr error
	if attempt.Status == model.PipelineTriggerRetryAttemptStatusRunning {
		run, runErr = s.loadTerminalAttempt(ctx, attempt.OrgSlug, attempt.ProjectSlug, attempt.EnvironmentSlug, attempt.RunSlug.String())
		if runErr != nil {
			return runErr
		}
	} else {
		run, runErr = s.executeRetryAttempt(ctx, attempt)
	}
	var retryNotification notificationsvc.CreateCandidateParams
	var shouldNotifyRetry bool
	if err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.completeRetryAttemptTx(ctx, tx, attempt, run, runErr); err != nil {
			return err
		}
		if input, ok := retryDecisionInputForRetryAttempt(attempt, run); ok {
			input.ExecutionError = runErr
			candidate, shouldNotify, err := s.recordRetryDecisionInTx(ctx, tx, input)
			if err != nil {
				return err
			}
			retryNotification = candidate
			shouldNotifyRetry = shouldNotify
		}
		return nil
	}); err != nil {
		return err
	}
	s.createRetryDecisionNotification(ctx, retryNotification, shouldNotifyRetry)
	if runErr != nil && run == nil {
		return runErr
	}
	return nil
}

// executeClaimedFire selects the pipeline version policy captured on the claimed fire.
func (s *Service) executeClaimedFire(
	ctx context.Context,
	fire ClaimedScheduleFire,
) (*pipelinesvc.PipelineRunDetail, error) {
	runSlug := fire.FireSlug
	source := s.runSource(fire.Trigger, fire.Claim, runSlug)
	switch fire.Trigger.VersionPolicy.Mode {
	case model.PipelineTriggerVersionPolicyModeLatestDeployed:
		return s.executeLatestDeployedFire(ctx, fire)
	case model.PipelineTriggerVersionPolicyModePinned:
		if fire.Trigger.VersionPolicy.PinnedVersionHash == nil || *fire.Trigger.VersionPolicy.PinnedVersionHash == "" {
			return nil, xerror.InvalidInput(xerror.PipelineTrigger).
				Detail("pinned schedule trigger is missing a pipeline version hash")
		}
		return s.executor.ExecuteVersion(
			ctx,
			fire.Trigger.OrgSlug,
			fire.Trigger.ProjectSlug,
			fire.Trigger.EnvironmentSlug,
			fire.Trigger.PipelineSlug,
			*fire.Trigger.VersionPolicy.PinnedVersionHash,
			&runSlug,
			nil,
			model.SystemEventActor(),
			source,
			map[string]any{},
		)
	default:
		return nil, xerror.InvalidInput(xerror.PipelineTrigger).
			Detail("schedule trigger version policy is invalid")
	}
}

// executeLatestDeployedFire uses the existing deployed execution entrypoint.
func (s *Service) executeLatestDeployedFire(
	ctx context.Context,
	fire ClaimedScheduleFire,
) (*pipelinesvc.PipelineRunDetail, error) {
	runSlug := fire.FireSlug
	return s.executor.Execute(
		ctx,
		fire.Trigger.OrgSlug,
		fire.Trigger.ProjectSlug,
		fire.Trigger.EnvironmentSlug,
		fire.Trigger.PipelineSlug,
		&runSlug,
		nil,
		model.SystemEventActor(),
		s.runSource(fire.Trigger, fire.Claim, runSlug),
		map[string]any{},
	)
}

// executeRetryAttempt uses the immutable pipeline version captured from the failed run.
func (s *Service) executeRetryAttempt(
	ctx context.Context,
	attempt TriggerRetryAttempt,
) (*pipelinesvc.PipelineRunDetail, error) {
	runSlug := attempt.RunSlug
	source := attempt.Source
	source.EnvironmentSlug = attempt.EnvironmentSlug
	source.AttemptNumber = attempt.AttemptNumber
	source.RootRunSlug = attempt.RootRunSlug
	source.RetryOfRunSlug = &attempt.RetryOfRunSlug
	return s.executor.ExecuteVersion(
		ctx,
		attempt.OrgSlug,
		attempt.ProjectSlug,
		attempt.EnvironmentSlug,
		attempt.PipelineSlug,
		attempt.PipelineVersionSpecHash,
		&runSlug,
		nil,
		model.SystemEventActor(),
		source,
		map[string]any{},
	)
}

// claimDueTrigger applies schedule policy, persists an idempotent claim, and records skipped fires.
func (s *Service) claimDueTrigger(
	ctx context.Context,
	tx postgres.DBTX,
	trigger DueScheduleTrigger,
	now time.Time,
	result *DispatchResult,
) (claimedScheduleFire, bool, error) {
	if !trigger.Enabled {
		result.SkippedDisabled++
		return claimedScheduleFire{}, false, nil
	}

	decision, due, err := dueScheduleDecision(trigger, now)
	if err != nil || !due {
		return claimedScheduleFire{}, false, err
	}

	claim := ScheduleFireClaim{
		OrgSlug:         trigger.OrgSlug,
		ProjectSlug:     trigger.ProjectSlug,
		EnvironmentSlug: trigger.EnvironmentSlug,
		TriggerSlug:     trigger.TriggerSlug,
		TriggerRevision: trigger.TriggerRevision,
		ScheduledFireAt: decision.scheduledFireAt,
		ActualFireAt:    now,
		MisfireResult:   decision.misfireResult,
	}
	claimResult, err := s.repo.ClaimScheduleFire(ctx, tx, claim)
	if err != nil {
		return claimedScheduleFire{}, false, err
	}
	if !claimResult.Claimed {
		result.AlreadyClaimed++
		return claimedScheduleFire{
			trigger: trigger,
			claim:   claim,
			result:  claimResult,
		}, false, nil
	}

	result.Claimed++
	if s.notifier != nil && now.Sub(claim.ScheduledFireAt) > scheduleOnTimeGrace {
		kind := model.NotificationTypePipelineScheduledRunDelayed
		if !decision.shouldExecute {
			kind = model.NotificationTypePipelineScheduledRunMissing
		}
		candidate := scheduleNotificationCandidate(pipelinetriggers.DelayedScheduleFire{
			OrgSlug: trigger.OrgSlug, ProjectSlug: trigger.ProjectSlug, EnvironmentSlug: trigger.EnvironmentSlug,
			PipelineSlug: trigger.PipelineSlug, TriggerSlug: trigger.TriggerSlug, TriggerRevision: trigger.TriggerRevision,
			ScheduledFireAt: claim.ScheduledFireAt, ActualFireAt: claim.ActualFireAt,
		}, kind)
		if err := s.notifier.EnqueueCandidate(ctx, tx, candidate); err != nil {
			return claimedScheduleFire{}, false, err
		}
	}
	if !decision.shouldExecute {
		result.SkippedMisfire++
		err := s.repo.CompleteScheduleFire(ctx, tx, ScheduleFireCompletion{
			OrgSlug:         claim.OrgSlug,
			ProjectSlug:     claim.ProjectSlug,
			EnvironmentSlug: claim.EnvironmentSlug,
			TriggerSlug:     claim.TriggerSlug,
			TriggerRevision: claim.TriggerRevision,
			ScheduledFireAt: claim.ScheduledFireAt,
			Status:          scheduleFireStatusSkipped,
		})
		if err != nil {
			return claimedScheduleFire{}, false, err
		}
		return claimedScheduleFire{}, false, nil
	}

	return claimedScheduleFire{
		trigger: trigger,
		claim:   claim,
		result:  claimResult,
	}, true, nil
}

// dueScheduleDecision returns the latest fire due at or before now with misfire policy applied.
func dueScheduleDecision(
	trigger DueScheduleTrigger,
	now time.Time,
) (scheduleDecision, bool, error) {
	location, err := time.LoadLocation(trigger.ScheduleConfig.Timezone)
	if err != nil {
		return scheduleDecision{}, false, xerror.InvalidInput(xerror.PipelineTrigger).With(err).Detail(fmt.Sprintf("schedule timezone %q is invalid", trigger.ScheduleConfig.Timezone))
	}
	schedule, err := cron.ParseStandard(trigger.ScheduleConfig.Cron)
	if err != nil {
		return scheduleDecision{}, false, xerror.InvalidInput(xerror.PipelineTrigger).With(err).Detail(fmt.Sprintf("schedule cron %q is invalid", trigger.ScheduleConfig.Cron))
	}

	nowInLocation := now.In(location)
	cursor := initialScheduleCursor(trigger, nowInLocation)
	var latest time.Time
	for {
		next := schedule.Next(cursor)
		if next.After(nowInLocation) {
			break
		}
		latest = next
		cursor = next
	}
	if latest.IsZero() {
		return scheduleDecision{}, false, nil
	}

	if nowInLocation.Sub(latest) > scheduleOnTimeGrace {
		return misfireDecision(trigger.ScheduleConfig.MisfirePolicy, latest.UTC())
	}
	return scheduleDecision{
		scheduledFireAt: latest.UTC(),
		misfireResult:   model.PipelineTriggerScheduleMisfireResultOnTime,
		shouldExecute:   true,
	}, true, nil
}

// initialScheduleCursor bounds catch-up scanning while still finding the current due fire.
func initialScheduleCursor(trigger DueScheduleTrigger, nowInLocation time.Time) time.Time {
	cursor := nowInLocation.Add(-initialScheduleLookback)
	if trigger.LastScheduledFireAt != nil {
		cursor = trigger.LastScheduledFireAt.In(nowInLocation.Location())
		minCursor := nowInLocation.Add(-maxScheduleLookback)
		if cursor.Before(minCursor) {
			cursor = minCursor
		}
	}
	return cursor
}

// misfireDecision translates a late fire into the persisted result and execution choice.
func misfireDecision(
	policy model.PipelineTriggerScheduleMisfirePolicy,
	scheduledFireAt time.Time,
) (scheduleDecision, bool, error) {
	switch policy {
	case model.PipelineTriggerScheduleMisfirePolicySkip:
		return scheduleDecision{
			scheduledFireAt: scheduledFireAt,
			misfireResult:   model.PipelineTriggerScheduleMisfireResultSkipped,
			shouldExecute:   false,
		}, true, nil
	case model.PipelineTriggerScheduleMisfirePolicyFireOnce:
		return scheduleDecision{
			scheduledFireAt: scheduledFireAt,
			misfireResult:   model.PipelineTriggerScheduleMisfireResultFireOnce,
			shouldExecute:   true,
		}, true, nil
	default:
		return scheduleDecision{}, false, xerror.InvalidInput(xerror.PipelineTrigger).
			Detail("schedule misfire policy is invalid")
	}
}

// runSource builds the immutable schedule-backed source summary passed to pipeline execution.
func (s *Service) runSource(
	trigger DueScheduleTrigger,
	claim ScheduleFireClaim,
	rootRunSlug slug.Slug,
) pipelinerun.RunSourceSummary {
	return pipelinerun.RunSourceSummary{
		Type:            model.PipelineRunSourceTypeSchedule,
		Summary:         fmt.Sprintf("Scheduled run from trigger %s", trigger.TriggerSlug),
		EnvironmentSlug: trigger.EnvironmentSlug,
		Entrypoint:      model.PipelineRunSourceEntrypointScheduler,
		ActorType:       model.EventActorTypeSystem,
		ParameterKeys:   append([]string(nil), trigger.RuntimeInputKeys...),
		AttemptNumber:   1,
		RootRunSlug:     rootRunSlug,
		Schedule: &pipelinerun.RunSourceScheduleSummary{
			TriggerSlug:     trigger.TriggerSlug,
			TriggerRevision: trigger.TriggerRevision,
			Cron:            trigger.ScheduleConfig.Cron,
			Timezone:        trigger.ScheduleConfig.Timezone,
			ScheduledFireAt: claim.ScheduledFireAt,
			ActualFireAt:    claim.ActualFireAt,
			MisfireResult:   claim.MisfireResult,
		},
	}
}

// retryDecisionInputForFailedRun builds retry decision input for a failed first-attempt schedule run.
func retryDecisionInputForFailedRun(
	fire ClaimedScheduleFire,
	run *pipelinesvc.PipelineRunDetail,
) (RetryDecisionInput, bool) {
	if run == nil || run.Metadata.Status != scheduleFireStatusFailed {
		return RetryDecisionInput{}, false
	}
	baseAt := retryDecisionBaseAt(run.Metadata.StartedAt, fire.Claim.ActualFireAt)
	return RetryDecisionInput{
		OrgSlug:                 fire.Trigger.OrgSlug,
		ProjectSlug:             fire.Trigger.ProjectSlug,
		EnvironmentSlug:         fire.Trigger.EnvironmentSlug,
		TriggerSlug:             fire.Trigger.TriggerSlug,
		TriggerRevision:         fire.Trigger.TriggerRevision,
		PipelineSlug:            fire.Trigger.PipelineSlug,
		VersionPolicy:           fire.Trigger.VersionPolicy,
		PipelineVersionSpecHash: run.Metadata.PipelineVersionSpecHash,
		RuntimeInputKeys:        fire.Trigger.RuntimeInputKeys,
		RetryPolicy:             fire.Trigger.RetryPolicy,
		FailedRun:               run,
		BaseAt:                  baseAt,
		ElapsedBaseAt:           baseAt,
	}, true
}

// retryDecisionInputForRetryAttempt builds follow-up retry decision input after a retry run fails.
func retryDecisionInputForRetryAttempt(
	attempt TriggerRetryAttempt,
	run *pipelinesvc.PipelineRunDetail,
) (RetryDecisionInput, bool) {
	if run == nil || run.Metadata.Status != scheduleFireStatusFailed {
		return RetryDecisionInput{}, false
	}
	baseAt := retryDecisionBaseAt(run.Metadata.StartedAt, attempt.ScheduledAt)
	return RetryDecisionInput{
		OrgSlug:                 attempt.OrgSlug,
		ProjectSlug:             attempt.ProjectSlug,
		EnvironmentSlug:         attempt.EnvironmentSlug,
		TriggerSlug:             attempt.TriggerSlug,
		TriggerRevision:         attempt.TriggerRevision,
		PipelineSlug:            attempt.PipelineSlug,
		VersionPolicy:           attempt.VersionPolicy,
		PipelineVersionSpecHash: run.Metadata.PipelineVersionSpecHash,
		RuntimeInputKeys:        attempt.RuntimeInputKeys,
		RetryPolicy:             attempt.RetryPolicy,
		FailedRun:               run,
		BaseAt:                  baseAt,
		ElapsedBaseAt:           retryElapsedBaseAt(run.Metadata.Source, run.Metadata.StartedAt, attempt.ScheduledAt),
	}, true
}

// RetryDecisionInput combines a captured policy with authoritative failed-run evidence.
type RetryDecisionInput struct {
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
	FailedRun               *pipelinesvc.PipelineRunDetail
	ExecutionError          error
	BaseAt                  time.Time
	ElapsedBaseAt           time.Time
}

// recordRetryDecisionInTx stores retry state and enqueues queued attempts in the caller transaction.
func (s *Service) recordRetryDecisionInTx(
	ctx context.Context,
	tx postgres.DBTX,
	input RetryDecisionInput,
) (notificationsvc.CreateCandidateParams, bool, error) {
	if input.FailedRun == nil {
		return notificationsvc.CreateCandidateParams{}, false, nil
	}
	record, shouldEnqueue, err := BuildRetryAttempt(input)
	if err != nil {
		return notificationsvc.CreateCandidateParams{}, false, err
	}

	attempt, err := s.repo.RecordTriggerRetryAttempt(ctx, tx, record)
	if err != nil {
		return notificationsvc.CreateCandidateParams{}, false, err
	}
	if !shouldEnqueue {
		if s.notifier != nil {
			candidate := notificationsvc.RunFailedCandidate(record.OrgSlug, record.ProjectSlug, record.EnvironmentSlug,
				record.PipelineSlug, record.RetryOfRunSlug, record.RootRunSlug, input.BaseAt)
			candidate.SourceEventSlug = record.SourceEventSlug
			candidate.SourceRefs["trigger_slug"] = record.TriggerSlug.String()
			candidate.SourceRefs["trigger_revision"] = strconv.FormatInt(record.TriggerRevision, 10)
			if err := s.notifier.EnqueueCandidate(ctx, tx, candidate); err != nil {
				return notificationsvc.CreateCandidateParams{}, false, err
			}
		}
		return retryDecisionNotificationCandidate(attempt, record), true, nil
	}
	if s.retryProducer == nil {
		return notificationsvc.CreateCandidateParams{}, false, xerror.Unexpected().Detail("pipeline retry producer is required to enqueue trigger retry attempt")
	}
	queued, err := s.retryProducer.EnqueueTriggerRetryExecution(ctx, tx, jobs.PipelineTriggerRetryExecutionArgs{
		OrganizationSlug: attempt.OrgSlug,
		ProjectSlug:      attempt.ProjectSlug,
		EnvironmentSlug:  attempt.EnvironmentSlug,
		RetryAttemptSlug: attempt.AttemptSlug,
		ScheduledAt:      attempt.ScheduledAt,
	})
	if err != nil {
		return notificationsvc.CreateCandidateParams{}, false, err
	}
	source := attempt.Source
	source.EnvironmentSlug = attempt.EnvironmentSlug
	source.AttemptNumber = attempt.AttemptNumber
	source.RootRunSlug = attempt.RootRunSlug
	source.RetryOfRunSlug = &attempt.RetryOfRunSlug
	err = s.executor.AcceptQueuedRun(ctx, attempt.OrgSlug, attempt.ProjectSlug, attempt.EnvironmentSlug,
		attempt.PipelineSlug, attempt.RunSlug, source, time.Now().UTC(), queued.ID)
	return notificationsvc.CreateCandidateParams{}, false, err
}

// createRetryDecisionNotification emits retry decision notifications after retry state commits.
func (s *Service) createRetryDecisionNotification(
	ctx context.Context,
	params notificationsvc.CreateCandidateParams,
	shouldNotify bool,
) {
	if s.notifier == nil || !shouldNotify {
		return
	}
	if err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return s.notifier.EnqueueCandidate(ctx, tx, params)
	}); err != nil {
		slog.WarnContext(
			ctx,
			"pipeline retry notification candidate failed",
			slog.String("org_slug", params.OrgSlug.String()),
			slog.Any("project_slug", optionalSlugLogValue(params.ProjectSlug)),
			slog.Any("environment_slug", optionalSlugLogValue(params.EnvironmentSlug)),
			slog.String("dedupe_fingerprint", params.DedupeFingerprint),
			slog.Any("error", err),
		)
	}
}

// retryDecisionNotificationCandidate builds the generic candidate for no-retry decisions.
func retryDecisionNotificationCandidate(
	attempt TriggerRetryAttempt,
	record TriggerRetryAttemptRecord,
) notificationsvc.CreateCandidateParams {
	projectSlug := record.ProjectSlug
	environmentSlug := record.EnvironmentSlug
	sourceRefs := map[string]string{
		"project_slug":       record.ProjectSlug.String(),
		"environment_slug":   record.EnvironmentSlug.String(),
		"pipeline_slug":      record.PipelineSlug.String(),
		"run_slug":           record.RetryOfRunSlug.String(),
		"root_run_slug":      record.RootRunSlug.String(),
		"retry_of_run_slug":  record.RetryOfRunSlug.String(),
		"retry_attempt_slug": attempt.AttemptSlug.String(),
		"trigger_slug":       record.TriggerSlug.String(),
		"trigger_revision":   strconv.FormatInt(record.TriggerRevision, 10),
	}

	return notificationsvc.CreateCandidateParams{
		OrgSlug:           record.OrgSlug,
		ProjectSlug:       &projectSlug,
		EnvironmentSlug:   &environmentSlug,
		NotificationType:  model.NotificationTypePipelineRetryExhausted,
		Severity:          model.NotificationSeverityWarning,
		DedupeFingerprint: retryDecisionNotificationDedupe(record),
		SourceEventSlug:   record.SourceEventSlug,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs:        sourceRefs,
		SafeMetadata: map[string]any{
			"summary":               retryDecisionNotificationSummary(record),
			"retry_status":          record.Status.String(),
			"retry_decision_reason": record.DecisionReason,
			"failure_kind":          record.FailureKind.String(),
			"attempt_number":        record.AttemptNumber,
			"max_attempts":          record.RetryPolicy.MaxAttempts,
			"pipeline_slug":         record.PipelineSlug.String(),
			"trigger_slug":          record.TriggerSlug.String(),
			"trigger_revision":      record.TriggerRevision,
			"root_run_slug":         record.RootRunSlug.String(),
			"retry_of_run_slug":     record.RetryOfRunSlug.String(),
			"retry_attempt_slug":    attempt.AttemptSlug.String(),
			"scheduled_at":          record.ScheduledAt.UTC().Format(time.RFC3339),
		},
	}
}

// retryDecisionNotificationDedupe scopes retry exhaustion notifications to one retry group.
func retryDecisionNotificationDedupe(record TriggerRetryAttemptRecord) string {
	return fmt.Sprintf(
		"pipeline.retry_exhausted:%s:%s:%s:%d:%s",
		record.ProjectSlug,
		record.EnvironmentSlug,
		record.TriggerSlug,
		record.TriggerRevision,
		record.RootRunSlug,
	)
}

// retryDecisionNotificationSummary returns a safe display summary for operator surfaces.
func retryDecisionNotificationSummary(record TriggerRetryAttemptRecord) string {
	switch record.Status {
	case model.PipelineTriggerRetryAttemptStatusExhausted:
		return fmt.Sprintf("Pipeline %s exhausted retries after run %s", record.PipelineSlug, record.RetryOfRunSlug)
	case model.PipelineTriggerRetryAttemptStatusSkipped:
		return fmt.Sprintf("Pipeline %s will not retry run %s", record.PipelineSlug, record.RetryOfRunSlug)
	default:
		return fmt.Sprintf("Pipeline %s has terminal retry decision %s for run %s", record.PipelineSlug, record.Status, record.RetryOfRunSlug)
	}
}

// optionalSlugLogValue converts optional slugs into log-safe scalar values.
func optionalSlugLogValue(value *slug.Slug) any {
	if value == nil {
		return nil
	}
	return value.String()
}

// BuildRetryAttempt converts one failed run into a durable retry decision.
func BuildRetryAttempt(input RetryDecisionInput) (TriggerRetryAttemptRecord, bool, error) {
	failed := input.FailedRun.Metadata
	source := failed.Source
	source.EnvironmentSlug = input.EnvironmentSlug
	if source.AttemptNumber <= 0 {
		source.AttemptNumber = 1
	}
	rootRunSlug := source.RootRunSlug
	if rootRunSlug == "" {
		rootRunSlug = slug.FromString(failed.RunSlug)
		source.RootRunSlug = rootRunSlug
	}
	retryOfRunSlug := slug.FromString(failed.RunSlug)
	nextAttemptNumber := source.AttemptNumber + 1
	source.AttemptNumber = nextAttemptNumber
	source.RetryOfRunSlug = &retryOfRunSlug

	failureKind := retryFailureKindFromRun(failed.FailureKind)
	status := model.PipelineTriggerRetryAttemptStatusQueued
	decisionReason := ""
	shouldEnqueue := true
	switch {
	case !input.RetryPolicy.Enabled || input.RetryPolicy.MaxAttempts <= 1:
		status = model.PipelineTriggerRetryAttemptStatusSkipped
		decisionReason = "retry_policy_disabled"
		shouldEnqueue = false
	// Response limits are checked after the external request, which may have written data.
	case (input.FailedRun.Timeline.Failure != nil && input.FailedRun.Timeline.Failure.AutomaticRetryProhibited) ||
		xerror.Is(input.ExecutionError, xerror.ExecutionRowLimitExceeded()) ||
		xerror.Is(input.ExecutionError, xerror.ExecutionResponseSizeExceeded()):
		status = model.PipelineTriggerRetryAttemptStatusSkipped
		decisionReason = "execution_response_limit_exceeded"
		shouldEnqueue = false
	case failureKind == model.PipelineTriggerRetryFailureKindUnknown:
		status = model.PipelineTriggerRetryAttemptStatusSkipped
		decisionReason = "non_retryable_failure_kind"
		shouldEnqueue = false
	case !retryPolicyIncludesFailureKind(input.RetryPolicy, failureKind):
		status = model.PipelineTriggerRetryAttemptStatusSkipped
		decisionReason = "non_retryable_failure_kind"
		shouldEnqueue = false
	case nextAttemptNumber > input.RetryPolicy.MaxAttempts:
		status = model.PipelineTriggerRetryAttemptStatusExhausted
		decisionReason = "max_attempts_exhausted"
		shouldEnqueue = false
	}

	scheduledAt := retryScheduledAt(input.BaseAt, input.RetryPolicy, nextAttemptNumber)
	elapsedBaseAt := retryDecisionBaseAt(input.ElapsedBaseAt, failed.StartedAt)
	if shouldEnqueue && retryMaxElapsedExceeded(elapsedBaseAt, scheduledAt, input.RetryPolicy) {
		status = model.PipelineTriggerRetryAttemptStatusExhausted
		decisionReason = "max_elapsed_exhausted"
		shouldEnqueue = false
	}

	runSlug, err := retryRunSlug(rootRunSlug, nextAttemptNumber)
	if err != nil {
		return TriggerRetryAttemptRecord{}, false, err
	}

	return TriggerRetryAttemptRecord{
		OrgSlug:                 input.OrgSlug,
		ProjectSlug:             input.ProjectSlug,
		EnvironmentSlug:         input.EnvironmentSlug,
		TriggerSlug:             input.TriggerSlug,
		TriggerRevision:         input.TriggerRevision,
		Status:                  status,
		DecisionReason:          decisionReason,
		PipelineSlug:            input.PipelineSlug,
		VersionPolicy:           input.VersionPolicy,
		PipelineVersionSpecHash: input.PipelineVersionSpecHash,
		RuntimeInputKeys:        append([]string(nil), input.RuntimeInputKeys...),
		RetryPolicy:             input.RetryPolicy,
		Source:                  source,
		RootRunSlug:             rootRunSlug,
		RetryOfRunSlug:          retryOfRunSlug,
		RunSlug:                 runSlug,
		AttemptNumber:           nextAttemptNumber,
		FailureKind:             failureKind,
		SourceEventSlug:         failed.Source.SourceEventSlug,
		ScheduledAt:             scheduledAt,
	}, shouldEnqueue, nil
}

// retryFailureKindFromRun translates persisted run failure text into retry-policy failure kind.
func retryFailureKindFromRun(value *string) model.PipelineTriggerRetryFailureKind {
	if value == nil {
		return model.PipelineTriggerRetryFailureKindUnknown
	}
	parsed, err := model.PipelineTriggerRetryFailureKindString(*value)
	if err != nil {
		return model.PipelineTriggerRetryFailureKindUnknown
	}
	return parsed
}

// retryPolicyIncludesFailureKind reports whether the captured policy allows this failure kind.
func retryPolicyIncludesFailureKind(
	policy model.PipelineTriggerRetryPolicy,
	failureKind model.PipelineTriggerRetryFailureKind,
) bool {
	for _, retryable := range policy.RetryableFailureKinds {
		if retryable == failureKind {
			return true
		}
	}
	return false
}

// retryDecisionBaseAt selects the first preferred stable clock boundary for retry budgeting.
func retryDecisionBaseAt(candidates ...time.Time) time.Time {
	for _, candidate := range candidates {
		if !candidate.IsZero() {
			return candidate
		}
	}
	return time.Now().UTC()
}

// retryElapsedBaseAt preserves the original schedule boundary for follow-up retry elapsed caps.
func retryElapsedBaseAt(source pipelinerun.RunSourceSummary, candidates ...time.Time) time.Time {
	if source.AttemptNumber > 1 && source.Schedule != nil {
		candidates = append([]time.Time{source.Schedule.ActualFireAt}, candidates...)
	}
	return retryDecisionBaseAt(candidates...)
}

// retryScheduledAt applies the captured backoff policy from the retry base timestamp.
func retryScheduledAt(
	baseAt time.Time,
	policy model.PipelineTriggerRetryPolicy,
	attemptNumber int,
) time.Time {
	if baseAt.IsZero() {
		baseAt = time.Now().UTC()
	}
	delay := retryDelay(policy, attemptNumber)
	return baseAt.Add(delay)
}

// retryDelay computes the deterministic delay for an attempt without applying jitter.
func retryDelay(policy model.PipelineTriggerRetryPolicy, attemptNumber int) time.Duration {
	switch policy.BackoffStrategy {
	case model.PipelineTriggerBackoffStrategyFixed:
		return time.Duration(policy.InitialDelaySeconds) * time.Second
	case model.PipelineTriggerBackoffStrategyExponential:
		delaySeconds := policy.InitialDelaySeconds
		maxInt := int(^uint(0) >> 1)
		for i := 2; i < attemptNumber; i++ {
			if delaySeconds > maxInt/2 {
				delaySeconds = maxInt
				break
			}
			delaySeconds *= 2
		}
		if policy.MaxDelaySeconds > 0 && delaySeconds > policy.MaxDelaySeconds {
			delaySeconds = policy.MaxDelaySeconds
		}
		return time.Duration(delaySeconds) * time.Second
	default:
		return 0
	}
}

// retryMaxElapsedExceeded reports whether the next scheduled retry violates max elapsed time.
func retryMaxElapsedExceeded(
	startedAt time.Time,
	scheduledAt time.Time,
	policy model.PipelineTriggerRetryPolicy,
) bool {
	if policy.MaxElapsedSeconds == nil || startedAt.IsZero() {
		return false
	}
	return scheduledAt.Sub(startedAt) > time.Duration(*policy.MaxElapsedSeconds)*time.Second
}

// retryRunSlug derives the deterministic run slug reserved for a retry attempt.
func retryRunSlug(rootRunSlug slug.Slug, attemptNumber int) (slug.Slug, error) {
	return slug.FromName(fmt.Sprintf("%s-retry-%d", rootRunSlug, attemptNumber))
}

// completeRetryAttemptTx records terminal retry attempt execution state in the caller transaction.
func (s *Service) completeRetryAttemptTx(
	ctx context.Context,
	tx postgres.DBTX,
	attempt TriggerRetryAttempt,
	run *pipelinesvc.PipelineRunDetail,
	runErr error,
) error {
	if runErr != nil && run == nil {
		if err := s.executor.FailUnadmittedRun(ctx, attempt.OrgSlug, attempt.ProjectSlug, attempt.EnvironmentSlug, attempt.RunSlug); err != nil {
			return err
		}
		errorSummary := runErr.Error()
		return s.repo.CompleteTriggerRetryAttempt(
			ctx,
			tx,
			attempt.OrgSlug,
			attempt.ProjectSlug,
			attempt.EnvironmentSlug,
			attempt.AttemptSlug,
			model.PipelineTriggerRetryAttemptStatusFailed,
			nil,
			&errorSummary,
		)
	}
	if run == nil {
		return xerror.Unexpected().Detail("pipeline retry attempt completed without run detail")
	}

	status := model.PipelineTriggerRetryAttemptStatusSucceeded
	sourceEventSlug := run.Metadata.Source.SourceEventSlug
	if run.Metadata.Status == scheduleFireStatusFailed || runErr != nil {
		status = model.PipelineTriggerRetryAttemptStatusFailed
	}

	return s.repo.CompleteTriggerRetryAttempt(
		ctx,
		tx,
		attempt.OrgSlug,
		attempt.ProjectSlug,
		attempt.EnvironmentSlug,
		attempt.AttemptSlug,
		status,
		sourceEventSlug,
		nil,
	)
}

// completeExecutedFireTx records schedule fire completion in the caller transaction.
func (s *Service) completeExecutedFireTx(
	ctx context.Context,
	tx postgres.DBTX,
	fire ClaimedScheduleFire,
	run *pipelinesvc.PipelineRunDetail,
	runErr error,
) error {
	status := scheduleFireStatusSucceeded
	var runSlug *string
	var sourceEventSlug *slug.Slug
	if run != nil {
		runSlug = &run.Metadata.RunSlug
		sourceEventSlug = run.Metadata.Source.SourceEventSlug
		if run.Metadata.Status != "" {
			status = run.Metadata.Status
		}
	}
	if runErr != nil {
		status = scheduleFireStatusFailed
	}
	var errorSummary *string
	if runErr != nil && run == nil {
		summary := runErr.Error()
		errorSummary = &summary
	}

	if run == nil && runErr != nil {
		if err := s.executor.FailUnadmittedRun(ctx, fire.Claim.OrgSlug, fire.Claim.ProjectSlug, fire.Claim.EnvironmentSlug, fire.FireSlug); err != nil {
			return err
		}
	}
	if run == nil && runErr != nil && s.notifier != nil {
		candidate := scheduleNotificationCandidate(pipelinetriggers.DelayedScheduleFire{
			OrgSlug: fire.Claim.OrgSlug, ProjectSlug: fire.Claim.ProjectSlug, EnvironmentSlug: fire.Claim.EnvironmentSlug,
			PipelineSlug: fire.Trigger.PipelineSlug, TriggerSlug: fire.Claim.TriggerSlug, TriggerRevision: fire.Claim.TriggerRevision,
			ScheduledFireAt: fire.Claim.ScheduledFireAt, ActualFireAt: fire.Claim.ActualFireAt,
		}, model.NotificationTypePipelineScheduledRunMissing)
		if err := s.notifier.EnqueueCandidate(ctx, tx, candidate); err != nil {
			return err
		}
	}
	return s.repo.CompleteScheduleFire(ctx, tx, ScheduleFireCompletion{
		OrgSlug:         fire.Claim.OrgSlug,
		ProjectSlug:     fire.Claim.ProjectSlug,
		EnvironmentSlug: fire.Claim.EnvironmentSlug,
		TriggerSlug:     fire.Claim.TriggerSlug,
		TriggerRevision: fire.Claim.TriggerRevision,
		ScheduledFireAt: fire.Claim.ScheduledFireAt,
		Status:          status,
		RunSlug:         runSlug,
		SourceEventSlug: sourceEventSlug,
		ErrorSummary:    errorSummary,
	})
}

// A running owner may have crashed after terminal persistence. Missing or live
// attempts stay unresolved; lease expiry is not permission to repeat effects.
func (s *Service) loadTerminalAttempt(ctx context.Context, org, project, environment slug.Slug, runSlug string) (*pipelinesvc.PipelineRunDetail, error) {
	run, err := s.executor.GetRun(ctx, org, project, environment, runSlug)
	if err != nil {
		return nil, xerror.Wrapf("load attempt for finalization: %w", err)
	}
	if run == nil || (run.Timeline.Failure != nil && run.Timeline.Failure.OutcomeUncertain) || (run.Metadata.Status != "succeeded" && run.Metadata.Status != "failed" && run.Metadata.Status != "canceled") {
		return nil, xerror.Unexpected().Detail("logical run finalization is awaiting a terminal attempt")
	}
	return run, nil
}
