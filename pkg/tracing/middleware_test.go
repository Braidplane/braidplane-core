package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Braidplane/braidplane-core/pkg/reqctx"
)

func TestHTTPMiddlewareStartsServerSpanWithRouteAndRequestID(t *testing.T) { // This test mutates the global OpenTelemetry provider.
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("requestid", "req-123")
		c.SetContext(reqctx.WithRequestID(c.Context(), "req-123"))
		return c.Next()
	})
	app.Use(HTTPMiddleware("backend"))
	app.Get("/orgs/:orgSlug/queries/:querySlug", func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusNoContent)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme/queries/active-users", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, "req-123", resp.Header.Get(fiber.HeaderXRequestID))
	require.Len(t, recorder.Ended(), 1)

	span := recorder.Ended()[0]
	require.Equal(t, "GET /orgs/:orgSlug/queries/:querySlug", span.Name())
	require.Equal(t, "server", span.SpanKind().String())

	attrs := spanAttributes(span)
	require.Equal(t, "GET", attrs["http.request.method"])
	require.Equal(t, "/orgs/:orgSlug/queries/:querySlug", attrs["http.route"])
	require.Equal(t, "req-123", attrs["request_id"])
	require.Equal(t, int64(http.StatusNoContent), attrs["http.response.status_code"])
}

func TestHTTPMiddlewareContinuesIncomingTraceparent(t *testing.T) { // This test mutates the global OpenTelemetry provider.
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	app := fiber.New()
	app.Use(HTTPMiddleware("backend"))
	app.Get("/traced", func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/traced", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, recorder.Ended(), 1)
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", recorder.Ended()[0].SpanContext().TraceID().String())
	require.Equal(t, "00f067aa0ba902b7", recorder.Ended()[0].Parent().SpanID().String())
}

func TestHTTPMiddlewareDoesNotMarkClientErrorsAsSpanErrors(t *testing.T) { // This test mutates the global OpenTelemetry provider.
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	app := fiber.New()
	app.Use(HTTPMiddleware("backend"))
	app.Get("/missing", func(c fiber.Ctx) error {
		return fiber.NewError(http.StatusNotFound, "missing")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/missing", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Len(t, recorder.Ended(), 1)

	span := recorder.Ended()[0]
	require.Equal(t, codes.Unset, span.Status().Code)

	attrs := spanAttributes(span)
	require.Equal(t, int64(http.StatusNotFound), attrs["http.response.status_code"])
	require.Equal(t, "error", attrs["error.type"])
}

func spanAttributes(span sdktrace.ReadOnlySpan) map[string]any {
	attrs := make(map[string]any)
	for _, attr := range span.Attributes() {
		attrs[string(attr.Key)] = attr.Value.AsInterface()
	}
	return attrs
}
