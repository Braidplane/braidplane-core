package tracing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestStartMethodSpanCreatesChildSpanWithOperationalAttributes(t *testing.T) { // This test mutates the global OpenTelemetry provider.
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	ctx, parent := otel.Tracer("braidplane-test").Start(context.Background(), "parent")
	methodCtx, methodSpan := StartMethodSpan(ctx, MethodSpan{
		Layer:     "service",
		Package:   "service/projectssvc",
		Interface: "Service",
		Method:    "CreateProject",
	})
	RecordError(methodSpan, xerror.InvalidInput(xerror.Project).Detail("invalid project"))
	methodSpan.End()
	parent.End()

	require.NotEqual(t, ctx, methodCtx)
	require.Len(t, recorder.Ended(), 2)

	span := recorder.Ended()[0]
	require.Equal(t, "service/projectssvc.CreateProject", span.Name())
	require.Equal(t, "internal", span.SpanKind().String())

	attrs := spanAttributes(span)
	require.Equal(t, "service", attrs["braidplane.layer"])
	require.Equal(t, "service/projectssvc.Service.CreateProject", attrs["braidplane.method"])
	require.Equal(t, "PROJECT_INVALID_INPUT", attrs["error.type"])
	require.Equal(t, "invalid_input", attrs["error.kind"])
}
