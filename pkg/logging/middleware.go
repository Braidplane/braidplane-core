package logging

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// HTTPMiddleware returns a Fiber middleware that logs HTTP request metadata.
func HTTPMiddleware(logger *slog.Logger, logRequests bool) fiber.Handler {
	return func(c fiber.Ctx) error {
		c.SetContext(ensureTraceContext(c))

		start := time.Now()
		if logRequests && logger.Enabled(c.Context(), slog.LevelDebug) {
			logger.LogAttrs(c.Context(), slog.LevelDebug, "HTTP request received", append(
				fiberContextAttrs(c), slog.String("http.method", c.Method()),
			)...)
		}

		err := c.Next()
		duration := time.Since(start)
		statusCode := responseStatusCode(c, err)
		route := fiberRoute(c)

		completionAttrs := []slog.Attr{
			slog.String("braidplane.event", "http.request.completed"),
			slog.String("http.method", c.Method()),
			slog.String("http.route", route),
			slog.Int("http.status_code", statusCode),
			slog.String("outcome", httpOutcome(statusCode)),
			slog.Int64("duration_ms", duration.Milliseconds()),
		}
		completionAttrs = append(completionAttrs, fiberContextAttrs(c)...)

		if err != nil {
			completionAttrs = append(completionAttrs, ErrorAttrs(err)...)
		}

		level := slog.LevelInfo
		if statusCode >= http.StatusInternalServerError {
			level = slog.LevelError
		} else if statusCode >= http.StatusBadRequest {
			level = slog.LevelWarn
		}
		logger.LogAttrs(c.Context(), level, "HTTP request completed", completionAttrs...)

		return err
	}
}

func ensureTraceContext(c fiber.Ctx) context.Context {
	ctx := c.Context()
	if trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}

	carrier := propagation.MapCarrier{}
	if traceparent := c.Get("traceparent"); traceparent != "" {
		carrier.Set("traceparent", traceparent)
	}
	if tracestate := c.Get("tracestate"); tracestate != "" {
		carrier.Set("tracestate", tracestate)
	}
	if baggage := c.Get("baggage"); baggage != "" {
		carrier.Set("baggage", baggage)
	}
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	).Extract(ctx, carrier)
}

func fiberRoute(c fiber.Ctx) string {
	route := c.Route()
	if route == nil {
		return ""
	}
	return route.Path
}

func fiberContextAttrs(c fiber.Ctx) []slog.Attr {
	ctx := c.Context()
	if requestID := reqctx.FiberRequestID(c); requestID != "" {
		ctx = reqctx.WithRequestID(ctx, requestID)
	}

	return ContextAttrs(ctx)
}

func responseStatusCode(c fiber.Ctx, err error) int {
	if err == nil {
		return c.Response().StatusCode()
	}

	var fiberErr *fiber.Error
	if xerror.As(err, &fiberErr) {
		return fiberErr.Code
	}

	var apiErr *apierror.Error
	if xerror.As(err, &apiErr) {
		return apiErr.HTTPStatus
	}

	return http.StatusInternalServerError
}

func httpOutcome(statusCode int) string {
	if statusCode >= http.StatusBadRequest {
		return errorValue
	}
	return "success"
}

// ErrorAttrs returns bounded classifications without formatting the error.
func ErrorAttrs(err error) []slog.Attr {
	if err == nil {
		return nil
	}

	code, kind, category := xerror.Classify(err)
	var fiberErr *fiber.Error
	var apiErr *apierror.Error
	switch {
	case xerror.As(err, &fiberErr):
		code = "fiber_error"
	case xerror.As(err, &apiErr) && apiErr != nil:
		code, kind = apiErr.Code, "api_error"
	}

	attrs := []slog.Attr{slog.String("error.type", code)}
	if kind != "" {
		attrs = append(attrs, slog.String("error.kind", kind))
	}
	if category != "" {
		attrs = append(attrs, slog.String("error.category", category))
	}
	return attrs
}

// ErrorAttr adds classification fields to a log call with other attributes.
func ErrorAttr(err error) slog.Attr {
	return slog.GroupAttrs("", ErrorAttrs(err)...)
}

// LogError records a safe error diagnostic with the process logger.
func LogError(ctx context.Context, message string, err error) {
	LogErrorWith(ctx, slog.Default(), message, err)
}

// LogErrorWith records a safe error diagnostic with the supplied logger.
func LogErrorWith(ctx context.Context, logger *slog.Logger, message string, err error) {
	if err != nil {
		logger.LogAttrs(ctx, slog.LevelError, message, append(ContextAttrs(ctx), ErrorAttrs(err)...)...)
	}
}
