package frontend

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/Braidplane/braidplane-core/pkg/metrics"
	"github.com/Braidplane/braidplane-core/pkg/tracing"
)

const (
	frontendAPIDefaultTimeout          = 15 * time.Second
	frontendAPIExecutionGrace          = 15 * time.Second
	frontendAPIDefaultExecutionTimeout = 10 * time.Minute
)

func newAPIHTTPClient() *http.Client {
	return &http.Client{
		Transport: defaultTimeoutRoundTripper{
			base: metrics.NewHTTPClientRoundTripper(
				tracingRoundTripper{base: http.DefaultTransport},
				"frontend",
				"backend_api",
			),
			timeout: frontendAPIDefaultTimeout,
		},
	}
}

// NewAPIHTTPClient builds the instrumented client required by a separately composed frontend presentation.
func NewAPIHTTPClient() *http.Client {
	return newAPIHTTPClient()
}

func frontendAPIExecutionContext(
	ctx context.Context,
	timeoutSeconds int,
) (context.Context, context.CancelFunc) {
	timeout := frontendAPIDefaultExecutionTimeout
	if timeoutSeconds > 0 {
		timeout = time.Duration(timeoutSeconds)*time.Second + frontendAPIExecutionGrace
	}

	return context.WithTimeout(ctx, timeout)
}

type tracingRoundTripper struct {
	base http.RoundTripper
}

type defaultTimeoutRoundTripper struct {
	base    http.RoundTripper
	timeout time.Duration
}

// RoundTrip keeps derived timeout cancellation active through body reads.
// Successful requests transfer cleanup to body close; transport errors cancel immediately.
func (rt defaultTimeoutRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}

	if rt.timeout <= 0 {
		return base.RoundTrip(req)
	}
	if _, ok := req.Context().Deadline(); ok {
		return base.RoundTrip(req)
	}

	ctx, cancel := context.WithTimeout(req.Context(), rt.timeout)
	resp, err := base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return resp, err
	}

	resp.Body = cancelOnCloseBody{
		ReadCloser: resp.Body,
		cancel:     cancel,
	}
	return resp, nil
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

// Close releases the derived timeout context after the wrapped response body closes,
// including when close fails.
func (b cancelOnCloseBody) Close() error {
	defer b.cancel()
	return b.ReadCloser.Close()
}

func (rt tracingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.base
	if base == nil {
		base = http.DefaultTransport
	}

	ctx, span := otel.Tracer("braidplane/frontend/http").Start(
		req.Context(),
		"HTTP "+req.Method,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("http.request.method", req.Method)),
	)
	defer span.End()

	cloned := req.Clone(ctx)
	propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	).Inject(ctx, propagation.HeaderCarrier(cloned.Header))

	resp, err := base.RoundTrip(cloned)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	span.SetAttributes(attribute.Int("http.response.status_code", resp.StatusCode))
	if resp.StatusCode >= http.StatusInternalServerError {
		span.SetStatus(codes.Error, strconv.Itoa(resp.StatusCode))
		span.SetAttributes(attribute.String("error.type", strconv.Itoa(resp.StatusCode)))
	}
	return resp, nil
}
