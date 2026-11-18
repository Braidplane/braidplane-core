package tracing

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// RecordError records a sanitized error classification on span.
func RecordError(span trace.Span, err error) {
	if span == nil || err == nil {
		return
	}

	attrs := ErrorAttributes(err)
	span.SetStatus(codes.Error, attrs[0].Value.AsString())
	span.SetAttributes(attrs...)
}

// ErrorAttributes returns safe error classification attributes for spans.
func ErrorAttributes(err error) []attribute.KeyValue {
	if err == nil {
		return nil
	}
	code, kind, category := xerror.Classify(err)
	attrs := []attribute.KeyValue{attribute.String("error.type", code)}
	if kind != "" {
		attrs = append(attrs, attribute.String("error.kind", kind))
	}
	if category != "" {
		attrs = append(attrs, attribute.String("error.category", category))
	}
	return attrs
}
