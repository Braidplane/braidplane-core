package frontend

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestLogRenderedBackendAPIFailureClassifiesAndRedacts(t *testing.T) {
	t.Parallel()

	traceID, err := trace.TraceIDFromHex("4db57c86403bd979b96119f226f33455")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("00f067aa0ba902b7")
	require.NoError(t, err)
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID})

	tests := []struct {
		name               string
		callErr            error
		payload            *api.ErrorResponse
		wantCategory       string
		wantUpstreamStatus float64
	}{
		{
			name:         "RequestCanceled",
			callErr:      xerror.Unexpected().With(context.Canceled),
			wantCategory: "request_canceled",
		},
		{
			name:         "DeadlineExceeded",
			callErr:      xerror.Unexpected().With(context.DeadlineExceeded),
			wantCategory: "deadline_exceeded",
		},
		{
			name:               "UpstreamResponse",
			callErr:            unexpectedBackendAPIResponse("get data source", http.StatusBadGateway),
			payload:            mustAPIErrorResponse(t, `{"error":{"code":"UPSTREAM_FAILED","message":"secret-message-marker","detailed_error":"token=secret-token-marker"}}`),
			wantCategory:       "upstream_response",
			wantUpstreamStatus: http.StatusBadGateway,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var logBuffer bytes.Buffer
			router := &Router{logger: slog.New(slog.NewJSONHandler(&logBuffer, nil))}
			app := fiber.New()
			app.Get("/", func(c fiber.Ctx) error {
				ctx := reqctx.WithRequestID(c.Context(), "req-rendered-failure")
				ctx = trace.ContextWithSpanContext(ctx, spanContext)
				c.SetContext(ctx)
				router.logRenderedBackendAPIFailure(
					c,
					"get data source",
					tc.payload,
					tc.callErr,
					"acme-corp",
					"hubspot-crm",
				)
				return c.SendStatus(http.StatusOK)
			})

			resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())

			var entry map[string]any
			require.NoError(t, json.Unmarshal(logBuffer.Bytes(), &entry))
			require.Equal(t, "WARN", entry["level"])
			require.Equal(t, "frontend.backend_api.rendered_failure", entry["braidplane.event"])
			require.Equal(t, tc.wantCategory, entry["error.category"])
			require.Equal(t, "req-rendered-failure", entry["request_id"])
			require.Equal(t, traceID.String(), entry["trace_id"])
			require.Equal(t, "acme-corp", entry["organization.slug"])
			require.Equal(t, "hubspot-crm", entry["data_source.slug"])
			if tc.wantUpstreamStatus > 0 {
				require.InDelta(t, tc.wantUpstreamStatus, entry["upstream.status_code"], 0)
			} else {
				require.NotContains(t, entry, "upstream.status_code")
			}
			require.NotContains(t, logBuffer.String(), "secret-message-marker")
			require.NotContains(t, logBuffer.String(), "secret-token-marker")
		})
	}
}

func mustAPIErrorResponse(t *testing.T, raw string) *api.ErrorResponse {
	t.Helper()
	var result api.ErrorResponse
	require.NoError(t, json.Unmarshal([]byte(raw), &result))
	return &result
}
