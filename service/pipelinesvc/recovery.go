package pipelinesvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/protectedenv"
)

// RecoverRun accepts terminal sink failures and fenced worker loss with retained, pinned inputs.
func (s *service) RecoverRun(ctx context.Context, orgSlug, projectSlug, environmentSlug, sourceRunSlug slug.Slug, confirmation *slug.Slug, actor model.EventActor, source pipelinerun.RunSourceSummary) (*PipelineRunDetail, error) {
	original, err := s.GetRun(ctx, orgSlug, projectSlug, environmentSlug, sourceRunSlug.String())
	if err != nil {
		return nil, err
	}
	workerLoss := original.Timeline.Failure != nil && original.Timeline.Failure.ReasonCode == pipelinerun.FailureReasonCodeWorkerOwnershipLost
	if original.Metadata.Status != pipelineRunStatusFailed || original.Timeline.Failure == nil || (original.Timeline.Failure.Side != "sink" && !workerLoss) {
		return nil, xerror.InvalidInput(xerror.PipelineRun).WithPublicDetail("Recovery requires a terminal sink failure or fenced worker-loss attempt with complete retained source and transform outputs.")
	}
	if err := s.admission.CheckRunAccess(ctx, orgSlug); err != nil {
		return nil, err
	}
	protection, err := s.pipelineStore.ProjectEnvironmentProtection(ctx, orgSlug, projectSlug, environmentSlug)
	if err != nil {
		return nil, err
	}
	if err := protectedenv.Confirm(protection, confirmation); err != nil {
		return nil, err
	}
	pipelineSlug, err := slug.New(original.Metadata.PipelineSlug)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	version, err := s.pipelineStore.FindVersionBySpecHash(ctx, orgSlug, projectSlug, pipelineSlug, original.Metadata.PipelineVersionSpecHash)
	if err != nil {
		return nil, err
	}
	if version.ID != original.Metadata.PipelineVersionID {
		return nil, xerror.InvalidInput(xerror.PipelineRun).Detail("recovery version does not match the original run")
	}
	source.EnvironmentSlug = environmentSlug
	source.Summary = "Operator sink recovery"
	source, err = validateRunSourceSummary(source)
	if err != nil {
		return nil, err
	}
	runSlug, err := acceptedPipelineRunSlug(nil, pipelineSlug, s.executionClock.Now().UTC())
	if err != nil {
		return nil, err
	}
	originalIdentity := sourceRunSlug.String()
	if original.Metadata.ReplayOfRunSlug != nil {
		originalIdentity = *original.Metadata.ReplayOfRunSlug
	}
	if original.Timeline.SinkReplayIdentity != "" {
		originalIdentity = original.Timeline.SinkReplayIdentity
	}
	// Record the operator's attempt before dispatch so interruption remains audited.
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if workerLoss {
			if err := s.runRepo.LockRecoveryFence(ctx, tx, orgSlug, projectSlug, environmentSlug, sourceRunSlug.String()); err != nil {
				if xerror.Is(err, pipelineruns.ErrRunNotFound) {
					return xerror.InvalidInput(xerror.PipelineRun).With(err).WithPublicDetail("Worker-loss recovery requires retained queue evidence that ownership has ended. Wait for native queue rescue and terminal reconciliation; silence or a missing queue record is insufficient.")
				}
				return err
			}
		}
		_, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, pipelineRunExecutedEvent{
			projectSlug: projectSlug, environmentSlug: environmentSlug, pipelineSlug: pipelineSlug,
			runSlug: runSlug, versionSpecHash: version.SpecHash, status: "recovery_requested",
			sourceType: source.Type.String(), sourceSummary: source.Summary, sourceEntrypoint: source.Entrypoint.String(),
			replayOfRunSlug: &originalIdentity, protectedEnvironment: protection.Protected,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	var outcome *pipelineruntime.RunOutcome
	runErr := s.admission.WithPipelineExecution(ctx, orgSlug, false, func(ctx context.Context) error {
		var err error
		outcome, err = s.runtime.Recover(ctx, pipelineruntime.ReplayRequest{
			OrgSlug: orgSlug, ProjectSlug: projectSlug, EnvironmentSlug: environmentSlug,
			SourceRunSlug: sourceRunSlug.String(), ReplayRunSlug: runSlug.String(), Source: source,
			InputMode: pipelineruntime.ReplayInputModePinned, PipelineVersionID: version.ID,
		})
		return err
	})
	if outcome == nil {
		return nil, runErr
	}
	run, err := runDetailFromOutcome(ctx, s.executionClock, outcome, version.SpecHash)
	if err != nil {
		return nil, err
	}
	if err := s.recordPipelineRunEventInTx(ctx, orgSlug, projectSlug, actor, run, protection.Protected); err != nil {
		// Returning a failed run here would let HTTP mask audit failure as a normal recovery result.
		return nil, err
	}
	return run, runErr
}
