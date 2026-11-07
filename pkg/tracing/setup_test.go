package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestSetupInstallsTraceContextAndBaggagePropagators(t *testing.T) { // This test mutates global OpenTelemetry providers.
	oldProvider := otel.GetTracerProvider()
	oldPropagator := otel.GetTextMapPropagator()

	closer, err := Setup(context.Background(), Config{
		Component:              "backend",
		Environment:            "local",
		OTLPTracesEndpoint:     "",
		SamplingRatio:          1,
		InstallGlobalProviders: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		otel.SetTextMapPropagator(oldPropagator)
		require.NoError(t, closer.Close())
	})

	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	}))
	member, err := baggage.NewMember("braidplane.component", "frontend")
	require.NoError(t, err)
	bag, err := baggage.New(member)
	require.NoError(t, err)
	ctx = baggage.ContextWithBaggage(ctx, bag)

	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)

	require.Equal(
		t,
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		carrier.Get("traceparent"),
	)
	require.Equal(t, "braidplane.component=frontend", carrier.Get("baggage"))
}

func TestNewTracerProviderUsesParentBasedRatioSampler(t *testing.T) { // This test intentionally exercises sampler state serially.
	recorder := tracetest.NewSpanRecorder()
	provider, closer, err := NewTracerProvider(context.Background(), Config{
		Component:          "backend",
		Environment:        "local",
		OTLPTracesEndpoint: "",
		SamplingRatio:      0,
	}, WithSpanProcessor(recorder))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, closer.Close())
	})

	tracer := provider.Tracer("braidplane-test")
	_, unsampled := tracer.Start(context.Background(), "unsampled-root")
	unsampled.End()
	require.Empty(t, recorder.Ended())

	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	parent := trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	}))

	_, child := tracer.Start(parent, "sampled-child")
	child.End()

	require.Len(t, recorder.Ended(), 1)
	require.Equal(t, "sampled-child", recorder.Ended()[0].Name())
}

func TestEmptyEndpointIgnoresAmbientOTLPExportSettings(t *testing.T) {
	var calls atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", collector.URL+"/v1/traces")
	provider, closer, err := NewTracerProvider(t.Context(), Config{SamplingRatio: 1})
	require.NoError(t, err)
	_, span := provider.Tracer("synthetic").Start(t.Context(), "local-span")
	span.End()
	require.NoError(t, provider.ForceFlush(t.Context()))
	require.NoError(t, closer.Close())
	require.Zero(t, calls.Load(), "an empty application endpoint must disable trace exports")
}
