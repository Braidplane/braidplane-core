package frontend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// handleAlertsPage renders organization alert destination and policy management.
func (r *Router) handleAlertsPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	for _, key := range []string{"policy_slug", "destination_slug", "project_slug", "environment_slug"} {
		if value := c.Query(key); value != "" {
			if _, err := slug.New(value); err != nil {
				return c.SendStatus(http.StatusBadRequest)
			}
		}
	}
	r.loadOperationalCapabilities(c, orgSlug.String())
	if condition := c.Query("notification_type"); condition != "" && !views.NotificationTypeAvailable(condition) && (condition != string(api.NotificationTypePipelineFreshnessSLABreached) || !views.RecencyAvailable(c.Context())) {
		return c.Status(http.StatusBadRequest).SendString("This condition is not currently monitored by notifications.")
	}
	if c.Query("environment_slug") != "" && c.Query("project_slug") == "" {
		return c.SendStatus(http.StatusBadRequest)
	}
	form := alertPolicySetupFormFromReturn(c.OriginalURL(), orgSlug.String())
	if c.Query("notification_type") == string(api.NotificationTypePipelineFreshnessSLABreached) && views.RecencyAvailable(c.Context()) {
		form.NotificationType = c.Query("notification_type")
		if target, err := slug.New(c.Query("pipeline_slug")); err == nil {
			form.SourceKind, form.SourceRef = string(api.EventEntityTypePipeline), target.String()
		}
	}

	return r.renderAlertsPage(
		c,
		orgSlug,
		http.StatusOK,
		views.AlertTabFromString(c.Query("tab")),
		views.DefaultAlertDestinationFormData(),
		form,
		"",
		"",
		"",
	)
}

// handleAlertPolicySelectorOptions returns the dependent alert policy selector fields for HTMX form refreshes.
func (r *Router) handleAlertPolicySelectorOptions(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	formData := alertPolicyFormDataFromRequest(c)
	if strings.TrimSpace(formData.Scope) == "" {
		formData.Scope = string(api.AlertPolicyScopeOrganization)
	}
	formData = views.AlertPolicyFormDataForRender(formData)
	if !views.AlertPolicySourceRefEnabled(formData) {
		formData.SourceRef = ""
	}

	selectorTargetID := strings.TrimSpace(c.FormValue("selector_target_id"))
	if selectorTargetID == "" {
		selectorTargetID = views.AlertPolicyCreateSelectorID()
	}

	r.loadOperationalCapabilities(c, orgSlug.String())
	dynamicOptions := views.AlertPolicyDynamicOptions{}
	if views.AlertPolicyProjectScopeEnabled(formData) {
		projects, status, errorResponse, projectErr := r.alertPolicyProjectOptionsViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
		)
		if projectErr != nil {
			if status == fiber.StatusUnauthorized {
				return redirectToLogin(c, c.OriginalURL())
			}
			r.logger.DebugContext(
				c.Context(),
				"alert policy projects unavailable",
				"status", status,
				"error", backendErrorDetails("list projects for alert policy selector", status, errorResponse, projectErr),
			)
			dynamicOptions.Projects = []views.AlertFormOption{}
		} else {
			dynamicOptions.Projects = projects
		}
	}

	if views.AlertPolicyEnvironmentScopeEnabled(formData) && strings.TrimSpace(formData.ProjectSlug) != "" {
		environments, status, errorResponse, environmentErr := r.alertPolicyEnvironmentOptionsViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			formData.ProjectSlug,
		)
		if environmentErr != nil {
			if status == fiber.StatusUnauthorized {
				return redirectToLogin(c, c.OriginalURL())
			}
			r.logger.DebugContext(
				c.Context(),
				"alert policy project environments unavailable",
				"status", status,
				"error", backendErrorDetails("list project environments for alert policy selector", status, errorResponse, environmentErr),
			)
			dynamicOptions.Environments = []views.AlertFormOption{}
		} else {
			dynamicOptions.Environments = environments
			if strings.TrimSpace(formData.EnvironmentSlug) != "" &&
				!alertPolicyFormOptionsContain(dynamicOptions.Environments, formData.EnvironmentSlug) {
				formData.EnvironmentSlug = ""
			}
		}
	}

	sourceMetadata, sourceRefs, status, errorResponse, sourceMetadataErr := r.alertPolicySourceMetadataViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		formData,
	)
	if sourceMetadataErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		r.logger.DebugContext(
			c.Context(),
			"alert policy source metadata unavailable",
			"status", status,
			"error", backendErrorDetails("list alert policy source metadata", status, errorResponse, sourceMetadataErr),
		)
		dynamicOptions.SourceMetadata = []api.AlertPolicySourceMetadata{}
		dynamicOptions.SourceRefs = []views.AlertFormOption{}
	} else {
		dynamicOptions.SourceMetadata = sourceMetadata
		dynamicOptions.SourceRefs = sourceRefs
	}

	return renderTempl(c, views.AlertPolicySelectorFields(orgSlug.String(), selectorTargetID, formData, dynamicOptions))
}

// handleCreateAlertDestinationSubmit validates and creates an alert destination.
func (r *Router) handleCreateAlertDestinationSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	formData := alertDestinationFormDataFromRequest(c)
	payload, validationErr := alertCreateDestinationPayloadFromForm(formData)
	if validationErr != "" {
		return r.renderAlertsPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.AlertTabDestinations,
			formData,
			alertPolicySetupFormFromReturn(c.FormValue("notification_setup"), orgSlug.String()),
			"Alert destination could not be created.",
			validationErr,
			"",
		)
	}

	_, status, errorResponse, createErr := r.createAlertDestinationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		payload,
	)
	if createErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Alert destination could not be created."
		}
		return r.renderAlertsPage(
			c,
			orgSlug,
			status,
			views.AlertTabDestinations,
			formData,
			alertPolicySetupFormFromReturn(c.FormValue("notification_setup"), orgSlug.String()),
			message,
			backendErrorDetails("create alert destination", status, errorResponse, createErr),
			"",
		)
	}

	if setup := alertSetupReturnTo(c.FormValue("notification_setup"), orgSlug.String()); setup != "" {
		return redirectHTMXOrSeeOther(c, setup)
	}
	return redirectHTMXOrSeeOther(c, views.AlertJourneyHref(orgSlug.String(), views.AlertTabDestinations, notificationReturnTo(c.FormValue("notification_return"), orgSlug.String()), ""))
}

// handleUpdateAlertDestinationSubmit replaces mutable alert destination fields.
func (r *Router) handleUpdateAlertDestinationSubmit(c fiber.Ctx) error {
	orgSlug, destinationSlug, err := parseAlertDestinationSlugs(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	formData := alertDestinationFormDataFromRequest(c)
	payload, validationErr, invalidField := alertUpdateDestinationPayloadFromForm(formData)
	if validationErr != "" {
		formData.ErrorMessage = validationErr
		formData.InvalidField = invalidField
		return r.renderAlertsPageWithEditOverrides(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.AlertTabDestinations,
			views.DefaultAlertDestinationFormData(),
			views.DefaultAlertPolicyFormData(),
			views.AlertEditFormOverrides{
				DestinationSlug:     destinationSlug.String(),
				DestinationFormData: formData,
			},
			"Alert destination could not be updated.",
			validationErr,
			"",
		)
	}

	_, status, errorResponse, updateErr := r.updateAlertDestinationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		destinationSlug.String(),
		payload,
	)
	if updateErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Alert destination could not be updated."
		}
		formData.ErrorMessage = formFeedbackFromErrorResponse(errorResponse, message)
		formData.InvalidField = formValidationField(errorResponse, "name", "type", "to", "connection_slug")
		return r.renderAlertsPageWithEditOverrides(
			c,
			orgSlug,
			status,
			views.AlertTabDestinations,
			views.DefaultAlertDestinationFormData(),
			views.DefaultAlertPolicyFormData(),
			views.AlertEditFormOverrides{
				DestinationSlug:     destinationSlug.String(),
				DestinationFormData: formData,
			},
			message,
			backendErrorDetails("update alert destination", status, errorResponse, updateErr),
			"",
		)
	}

	return redirectHTMXOrSeeOther(c, views.AlertJourneyHref(orgSlug.String(), views.AlertTabDestinations, notificationReturnTo(c.FormValue("notification_return"), orgSlug.String()), ""))
}

// handleTestAlertDestinationSubmit enqueues a test delivery through one alert destination.
func (r *Router) handleTestAlertDestinationSubmit(c fiber.Ctx) error {
	orgSlug, destinationSlug, err := parseAlertDestinationSlugs(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	result, status, errorResponse, testErr := r.testAlertDestinationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		destinationSlug.String(),
	)
	if testErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Alert destination test could not be accepted."
		}
		return r.renderAlertsPage(
			c,
			orgSlug,
			status,
			views.AlertTabDestinations,
			views.DefaultAlertDestinationFormData(),
			views.DefaultAlertPolicyFormData(),
			message,
			backendErrorDetails("test alert destination", status, errorResponse, testErr),
			"",
		)
	}

	return r.renderAlertsPage(
		c,
		orgSlug,
		http.StatusOK,
		views.AlertTabDestinations,
		views.DefaultAlertDestinationFormData(),
		views.DefaultAlertPolicyFormData(),
		"",
		"",
		alertDestinationTestSuccessSummary(result),
	)
}

// handleDeleteAlertDestinationSubmit removes an alert destination after slug confirmation.
func (r *Router) handleDeleteAlertDestinationSubmit(c fiber.Ctx) error {
	orgSlug, destinationSlug, err := parseAlertDestinationSlugs(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	if !alertDeleteConfirmationMatches(c, "destination_confirmation", destinationSlug) {
		return r.renderAlertsPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.AlertTabDestinations,
			views.DefaultAlertDestinationFormData(),
			views.DefaultAlertPolicyFormData(),
			"Alert destination could not be deleted.",
			"Validation: type the destination slug to confirm deletion.",
			"",
		)
	}

	status, errorResponse, deleteErr := r.deleteAlertDestinationViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		destinationSlug.String(),
	)
	if deleteErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Alert destination could not be deleted."
		}
		return r.renderAlertsPage(
			c,
			orgSlug,
			status,
			views.AlertTabDestinations,
			views.DefaultAlertDestinationFormData(),
			views.DefaultAlertPolicyFormData(),
			message,
			backendErrorDetails("delete alert destination", status, errorResponse, deleteErr),
			"",
		)
	}

	return redirectHTMXOrSeeOther(c, views.AlertJourneyHref(orgSlug.String(), views.AlertTabDestinations, notificationReturnTo(c.FormValue("notification_return"), orgSlug.String()), ""))
}

// handleCreateAlertPolicySubmit validates and creates an alert policy with destination bindings.
func (r *Router) handleCreateAlertPolicySubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	formData := alertPolicyFormDataFromRequest(c)
	payload, validationErr := alertCreatePolicyPayloadFromForm(formData)
	if validationErr != "" {
		return r.renderAlertsPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.AlertTabPolicies,
			views.DefaultAlertDestinationFormData(),
			formData,
			"Alert policy could not be created.",
			validationErr,
			"",
		)
	}

	_, status, errorResponse, createErr := r.createAlertPolicyViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		payload,
	)
	if createErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Alert policy could not be created."
		}
		return r.renderAlertsPage(
			c,
			orgSlug,
			status,
			views.AlertTabPolicies,
			views.DefaultAlertDestinationFormData(),
			formData,
			message,
			backendErrorDetails("create alert policy", status, errorResponse, createErr),
			"",
		)
	}

	return redirectHTMXOrSeeOther(c, views.AlertJourneyHref(orgSlug.String(), views.AlertTabPolicies, notificationReturnTo(c.FormValue("notification_return"), orgSlug.String()), ""))
}

// handleUpdateAlertPolicySubmit replaces mutable alert policy fields and bindings.
func (r *Router) handleUpdateAlertPolicySubmit(c fiber.Ctx) error {
	orgSlug, policySlug, err := parseAlertPolicySlugs(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	formData := alertPolicyFormDataFromRequest(c)
	payload, validationErr := alertUpdatePolicyPayloadFromForm(formData)
	if validationErr != "" {
		return r.renderAlertsPageWithEditOverrides(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.AlertTabPolicies,
			views.DefaultAlertDestinationFormData(),
			views.DefaultAlertPolicyFormData(),
			views.AlertEditFormOverrides{
				PolicySlug:     policySlug.String(),
				PolicyFormData: formData,
			},
			"Alert policy could not be updated.",
			validationErr,
			"",
		)
	}

	_, status, errorResponse, updateErr := r.updateAlertPolicyViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		policySlug.String(),
		payload,
	)
	if updateErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Alert policy could not be updated."
		}
		return r.renderAlertsPageWithEditOverrides(
			c,
			orgSlug,
			status,
			views.AlertTabPolicies,
			views.DefaultAlertDestinationFormData(),
			views.DefaultAlertPolicyFormData(),
			views.AlertEditFormOverrides{
				PolicySlug:     policySlug.String(),
				PolicyFormData: formData,
			},
			message,
			backendErrorDetails("update alert policy", status, errorResponse, updateErr),
			"",
		)
	}

	return redirectHTMXOrSeeOther(c, views.AlertJourneyHref(orgSlug.String(), views.AlertTabPolicies, notificationReturnTo(c.FormValue("notification_return"), orgSlug.String()), ""))
}

// handleDeleteAlertPolicySubmit removes an alert policy after slug confirmation.
func (r *Router) handleDeleteAlertPolicySubmit(c fiber.Ctx) error {
	orgSlug, policySlug, err := parseAlertPolicySlugs(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	if !alertDeleteConfirmationMatches(c, "policy_confirmation", policySlug) {
		return r.renderAlertsPage(
			c,
			orgSlug,
			http.StatusBadRequest,
			views.AlertTabPolicies,
			views.DefaultAlertDestinationFormData(),
			views.DefaultAlertPolicyFormData(),
			"Alert policy could not be deleted.",
			"Validation: type the policy slug to confirm deletion.",
			"",
		)
	}

	status, errorResponse, deleteErr := r.deleteAlertPolicyViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		policySlug.String(),
	)
	if deleteErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Alert policy could not be deleted."
		}
		return r.renderAlertsPage(
			c,
			orgSlug,
			status,
			views.AlertTabPolicies,
			views.DefaultAlertDestinationFormData(),
			views.DefaultAlertPolicyFormData(),
			message,
			backendErrorDetails("delete alert policy", status, errorResponse, deleteErr),
			"",
		)
	}

	return redirectHTMXOrSeeOther(c, views.AlertJourneyHref(orgSlug.String(), views.AlertTabPolicies, notificationReturnTo(c.FormValue("notification_return"), orgSlug.String()), ""))
}

// renderAlertsPage loads alert destinations and policies for the management page.
func (r *Router) renderAlertsPage(
	c fiber.Ctx,
	orgSlug slug.Slug,
	statusCode int,
	activeTab views.AlertTab,
	destinationFormData views.AlertDestinationFormData,
	policyFormData views.AlertPolicyFormData,
	contentErrorSummary string,
	contentErrorDetails string,
	contentSuccessSummary string,
) error {
	return r.renderAlertsPageWithEditOverrides(
		c,
		orgSlug,
		statusCode,
		activeTab,
		destinationFormData,
		policyFormData,
		views.AlertEditFormOverrides{},
		contentErrorSummary,
		contentErrorDetails,
		contentSuccessSummary,
	)
}

// renderAlertsPageWithEditOverrides loads alerts and applies sticky failed edit values.
func (r *Router) renderAlertsPageWithEditOverrides(
	c fiber.Ctx,
	orgSlug slug.Slug,
	statusCode int,
	activeTab views.AlertTab,
	destinationFormData views.AlertDestinationFormData,
	policyFormData views.AlertPolicyFormData,
	editFormOverrides views.AlertEditFormOverrides,
	contentErrorSummary string,
	contentErrorDetails string,
	contentSuccessSummary string,
) error {
	r.loadOperationalCapabilities(c, orgSlug.String())

	activeTab = views.AlertTabFromString(string(activeTab))
	permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String())
	policyFormData.NotificationReturn = notificationReturnTo(c.FormValue("notification_return", c.Query("notification_return")), orgSlug.String())

	editFormOverrides.NotificationReturn = policyFormData.NotificationReturn
	if (activeTab == views.AlertTabPolicies && !permissions.Can("alert_policies", "read")) || (activeTab == views.AlertTabDestinations && !permissions.Can("alert_destinations", "read")) {
		if contentErrorSummary == "" {
			contentErrorSummary = "Configuration details are restricted. Each resource requires its own read permission."
		}
	}
	var destinations []api.AlertDestination
	var destinationStatus int
	var destinationErrorResponse *api.ErrorResponse
	var destinationErr error
	if permissions.Can("alert_destinations", "read") {
		if selected := c.Query("destination_slug"); selected != "" {
			destinations, destinationStatus, destinationErrorResponse, destinationErr = r.getAlertDestinationForView(c, orgSlug.String(), selected)
		} else {
			destinations, destinationStatus, destinationErrorResponse, destinationErr = r.listAlertDestinationsViaAPI(
				c.Context(),
				c.Get(fiber.HeaderCookie),
				orgSlug.String(),
				&api.ListAlertDestinationsParams{},
			)
		}
	}
	if destinationErr != nil {
		if destinationStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if contentErrorSummary == "" {
			contentErrorSummary = "Alert destination details are unavailable or access is denied. Recorded delivery evidence is unchanged."
			contentErrorDetails = backendErrorDetails(
				"list alert destinations",
				destinationStatus,
				destinationErrorResponse,
				destinationErr,
			)
		}
		if statusCode < http.StatusBadRequest && destinationStatus >= http.StatusBadRequest {
			statusCode = destinationStatus
		}
		destinations = nil
	}

	var policies []api.AlertPolicy
	var policyStatus int
	var policyErrorResponse *api.ErrorResponse
	var policyErr error
	if permissions.Can("alert_policies", "read") {
		if selected := c.Query("policy_slug"); selected != "" {
			policies, policyStatus, policyErrorResponse, policyErr = r.getAlertPolicyForView(c, orgSlug.String(), selected)
		} else {
			policies, policyStatus, policyErrorResponse, policyErr = r.listAlertPoliciesViaAPI(
				c.Context(),
				c.Get(fiber.HeaderCookie),
				orgSlug.String(),
				&api.ListAlertPoliciesParams{},
			)
		}
	}
	if policyErr != nil {
		if policyStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if activeTab == views.AlertTabPolicies && contentErrorSummary == "" {
			contentErrorSummary = "Alert policy details are unavailable or access is denied. Recorded delivery evidence is unchanged."
			contentErrorDetails = backendErrorDetails("list alert policies", policyStatus, policyErrorResponse, policyErr)
		}
		if activeTab == views.AlertTabPolicies && statusCode < http.StatusBadRequest && policyStatus >= http.StatusBadRequest {
			statusCode = policyStatus
		}
		policies = nil
	}

	var connections []api.NotificationConnection
	if activeTab == views.AlertTabDestinations && permissions.Can("alert_destinations", "read") {
		response, err := r.apiClient.ListNotificationConnectionsWithResponse(c.Context(), orgSlug.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
		switch {
		case err == nil && response != nil && response.JSON200 != nil:
			connections = *response.JSON200
		case response != nil && response.StatusCode() == http.StatusUnauthorized:
			return redirectToLogin(c, c.OriginalURL())
		default:
			if contentErrorSummary == "" {
				contentErrorSummary = "Notification connections are temporarily unavailable."
			}
			if statusCode < http.StatusBadRequest {
				statusCode = http.StatusServiceUnavailable
				if response != nil && response.StatusCode() >= http.StatusBadRequest {
					statusCode = response.StatusCode()
				}
			}
		}
	}
	shellData := r.buildAlertsShellData(c, orgSlug.String(), "", "")

	setHTMLResponseStatus(c, statusCode)
	return renderTempl(
		c,
		views.AlertsPage(
			shellData,
			orgSlug.String(),
			activeTab,
			destinations,
			policies,
			connections,
			destinationFormData,
			policyFormData,
			editFormOverrides,
			permissions,
			contentErrorSummary,
			contentErrorDetails,
			contentSuccessSummary,
		),
	)
}

// buildAlertsShellData prepares shared shell chrome for the alerts management page.
func (r *Router) buildAlertsShellData(
	c fiber.Ctx,
	orgSlug string,
	pageError string,
	pageErrorDetails string,
) views.AuthenticatedShellData {
	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Alerts",
		orgSlug,
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	// The workspace owns this link so it survives HTMX tab swaps.
	shellData.NotificationReturnHref = ""
	shellData.Subtitle = "Configure delivery policy for selected notifications."
	return shellData
}

// alertDestinationFormDataFromRequest reads alert destination form fields.
func alertDestinationFormDataFromRequest(c fiber.Ctx) views.AlertDestinationFormData {
	return views.AlertDestinationFormData{
		Name:           strings.TrimSpace(c.FormValue("name")),
		Slug:           strings.TrimSpace(c.FormValue("slug")),
		Type:           strings.TrimSpace(c.FormValue("type")),
		To:             strings.TrimSpace(c.FormValue("to")),
		ConnectionSlug: strings.TrimSpace(c.FormValue("connection_slug")),
		Enabled:        formCheckboxEnabled(c, "enabled"),
	}
}

// alertPolicyFormDataFromRequest reads alert policy form fields and repeated destination bindings.
func alertPolicyFormDataFromRequest(c fiber.Ctx) views.AlertPolicyFormData {
	return views.AlertPolicyFormData{
		NotificationReturn: notificationReturnTo(c.FormValue("notification_return"), c.Params("slug")),
		PolicyKind:         strings.TrimSpace(c.FormValue("policy_kind")),
		Name:               strings.TrimSpace(c.FormValue("name")),
		Slug:               strings.TrimSpace(c.FormValue("slug")),
		Scope:              strings.TrimSpace(c.FormValue("scope")),
		ProjectSlug:        strings.TrimSpace(c.FormValue("project_slug")),
		EnvironmentSlug:    strings.TrimSpace(c.FormValue("environment_slug")),
		NotificationType:   strings.TrimSpace(c.FormValue("notification_type")),
		Severity:           strings.TrimSpace(c.FormValue("severity")),
		LifecycleState:     strings.TrimSpace(c.FormValue("lifecycle_state")),
		SourceKind:         strings.TrimSpace(c.FormValue("source_kind")),
		SourceAction:       strings.TrimSpace(c.FormValue("source_action")),
		SourceRef:          strings.TrimSpace(c.FormValue("source_ref")),
		DestinationSlugs:   repeatedFormValues(c, "destination_slug"),
		Enabled:            formCheckboxEnabled(c, "enabled"),
	}
}

// alertCreateDestinationPayloadFromForm converts a create-destination form to an API payload.
func alertCreateDestinationPayloadFromForm(
	formData views.AlertDestinationFormData,
) (api.CreateAlertDestinationJSONRequestBody, string) {
	destinationSlug, validationErr := optionalAPISlug(formData.Slug, "destination")
	if validationErr != "" {
		return api.CreateAlertDestinationJSONRequestBody{}, validationErr
	}

	payload, validationErr, _ := alertDestinationPayloadFieldsFromForm(formData)
	if validationErr != "" {
		return api.CreateAlertDestinationJSONRequestBody{}, validationErr
	}
	return api.CreateAlertDestinationJSONRequestBody{
		Name:            payload.Name,
		Slug:            destinationSlug,
		Type:            payload.Type,
		NonSecretConfig: payload.NonSecretConfig,
	}, ""
}

// alertUpdateDestinationPayloadFromForm converts an update-destination form to an API payload.
func alertUpdateDestinationPayloadFromForm(
	formData views.AlertDestinationFormData,
) (api.UpdateAlertDestinationJSONRequestBody, string, string) {
	payload, validationErr, invalidField := alertDestinationPayloadFieldsFromForm(formData)
	if validationErr != "" {
		return api.UpdateAlertDestinationJSONRequestBody{}, validationErr, invalidField
	}
	return api.UpdateAlertDestinationJSONRequestBody{
		Name:            payload.Name,
		Type:            payload.Type,
		NonSecretConfig: payload.NonSecretConfig,
		Enabled:         formData.Enabled,
	}, "", ""
}

type alertDestinationPayloadFields struct {
	Name            string
	Type            api.AlertDestinationType
	NonSecretConfig api.AlertDestinationConfig
}

// alertDestinationPayloadFieldsFromForm normalizes shared destination payload fields.
func alertDestinationPayloadFieldsFromForm(formData views.AlertDestinationFormData) (alertDestinationPayloadFields, string, string) {
	name := strings.TrimSpace(formData.Name)
	if name == "" {
		return alertDestinationPayloadFields{}, "Validation: destination name is required.", "name"
	}

	destinationType := api.AlertDestinationType(strings.TrimSpace(formData.Type))
	if destinationType == "" {
		destinationType = api.AlertDestinationTypeEmail
	}
	if !destinationType.Valid() || destinationType == api.AlertDestinationTypeUnknown {
		return alertDestinationPayloadFields{}, "Validation: destination type is invalid.", "type"
	}

	config := map[string]interface{}{}
	if destinationType == api.AlertDestinationTypeSlack {
		connection, err := slug.New(formData.ConnectionSlug)
		if err != nil {
			return alertDestinationPayloadFields{}, "Validation: select a managed Slack connection.", "connection_slug"
		}
		config["connection_slug"] = connection.String()
	} else if recipients := alertDestinationRecipientsFromForm(formData.To); len(recipients) > 0 {
		config["recipients"] = recipients
	}
	nonSecretConfig, validationErr := alertDestinationConfigFromMap(config)
	if validationErr != "" {
		return alertDestinationPayloadFields{}, validationErr, ""
	}

	return alertDestinationPayloadFields{
		Name:            name,
		Type:            destinationType,
		NonSecretConfig: nonSecretConfig,
	}, "", ""
}

// alertDestinationConfigFromMap wraps non-secret config in the generated OpenAPI union.
func alertDestinationConfigFromMap(config map[string]interface{}) (api.AlertDestinationConfig, string) {
	raw, err := json.Marshal(config)
	if err != nil {
		return api.AlertDestinationConfig{}, "Validation: destination config is invalid."
	}
	var ret api.AlertDestinationConfig
	if err := ret.UnmarshalJSON(raw); err != nil {
		return api.AlertDestinationConfig{}, "Validation: destination config is invalid."
	}
	return ret, ""
}

// alertDestinationRecipientsFromForm parses the comma-separated multi-email form value.
func alertDestinationRecipientsFromForm(value string) []string {
	fields := strings.Split(value, ",")
	recipients := make([]string, 0, len(fields))
	for _, field := range fields {
		if recipient := strings.TrimSpace(field); recipient != "" {
			recipients = append(recipients, recipient)
		}
	}
	return recipients
}

// alertCreatePolicyPayloadFromForm converts a create-policy form to an API payload.
func alertCreatePolicyPayloadFromForm(formData views.AlertPolicyFormData) (api.CreateAlertPolicyJSONRequestBody, string) {
	policySlug, validationErr := optionalAPISlug(formData.Slug, "policy")
	if validationErr != "" {
		return api.CreateAlertPolicyJSONRequestBody{}, validationErr
	}

	payload, validationErr := alertPolicyPayloadFieldsFromForm(formData)
	if validationErr != "" {
		return api.CreateAlertPolicyJSONRequestBody{}, validationErr
	}
	return api.CreateAlertPolicyJSONRequestBody{
		Name:             payload.Name,
		Slug:             policySlug,
		Scope:            payload.Scope,
		ProjectSlug:      payload.ProjectSlug,
		EnvironmentSlug:  payload.EnvironmentSlug,
		NotificationType: payload.NotificationType,
		Severity:         payload.Severity,
		LifecycleState:   payload.LifecycleState,
		SourceKind:       payload.SourceKind,
		SourceAction:     payload.SourceAction,
		SourceRef:        payload.SourceRef,
		DestinationSlugs: payload.DestinationSlugs,
	}, ""
}

// alertUpdatePolicyPayloadFromForm converts an update-policy form to an API payload.
func alertUpdatePolicyPayloadFromForm(formData views.AlertPolicyFormData) (api.UpdateAlertPolicyJSONRequestBody, string) {
	payload, validationErr := alertPolicyPayloadFieldsFromForm(formData)
	if validationErr != "" {
		return api.UpdateAlertPolicyJSONRequestBody{}, validationErr
	}
	return api.UpdateAlertPolicyJSONRequestBody{
		Name:             payload.Name,
		Scope:            payload.Scope,
		ProjectSlug:      payload.ProjectSlug,
		EnvironmentSlug:  payload.EnvironmentSlug,
		NotificationType: payload.NotificationType,
		Severity:         payload.Severity,
		LifecycleState:   payload.LifecycleState,
		SourceKind:       payload.SourceKind,
		SourceAction:     payload.SourceAction,
		SourceRef:        payload.SourceRef,
		DestinationSlugs: payload.DestinationSlugs,
		Enabled:          formData.Enabled,
	}, ""
}

type alertPolicyPayloadFields struct {
	Name             string
	Scope            api.AlertPolicyScope
	ProjectSlug      *api.NullableSlug
	EnvironmentSlug  *api.NullableSlug
	NotificationType *api.NotificationType
	Severity         *api.NotificationSeverity
	LifecycleState   *api.NotificationLifecycleState
	SourceKind       *api.AlertPolicySourceKind
	SourceAction     *api.AlertPolicySourceAction
	SourceRef        *string
	DestinationSlugs []api.Slug
}

// alertPolicyPayloadFieldsFromForm normalizes shared policy payload fields.
func alertPolicyPayloadFieldsFromForm(formData views.AlertPolicyFormData) (alertPolicyPayloadFields, string) {
	name := strings.TrimSpace(formData.Name)
	if name == "" {
		return alertPolicyPayloadFields{}, "Validation: policy name is required."
	}

	scope := api.AlertPolicyScope(strings.TrimSpace(formData.Scope))
	if scope == "" {
		scope = api.AlertPolicyScopeOrganization
	}
	if !scope.Valid() || scope == api.AlertPolicyScopeUnknown {
		return alertPolicyPayloadFields{}, "Validation: policy scope is invalid."
	}

	projectSlug, validationErr := optionalAPISlug(formData.ProjectSlug, "project")
	if validationErr != "" {
		return alertPolicyPayloadFields{}, validationErr
	}
	environmentSlug, validationErr := optionalAPISlug(formData.EnvironmentSlug, "environment")
	if validationErr != "" {
		return alertPolicyPayloadFields{}, validationErr
	}

	notificationType, validationErr := alertPolicyNotificationTypeFromForm(formData.NotificationType)
	if validationErr != "" {
		return alertPolicyPayloadFields{}, validationErr
	}
	severity, validationErr := alertPolicySeverityFromForm(formData.Severity)
	if validationErr != "" {
		return alertPolicyPayloadFields{}, validationErr
	}
	lifecycleState, validationErr := alertPolicyLifecycleStateFromForm(formData.LifecycleState)
	if validationErr != "" {
		return alertPolicyPayloadFields{}, validationErr
	}

	sourceKind, validationErr := alertPolicySourceKindFromForm(formData.SourceKind)
	if validationErr != "" {
		return alertPolicyPayloadFields{}, validationErr
	}
	sourceAction, validationErr := alertPolicySourceActionFromForm(formData.SourceAction)
	if validationErr != "" {
		return alertPolicyPayloadFields{}, validationErr
	}
	sourceRef, validationErr := optionalAPISlug(formData.SourceRef, "source ref")
	if validationErr != "" {
		return alertPolicyPayloadFields{}, validationErr
	}
	if sourceAction != nil && sourceKind == nil {
		return alertPolicyPayloadFields{}, "Validation: choose a source kind before choosing a source action."
	}
	if sourceRef != nil && sourceKind == nil {
		return alertPolicyPayloadFields{}, "Validation: choose a source kind before entering a source ref."
	}

	destinationSlugs, validationErr := alertDestinationSlugsFromForm(formData.DestinationSlugs)
	if validationErr != "" {
		return alertPolicyPayloadFields{}, validationErr
	}

	return alertPolicyPayloadFields{
		Name:             name,
		Scope:            scope,
		ProjectSlug:      projectSlug,
		EnvironmentSlug:  environmentSlug,
		NotificationType: notificationType,
		Severity:         severity,
		LifecycleState:   lifecycleState,
		SourceKind:       sourceKind,
		SourceAction:     sourceAction,
		SourceRef:        sourceRef,
		DestinationSlugs: destinationSlugs,
	}, ""
}

// alertPolicyNotificationTypeFromForm validates an optional notification type selector.
func alertPolicyNotificationTypeFromForm(raw string) (*api.NotificationType, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, ""
	}
	value := api.NotificationType(trimmed)
	if !value.Valid() || value == api.NotificationTypeUnknown {
		return nil, "Validation: notification type is invalid."
	}
	return &value, ""
}

// alertPolicySeverityFromForm validates an optional severity selector.
func alertPolicySeverityFromForm(raw string) (*api.NotificationSeverity, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, ""
	}
	value := api.NotificationSeverity(trimmed)
	if !value.Valid() || value == api.NotificationSeverityUnknown {
		return nil, "Validation: severity is invalid."
	}
	return &value, ""
}

// alertPolicyLifecycleStateFromForm validates an optional lifecycle selector.
func alertPolicyLifecycleStateFromForm(raw string) (*api.NotificationLifecycleState, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, ""
	}
	value := api.NotificationLifecycleState(trimmed)
	if !value.Valid() || value == api.NotificationLifecycleStateUnknown {
		return nil, "Validation: lifecycle state is invalid."
	}
	return &value, ""
}

// alertPolicySourceKindFromForm validates an optional alert source selector.
func alertPolicySourceKindFromForm(raw string) (*api.AlertPolicySourceKind, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, ""
	}
	sourceKind := api.AlertPolicySourceKind(trimmed)
	if !sourceKind.Valid() || sourceKind == api.EventEntityTypeUnknown {
		return nil, "Validation: source kind is invalid."
	}
	return &sourceKind, ""
}

// alertPolicySourceActionFromForm validates an optional alert source action selector.
func alertPolicySourceActionFromForm(raw string) (*api.AlertPolicySourceAction, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, ""
	}
	sourceAction := api.AlertPolicySourceAction(trimmed)
	if !sourceAction.Valid() || sourceAction == api.EventActionUnknown {
		return nil, "Validation: source action is invalid."
	}
	return &sourceAction, ""
}

// alertDestinationSlugsFromForm validates repeated destination slug form values.
func alertDestinationSlugsFromForm(rawValues []string) ([]api.Slug, string) {
	destinationSlugs := make([]api.Slug, 0, len(rawValues))
	for _, raw := range rawValues {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		value, err := slug.New(trimmed)
		if err != nil {
			return nil, "Validation: destination slugs must use lowercase letters, digits, and hyphens only."
		}
		destinationSlugs = append(destinationSlugs, value.String())
	}
	if len(destinationSlugs) == 0 {
		return nil, "Validation: choose at least one alert destination."
	}
	return destinationSlugs, ""
}

// optionalAPISlug validates an optional slug form value for API payloads.
func optionalAPISlug(raw, label string) (*api.NullableSlug, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, ""
	}
	value, err := slug.New(trimmed)
	if err != nil {
		return nil, "Validation: " + label + " slug must use lowercase letters, digits, and hyphens only."
	}
	apiValue := value.String()
	return &apiValue, ""
}

// formCheckboxEnabled reports whether a submitted checkbox value is on.
func formCheckboxEnabled(c fiber.Ctx, name string) bool {
	switch strings.ToLower(strings.TrimSpace(c.FormValue(name))) {
	case "on", booleanTrueString, "1", "yes":
		return true
	default:
		return false
	}
}

// alertDeleteConfirmationMatches checks typed destructive alert confirmation fields.
func alertDeleteConfirmationMatches(c fiber.Ctx, field string, value slug.Slug) bool {
	return strings.TrimSpace(c.FormValue(field)) == value.String()
}

// alertDestinationTestSuccessSummary formats safe queue metadata for the operator result banner.
func alertDestinationTestSuccessSummary(result *api.AlertDestinationTestResponse) string {
	if result == nil {
		return "Alert destination test accepted."
	}

	recipientLabel := "recipients"
	if result.RecipientCount == 1 {
		recipientLabel = "recipient"
	}
	jobCount := len(result.EnqueuedJobs)
	jobLabel := "delivery jobs"
	if jobCount == 1 {
		jobLabel = "delivery job"
	}

	return fmt.Sprintf(
		"Alert destination test accepted for %d %s and %d %s.",
		result.RecipientCount,
		recipientLabel,
		jobCount,
		jobLabel,
	)
}

// parseAlertDestinationSlugs validates organization and destination slugs from route params.
func parseAlertDestinationSlugs(c fiber.Ctx) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return "", "", xerror.InvalidSlugFormat().Detail("invalid organization slug")
	}

	destinationSlug, err := slug.New(c.Params("destinationSlug"))
	if err != nil {
		return "", "", xerror.InvalidSlugFormat().Detail("invalid alert destination slug")
	}

	return orgSlug, destinationSlug, nil
}

// parseAlertPolicySlugs validates organization and policy slugs from route params.
func parseAlertPolicySlugs(c fiber.Ctx) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return "", "", xerror.InvalidSlugFormat().Detail("invalid organization slug")
	}

	policySlug, err := slug.New(c.Params("policySlug"))
	if err != nil {
		return "", "", xerror.InvalidSlugFormat().Detail("invalid alert policy slug")
	}

	return orgSlug, policySlug, nil
}

// listAlertDestinationsViaAPI lists alert destinations through the backend API client.
func (r *Router) listAlertDestinationsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	params *api.ListAlertDestinationsParams,
) ([]api.AlertDestination, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListAlertDestinationsWithResponse(
		ctx,
		organizationSlug,
		params,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200.AlertDestinations, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"list alert destinations api returned status %d",
		resp.StatusCode(),
	)
}

// createAlertDestinationViaAPI creates an alert destination through the backend API client.
func (r *Router) createAlertDestinationViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	body api.CreateAlertDestinationJSONRequestBody,
) (*api.AlertDestination, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.CreateAlertDestinationWithResponse(
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
		"create alert destination api returned status %d",
		resp.StatusCode(),
	)
}

// updateAlertDestinationViaAPI updates an alert destination through the backend API client.
func (r *Router) updateAlertDestinationViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	destinationSlug string,
	body api.UpdateAlertDestinationJSONRequestBody,
) (*api.AlertDestination, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpdateAlertDestinationWithResponse(
		ctx,
		organizationSlug,
		destinationSlug,
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
		"update alert destination api returned status %d",
		resp.StatusCode(),
	)
}

// testAlertDestinationViaAPI enqueues a destination test through the backend API client.
func (r *Router) testAlertDestinationViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	destinationSlug string,
) (*api.AlertDestinationTestResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.TestAlertDestinationWithResponse(
		ctx,
		organizationSlug,
		destinationSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusAccepted && resp.JSON202 != nil {
		return resp.JSON202, http.StatusAccepted, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"test alert destination api returned status %d",
		resp.StatusCode(),
	)
}

// deleteAlertDestinationViaAPI deletes an alert destination through the backend API client.
func (r *Router) deleteAlertDestinationViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	destinationSlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.DeleteAlertDestinationWithResponse(
		ctx,
		organizationSlug,
		destinationSlug,
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
		"delete alert destination api returned status %d",
		resp.StatusCode(),
	)
}

// alertPolicyProjectOptionsViaAPI loads project choices for alert policy scope selectors.
func (r *Router) alertPolicyProjectOptionsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
) ([]views.AlertFormOption, int, *api.ErrorResponse, error) {
	projects, status, errorResponse, err := r.listAllProjectsForAlertPolicySelectorsViaAPI(ctx, cookieHeader, organizationSlug)
	if err != nil {
		return nil, status, errorResponse, err
	}
	return alertPolicyProjectOptions(projects), http.StatusOK, nil, nil
}

// listAllProjectsForAlertPolicySelectorsViaAPI loads project choices for scoped policy selectors.
func (r *Router) listAllProjectsForAlertPolicySelectorsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
) ([]api.Project, int, *api.ErrorResponse, error) {
	limit := api.LimitParam(100)
	sortBy := api.ListProjectsParamsSortByProjectName
	order := api.ListProjectsParamsOrderAsc
	direction := api.ListProjectsParamsDirectionNext
	params := &api.ListProjectsParams{
		Limit:  &limit,
		SortBy: &sortBy,
		Order:  &order,
	}
	projects := []api.Project{}
	seenCursors := map[string]struct{}{}

	for {
		resp, err := r.apiClient.ListProjectsWithResponse(
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
				"list projects api returned status %d",
				resp.StatusCode(),
			)
		}

		projects = append(projects, resp.JSON200.Projects...)
		if !resp.JSON200.HasMore || resp.JSON200.NextCursor == nil {
			return projects, http.StatusOK, nil, nil
		}
		nextCursor := strings.TrimSpace(*resp.JSON200.NextCursor)
		if nextCursor == "" {
			return projects, http.StatusOK, nil, nil
		}
		if _, exists := seenCursors[nextCursor]; exists {
			return nil, http.StatusInternalServerError, nil, xerror.Unexpected().
				Detailf("list projects pagination returned repeated cursor %q", nextCursor)
		}
		seenCursors[nextCursor] = struct{}{}

		params = &api.ListProjectsParams{
			Limit:     &limit,
			Cursor:    &nextCursor,
			SortBy:    &sortBy,
			Order:     &order,
			Direction: &direction,
		}
	}
}

// alertPolicyEnvironmentOptionsViaAPI loads project-enabled environments for alert policy scope selectors.
func (r *Router) alertPolicyEnvironmentOptionsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
) ([]views.AlertFormOption, int, *api.ErrorResponse, error) {
	projectEnvironments, status, errorResponse, err := r.listProjectEnvironmentsViaAPI(
		ctx,
		cookieHeader,
		organizationSlug,
		projectSlug,
	)
	if err != nil {
		return nil, status, errorResponse, err
	}
	return alertPolicyEnvironmentOptions(projectEnvironments.ProjectEnvironments), http.StatusOK, nil, nil
}

// alertPolicyProjectOptions maps API projects to sorted dropdown options.
func alertPolicyProjectOptions(projects []api.Project) []views.AlertFormOption {
	options := make([]views.AlertFormOption, 0, len(projects))
	for _, project := range projects {
		value := strings.TrimSpace(project.Slug)
		if value == "" {
			continue
		}
		label := alertPolicyOptionLabel(strings.TrimSpace(project.Name), value)
		options = append(options, views.AlertFormOption{Value: value, Label: label})
	}
	sortAlertPolicyFormOptions(options)
	return options
}

// alertPolicyEnvironmentOptions maps API project environments to sorted dropdown options.
func alertPolicyEnvironmentOptions(environments []api.ProjectEnvironment) []views.AlertFormOption {
	options := make([]views.AlertFormOption, 0, len(environments))
	for _, environment := range environments {
		value := strings.TrimSpace(environment.EnvironmentSlug)
		if value == "" {
			continue
		}
		label := alertPolicyOptionLabel(strings.TrimSpace(environment.EnvironmentName), value)
		if environment.IsDefault {
			label += " (default)"
		}
		options = append(options, views.AlertFormOption{Value: value, Label: label})
	}
	sortAlertPolicyFormOptions(options)
	return options
}

// alertPolicySourceMetadataViaAPI loads source selector metadata and scoped refs in one API call.
func (r *Router) alertPolicySourceMetadataViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	formData views.AlertPolicyFormData,
) ([]api.AlertPolicySourceMetadata, []views.AlertFormOption, int, *api.ErrorResponse, error) {
	scope := api.AlertPolicyScope(strings.TrimSpace(formData.Scope))
	if scope == "" {
		scope = api.AlertPolicyScopeOrganization
	}
	params := &api.ListAlertPolicySourceMetadataParams{
		Scope: &scope,
	}

	if projectSlug := strings.TrimSpace(formData.ProjectSlug); projectSlug != "" {
		value := projectSlug
		params.ProjectSlug = &value
	}
	if environmentSlug := strings.TrimSpace(formData.EnvironmentSlug); environmentSlug != "" {
		value := environmentSlug
		params.EnvironmentSlug = &value
	}
	if sourceKind := api.EventEntityType(strings.TrimSpace(formData.SourceKind)); sourceKind != "" &&
		sourceKind != api.EventEntityTypeUnknown && sourceKind.Valid() {
		params.SourceKind = &sourceKind
	}
	if sourceAction := api.EventAction(strings.TrimSpace(formData.SourceAction)); sourceAction != "" &&
		sourceAction != api.EventActionUnknown && sourceAction.Valid() {
		params.SourceAction = &sourceAction
	}

	resp, err := r.apiClient.ListAlertPolicySourceMetadataWithResponse(
		ctx,
		organizationSlug,
		params,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200.Sources,
			alertPolicySourceRefOptionsFromAPI(resp.JSON200.SourceRefs),
			http.StatusOK,
			nil,
			nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"list alert policy source metadata api returned status %d",
		resp.StatusCode(),
	)
}

// alertPolicySourceRefOptionsFromAPI maps API metadata refs to sorted selector options.
func alertPolicySourceRefOptionsFromAPI(refs []api.AlertPolicySourceRefOption) []views.AlertFormOption {
	options := make([]views.AlertFormOption, 0, len(refs))
	for _, ref := range refs {
		value := strings.TrimSpace(ref.Value)
		if value == "" {
			continue
		}
		label := strings.TrimSpace(ref.Label)
		if label == "" {
			label = value
		}
		options = append(options, views.AlertFormOption{Value: value, Label: label})
	}
	sortAlertPolicyFormOptions(options)
	return options
}

// alertPolicyOptionLabel formats a display name without losing the submitted slug.
func alertPolicyOptionLabel(name, slugValue string) string {
	if name == "" || name == slugValue {
		return slugValue
	}
	return fmt.Sprintf("%s (%s)", name, slugValue)
}

// alertPolicyFormOptionsContain reports whether a dropdown option exists for a value.
func alertPolicyFormOptionsContain(options []views.AlertFormOption, value string) bool {
	value = strings.TrimSpace(value)
	for _, option := range options {
		if strings.TrimSpace(option.Value) == value {
			return true
		}
	}
	return false
}

// sortAlertPolicyFormOptions orders alert selector options by label.
func sortAlertPolicyFormOptions(options []views.AlertFormOption) {
	sort.SliceStable(options, func(i, j int) bool {
		return strings.ToLower(options[i].Label) < strings.ToLower(options[j].Label)
	})
}

// listAlertPoliciesViaAPI lists alert policies through the backend API client.
func (r *Router) listAlertPoliciesViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	params *api.ListAlertPoliciesParams,
) ([]api.AlertPolicy, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListAlertPoliciesWithResponse(
		ctx,
		organizationSlug,
		params,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200.AlertPolicies, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"list alert policies api returned status %d",
		resp.StatusCode(),
	)
}

// createAlertPolicyViaAPI creates an alert policy through the backend API client.
func (r *Router) createAlertPolicyViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	body api.CreateAlertPolicyJSONRequestBody,
) (*api.AlertPolicy, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.CreateAlertPolicyWithResponse(
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
		"create alert policy api returned status %d",
		resp.StatusCode(),
	)
}

// updateAlertPolicyViaAPI updates an alert policy through the backend API client.
func (r *Router) updateAlertPolicyViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	policySlug string,
	body api.UpdateAlertPolicyJSONRequestBody,
) (*api.AlertPolicy, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpdateAlertPolicyWithResponse(
		ctx,
		organizationSlug,
		policySlug,
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
		"update alert policy api returned status %d",
		resp.StatusCode(),
	)
}

// deleteAlertPolicyViaAPI deletes an alert policy through the backend API client.
func (r *Router) deleteAlertPolicyViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	policySlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.DeleteAlertPolicyWithResponse(
		ctx,
		organizationSlug,
		policySlug,
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
		"delete alert policy api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) getAlertPolicyForView(c fiber.Ctx, org, selected string) ([]api.AlertPolicy, int, *api.ErrorResponse, error) {
	response, err := r.apiClient.GetAlertPolicyWithResponse(c.Context(), org, selected, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return nil, http.StatusServiceUnavailable, nil, xerror.Unexpected().With(err)
	}
	if response.JSON200 != nil {
		return []api.AlertPolicy{*response.JSON200}, http.StatusOK, nil, nil
	}
	return nil, response.StatusCode(), parseAPIErrorResponse(response.Body), xerror.Unexpected().Detail("alert policy unavailable")
}

func (r *Router) getAlertDestinationForView(c fiber.Ctx, org, selected string) ([]api.AlertDestination, int, *api.ErrorResponse, error) {
	response, err := r.apiClient.GetAlertDestinationWithResponse(c.Context(), org, selected, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return nil, http.StatusServiceUnavailable, nil, xerror.Unexpected().With(err)
	}
	if response.JSON200 != nil {
		return []api.AlertDestination{*response.JSON200}, http.StatusOK, nil, nil
	}
	return nil, response.StatusCode(), parseAPIErrorResponse(response.Body), xerror.Unexpected().Detail("alert destination unavailable")
}

func alertSetupReturnTo(raw, org string) string {
	parsed, err := url.Parse(safeReturnTo(raw))
	if err != nil || parsed.Path != "/orgs/"+org+"/alerts" {
		return ""
	}
	return parsed.String()
}

func alertPolicySetupFormFromReturn(raw, org string) views.AlertPolicyFormData {
	form := views.DefaultAlertPolicyFormData()
	setup := alertSetupReturnTo(raw, org)
	if setup == "" {
		return form
	}
	parsed, _ := url.Parse(setup)
	values := parsed.Query()
	if condition := values.Get("notification_type"); views.NotificationTypeAvailable(condition) {
		form.NotificationType = condition
	}
	if project, err := slug.New(values.Get("project_slug")); err == nil {
		form.Scope, form.ProjectSlug = string(api.AlertPolicyScopeProject), project.String()
		if environment, err := slug.New(values.Get("environment_slug")); err == nil {
			form.Scope, form.EnvironmentSlug = string(api.AlertPolicyScopeProjectEnvironment), environment.String()
		}
	}
	form.NotificationReturn = notificationReturnTo(values.Get("notification_return"), org)
	return form
}
