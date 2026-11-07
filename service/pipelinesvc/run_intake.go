package pipelinesvc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

var errWorkerOwnershipLost = xerror.New(xerror.KindInternal, "RUN_WORKER_OWNERSHIP_LOST", "worker ownership ended before the attempt recorded completion; external effects may be incomplete")

// AcceptQueuedRun records intake in the caller's enqueue transaction, before artifact selection.
func (s *service) AcceptQueuedRun(ctx context.Context, org, project, environment, pipeline, run slug.Slug, source pipelinerun.RunSourceSummary, at time.Time, jobID int64) error {
	if at.IsZero() || jobID <= 0 || (source.Type != model.PipelineRunSourceTypeSchedule && source.Type != model.PipelineRunSourceTypeWebhook) {
		return xerror.InvalidInput(xerror.PipelineRun).Detail("queued trigger run requires a recorded time, worker job and trigger source")
	}
	source = pipelinerun.NormalizeAttemptSource(source, run)
	timeline := queuedPipelineRunTimeline(run, pipeline, project, environment, at.UTC())
	raw, err := json.Marshal(timeline)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		_, err := s.runRepo.CreateRun(ctx, db, org, project, environment, &pipelineruns.CreateRunParams{
			WorkerJobID: &jobID, PipelineSlug: pipeline.String(), RunSlug: run.String(), Source: source,
			Status: pipelineRunStatusQueued, StartedAt: at.UTC(), Timeline: raw,
		})
		if !xerror.Is(err, pipelineruns.ErrRunAlreadyExists) {
			return err
		}
		prior, err := s.runRepo.FindRunBySlug(ctx, db, org, project, environment, run.String())
		if err != nil {
			return err
		}
		if prior.PipelineSlug != pipeline.String() || prior.Source.Type != source.Type || prior.Source.RootRunSlug != source.RootRunSlug || prior.Source.AttemptNumber != source.AttemptNumber || prior.WorkerJobID == nil || *prior.WorkerJobID != jobID {
			return xerror.InvalidInput(xerror.PipelineRun).Detail("queued run identity conflicts with retained intake")
		}
		return nil
	})
}

func (s *service) StartQueuedRun(ctx context.Context, org, project, environment, run slug.Slug, at time.Time) error {
	if at.IsZero() {
		return xerror.InvalidInput(xerror.PipelineRun).Detail("worker start requires a recorded time")
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		prior, err := s.runRepo.FindRunBySlug(ctx, db, org, project, environment, run.String())
		// Older queue entries have no intake row; their first available fact remains runtime admission.
		if xerror.Is(err, pipelineruns.ErrRunNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if prior.Status != pipelineRunStatusQueued {
			return nil
		}
		var timeline pipelinerun.RunTimeline
		if err := json.Unmarshal(prior.Timeline, &timeline); err != nil {
			return xerror.Unexpected().With(err)
		}
		timeline.Status = pipelineRunStatusRunning
		timeline.Events = append(timeline.Events, pipelinerun.TimelineEvent{At: at.UTC(), Type: pipelineRunEventStarted, Status: pipelineRunStatusRunning})
		raw, err := json.Marshal(timeline)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		_, err = s.runRepo.MarkRunRunning(ctx, db, org, project, environment, slug.FromString(prior.PipelineSlug), run.String(), at.UTC(), raw)
		if xerror.Is(err, pipelineruns.ErrRunNotFound) {
			return nil
		}
		return err
	})
}

// ReconcileInterruptedRuns closes abandoned attempts only while their queue ownership is fenced.
func (s *service) ReconcileInterruptedRuns(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return xerror.InvalidInput(xerror.PipelineRun).Detail("reconciliation requires a recorded time")
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		refs, err := s.runRepo.ListInterruptedForSystem(ctx, db, now.Add(-pipelinerun.WorkerObservationStaleAfter))
		if err != nil {
			return err
		}
		for _, ref := range refs {
			row, err := s.runRepo.FindRunBySlug(ctx, db, ref.OrgSlug, ref.ProjectSlug, ref.EnvironmentSlug, ref.RunSlug)
			if err != nil {
				return err
			}
			if err := s.completeQueuedRunFailure(ctx, jobs.PipelineRunExecutionArgs{
				OrganizationSlug: ref.OrgSlug, ProjectSlug: ref.ProjectSlug, EnvironmentSlug: ref.EnvironmentSlug,
				PipelineSlug: slug.FromString(row.PipelineSlug), RunSlug: slug.FromString(row.RunSlug),
				Actor: model.SystemEventActor(), AcceptedAt: row.StartedAt,
			}, row.StartedAt, errWorkerOwnershipLost); err != nil {
				return err
			}
		}
		return nil
	})
}

// FailUnadmittedRun records a trigger owner's rejection without rewriting admitted effects.
func (s *service) FailUnadmittedRun(ctx context.Context, org, project, environment, run slug.Slug) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		row, err := s.runRepo.FindRunBySlug(ctx, db, org, project, environment, run.String())
		if xerror.Is(err, pipelineruns.ErrRunNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.Status != "queued" && row.Status != "running" {
			return nil
		}
		var timeline pipelinerun.RunTimeline
		if err := json.Unmarshal(row.Timeline, &timeline); err != nil {
			return xerror.Unexpected().With(err)
		}
		if timeline.Revision > 0 {
			return nil
		}
		return s.completeQueuedRunFailure(ctx, jobs.PipelineRunExecutionArgs{
			OrganizationSlug: org, ProjectSlug: project, EnvironmentSlug: environment,
			PipelineSlug: slug.FromString(row.PipelineSlug), RunSlug: run, Actor: model.SystemEventActor(), AcceptedAt: row.StartedAt,
		}, row.StartedAt,
			xerror.Unexpected().Detail("trigger execution was rejected before runtime admission"))
	})
}
