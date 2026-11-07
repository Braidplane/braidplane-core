package http

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func (s *server) ListProjects(
	c fiber.Ctx,
	slugParam api.SlugParam,
	params api.ListProjectsParams,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceProjects); err != nil {
		return err
	}

	response, err := s.projectsService.ListProjects(
		c.Context(),
		orgSlug,
		toProjectsListRequest(params),
	)
	if err != nil {
		return mapServiceError(err)
	}

	apiProjects := make([]api.Project, 0, len(response.Items))
	for _, project := range response.Items {
		apiProjects = append(apiProjects, projectToHTTP(project))
	}

	return c.JSON(api.ProjectListResponse{
		Projects:   apiProjects,
		HasMore:    response.HasMore,
		NextCursor: response.NextCursor,
		PrevCursor: response.PrevCursor,
	})
}

func (s *server) CreateProject(c fiber.Ctx, slugParam api.SlugParam) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Create, model.ResourceProjects); err != nil {
		return err
	}
	if err := s.admissionService.CheckProjectAdmission(c.Context(), orgSlug); err != nil {
		return mapServiceError(err)
	}

	var req api.NewProject
	if requestBodyHasKey(c.Body(), "pipeline_backend_data_source_slug") {
		return apierror.ValidationFailed(
			"pipeline_backend_data_source_slug is no longer supported; use PUT /pipeline-backend",
		)
	}
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	var projectSlug slug.Slug
	if req.Slug != nil {
		parsedSlug, err := slug.New(*req.Slug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid project slug: %v", err))
		}
		projectSlug = parsedSlug
	}

	created, err := s.projectsService.CreateProject(
		c.Context(),
		orgSlug,
		model.NewProject{
			Name: req.Name,
			Slug: projectSlug,
		},
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.Status(fiber.StatusCreated).JSON(projectToHTTP(*created))
}

func (s *server) GetProject(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	projectSlug, err := slug.New(projectSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid project slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Read, model.ResourceProjects); err != nil {
		return err
	}

	project, err := s.projectsService.GetProject(c.Context(), orgSlug, projectSlug)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(projectToHTTP(*project))
}

func (s *server) UpdateProject(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	projectSlug, err := slug.New(projectSlugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid project slug: %v", err))
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceProjects); err != nil {
		return err
	}

	var req api.UpdateProject
	if requestBodyHasKey(c.Body(), "pipeline_backend_data_source_slug") {
		return apierror.ValidationFailed(
			"pipeline_backend_data_source_slug is no longer supported; use PUT /pipeline-backend",
		)
	}
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	project, err := s.projectsService.UpdateProject(
		c.Context(),
		orgSlug,
		projectSlug,
		model.UpdateProject{Name: req.Name},
		s.eventActor(c),
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(projectToHTTP(*project))
}

func (s *server) DeleteProject(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
	params api.DeleteProjectParams,
) error {
	orgSlug, projectSlug, err := parseProjectPath(slugParam, projectSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Delete, model.ResourceProjects); err != nil {
		return err
	}
	if params.CleanupRemote != nil && *params.CleanupRemote {
		if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "pipeline.git", false); err != nil {
			return err
		}
	}

	if err := s.projectsService.DeleteProject(
		c.Context(),
		orgSlug,
		projectSlug,
		model.DeleteProjectOptions{
			CleanupRemote: params.CleanupRemote != nil && *params.CleanupRemote,
		},
		s.eventActor(c),
	); err != nil {
		return mapServiceError(err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func (s *server) SetProjectPipelineBackend(
	c fiber.Ctx,
	slugParam api.SlugParam,
	projectSlugParam api.ProjectSlugParam,
) error {
	orgSlug, projectSlug, err := parseProjectPath(slugParam, projectSlugParam)
	if err != nil {
		return err
	}

	if err := s.canOrg(c, orgSlug, model.Update, model.ResourceProjects); err != nil {
		return err
	}

	var req api.SetProjectPipelineBackendRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}

	dataSourceSlug, err := slug.New(req.DataSourceSlug)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}
	mode, err := model.SetProjectPipelineBackendModeString(string(req.Mode))
	if err != nil {
		slog.LogAttrs(
			c.Context(),
			slog.LevelWarn,
			"invalid project pipeline backend mode",
			slog.String("mode", string(req.Mode)),
			slog.Any("error", err),
		)
		return apierror.ValidationFailed("Invalid pipeline backend mode")
	}
	if mode == model.SetProjectPipelineBackendModeUnknown {
		slog.LogAttrs(
			c.Context(),
			slog.LevelWarn,
			"reserved project pipeline backend mode rejected",
			slog.String("mode", string(req.Mode)),
		)
		return apierror.ValidationFailed("Invalid pipeline backend mode")
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "pipeline.reconcile", false); err != nil {
		return err
	}

	project, err := s.projectsService.SetProjectPipelineBackend(
		c.Context(),
		orgSlug,
		projectSlug,
		model.SetProjectPipelineBackend{
			DataSourceSlug: dataSourceSlug,
			TrackedBranch:  strings.TrimSpace(req.TrackedBranch),
			PipelineDir:    strings.TrimSpace(req.PipelineDir),
			ProviderOwner:  strings.TrimSpace(stringValue(req.ProviderOwner)),
			RepositoryName: strings.TrimSpace(stringValue(req.RepositoryName)),
			Mode:           mode,
		},
	)
	if err != nil {
		return mapServiceError(err)
	}

	return c.JSON(projectToHTTP(*project))
}

func projectToHTTP(project model.Project) api.Project {
	return api.Project{
		Slug:            project.Slug.String(),
		Name:            project.Name,
		PipelineBackend: projectPipelineBackendToHTTP(project.PipelineBackend),
		CreatedAt:       project.CreatedAt,
		UpdatedAt:       project.UpdatedAt,
	}
}

func projectPipelineBackendToHTTP(
	value *model.ProjectPipelineBackend,
) *api.ProjectPipelineBackend {
	if value == nil {
		return nil
	}

	return &api.ProjectPipelineBackend{
		DataSourceSlug: value.DataSourceSlug.String(),
		Repo:           value.Repo,
		TrackedBranch:  value.TrackedBranch,
		PipelineDir:    value.PipelineDir,
		FirstCommitSha: value.FirstCommitSHA,
	}
}

func parseProjectPath(slugParam api.SlugParam, projectSlugParam api.ProjectSlugParam) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}

	projectSlug, err := slug.New(projectSlugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid project slug: %v", err))
	}

	return orgSlug, projectSlug, nil
}
