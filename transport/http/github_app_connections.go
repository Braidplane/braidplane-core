package http

import (
	"fmt"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/githubappsvc"
)

func (s *server) GetOrganizationGitHubAppConnection(c fiber.Ctx, slugParam api.SlugParam) error {
	if s.githubAppService == nil {
		return fiber.ErrNotFound
	}
	org, err := githubAppOrg(c, s, slugParam, model.Read)
	if err != nil {
		return err
	}
	connection, err := s.githubAppService.Get(c.Context(), org)
	if err != nil {
		return mapServiceError(err)
	}
	if connection == nil {
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.JSON(githubAppConnectionToHTTP(connection))
}

func (s *server) StartOrganizationGitHubAppInstall(c fiber.Ctx, slugParam api.SlugParam) error {
	if s.githubAppService == nil {
		return fiber.ErrNotFound
	}
	org, err := githubAppOrg(c, s, slugParam, model.Update)
	if err != nil {
		return err
	}
	authorizationURL, err := s.githubAppService.Start(c.Context(), org, s.eventActor(c))
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(api.GitHubAppInstallStart{AuthorizationUrl: authorizationURL})
}

func (s *server) AuthorizeOrganizationGitHubAppConnection(c fiber.Ctx, slugParam api.SlugParam) error {
	if s.githubAppService == nil {
		return fiber.ErrNotFound
	}
	org, err := githubAppOrg(c, s, slugParam, model.Update)
	if err != nil {
		return err
	}
	var request api.GitHubAppExistingInstallationAuthorizationRequest
	if err := c.Bind().Body(&request); err != nil {
		return apierror.InvalidRequestBody()
	}
	authorizationURL, err := s.githubAppService.StartExistingAuthorization(c.Context(), org, s.eventActor(c), request.AccountLogin)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(api.GitHubAppInstallStart{AuthorizationUrl: authorizationURL})
}

func (s *server) CompleteOrganizationGitHubAppInstall(c fiber.Ctx, params api.CompleteOrganizationGitHubAppInstallParams) error {
	if s.githubAppService == nil {
		return fiber.ErrNotFound
	}
	actor := s.eventActor(c)
	org, setupSlug, err := s.githubAppService.RepositorySetupForState(c.Context(), actor, params.State)
	if err != nil {
		return mapServiceError(err)
	}
	if err := s.canOrgUserSession(c, org, model.Update, model.ResourceOrganizations); err != nil {
		return err
	}
	if params.Error == nil && (params.Code == nil || *params.Code == "" || params.InstallationId != nil && *params.InstallationId <= 0 || setupSlug != "" && params.InstallationId == nil) {
		return apierror.ValidationFailed("GitHub authorization code is required; installation ID must be positive when supplied and is required for repository setup")
	}
	if params.Error != nil && (params.Code != nil || params.InstallationId != nil) {
		return apierror.ValidationFailed("GitHub callback cannot contain both authorization and error results")
	}
	if setupSlug != "" {
		if err := s.canOrg(c, org, model.Update, model.ResourceProjects); err != nil {
			return err
		}
		if err := s.canOrg(c, org, model.Create, model.ResourceDataSources); err != nil {
			return err
		}
		if params.Error != nil {
			setup, err := s.repositorySetupService.RejectGitHubAuthorization(c.Context(), org, setupSlug, actor, params.State, string(*params.Error))
			if err != nil {
				return mapServiceError(err)
			}
			c.Set(fiber.HeaderLocation, fmt.Sprintf("/organizations/%s/projects/%s?section=repository&repository_path=create&repository_setup=failed", org, setup.ProjectSlug))
			return apierror.ValidationFailed("GitHub authorization failed; setup progress was saved so you can authorize again")
		}
		if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "pipeline.reconcile", false); err != nil {
			return err
		}
		setup, err := s.repositorySetupService.CompleteGitHub(c.Context(), org, setupSlug, actor, model.GitHubRepositoryAuthorization{State: params.State, Code: *params.Code, InstallationID: *params.InstallationId})
		if err != nil {
			return mapServiceError(err)
		}
		connection, err := s.githubAppService.Get(c.Context(), org)
		if err != nil {
			return mapServiceError(err)
		}
		if connection == nil {
			return apierror.ValidationFailed("GitHub installation binding is unavailable")
		}
		response := githubAppConnectionToHTTP(connection)
		result := projectRepositorySetupToHTTP(setup)
		response.RepositorySetup = &result
		return c.JSON(response)
	}
	if params.Error != nil {
		if err := s.githubAppService.ConsumeRejectedInstall(c.Context(), org, "", actor, params.State); err != nil {
			return mapServiceError(err)
		}
		return apierror.ValidationFailed("GitHub authorization failed; start authorization again")
	}
	installationID := int64(0)
	if params.InstallationId != nil {
		installationID = *params.InstallationId
	}
	connection, err := s.githubAppService.Complete(c.Context(), actor, params.State, *params.Code, installationID)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(githubAppConnectionToHTTP(connection))
}

func (s *server) DisconnectOrganizationGitHubAppConnection(c fiber.Ctx, slugParam api.SlugParam) error {
	if s.githubAppService == nil {
		return fiber.ErrNotFound
	}
	org, err := githubAppOrg(c, s, slugParam, model.Update)
	if err != nil {
		return err
	}
	if err := s.githubAppService.Disconnect(c.Context(), org, s.eventActor(c)); err != nil {
		return mapServiceError(err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func githubAppOrg(c fiber.Ctx, s *server, value api.SlugParam, action model.Action) (slug.Slug, error) {
	org, err := slug.New(value)
	if err != nil {
		return "", apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}
	if err := s.canOrgUserSession(c, org, action, model.ResourceOrganizations); err != nil {
		return "", err
	}
	return org, nil
}

func githubAppConnectionToHTTP(connection *githubappsvc.Connection) api.GitHubAppConnection {
	repos := make([]api.GitHubAppRepository, len(connection.Repositories))
	for i, repo := range connection.Repositories {
		repos[i] = api.GitHubAppRepository{Id: repo.ID, FullName: repo.FullName, OwnerLogin: repo.OwnerLogin, Name: repo.Name}
	}
	return api.GitHubAppConnection{
		OrganizationSlug: connection.Organization.String(),
		InstallationId:   connection.InstallationID, AccountId: connection.AccountID,
		AccountLogin: connection.AccountLogin, AccountType: connection.AccountType,
		RepositorySelection: api.GitHubAppConnectionRepositorySelection(connection.RepositorySelection),
		Repositories:        repos, SuspendedAt: connection.SuspendedAt, DeletedAt: connection.DeletedAt,
	}
}
