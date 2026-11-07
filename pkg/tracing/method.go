package tracing

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// MethodSpan describes one service or repository method span.
type MethodSpan struct {
	Layer     string
	Package   string
	Interface string
	Method    string
}

// StartMethodSpan starts an internal span for a decorated method boundary.
func StartMethodSpan(ctx context.Context, method MethodSpan) (context.Context, trace.Span) {
	layer := strings.TrimSpace(method.Layer)
	pkg := strings.TrimSpace(method.Package)
	name := pkg
	if name == "" {
		name = layer
	}
	if method.Method != "" {
		name += "." + strings.TrimSpace(method.Method)
	}
	if name == "" {
		name = "method"
	}

	attrs := []attribute.KeyValue{
		attribute.String("braidplane.layer", layer),
		attribute.String("braidplane.method", methodName(method)),
	}

	return otel.Tracer("braidplane/method").Start( //nolint:spancheck // Caller receives the span and owns End.
		ctx,
		name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...),
	)
}

func methodName(method MethodSpan) string {
	parts := make([]string, 0, 3)
	for _, part := range []string{method.Package, method.Interface, method.Method} {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(parts) == 0 {
		return "method"
	}
	return strings.Join(parts, ".")
}
