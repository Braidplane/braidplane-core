package frontend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/trace"

	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
)

func TestFrontendRequestEditor_SetsCookieAndRequestID(t *testing.T) {
	t.Parallel()

	ctx := reqctx.WithRequestID(context.Background(), "req-123")
	editor := frontendRequestEditor(ctx, "session=abc")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)

	err := editor(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "session=abc", req.Header.Get(fiber.HeaderCookie))
	assert.Equal(t, "req-123", req.Header.Get(fiber.HeaderXRequestID))
}

func TestFrontendRequestEditor_OverwritesForwardedClientIP(t *testing.T) {
	t.Parallel()

	ctx := reqctx.WithClientIP(context.Background(), "198.18.12.34")
	editor := frontendRequestEditor(ctx, "")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com/sessions", nil)
	req.Header.Set(fiber.HeaderXForwardedFor, "203.0.113.9")

	err := editor(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "198.18.12.34", req.Header.Get(fiber.HeaderXForwardedFor))
}

func TestFrontendRequestEditor_PropagatesTraceContext(t *testing.T) {
	t.Parallel()

	traceID, err := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	traceState, err := trace.ParseTraceState("vendor=value")
	require.NoError(t, err)
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		TraceState: traceState,
		Remote:     true,
	}))
	editor := frontendRequestEditor(ctx, "")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)

	err = editor(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", req.Header.Get("traceparent"))
	assert.Equal(t, "vendor=value", req.Header.Get("tracestate"))
}

func TestFrontendRequestEditor_PropagatesSafeBaggage(t *testing.T) {
	t.Parallel()

	member, err := baggage.NewMember("braidplane.component", "frontend")
	require.NoError(t, err)
	bag, err := baggage.New(member)
	require.NoError(t, err)
	ctx := baggage.ContextWithBaggage(context.Background(), bag)

	editor := frontendRequestEditor(ctx, "")
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com", nil)

	err = editor(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "braidplane.component=frontend", req.Header.Get("baggage"))
}

func TestFrontendRequestEditor_GeneratesFallbackRequestID(t *testing.T) {
	t.Parallel()

	editor := frontendRequestEditor(context.Background(), "")
	reqA := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/a", nil)
	reqB := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/b", nil)

	err := editor(context.Background(), reqA)
	require.NoError(t, err)
	err = editor(context.Background(), reqB)
	require.NoError(t, err)

	requestIDA := reqA.Header.Get(fiber.HeaderXRequestID)
	requestIDB := reqB.Header.Get(fiber.HeaderXRequestID)

	assert.NotEmpty(t, requestIDA)
	assert.Equal(t, requestIDA, requestIDB)
	assert.Empty(t, reqA.Header.Get(fiber.HeaderCookie))
}

func TestFrontendRequestEditor_SetsCSRFOnlyForUnsafeCookieRequests(t *testing.T) {
	t.Parallel()

	ctx := httpsecurity.WithCSRFToken(context.Background(), "csrf-token")
	editor := frontendRequestEditor(ctx, "session=abc")

	getReq := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/profile", nil)
	err := editor(context.Background(), getReq)
	require.NoError(t, err)
	assert.Empty(t, getReq.Header.Get(httpsecurity.CSRFHeader))

	patchReq := httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "http://example.com/profile", nil)
	err = editor(context.Background(), patchReq)
	require.NoError(t, err)
	assert.Equal(t, "csrf-token", patchReq.Header.Get(httpsecurity.CSRFHeader))

	noCookieEditor := frontendRequestEditor(ctx, "")
	postReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com/profile", nil)
	err = noCookieEditor(context.Background(), postReq)
	require.NoError(t, err)
	assert.Empty(t, postReq.Header.Get(httpsecurity.CSRFHeader))
}
