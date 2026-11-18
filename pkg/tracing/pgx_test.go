package tracing

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
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

	const canary = "opaque-postgres-canary"
	failed := tracer.TraceQueryStart(context.Background(), nil, pgx.TraceQueryStartData{
		SQL: "SELECT $1 /* " + canary + " */", Args: []any{canary},
	})
	tracer.TraceQueryEnd(failed, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: "XX000", Detail: canary}})
	require.Len(t, recorder.Ended(), 2)
	span := recorder.Ended()[1]
	require.Equal(t, codes.Error, span.Status().Code)
	require.Equal(t, "database_error", span.Status().Description)
	encoded, err := json.Marshal(span.Attributes())
	require.NoError(t, err)
	require.NotContains(t, string(encoded), canary)
	require.NotContains(t, string(encoded), "db.query.text")
	require.Empty(t, span.Events())
}
