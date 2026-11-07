package http

import (
	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/gofiber/fiber/v3"
)

func organizationGitLabOwner(org slug.Slug) credentialssvc.CredentialOwner {
	return credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindOrganizationGitLab, OrgSlug: org}
}

func (s *server) GetOrganizationGitLabConnection(c fiber.Ctx, orgParam api.SlugParam) error {
	org, err := githubAppOrg(c, s, orgParam, model.Read)
	if err != nil {
		return err
	}
	connection, err := s.credentialsService.GetOrganizationGitLabConnection(c.Context(), org)
	if xerror.Is(err, credentialssvc.ErrCredentialNotFound) {
		return c.SendStatus(fiber.StatusNoContent)
	}
	if err != nil {
		return mapServiceError(err)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(credentialMetadataToHTTP(connection.Credential))
}

func (s *server) StartOrganizationGitLabAuthorization(c fiber.Ctx, orgParam api.SlugParam) error {
	org, err := githubAppOrg(c, s, orgParam, model.Update)
	if err != nil {
		return err
	}
	var request api.StartOrganizationGitLabAuthorizationJSONBody
	if err := c.Bind().Body(&request); err != nil {
		return fiber.ErrBadRequest
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "credential.mutation", true); err != nil {
		return err
	}
	connection, err := s.credentialsService.CreateOrganizationGitLabConnection(c.Context(), credentialssvc.CreateOrganizationGitLabConnectionParams{OrganizationSlug: org, Provider: "gitlab"})
	if err != nil {
		return mapServiceError(err)
	}
	intent := credentialssvc.OAuthInstallIntentRotate
	if connection.Credential.Status == credentialssvc.CredentialStatusPending {
		intent = credentialssvc.OAuthInstallIntentCreate
	}
	install, err := s.credentialsService.StartOAuthInstall(c.Context(), credentialssvc.StartOAuthInstallParams{Owner: organizationGitLabOwner(org), Provider: "gitlab", Intent: intent, IncludeWriteScopes: request.IncludeWriteScopes, Actor: s.eventActor(c)})
	if err != nil {
		return mapServiceError(err)
	}
	return c.Status(fiber.StatusCreated).JSON(providerInstallToHTTP(install))
}

func (s *server) DisconnectOrganizationGitLabConnection(c fiber.Ctx, orgParam api.SlugParam) error {
	org, err := githubAppOrg(c, s, orgParam, model.Update)
	if err != nil {
		return err
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "credential.mutation", true); err != nil {
		return err
	}
	metadata, err := s.credentialsService.Revoke(c.Context(), credentialssvc.RevokeParams{Owner: organizationGitLabOwner(org), Actor: s.eventActor(c)})
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(credentialMetadataToHTTP(metadata))
}

func (s *server) ListOrganizationGitLabRepositories(c fiber.Ctx, orgParam api.SlugParam) error {
	org, err := slug.New(orgParam)
	if err != nil {
		return fiber.ErrBadRequest
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	repositories, err := s.credentialsService.ListOrganizationGitLabRepositories(c.Context(), org)
	if err != nil {
		return mapServiceError(err)
	}
	response := api.ProviderRepositoryListResponse{Repositories: make([]api.ProviderRepository, 0, len(repositories))}
	for _, repository := range repositories {
		response.Repositories = append(response.Repositories, api.ProviderRepository{Id: repository.ID, Name: repository.Name, FullName: repository.FullName, WebUrl: repository.WebURL, DefaultBranch: optionalMetadataString(repository.DefaultBranch)})
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(response)
}

func (s *server) AttachDataSourceOrganizationGitLabConnection(c fiber.Ctx, orgParam api.SlugParam, dsParam api.DataSourceSlugParam) error {
	org, ds, err := credentialOwnerSlugs(orgParam, dsParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourceDataSources); err != nil {
		return err
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "credential.mutation", true); err != nil {
		return err
	}
	if err := s.datasourcesService.AttachOrganizationGitLabConnection(c.Context(), org, ds, s.eventActor(c)); err != nil {
		return mapServiceError(err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}
