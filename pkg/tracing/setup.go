package tracing

import (
	"context"
	"io"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const serviceName = "braidplane"

// Config contains tracing runtime configuration.
type Config struct {
	Component              string
	Environment            string
	OTLPTracesEndpoint     string
	SamplingRatio          float64
	InstallGlobalProviders bool
}

type providerOption func(*providerOptions)

type providerOptions struct {
	spanProcessors []sdktrace.SpanProcessor
}

// WithSpanProcessor adds a span processor to the tracer provider.
func WithSpanProcessor(processor sdktrace.SpanProcessor) providerOption {
	return func(options *providerOptions) {
		if processor != nil {
			options.spanProcessors = append(options.spanProcessors, processor)
		}
	}
}

// Setup configures tracing and optionally installs the provider and propagator globally.
func Setup(ctx context.Context, cfg Config, options ...providerOption) (io.Closer, error) {
	provider, closer, err := NewTracerProvider(ctx, cfg, options...)
	if err != nil {
		return nil, err
	}

	if cfg.InstallGlobalProviders {
		otel.SetTracerProvider(provider)
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))
	}

	return closer, nil
}

// NewTracerProvider creates an OpenTelemetry tracer provider.
func NewTracerProvider(
	ctx context.Context,
	cfg Config,
	options ...providerOption,
) (*sdktrace.TracerProvider, io.Closer, error) {
	providerOptions := providerOptions{}
	for _, option := range options {
		option(&providerOptions)
	}

	environment := strings.TrimSpace(cfg.Environment)
	if environment == "" {
		environment = "local"
	}
	component := strings.TrimSpace(cfg.Component)
	if component == "" {
		component = "backend"
	}

	res, err := resource.New(
		ctx,
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("service.namespace", serviceName),
			attribute.String("deployment.environment", environment),
			attribute.String("braidplane.component", component),
		),
	)
	if err != nil {
		return nil, nil, xerror.Wrapf("create trace resource: %w", err)
	}

	samplingRatio := cfg.SamplingRatio
	if samplingRatio < 0 {
		samplingRatio = 0
	}
	if samplingRatio > 1 {
		samplingRatio = 1
	}

	sdkOptions := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(samplingRatio))),
	}

	closers := make([]io.Closer, 0)
	if endpoint := strings.TrimSpace(cfg.OTLPTracesEndpoint); endpoint != "" {
		exporter, err := otlptracehttp.New(
			ctx,
			otlptracehttp.WithEndpointURL(endpoint),
			otlptracehttp.WithTimeout(5*time.Second),
		)
		if err != nil {
			return nil, nil, xerror.Wrapf("create OTLP trace exporter: %w", err)
		}
		sdkOptions = append(sdkOptions, sdktrace.WithBatcher(exporter))
	}

	for _, processor := range providerOptions.spanProcessors {
		sdkOptions = append(sdkOptions, sdktrace.WithSpanProcessor(processor))
	}

	provider := sdktrace.NewTracerProvider(sdkOptions...)
	closers = append(closers, closeFunc(func() error {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := provider.Shutdown(shutdownCtx); err != nil {
			return xerror.Wrapf("shutdown tracer provider: %w", err)
		}
		return nil
	}))

	return provider, multiCloser{closers: closers}, nil
}

type closeFunc func() error

func (fn closeFunc) Close() error {
	return fn()
}

type multiCloser struct {
	closers []io.Closer
}

func (mc multiCloser) Close() error {
	for _, closer := range mc.closers {
		if closer == nil {
			continue
		}
		if err := closer.Close(); err != nil {
			return err
		}
	}
	return nil
}
