package http

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/gofiber/fiber/v3"
	openapi_types "github.com/oapi-codegen/runtime/types"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

// StartProviderDataSourceInstall creates a pending provider-backed source and returns its authorization URL.
func (s *server) StartProviderDataSourceInstall(
	c fiber.Ctx,
	slugParam api.SlugParam,
	provider api.ProviderKey,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}
	if err := s.canOrg(c, orgSlug, model.Create, model.ResourceDataSources); err != nil {
		return err
	}
	if err := s.admissionService.CheckDataSourceAdmission(c.Context(), orgSlug); err != nil {
		return mapServiceError(err)
	}

	var req api.StartProviderDataSourceInstallRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.InvalidRequestBody()
	}
	dsType, err := model.DataSourceTypeString(string(req.Type))
	if err != nil || provider != string(req.Type) || (dsType != model.DataSourceTypeHubSpot && dsType != model.DataSourceTypeGitlab && dsType != model.DataSourceTypeBigQuery && dsType != model.DataSourceTypeGoogleWorkspace) {
		return apierror.ValidationFailed("Provider and data source type must both be hubspot, gitlab, bigquery, or google_workspace")
	}

	var dsSlug slug.Slug
	if req.Slug != nil {
		dsSlug, err = slug.New(*req.Slug)
		if err != nil {
			return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
		}
	}
	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source config: %v", err))
	}
	result, err := s.datasourcesService.StartProviderDataSourceInstall(
		c.Context(),
		orgSlug,
		s.eventActor(c),
		provider,
		&datasourcessvc.StartProviderDataSourceInstallParams{
			Name:       req.Name,
			Slug:       dsSlug,
			Type:       dsType,
			ConfigJSON: configJSON,
			ReadOnly:   req.ReadOnly,
		},
	)
	if err != nil {
		return mapServiceError(err)
	}
	return c.Status(http.StatusCreated).JSON(api.ProviderDataSourceInstall{
		DataSource: dataSourceToHTTP(result.DataSource),
		Install:    providerInstallToHTTP(result.Install),
	})
}

// StartProviderConnectorInstall creates a single-use OAuth install without returning credential values.
func (s *server) StartProviderConnectorInstall(
	c fiber.Ctx,
	slugParam api.SlugParam,
	provider api.ProviderKey,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}
	var req api.StartProviderConnectorInstallRequest
	if err := c.Bind().Body(&req); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid provider install request: %v", err))
	}
	requestedIntent, err := credentialssvc.OAuthInstallIntentString(string(req.Intent))
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid provider install intent: %v", err))
	}
	if err := s.canOrg(c, orgSlug, oauthInstallAction(provider, requestedIntent), model.ResourceDataSources); err != nil {
		return err
	}
	owner, err := providerInstallOwner(orgSlug, req)
	if err != nil {
		return err
	}
	dataSource, err := s.datasourcesService.FindBySlug(c.Context(), orgSlug, owner.DataSourceSlug)
	if err != nil {
		return mapServiceError(err)
	}
	intent := oauthInstallIntentForDataSource(dataSource)
	if err := s.canOrg(c, orgSlug, oauthInstallAction(provider, intent), model.ResourceDataSources); err != nil {
		return err
	}
	if provider != dataSource.Type.String() {
		return apierror.ValidationFailed("Provider must match the data source type")
	}
	accessMode := ""
	if req.AccessMode != nil {
		accessMode = string(*req.AccessMode)
	}
	if dataSource.Type == model.DataSourceTypeGoogleWorkspace {
		if accessMode != "" && accessMode != workspace.SelectedFiles {
			return apierror.ValidationFailed("Workspace access_mode must be selected_files")
		}
		accessMode = workspace.SelectedFiles
	}
	install, err := s.credentialsService.StartOAuthInstall(
		c.Context(),
		credentialssvc.StartOAuthInstallParams{
			Owner:              owner,
			Provider:           provider,
			IncludeWriteScopes: !dataSource.ReadOnly,
			AccessMode:         accessMode,
			Intent:             intent,
			Actor:              s.eventActor(c),
		},
	)
	if err != nil {
		return mapServiceError(err)
	}
	return c.Status(http.StatusCreated).JSON(providerInstallToHTTP(install))
}

// HandleProviderConnectorCallback consumes provider callbacks without exposing provider payloads.
func (s *server) HandleProviderConnectorCallback(
	c fiber.Ctx,
	provider api.ProviderKey,
	params api.HandleProviderConnectorCallbackParams,
) error {
	if params.State == nil {
		return s.redirectProviderOAuthFailure(c, "")
	}
	install, err := s.credentialsService.FindOAuthInstall(
		c.Context(),
		credentialssvc.FindOAuthInstallParams{
			Provider: provider,
			State:    *params.State,
		},
	)
	if install.RepositorySetupSlug != "" || install.RepositorySetupProjectSlug != "" {
		return s.handleRepositoryGitLabCallback(c, provider, params, install, err)
	}
	if err != nil {
		if install.Owner.OrgSlug != "" &&
			(xerror.Is(err, credentialssvc.ErrOAuthStateExpired) ||
				xerror.Is(err, credentialssvc.ErrOAuthStateConsumed)) {
			action, resource := oauthCallbackPermission(install.Provider, install.Owner, install.Intent)
			if _, authErr := s.canOrgUserPrincipal(
				c,
				install.Owner.OrgSlug,
				action,
				resource,
			); authErr != nil {
				return authErr
			}
			status := "error"
			if xerror.Is(err, credentialssvc.ErrOAuthStateExpired) {
				status = "expired"
			}
			return s.redirectProviderOAuthResult(c, install.Owner, status)
		}
		return s.redirectProviderOAuthFailure(c, "")
	}
	action, resource := oauthCallbackPermission(install.Provider, install.Owner, install.Intent)
	userSlug, err := s.canOrgUserPrincipal(
		c,
		install.Owner.OrgSlug,
		action,
		resource,
	)
	if err != nil {
		return err
	}
	if params.Error != nil {
		if _, err := s.credentialsService.ConsumeOAuthInstall(
			c.Context(),
			credentialssvc.ConsumeOAuthInstallParams{Provider: provider, State: *params.State},
		); err != nil {
			return s.redirectProviderOAuthResult(c, install.Owner, "error")
		}
		return s.redirectProviderOAuthResult(c, install.Owner, "denied")
	}
	if params.Code == nil {
		if _, err := s.credentialsService.ConsumeOAuthInstall(
			c.Context(),
			credentialssvc.ConsumeOAuthInstallParams{Provider: provider, State: *params.State},
		); err != nil {
			return s.redirectProviderOAuthResult(c, install.Owner, "error")
		}
		return s.redirectProviderOAuthResult(c, install.Owner, "error")
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, install.Owner.OrgSlug.String(), "credential.mutation", true); err != nil {
		return s.redirectProviderOAuthResult(c, install.Owner, "error")
	}
	complete := s.datasourcesService.CompleteProviderOAuthCallback
	if install.Owner.Kind == credentialssvc.CredentialOwnerKindOrganizationGitLab || install.Owner.Kind == credentialssvc.CredentialOwnerKindNotificationConnection {
		complete = s.credentialsService.CompleteOAuthCallback
	}
	issuer := ""
	if params.Iss != nil {
		issuer = *params.Iss
	}
	result, err := complete(
		c.Context(),
		credentialssvc.CompleteOAuthCallbackParams{
			Provider: provider,
			State:    *params.State,
			Code:     *params.Code,
			Issuer:   issuer,
			Actor:    model.UserEventActor(userSlug),
		},
	)
	if err != nil {
		return s.redirectProviderOAuthResult(c, install.Owner, "error")
	}
	status := "success"
	if provider == "gitlab" && result.Owner.Kind != credentialssvc.CredentialOwnerKindOrganizationGitLab && result.Intent == credentialssvc.OAuthInstallIntentCreate {
		status = "select_repository"
	}
	return s.redirectProviderOAuthResult(c, result.Owner, status)
}

func (s *server) handleRepositoryGitLabCallback(c fiber.Ctx, provider api.ProviderKey, params api.HandleProviderConnectorCallbackParams, install credentialssvc.CredentialInstall, installErr error) error {
	if provider != "gitlab" || install.Provider != "gitlab" || install.Owner.Kind != credentialssvc.CredentialOwnerKindOrganizationGitLab || install.RepositorySetupSlug == "" || install.RepositorySetupProjectSlug == "" {
		return s.redirectProviderOAuthFailure(c, "")
	}
	org, project, setup := install.Owner.OrgSlug, install.RepositorySetupProjectSlug, install.RepositorySetupSlug
	user, err := s.canOrgUserPrincipal(c, org, model.Update, model.ResourceOrganizations)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourceProjects); err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Create, model.ResourceDataSources); err != nil {
		return err
	}
	if user != install.Actor.UserSlugValue() {
		return mapServiceError(credentialssvc.ErrOAuthStateInvalid)
	}
	redirect := func(status string) error {
		query := url.Values{"section": {"repository"}, "gitlab_setup": {status}}
		if status != "success" {
			query.Set("repository_path", "create")
		}
		return c.Redirect().Status(http.StatusFound).To(fmt.Sprintf("%s/organizations/%s/projects/%s?%s", s.frontendURL, org, project, query.Encode()))
	}
	if installErr != nil {
		if xerror.Is(installErr, credentialssvc.ErrOAuthStateExpired) {
			return redirect("expired")
		}
		return redirect("error")
	}
	current, err := s.repositorySetupService.GetAttempt(c.Context(), org, project, setup)
	if err != nil || current.AbandonedAt != nil || current.Status == model.ProjectRepositorySetupStatusAttached || current.Request.Provider != model.DataSourceTypeGitlab {
		return redirect("stale")
	}
	if params.Error != nil || params.Code == nil {
		providerError := ""
		if params.Error != nil {
			providerError = *params.Error
		}
		if _, err := s.repositorySetupService.RejectGitLabAuthorization(c.Context(), org, project, setup, model.UserEventActor(user), *params.State, providerError); err != nil {
			return redirect("error")
		}
		if params.Error != nil {
			return redirect("denied")
		}
		return redirect("error")
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "credential.mutation", true); err != nil {
		return redirect("error")
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "pipeline.reconcile", false); err != nil {
		return redirect("error")
	}
	issuer := ""
	if params.Iss != nil {
		issuer = *params.Iss
	}
	completed, err := s.repositorySetupService.CompleteGitLab(c.Context(), org, project, setup, model.UserEventActor(user), model.GitLabRepositoryAuthorization{State: *params.State, Code: *params.Code, Issuer: issuer})
	if err != nil || completed.Status != model.ProjectRepositorySetupStatusAttached {
		return redirect("error")
	}
	return redirect("success")
}

// RefreshDataSourceProviderGrant forces a provider grant refresh and returns safe metadata.
func (s *server) RefreshDataSourceProviderGrant(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	if err := s.canManageDataSourceGrant(c, slugParam, dsSlugParam); err != nil {
		return err
	}
	orgSlug, dsSlug, err := credentialOwnerSlugs(slugParam, dsSlugParam)
	if err != nil {
		return err
	}
	if _, err := s.datasourcesService.FindBySlug(c.Context(), orgSlug, dsSlug); err != nil {
		return mapServiceError(err)
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "credential.mutation", true); err != nil {
		return err
	}
	metadata, err := s.credentialsService.Refresh(
		c.Context(),
		credentialssvc.RefreshGrantParams{
			Owner: dataSourceCredentialOwner(orgSlug, dsSlug),
			Actor: s.eventActor(c),
		},
	)
	if err != nil {
		return mapServiceError(err)
	}
	if err := s.datasourcesService.RecordCredentialLifecycleEvent(
		c.Context(),
		orgSlug,
		dsSlug,
		s.eventActor(c),
		model.EventActionRefresh,
	); err != nil {
		slog.WarnContext(c.Context(), "failed to record provider grant refresh event", "error", err)
	}
	return c.JSON(credentialMetadataToHTTP(metadata))
}

// RevokeDataSourceProviderGrant revokes a provider grant and returns safe metadata.
func (s *server) RevokeDataSourceProviderGrant(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	if err := s.canManageDataSourceGrant(c, slugParam, dsSlugParam); err != nil {
		return err
	}
	orgSlug, dsSlug, err := credentialOwnerSlugs(slugParam, dsSlugParam)
	if err != nil {
		return err
	}
	if _, err := s.datasourcesService.FindBySlug(c.Context(), orgSlug, dsSlug); err != nil {
		return mapServiceError(err)
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, orgSlug.String(), "credential.mutation", true); err != nil {
		return err
	}
	metadata, err := s.credentialsService.Revoke(
		c.Context(),
		credentialssvc.RevokeParams{
			Owner: dataSourceCredentialOwner(orgSlug, dsSlug),
			Actor: s.eventActor(c),
		},
	)
	if err != nil {
		return mapServiceError(err)
	}
	if err := s.datasourcesService.RecordCredentialLifecycleEvent(
		c.Context(),
		orgSlug,
		dsSlug,
		s.eventActor(c),
		model.EventActionRevoke,
	); err != nil {
		slog.WarnContext(c.Context(), "failed to record provider grant revoke event", "error", err)
	}
	return c.JSON(credentialMetadataToHTTP(metadata))
}

// RotateDataSourceProviderGrant starts replacement grant authorization without exposing credentials.
func (s *server) RotateDataSourceProviderGrant(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
	params api.RotateDataSourceProviderGrantParams,
) error {
	if err := s.canManageDataSourceGrant(c, slugParam, dsSlugParam); err != nil {
		return err
	}
	orgSlug, dsSlug, err := credentialOwnerSlugs(slugParam, dsSlugParam)
	if err != nil {
		return err
	}
	dataSource, err := s.datasourcesService.FindBySlug(c.Context(), orgSlug, dsSlug)
	if err != nil {
		return mapServiceError(err)
	}
	accessMode := ""
	if params.AccessMode != nil {
		accessMode = string(*params.AccessMode)
	}
	if dataSource.Type == model.DataSourceTypeGoogleWorkspace {
		if accessMode != "" && accessMode != workspace.SelectedFiles {
			return apierror.ValidationFailed("Workspace access_mode must be selected_files")
		}
		accessMode = workspace.SelectedFiles
	}
	install, err := s.credentialsService.Rotate(
		c.Context(),
		credentialssvc.RotateParams{
			Owner:              dataSourceCredentialOwner(orgSlug, dsSlug),
			Actor:              s.eventActor(c),
			IncludeWriteScopes: !dataSource.ReadOnly,
			AccessMode:         accessMode,
		},
	)
	if err != nil {
		return mapServiceError(err)
	}
	return c.Status(http.StatusCreated).JSON(providerInstallToHTTP(install))
}

func (s *server) canManageDataSourceGrant(
	c fiber.Ctx,
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) error {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}
	if _, err := slug.New(dsSlugParam); err != nil {
		return apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}
	return s.canOrg(c, orgSlug, model.Update, model.ResourceDataSources)
}

func (s *server) redirectProviderOAuthFailure(c fiber.Ctx, orgSlug slug.Slug) error {
	if orgSlug != "" {
		return c.Redirect().Status(http.StatusFound).To(fmt.Sprintf("%s/orgs/%s/data-sources?provider_oauth=error", s.frontendURL, orgSlug))
	}
	return c.Redirect().Status(http.StatusFound).To(fmt.Sprintf("%s/data-sources?provider_oauth=error", s.frontendURL))
}

// redirectProviderOAuthResult returns an organization-scoped safe result without echoing provider callback details.
func (s *server) redirectProviderOAuthResult(
	c fiber.Ctx,
	owner credentialssvc.CredentialOwner,
	status string,
) error {
	query := url.Values{"provider_oauth": {status}}
	if owner.Kind == credentialssvc.CredentialOwnerKindNotificationConnection {
		return c.Redirect().Status(http.StatusFound).To(fmt.Sprintf("%s/orgs/%s/alerts/connections?%s", s.frontendURL, owner.OrgSlug, query.Encode()))
	}
	if owner.Kind == credentialssvc.CredentialOwnerKindOrganizationGitLab {
		return c.Redirect().Status(http.StatusFound).To(fmt.Sprintf("%s/organizations/%s/connections?%s", s.frontendURL, owner.OrgSlug, query.Encode()))
	}
	if owner.DataSourceSlug != "" {
		query.Set("selected", owner.DataSourceSlug.String())
	}
	return c.Redirect().Status(http.StatusFound).To(fmt.Sprintf("%s/orgs/%s/data-sources?%s", s.frontendURL, owner.OrgSlug, query.Encode()))
}

func providerInstallOwner(
	orgSlug slug.Slug,
	req api.StartProviderConnectorInstallRequest,
) (credentialssvc.CredentialOwner, error) {
	rawSlug := req.DataSourceSlug
	if rawSlug == "" {
		return credentialssvc.CredentialOwner{}, apierror.ValidationFailed("Provider install requires data_source_slug")
	}
	dsSlug, err := slug.New(rawSlug)
	if err != nil {
		return credentialssvc.CredentialOwner{}, apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}
	return dataSourceCredentialOwner(orgSlug, dsSlug), nil
}

func credentialOwnerSlugs(
	slugParam api.SlugParam,
	dsSlugParam api.DataSourceSlugParam,
) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(slugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid organization slug: %v", err))
	}
	dsSlug, err := slug.New(dsSlugParam)
	if err != nil {
		return "", "", apierror.ValidationFailed(fmt.Sprintf("Invalid data source slug: %v", err))
	}
	return orgSlug, dsSlug, nil
}

func dataSourceCredentialOwner(orgSlug, dsSlug slug.Slug) credentialssvc.CredentialOwner {
	return credentialssvc.CredentialOwner{
		OrgSlug:        orgSlug,
		DataSourceSlug: dsSlug,
	}
}

func providerInstallToHTTP(install credentialssvc.ProviderConnectorInstall) api.ProviderConnectorInstall {
	return api.ProviderConnectorInstall{
		Slug:             install.Slug.String(),
		Provider:         install.Provider,
		AuthorizationUrl: install.AuthorizationURL,
		ExpiresAt:        install.ExpiresAt,
		Intent:           api.ProviderInstallIntent(install.Intent.String()),
		RequestedScopes:  append([]string(nil), install.RequestedScopes...),
	}
}

func credentialMetadataToHTTP(metadata credentialssvc.CredentialMetadata) api.CredentialMetadata {
	return api.CredentialMetadata{
		Kind:      api.CredentialKind(metadata.Kind.String()),
		Status:    api.CredentialStatus(metadata.Status.String()),
		UpdatedAt: metadata.UpdatedAt,
		Oauth:     oauthMetadataToHTTP(metadata.OAuth),
	}
}

func oauthMetadataToHTTP(metadata credentialssvc.CredentialOAuthMetadata) *api.CredentialOAuthMetadata {
	if metadata.Provider == "" {
		return nil
	}
	return &api.CredentialOAuthMetadata{
		Provider:             metadata.Provider,
		Scopes:               append([]string{}, metadata.Scopes...),
		Account:              providerAccountToHTTP(metadata.Account),
		AccessTokenExpiresAt: timePtrOrNil(metadata.AccessTokenExpiresAt),
		LastRefreshedAt:      metadata.LastRefreshedAt,
		RevokedAt:            metadata.RevokedAt,
		FailureCode:          optionalMetadataString(metadata.FailureCode),
		FailureMessage:       optionalMetadataString(metadata.FailureMessage),
	}
}

// oauthInstallAction maps immutable install intent to the permission needed to finish that lifecycle operation.
func oauthInstallAction(provider string, intent credentialssvc.OAuthInstallIntent) model.Action {
	if provider == "http" || intent == credentialssvc.OAuthInstallIntentRotate {
		return model.Update
	}
	return model.Create
}

func oauthCallbackPermission(provider string, owner credentialssvc.CredentialOwner, intent credentialssvc.OAuthInstallIntent) (model.Action, model.Resource) {
	if owner.Kind == credentialssvc.CredentialOwnerKindNotificationConnection {
		return model.Update, model.ResourceAlertDestinations
	}
	if owner.Kind == credentialssvc.CredentialOwnerKindOrganizationGitLab {
		return model.Update, model.ResourceOrganizations
	}
	return oauthInstallAction(provider, intent), model.ResourceDataSources
}

// oauthInstallIntentForDataSource derives grant creation or rotation from trusted persisted credential state.
func oauthInstallIntentForDataSource(dataSource *datasourcessvc.DataSource) credentialssvc.OAuthInstallIntent {
	if !dataSource.IsActive && dataSource.Credential != nil &&
		dataSource.Credential.Kind == credentialssvc.CredentialKindOAuth2Grant &&
		dataSource.Credential.Status == credentialssvc.CredentialStatusPending {
		return credentialssvc.OAuthInstallIntentCreate
	}
	return credentialssvc.OAuthInstallIntentRotate
}

func providerAccountToHTTP(account credentialssvc.ProviderAccount) *api.OAuthProviderAccount {
	if account.ID == "" && account.Name == "" && account.Email == "" {
		return nil
	}
	mapped := api.OAuthProviderAccount{
		Id:    optionalMetadataString(account.ID),
		Name:  optionalMetadataString(account.Name),
		Email: optionalMetadataEmail(account.Email),
	}
	return &mapped
}

func timePtrOrNil(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func optionalMetadataString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalMetadataEmail(value string) *openapi_types.Email {
	if value == "" {
		return nil
	}
	email := openapi_types.Email(value)
	return &email
}

func workspaceFileToHTTP(f workspace.File) api.GoogleWorkspaceFile {
	return api.GoogleWorkspaceFile{FileId: f.FileID, Name: f.Name, MimeType: f.MIMEType, DriveId: &f.DriveID, CanDownload: &f.CanDownload, CanEdit: &f.CanEdit, CanAddChildren: &f.CanAddChildren}
}

func workspaceDiscoveryToHTTP(page *workspace.Discovery) api.GoogleWorkspaceFileDiscovery {
	result := api.GoogleWorkspaceFileDiscovery{Files: []api.GoogleWorkspaceFile{}}
	for _, f := range page.Files {
		result.Files = append(result.Files, workspaceFileToHTTP(f))
	}
	if page.NextPageToken != "" {
		result.NextPageToken = &page.NextPageToken
	}
	return result
}

func (s *server) ListGoogleWorkspaceFiles(c fiber.Ctx, slugParam api.SlugParam, dsSlugParam api.DataSourceSlugParam, params api.ListGoogleWorkspaceFilesParams) error {
	org, source, err := credentialOwnerSlugs(slugParam, dsSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	options := workspace.ListOptions{}
	if params.PageToken != nil {
		options.PageToken = *params.PageToken
	}
	if params.FolderId != nil {
		options.FolderID = *params.FolderId
	}
	if params.DriveId != nil {
		options.DriveID = *params.DriveId
	}
	if params.MimeType != nil {
		options.MIMEType = *params.MimeType
	}
	page, err := s.datasourcesService.ListGoogleWorkspaceFiles(c.Context(), org, source, options)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(workspaceDiscoveryToHTTP(page))
}

func (s *server) GetGoogleWorkspacePicker(c fiber.Ctx, slugParam api.SlugParam, sourceParam api.DataSourceSlugParam) error {
	org, source, err := credentialOwnerSlugs(slugParam, sourceParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourceDataSources); err != nil {
		return err
	}
	picker, err := s.datasourcesService.GetGoogleWorkspacePicker(c.Context(), org, source)
	if err != nil {
		return mapServiceError(err)
	}
	c.Set("Cache-Control", "no-store")
	return c.JSON(api.GoogleWorkspacePicker{ClientId: picker.ClientID, DeveloperKey: picker.DeveloperKey, AppId: picker.AppID, AccountEmail: picker.AccountEmail, ExpectedRevision: picker.ExpectedRevision})
}

func (s *server) VerifyGoogleWorkspaceFiles(c fiber.Ctx, slugParam api.SlugParam, sourceParam api.DataSourceSlugParam) error {
	org, source, err := credentialOwnerSlugs(slugParam, sourceParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Update, model.ResourceDataSources); err != nil {
		return err
	}
	var request api.GoogleWorkspaceVerificationRequest
	if err := c.Bind().Body(&request); err != nil {
		return apierror.InvalidRequestBody()
	}
	params := datasourcessvc.WorkspaceVerificationParams{FileIDs: request.FileIds, ExpectedRevision: request.ExpectedRevision}
	page, err := s.datasourcesService.VerifyGoogleWorkspaceFiles(c.Context(), org, source, s.eventActor(c), params)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(workspaceDiscoveryToHTTP(page))
}

func (s *server) GetGoogleWorkspaceFile(c fiber.Ctx, slugParam api.SlugParam, dsSlugParam api.DataSourceSlugParam, id string) error {
	org, source, err := credentialOwnerSlugs(slugParam, dsSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	f, err := s.datasourcesService.GetGoogleWorkspaceFile(c.Context(), org, source, id)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(workspaceFileToHTTP(*f))
}

func (s *server) GetGoogleWorkspaceWorksheets(c fiber.Ctx, slugParam api.SlugParam, dsSlugParam api.DataSourceSlugParam, id string) error {
	org, source, err := credentialOwnerSlugs(slugParam, dsSlugParam)
	if err != nil {
		return err
	}
	if err := s.canOrg(c, org, model.Read, model.ResourceDataSources); err != nil {
		return err
	}
	metadata, err := s.datasourcesService.GetGoogleWorkspaceWorksheets(c.Context(), org, source, id)
	if err != nil {
		return mapServiceError(err)
	}
	return c.JSON(metadata)
}

// ConnectDataSourceHTTPOAuth uses the saved encrypted app for consent or machine acquisition.
func (s *server) ConnectDataSourceHTTPOAuth(c fiber.Ctx, orgParam api.SlugParam, sourceParam api.DataSourceSlugParam) error {
	if err := s.canManageDataSourceGrant(c, orgParam, sourceParam); err != nil {
		return err
	}
	org, source, err := credentialOwnerSlugs(orgParam, sourceParam)
	if err != nil {
		return err
	}
	if err := chargeAuthorizedOrganizationAbuseControl(c, org.String(), "credential.mutation", true); err != nil {
		return err
	}
	result, err := s.datasourcesService.ConnectHTTPOAuth(c.Context(), org, source, s.eventActor(c))
	if err != nil {
		return mapServiceError(err)
	}
	response := api.HTTPOAuthConnection{DataSource: dataSourceToHTTP(result.DataSource)}
	if result.Install != nil {
		install := providerInstallToHTTP(*result.Install)
		response.Install = &install
	}
	return c.JSON(response)
}
