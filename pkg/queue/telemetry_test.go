package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestTelemetryMiddlewareCarriesOnlySafeCorrelationMetadata(t *testing.T) {
	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled,
	}))
	ctx = reqctx.WithRequestID(ctx, "req-123")
	secret, err := baggage.NewMember("email", "person@example.com")
	require.NoError(t, err)
	bag, err := baggage.New(secret)
	require.NoError(t, err)
	ctx = baggage.ContextWithBaggage(ctx, bag)

	params := []*rivertype.JobInsertParams{{
		Kind: "test_job", Queue: "test_queue", EncodedArgs: []byte(`{"token":"secret-token"}`),
		Metadata: []byte(`{"existing":"kept"}`),
	}}
	middleware := newTelemetryMiddleware(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	_, err = middleware.InsertMany(ctx, params, func(context.Context) ([]*rivertype.JobInsertResult, error) {
		return nil, nil
	})
	require.NoError(t, err)

	var metadata map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(params[0].Metadata, &metadata))
	require.JSONEq(t, `"kept"`, string(metadata["existing"]))
	require.JSONEq(t, `{"request_id":"req-123","traceparent":"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"}`, string(metadata[telemetryMetadataKey]))
	require.NotContains(t, string(params[0].Metadata), "person@example.com")
	require.NotContains(t, string(params[0].Metadata), "secret-token")
}

func TestMergeTelemetryMetadataAcceptsJSONNull(t *testing.T) {
	merged := mergeTelemetryMetadata([]byte("null"), telemetryMetadata{RequestID: "req-123"})
	require.JSONEq(t, `{"braidplane":{"request_id":"req-123"}}`, string(merged))
}

func TestTelemetryMiddlewareCreatesCorrelatedWorkerSpanWithSafeOutcomes(t *testing.T) { // Mutates the global tracer provider.
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	middleware := newTelemetryMiddleware(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	metadata := mergeTelemetryMetadata(nil, telemetryMetadata{
		RequestID:   "req-123",
		Traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	})
	job := &rivertype.JobRow{Kind: "schema_import", Queue: "schema", Attempt: 2, Metadata: metadata}

	require.NoError(t, middleware.Work(context.Background(), job, func(ctx context.Context) error {
		require.Equal(t, "req-123", *reqctx.GetRequestID(ctx))
		MarkWorkerPersistedFailure(ctx, "SAFE_CODE", "provider")
		return nil
	}))

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, traceIDString(t, "4bf92f3577b34da6a3ce929d0e0e4736"), spans[0].SpanContext().TraceID())
	require.Equal(t, "persisted_domain_failure", spanAttribute(spans[0].Attributes(), "job.outcome"))
	require.Equal(t, "SAFE_CODE", spanAttribute(spans[0].Attributes(), "diagnostic.code"))
	require.Equal(t, "provider", spanAttribute(spans[0].Attributes(), "diagnostic.category"))
	require.Equal(t, "Ok", spans[0].Status().Code.String())
}

func TestTelemetryMiddlewareClassifiesExecutionFailureWithoutRecordingMessage(t *testing.T) { // Mutates the global tracer provider.
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	middleware := newTelemetryMiddleware(slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	workerErr := xerror.Unexpected().Detail("password=secret-marker")
	err := middleware.Work(context.Background(), &rivertype.JobRow{Kind: "test", Queue: "default", Attempt: 1}, func(context.Context) error {
		return workerErr
	})
	require.ErrorIs(t, err, workerErr)

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "execution_failed", spanAttribute(spans[0].Attributes(), "job.outcome"))
	errorType := spanAttribute(spans[0].Attributes(), "error.type")
	require.NotEmpty(t, errorType)
	require.NotContains(t, errorType, "secret-marker")
	for _, event := range spans[0].Events() {
		require.NotContains(t, event.Name, "password=secret-marker")
		for _, attr := range event.Attributes {
			require.NotContains(t, attr.Value.String(), "password=secret-marker")
		}
	}
}

func spanAttribute(attrs []attribute.KeyValue, key string) string {
	for _, attr := range attrs {
		if string(attr.Key) == key {
			return attr.Value.AsString()
		}
	}
	return ""
}

func traceIDString(t *testing.T, value string) trace.TraceID {
	t.Helper()
	id, err := trace.TraceIDFromHex(value)
	require.NoError(t, err)
	return id
}

func TestWorkerAttemptErrorsKeepRiverControlFlowWithoutRetainingValues(t *testing.T) {
	const canary = "opaque-worker-canary"
	cause := xerror.Unexpected().Detail(canary)
	for _, tt := range []struct {
		name, outcome, code string
		err                 error
	}{
		{name: "success", outcome: "succeeded"},
		{name: "retry", outcome: "execution_failed", code: xerror.Unexpected().Code(), err: cause},
		{name: "unknown", outcome: "execution_failed", code: "error", err: fmt.Errorf("%s", canary)},
		{name: "explicit cancel", outcome: "canceled", code: xerror.Unexpected().Code(), err: river.JobCancel(cause)},
		{name: "shutdown cancel", outcome: "canceled", code: "request_canceled", err: fmt.Errorf("%s: %w", canary, context.Canceled)},
		{name: "snooze", outcome: "snoozed", code: "job_snoozed", err: river.JobSnooze(time.Minute)},
		{name: "panic", outcome: "panicked", code: errWorkerPanic.Code()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			middleware := newTelemetryMiddleware(slog.New(slog.NewJSONHandler(&logs, nil)))
			err := middleware.Work(t.Context(), &rivertype.JobRow{Kind: "test", Attempt: 1}, func(context.Context) error {
				if tt.name == "panic" {
					panic(canary)
				}
				return tt.err
			})

			if tt.code == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, tt.code)
				if tt.err != nil {
					require.ErrorIs(t, err, tt.err)
				}
				var cancel *river.JobCancelError
				require.Equal(t, tt.name == "explicit cancel", xerror.As(err, &cancel))
				var snooze *river.JobSnoozeError
				if xerror.As(err, &snooze) {
					require.Equal(t, time.Minute, snooze.Duration)
				}
				attempt, marshalErr := json.Marshal(rivertype.AttemptError{Error: err.Error()})
				require.NoError(t, marshalErr)
				require.NotContains(t, string(attempt), canary)
			}
			require.Contains(t, logs.String(), tt.outcome)
			require.NotContains(t, logs.String(), canary)
		})
	}
}

func TestRiverLoggerOmitsDependencyErrorText(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(riverLogHandler{slog.NewJSONHandler(&logs, nil)})
	logger.With("err", "bound-error-canary").Error("River operation failed",
		"error", fmt.Errorf("database-error-canary: %w", context.DeadlineExceeded),
		"panic_val", "panic-value-canary", "job_id", 42)
	require.Contains(t, logs.String(), "deadline_exceeded")
	require.Contains(t, logs.String(), `"job_id":42`)
	require.NotContains(t, logs.String(), "canary")
}
