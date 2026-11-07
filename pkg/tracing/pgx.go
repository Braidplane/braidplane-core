package tracing

import (
	"context"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	semconv "go.opentelemetry.io/otel/semconv/v1.28.0"
	"go.opentelemetry.io/otel/trace"
)

type pgxSpanKey struct{}

// NewPGXTracer returns a pgx tracer that records database dependency spans.
func NewPGXTracer() pgx.QueryTracer {
	return pgxTracer{}
}

type pgxTracer struct{}

func (pgxTracer) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	_ pgx.TraceQueryStartData,
) context.Context {
	ctx, span := otel.Tracer("braidplane/postgres").Start( //nolint:spancheck // pgx calls TraceQueryEnd to end the span stored on the returned context.
		ctx,
		"postgres.query",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			semconv.DBSystemPostgreSQL,
		),
	)
	return context.WithValue(ctx, pgxSpanKey{}, span) //nolint:spancheck // pgx calls TraceQueryEnd to end the stored span.
}

func (pgxTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := ctx.Value(pgxSpanKey{}).(trace.Span)
	if !ok {
		return
	}
	defer span.End()
	if data.Err != nil {
		RecordError(span, data.Err)
	}
}
