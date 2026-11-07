package frontend

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (r *Router) handleEventsPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	filters := views.EventFilters{
		EventClass:     strings.TrimSpace(c.Query("event_class")),
		ActorType:      strings.TrimSpace(c.Query("actor_type")),
		ActorSlug:      strings.TrimSpace(c.Query("actor_slug")),
		EntityType:     strings.TrimSpace(c.Query("entity_type")),
		EntitySlug:     strings.TrimSpace(c.Query("entity_slug")),
		Action:         strings.TrimSpace(c.Query("action")),
		RequestID:      strings.TrimSpace(c.Query("request_id")),
		OccurredFrom:   strings.TrimSpace(c.Query("occurred_from")),
		OccurredBefore: strings.TrimSpace(c.Query("occurred_before")),
	}
	if filters.EventClass != views.EventClassSecurityAudit {
		filters.EventClass = views.EventClassGeneric
	}

	limit := api.LimitParam(20)

	cursor := strings.TrimSpace(c.Query("cursor"))
	direction := strings.TrimSpace(c.Query("direction"))
	eventList, status, errorResponse, listErr := r.listEventsForView(
		c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), filters, limit, cursor, direction,
	)
	if listErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while loading activity."
		}

		shellData := r.buildEventsShellData(c, orgSlug.String(), message, backendErrorDetails("list events", status, errorResponse, listErr))

		c.Status(status)
		return renderTempl(
			c,
			views.EventsPage(
				shellData,
				orgSlug.String(),
				[]api.EventRecord{},
				false,
				"",
				"",
				filters,
				0,
			),
		)
	}

	nextCursor := ""
	if eventList.NextCursor != nil {
		nextCursor = *eventList.NextCursor
	}
	prevCursor := ""
	if eventList.PrevCursor != nil {
		prevCursor = *eventList.PrevCursor
	}

	shellData := r.buildEventsShellData(c, orgSlug.String(), "", "")
	retentionDays := 0
	if filters.EventClass == views.EventClassSecurityAudit && eventList.EventLogRetentionDays != nil {
		retentionDays = *eventList.EventLogRetentionDays
	}

	return renderTempl(
		c,
		views.EventsPage(
			shellData,
			orgSlug.String(),
			eventList.Events,
			eventList.HasMore,
			nextCursor,
			prevCursor,
			filters,
			retentionDays,
		),
	)
}

func (r *Router) handleSecurityAuditEventsExport(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	filters := views.EventFilters{
		EventClass:     views.EventClassSecurityAudit,
		ActorType:      strings.TrimSpace(c.Query("actor_type")),
		ActorSlug:      strings.TrimSpace(c.Query("actor_slug")),
		EntityType:     strings.TrimSpace(c.Query("entity_type")),
		EntitySlug:     strings.TrimSpace(c.Query("entity_slug")),
		Action:         strings.TrimSpace(c.Query("action")),
		RequestID:      strings.TrimSpace(c.Query("request_id")),
		OccurredFrom:   strings.TrimSpace(c.Query("occurred_from")),
		OccurredBefore: strings.TrimSpace(c.Query("occurred_before")),
	}
	params, parseErr := securityAuditExportParams(filters)
	if parseErr != nil {
		return c.Status(fiber.StatusBadRequest).SendString(eventValidationMessage(parseErr))
	}
	resp, err := r.apiClient.ExportSecurityAuditEventsWithResponse(
		c.Context(), orgSlug.String(), params,
		frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)),
	)
	if err != nil {
		wrapped := xerror.Unexpected().With(err)
		r.logger.Error(
			"security audit export failed",
			slog.Any("error", wrapped),
		)
		return c.Status(fiber.StatusInternalServerError).SendString("Unable to export security audit events.")
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if resp.StatusCode() != http.StatusOK {
		return c.Status(resp.StatusCode()).Send(resp.Body)
	}

	c.Set(fiber.HeaderContentType, "text/csv; charset=utf-8")
	if resp.HTTPResponse != nil {
		c.Set(fiber.HeaderContentDisposition, resp.HTTPResponse.Header.Get(fiber.HeaderContentDisposition))
	}
	return c.Send(resp.Body)
}

func (r *Router) listEventsForView(
	ctx context.Context,
	cookieHeader, organizationSlug string,
	filters views.EventFilters,
	limit api.LimitParam,
	cursor, direction string,
) (*api.EventListResponse, int, *api.ErrorResponse, error) {
	if filters.EventClass == views.EventClassSecurityAudit {
		params, err := securityAuditListParams(filters, limit, cursor, direction)
		if err != nil {
			return nil, http.StatusBadRequest, &api.ErrorResponse{
				Error: api.Error{Code: "BAD_REQUEST", Message: eventValidationMessage(err)},
			}, err
		}
		resp, err := r.apiClient.ListSecurityAuditEventsWithResponse(
			ctx, organizationSlug, params, frontendRequestEditor(ctx, cookieHeader),
		)
		if err != nil {
			return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
		}
		if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
			return resp.JSON200, http.StatusOK, nil, nil
		}
		return nil, resp.StatusCode(), parseAPIErrorResponse(resp.Body), xerror.Unexpected().Detailf(
			"list security audit events api returned status %d", resp.StatusCode(),
		)
	}

	params := &api.ListEventsParams{Limit: &limit}
	if cursor != "" {
		params.Cursor = &cursor
	}
	switch direction {
	case pageDirectionPrev:
		d := api.ListEventsParamsDirection(api.PageDirectionParamPrev)
		params.Direction = &d
	case pageDirectionNext:
		d := api.ListEventsParamsDirection(api.PageDirectionParamNext)
		params.Direction = &d
	}
	if filters.ActorType != "" {
		value := api.EventActorType(filters.ActorType)
		params.ActorType = &value
	}
	if filters.ActorSlug != "" {
		params.ActorSlug = &filters.ActorSlug
	}
	if filters.EntityType != "" {
		value := api.EventEntityType(filters.EntityType)
		params.EntityType = &value
	}
	if filters.EntitySlug != "" {
		params.EntitySlug = &filters.EntitySlug
	}
	if filters.Action != "" {
		value := api.EventAction(filters.Action)
		params.Action = &value
	}
	return r.listEventsViaAPI(ctx, cookieHeader, organizationSlug, params)
}

func securityAuditListParams(
	filters views.EventFilters, limit api.LimitParam, cursor, direction string,
) (*api.ListSecurityAuditEventsParams, error) {
	params := &api.ListSecurityAuditEventsParams{Limit: &limit}
	if cursor != "" {
		params.Cursor = &cursor
	}
	switch direction {
	case pageDirectionPrev:
		value := api.ListSecurityAuditEventsParamsDirection(api.PageDirectionParamPrev)
		params.Direction = &value
	case pageDirectionNext:
		value := api.ListSecurityAuditEventsParamsDirection(api.PageDirectionParamNext)
		params.Direction = &value
	}
	applySecurityAuditFilters(filters, &params.ActorType, &params.ActorSlug, &params.EntityType,
		&params.EntitySlug, &params.Action, &params.RequestId)
	from, before, err := parseEventTimeBounds(filters)
	if err != nil {
		return nil, err
	}
	params.OccurredFrom, params.OccurredBefore = from, before
	return params, nil
}

func securityAuditExportParams(filters views.EventFilters) (*api.ExportSecurityAuditEventsParams, error) {
	params := &api.ExportSecurityAuditEventsParams{}
	applySecurityAuditFilters(filters, &params.ActorType, &params.ActorSlug, &params.EntityType,
		&params.EntitySlug, &params.Action, &params.RequestId)
	from, before, err := parseEventTimeBounds(filters)
	if err != nil {
		return nil, err
	}
	params.OccurredFrom, params.OccurredBefore = from, before
	return params, nil
}

func applySecurityAuditFilters(
	filters views.EventFilters,
	actorType **api.EventActorTypeFilterParam,
	actorSlug **api.EventActorSlugFilterParam,
	entityType **api.EventEntityTypeFilterParam,
	entitySlug **api.EventEntitySlugFilterParam,
	action **api.EventActionFilterParam,
	requestID **api.EventRequestIdFilterParam,
) {
	if filters.ActorType != "" {
		value := api.EventActorTypeFilterParam(filters.ActorType)
		*actorType = &value
	}
	if filters.ActorSlug != "" {
		value := filters.ActorSlug
		*actorSlug = &value
	}
	if filters.EntityType != "" {
		value := api.EventEntityTypeFilterParam(filters.EntityType)
		*entityType = &value
	}
	if filters.EntitySlug != "" {
		value := filters.EntitySlug
		*entitySlug = &value
	}
	if filters.Action != "" {
		value := api.EventActionFilterParam(filters.Action)
		*action = &value
	}
	if filters.RequestID != "" {
		value := filters.RequestID
		*requestID = &value
	}
}

func parseEventTimeBounds(filters views.EventFilters) (*time.Time, *time.Time, error) {
	parse := func(value string) (time.Time, bool, error) {
		if value == "" {
			return time.Time{}, false, nil
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			const message = "Occurrence times must use RFC 3339."
			return time.Time{}, false, xerror.ValidationFailed().Detail(message).WithPublicDetail(message)
		}
		return parsed, true, nil
	}
	fromValue, hasFrom, err := parse(filters.OccurredFrom)
	if err != nil {
		return nil, nil, err
	}
	beforeValue, hasBefore, err := parse(filters.OccurredBefore)
	if err != nil {
		return nil, nil, err
	}
	if hasFrom && hasBefore && !fromValue.Before(beforeValue) {
		const message = "Occurred before must be later than occurred from."
		return nil, nil, xerror.ValidationFailed().Detail(message).WithPublicDetail(message)
	}
	var from, before *time.Time
	if hasFrom {
		from = &fromValue
	}
	if hasBefore {
		before = &beforeValue
	}
	return from, before, nil
}

func eventValidationMessage(err error) string {
	if detail := xerror.PublicDetail(err); detail != "" {
		return detail
	}
	return err.Error()
}

func (r *Router) buildEventsShellData(
	c fiber.Ctx,
	orgSlug string,
	pageError string,
	pageErrorDetails string,
) views.AuthenticatedShellData {
	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Events",
		orgSlug,
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Review retained organization activity and security audit history."
	return shellData
}

func (r *Router) listEventsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	params *api.ListEventsParams,
) (*api.EventListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListEventsWithResponse(
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
		"list events api returned status %d",
		resp.StatusCode(),
	)
}
