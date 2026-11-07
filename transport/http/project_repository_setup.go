package http

import (
	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (s *server) GetProjectRepositorySetup(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam) error {
	org, project, err := parseProjectPath(orgParam, projectParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceProjects); err != nil {
		return err
	}
	setup, err := s.repositorySetupService.Get(c.Context(), org, project)
	if err != nil {
		return mapServiceError(err)
	}
	if setup == nil {
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.JSON(projectRepositorySetupToHTTP(setup))
}

func (s *server) CreateProjectRepository(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam) error {
	org, project, err := parseProjectPath(orgParam, projectParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourceOrganizations); err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourceProjects); err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Create, model.ResourceDataSources); err != nil {
		return err
	}
	var req api.CreateProjectRepositoryRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	provider, err := model.DataSourceTypeString(string(req.Provider))
	if err != nil {
		return apierror.ValidationFailed("Invalid repository provider")
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "pipeline.reconcile", false); err != nil {
		return err
	}
	setup, err := s.repositorySetupService.Create(c.Context(), org, project, model.CreateProjectRepository{
		Provider: provider, Owner: req.Owner, RepositoryName: req.RepositoryName,
		TrackedBranch: stringValue(req.TrackedBranch), PipelineDir: stringValue(req.PipelineDir),
		CreatePrivateRepository: bool(req.CreatePrivateRepository),
	}, s.eventActor(c))
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(projectRepositorySetupToHTTP(setup))
}

func (s *server) GetProjectRepositorySetupAttempt(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam, setupParam api.Slug) error {
	org, project, err := parseProjectPath(orgParam, projectParam)
	if err != nil {
		return err
	}
	setup, err := slug.New(setupParam)
	if err != nil {
		return apierror.ValidationFailed("Invalid repository setup slug")
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceProjects); err != nil {
		return err
	}
	result, err := s.repositorySetupService.GetAttempt(c.Context(), org, project, setup)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(projectRepositorySetupToHTTP(result))
}

func (s *server) AbandonProjectRepositorySetup(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam) error {
	org, project, err := parseProjectPath(orgParam, projectParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourceOrganizations); err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourceProjects); err != nil {
		return err
	}
	var req api.AbandonProjectRepositorySetupRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	setup, err := slug.New(req.SetupSlug)
	if err != nil {
		return apierror.ValidationFailed("Invalid repository setup slug")
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "pipeline.reconcile", false); err != nil {
		return err
	}
	result, err := s.repositorySetupService.Abandon(c.Context(), org, project, setup, bool(req.PreserveResources), s.eventActor(c))
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(projectRepositorySetupToHTTP(result))
}

func (s *server) CompleteProjectRepositoryGitHubAuthorization(c fiber.Ctx, orgParam api.SlugParam, projectParam api.ProjectSlugParam) error {
	org, project, err := parseProjectPath(orgParam, projectParam)
	if err != nil {
		return err
	}
	if _, err := s.canOrgUserPrincipal(c, org, model.Update, model.ResourceOrganizations); err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourceProjects); err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Create, model.ResourceDataSources); err != nil {
		return err
	}
	var req api.ProjectRepositoryGitHubAuthorization
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	setup, err := s.repositorySetupService.Get(c.Context(), org, project)
	if err != nil {
		return mapServiceError(err)
	}
	if setup == nil {
		return apierror.ValidationFailed("Start repository setup before authorizing GitHub")
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "pipeline.reconcile", false); err != nil {
		return err
	}
	completed, err := s.repositorySetupService.CompleteGitHub(c.Context(), org, setup.Slug, s.eventActor(c), model.GitHubRepositoryAuthorization{State: req.State, Code: req.Code, InstallationID: req.InstallationId})
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(projectRepositorySetupToHTTP(completed))
}

func projectRepositorySetupToHTTP(setup *model.ProjectRepositorySetup) api.ProjectRepositorySetup {
	result := api.ProjectRepositorySetup{
		Slug: setup.Slug.String(), ProjectSlug: setup.ProjectSlug.String(), DataSourceSlug: setup.DataSourceSlug.String(),
		Status: api.ProjectRepositorySetupStatus(setup.Status.String()),
		Request: api.CreateProjectRepositoryRequest{
			Provider: api.CreateProjectRepositoryRequestProvider(setup.Request.Provider.String()),
			Owner:    setup.Request.Owner, RepositoryName: setup.Request.RepositoryName,
			TrackedBranch: ptr.Ptr(setup.Request.TrackedBranch), PipelineDir: ptr.Ptr(setup.Request.PipelineDir),
			CreatePrivateRepository: api.CreateProjectRepositoryRequestCreatePrivateRepository(setup.Request.CreatePrivateRepository),
		},
	}
	if setup.AuthorizationURL != "" {
		result.AuthorizationUrl = ptr.Ptr(setup.AuthorizationURL)
	}
	if setup.RepositoryID != "" {
		result.RepositoryId = ptr.Ptr(setup.RepositoryID)
	}
	if setup.RepositoryURL != "" {
		result.RepositoryUrl = ptr.Ptr(setup.RepositoryURL)
	}
	if setup.FirstCommitSHA != "" {
		result.FirstCommitSha = ptr.Ptr(setup.FirstCommitSHA)
	}
	if setup.FailureCode != "" {
		result.FailureCode = ptr.Ptr(setup.FailureCode)
	}
	result.AbandonedAt = setup.AbandonedAt
	return result
}
