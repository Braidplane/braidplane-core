package frontend

import (
	"context"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (r *Router) handleNotificationsPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	return r.renderNotificationsPage(c, orgSlug, notificationFiltersFromQuery(c), http.StatusOK, "", "")
}

// handleAcknowledgeNotificationSubmit marks a notification as seen and returns to the current list.
func (r *Router) handleAcknowledgeNotificationSubmit(c fiber.Ctx) error {
	return r.handleNotificationLifecycleSubmit(c, "acknowledge")
}

// handleResolveNotificationSubmit closes a notification and returns to the current list.
func (r *Router) handleResolveNotificationSubmit(c fiber.Ctx) error {
	return r.handleNotificationLifecycleSubmit(c, "resolve")
}

// handleNotificationLifecycleSubmit runs one notification lifecycle action through the backend API.
func (r *Router) handleNotificationLifecycleSubmit(c fiber.Ctx, action string) error {
	orgSlug, notificationSlug, err := parseNotificationActionSlugs(c)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}
	filters := notificationFiltersFromForm(c)

	var status int
	var errorResponse *api.ErrorResponse
	var actionErr error
	switch action {
	case "acknowledge":
		status, errorResponse, actionErr = r.acknowledgeNotificationViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			notificationSlug.String(),
		)
	case "resolve":
		status, errorResponse, actionErr = r.resolveNotificationViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			notificationSlug.String(),
		)
	default:
		return c.Status(fiber.StatusBadRequest).SendString("invalid notification action")
	}
	if actionErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Notification could not be updated."
		}
		return r.renderNotificationsPage(
			c,
			orgSlug,
			filters,
			status,
			message,
			backendErrorDetails(action+" notification", status, errorResponse, actionErr),
		)
	}

	return redirectHTMXOrSeeOther(c, views.NotificationsListHref(orgSlug.String(), filters.Cursor, filters.Direction, filters))
}

// notificationFiltersFromQuery reads the Notifications list filters from a GET request.
func notificationFiltersFromQuery(c fiber.Ctx) views.NotificationFilters {
	return views.NormalizeNotificationFilters(views.NotificationFilters{
		State:            strings.TrimSpace(c.Query("state")),
		Cursor:           strings.Clone(c.Query("cursor")),
		Direction:        strings.Clone(c.Query("direction")),
		NotificationType: strings.TrimSpace(c.Query("notification_type")),
		Severity:         strings.TrimSpace(c.Query("severity")),
	})
}

// notificationFiltersFromForm reads the Notifications list filters preserved by lifecycle forms.
func notificationFiltersFromForm(c fiber.Ctx) views.NotificationFilters {
	return views.NormalizeNotificationFilters(views.NotificationFilters{
		State:            strings.TrimSpace(c.FormValue("state")),
		Cursor:           strings.Clone(c.FormValue("cursor")),
		Direction:        strings.Clone(c.FormValue("direction")),
		NotificationType: strings.TrimSpace(c.FormValue("notification_type")),
		Severity:         strings.TrimSpace(c.FormValue("severity")),
	})
}

// parseNotificationActionSlugs validates notification action route parameters before API calls.
func parseNotificationActionSlugs(c fiber.Ctx) (slug.Slug, slug.Slug, error) {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return "", "", xerror.Wrapf("invalid organization slug: %w", err)
	}
	notificationSlug, err := slug.New(c.Params("notificationSlug"))
	if err != nil {
		return "", "", xerror.Wrapf("invalid notification slug: %w", err)
	}
	return orgSlug, notificationSlug, nil
}

// renderNotificationsPage loads notifications and diagnostics for the SSR Notifications workspace.
func (r *Router) renderNotificationsPage(
	c fiber.Ctx,
	orgSlug slug.Slug,
	filters views.NotificationFilters,
	statusCode int,
	workspaceError string,
	pageErrorDetails string,
) error {
	filters = views.NormalizeNotificationFilters(filters)
	if statusCode == 0 {
		statusCode = http.StatusOK
	}

	params := &api.ListNotificationsParams{}
	limit := api.LimitParam(20)
	params.Limit = &limit

	cursor := strings.TrimSpace(filters.Cursor)
	if cursor != "" {
		cursorParam := cursor
		params.Cursor = &cursorParam
	}

	direction := strings.TrimSpace(filters.Direction)
	switch direction {
	case pageDirectionPrev:
		d := api.ListNotificationsParamsDirection(api.PageDirectionParamPrev)
		params.Direction = &d
	case pageDirectionNext:
		d := api.ListNotificationsParamsDirection(api.PageDirectionParamNext)
		params.Direction = &d
	}

	if filters.State == views.NotificationFilterStateResolved {
		lifecycleState := api.NotificationLifecycleStateResolved
		params.LifecycleState = &lifecycleState
	}
	if filters.NotificationType != "" {
		notificationType := api.NotificationType(filters.NotificationType)
		params.NotificationType = &notificationType
	}
	if filters.Severity != "" {
		severity := api.NotificationSeverity(filters.Severity)
		params.Severity = &severity
	}

	notificationList, status, errorResponse, listErr := r.listNotificationsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		params,
	)
	if listErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while loading notifications."
		}
		workspaceError = message
		pageErrorDetails = backendErrorDetails("list notifications", status, errorResponse, listErr)
		statusCode = status

		shellData := r.buildNotificationsShellData(
			c,
			orgSlug.String(),
			message,
			pageErrorDetails,
		)

		return r.writeNotificationsPage(
			c,
			shellData,
			orgSlug,
			[]api.NotificationRecord{},
			map[string][]api.AlertDeliveryAttempt{},
			false,
			"",
			"",
			filters,
			views.DefaultOrganizationPermissions(),
			statusCode,
			workspaceError,
			"",
		)
	}

	r.loadOperationalCapabilities(c, orgSlug.String())
	deliveryAttemptsByNotification := map[string][]api.AlertDeliveryAttempt{}
	diagnosticsError := ""
	notificationSlugs := make([]api.Slug, 0, len(notificationList.Notifications))
	for _, notification := range notificationList.Notifications {
		notificationSlugs = append(notificationSlugs, notification.Slug)
	}
	if len(notificationSlugs) > 0 {
		attemptLimit := api.LimitPerNotificationParam(5)
		attemptParams := &api.ListAlertDeliveryAttemptsParams{
			NotificationSlugs:    notificationSlugs,
			LimitPerNotification: &attemptLimit,
		}

		attemptList, status, errorResponse, attemptsErr := r.listAlertDeliveryAttemptsViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			attemptParams,
		)
		if attemptsErr != nil {
			if status == fiber.StatusUnauthorized {
				return redirectToLogin(c, c.OriginalURL())
			}
			diagnosticsError = messageFromErrorResponse(errorResponse)
			if diagnosticsError == "" {
				diagnosticsError = "Something went wrong while loading alert delivery diagnostics."
			}
		} else {
			for _, attempt := range attemptList.AlertDeliveryAttempts {
				deliveryAttemptsByNotification[attempt.NotificationRecordSlug] = append(
					deliveryAttemptsByNotification[attempt.NotificationRecordSlug],
					attempt,
				)
			}
		}
	}

	nextCursor := ""
	if notificationList.NextCursor != nil {
		nextCursor = *notificationList.NextCursor
	}
	prevCursor := ""
	if notificationList.PrevCursor != nil {
		prevCursor = *notificationList.PrevCursor
	}
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)

	shellData := r.buildNotificationsShellData(c, orgSlug.String(), workspaceError, pageErrorDetails)
	return r.writeNotificationsPage(
		c,
		shellData,
		orgSlug,
		notificationList.Notifications,
		deliveryAttemptsByNotification,
		notificationList.HasMore,
		nextCursor,
		prevCursor,
		filters,
		permissions,
		statusCode,
		workspaceError,
		diagnosticsError,
	)
}

// writeNotificationsPage writes the shared notifications shell and workspace response.
func (r *Router) writeNotificationsPage(
	c fiber.Ctx,
	shellData views.AuthenticatedShellData,
	orgSlug slug.Slug,
	notifications []api.NotificationRecord,
	deliveryAttemptsByNotification map[string][]api.AlertDeliveryAttempt,
	hasMore bool,
	nextCursor string,
	prevCursor string,
	filters views.NotificationFilters,
	permissions views.OrganizationPermissions,
	statusCode int,
	workspaceError string,
	diagnosticsError string,
) error {
	setHTMLResponseStatus(c, statusCode)
	return renderTempl(
		c,
		views.NotificationsPage(
			shellData,
			orgSlug.String(),
			notifications,
			deliveryAttemptsByNotification,
			hasMore,
			nextCursor,
			prevCursor,
			filters,
			permissions,
			workspaceError,
			diagnosticsError,
		),
	)
}

func (r *Router) buildNotificationsShellData(
	c fiber.Ctx,
	orgSlug string,
	pageError string,
	pageErrorDetails string,
) views.AuthenticatedShellData {
	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Notifications",
		orgSlug,
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Review active and recently resolved operational notifications."
	return shellData
}

func (r *Router) listNotificationsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	params *api.ListNotificationsParams,
) (*api.NotificationListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListNotificationsWithResponse(
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
		"list notifications api returned status %d",
		resp.StatusCode(),
	)
}

// acknowledgeNotificationViaAPI forwards an operator acknowledgement to the backend API.
func (r *Router) acknowledgeNotificationViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	notificationSlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.AcknowledgeNotificationWithResponse(
		ctx,
		organizationSlug,
		notificationSlug,
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
		"acknowledge notification api returned status %d",
		resp.StatusCode(),
	)
}

// resolveNotificationViaAPI forwards an operator resolution to the backend API.
func (r *Router) resolveNotificationViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	notificationSlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ResolveNotificationWithResponse(
		ctx,
		organizationSlug,
		notificationSlug,
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
		"resolve notification api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) listAlertDeliveryAttemptsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	params *api.ListAlertDeliveryAttemptsParams,
) (*api.AlertDeliveryAttemptListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListAlertDeliveryAttemptsWithResponse(
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
		"list alert delivery attempts api returned status %d",
		resp.StatusCode(),
	)
}
