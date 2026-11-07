package frontend

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	organizationEnvironmentsLoadErrorMessage = "Something went wrong while loading organization environments."
	projectEnvironmentsLoadErrorMessage      = "Something went wrong while loading project environments."
	projectEnvironmentsShellSubtitle         = "Review project runtime enablement, protected deployment context, and data-source bindings."
)

func (r *Router) handleOrganizationEnvironmentsPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	return r.renderOrganizationEnvironmentsPage(
		c,
		orgSlug,
		http.StatusOK,
		"",
		"",
		views.DefaultEnvironmentCreateFormData(),
	)
}

func (r *Router) handleOrganizationEnvironmentDetailPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	environmentSlug, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}

	return r.renderOrganizationEnvironmentDetailPage(
		c,
		orgSlug,
		environmentSlug,
		http.StatusOK,
		"",
		"",
		views.DefaultEnvironmentCreateFormData(),
	)
}

func (r *Router) handleCreateEnvironmentSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	formData := environmentCreateFormDataFromRequest(c)
	name := strings.TrimSpace(formData.Name)
	if name == "" {
		return r.renderOrganizationEnvironmentsPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			"Environment name is required.",
			"Validation: environment name cannot be empty.",
			formData,
		)
	}
	environmentColor, ok := views.EnvironmentColorFromForm(formData.Color)
	if !ok {
		return r.renderOrganizationEnvironmentsPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			"Environment color is invalid.",
			"Validation: choose one of the supported environment colors.",
			formData,
		)
	}

	request := api.NewEnvironment{Name: name, Color: environmentColor}
	if formData.Protected {
		request.Protected = &formData.Protected
	}
	if description := strings.TrimSpace(formData.Description); description != "" {
		request.Description = &description
	}
	if environmentSlug := strings.TrimSpace(formData.Slug); environmentSlug != "" {
		parsedSlug, err := slug.New(environmentSlug)
		if err != nil {
			return r.renderOrganizationEnvironmentsPage(
				c,
				orgSlug,
				http.StatusBadRequest,
				"Environment slug is invalid.",
				"Validation: slug must use lowercase letters, digits, and hyphens only.",
				formData,
			)
		}
		apiSlug := parsedSlug.String()
		request.Slug = &apiSlug
	}

	status, errorResponse, err := r.createEnvironmentViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		request,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while creating the environment."
		}
		return r.renderOrganizationEnvironmentsPage(
			c,
			orgSlug,
			status,
			message,
			backendErrorDetails("create environment", status, errorResponse, err),
			formData,
		)
	}

	return redirectHTMXOrSeeOther(c, organizationEnvironmentsHref(orgSlug.String()))
}

func (r *Router) handleUpdateEnvironmentSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	environmentSlug, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}

	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		if environmentReturnToFullPage(c) {
			return r.renderOrganizationEnvironmentDetailPage(
				c,
				orgSlug,
				environmentSlug,
				http.StatusBadRequest,
				"Environment name is required.",
				"Validation: environment name cannot be empty.",
				views.DefaultEnvironmentCreateFormData(),
			)
		}
		return r.renderOrganizationEnvironmentsPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			"Environment name is required.",
			"Validation: environment name cannot be empty.",
			views.DefaultEnvironmentCreateFormData(),
		)
	}
	environmentColor, ok := views.EnvironmentColorFromForm(c.FormValue("color"))
	if !ok {
		if environmentReturnToFullPage(c) {
			return r.renderOrganizationEnvironmentDetailPage(
				c,
				orgSlug,
				environmentSlug,
				http.StatusBadRequest,
				"Environment color is invalid.",
				"Validation: choose one of the supported environment colors.",
				views.DefaultEnvironmentCreateFormData(),
			)
		}
		return r.renderOrganizationEnvironmentsPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			"Environment color is invalid.",
			"Validation: choose one of the supported environment colors.",
			views.DefaultEnvironmentCreateFormData(),
		)
	}

	request := api.UpdateEnvironment{
		Name:                             name,
		Color:                            environmentColor,
		Protected:                        formValueBool(c, "protected"),
		ProtectedEnvironmentConfirmation: optionalAPIFormSlug(c, "protected_environment_confirmation"),
	}
	if description := strings.TrimSpace(c.FormValue("description")); description != "" {
		request.Description = &description
	}

	status, errorResponse, err := r.updateEnvironmentViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		environmentSlug.String(),
		request,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while updating the environment."
		}
		if environmentReturnToFullPage(c) {
			return r.renderOrganizationEnvironmentDetailPage(
				c,
				orgSlug,
				environmentSlug,
				status,
				message,
				backendErrorDetails("update environment", status, errorResponse, err),
				views.DefaultEnvironmentCreateFormData(),
			)
		}
		return r.renderOrganizationEnvironmentsPage(
			c,
			orgSlug,
			status,
			message,
			backendErrorDetails("update environment", status, errorResponse, err),
			views.DefaultEnvironmentCreateFormData(),
		)
	}

	if environmentReturnToFullPage(c) {
		return redirectHTMXOrSeeOther(c, environmentRuntimeVariableRedirectHref(c, orgSlug.String(), environmentSlug.String()))
	}
	return redirectHTMXOrSeeOther(c, views.EnvironmentSelectionHref(orgSlug.String(), environmentSlug.String()))
}

func (r *Router) handleDeleteEnvironmentSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	environmentSlug, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}

	confirmation := strings.TrimSpace(c.FormValue("protected_environment_confirmation"))
	if confirmation == "" {
		confirmation = strings.TrimSpace(c.FormValue("confirm_slug"))
	}
	if confirmation != environmentSlug.String() {
		if environmentReturnToFullPage(c) {
			return r.renderOrganizationEnvironmentDetailPage(
				c,
				orgSlug,
				environmentSlug,
				http.StatusBadRequest,
				"Environment deletion was not confirmed.",
				"Validation: type the environment slug exactly before deleting.",
				views.DefaultEnvironmentCreateFormData(),
			)
		}
		return r.renderOrganizationEnvironmentsPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			"Environment deletion was not confirmed.",
			"Validation: type the environment slug exactly before deleting.",
			views.DefaultEnvironmentCreateFormData(),
		)
	}

	status, errorResponse, err := r.deleteEnvironmentViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		environmentSlug.String(),
		strings.TrimSpace(c.FormValue("delete_reason")),
		confirmation,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while deleting the environment."
		}
		if environmentReturnToFullPage(c) {
			return r.renderOrganizationEnvironmentDetailPage(
				c,
				orgSlug,
				environmentSlug,
				status,
				message,
				backendErrorDetails("delete environment", status, errorResponse, err),
				views.DefaultEnvironmentCreateFormData(),
			)
		}
		return r.renderOrganizationEnvironmentsPage(
			c,
			orgSlug,
			status,
			message,
			backendErrorDetails("delete environment", status, errorResponse, err),
			views.DefaultEnvironmentCreateFormData(),
		)
	}

	return redirectHTMXOrSeeOther(c, organizationEnvironmentsHref(orgSlug.String()))
}

// handleUpsertEnvironmentRuntimeVariableSubmit validates and persists an environment-scoped runtime variable.
func (r *Router) handleUpsertEnvironmentRuntimeVariableSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	environmentSlug, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}

	key := strings.TrimSpace(c.FormValue("key"))
	if key == "" {
		return r.renderEnvironmentRuntimeVariablePage(
			c,
			orgSlug,
			environmentSlug,
			http.StatusBadRequest,
			"Runtime variable key is required.",
			"Validation: provide the variable key referenced by pipeline specs.",
		)
	}
	valueType := api.RuntimeVariableValueType(strings.TrimSpace(c.FormValue("value_type")))
	if !valueType.Valid() {
		return r.renderEnvironmentRuntimeVariablePage(
			c,
			orgSlug,
			environmentSlug,
			http.StatusBadRequest,
			"Runtime variable type is invalid.",
			"Validation: choose text, integer, boolean, date, or uuid.",
		)
	}
	value := strings.TrimSpace(c.FormValue("value"))
	if value == "" {
		return r.renderEnvironmentRuntimeVariablePage(
			c,
			orgSlug,
			environmentSlug,
			http.StatusBadRequest,
			"Runtime variable value is required.",
			"Validation: runtime variables store a non-empty scalar value.",
		)
	}

	payload := api.UpsertEnvironmentRuntimeVariableJSONRequestBody{
		ValueType: valueType,
		Value:     value,
	}
	if c.Request().PostArgs().Has("description") {
		description := strings.TrimSpace(c.FormValue("description"))
		payload.Description = &description
	}
	if confirmation := optionalAPIFormSlug(c, "protected_environment_confirmation"); confirmation != nil {
		payload.ProtectedEnvironmentConfirmation = confirmation
	}

	status, errorResponse, upsertErr := r.upsertEnvironmentRuntimeVariableViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		environmentSlug.String(),
		key,
		payload,
	)
	if upsertErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Runtime variable could not be saved."
		}
		return r.renderEnvironmentRuntimeVariablePage(
			c,
			orgSlug,
			environmentSlug,
			status,
			message,
			backendErrorDetails("upsert runtime variable", status, errorResponse, upsertErr),
		)
	}

	return redirectHTMXOrSeeOther(c, environmentRuntimeVariableRedirectHref(c, orgSlug.String(), environmentSlug.String()))
}

// handleDeleteEnvironmentRuntimeVariableSubmit deletes an environment-scoped runtime variable.
func (r *Router) handleDeleteEnvironmentRuntimeVariableSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	environmentSlug, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}
	key := strings.TrimSpace(c.Params("runtimeVariableKey"))
	if key == "" {
		return c.Status(fiber.StatusBadRequest).SendString("invalid runtime variable key")
	}

	status, errorResponse, deleteErr := r.deleteEnvironmentRuntimeVariableViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		environmentSlug.String(),
		key,
		strings.TrimSpace(c.FormValue("protected_environment_confirmation")),
	)
	if deleteErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Runtime variable could not be deleted."
		}
		return r.renderEnvironmentRuntimeVariablePage(
			c,
			orgSlug,
			environmentSlug,
			status,
			message,
			backendErrorDetails("delete runtime variable", status, errorResponse, deleteErr),
		)
	}

	return redirectHTMXOrSeeOther(c, environmentRuntimeVariableRedirectHref(c, orgSlug.String(), environmentSlug.String()))
}

// handleUpsertEnvironmentSecretSubmit validates and persists a write-only environment secret.
func (r *Router) handleUpsertEnvironmentSecretSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	environmentSlug, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}

	key := strings.TrimSpace(c.FormValue("key"))
	if key == "" {
		return r.renderEnvironmentRuntimeVariablePage(
			c,
			orgSlug,
			environmentSlug,
			http.StatusBadRequest,
			"Environment secret key is required.",
			"Validation: provide the secret key referenced by pipeline specs.",
		)
	}
	value := string(c.Request().PostArgs().Peek("value"))
	if value == "" {
		return r.renderEnvironmentRuntimeVariablePage(
			c,
			orgSlug,
			environmentSlug,
			http.StatusBadRequest,
			"Environment secret value is required.",
			"Validation: enter the plaintext value to encrypt for this secret.",
		)
	}

	payload := api.UpsertEnvironmentSecretJSONRequestBody{
		Value: &value,
	}
	if c.Request().PostArgs().Has("description") {
		description := strings.TrimSpace(c.FormValue("description"))
		payload.Description = &description
	}
	if confirmation := optionalAPIFormSlug(c, "protected_environment_confirmation"); confirmation != nil {
		payload.ProtectedEnvironmentConfirmation = confirmation
	}

	status, errorResponse, upsertErr := r.upsertEnvironmentSecretViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		environmentSlug.String(),
		key,
		payload,
	)
	if upsertErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Environment secret could not be saved."
		}
		return r.renderEnvironmentRuntimeVariablePage(
			c,
			orgSlug,
			environmentSlug,
			status,
			message,
			backendErrorDetails("upsert environment secret", status, errorResponse, upsertErr),
		)
	}

	return redirectHTMXOrSeeOther(c, environmentRuntimeVariableRedirectHref(c, orgSlug.String(), environmentSlug.String()))
}

// handleDeleteEnvironmentSecretSubmit deletes an environment-scoped secret.
func (r *Router) handleDeleteEnvironmentSecretSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	environmentSlug, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}
	key := strings.TrimSpace(c.Params("environmentSecretKey"))
	if key == "" {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment secret key")
	}

	status, errorResponse, deleteErr := r.deleteEnvironmentSecretViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		environmentSlug.String(),
		key,
		strings.TrimSpace(c.FormValue("protected_environment_confirmation")),
	)
	if deleteErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Environment secret could not be deleted."
		}
		return r.renderEnvironmentRuntimeVariablePage(
			c,
			orgSlug,
			environmentSlug,
			status,
			message,
			backendErrorDetails("delete environment secret", status, errorResponse, deleteErr),
		)
	}

	return redirectHTMXOrSeeOther(c, environmentRuntimeVariableRedirectHref(c, orgSlug.String(), environmentSlug.String()))
}

func (r *Router) handleProjectEnvironmentsPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := requiredProjectSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	return r.renderProjectEnvironmentsPage(c, orgSlug, projectSlug, http.StatusOK, "", "")
}

func (r *Router) handleEnableProjectEnvironmentSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := requiredProjectSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}
	environmentSlug, err := slug.New(string(c.Request().PostArgs().Peek("environment_slug")))
	if err != nil {
		return r.renderProjectEnvironmentsPage(
			c,
			orgSlug,
			projectSlug,
			http.StatusBadRequest,
			"Environment slug is invalid.",
			"Validation: choose a valid runtime target before enabling.",
		)
	}

	status, errorResponse, err := r.enableProjectEnvironmentViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
		environmentSlug.String(),
		formValueBool(c, "is_default"),
		strings.TrimSpace(c.FormValue("protected_environment_confirmation")),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while enabling the project environment."
		}
		return r.renderProjectEnvironmentsPage(
			c,
			orgSlug,
			projectSlug,
			status,
			message,
			backendErrorDetails("enable project environment", status, errorResponse, err),
		)
	}

	return redirectHTMXOrSeeOther(c, projectEnvironmentRedirectHref(c, orgSlug.String(), projectSlug, environmentSlug.String()))
}

func (r *Router) handleSetDefaultProjectEnvironmentSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := requiredProjectSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}
	environmentSlug, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}

	status, errorResponse, err := r.enableProjectEnvironmentViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
		environmentSlug.String(),
		true,
		strings.TrimSpace(c.FormValue("protected_environment_confirmation")),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while setting the default environment."
		}
		return r.renderProjectEnvironmentsPage(
			c,
			orgSlug,
			projectSlug,
			status,
			message,
			backendErrorDetails("set default project environment", status, errorResponse, err),
		)
	}

	return redirectHTMXOrSeeOther(c, projectEnvironmentRedirectHref(c, orgSlug.String(), projectSlug, environmentSlug.String()))
}

func (r *Router) handleDisableProjectEnvironmentSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := requiredProjectSlug(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}
	environmentSlug, err := slug.New(c.Params("environmentSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}

	status, errorResponse, err := r.disableProjectEnvironmentViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
		environmentSlug.String(),
		strings.TrimSpace(c.FormValue("protected_environment_confirmation")),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while disabling the project environment."
		}
		return r.renderProjectEnvironmentsPage(
			c,
			orgSlug,
			projectSlug,
			status,
			message,
			backendErrorDetails("disable project environment", status, errorResponse, err),
		)
	}

	if c.Query("section") == "configuration" {
		return redirectHTMXOrSeeOther(c, projectEnvironmentRedirectHref(c, orgSlug.String(), projectSlug, environmentSlug.String()))
	}
	return redirectHTMXOrSeeOther(c, views.ProjectEnvironmentsHref(orgSlug.String(), projectSlug))
}

func (r *Router) renderProjectEnvironmentsPage(
	c fiber.Ctx,
	orgSlug slug.Slug,
	projectSlug string,
	statusCode int,
	pageError string,
	pageErrorDetails string,
) error {
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	selectedEnvironmentSlug := projectEnvironmentSelectedSlugFromRequest(c)
	catalog, status, errorResponse, err := r.listEnvironmentsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if pageError == "" {
			pageError = messageFromErrorResponse(errorResponse)
			if pageError == "" {
				pageError = organizationEnvironmentsLoadErrorMessage
			}
			pageErrorDetails = backendErrorDetails("list organization environments", status, errorResponse, err)
			statusCode = status
		}
	}

	projectEnvironments, status, errorResponse, err := r.listProjectEnvironmentsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = projectEnvironmentsLoadErrorMessage
		}
		shellData := r.buildShellDataForProjectPage(
			c,
			"Environments",
			orgSlug.String(),
			projectSlug,
			message,
			backendErrorDetails("list project environments", status, errorResponse, err),
			c.Get(fiber.HeaderCookie),
		)
		shellData.Subtitle = projectEnvironmentsShellSubtitle
		c.Status(status)
		return renderTempl(
			c,
			views.ProjectEnvironmentsPage(
				shellData,
				orgSlug.String(),
				projectSlug,
				selectedEnvironmentSlug,
				nil,
				nil,
				nil,
				nil,
				true,
				false,
				permissions,
			),
		)
	}

	refs, status, errorResponse, err := r.listDataSourceRefsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while loading data-source references."
		}
		shellData := r.buildShellDataForProjectPage(
			c,
			"Environments",
			orgSlug.String(),
			projectSlug,
			message,
			backendErrorDetails("list data-source refs", status, errorResponse, err),
			c.Get(fiber.HeaderCookie),
		)
		shellData.Subtitle = projectEnvironmentsShellSubtitle
		c.Status(status)
		return renderTempl(
			c,
			views.ProjectEnvironmentsPage(
				shellData,
				orgSlug.String(),
				projectSlug,
				selectedEnvironmentSlug,
				projectEnvironments.ProjectEnvironments,
				catalogEnvironments(catalog),
				nil,
				nil,
				false,
				true,
				permissions,
			),
		)
	}

	bindings := map[string][]api.DataSourceBinding{}
	for _, environment := range projectEnvironments.ProjectEnvironments {
		environmentSlug := environment.EnvironmentSlug
		bindingList, status, errorResponse, err := r.listDataSourceBindingsViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			projectSlug,
			environmentSlug,
		)
		if err != nil {
			if status == fiber.StatusUnauthorized {
				return redirectToLogin(c, c.OriginalURL())
			}
			message := messageFromErrorResponse(errorResponse)
			if message == "" {
				message = "Something went wrong while loading data-source bindings."
			}
			shellData := r.buildShellDataForProjectPage(
				c,
				"Environments",
				orgSlug.String(),
				projectSlug,
				message,
				backendErrorDetails("list data-source bindings", status, errorResponse, err),
				c.Get(fiber.HeaderCookie),
			)
			shellData.Subtitle = projectEnvironmentsShellSubtitle
			c.Status(status)
			return renderTempl(
				c,
				views.ProjectEnvironmentsPage(
					shellData,
					orgSlug.String(),
					projectSlug,
					selectedEnvironmentSlug,
					projectEnvironments.ProjectEnvironments,
					catalogEnvironments(catalog),
					refs.DataSourceRefs,
					nil,
					false,
					true,
					permissions,
				),
			)
		}
		bindings[environmentSlug] = bindingList.DataSourceBindings
	}

	shellData := r.buildShellDataForProjectPage(
		c,
		"Environments",
		orgSlug.String(),
		projectSlug,
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = projectEnvironmentsShellSubtitle

	setHTMLResponseStatus(c, statusCode)
	return renderTempl(
		c,
		views.ProjectEnvironmentsPage(
			shellData,
			orgSlug.String(),
			projectSlug,
			selectedEnvironmentSlug,
			projectEnvironments.ProjectEnvironments,
			catalogEnvironments(catalog),
			refs.DataSourceRefs,
			bindings,
			false,
			false,
			permissions,
		),
	)
}

func (r *Router) renderEnvironmentRuntimeVariablePage(
	c fiber.Ctx,
	orgSlug slug.Slug,
	environmentSlug slug.Slug,
	statusCode int,
	pageError string,
	pageErrorDetails string,
) error {
	if environmentReturnToFullPage(c) {
		return r.renderOrganizationEnvironmentDetailPage(
			c,
			orgSlug,
			environmentSlug,
			statusCode,
			pageError,
			pageErrorDetails,
			views.DefaultEnvironmentCreateFormData(),
		)
	}

	return r.renderOrganizationEnvironmentsPage(
		c,
		orgSlug,
		statusCode,
		pageError,
		pageErrorDetails,
		views.DefaultEnvironmentCreateFormData(),
	)
}

func (r *Router) renderOrganizationEnvironmentDetailPage(
	c fiber.Ctx,
	orgSlug slug.Slug,
	environmentSlug slug.Slug,
	statusCode int,
	pageError string,
	pageErrorDetails string,
	formData views.EnvironmentCreateFormData,
) error {
	environments, status, errorResponse, err := r.listEnvironmentsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if pageError == "" {
			pageError = messageFromErrorResponse(errorResponse)
			if pageError == "" {
				pageError = organizationEnvironmentsLoadErrorMessage
			}
			pageErrorDetails = backendErrorDetails("list environments", status, errorResponse, err)
			statusCode = status
		}
		return r.renderOrganizationEnvironmentsPage(c, orgSlug, statusCode, pageError, pageErrorDetails, formData)
	}

	environment, found := selectedEnvironment(catalogEnvironments(environments), environmentSlug.String())
	if !found {
		return r.renderOrganizationEnvironmentsPage(
			c,
			orgSlug,
			http.StatusNotFound,
			"Environment was not found.",
			"Lookup: selected runtime target does not exist in this organization.",
			formData,
		)
	}

	runtimeVariables := []api.RuntimeVariable{}
	runtimeVariablesUnavailable := false
	if views.OrganizationEnvironmentSection(c.Query("section")) == "variables" {
		runtimeVariableList, runtimeStatus, runtimeErrorResponse, runtimeErr := r.listEnvironmentRuntimeVariablesViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			environmentSlug.String(),
		)
		if runtimeErr != nil {
			if runtimeStatus == fiber.StatusUnauthorized {
				return redirectToLogin(c, c.OriginalURL())
			}
			runtimeVariablesUnavailable = true
			if pageError == "" {
				pageError = messageFromErrorResponse(runtimeErrorResponse)
				if pageError == "" {
					pageError = "Something went wrong while loading runtime variables."
				}
				pageErrorDetails = backendErrorDetails("list runtime variables", runtimeStatus, runtimeErrorResponse, runtimeErr)
				statusCode = runtimeStatus
			}
		} else if runtimeVariableList != nil {
			runtimeVariables = runtimeVariableList.RuntimeVariables
		}
	}
	environmentSecrets := []api.EnvironmentSecret{}
	environmentSecretsUnavailable := false
	if views.OrganizationEnvironmentSection(c.Query("section")) == "secrets" {
		environmentSecretList, secretStatus, secretErrorResponse, secretErr := r.listEnvironmentSecretsViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			environmentSlug.String(),
		)
		if secretErr != nil {
			if secretStatus == fiber.StatusUnauthorized {
				return redirectToLogin(c, c.OriginalURL())
			}
			environmentSecretsUnavailable = true
			if pageError == "" {
				pageError = messageFromErrorResponse(secretErrorResponse)
				if pageError == "" {
					pageError = "Something went wrong while loading environment secrets."
				}
				pageErrorDetails = backendErrorDetails("list environment secrets", secretStatus, secretErrorResponse, secretErr)
				statusCode = secretStatus
			}
		} else if environmentSecretList != nil {
			environmentSecrets = environmentSecretList.EnvironmentSecrets
		}
	}
	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Environments",
		orgSlug.String(),
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Manage this organization runtime target, non-secret runtime variables, and write-only environment secrets."
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)

	setHTMLResponseStatus(c, statusCode)
	return renderTempl(
		c,
		views.OrganizationEnvironmentDetailPage(
			shellData,
			orgSlug.String(),
			environment,
			runtimeVariables,
			runtimeVariablesUnavailable,
			environmentSecrets,
			environmentSecretsUnavailable,
			formData,
			permissions,
		),
	)
}

func (r *Router) renderOrganizationEnvironmentsPage(
	c fiber.Ctx,
	orgSlug slug.Slug,
	statusCode int,
	pageError string,
	pageErrorDetails string,
	formData views.EnvironmentCreateFormData,
) error {
	environments, status, errorResponse, err := r.listEnvironmentsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if pageError == "" {
			pageError = messageFromErrorResponse(errorResponse)
			if pageError == "" {
				pageError = organizationEnvironmentsLoadErrorMessage
			}
			pageErrorDetails = backendErrorDetails("list environments", status, errorResponse, err)
			statusCode = status
		}
	}

	selectedEnvironmentSlug := organizationEnvironmentSelectedSlugFromRequest(c)
	runtimeVariables := []api.RuntimeVariable{}
	runtimeVariablesUnavailable := false
	environmentSecrets := []api.EnvironmentSecret{}
	environmentSecretsUnavailable := false

	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Environments",
		orgSlug.String(),
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Manage organization runtime targets, non-secret runtime variables, and write-only environment secrets. Project enablement and bindings stay on project runtime pages."
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)

	setHTMLResponseStatus(c, statusCode)
	return renderTempl(
		c,
		views.OrganizationEnvironmentsPage(
			shellData,
			orgSlug.String(),
			catalogEnvironments(environments),
			selectedEnvironmentSlug,
			runtimeVariables,
			runtimeVariablesUnavailable,
			environmentSecrets,
			environmentSecretsUnavailable,
			formData,
			permissions,
		),
	)
}

func (r *Router) listEnvironmentsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
) (*api.EnvironmentListResponse, int, *api.ErrorResponse, error) {
	params := &api.ListEnvironmentsParams{}
	limit := api.LimitParam(100)
	params.Limit = &limit
	sortBy := api.ListEnvironmentsParamsSortByEnvironmentName
	params.SortBy = &sortBy
	order := api.ListEnvironmentsParamsOrderAsc
	params.Order = &order

	environments := []api.Environment{}
	for {
		resp, err := r.apiClient.ListEnvironmentsWithResponse(
			ctx,
			organizationSlug,
			params,
			frontendRequestEditor(ctx, cookieHeader),
		)
		if err != nil {
			return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
		}

		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			errResp := parseAPIErrorResponse(resp.Body)
			return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
				"list environments api returned status %d",
				resp.StatusCode(),
			)
		}

		environments = append(environments, resp.JSON200.Environments...)
		if !resp.JSON200.HasMore || resp.JSON200.NextCursor == nil {
			return &api.EnvironmentListResponse{
				Environments: environments,
				HasMore:      false,
				NextCursor:   nil,
				PrevCursor:   nil,
			}, http.StatusOK, nil, nil
		}

		cursor := *resp.JSON200.NextCursor
		params.Cursor = &cursor
	}
}

func (r *Router) listDataSourceRefsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
) (*api.DataSourceRefListResponse, int, *api.ErrorResponse, error) {
	params := &api.ListDataSourceRefsParams{}
	limit := api.LimitParam(100)
	params.Limit = &limit

	refs := []api.DataSourceRef{}
	for {
		resp, err := r.apiClient.ListDataSourceRefsWithResponse(
			ctx,
			organizationSlug,
			projectSlugForAPI(projectSlug),
			params,
			frontendRequestEditor(ctx, cookieHeader),
		)
		if err != nil {
			return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
		}

		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			errResp := parseAPIErrorResponse(resp.Body)
			return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
				"list data-source refs api returned status %d",
				resp.StatusCode(),
			)
		}

		refs = append(refs, resp.JSON200.DataSourceRefs...)
		if !resp.JSON200.HasMore || resp.JSON200.NextCursor == nil {
			return &api.DataSourceRefListResponse{
				DataSourceRefs: refs,
				HasMore:        false,
				NextCursor:     nil,
				PrevCursor:     nil,
			}, http.StatusOK, nil, nil
		}

		cursor := *resp.JSON200.NextCursor
		params.Cursor = &cursor
	}
}

func (r *Router) createEnvironmentViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	request api.NewEnvironment,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.CreateEnvironmentWithResponse(
		ctx,
		organizationSlug,
		request,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusCreated && resp.JSON201 != nil {
		return http.StatusCreated, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"create environment api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) updateEnvironmentViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	environmentSlug string,
	request api.UpdateEnvironment,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpdateEnvironmentWithResponse(
		ctx,
		organizationSlug,
		environmentSlug,
		request,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"update environment api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) deleteEnvironmentViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	environmentSlug string,
	deleteReason string,
	protectedConfirmation string,
) (int, *api.ErrorResponse, error) {
	params := &api.DeleteEnvironmentParams{}
	if deleteReason != "" {
		params.DeleteReason = &deleteReason
	}
	if confirmation := strings.TrimSpace(protectedConfirmation); confirmation != "" {
		params.ProtectedEnvironmentConfirmation = &confirmation
	}

	resp, err := r.apiClient.DeleteEnvironmentWithResponse(
		ctx,
		organizationSlug,
		environmentSlug,
		params,
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
		"delete environment api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) enableProjectEnvironmentViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	isDefault bool,
	protectedConfirmation string,
) (int, *api.ErrorResponse, error) {
	request := api.EnableProjectEnvironmentRequest{IsDefault: &isDefault}
	if confirmation := strings.TrimSpace(protectedConfirmation); confirmation != "" {
		request.ProtectedEnvironmentConfirmation = &confirmation
	}
	resp, err := r.apiClient.EnableProjectEnvironmentWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		environmentSlug,
		request,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"enable project environment api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) disableProjectEnvironmentViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	protectedConfirmation string,
) (int, *api.ErrorResponse, error) {
	params := &api.DisableProjectEnvironmentParams{}
	if confirmation := strings.TrimSpace(protectedConfirmation); confirmation != "" {
		params.ProtectedEnvironmentConfirmation = &confirmation
	}
	resp, err := r.apiClient.DisableProjectEnvironmentWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		environmentSlug,
		params,
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
		"disable project environment api returned status %d",
		resp.StatusCode(),
	)
}

func environmentCreateFormDataFromRequest(c fiber.Ctx) views.EnvironmentCreateFormData {
	return views.EnvironmentCreateFormData{
		Name:        strings.TrimSpace(c.FormValue("name")),
		Slug:        strings.TrimSpace(c.FormValue("slug")),
		Description: strings.TrimSpace(c.FormValue("description")),
		Color:       strings.TrimSpace(c.FormValue("color")),
		Protected:   formValueBool(c, "protected"),
	}
}

func optionalAPIFormSlug(c fiber.Ctx, key string) *api.NullableSlug {
	value := strings.TrimSpace(c.FormValue(key))
	if value == "" {
		return nil
	}
	return &value
}

func catalogEnvironments(list *api.EnvironmentListResponse) []api.Environment {
	if list == nil {
		return nil
	}
	return list.Environments
}

func organizationEnvironmentsHref(orgSlug string) string {
	return "/organizations/" + strings.TrimSpace(orgSlug) + "/environments"
}

func projectEnvironmentRedirectHref(c fiber.Ctx, orgSlug, projectSlug, environmentSlug string) string {
	if c.Query("section") == "configuration" {
		activeEnvironment := strings.TrimSpace(c.FormValue("active_environment_slug"))
		if activeEnvironment == "" {
			activeEnvironment = strings.TrimSpace(c.Query("environment_slug"))
		}
		return views.ProjectEnvironmentConfigurationHref(orgSlug, projectSlug, activeEnvironment, environmentSlug)
	}
	values := url.Values{}
	values.Set("environment_slug", strings.TrimSpace(environmentSlug))
	return views.ProjectEnvironmentsHref(orgSlug, projectSlug) + "?" + values.Encode()
}

// projectEnvironmentSelectedSlugFromRequest resolves the catalog row that should remain focused after an SSR action.
func projectEnvironmentSelectedSlugFromRequest(c fiber.Ctx) string {
	if selected := strings.TrimSpace(c.Query("selected")); selected != "" {
		return selected
	}
	if selected := strings.TrimSpace(c.FormValue("selected_environment_slug")); selected != "" {
		return selected
	}
	if selected := strings.TrimSpace(c.FormValue("environment_slug")); selected != "" {
		return selected
	}
	return strings.TrimSpace(c.Params("environmentSlug"))
}

func organizationEnvironmentSelectedSlugFromRequest(c fiber.Ctx) string {
	if selected := strings.TrimSpace(c.Query("environment_slug")); selected != "" {
		return selected
	}
	if selected := strings.TrimSpace(c.FormValue("environment_slug")); selected != "" {
		return selected
	}
	return strings.TrimSpace(c.Params("environmentSlug"))
}

func selectedEnvironment(environments []api.Environment, environmentSlug string) (api.Environment, bool) {
	environmentSlug = strings.TrimSpace(environmentSlug)
	for _, environment := range environments {
		if environment.Slug == environmentSlug {
			return environment, true
		}
	}
	return api.Environment{}, false
}

func environmentReturnToFullPage(c fiber.Ctx) bool {
	return strings.TrimSpace(c.FormValue("return_to")) == "full"
}

func environmentRuntimeVariableRedirectHref(c fiber.Ctx, orgSlug, environmentSlug string) string {
	if environmentReturnToFullPage(c) {
		u, _ := url.Parse(views.OrganizationEnvironmentDetailHref(orgSlug, environmentSlug))
		values := u.Query()
		for _, key := range []string{"section", "list_return"} {
			if value := c.Query(key); value != "" {
				values.Set(key, value)
			}
		}
		u.RawQuery = values.Encode()
		return u.String()
	}
	return views.EnvironmentSelectionHref(orgSlug, environmentSlug)
}

func formValueBool(c fiber.Ctx, key string) bool {
	value := strings.TrimSpace(c.FormValue(key))
	return value == "1" || value == booleanTrueString || value == "on"
}

func (r *Router) listDataSourceBindingsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
) (*api.DataSourceBindingListResponse, int, *api.ErrorResponse, error) {
	params := &api.ListDataSourceBindingsParams{}
	limit := api.LimitParam(100)
	params.Limit = &limit

	bindings := []api.DataSourceBinding{}
	for {
		resp, err := r.apiClient.ListDataSourceBindingsWithResponse(
			ctx,
			organizationSlug,
			projectSlugForAPI(projectSlug),
			environmentSlug,
			params,
			frontendRequestEditor(ctx, cookieHeader),
		)
		if err != nil {
			return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
		}

		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			errResp := parseAPIErrorResponse(resp.Body)
			return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
				"list data-source bindings api returned status %d",
				resp.StatusCode(),
			)
		}

		bindings = append(bindings, resp.JSON200.DataSourceBindings...)
		if !resp.JSON200.HasMore || resp.JSON200.NextCursor == nil {
			return &api.DataSourceBindingListResponse{
				DataSourceBindings: bindings,
				HasMore:            false,
				NextCursor:         nil,
				PrevCursor:         nil,
			}, http.StatusOK, nil, nil
		}

		cursor := *resp.JSON200.NextCursor
		params.Cursor = &cursor
	}
}

// upsertDataSourceBindingViaAPI saves one environment binding through the canonical API.
func (r *Router) upsertDataSourceBindingViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	pipelineSlug string,
	environmentSlug string,
	refSlug string,
	payload api.UpsertDataSourceBindingJSONRequestBody,
) (*api.DataSourceBinding, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpsertDataSourceBindingWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		pipelineSlug,
		environmentSlug,
		refSlug,
		payload,
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
		"upsert data-source binding api returned status %d",
		resp.StatusCode(),
	)
}

// deleteDataSourceBindingViaAPI removes one environment binding through the canonical API.
func (r *Router) deleteDataSourceBindingViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	pipelineSlug string,
	environmentSlug string,
	refSlug string,
	params *api.DeleteDataSourceBindingParams,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.DeleteDataSourceBindingWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		pipelineSlug,
		environmentSlug,
		refSlug,
		params,
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
		"delete data-source binding api returned status %d",
		resp.StatusCode(),
	)
}

// listEnvironmentRuntimeVariablesViaAPI loads all runtime variables for one organization runtime environment.
func (r *Router) listEnvironmentRuntimeVariablesViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	environmentSlug string,
) (*api.RuntimeVariableListResponse, int, *api.ErrorResponse, error) {
	params := &api.ListEnvironmentRuntimeVariablesParams{}
	limit := api.LimitParam(100)
	params.Limit = &limit
	order := api.ListEnvironmentRuntimeVariablesParamsOrderAsc
	params.Order = &order

	runtimeVariables := []api.RuntimeVariable{}
	for {
		resp, err := r.apiClient.ListEnvironmentRuntimeVariablesWithResponse(
			ctx,
			organizationSlug,
			environmentSlug,
			params,
			frontendRequestEditor(ctx, cookieHeader),
		)
		if err != nil {
			return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
		}

		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			errResp := parseAPIErrorResponse(resp.Body)
			return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
				"list environment runtime variables api returned status %d",
				resp.StatusCode(),
			)
		}

		runtimeVariables = append(runtimeVariables, resp.JSON200.RuntimeVariables...)
		if !resp.JSON200.HasMore || resp.JSON200.NextCursor == nil {
			return &api.RuntimeVariableListResponse{
				RuntimeVariables: runtimeVariables,
				HasMore:          false,
				NextCursor:       nil,
				PrevCursor:       nil,
			}, http.StatusOK, nil, nil
		}

		cursor := *resp.JSON200.NextCursor
		params.Cursor = &cursor
	}
}

// upsertEnvironmentRuntimeVariableViaAPI writes an environment runtime variable through the API.
func (r *Router) upsertEnvironmentRuntimeVariableViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	environmentSlug string,
	key string,
	payload api.UpsertEnvironmentRuntimeVariableJSONRequestBody,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpsertEnvironmentRuntimeVariableWithResponse(
		ctx,
		organizationSlug,
		environmentSlug,
		key,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"upsert environment runtime variable api returned status %d",
		resp.StatusCode(),
	)
}

// deleteEnvironmentRuntimeVariableViaAPI removes an environment runtime variable.
func (r *Router) deleteEnvironmentRuntimeVariableViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	environmentSlug string,
	key string,
	protectedConfirmation string,
) (int, *api.ErrorResponse, error) {
	params := &api.DeleteEnvironmentRuntimeVariableParams{}
	if confirmation := strings.TrimSpace(protectedConfirmation); confirmation != "" {
		value := confirmation
		params.ProtectedEnvironmentConfirmation = &value
	}
	resp, err := r.apiClient.DeleteEnvironmentRuntimeVariableWithResponse(
		ctx,
		organizationSlug,
		environmentSlug,
		key,
		params,
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
		"delete environment runtime variable api returned status %d",
		resp.StatusCode(),
	)
}

// listEnvironmentSecretsViaAPI loads all write-only secret metadata for one organization runtime environment.
func (r *Router) listEnvironmentSecretsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	environmentSlug string,
) (*api.EnvironmentSecretListResponse, int, *api.ErrorResponse, error) {
	params := &api.ListEnvironmentSecretsParams{}
	limit := api.LimitParam(100)
	params.Limit = &limit
	order := api.ListEnvironmentSecretsParamsOrderAsc
	params.Order = &order

	secrets := []api.EnvironmentSecret{}
	for {
		resp, err := r.apiClient.ListEnvironmentSecretsWithResponse(
			ctx,
			organizationSlug,
			environmentSlug,
			params,
			frontendRequestEditor(ctx, cookieHeader),
		)
		if err != nil {
			return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
		}

		if resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			errResp := parseAPIErrorResponse(resp.Body)
			return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
				"list environment secrets api returned status %d",
				resp.StatusCode(),
			)
		}

		secrets = append(secrets, resp.JSON200.EnvironmentSecrets...)
		if !resp.JSON200.HasMore || resp.JSON200.NextCursor == nil {
			return &api.EnvironmentSecretListResponse{
				EnvironmentSecrets: secrets,
				HasMore:            false,
				NextCursor:         nil,
				PrevCursor:         nil,
			}, http.StatusOK, nil, nil
		}

		cursor := *resp.JSON200.NextCursor
		params.Cursor = &cursor
	}
}

// upsertEnvironmentSecretViaAPI writes an environment secret through the API.
func (r *Router) upsertEnvironmentSecretViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	environmentSlug string,
	key string,
	payload api.UpsertEnvironmentSecretJSONRequestBody,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpsertEnvironmentSecretWithResponse(
		ctx,
		organizationSlug,
		environmentSlug,
		key,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"upsert environment secret api returned status %d",
		resp.StatusCode(),
	)
}

// deleteEnvironmentSecretViaAPI removes an environment secret.
func (r *Router) deleteEnvironmentSecretViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	environmentSlug string,
	key string,
	protectedConfirmation string,
) (int, *api.ErrorResponse, error) {
	params := &api.DeleteEnvironmentSecretParams{}
	if confirmation := strings.TrimSpace(protectedConfirmation); confirmation != "" {
		value := confirmation
		params.ProtectedEnvironmentConfirmation = &value
	}
	resp, err := r.apiClient.DeleteEnvironmentSecretWithResponse(
		ctx,
		organizationSlug,
		environmentSlug,
		key,
		params,
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
		"delete environment secret api returned status %d",
		resp.StatusCode(),
	)
}
