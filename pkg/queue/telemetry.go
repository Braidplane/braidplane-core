package queue

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/tracing"
)

const telemetryMetadataKey = "braidplane"

type telemetryMetadata struct {
	RequestID   string `json:"request_id,omitempty"`
	Traceparent string `json:"traceparent,omitempty"`
}

type workerOutcome struct {
	name               string
	diagnosticCode     string
	diagnosticCategory string
}

type workerOutcomeKey struct{}

type telemetryMiddleware struct {
	river.MiddlewareDefaults
	logger *slog.Logger
}

func newTelemetryMiddleware(logger *slog.Logger) *telemetryMiddleware {
	if logger == nil {
		logger = slog.Default()
	}
	return &telemetryMiddleware{logger: logger}
}

func (m *telemetryMiddleware) InsertMany(
	ctx context.Context,
	params []*rivertype.JobInsertParams,
	next func(context.Context) ([]*rivertype.JobInsertResult, error),
) ([]*rivertype.JobInsertResult, error) {
	metadata := metadataFromContext(ctx)
	for _, param := range params {
		if param != nil {
			param.Metadata = mergeTelemetryMetadata(param.Metadata, metadata)
		}
	}

	results, err := next(ctx)
	for _, param := range params {
		if param != nil {
			m.logger.LogAttrs(ctx, slog.LevelInfo, "River job enqueue completed", append(
				logging.ContextAttrs(ctx),
				slog.String("job.kind", param.Kind), slog.String("job.queue", param.Queue),
				slog.Bool("error", err != nil),
			)...)
		}
	}
	return results, err
}

func (m *telemetryMiddleware) Work(
	ctx context.Context,
	job *rivertype.JobRow,
	next func(context.Context) error,
) error {
	if job == nil {
		return next(ctx)
	}

	ctx = contextFromMetadata(ctx, job.Metadata)
	ctx, span := otel.Tracer("braidplane/river").Start(ctx, "river "+job.Kind,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("job.kind", job.Kind),
			attribute.String("job.queue", job.Queue),
			attribute.Int("job.attempt", job.Attempt),
		),
	)
	defer span.End()

	outcome := &workerOutcome{name: "succeeded"}
	ctx = context.WithValue(ctx, workerOutcomeKey{}, outcome)
	m.logger.LogAttrs(ctx, slog.LevelInfo, "River job execution started", workerLogAttrs(ctx, job, "started")...)
	err := next(ctx)
	if err != nil {
		outcome.name = "execution_failed"
		tracing.RecordError(span, err)
	}
	span.SetAttributes(attribute.String("job.outcome", outcome.name))
	if outcome.diagnosticCode != "" {
		span.SetAttributes(attribute.String("diagnostic.code", outcome.diagnosticCode))
	}
	if outcome.diagnosticCategory != "" {
		span.SetAttributes(attribute.String("diagnostic.category", outcome.diagnosticCategory))
	}
	if err == nil {
		span.SetStatus(codes.Ok, outcome.name)
	}
	m.logger.LogAttrs(ctx, slog.LevelInfo, "River job execution completed", workerLogAttrs(ctx, job, outcome.name)...)
	return err
}

func MarkWorkerSkipped(ctx context.Context) {
	if outcome, ok := ctx.Value(workerOutcomeKey{}).(*workerOutcome); ok {
		outcome.name = "skipped"
	}
}

func MarkWorkerPersistedFailure(ctx context.Context, code, category string) {
	if outcome, ok := ctx.Value(workerOutcomeKey{}).(*workerOutcome); ok {
		outcome.name = "persisted_domain_failure"
		outcome.diagnosticCode = code
		outcome.diagnosticCategory = category
	}
}

func metadataFromContext(ctx context.Context) telemetryMetadata {
	metadata := telemetryMetadata{}
	if requestID := reqctx.GetRequestID(ctx); requestID != nil {
		metadata.RequestID = *requestID
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	metadata.Traceparent = carrier.Get("traceparent")
	return metadata
}

func mergeTelemetryMetadata(raw []byte, telemetry telemetryMetadata) []byte {
	metadata := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &metadata)
	if metadata == nil {
		metadata = map[string]json.RawMessage{}
	}
	encoded, err := json.Marshal(telemetry)
	if err != nil {
		return raw
	}
	metadata[telemetryMetadataKey] = encoded
	merged, err := json.Marshal(metadata)
	if err != nil {
		return raw
	}
	return merged
}

func contextFromMetadata(ctx context.Context, raw []byte) context.Context {
	metadata := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &metadata) != nil {
		return ctx
	}
	var telemetry telemetryMetadata
	if json.Unmarshal(metadata[telemetryMetadataKey], &telemetry) != nil {
		return ctx
	}
	if telemetry.RequestID != "" {
		ctx = reqctx.WithRequestID(ctx, telemetry.RequestID)
	}
	if telemetry.Traceparent != "" {
		ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": telemetry.Traceparent})
	}
	return ctx
}

func workerLogAttrs(ctx context.Context, job *rivertype.JobRow, outcome string) []slog.Attr {
	return append(logging.ContextAttrs(ctx),
		slog.String("job.kind", job.Kind),
		slog.String("job.queue", job.Queue),
		slog.Int("job.attempt", job.Attempt),
		slog.String("outcome", outcome),
	)
}
