package frontend

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const workspaceGatePath = "/workspace"

// requireWorkspace redirects authenticated users without organization membership to the setup gate.
func (r *Router) requireWorkspace(c fiber.Ctx) error {
	if c.Method() != http.MethodGet && (c.Method() != http.MethodPost || c.Path() != organizationsPath) {
		return c.Next()
	}
	if workspaceGateShouldBypassPathValidation(c) {
		return c.Next()
	}
	// A selected active organization is established only by membership-backed flows.
	// Org-scoped backend authorization remains authoritative for revoked access.
	if reqctx.GetActiveOrganizationSlug(c, r.sessionStore).String() != "" {
		return c.Next()
	}

	organizations, status, errorResponse, err := r.listWorkspaceGateOrganizations(c)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		r.logger.Error(
			"workspace gate organization lookup failed",
			slog.Int("status", status),
			slog.String("details", errorPopupDetails(errorResponse, "")),
			slog.Any("error", err),
		)
		return c.Status(fiber.StatusServiceUnavailable).SendString("workspace membership unavailable")
	}

	if len(organizations) == 0 {
		return redirectHTMXOrSeeOther(c, workspaceGateRedirectURL(productReturnTo(c)))
	}

	r.ensureActiveOrganizationMembership(c, organizations)
	return c.Next()
}

func workspaceGateShouldBypassPathValidation(c fiber.Ctx) bool {
	return strings.HasSuffix(c.Path(), "/canonical-state")
}

// handleWorkspaceGatePage renders the post-auth workspace setup gate for users without membership.
func (r *Router) handleWorkspaceGatePage(c fiber.Ctx) error {
	returnTo := safeReturnTo(c.Query("return_to"))
	organizations, status, listErrorResponse, err := r.listWorkspaceGateOrganizations(c)
	if err != nil && status == fiber.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if err == nil && len(organizations) > 0 {
		r.ensureActiveOrganizationMembership(c, organizations)
		return redirectHTMXOrSeeOther(c, resolvePostLoginRedirect(returnTo))
	}

	profile, profileStatus, profileErrorResponse, profileErr := r.getProfileViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
	)
	pageError := ""
	pageErrorDetails := ""
	if err != nil {
		pageError = messageFromErrorResponse(listErrorResponse)
		if pageError == "" {
			pageError = fmt.Sprintf("Failed to load workspace memberships (status %d).", status)
		}
		pageErrorDetails = backendErrorDetails("load workspace memberships", status, listErrorResponse, err)
	}
	if profileErr != nil {
		if profileStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if pageError == "" {
			pageError = messageFromErrorResponse(profileErrorResponse)
			if pageError == "" {
				pageError = fmt.Sprintf("Failed to load workspace setup (status %d).", profileStatus)
			}
			pageErrorDetails = backendErrorDetails("load workspace setup profile", profileStatus, profileErrorResponse, profileErr)
		}
	}

	usageType := workspaceGateUsageType(c.Query("usage_type"))
	organizationWorkspaceName := ""
	personalWorkspaceName := derivePersonalWorkspaceName(profile)

	return renderTempl(c, views.WorkspaceGatePage(
		"Set up workspace",
		usageType,
		organizationWorkspaceName,
		personalWorkspaceName,
		returnTo,
		pageError,
		pageErrorDetails,
		strings.TrimSpace(c.FormValue("slug")),
	))
}

// handleWorkspaceGateSubmit creates the user's first organization workspace after authentication.
func (r *Router) handleWorkspaceGateSubmit(c fiber.Ctx) error {
	usageType := normalizeSignupUsageType(c.FormValue("usage_type"))
	returnTo := safeReturnTo(c.FormValue("return_to"))
	organizationWorkspaceName := strings.TrimSpace(c.FormValue("organization_workspace_name"))

	organizations, listStatus, listErrorResponse, listErr := r.listWorkspaceGateOrganizations(c)
	if listErr != nil {
		if listStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		return r.renderWorkspaceGateSubmitError(
			c,
			usageType,
			organizationWorkspaceName,
			"",
			returnTo,
			listStatus,
			listErrorResponse,
			listErr,
		)
	}
	if len(organizations) > 0 {
		r.ensureActiveOrganizationMembership(c, organizations)
		return redirectHTMXOrSeeOther(c, resolvePostLoginRedirect(returnTo))
	}

	profile, status, errorResponse, err := r.getProfileViaAPI(c.Context(), c.Get(fiber.HeaderCookie))
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		return r.renderWorkspaceGateSubmitError(
			c,
			usageType,
			organizationWorkspaceName,
			"",
			returnTo,
			status,
			errorResponse,
			err,
		)
	}

	organizationName := organizationWorkspaceName
	personalWorkspaceName := derivePersonalWorkspaceName(profile)
	if usageType == signupUsagePersonal {
		organizationName = derivePersonalWorkspaceName(profile)
	}
	customSlug, validationMessage := optionalAPISlug(c.FormValue("slug"), "workspace")
	if validationMessage == "" {
		validationMessage = validateOrganizationInput(organizationName)
	}
	if validationMessage != "" {
		setHTMXAwareErrorStatus(c, fiber.StatusBadRequest)
		return renderTempl(c, views.WorkspaceGatePage(
			"Set up workspace",
			usageType,
			organizationWorkspaceName,
			personalWorkspaceName,
			returnTo,
			validationMessage,
			"",
			strings.TrimSpace(c.FormValue("slug")),
		))
	}

	createdOrganization, createStatus, createErrorResponse, createErr := r.createOrganizationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		api.CreateOrganizationJSONRequestBody{Name: organizationName, Slug: customSlug},
	)
	if createErr != nil {
		if createStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		return r.renderWorkspaceGateSubmitError(
			c,
			usageType,
			organizationWorkspaceName,
			personalWorkspaceName,
			returnTo,
			createStatus,
			createErrorResponse,
			createErr,
		)
	}

	if createdOrganization != nil {
		if orgSlug, slugErr := slug.New(createdOrganization.Slug); slugErr == nil {
			if setErr := reqctx.SetActiveOrganizationSlug(c, r.sessionStore, orgSlug); setErr != nil {
				r.logger.Error("failed to set active organization from workspace gate", slog.Any("error", setErr))
			}
		}
	}

	return redirectHTMXOrSeeOther(c, resolvePostLoginRedirect(returnTo))
}

// renderWorkspaceGateSubmitError re-renders the gate with backend error details preserved.
func (r *Router) renderWorkspaceGateSubmitError(
	c fiber.Ctx,
	usageType string,
	organizationWorkspaceName string,
	personalWorkspaceName string,
	returnTo string,
	status int,
	errorResponse *api.ErrorResponse,
	err error,
) error {
	if status < 400 || status >= 600 {
		status = fiber.StatusInternalServerError
	}
	message := messageFromErrorResponse(errorResponse)
	if message == "" {
		message = fmt.Sprintf("Workspace setup failed with status %d.", status)
	}
	if personalWorkspaceName == "" {
		personalWorkspaceName = "Personal workspace"
	}

	setHTMXAwareErrorStatus(c, status)
	return renderTempl(c, views.WorkspaceGatePage(
		"Set up workspace",
		usageType,
		organizationWorkspaceName,
		personalWorkspaceName,
		returnTo,
		message,
		backendErrorDetails("workspace setup", status, errorResponse, err),
		strings.TrimSpace(c.FormValue("slug")),
	))
}

func (r *Router) listWorkspaceGateOrganizations(
	c fiber.Ctx,
) ([]api.OrganizationWithRole, int, *api.ErrorResponse, error) {
	params := &api.ListOrganizationsParams{}
	limit := api.LimitParam(100)
	params.Limit = &limit

	orgList, status, errorResponse, err := r.listOrganizationsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		params,
	)
	if err != nil {
		return nil, status, errorResponse, err
	}
	return orgList.Organizations, status, nil, nil
}

func (r *Router) ensureActiveOrganizationMembership(
	c fiber.Ctx,
	organizations []api.OrganizationWithRole,
) {
	if len(organizations) == 0 {
		return
	}

	activeOrganizationSlug := reqctx.GetActiveOrganizationSlug(c, r.sessionStore).String()
	if organizationListContainsSlug(organizations, activeOrganizationSlug) {
		return
	}

	orgSlug, err := slug.New(organizations[0].Slug)
	if err != nil {
		r.logger.Error("workspace gate received invalid organization slug", slog.Any("error", err))
		return
	}
	if err := reqctx.SetActiveOrganizationSlug(c, r.sessionStore, orgSlug); err != nil {
		r.logger.Error("failed to set active organization from membership", slog.Any("error", err))
	}
}

func organizationListContainsSlug(organizations []api.OrganizationWithRole, orgSlug string) bool {
	orgSlug = strings.TrimSpace(orgSlug)
	if orgSlug == "" {
		return false
	}
	for _, organization := range organizations {
		if organization.Slug == orgSlug {
			return true
		}
	}
	return false
}

func workspaceGateUsageType(queryUsageType string) string {
	if strings.TrimSpace(queryUsageType) != "" {
		return normalizeSignupUsageType(queryUsageType)
	}
	return signupUsagePersonal
}

func derivePersonalWorkspaceName(profile *api.User) string {
	base := ""
	if profile != nil {
		base = strings.TrimSpace(profile.FullName)
		if base == "" {
			base = emailLocalPart(string(profile.Email))
		}
	}
	if base == "" {
		base = "Personal"
	}
	return base + "'s workspace"
}

func emailLocalPart(email string) string {
	email = strings.TrimSpace(email)
	if at := strings.Index(email, "@"); at > 0 {
		return strings.TrimSpace(email[:at])
	}
	return email
}

func productReturnTo(c fiber.Ctx) string {
	if c.Method() != http.MethodGet {
		return defaultPostLoginPath
	}
	returnTo := safeReturnTo(c.OriginalURL())
	if returnTo == "" {
		return defaultPostLoginPath
	}
	return returnTo
}

func workspaceGateRedirectURL(returnTo string) string {
	values := url.Values{}
	if safe := safeReturnTo(returnTo); safe != "" {
		values.Set("return_to", safe)
	}
	if encoded := values.Encode(); encoded != "" {
		return workspaceGatePath + "?" + encoded
	}
	return workspaceGatePath
}

func (r *Router) userCanSelectOrganization(
	c fiber.Ctx,
	orgSlug slug.Slug,
) (bool, int, *api.ErrorResponse, error) {
	organizations, status, errorResponse, err := r.listWorkspaceGateOrganizations(c)
	if err != nil {
		return false, status, errorResponse, err
	}
	return organizationListContainsSlug(organizations, orgSlug.String()), status, nil, nil
}
