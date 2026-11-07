package frontend

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type backendAPIResponseError struct {
	action string
	status int
}

func (e *backendAPIResponseError) Error() string {
	return fmt.Sprintf("%s api returned status %d", e.action, e.status)
}

func unexpectedBackendAPIResponse(action string, status int) error {
	return xerror.Unexpected().With(&backendAPIResponseError{action: action, status: status})
}

// logRenderedBackendAPIFailure records the safe classification of an API-client
// failure that the frontend converts into rendered HTML or a redirect.
func (r *Router) logRenderedBackendAPIFailure(
	c fiber.Ctx,
	action string,
	payload *api.ErrorResponse,
	callErr error,
	organizationSlug string,
	dataSourceSlug string,
) {
	if r == nil || r.logger == nil || c == nil || callErr == nil {
		return
	}

	category := "transport"
	upstreamStatus := 0
	switch {
	case xerror.Is(callErr, context.Canceled):
		category = "request_canceled"
	case xerror.Is(callErr, context.DeadlineExceeded):
		category = "deadline_exceeded"
	default:
		var responseErr *backendAPIResponseError
		if xerror.As(callErr, &responseErr) && responseErr != nil {
			category = "upstream_response"
			upstreamStatus = responseErr.status
		}
	}

	attrs := []slog.Attr{
		slog.String("braidplane.event", "frontend.backend_api.rendered_failure"),
		slog.String("action", strings.TrimSpace(action)),
		slog.String("error.category", category),
	}
	if upstreamStatus > 0 {
		attrs = append(attrs, slog.Int("upstream.status_code", upstreamStatus))
	}
	if payload != nil && strings.TrimSpace(payload.Error.Code) != "" {
		attrs = append(attrs, slog.String("diagnostic.code", strings.TrimSpace(payload.Error.Code)))
	}
	if organizationSlug = strings.TrimSpace(organizationSlug); organizationSlug != "" {
		attrs = append(attrs, slog.String("organization.slug", organizationSlug))
	}
	if dataSourceSlug = strings.TrimSpace(dataSourceSlug); dataSourceSlug != "" {
		attrs = append(attrs, slog.String("data_source.slug", dataSourceSlug))
	}
	attrs = append(attrs, logging.ContextAttrs(c.Context())...)

	r.logger.LogAttrs(
		c.Context(),
		slog.LevelWarn,
		"frontend backend API failure rendered",
		attrs...,
	)
}
