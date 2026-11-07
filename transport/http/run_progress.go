package http

import (
	"fmt"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (s *server) GetPipelineRunProgress(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, environmentParam api.EnvironmentSlugParam, runParam api.RunSlugParam, params api.GetPipelineRunProgressParams) error {
	org, project, environment, err := parseOrgProjectEnvironmentSlugs(orgParam, projectParam, environmentParam)
	if err != nil {
		return err
	}
	run, err := slug.New(runParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid run slug: %v", err))
	}
	if err := s.canOrg(c, org, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	after, limit := int64(0), 100
	if params.AfterEvent != nil {
		after = *params.AfterEvent
	}
	if params.Limit != nil {
		limit = *params.Limit
	}
	progress, err := s.pipelinesService.GetRunProgress(c.Context(), org, project, environment, run.String(), after, limit)
	if err != nil {
		return mapServiceError(err)
	}
	events := make([]api.PipelineRunTimelineEvent, len(progress.Events))
	for i, event := range progress.Events {
		events[i] = api.PipelineRunTimelineEvent{At: event.At, Type: event.Type, Step: optionalCheckString(event.Step.String()), Status: event.Status}
	}
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.JSON(api.PipelineRunProgress{
		CompiledPlanDigest: progress.CompiledPlanDigest, DeploymentResolutionDigest: progress.DeploymentResolutionDigest, CompletedSteps: progress.CompletedSteps, FailedSteps: progress.FailedSteps, PendingSteps: progress.PendingSteps, InterruptedSteps: progress.InterruptedSteps, CurrentStep: progress.CurrentStep,
		Metadata: toAPIPipelineRunMetadata(&progress.Metadata), Revision: progress.Revision,
		Events: events, AfterEvent: progress.AfterEvent, NextAfterEvent: progress.NextAfterEvent,
		EventCount: progress.EventCount, HasMore: progress.HasMore,
		WorkerObservedAt: progress.WorkerObservedAt, WorkerLiveness: api.PipelineRunProgressWorkerLiveness(progress.WorkerLiveness.String()),
		ObservedAt: progress.ObservedAt, ServerTime: progress.ServerTime,
	})
}
