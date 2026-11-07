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
	span.SetStatus(codes.Error, errorType(err))
	span.SetAttributes(attrs...)
}

// ErrorAttributes returns safe error classification attributes for spans.
func ErrorAttributes(err error) []attribute.KeyValue {
	if err == nil {
		return nil
	}

	attrs := []attribute.KeyValue{attribute.String("error.type", errorType(err))}
	if kind := errorKind(err); kind != "" {
		attrs = append(attrs, attribute.String("error.kind", kind))
	}
	return attrs
}

func errorType(err error) string {
	if err == nil {
		return ""
	}

	var appErr *xerror.Error
	if xerror.As(err, &appErr) {
		return appErr.Code()
	}

	return "error"
}

func errorKind(err error) string {
	var appErr *xerror.Error
	if xerror.As(err, &appErr) {
		return appErr.Kind().String()
	}
	return ""
}
