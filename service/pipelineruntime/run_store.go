package pipelineruntime

import (
	"context"
	"encoding/json"
	"math"
	"time"

	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/checkpoints"
	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
)

type runWriter struct {
	notifier                notificationsvc.CandidateProducer
	txm                     postgres.TransactionManager
	clock                   clock.Clock
	runStorageSvc           runStorage
	incrementalCheckpointDB incrementalcheckpoints.Repository
}

type runStore struct {
	dbtx                    postgres.DBTX
	runWriter               pipelinerun.RunPersister
	timelineStore           pipelinerun.TimelineStore
	incrementalCheckpointDB incrementalcheckpoints.Repository
}

// NewRunStore creates the service-layer run store used by pipeline execution.
func NewRunStore(
	txm postgres.TransactionManager,
	dbtx postgres.DBTX,
	clk clock.Clock,
	notifier notificationsvc.CandidateProducer,
	runs pipelineruns.Repository,
) pipelinerun.RunStore {
	if dbtx == nil {
		panic("pipelineruntime: dbtx is required")
	}
	if runs == nil {
		panic("pipelineruntime: pipeline run repository is required")
	}

	incrementalCheckpointDB := incrementalcheckpoints.New()
	return &runStore{
		dbtx:                    dbtx,
		runWriter:               newRunWriter(txm, clk, incrementalCheckpointDB, notifier, runs),
		timelineStore:           checkpoints.New(dbtx),
		incrementalCheckpointDB: incrementalCheckpointDB,
	}
}

// newRunWriter creates the pipeline run writer used by the run store.
func newRunWriter(
	txm postgres.TransactionManager,
	clk clock.Clock,
	incrementalCheckpointDB incrementalcheckpoints.Repository,
	notifier notificationsvc.CandidateProducer,
	runs pipelineruns.Repository,
) pipelinerun.RunPersister {
	if notifier == nil {
		panic("pipelineruntime: notification candidate producer is required")
	}
	if txm == nil {
		panic("pipelineruntime: transaction manager is required")
	}
	if clk == nil {
		clk = clock.New()
	}
	if incrementalCheckpointDB == nil {
		incrementalCheckpointDB = incrementalcheckpoints.New()
	}

	return &runWriter{
		notifier:                notifier,
		txm:                     txm,
		clock:                   clk,
		runStorageSvc:           newRunStorageService(runs),
		incrementalCheckpointDB: incrementalCheckpointDB,
	}
}

func (s *runStore) PersistRun(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	runResult *pipelinerun.RunResult,
) error {
	return s.runWriter.PersistRun(ctx, orgSlug, projectSlug, environmentSlug, pipeline, runResult)
}

func (s *runStore) Get(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
) (*pipelinerun.RunTimeline, error) {
	return s.timelineStore.Get(ctx, orgSlug, projectSlug, environmentSlug, runSlug)
}

func (s *runStore) SaveCheckpoint(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
	checkpoint pipelinerun.ReplayCheckpoint,
) error {
	return s.timelineStore.SaveCheckpoint(ctx, orgSlug, projectSlug, environmentSlug, runSlug, checkpoint)
}

func (s *runStore) GetCheckpoint(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	runSlug string,
) (*pipelinerun.ReplayCheckpoint, error) {
	return s.timelineStore.GetCheckpoint(ctx, orgSlug, projectSlug, environmentSlug, runSlug)
}

func (s *runStore) GetCurrentIncrementalCheckpoint(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	lookup pipelinerun.IncrementalCheckpointLookup,
) (*pipelinerun.IncrementalCheckpoint, error) {
	checkpoint, err := s.incrementalCheckpointDB.GetCurrentCommitted(
		ctx,
		s.dbtx,
		orgSlug,
		projectSlug,
		environmentSlug,
		lookup,
	)
	if xerror.Is(err, pipelinerun.ErrIncrementalCheckpointNotFound) {
		return nil, pipelinerun.ErrIncrementalCheckpointNotFound.With(err)
	}
	return checkpoint, err
}

func (p *runWriter) PersistRun(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	runResult *pipelinerun.RunResult,
) error {
	if pipeline == nil {
		return pipelinerun.ErrNilPipeline
	}
	if runResult == nil {
		return xerror.Unexpected().Detail("run result is required")
	}

	timelineJSON, err := json.Marshal(runResult.Timeline)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	incrementalDiagnosticsJSON, err := json.Marshal(runResult.IncrementalDiagnostics)
	if err != nil {
		return xerror.Unexpected().With(err)
	}

	return p.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var errorSummary *string
		var failureKind *string
		var failureSide *string
		if runResult.Timeline.Failure != nil {
			if runResult.Timeline.Failure.Error != "" {
				errorSummary = &runResult.Timeline.Failure.Error
			}
			if runResult.Timeline.Failure.Kind != "" {
				k := runResult.Timeline.Failure.Kind
				failureKind = &k
			}
			if runResult.Timeline.Failure.Side != "" {
				s := runResult.Timeline.Failure.Side
				failureSide = &s
			}
		}

		startedAt := p.clock.Now().UTC()
		if len(runResult.Timeline.Events) > 0 {
			startedAt = runResult.Timeline.Events[0].At
		}
		for _, event := range runResult.Timeline.Events {
			if event.Type == "run_started" {
				startedAt = event.At
				break
			}
		}
		var endedAt *time.Time
		if n := len(runResult.Timeline.Events); n > 0 && runResult.Metadata.Status != "running" && runResult.Metadata.Status != "queued" {
			lastAt := runResult.Timeline.Events[n-1].At
			endedAt = &lastAt
		}
		if runResult.Metadata.StepCount > math.MaxInt32 {
			return xerror.InvalidInput(xerror.PipelineRun).Detailf("step count %d exceeds int32 range", runResult.Metadata.StepCount)
		}

		err := p.runStorageSvc.CreateRun(ctx, tx, orgSlug, projectSlug, environmentSlug, &createRunParams{
			PipelineVersionID:          runResult.Metadata.PipelineVersionID,
			DeploymentID:               runResult.Metadata.DeploymentID,
			DeploymentRevision:         runResult.Metadata.DeploymentRevision,
			DeploymentResolutionDigest: optionalResolutionDigest(runResult.Metadata.DeploymentResolutionDigest),
			PipelineSlug:               string(runResult.Timeline.PipelineSlug),
			RunSlug:                    runResult.Metadata.RunSlug,
			Source:                     runResult.Metadata.Source,
			RequestID:                  reqctx.GetRequestID(ctx),
			Status:                     runResult.Metadata.Status,
			DurationMs:                 runResult.Metadata.DurationMs,
			StepCount:                  int32(runResult.Metadata.StepCount),
			StepSlug:                   lastExecutedStepSlug(pipeline, runResult),
			ErrorSummary:               errorSummary,
			FailureKind:                failureKind,
			FailureSide:                failureSide,
			StartedAt:                  startedAt,
			EndedAt:                    endedAt,
			ReplayOfRunSlug:            runResult.Metadata.ReplayOfRunSlug,
			ReplayFromStep:             runResult.Metadata.ReplayFromStep,
			Incremental:                runResult.Metadata.Incremental,
			IncrementalDiagnostics:     incrementalDiagnosticsJSON,
			Timeline:                   timelineJSON,
		})
		if err != nil {
			return xerror.Wrapf("create pipeline run: %w", err)
		}

		for _, transition := range runResult.IncrementalCheckpointTransitions {
			switch transition.Status {
			case pipelinerun.IncrementalCheckpointTransitionCommitted:
				if err := p.incrementalCheckpointDB.CommitObservation(
					ctx,
					tx,
					orgSlug,
					projectSlug,
					environmentSlug,
					transition,
				); err != nil {
					return xerror.Wrapf("commit incremental checkpoint: %w", err)
				}
			case pipelinerun.IncrementalCheckpointTransitionBlocked:
				if err := p.incrementalCheckpointDB.RecordBlockedObservation(
					ctx,
					tx,
					orgSlug,
					projectSlug,
					environmentSlug,
					transition,
				); err != nil {
					return xerror.Wrapf("record blocked incremental checkpoint: %w", err)
				}
			case pipelinerun.IncrementalCheckpointTransitionUnknown:
				continue
			}
		}

		candidates, err := runNotificationCandidates(orgSlug, projectSlug, environmentSlug, pipeline.Metadata.Slug, runResult)
		if err != nil {
			return err
		}
		for _, candidate := range candidates {
			if err := p.notifier.EnqueueCandidate(ctx, tx, candidate); err != nil {
				return xerror.Wrapf("enqueue run notification candidate: %w", err)
			}
		}

		return nil
	})
}

func optionalResolutionDigest(digest string) *string {
	if digest == "" {
		return nil
	}
	return &digest
}

func (s *runStore) PreviousSuccess(ctx context.Context, org, project, environment, pipeline slug.Slug, version int64, before time.Time) (*pipelineruntime.SuccessfulRun, error) {
	prior, err := pipelineruns.New().PreviousSuccess(ctx, s.dbtx, org, project, environment, pipeline, version, before)
	if xerror.Is(err, pipelineruns.ErrRunNotFound) {
		return nil, pipelinerun.ErrTimelineNotFound.With(err)
	}
	return prior, err
}

func (s *runStore) ObserveWorker(ctx context.Context, org, project, environment slug.Slug, run string, at time.Time) error {
	return s.timelineStore.ObserveWorker(ctx, org, project, environment, run, at)
}
