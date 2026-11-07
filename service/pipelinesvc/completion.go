package pipelinesvc

import (
	"context"
	"fmt"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type CompletionAuthorityChecker interface {
	CheckCompletionSnapshot(context.Context, slug.Slug, slug.Slug, slug.Slug, int) (*model.PipelineCompletionSnapshot, error)
}

// AcceptCompletionRun shares the dispatcher's transaction, including its
// authority locks and the snapshot's acceptance decision.
func (s *service) AcceptCompletionRun(ctx context.Context, org, project, environment slug.Slug, snapshot *model.PipelineCompletionSnapshot) (jobs.PipelineRunExecutionArgs, error) {
	if snapshot == nil || snapshot.State != "pending" || snapshot.VersionID == 0 || snapshot.DeploymentID == 0 {
		return jobs.PipelineRunExecutionArgs{}, xerror.InvalidInput(xerror.PipelineTrigger).Detail("completion snapshot is not admissible")
	}
	acceptedAt := s.executionClock.Now().UTC()
	runSlug := slug.Slug(fmt.Sprintf("completion-%d", snapshot.ID))
	source := pipelinerun.NormalizeAttemptSource(pipelinerun.RunSourceSummary{
		Type: model.PipelineRunSourceTypeCompletion, Entrypoint: model.PipelineRunSourceEntrypointScheduler,
		ActorType: model.EventActorTypeSystem, EnvironmentSlug: environment, ParameterKeys: []string{},
		// Causal endpoint details remain in the snapshot until independently authorized reads.
		Summary: "Completion-triggered run",
	}, runSlug)
	return s.acceptCompletionAttempt(ctx, org, project, environment, snapshot, runSlug, source, acceptedAt, time.Time{})
}

func (s *service) AcceptCompletionRetryRun(ctx context.Context, org, project, environment slug.Slug, snapshot *model.PipelineCompletionSnapshot, runSlug slug.Slug, source pipelinerun.RunSourceSummary, scheduledAt time.Time) (jobs.PipelineRunExecutionArgs, error) {
	if snapshot == nil || snapshot.State != "accepted" || source.Type != model.PipelineRunSourceTypeCompletion ||
		source.RootRunSlug != snapshot.ChildRunSlug || source.AttemptNumber < 2 || source.AttemptNumber > 10 ||
		runSlug.String() != fmt.Sprintf("%s-retry-%d", snapshot.ChildRunSlug, source.AttemptNumber) {
		return jobs.PipelineRunExecutionArgs{}, xerror.InvalidInput(xerror.PipelineRun).Detail("completion retry does not match its accepted root")
	}
	source.EnvironmentSlug = environment
	validated, err := validateRunSourceSummary(source)
	if err != nil {
		return jobs.PipelineRunExecutionArgs{}, err
	}
	return s.acceptCompletionAttempt(ctx, org, project, environment, snapshot, runSlug, validated, s.executionClock.Now().UTC(), scheduledAt)
}

func (s *service) acceptCompletionAttempt(ctx context.Context, org, project, environment slug.Slug, snapshot *model.PipelineCompletionSnapshot, runSlug slug.Slug, source pipelinerun.RunSourceSummary, acceptedAt, scheduledAt time.Time) (jobs.PipelineRunExecutionArgs, error) {
	args := jobs.PipelineRunExecutionArgs{
		ScheduledAt:          scheduledAt,
		CompletionSnapshotID: snapshot.ID, OrganizationSlug: org, ProjectSlug: project, EnvironmentSlug: environment,
		PipelineSlug: snapshot.PipelineSlug, PipelineVersionHash: snapshot.VersionHash,
		DeploymentResolutionDigest: &snapshot.ResolutionDigest, RunSlug: runSlug, AcceptedAt: acceptedAt,
		Actor: model.SystemEventActor(), Source: source,
	}
	deployment := &pipelinesrepo.PipelineDeployment{
		ID: snapshot.DeploymentID, Revision: snapshot.DeploymentRevision,
		CurrentVersionID: snapshot.VersionID, CurrentVersionSpecHash: snapshot.VersionHash,
		CurrentResolutionDigest: &snapshot.ResolutionDigest,
	}
	err := s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		if _, _, err := s.createQueuedRun(ctx, db, org, project, environment, snapshot.PipelineSlug, runSlug,
			snapshot.VersionID, source, acceptedAt, deployment, pipelinerun.IncrementalRunOptions{}); err != nil {
			return err
		}
		_, err := s.pipelineRunProducer.EnqueuePipelineRunExecution(ctx, db, args)
		return err
	})
	return args, err
}

func (s *service) ExecuteCompletionPipelineRun(ctx context.Context, args jobs.PipelineRunExecutionArgs, checker CompletionAuthorityChecker) error {
	if checker == nil || args.CompletionSnapshotID <= 0 || args.Source.Type != model.PipelineRunSourceTypeCompletion {
		return xerror.InvalidInput(xerror.PipelineRun).Detail("completion execution requires its authority checker and snapshot")
	}
	return s.executeQueuedPipelineRun(ctx, args, checker)
}

// GetRunCompletion reads historical evidence without checking current execution eligibility.
func (s *service) GetRunCompletion(ctx context.Context, org, project, environment, run slug.Slug) (*model.PipelineRunCompletion, error) {
	var evidence *model.PipelineRunCompletion
	err := s.txm.RunInTx(postgres.WithConsistentSnapshot(ctx), func(ctx context.Context, db postgres.DBTX) error {
		var err error
		evidence, err = s.runRepo.InspectLogicalRun(ctx, db, org, project, environment, run)
		if err != nil {
			if xerror.Is(err, pipelineruns.ErrRunNotFound) {
				return ErrPipelineRunNotFound.With(err)
			}
			return err
		}
		decisions, err := s.runRepo.ListCompletionDecisions(ctx, db, org, project, environment, run)
		if err != nil {
			return err
		}
		if len(decisions) > 100 {
			evidence.Truncated = true
			decisions = decisions[:100]
		}
		for _, decision := range decisions {
			if decision.IsParent {
				evidence.Parent = &decision
			} else {
				evidence.Children = append(evidence.Children, decision)
			}
		}
		return nil
	})
	return evidence, err
}
