package pipelinetriggersvc

import (
	"context"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/pipelineschedulesvc"
)

func (s *CompletionDispatcher) finalizeAttempt(ctx context.Context, ref pipelinetriggers.CompletionDispatchRef) error {
	return s.txm.RunInTx(postgres.WithConsistentSnapshot(ctx), func(ctx context.Context, db postgres.DBTX) error {
		snapshot, authorityErr := s.checker.CheckCompletionSnapshot(ctx, ref.OrgSlug, ref.ProjectSlug, ref.EnvironmentSlug, ref.ID)
		if snapshot != nil && snapshot.State != "accepted" {
			return nil
		}
		if authorityErr != nil {
			var typed *xerror.Error
			if snapshot == nil || !xerror.As(authorityErr, &typed) || typed.Kind() != xerror.KindInvalidInput {
				return authorityErr
			}
		}
		evaluatedAt, err := s.repo.FenceCompletionRetryDecision(ctx, db, ref.OrgSlug, ref.ID)
		if err != nil {
			return err
		}
		// A candidate listed before this transaction may already have a committed retry decision.
		decisions, err := s.repo.ListTriggerRetryAttemptsForRun(ctx, db, ref.OrgSlug, ref.ProjectSlug, ref.EnvironmentSlug, ref.FailedRunSlug)
		if err != nil {
			return err
		}
		for _, decision := range decisions {
			if decision.RetryOfRunSlug == ref.FailedRunSlug {
				return nil
			}
		}
		failed, err := s.executor.GetRun(ctx, ref.OrgSlug, ref.ProjectSlug, ref.EnvironmentSlug, ref.FailedRunSlug.String())
		if err != nil {
			return err
		}
		if failed.Metadata.Status != "failed" || failed.Metadata.Source.RootRunSlug != snapshot.ChildRunSlug ||
			failed.Metadata.PipelineVersionSpecHash != snapshot.VersionHash || failed.Metadata.PipelineSlug != snapshot.PipelineSlug.String() {
			return xerror.Unexpected().Detail("completion finalization does not match its failed attempt")
		}
		root := failed
		if failed.Metadata.Source.AttemptNumber > 1 {
			root, err = s.executor.GetRun(ctx, ref.OrgSlug, ref.ProjectSlug, ref.EnvironmentSlug, snapshot.ChildRunSlug.String())
			if err != nil {
				return err
			}
		}
		baseAt := failed.Metadata.StartedAt
		if evaluatedAt.After(baseAt) {
			baseAt = evaluatedAt
		}
		record, enqueue, err := pipelineschedulesvc.BuildRetryAttempt(pipelineschedulesvc.RetryDecisionInput{
			OrgSlug: ref.OrgSlug, ProjectSlug: ref.ProjectSlug, EnvironmentSlug: ref.EnvironmentSlug,
			TriggerSlug: snapshot.TriggerSlug, TriggerRevision: snapshot.TriggerRevision, PipelineSlug: snapshot.PipelineSlug,
			VersionPolicy:           model.PipelineTriggerVersionPolicy{Mode: model.PipelineTriggerVersionPolicyModePinned, PinnedVersionHash: &snapshot.VersionHash},
			PipelineVersionSpecHash: snapshot.VersionHash, RuntimeInputKeys: []string{}, RetryPolicy: snapshot.RetryPolicy,
			FailedRun: failed, BaseAt: baseAt, ElapsedBaseAt: root.Metadata.StartedAt,
		})
		if err != nil {
			return err
		}
		if authorityErr != nil {
			record.Status = model.PipelineTriggerRetryAttemptStatusSkipped
			record.DecisionReason = snapshot.DecisionReason
			enqueue = false
		}
		if !enqueue {
			_, err := s.repo.RecordTriggerRetryAttempt(ctx, db, record)
			return err
		}
		args, err := s.executor.AcceptCompletionRetryRun(ctx, ref.OrgSlug, ref.ProjectSlug, ref.EnvironmentSlug, snapshot, record.RunSlug, record.Source, record.ScheduledAt)
		if xerror.Is(err, admissionsvc.ErrAdmissionDenied) {
			return s.repo.DeferCompletionRunFinalization(ctx, db, ref.OrgSlug, ref.ID, ref.FailedRunSlug, "capacity_waiting")
		}
		if err != nil {
			return err
		}
		if _, err := s.repo.RecordTriggerRetryAttempt(ctx, db, record); err != nil {
			return err
		}
		raw, err := json.Marshal(args)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		return s.repo.SaveCompletionRetryExecution(ctx, db, ref.OrgSlug, ref.ID, record.RunSlug, raw)
	})
}
