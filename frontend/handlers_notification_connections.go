package frontend

import (
	"net/http"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (r *Router) handleNotificationConnectionsPage(c fiber.Ctx) error {
	return r.renderNotificationConnections(c, http.StatusOK, "", false)
}

func (r *Router) renderNotificationConnections(c fiber.Ctx, status int, message string, failed bool) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid organization slug")
	}
	response, err := r.apiClient.ListNotificationConnectionsWithResponse(c.Context(), org.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	connections := []api.NotificationConnection{}
	if err != nil || response.JSON200 == nil {
		if response != nil && response.StatusCode() == http.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if message == "" {
			message = "Notification connections could not be loaded."
		}
		if status < http.StatusBadRequest {
			status = http.StatusServiceUnavailable
			if response != nil && response.StatusCode() >= http.StatusBadRequest {
				status = response.StatusCode()
			}
		}
		failed = true
	} else {
		connections = *response.JSON200
	}
	if message == "" {
		switch c.Query("provider_oauth") {
		case "success":
			message = "Slack connected. Configure a destination to route notifications."
		case "denied":
			message, failed = "Slack installation was canceled.", true
		case "expired":
			message, failed = "Slack setup expired. Start again.", true
		case "error":
			message, failed = "Slack could not be connected. Start again.", true
		}
	}
	permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), org.String())
	setHTMLResponseStatus(c, status)
	return renderTempl(c, views.NotificationConnectionsPage(r.buildAlertsShellData(c, org.String(), "", ""), org.String(), connections, permissions, message, failed, views.NotificationConnectionFormData{Name: strings.TrimSpace(c.FormValue("name")), Slug: strings.TrimSpace(c.FormValue("slug"))}))
}

func (r *Router) handleCreateNotificationConnection(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid organization slug")
	}
	connection, validationErr := optionalAPISlug(c.FormValue("slug"), "connection")
	if validationErr != "" {
		return r.renderNotificationConnections(c, http.StatusBadRequest, "Enter a valid connection slug.", true)
	}
	response, err := r.apiClient.CreateNotificationConnectionWithResponse(c.Context(), org.String(), api.NewNotificationConnection{Provider: api.NotificationProvider(c.FormValue("provider")), Slug: connection, Name: strings.TrimSpace(c.FormValue("name"))}, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return r.renderNotificationConnections(c, http.StatusServiceUnavailable, "Connection could not be created.", true)
	}
	if response.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if response.JSON201 == nil {
		return r.renderNotificationConnections(c, response.StatusCode(), formFeedbackFromErrorResponse(parseAPIErrorResponse(response.Body), "Connection could not be created."), true)
	}
	return redirectHTMXOrSeeOther(c, "/orgs/"+org.String()+"/alerts/connections")
}

func (r *Router) handleConnectNotificationConnection(c fiber.Ctx) error {
	org, connection, err := notificationConnectionRouteSlugs(c)
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid connection identity")
	}
	response, err := r.apiClient.ConnectNotificationConnectionWithResponse(c.Context(), org.String(), connection.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return r.renderNotificationConnections(c, http.StatusServiceUnavailable, "Provider setup could not be started.", true)
	}
	if response.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if response.JSON201 == nil {
		return r.renderNotificationConnections(c, response.StatusCode(), formFeedbackFromErrorResponse(parseAPIErrorResponse(response.Body), "Provider setup could not be started."), true)
	}
	return redirectHTMXOrSeeOther(c, response.JSON201.AuthorizationUrl)
}

func (r *Router) handleDisconnectNotificationConnection(c fiber.Ctx) error {
	org, connection, err := notificationConnectionRouteSlugs(c)
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("invalid connection identity")
	}
	response, err := r.apiClient.DisconnectNotificationConnectionWithResponse(c.Context(), org.String(), connection.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return r.renderNotificationConnections(c, http.StatusServiceUnavailable, "Connection could not be disconnected.", true)
	}
	if response.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if response.JSON200 == nil {
		return r.renderNotificationConnections(c, response.StatusCode(), formFeedbackFromErrorResponse(parseAPIErrorResponse(response.Body), "Connection could not be disconnected."), true)
	}
	return r.renderNotificationConnections(c, http.StatusOK, "Connection disconnected in Braidplane.", false)
}

func notificationConnectionRouteSlugs(c fiber.Ctx) (slug.Slug, slug.Slug, error) {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return "", "", err
	}
	connection, err := slug.New(c.Params("notification_connection_slug"))
	return org, connection, err
}
