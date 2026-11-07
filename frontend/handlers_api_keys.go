package frontend

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (r *Router) handleOrganizationSecurityPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	return r.renderOrganizationSecurityPage(
		c,
		orgSlug,
		http.StatusOK,
		views.DefaultAPIKeyCreateFormData(),
		nil,
		"",
		"",
	)
}

func (r *Router) handleCreateAPIKeySubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	formData := apiKeyCreateFormDataFromRequest(c)
	customSlug, slugError := optionalAPISlug(formData.Slug, "API key")
	if slugError != "" {
		return r.renderOrganizationSecurityPage(c, orgSlug, http.StatusBadRequest, formData, nil, "API key could not be created.", slugError)
	}
	if formData.Name == "" || formData.Role == "" {
		return r.renderOrganizationSecurityPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			formData,
			nil,
			"API key could not be created.",
			"Validation: name and role are required.",
		)
	}

	if _, err := slug.New(formData.Role); err != nil {
		return r.renderOrganizationSecurityPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			formData,
			nil,
			"API key could not be created.",
			"Validation: role is invalid.",
		)
	}

	expiresAt, err := parseAPIKeyExpiresAt(formData.ExpiresAt)
	if err != nil {
		return r.renderOrganizationSecurityPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			formData,
			nil,
			"API key could not be created.",
			"Validation: expiration must be a valid date and time.",
		)
	}

	var description *string
	if formData.Description != "" {
		description = &formData.Description
	}

	payload := api.CreateAPIKeyJSONRequestBody{
		Slug:             customSlug,
		Name:             formData.Name,
		Role:             formData.Role,
		Description:      description,
		ExpiresAt:        expiresAt.value,
		CanManageApiKeys: &formData.CanManageAPIKeys,
	}

	created, status, errorResponse, createErr := r.createAPIKeyViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		payload,
	)
	if createErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		return r.renderOrganizationSecurityPage(
			c,
			orgSlug,
			status,
			formData,
			nil,
			"API key could not be created.",
			backendErrorDetails("create api key", status, errorResponse, createErr),
		)
	}

	return r.renderOrganizationSecurityPage(
		c,
		orgSlug,
		http.StatusOK,
		views.DefaultAPIKeyCreateFormData(),
		&views.APIKeyTokenNotice{
			Action: "Created",
			Name:   created.Name,
			Slug:   created.Slug,
			Token:  created.Token,
		},
		"",
		"",
	)
}

func (r *Router) handleUpdateAPIKeySubmit(c fiber.Ctx) error {
	orgSlug, apiKeySlug, err := parseOrganizationAPIKeySlugs(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return r.renderOrganizationSecurityPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.DefaultAPIKeyCreateFormData(),
			nil,
			"API key could not be updated.",
			"Validation: name is required.",
		)
	}

	descriptionValue := strings.TrimSpace(c.FormValue("description"))
	var description *string
	if descriptionValue != "" {
		description = &descriptionValue
	}

	expiresAt, err := parseAPIKeyExpiresAt(c.FormValue("expires_at"))
	if err != nil {
		return r.renderOrganizationSecurityPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.DefaultAPIKeyCreateFormData(),
			nil,
			"API key could not be updated.",
			"Validation: expiration must be a valid date and time.",
		)
	}

	payload := api.UpdateAPIKeyJSONRequestBody{
		Name:             name,
		Description:      description,
		ExpiresAt:        expiresAt.value,
		CanManageApiKeys: apiKeyManagementCapabilityFromRequest(c),
	}

	_, status, errorResponse, updateErr := r.updateAPIKeyViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		apiKeySlug.String(),
		payload,
	)
	if updateErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		return r.renderOrganizationSecurityPage(
			c,
			orgSlug,
			status,
			views.DefaultAPIKeyCreateFormData(),
			nil,
			"API key could not be updated.",
			backendErrorDetails("update api key", status, errorResponse, updateErr),
		)
	}

	return redirectHTMXOrSeeOther(c, "/organizations/"+orgSlug.String()+"/security")
}

func (r *Router) handleRotateAPIKeySubmit(c fiber.Ctx) error {
	orgSlug, apiKeySlug, err := parseOrganizationAPIKeySlugs(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	if !apiKeyConfirmationMatches(c, apiKeySlug) {
		return r.renderOrganizationSecurityPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.DefaultAPIKeyCreateFormData(),
			nil,
			"API key could not be rotated.",
			"Validation: type the API key slug to confirm rotation.",
		)
	}

	rotated, status, errorResponse, rotateErr := r.rotateAPIKeyViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		apiKeySlug.String(),
	)
	if rotateErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		return r.renderOrganizationSecurityPage(
			c,
			orgSlug,
			status,
			views.DefaultAPIKeyCreateFormData(),
			nil,
			"API key could not be rotated.",
			backendErrorDetails("rotate api key", status, errorResponse, rotateErr),
		)
	}

	return r.renderOrganizationSecurityPage(
		c,
		orgSlug,
		http.StatusOK,
		views.DefaultAPIKeyCreateFormData(),
		&views.APIKeyTokenNotice{
			Action: "Rotated",
			Name:   rotated.Name,
			Slug:   rotated.Slug,
			Token:  rotated.Token,
		},
		"",
		"",
	)
}

func (r *Router) handleRevokeAPIKeySubmit(c fiber.Ctx) error {
	orgSlug, apiKeySlug, err := parseOrganizationAPIKeySlugs(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	if !apiKeyConfirmationMatches(c, apiKeySlug) {
		return r.renderOrganizationSecurityPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.DefaultAPIKeyCreateFormData(),
			nil,
			"API key could not be revoked.",
			"Validation: type the API key slug to confirm revocation.",
		)
	}

	status, errorResponse, deleteErr := r.deleteAPIKeyViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		apiKeySlug.String(),
	)
	if deleteErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		return r.renderOrganizationSecurityPage(
			c,
			orgSlug,
			status,
			views.DefaultAPIKeyCreateFormData(),
			nil,
			"API key could not be revoked.",
			backendErrorDetails("delete api key", status, errorResponse, deleteErr),
		)
	}

	return redirectHTMXOrSeeOther(c, "/organizations/"+orgSlug.String()+"/security")
}

// handleDeleteAPIKeySubmit deletes revoked API key metadata after typed confirmation.
func (r *Router) handleDeleteAPIKeySubmit(c fiber.Ctx) error {
	orgSlug, apiKeySlug, err := parseOrganizationAPIKeySlugs(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	if !apiKeyConfirmationMatches(c, apiKeySlug) {
		return r.renderOrganizationSecurityPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.DefaultAPIKeyCreateFormData(),
			nil,
			"API key could not be removed.",
			"Validation: type the API key slug to confirm removal.",
		)
	}

	status, errorResponse, deleteErr := r.deleteRevokedAPIKeyViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		apiKeySlug.String(),
	)
	if deleteErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		return r.renderOrganizationSecurityPage(
			c,
			orgSlug,
			status,
			views.DefaultAPIKeyCreateFormData(),
			nil,
			"API key could not be removed.",
			backendErrorDetails("delete revoked api key", status, errorResponse, deleteErr),
		)
	}

	return redirectHTMXOrSeeOther(c, "/organizations/"+orgSlug.String()+"/security")
}

func (r *Router) renderOrganizationSecurityPage(
	c fiber.Ctx,
	orgSlug slug.Slug,
	statusCode int,
	createFormData views.APIKeyCreateFormData,
	tokenNotice *views.APIKeyTokenNotice,
	contentErrorSummary string,
	contentErrorDetails string,
) error {
	organization, status, errorResponse, getErr := r.getOrganizationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = fmt.Sprintf("Failed to load organization (status %d).", status)
		}

		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Organization security",
			orgSlug.String(),
			message,
			errorPopupDetails(errorResponse, ""),
			c.Get(fiber.HeaderCookie),
		)
		shellData.Breadcrumbs = []views.BreadcrumbItem{
			{Label: "Organizations", Href: "/organizations"},
			{Label: orgSlug.String(), Href: "/organizations/" + orgSlug.String()},
			{Label: "Security"},
		}

		c.Status(status)
		return renderTempl(
			c,
			views.OrganizationDetailUnavailablePage(shellData, orgSlug.String(), message),
		)
	}

	var apiKeys []api.APIKey
	hasMore := false
	nextCursor := ""
	prevCursor := ""

	params := &api.ListAPIKeysParams{}
	limit := api.LimitParam(20)
	params.Limit = &limit

	cursor := strings.TrimSpace(c.Query("cursor"))
	if cursor != "" {
		params.Cursor = &cursor
	}

	direction := strings.TrimSpace(c.Query("direction"))
	switch direction {
	case pageDirectionPrev:
		d := api.ListAPIKeysParamsDirection(api.PageDirectionParamPrev)
		params.Direction = &d
	case pageDirectionNext:
		d := api.ListAPIKeysParamsDirection(api.PageDirectionParamNext)
		params.Direction = &d
	}

	list, listStatus, listErrorResponse, listErr := r.listAPIKeysViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		params,
	)
	if listErr != nil {
		if listStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if contentErrorSummary == "" {
			if listStatus == fiber.StatusForbidden {
				contentErrorSummary = "You do not have access to this organization's API keys."
			} else {
				contentErrorSummary = "Something went wrong while loading API keys."
			}
			contentErrorDetails = backendErrorDetails(
				"list api keys",
				listStatus,
				listErrorResponse,
				listErr,
			)
		}
		if statusCode < http.StatusBadRequest && listStatus >= http.StatusBadRequest {
			statusCode = listStatus
		}
	} else {
		apiKeys = list.ApiKeys
		if apiKeys == nil {
			apiKeys = []api.APIKey{}
		}
		hasMore = list.HasMore
		if list.NextCursor != nil {
			nextCursor = *list.NextCursor
		}
		if list.PrevCursor != nil {
			prevCursor = *list.PrevCursor
		}
	}

	roles, rolesStatus, rolesErrorResponse, rolesErr := r.listRolesViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		false,
		false,
	)
	if rolesErr != nil {
		if rolesStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if contentErrorSummary == "" {
			contentErrorSummary = "Role options are temporarily unavailable."
			contentErrorDetails = backendErrorDetails(
				"list roles",
				rolesStatus,
				rolesErrorResponse,
				rolesErr,
			)
		}
	}

	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Organization security",
		orgSlug.String(),
		"",
		"",
		c.Get(fiber.HeaderCookie),
	)
	shellData.Breadcrumbs = []views.BreadcrumbItem{
		{Label: "Organizations", Href: "/organizations"},
		{Label: organization.Name, Href: "/organizations/" + organization.Slug},
		{Label: "Security"},
	}

	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)

	if statusCode >= 400 {
		c.Status(statusCode)
	}
	return renderTempl(
		c,
		views.OrganizationSecurityPage(
			shellData,
			*organization,
			apiKeys,
			roles,
			hasMore,
			nextCursor,
			prevCursor,
			createFormData,
			tokenNotice,
			permissions,
			contentErrorSummary,
			contentErrorDetails,
		),
	)
}

func apiKeyCreateFormDataFromRequest(c fiber.Ctx) views.APIKeyCreateFormData {
	return views.APIKeyCreateFormData{
		Slug:             strings.TrimSpace(c.FormValue("slug")),
		Name:             strings.TrimSpace(c.FormValue("name")),
		Role:             strings.TrimSpace(c.FormValue("role")),
		Description:      strings.TrimSpace(c.FormValue("description")),
		ExpiresAt:        strings.TrimSpace(c.FormValue("expires_at")),
		CanManageAPIKeys: formCheckboxEnabled(c, "can_manage_api_keys"),
	}
}

// apiKeyManagementCapabilityFromRequest preserves an explicit false value when a checkbox is unchecked.
func apiKeyManagementCapabilityFromRequest(c fiber.Ctx) *bool {
	value := formCheckboxEnabled(c, "can_manage_api_keys")
	return &value
}

// apiKeyConfirmationMatches verifies the destructive API-key confirmation typed in the SSR modal.
func apiKeyConfirmationMatches(c fiber.Ctx, apiKeySlug slug.Slug) bool {
	return strings.TrimSpace(c.FormValue("api_key_confirmation")) == apiKeySlug.String()
}

type apiKeyExpirationInput struct {
	value *api.NullableTimestamp
}

func parseAPIKeyExpiresAt(value string) (apiKeyExpirationInput, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return apiKeyExpirationInput{}, nil
	}

	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return apiKeyExpirationInput{value: &parsed}, nil
	}

	parsed, err := time.ParseInLocation("2006-01-02T15:04", value, time.Local)
	if err != nil {
		return apiKeyExpirationInput{}, xerror.ValidationFailed().Detailf("parse API key expiration: %w", err)
	}
	return apiKeyExpirationInput{value: &parsed}, nil
}

func parseOrganizationAPIKeySlugs(c fiber.Ctx) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return "", "", xerror.InvalidSlugFormat().Detail("invalid organization slug")
	}

	apiKeySlug, err := slug.New(c.Params("apiKeySlug"))
	if err != nil {
		return "", "", xerror.InvalidSlugFormat().Detail("invalid api key slug")
	}

	return orgSlug, apiKeySlug, nil
}

func (r *Router) listAPIKeysViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	params *api.ListAPIKeysParams,
) (*api.APIKeyListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListAPIKeysWithResponse(
		ctx,
		organizationSlug,
		params,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"list api keys api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) createAPIKeyViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	body api.CreateAPIKeyJSONRequestBody,
) (*api.APIKeyWithToken, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.CreateAPIKeyWithResponse(
		ctx,
		organizationSlug,
		body,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusCreated && resp.JSON201 != nil {
		return resp.JSON201, http.StatusCreated, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"create api key api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) updateAPIKeyViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	apiKeySlug string,
	body api.UpdateAPIKeyJSONRequestBody,
) (*api.APIKey, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpdateAPIKeyWithResponse(
		ctx,
		organizationSlug,
		apiKeySlug,
		body,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"update api key api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) rotateAPIKeyViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	apiKeySlug string,
) (*api.APIKeyWithToken, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.RotateAPIKeyWithResponse(
		ctx,
		organizationSlug,
		apiKeySlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"rotate api key api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) deleteAPIKeyViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	apiKeySlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.DeleteAPIKeyWithResponse(
		ctx,
		organizationSlug,
		apiKeySlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"delete api key api returned status %d",
		resp.StatusCode(),
	)
}

// deleteRevokedAPIKeyViaAPI calls the backend hard-delete operation for revoked API key metadata.
func (r *Router) deleteRevokedAPIKeyViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	apiKeySlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.DeleteRevokedAPIKeyWithResponse(
		ctx,
		organizationSlug,
		apiKeySlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"delete revoked api key api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) listRolesViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	includeDeleted bool,
	onlyDeleted bool,
) ([]api.OrgRole, int, *api.ErrorResponse, error) {
	params := &api.ListRolesParams{}
	limit := api.LimitParam(100)
	params.Limit = &limit
	if includeDeleted {
		params.IncludeDeleted = &includeDeleted
	}
	if onlyDeleted {
		params.OnlyDeleted = &onlyDeleted
	}

	resp, err := r.apiClient.ListRolesWithResponse(
		ctx,
		organizationSlug,
		params,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200.Roles, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"list roles api returned status %d",
		resp.StatusCode(),
	)
}
