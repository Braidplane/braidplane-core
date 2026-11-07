package frontend

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var webhookDeliveryIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func (r *Router) handleProjectPipelineSchedulesPage(c fiber.Ctx) error {
	projectSlug, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	return r.handlePipelineSchedulesPageWithProject(c, projectSlug.String())
}

func (r *Router) handlePipelineSchedulesPageWithProject(c fiber.Ctx, activeProjectSlug string) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	environmentSlug, environmentStatus, err := r.resolveRuntimeEnvironmentSlug(c)
	if err != nil {
		if environmentStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if strings.TrimSpace(requestRuntimeEnvironmentSlug(c)) == "" {
			return r.renderPipelineSchedulesPageDefaultEnvironmentError(c, orgSlug.String(), activeProjectSlug, environmentStatus, err)
		}
		return c.Status(environmentStatus).SendString(err.Error())
	}

	filters := views.PipelineScheduleFilters{
		PipelineSlug: strings.TrimSpace(c.Query("pipeline_slug")),
		Enabled:      strings.TrimSpace(c.Query("enabled")),
	}
	triggerParams := &api.ListPipelineTriggersParams{}
	triggerLimit := api.LimitParam(100)
	triggerParams.Limit = &triggerLimit

	triggerList, status, errorResponse, listErr := r.listPipelineTriggersViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		activeProjectSlug,
		environmentSlug.String(),
		triggerParams,
	)
	if listErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while loading pipeline schedules."
		}
		shellData := r.buildSchedulesShellData(
			c,
			orgSlug.String(),
			activeProjectSlug,
			environmentSlug.String(),
			message,
			backendErrorDetails("list pipeline triggers", status, errorResponse, listErr),
		)

		c.Status(status)
		return renderTempl(
			c,
			views.PipelineSchedulesPage(
				shellData,
				orgSlug.String(),
				activeProjectSlug,
				environmentSlug.String(),
				nil,
				filters,
				views.PipelineScheduleSelectedDetail{},
			),
		)
	}

	triggers := views.FilterPipelineScheduleTriggers(triggerList.PipelineTriggers, filters)
	selectedTriggerSlug := strings.TrimSpace(c.Query("trigger_slug"))
	if selectedTriggerSlug == "" && c.Query("preview") != "closed" && len(triggers) > 0 {
		selectedTriggerSlug = triggers[0].Slug
	}
	selectedDetail := r.loadPipelineScheduleSelectedDetail(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		activeProjectSlug,
		environmentSlug.String(),
		selectedTriggerSlug,
		"",
		"",
		1,
	)
	selectedDetail.DeliveryActionSuccess = strings.TrimSpace(c.Query("delivery_success"))
	selectedDetail.DeliveryActionError = strings.TrimSpace(c.Query("delivery_error"))

	shellData := r.buildSchedulesShellData(c, orgSlug.String(), activeProjectSlug, environmentSlug.String(), "", "")

	return renderTempl(
		c,
		views.PipelineSchedulesPage(
			shellData,
			orgSlug.String(),
			activeProjectSlug,
			environmentSlug.String(),
			triggers,
			filters,
			selectedDetail,
		),
	)
}

func (r *Router) loadPipelineScheduleSelectedDetail(
	ctx context.Context,
	cookieHeader string,
	orgSlug string,
	activeProjectSlug string,
	environmentSlug string,
	triggerSlug string,
	fireCursor string,
	fireDirection string,
	historyLimit api.LimitParam,
) views.PipelineScheduleSelectedDetail {
	selected := views.PipelineScheduleSelectedDetail{
		Slug:          strings.TrimSpace(triggerSlug),
		FireCursor:    strings.TrimSpace(fireCursor),
		FireDirection: strings.TrimSpace(fireDirection),
	}
	if selected.Slug == "" {
		return selected
	}
	if _, err := slug.New(selected.Slug); err != nil {
		selected.ErrorSummary = "Schedule details are unavailable."
		selected.ErrorDetails = "Validation: selected trigger slug is invalid."
		selected.ErrorDetailsSanitized = true
		return selected
	}

	triggerRecord, status, errorResponse, getErr := r.getPipelineTriggerViaAPI(
		ctx,
		cookieHeader,
		orgSlug,
		activeProjectSlug,
		environmentSlug,
		selected.Slug,
	)
	if getErr != nil {
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while loading schedule details."
		}
		selected.ErrorSummary = message
		selected.ErrorStatus = status
		selected.ErrorDetails = backendErrorDetails("get selected pipeline trigger", status, errorResponse, getErr)
		return selected
	}
	selected.Trigger = triggerRecord
	if triggerRecord.Type == api.PipelineTriggerTypeCompletion {
		deployment, status, _, err := r.getPipelineDeploymentViaAPI(ctx, cookieHeader, orgSlug, activeProjectSlug, environmentSlug, triggerRecord.PipelineSlug)
		switch {
		case err == nil:
			selected.CompletionPreview = deployment
		case status == http.StatusNotFound:
			selected.CompletionPreviewError = "No current deployment. A matching closure will be refused without a child."
		default:
			selected.CompletionPreviewError = "Current deployment preview is unavailable. This is not execution evidence."
		}
		return selected
	}
	if historyLimit == 0 {
		return selected
	}
	if triggerRecord.Type == api.PipelineTriggerTypeWebhook {
		deliveryParams := &api.ListPipelineWebhookDeliveriesParams{}
		deliveryLimit := historyLimit
		deliveryParams.Limit = &deliveryLimit
		if selected.FireCursor != "" {
			cursor := selected.FireCursor
			deliveryParams.Cursor = &cursor
		}
		switch selected.FireDirection {
		case pageDirectionPrev:
			direction := api.ListPipelineWebhookDeliveriesParamsDirection(api.PageDirectionParamPrev)
			deliveryParams.Direction = &direction
		case pageDirectionNext:
			direction := api.ListPipelineWebhookDeliveriesParamsDirection(api.PageDirectionParamNext)
			deliveryParams.Direction = &direction
		}
		deliveries, _, deliveryErrorResponse, deliveryErr := r.listPipelineWebhookDeliveriesViaAPI(
			ctx, cookieHeader, orgSlug, activeProjectSlug, environmentSlug, selected.Slug, deliveryParams,
		)
		if deliveryErr != nil {
			selected.DeliveryHistoryUnavailable = true
			selected.DeliveryHistoryErrorSummary = messageFromErrorResponse(deliveryErrorResponse)
			if selected.DeliveryHistoryErrorSummary == "" {
				selected.DeliveryHistoryErrorSummary = "Webhook delivery history is unavailable."
			}
			selected.DeliveryHistoryErrorDetails = detailedErrorFromErrorResponse(deliveryErrorResponse)
			selected.DeliveryHistoryErrorDetailsSanitized = selected.DeliveryHistoryErrorDetails != ""
			return selected
		}
		selected.Deliveries = deliveries.Deliveries
		selected.DeliveriesHasMore = deliveries.HasMore
		if deliveries.NextCursor != nil {
			selected.DeliveriesNextCursor = *deliveries.NextCursor
		}
		if deliveries.PrevCursor != nil {
			selected.DeliveriesPrevCursor = *deliveries.PrevCursor
		}
		return selected
	}

	fireParams := &api.ListPipelineScheduleFiresParams{}
	fireLimit := historyLimit
	fireParams.Limit = &fireLimit
	if selected.FireCursor != "" {
		cursor := selected.FireCursor
		fireParams.Cursor = &cursor
	}
	switch selected.FireDirection {
	case pageDirectionPrev:
		direction := api.ListPipelineScheduleFiresParamsDirection(api.PageDirectionParamPrev)
		fireParams.Direction = &direction
	case pageDirectionNext:
		direction := api.ListPipelineScheduleFiresParamsDirection(api.PageDirectionParamNext)
		fireParams.Direction = &direction
	}

	fireList, fireStatus, fireErrorResponse, fireErr := r.listPipelineScheduleFiresViaAPI(
		ctx,
		cookieHeader,
		orgSlug,
		activeProjectSlug,
		environmentSlug,
		selected.Slug,
		fireParams,
	)
	if fireErr != nil {
		message := messageFromErrorResponse(fireErrorResponse)
		if message == "" {
			message = "Schedule fire history is unavailable."
		}
		selected.FireHistoryUnavailable = true
		selected.FireHistoryErrorSummary = message
		selected.FireHistoryErrorDetails = backendErrorDetails("list schedule fires", fireStatus, fireErrorResponse, fireErr)
		return selected
	}

	selected.Fires = fireList.ScheduleFires
	selected.FiresHasMore = fireList.HasMore
	if fireList.NextCursor != nil {
		selected.FiresNextCursor = *fireList.NextCursor
	}
	if fireList.PrevCursor != nil {
		selected.FiresPrevCursor = *fireList.PrevCursor
	}
	return selected
}

func (r *Router) buildSchedulesShellData(
	c fiber.Ctx,
	orgSlug string,
	activeProjectSlug string,
	environmentSlug string,
	pageError string,
	pageErrorDetails string,
) views.AuthenticatedShellData {
	shellData := r.buildShellDataForProjectPage(
		c,
		"Pipeline schedules",
		orgSlug,
		activeProjectSlug,
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Product-managed schedule definitions and fire diagnostics for this runtime environment."
	return preserveSchedulesEnvironmentInShellData(shellData, environmentSlug)
}

func (r *Router) renderPipelineSchedulesPageDefaultEnvironmentError(
	c fiber.Ctx,
	orgSlug string,
	activeProjectSlug string,
	status int,
	err error,
) error {
	pageError := projectEnvironmentsLoadErrorMessage
	if status == fiber.StatusBadRequest {
		pageError = "Select a runtime environment before viewing pipeline schedules."
	}
	shellData := r.buildSchedulesShellData(
		c,
		orgSlug,
		activeProjectSlug,
		"",
		pageError,
		fmt.Sprintf("resolve default runtime environment: %v", err),
	)

	c.Status(status)
	return renderTempl(
		c,
		views.PipelineSchedulesPage(
			shellData,
			orgSlug,
			activeProjectSlug,
			"",
			nil,
			views.PipelineScheduleFilters{},
			views.PipelineScheduleSelectedDetail{},
		),
	)
}

func preserveSchedulesEnvironmentInShellData(
	shellData views.AuthenticatedShellData,
	environmentSlug string,
) views.AuthenticatedShellData {
	environmentSlug = strings.TrimSpace(environmentSlug)
	if environmentSlug == "" {
		return shellData
	}

	for i := range shellData.NavItems {
		item := &shellData.NavItems[i]
		if item.Label != "Schedules" || !item.Enabled || strings.TrimSpace(item.Href) == "" {
			continue
		}

		item.Href = appendSchedulesEnvironmentQuery(item.Href, environmentSlug)
	}
	return shellData
}

func appendSchedulesEnvironmentQuery(href, environmentSlug string) string {
	if strings.TrimSpace(href) == "" || strings.TrimSpace(environmentSlug) == "" {
		return href
	}

	parsed, err := url.Parse(href)
	if err != nil {
		return href
	}

	values := parsed.Query()
	values.Set("environment_slug", strings.TrimSpace(environmentSlug))
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func (r *Router) listPipelineTriggersViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	params *api.ListPipelineTriggersParams,
) (*api.PipelineTriggerListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListPipelineTriggersWithResponse(
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

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"list pipeline triggers api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) getPipelineTriggerViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	triggerSlug string,
) (*api.PipelineTrigger, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetPipelineTriggerWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		environmentSlug,
		triggerSlug,
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
		"get pipeline trigger api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) listPipelineScheduleFiresViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	triggerSlug string,
	params *api.ListPipelineScheduleFiresParams,
) (*api.PipelineScheduleFireListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListPipelineScheduleFiresWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		environmentSlug,
		triggerSlug,
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
		"list pipeline schedule fires api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) listPipelineWebhookDeliveriesViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	triggerSlug string,
	params *api.ListPipelineWebhookDeliveriesParams,
) (*api.PipelineWebhookDeliveryListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListPipelineWebhookDeliveriesWithResponse(
		ctx, organizationSlug, projectSlugForAPI(projectSlug), environmentSlug, triggerSlug, params,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}
	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}
	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf("list pipeline webhook deliveries api returned status %d", resp.StatusCode())
}

func (r *Router) handlePipelineWebhookDeliveryReplaySubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}
	triggerSlug, err := slug.New(c.Params("triggerSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid trigger slug")
	}
	environmentSlug, err := slug.New(c.FormValue("environment_slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid environment slug")
	}
	deliveryID := strings.TrimSpace(c.Params("deliveryID"))
	if !webhookDeliveryIDPattern.MatchString(deliveryID) {
		return c.Status(fiber.StatusBadRequest).SendString("invalid webhook delivery ID")
	}
	values := url.Values{"environment_slug": {environmentSlug.String()}, "section": {"history"}}
	if listReturn := strings.TrimSpace(c.FormValue("list_return")); listReturn != "" {
		values.Set("list_return", listReturn)
	}
	destination := "/orgs/" + orgSlug.String() + "/projects/" + projectSlug.String() + "/schedules/" + triggerSlug.String()

	resp, replayErr := r.apiClient.ReplayPipelineWebhookDeliveryWithResponse(
		c.Context(), orgSlug.String(), projectSlugForAPI(projectSlug.String()), environmentSlug.String(), triggerSlug.String(), deliveryID,
		frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)),
	)
	if replayErr != nil {
		values.Set("delivery_error", "Webhook delivery replay failed.")
		return c.Redirect().Status(fiber.StatusSeeOther).To(destination + "?" + values.Encode())
	}
	if resp.StatusCode() == fiber.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if resp.StatusCode() != fiber.StatusOK {
		message := messageFromErrorResponse(parseAPIErrorResponse(resp.Body))
		if message == "" {
			message = "Webhook delivery replay is unavailable."
		}
		values.Set("delivery_error", message)
		return c.Redirect().Status(fiber.StatusSeeOther).To(destination + "?" + values.Encode())
	}
	values.Set("delivery_success", "Webhook delivery replay queued.")
	return c.Redirect().Status(fiber.StatusSeeOther).To(destination + "?" + values.Encode())
}

func (r *Router) handlePipelineScheduleDetailPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	projectSlug, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}
	triggerSlug, err := slug.New(c.Params("triggerSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid trigger slug")
	}
	environmentSlug, status, err := r.resolveRuntimeEnvironmentSlug(c)
	if err != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		return c.Status(status).SendString(projectEnvironmentsLoadErrorMessage)
	}
	historyLimit := api.LimitParam(0)
	if c.Query("section") == "history" {
		historyLimit = 20
	}
	selected := r.loadPipelineScheduleSelectedDetail(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), projectSlug.String(), environmentSlug.String(), triggerSlug.String(), c.Query("fire_cursor"), c.Query("fire_direction"), historyLimit)
	if selected.ErrorStatus == fiber.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if selected.ErrorStatus != 0 {
		c.Status(selected.ErrorStatus)
	}
	selected.DeliveryActionSuccess = c.Query("delivery_success")
	selected.DeliveryActionError = c.Query("delivery_error")
	shell := r.buildSchedulesShellData(c, orgSlug.String(), projectSlug.String(), environmentSlug.String(), "", "")
	return renderTempl(c, views.PipelineScheduleDetailPage(shell, orgSlug.String(), projectSlug.String(), environmentSlug.String(), selected))
}
