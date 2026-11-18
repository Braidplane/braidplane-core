package http

import (
	"fmt"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/readinesssvc"
)

func (s *server) GetProjectReadiness(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam) error {
	return s.getReadiness(c, org, project, "")
}

func (s *server) GetPipelineReadiness(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam, pipeline api.PipelineSlugParam) error {
	pipelineSlug, err := slug.New(pipeline)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid pipeline slug: %v", err))
	}
	return s.getReadiness(c, org, project, pipelineSlug)
}

func (s *server) getReadiness(c fiber.Ctx, org api.SlugParam, project api.ProjectSlugParam, pipeline slug.Slug) error {
	orgSlug, projectSlug, err := parseOrgProjectSlugs(org, project)
	if err != nil {
		return err
	}
	for _, resource := range []model.Resource{model.ResourcePipelines, model.ResourceEnvironments, model.ResourceDataSources} {
		if err := s.canOrg(c, orgSlug, model.Read, resource); err != nil {
			return err
		}
	}
	if pipeline == "" {
		if _, err := s.projectsService.GetProject(c.Context(), orgSlug, projectSlug); err != nil {
			return mapServiceError(err)
		}
	}
	report, err := readinesssvc.Load(c.Context(), s.pipelinesService, s.environmentsService, s.dataSourceRefsService, orgSlug, projectSlug, pipeline)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(report)
}
