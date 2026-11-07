package frontend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/Braidplane/braidplane-core/pkg/metrics"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestNewAPIHTTPClientRecordsBackendRequestSpan(t *testing.T) { // This test mutates the global OpenTelemetry provider.
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	client := newAPIHTTPClient()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/api/organizations", nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	require.Len(t, recorder.Ended(), 1)
	require.Equal(t, "HTTP GET", recorder.Ended()[0].Name())
	require.Equal(t, "client", recorder.Ended()[0].SpanKind().String())
}

func TestNewAPIHTTPClientDoesNotSetGlobalTotalTimeout(t *testing.T) {
	t.Parallel()

	client := newAPIHTTPClient()

	require.Zero(t, client.Timeout)
}

func TestNewAPIHTTPClientRecordsBackendAPIMetrics(t *testing.T) { // This test mutates the global metrics meter.
	meter, err := metrics.New(metrics.Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "frontend",
		Environment: "test",
	})
	require.NoError(t, err)
	restore := metrics.SetProcessMeter(meter)
	t.Cleanup(restore)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	client := newAPIHTTPClient()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+"/api/organizations", nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	body := scrapeFrontendMetrics(t, meter)
	require.Contains(t, body, "http_client_request_duration_seconds")
	require.Contains(t, body, "http_client_active_requests")
	require.Contains(t, body, `service_name="frontend"`)
	require.Contains(t, body, `deployment_environment="test"`)
	require.Contains(t, body, `braidplane_component="frontend"`)
	require.Contains(t, body, `server_address="backend_api"`)
	require.Contains(t, body, `http_request_method="GET"`)
	require.Contains(t, body, `http_response_status_code="503"`)
	require.Contains(t, body, `error_type="503"`)
	require.NotContains(t, body, req.URL.Host)
}

func TestDefaultTimeoutRoundTripperAddsDeadlineWhenMissing(t *testing.T) {
	t.Parallel()

	recorder := &deadlineRecordingRoundTripper{}
	rt := defaultTimeoutRoundTripper{base: recorder, timeout: frontendAPIDefaultTimeout}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.True(t, recorder.hasDeadline)
	require.WithinDuration(t, time.Now().Add(frontendAPIDefaultTimeout), recorder.deadline, time.Second)
}

func TestDefaultTimeoutRoundTripperPreservesExistingDeadline(t *testing.T) {
	t.Parallel()

	recorder := &deadlineRecordingRoundTripper{}
	rt := defaultTimeoutRoundTripper{base: recorder, timeout: frontendAPIDefaultTimeout}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com/execute", nil).WithContext(ctx)
	wantDeadline, ok := req.Context().Deadline()
	require.True(t, ok)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.True(t, recorder.hasDeadline)
	require.Equal(t, wantDeadline, recorder.deadline)
}

func TestDefaultTimeoutRoundTripperKeepsContextUntilResponseBodyCloses(t *testing.T) {
	t.Parallel()

	recorder := &contextRecordingRoundTripper{
		body: func(ctx context.Context) io.ReadCloser {
			return &contextCheckingBody{
				ctx:    ctx,
				reader: strings.NewReader("response body"),
			}
		},
	}
	rt := defaultTimeoutRoundTripper{base: recorder, timeout: time.Minute}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "response body", string(body))
	require.NoError(t, recorder.ctx.Err())

	require.NoError(t, resp.Body.Close())
	require.ErrorIs(t, recorder.ctx.Err(), context.Canceled)
}

func TestDefaultTimeoutRoundTripperCancelsContextOnTransportError(t *testing.T) {
	t.Parallel()

	transportErr := xerror.New(xerror.KindInternal, "TEST_TRANSPORT_FAILED", "transport failed")
	recorder := &contextRecordingRoundTripper{err: transportErr}
	rt := defaultTimeoutRoundTripper{base: recorder, timeout: time.Minute}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)

	resp, err := rt.RoundTrip(req)
	if resp != nil {
		t.Cleanup(func() {
			require.NoError(t, resp.Body.Close())
		})
	}
	require.Nil(t, resp)
	require.ErrorIs(t, err, transportErr)
	require.ErrorIs(t, recorder.ctx.Err(), context.Canceled)
}

func TestDefaultTimeoutRoundTripperInterruptsOverdueRequest(t *testing.T) {
	t.Parallel()

	recorder := &contextRecordingRoundTripper{waitForCancellation: true}
	rt := defaultTimeoutRoundTripper{base: recorder, timeout: 10 * time.Millisecond}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)

	resp, err := rt.RoundTrip(req)
	if resp != nil {
		t.Cleanup(func() {
			require.NoError(t, resp.Body.Close())
		})
	}
	require.Nil(t, resp)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestDefaultTimeoutRoundTripperInterruptsOverdueResponseBodyRead(t *testing.T) {
	t.Parallel()

	recorder := &contextRecordingRoundTripper{
		body: func(ctx context.Context) io.ReadCloser {
			return &contextCheckingBody{ctx: ctx}
		},
	}
	rt := defaultTimeoutRoundTripper{base: recorder, timeout: 10 * time.Millisecond}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	_, err = io.ReadAll(resp.Body)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

type deadlineRecordingRoundTripper struct {
	deadline    time.Time
	hasDeadline bool
}

func (rt *deadlineRecordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.deadline, rt.hasDeadline = req.Context().Deadline()
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

type contextRecordingRoundTripper struct {
	ctx                 context.Context
	body                func(context.Context) io.ReadCloser
	err                 error
	waitForCancellation bool
}

// RoundTrip records the request context and can block until cancellation to exercise timeout cleanup.
func (rt *contextRecordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.ctx = req.Context()
	if rt.waitForCancellation {
		<-rt.ctx.Done()
		return nil, rt.ctx.Err()
	}
	if rt.err != nil {
		return nil, rt.err
	}

	body := io.NopCloser(strings.NewReader(""))
	if rt.body != nil {
		body = rt.body(req.Context())
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       body,
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

type contextCheckingBody struct {
	ctx    context.Context
	reader io.Reader
}

// Read either consumes the configured reader while the context is live or blocks until cancellation.
func (b *contextCheckingBody) Read(p []byte) (int, error) {
	if b.reader != nil {
		select {
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		default:
			return b.reader.Read(p)
		}
	}

	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (*contextCheckingBody) Close() error {
	return nil
}

func scrapeFrontendMetrics(t *testing.T, meter *metrics.Meter) string {
	t.Helper()

	app := fiber.New()
	meter.RegisterRoutes(app)

	resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(bodyBytes)
}
