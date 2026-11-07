package http

import (
	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/proofconfigsvc"
	"github.com/gofiber/fiber/v3"
)

func (s *server) ListProofConfigurations(c fiber.Ctx, orgParam api.SlugParam) error {
	org, err := slug.New(orgParam)
	if err != nil {
		return fiber.ErrBadRequest
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourcePipelines); err != nil {
		return err
	}
	configurations, err := s.proofConfigurationService.List(c.Context(), org)
	if err != nil {
		return mapServiceError(err)
	}
	response := make([]api.DataSourceProofConfiguration, 0, len(configurations))
	for _, configuration := range configurations {
		response = append(response, proofConfigurationResponse(configuration))
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(response)
}

func (s *server) GetDataSourceProofConfiguration(c fiber.Ctx, orgParam api.SlugParam, dsParam api.DataSourceSlugParam) error {
	org, ds, err := s.proofConfigurationScope(c, orgParam, dsParam, model.Read)
	if err != nil {
		return err
	}
	configuration, err := s.proofConfigurationService.Get(c.Context(), org, ds)
	if err != nil {
		return mapServiceError(err)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(proofConfigurationResponse(*configuration))
}

func (s *server) SetDataSourceProofConfiguration(c fiber.Ctx, orgParam api.SlugParam, dsParam api.DataSourceSlugParam) error {
	org, ds, err := s.proofConfigurationScope(c, orgParam, dsParam, model.Update)
	if err != nil {
		return err
	}
	var request api.DataSourceProofSettings
	if err := c.Bind().Body(&request); err != nil {
		return fiber.ErrBadRequest
	}
	settings := proofconfigsvc.Settings{Enabled: request.Enabled, PublishChecks: request.PublishChecks, ExpectedRepositoryID: request.ExpectedRepositoryId}
	for _, target := range request.Targets {
		project, err := slug.New(target.ProjectSlug)
		if err != nil {
			return fiber.ErrBadRequest
		}
		environment, err := slug.New(target.EnvironmentSlug)
		if err != nil {
			return fiber.ErrBadRequest
		}
		settings.Targets = append(settings.Targets, proofconfigsvc.Target{Project: project, Environment: environment, Ref: target.TargetRef, Path: target.PipelinePath})
	}
	configuration, err := s.proofConfigurationService.Set(c.Context(), org, ds, s.eventActor(c), settings)
	if err != nil {
		return mapServiceError(err)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(proofConfigurationResponse(*configuration))
}

func (s *server) proofConfigurationScope(c fiber.Ctx, orgParam, dsParam string, action model.Action) (slug.Slug, slug.Slug, error) {
	org, err := slug.New(orgParam)
	if err != nil {
		return "", "", fiber.ErrBadRequest
	}
	ds, err := slug.New(dsParam)
	if err != nil {
		return "", "", fiber.ErrBadRequest
	}
	if err := s.canOrg(c, org, action, model.ResourceDataSources); err != nil {
		return "", "", err
	}
	if err := s.canOrg(c, org, action, model.ResourcePipelines); err != nil {
		return "", "", err
	}
	return org, ds, nil
}

func proofConfigurationResponse(configuration proofconfigsvc.Configuration) api.DataSourceProofConfiguration {
	source := configuration.Source
	result := api.DataSourceProofConfiguration{DataSourceSlug: source.Slug.String(), Name: source.Name, Provider: source.Provider, RepositoryId: source.RepositoryID, Enabled: source.Enabled, PublishChecks: source.PublishChecks, Status: configuration.Status, Targets: []api.ProofAssessmentTarget{}, AvailableTargets: []api.ProofTargetContext{}}
	result.WebhookSetupRequired = source.Provider == "gitlab" && source.Enabled && !source.WebhookReady
	for _, target := range configuration.Targets {
		result.Targets = append(result.Targets, api.ProofAssessmentTarget{ProjectSlug: target.Project.String(), EnvironmentSlug: target.Environment.String(), TargetRef: target.Ref, PipelinePath: target.Path})
	}
	for _, target := range configuration.Contexts {
		result.AvailableTargets = append(result.AvailableTargets, api.ProofTargetContext{ProjectSlug: target.Project.String(), EnvironmentSlug: target.Environment.String(), TargetRef: target.Ref, PipelineDirectory: target.Directory})
	}
	return result
}
