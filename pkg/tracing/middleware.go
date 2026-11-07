package tracing

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// HTTPMiddleware starts or continues a server span for a Fiber request.
func HTTPMiddleware(component string) fiber.Handler {
	return func(c fiber.Ctx) error {
		ctx := extractHTTPContext(c.Context(), c)
		requestID := strings.TrimSpace(reqctx.FiberRequestID(c))
		if requestID == "" {
			requestID = strings.TrimSpace(c.Get(fiber.HeaderXRequestID))
		}
		if requestID != "" {
			ctx = reqctx.WithRequestID(ctx, requestID)
			c.Locals("requestid", requestID)
			c.Set(fiber.HeaderXRequestID, requestID)
		}

		spanName := c.Method()
		ctx, span := otel.Tracer("braidplane/http/"+strings.TrimSpace(component)).Start(
			ctx,
			spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.request.method", c.Method()),
			),
		)
		defer span.End()

		c.SetContext(ctx)
		err := c.Next()

		statusCode := c.Response().StatusCode()
		if err != nil {
			statusCode = responseStatusCode(c, err)
			if statusCode >= http.StatusInternalServerError {
				RecordError(span, err)
			} else {
				span.SetAttributes(ErrorAttributes(err)...)
			}
		}
		route := fiberRoute(c)
		if route != "" {
			span.SetName(c.Method() + " " + route)
		}

		attrs := []attribute.KeyValue{
			attribute.String("http.request.method", c.Method()),
			attribute.Int("http.response.status_code", statusCode),
		}
		if route != "" {
			attrs = append(attrs, attribute.String("http.route", route))
		}
		if requestID != "" {
			attrs = append(attrs, attribute.String("request_id", requestID))
		}
		span.SetAttributes(attrs...)

		if statusCode >= http.StatusInternalServerError && err == nil {
			span.SetStatus(codes.Error, strconv.Itoa(statusCode))
			span.SetAttributes(attribute.String("error.type", strconv.Itoa(statusCode)))
		}

		return err
	}
}

func extractHTTPContext(ctx context.Context, c fiber.Ctx) context.Context {
	carrier := propagation.MapCarrier{}
	for _, header := range []string{"traceparent", "tracestate", "baggage"} {
		if value := c.Get(header); value != "" {
			carrier.Set(header, value)
		}
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
