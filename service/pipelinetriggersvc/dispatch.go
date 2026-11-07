package pipelinetriggersvc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/Braidplane/braidplane-core/service/worker/producer"
)

type CompletionRunExecutor interface {
	AcceptCompletionRun(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot) (jobs.PipelineRunExecutionArgs, error)
	AcceptCompletionRetryRun(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot, slug.Slug, pipelinerun.RunSourceSummary, time.Time) (jobs.PipelineRunExecutionArgs, error)
	GetRun(context.Context, slug.Slug, slug.Slug, slug.Slug, string) (*pipelinesvc.PipelineRunDetail, error)
	ExecuteCompletionPipelineRun(context.Context, jobs.PipelineRunExecutionArgs, pipelinesvc.CompletionAuthorityChecker) error
	ExecuteQueuedPipelineRun(context.Context, jobs.PipelineRunExecutionArgs) error
}

type CompletionDispatcher struct {
	enabled  bool
	txm      postgres.TransactionManager
	repo     pipelinetriggers.Repository
	checker  pipelinesvc.CompletionAuthorityChecker
	executor CompletionRunExecutor
	producer producer.PipelineRunProducer
}

func NewCompletionDispatcher(enabled bool, txm postgres.TransactionManager, repo pipelinetriggers.Repository, checker pipelinesvc.CompletionAuthorityChecker, executor CompletionRunExecutor, producer producer.PipelineRunProducer) (*CompletionDispatcher, error) {
	if txm == nil || repo == nil || checker == nil || executor == nil || producer == nil {
		return nil, xerror.Unexpected().Detail("completion dispatch dependencies are required")
	}
	return &CompletionDispatcher{enabled: enabled, txm: txm, repo: repo, checker: checker, executor: executor, producer: producer}, nil
}

func (s *CompletionDispatcher) Dispatch(ctx context.Context) error {
	if !s.enabled {
		return nil
	}
	var pending, repairs, finalizations []pipelinetriggers.CompletionDispatchRef
	if err := s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		if err := s.repo.ReconcileLogicalRunFinalizationsForSystem(ctx, db); err != nil {
			return err
		}
		if err := s.repo.CompleteCompletionSnapshotsForSystem(ctx, db); err != nil {
			return err
		}
		var err error
		finalizations, err = s.repo.ListCompletionRunFinalizationsForSystem(ctx, db)
		if err != nil {
			return err
		}
		pending, err = s.repo.ListPendingCompletionsForSystem(ctx, db)
		if err != nil {
			return err
		}
		repairs, err = s.repo.ListCompletionRepairsForSystem(ctx, db)
		return err
	}); err != nil {
		return err
	}
	var failures []error
	for _, ref := range finalizations {
		if err := s.finalizeAttempt(ctx, ref); err != nil {
			failures = append(failures, err)
			if err := s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
				return s.repo.DeferCompletionRunFinalization(ctx, db, ref.OrgSlug, ref.ID, ref.FailedRunSlug, "dispatch_retry_pending")
			}); err != nil {
				failures = append(failures, err)
			}
		}
	}
	for _, ref := range pending {
		dispatchErr := s.accept(ctx, ref)
		if dispatchErr == nil {
			continue
		}
		failures = append(failures, dispatchErr)
		// Keep safe diagnostics even when the child/job transaction rolled back.
		if err := s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
			var typed *xerror.Error
			if xerror.As(dispatchErr, &typed) && (typed.Kind() == xerror.KindInvalidInput || typed.Kind() == xerror.KindNotFound) {
				return s.repo.RefuseCompletionSnapshot(ctx, db, ref.OrgSlug, ref.ID, "dispatch_failed")
			}
			return s.repo.DeferCompletionSnapshot(ctx, db, ref.OrgSlug, ref.ID, "dispatch_retry_pending")
		}); err != nil {
			failures = append(failures, err)
		}
	}
	for _, ref := range repairs {
		if err := s.repair(ctx, ref); err != nil {
			failures = append(failures, err)
		}
	}
	return xerror.Join(failures...)
}

func (s *CompletionDispatcher) accept(ctx context.Context, ref pipelinetriggers.CompletionDispatchRef) error {
	return s.txm.RunInTx(postgres.WithConsistentSnapshot(ctx), func(ctx context.Context, db postgres.DBTX) error {
		snapshot, err := s.checker.CheckCompletionSnapshot(ctx, ref.OrgSlug, ref.ProjectSlug, ref.EnvironmentSlug, ref.ID)
		if snapshot != nil && snapshot.State != "pending" {
			return nil
		}
		if err != nil {
			return err
		}
		args, err := s.executor.AcceptCompletionRun(ctx, ref.OrgSlug, ref.ProjectSlug, ref.EnvironmentSlug, snapshot)
		if xerror.Is(err, admissionsvc.ErrAdmissionDenied) {
			return s.repo.DeferCompletionSnapshot(ctx, db, ref.OrgSlug, ref.ID, "capacity_waiting")
		}
		if err != nil {
			return err
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		return s.repo.AcceptCompletionSnapshot(ctx, db, ref.OrgSlug, ref.ID, args.RunSlug, raw)
	})
}

func (s *CompletionDispatcher) repair(ctx context.Context, ref pipelinetriggers.CompletionDispatchRef) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		snapshot, err := s.repo.LoadCompletionSnapshot(ctx, db, ref.OrgSlug, ref.ProjectSlug, ref.EnvironmentSlug, ref.ID)
		if err != nil {
			return err
		}
		if snapshot.State != "accepted" {
			return nil
		}
		var args jobs.PipelineRunExecutionArgs
		if err := json.Unmarshal(ref.ExecutionArgs, &args); err != nil {
			return xerror.Unexpected().With(err)
		}
		expectedRun := snapshot.ChildRunSlug
		if args.Source.AttemptNumber > 1 {
			expectedRun = slug.Slug(fmt.Sprintf("%s-retry-%d", snapshot.ChildRunSlug, args.Source.AttemptNumber))
		}
		if args.CompletionSnapshotID != ref.ID || args.Source.RootRunSlug != snapshot.ChildRunSlug || args.RunSlug != expectedRun || args.PipelineVersionHash != snapshot.VersionHash ||
			args.PipelineSlug != snapshot.PipelineSlug || args.DeploymentResolutionDigest == nil || *args.DeploymentResolutionDigest != snapshot.ResolutionDigest ||
			args.OrganizationSlug != ref.OrgSlug || args.ProjectSlug != ref.ProjectSlug || args.EnvironmentSlug != ref.EnvironmentSlug {
			return xerror.Unexpected().Detail("saved completion execution identity does not match its snapshot")
		}
		_, err = s.producer.EnqueuePipelineRunExecution(ctx, db, args)
		return err
	})
}

func (s *CompletionDispatcher) ExecuteQueuedPipelineRun(ctx context.Context, args jobs.PipelineRunExecutionArgs) error {
	if args.CompletionSnapshotID == 0 {
		return s.executor.ExecuteQueuedPipelineRun(ctx, args)
	}
	if !s.enabled {
		return xerror.Wrapf("snooze disabled completion execution: %w", river.JobSnooze(time.Minute))
	}
	return s.executor.ExecuteCompletionPipelineRun(ctx, args, s.checker)
}
