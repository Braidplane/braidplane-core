package pipelinesvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

type ScheduledPreflightTarget struct {
	VersionHash      string
	ResolutionDigest *string
	Pipeline         *pipelinespec.Pipeline
}

// ResolveScheduledPreflight uses the scheduled run's version policy and environment.
// Subsequent checks pass the original hash and resolution digest while refreshes finish.
func (s *service) ResolveScheduledPreflight(
	ctx context.Context, org, project, environment, pipelineSlug slug.Slug,
	policy model.PipelineTriggerVersionPolicy, versionHash, resolutionDigest *string,
) (*ScheduledPreflightTarget, error) {
	if versionHash == nil {
		deployment, err := s.pipelineStore.FindDeployment(ctx, org, project, environment, pipelineSlug)
		if err != nil {
			return nil, err
		}
		switch policy.Mode {
		case model.PipelineTriggerVersionPolicyModeLatestDeployed:
			versionHash = &deployment.CurrentVersionSpecHash
		case model.PipelineTriggerVersionPolicyModePinned:
			versionHash = policy.PinnedVersionHash
		case model.PipelineTriggerVersionPolicyModeUnknown:
			return nil, xerror.InvalidInput(xerror.PipelineTrigger).Detail("schedule version policy is invalid")
		}
		if versionHash == nil || *versionHash == "" {
			return nil, xerror.InvalidInput(xerror.PipelineTrigger).Detail("schedule has no selected pipeline version")
		}
		if *versionHash == deployment.CurrentVersionSpecHash {
			resolutionDigest = deployment.CurrentResolutionDigest
		}
	}
	target := &ScheduledPreflightTarget{VersionHash: *versionHash, ResolutionDigest: resolutionDigest}
	version, err := s.pipelineStore.FindVersionBySpecHash(ctx, org, project, pipelineSlug, *versionHash)
	if err != nil {
		return target, err
	}
	pipeline, _, err := s.executionPipeline(ctx, org, project, environment, version, resolutionDigest)
	if err != nil {
		return target, err
	}
	if resolutionDigest == nil {
		pipeline, err = s.resolveExecutionDataSources(ctx, org, project, environment, pipeline)
		if err != nil {
			return target, err
		}
	}
	target.Pipeline = pipeline
	return target, nil
}

func (s *service) resolveExecutionDataSources(ctx context.Context, org, project, environment slug.Slug, pipeline *pipelinespec.Pipeline) (*pipelinespec.Pipeline, error) {
	if s.dataSourceRefResolver == nil {
		return pipeline, nil
	}
	resolved, err := s.dataSourceRefResolver.ResolvePipelineDataSourceRefs(ctx, org, project, environment, pipeline)
	if err != nil {
		return nil, err
	}
	if resolved != nil && resolved.Pipeline != nil {
		return resolved.Pipeline, nil
	}
	return pipeline, nil
}

// ValidateScheduledPreflight reads contracts and cached metadata for the resolved version.
func (s *service) ValidateScheduledPreflight(ctx context.Context, org, environment slug.Slug, pipeline *pipelinespec.Pipeline) (*model.PipelineSchedulePreflightDiagnostic, error) {
	if s.runtime == nil {
		return nil, xerror.ExecutionUnavailable().Detail("pipeline validation runtime is required")
	}
	operations, err := s.resolveSavedWorkspaceOperations(ctx, org, environment, pipeline)
	if err != nil {
		if !xerror.Is(err, queriessvc.ErrQueryNotFound) && !xerror.Is(err, xerror.InvalidInput(xerror.Query)) {
			return nil, err
		}
		return scheduledPreflightDiagnostic(err, pipeline), nil
	}
	if err := pipelinevalidate.Validate(operations); err != nil {
		return scheduledPreflightDiagnostic(err, operations), nil
	}
	if err := s.runtime.PreValidateTransforms(ctx, org, pipeline); err != nil {
		if !isPipelineValidationDiagnosticError(err) {
			return nil, err
		}
		return scheduledPreflightDiagnostic(err, pipeline), nil
	}
	if err := pipelinevalidate.ValidateStepInputCompatibility(pipeline); err != nil {
		return scheduledPreflightDiagnostic(err, pipeline), nil
	}
	return nil, nil //nolint:nilnil // A successful check has no diagnostic.
}

func scheduledPreflightDiagnostic(err error, pipeline *pipelinespec.Pipeline) *model.PipelineSchedulePreflightDiagnostic {
	diagnostic := pipelineValidationDiagnosticFromPipelineError(err, pipeline)
	result := &model.PipelineSchedulePreflightDiagnostic{
		Code: diagnostic.Code, Message: diagnostic.Message, StepSlug: diagnostic.StepSlug,
	}
	if diagnostic.StepSlug != nil {
		for _, step := range pipeline.Steps {
			if step.Slug.String() == *diagnostic.StepSlug && step.Query != nil && step.Query.DataSourceSlug != "" {
				result.DataSourceSlug = &step.Query.DataSourceSlug
				break
			}
		}
	}
	return result
}
