package http

import (
	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/gofiber/fiber/v3"
)

func (s *server) GetPipelineExecutionControl(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, envParam api.EnvironmentSlugParam, pipelineParam api.PipelineSlugParam) error {
	org, project, environment, pipeline, err := parseDeploymentSlugs(orgParam, projectParam, envParam, pipelineParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	state, err := s.pipelinesService.GetExecutionControl(c.Context(), pipelinesvc.ExecutionScope{Org: org, Project: project, Environment: environment, Pipeline: pipeline})
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(toAPIExecutionControl(state))
}

func (s *server) SetPipelineExecutionControl(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, envParam api.EnvironmentSlugParam, pipelineParam api.PipelineSlugParam) error {
	org, project, environment, pipeline, err := parseDeploymentSlugs(orgParam, projectParam, envParam, pipelineParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourcePipelines); err != nil {
		return err
	}
	var req struct {
		Paused           *bool  `json:"paused"`
		ExpectedRevision *int64 `json:"expected_revision"`
	}
	if err := c.Bind().Body(&req); err != nil || req.Paused == nil || req.ExpectedRevision == nil {
		return apierror.InvalidRequestBody()
	}
	state, err := s.pipelinesService.SetExecutionControl(c.Context(), pipelinesvc.ExecutionScope{Org: org, Project: project, Environment: environment, Pipeline: pipeline}, *req.ExpectedRevision, *req.Paused, s.eventActor(c))
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(toAPIExecutionControl(state))
}

func toAPIExecutionControl(state *pipelinesvc.ExecutionControl) api.PipelineExecutionControl {
	attempts := make([]api.PipelineExecutionControlAttempt, 0, len(state.Attempts))
	for _, attempt := range state.Attempts {
		attempts = append(attempts, api.PipelineExecutionControlAttempt{
			RunSlug: attempt.RunSlug, SourceType: attempt.SourceType, Phase: attempt.Phase, Status: attempt.Status,
			StartedAt: attempt.StartedAt, EndedAt: attempt.EndedAt, KnownCompletedWrite: attempt.KnownCompletedWrite,
			OutcomeUncertain: attempt.OutcomeUncertain, LastObservedAt: attempt.LastObservedAt, HasWorkerJob: attempt.HasWorkerJob,
		})
	}
	return api.PipelineExecutionControl{
		Paused: state.Paused, Revision: state.Revision, Drained: state.Paused && state.ActiveCount == 0,
		ChangedAt: state.ChangedAt, ChangedByType: state.ChangedByType, ChangedBySlug: state.ChangedBySlug,
		QueuedCount: state.QueuedCount, ActiveCount: state.ActiveCount, FinishedCount: state.FinishedCount, UncertainCount: state.UncertainCount, Attempts: attempts,
	}
}
