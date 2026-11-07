package logging

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/redaction"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// HTTPMiddleware returns a Fiber middleware that logs full HTTP requests and responses
func HTTPMiddleware(logger *slog.Logger, logBodies bool) fiber.Handler {
	return func(c fiber.Ctx) error {
		c.SetContext(ensureTraceContext(c))

		start := time.Now()
		debugEnabled := logger.Enabled(c.Context(), slog.LevelDebug)

		protectedProof := protectedProofRoute(c.Path())
		var requestBody []byte
		if debugEnabled && logBodies && !protectedProof && len(c.Body()) > 0 {
			requestBody = make([]byte, len(c.Body()))
			copy(requestBody, c.Body())
		}

		err := c.Next()

		duration := time.Since(start)
		statusCode := responseStatusCode(c, err)
		route := fiberRoute(c)
		protectedProof = protectedProof || protectedProofRoute(route)
		if debugEnabled {
			attrs := []slog.Attr{
				slog.String("http.method", c.Method()),
				slog.String("http.route", route),
				slog.String("client.address", c.IP()),
			}
			attrs = append(attrs, fiberContextAttrs(c)...)

			if logBodies && !protectedProof && len(requestBody) > 0 {
				attrs = append(attrs, slog.String("request_body", redactBody(requestBody)))
			}

			if !protectedProof {
				headers := make(map[string]string)
				for key, value := range c.Request().Header.All() {
					headers[string(key)] = redactHeaderValue(string(key), string(value))
				}
				attrs = append(attrs, slog.Any("request_headers", headers))
				attrs = append(attrs, slog.String("user_agent.original", c.Get("User-Agent")))
			}

			logger.LogAttrs(c.Context(), slog.LevelDebug, "HTTP request received", attrs...)
		}

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
			completionAttrs = append(completionAttrs, httpErrorAttrs(err, statusCode)...)
		}

		level := slog.LevelInfo
		if statusCode >= http.StatusInternalServerError {
			level = slog.LevelError
		} else if statusCode >= http.StatusBadRequest {
			level = slog.LevelWarn
		}
		logger.LogAttrs(c.Context(), level, "HTTP request completed", completionAttrs...)

		if debugEnabled {
			responseAttrs := []slog.Attr{
				slog.String("http.method", c.Method()),
				slog.String("http.route", route),
				slog.Int("http.status_code", statusCode),
				slog.Duration("duration", duration),
			}
			responseAttrs = append(responseAttrs, fiberContextAttrs(c)...)

			responseBody := c.Response().Body()
			if logBodies && !protectedProof && len(responseBody) > 0 {
				responseAttrs = append(
					responseAttrs,
					slog.String("response_body", redactBody(responseBody)),
				)
			}

			if !protectedProof {
				respHeaders := make(map[string]string)
				for key, value := range c.Response().Header.All() {
					respHeaders[string(key)] = redactHeaderValue(string(key), string(value))
				}
				responseAttrs = append(responseAttrs, slog.Any("response_headers", respHeaders))
			}

			logger.LogAttrs(c.Context(), slog.LevelDebug, "HTTP response sent", responseAttrs...)
		}

		if err != nil {
			return err
		}

		return nil
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

func errorType(err error) string {
	if err == nil {
		return ""
	}

	var fiberErr *fiber.Error
	if xerror.As(err, &fiberErr) {
		return "fiber_error"
	}

	var apiErr *apierror.Error
	if xerror.As(err, &apiErr) {
		return apiErr.Code
	}

	var appErr *xerror.Error
	if xerror.As(err, &appErr) {
		return appErr.Code()
	}

	return errorValue
}

func errorKind(err error) string {
	var apiErr *apierror.Error
	if xerror.As(err, &apiErr) {
		return "api_error"
	}

	var appErr *xerror.Error
	if xerror.As(err, &appErr) {
		return appErr.Kind().String()
	}
	return ""
}

func errorAttrs(err error, sensitiveValues ...string) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("error.type", errorType(err)),
		slog.String("error.message", redaction.ErrorMessage(err, sensitiveValues...)),
	}
	if kind := errorKind(err); kind != "" {
		attrs = append(attrs, slog.String("error.kind", kind))
	}
	return attrs
}

func httpErrorAttrs(err error, status int) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("error.type", errorType(err)),
		slog.String("error.message", http.StatusText(status)),
	}
	if kind := errorKind(err); kind != "" {
		attrs = append(attrs, slog.String("error.kind", kind))
	}
	return attrs
}

func redactHeaderValue(key, value string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "authorization", "cookie", "set-cookie", "x-csrf-token", "referer", "hx-current-url":
		return "[REDACTED]"
	default:
		return value
	}
}

func redactBody(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}

	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "[REDACTED_NON_JSON_BODY]"
	}

	switch payload.(type) {
	case map[string]any, []any:
		// Traverse containers and redact sensitive keys recursively.
	default:
		return "[REDACTED_JSON_SCALAR]"
	}

	redacted := redactSensitiveFields(payload)
	out, err := json.Marshal(redacted)
	if err != nil {
		return "[REDACTED_BODY]"
	}

	return string(out)
}

func redactSensitiveFields(value any) any {
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for k, child := range v {
			if isSensitiveKey(k) {
				result[k] = "[REDACTED]"
				continue
			}
			result[k] = redactSensitiveFields(child)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i := range v {
			result[i] = redactSensitiveFields(v[i])
		}
		return result
	default:
		return value
	}
}

func isSensitiveKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	if k == "value" {
		return true
	}
	for _, marker := range []string{
		"api_key",
		"apikey",
		"authorization_url",
		"authorizationurl",
		"auth_url",
		"authurl",
		"bearer_token",
		"cookie",
		"credential",
		"csrf",
		"password",
		"secret",
		"session",
		"state",
		"token",
	} {
		if strings.Contains(k, marker) {
			return true
		}
	}
	return false
}

// Proof intents and metadata targets must remain absent even from debug payload logs.
func protectedProofRoute(path string) bool {
	path = strings.ToLower(path)
	return strings.Contains(path, "/proof-observation") || strings.Contains(path, "/proof-receipts")
}

// LogError records a safe error diagnostic at a boundary that returns a sanitized error.
func LogError(ctx context.Context, message string, err error, sensitiveValues ...string) {
	if err == nil {
		return
	}

	attrs := ContextAttrs(ctx)
	attrs = append(attrs, errorAttrs(err, sensitiveValues...)...)
	slog.Default().LogAttrs(ctx, slog.LevelError, message, attrs...)
}
