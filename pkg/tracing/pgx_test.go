package tracing

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestPGXTracerUsesStandardDatabaseSemanticConvention(t *testing.T) { // This test mutates the global OpenTelemetry provider.
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	tracer := NewPGXTracer()
	ctx := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	require.Len(t, recorder.Ended(), 1)

	attrs := spanAttributes(recorder.Ended()[0])
	require.Equal(t, "postgresql", attrs["db.system"])
	_, hasLegacyKey := attrs["db.system.name"]
	require.False(t, hasLegacyKey)
}
