package pipelinesvc

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelineruntimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/protectedenv"
)

// BootstrapCheckpoints initializes source progress atomically with its required audit event.
func (s *service) BootstrapCheckpoints(ctx context.Context, org, project, environment, pipelineSlug, requestSlug slug.Slug, confirmation *slug.Slug, actor model.EventActor, input map[string]any, seeds map[slug.Slug]json.RawMessage) error {
	if requestSlug == "" || len(seeds) == 0 {
		return xerror.InvalidInput(xerror.PipelineCheckpoint).Detail("bootstrap requires a request slug and source seeds")
	}
	if err := s.admission.CheckWriteAccess(ctx, org); err != nil {
		return err
	}
	if err := pipelineruntimevars.ValidateInput(input); err != nil {
		return err
	}
	deployment, err := s.pipelineStore.FindDeployment(ctx, org, project, environment, pipelineSlug)
	if err != nil {
		return err
	}
	protection, err := s.pipelineStore.ProjectEnvironmentProtection(ctx, org, project, environment)
	if err != nil {
		return err
	}
	if err := protectedenv.Confirm(protection, confirmation); err != nil {
		return err
	}
	version, err := s.pipelineStore.FindVersionBySpecHash(ctx, org, project, pipelineSlug, deployment.CurrentVersionSpecHash)
	if err != nil {
		return err
	}
	pipeline, _, err := s.executionPipeline(ctx, org, project, environment, version, deployment.CurrentResolutionDigest)
	if err != nil {
		return err
	}
	if deployment.CurrentResolutionDigest == nil && s.dataSourceRefResolver != nil {
		resolved, err := s.dataSourceRefResolver.ResolvePipelineDataSourceRefs(ctx, org, project, environment, pipeline)
		if err != nil {
			return err
		}
		if resolved != nil && resolved.Pipeline != nil {
			pipeline = resolved.Pipeline
		}
	}
	lookups, err := s.runtime.PrepareCheckpointBootstrap(ctx, pipelineruntime.RunRequest{OrgSlug: org, ProjectSlug: project, EnvironmentSlug: environment, Pipeline: pipeline, PipelineVersionID: version.ID, RuntimeInput: input})
	if err != nil {
		return err
	}
	if len(lookups) != len(seeds) {
		return xerror.InvalidInput(xerror.PipelineCheckpoint).Detail("bootstrap requires exactly one seed for every incremental source")
	}
	sort.Slice(lookups, func(i, j int) bool { return lookups[i].SourceStepSlug < lookups[j].SourceStepSlug })
	for _, lookup := range lookups {
		if _, err := checkpointBoundValue(seeds[lookup.SourceStepSlug], lookup.CheckpointKind, lookup.OperationType); err != nil {
			return err
		}
	}
	at := s.executionClock.Now().UTC()
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		for _, lookup := range lookups {
			if err := s.checkpoints.EnsureCommitLock(ctx, tx, org, project, environment, lookup); err != nil {
				return err
			}
			if err := s.checkpoints.LockCommit(ctx, tx, org, project, environment, lookup); err != nil {
				return err
			}
			metadata, err := json.Marshal(map[string]any{"operation": "checkpoint_bootstrap", "request_slug": requestSlug.String(), "summary": "Bootstrapped checkpoint"})
			if err != nil {
				return err
			}
			err = s.checkpoints.BootstrapInitial(ctx, tx, org, project, environment, pipelinerun.IncrementalCheckpointTransition{
				Lookup: lookup, CheckpointKind: lookup.CheckpointKind, ObservedUpperBound: seeds[lookup.SourceStepSlug], ObservedRunSlug: requestSlug.String(), DisplayMetadata: metadata, ObservedAt: at,
			})
			if err != nil {
				return err
			}
		}
		_, err := s.eventProducer.EnqueueRequired(ctx, tx, org, actor, pipelineEvent{
			securityAudit: true, projectSlug: project, pipelineSlug: pipelineSlug, action: model.EventActionUpdate,
			metadata: map[string]any{"operation": "checkpoint_bootstrap", "environment_slug": environment.String(), "request_slug": requestSlug.String(), "source_count": len(seeds), "checkpoint_policy": "initial_only"},
		})
		return err
	})
}
