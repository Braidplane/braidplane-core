package http

import (
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/runrecencysvc"
	"github.com/gofiber/fiber/v3"
)

func (s *server) GetPipelineOperationalCapabilities(c fiber.Ctx, orgParam api.SlugParam) error {
	org, err := slug.New(orgParam)
	if err != nil {
		return apierror.ValidationFailed("Invalid organization slug")
	}
	if err := s.canOrg(c, org, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	types := make([]api.NotificationType, 0)
	for _, kind := range model.ProducerBackedNotificationTypes() {
		types = append(types, api.NotificationType(kind.String()))
	}
	return c.JSON(api.PipelineOperationalCapabilities{SuccessfulRunRecency: true, NotificationTypes: types})
}

func (s *server) GetSuccessfulRunRecency(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, envParam api.EnvironmentSlugParam, pipelineParam api.PipelineSlugParam) error {
	org, project, environment, pipeline, err := parseDeploymentSlugs(orgParam, projectParam, envParam, pipelineParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	result, err := s.runRecencyService.Inspect(c.Context(), runrecencysvc.Scope{Org: org, Project: project, Environment: environment, Pipeline: pipeline}, time.Now())
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(toAPIRecency(result))
}

func (s *server) UpdateSuccessfulRunRecency(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, envParam api.EnvironmentSlugParam, pipelineParam api.PipelineSlugParam) error {
	org, project, environment, pipeline, err := parseDeploymentSlugs(orgParam, projectParam, envParam, pipelineParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourcePipelines); err != nil {
		return err
	}
	var req api.SuccessfulRunRecencyUpdate
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	if req.MaximumAgeSeconds <= 0 || req.MaximumAgeSeconds > 9223372036 || req.ExpectedRevision < 0 || req.ExpectedRevision == 9223372036854775807 {
		return apierror.ValidationFailed("Use a positive whole-second maximum age and a nonnegative expected revision")
	}
	// Updates require a current deployment, including no-op writes and disablement.
	if _, err := s.pipelinesService.GetDeployment(c.Context(), org, project, environment, pipeline); err != nil {
		return mapServiceError(err)
	}
	scope := runrecencysvc.Scope{Org: org, Project: project, Environment: environment, Pipeline: pipeline}
	now := time.Now()
	if _, err := s.runRecencyService.Update(c.Context(), scope, req.ExpectedRevision, time.Duration(req.MaximumAgeSeconds)*time.Second, req.Enabled, now); err != nil {
		return mapServiceError(err)
	}
	result, err := s.runRecencyService.Inspect(c.Context(), scope, now)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(toAPIRecency(result))
}

func toAPIRecency(i *runrecencysvc.Inspection) api.SuccessfulRunRecency {
	e := i.Expectation
	result := api.SuccessfulRunRecency{CapabilityEnabled: true, Configured: e.Revision > 0, Enabled: e.Enabled, Revision: e.Revision, EvaluatedAt: i.EvaluatedAt, DeadlineAt: i.Deadline, Outcome: api.SuccessfulRunRecencyOutcome(i.Outcome.String()), UnavailableReason: i.UnavailableReason, Episodes: make([]api.SuccessfulRunRecencyEpisode, 0, len(i.Episodes)), HasMoreEpisodes: i.HasMoreEpisodes}
	if e.Revision > 0 {
		age := int64(e.MaximumAge / time.Second)
		result.MaximumAgeSeconds, result.EnabledAt = &age, &e.EnabledAt
	}
	if e.DeploymentID != 0 {
		result.DeploymentRevision, result.VersionSpecHash = &e.DeploymentRevision, e.VersionSpecHash
		result.ResolutionDigest, result.DeployedAt = e.ResolutionDigest, &e.DeployedAt
		result.SuccessRunSlug, result.SucceededAt = e.SuccessRun, e.SucceededAt
	}
	for _, ep := range i.Episodes {
		result.Episodes = append(result.Episodes, api.SuccessfulRunRecencyEpisode{NotificationSlug: ep.Slug.String(), ExpectationRevision: ep.ExpectationRevision, MaximumAgeSeconds: int64(ep.MaximumAge / time.Second), EnabledAt: ep.EnabledAt, DeploymentRevision: ep.DeploymentRevision, VersionSpecHash: ep.VersionSpecHash, ResolutionDigest: ep.ResolutionDigest, DeployedAt: ep.DeployedAt, DeadlineAt: ep.Deadline, BreachedAt: ep.BreachedAt, LastSuccessRunSlug: ep.LastSuccessRun, LastSuccessAt: ep.LastSuccessAt, ResolvedAt: ep.ResolvedAt, ResolutionReason: ep.ResolutionReason, RecoveryRunSlug: ep.RecoveryRun, RecoverySucceededAt: ep.RecoverySucceededAt})
	}
	return result
}
