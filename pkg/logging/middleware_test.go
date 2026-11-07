package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
)

func TestHTTPMiddleware_AllowsRedirectResponses(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	logger := slog.New(slog.DiscardHandler)
	app.Use(HTTPMiddleware(logger, true))

	app.Get("/redirect", func(c fiber.Ctx) error {
		return c.Redirect().Status(fiber.StatusFound).To("/target")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/redirect", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, fiber.StatusFound, resp.StatusCode)
	require.Equal(t, "/target", resp.Header.Get("Location"))
}

func TestHTTPMiddleware_PreservesResponseBody(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	logger := slog.New(slog.DiscardHandler)
	app.Use(HTTPMiddleware(logger, true))

	app.Get("/json", func(c fiber.Ctx) error {
		return c.Status(fiber.StatusOK).JSON(fiber.Map{"ok": true})
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/json", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, fiber.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "{\"ok\":true}", string(body))
}

func TestHTTPMiddleware_RedactsSensitiveData(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := slog.New(
		slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug}),
	)

	app := fiber.New()
	app.Use(HTTPMiddleware(logger, true))
	app.Post("/login", func(c fiber.Ctx) error {
		c.Set("X-CSRF-Token", "csrf-response-secret")
		return c.Status(http.StatusOK).JSON(fiber.Map{
			"access_token": "secret-token",
			"ok":           true,
		})
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/login", strings.NewReader(`{"password":"abc"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Cookie", "session_id=abc")
	req.Header.Set("X-CSRF-Token", "csrf-request-secret")
	req.Header.Set("Referer", "https://app.example.com/auth/reset-password?token=referer-reset-secret")
	req.Header.Set("HX-Current-URL", "https://app.example.com/auth/reset-password?token=htmx-reset-secret")

	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	logs := logBuffer.String()
	require.Contains(t, logs, "[REDACTED]")
	require.NotContains(t, logs, "Bearer test")
	require.NotContains(t, logs, "session_id=abc")
	require.NotContains(t, logs, "csrf-request-secret")
	require.NotContains(t, logs, "csrf-response-secret")
	require.NotContains(t, logs, "referer-reset-secret")
	require.NotContains(t, logs, "htmx-reset-secret")
	require.NotContains(t, logs, "\"password\":\"abc\"")
	require.NotContains(t, logs, "secret-token")
}

func TestHTTPMiddleware_RedactsTopLevelJSONScalarResponse(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := slog.New(
		slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug}),
	)

	app := fiber.New()
	app.Use(HTTPMiddleware(logger, true))
	app.Get("/token", func(c fiber.Ctx) error {
		return c.Status(http.StatusOK).JSON("secret-token")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/token", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	logs := logBuffer.String()
	require.Contains(t, logs, "[REDACTED_JSON_SCALAR]")
	require.NotContains(t, logs, "secret-token")
}

func TestHTTPMiddleware_LogsCompletionWithoutBodiesByDefault(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := slog.New(
		slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}),
	)

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Locals("requestid", "req-123")
		return c.Next()
	})
	app.Use(HTTPMiddleware(logger, false))
	app.Post("/login", func(c fiber.Ctx) error {
		return c.Status(http.StatusCreated).JSON(fiber.Map{
			"access_token": "secret-token",
			"ok":           true,
		})
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/login", strings.NewReader(`{"password":"abc"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(logBuffer.Bytes(), &entry))
	require.Equal(t, "http.request.completed", entry["braidplane.event"])
	require.Equal(t, "req-123", entry["request_id"])
	require.Equal(t, http.MethodPost, entry["http.method"])
	require.InEpsilon(t, http.StatusCreated, entry["http.status_code"], 0)
	require.Equal(t, "success", entry["outcome"])
	require.Contains(t, entry, "duration_ms")

	logs := logBuffer.String()
	require.NotContains(t, logs, "request_body")
	require.NotContains(t, logs, "response_body")
	require.NotContains(t, logs, "abc")
	require.NotContains(t, logs, "secret-token")
}

func TestHTTPMiddleware_LogsErrorOutcomeForFailedCompletion(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := slog.New(
		slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}),
	)

	app := fiber.New()
	app.Use(HTTPMiddleware(logger, false))
	app.Get("/failure", func(c fiber.Ctx) error {
		return fiber.NewError(http.StatusServiceUnavailable, "dependency unavailable opaque-credential-value")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/failure", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(logBuffer.Bytes(), &entry))
	require.Equal(t, "http.request.completed", entry["braidplane.event"])
	require.Equal(t, "/failure", entry["http.route"])
	require.InEpsilon(t, http.StatusServiceUnavailable, entry["http.status_code"], 0)
	require.Equal(t, "error", entry["outcome"])
	require.Equal(t, "fiber_error", entry["error.type"])
	require.Equal(t, "ERROR", entry["level"])

	require.Equal(t, "Service Unavailable", entry["error.message"])
	require.NotContains(t, logBuffer.String(), "dependency unavailable")
	require.NotContains(t, logBuffer.String(), "opaque-credential-value")
}

func TestHTTPMiddleware_LogsAPIErrorStatusAndCode(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := slog.New(
		slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}),
	)

	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(HTTPMiddleware(logger, false))
	app.Post("/users", func(c fiber.Ctx) error {
		return apierror.ValidationFailed("invalid request")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/users", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(logBuffer.Bytes(), &entry))
	require.Equal(t, "http.request.completed", entry["braidplane.event"])
	require.InEpsilon(t, http.StatusBadRequest, entry["http.status_code"], 0)
	require.Equal(t, "error", entry["outcome"])
	require.Equal(t, "VALIDATION_FAILED", entry["error.type"])
	require.Equal(t, "api_error", entry["error.kind"])
	require.Equal(t, "WARN", entry["level"])
	require.Equal(t, "Bad Request", entry["error.message"])
	require.NotContains(t, logBuffer.String(), "invalid request")
}

func TestHTTPMiddleware_LogsErrorsWhenInfoLevelDisabled(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := slog.New(
		slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelWarn}),
	)

	app := fiber.New()
	app.Use(HTTPMiddleware(logger, false))
	app.Get("/failure", func(c fiber.Ctx) error {
		return fiber.NewError(http.StatusServiceUnavailable, "dependency unavailable")
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/failure", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(logBuffer.Bytes(), &entry))
	require.Equal(t, "http.request.completed", entry["braidplane.event"])
	require.InEpsilon(t, http.StatusServiceUnavailable, entry["http.status_code"], 0)
	require.Equal(t, "error", entry["outcome"])
	require.Equal(t, "ERROR", entry["level"])
}

func TestHTTPMiddleware_ExtractsTraceContextFromTraceparent(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := slog.New(
		slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}),
	)

	app := fiber.New()
	app.Use(HTTPMiddleware(logger, false))
	app.Get("/traced", func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusNoContent)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/traced", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(logBuffer.Bytes(), &entry))
	require.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", entry["trace_id"])
	require.Equal(t, "00f067aa0ba902b7", entry["span_id"])
}

func TestHTTPMiddleware_LogsHandlerContextAttrs(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := slog.New(
		slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}),
	)

	app := fiber.New()
	app.Use(HTTPMiddleware(logger, false))
	app.Get("/orgs/:orgSlug/queries/:querySlug", func(c fiber.Ctx) error {
		ctx := WithAttrs(
			c.Context(),
			slog.String("organization.slug", c.Params("orgSlug")),
			slog.String("query.slug", c.Params("querySlug")),
		)
		c.SetContext(ctx)
		return c.SendStatus(http.StatusNoContent)
	})

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme/queries/active-users", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(logBuffer.Bytes(), &entry))
	require.Equal(t, "acme", entry["organization.slug"])
	require.Equal(t, "active-users", entry["query.slug"])
}

func TestHTTPMiddleware_LogsRouteWithoutRawPathOrQueryValues(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := slog.New(
		slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug}),
	)

	app := fiber.New()
	app.Use(HTTPMiddleware(logger, false))
	app.Get("/orgs/:orgSlug/apps/:appSlug", func(c fiber.Ctx) error {
		return c.SendStatus(http.StatusNoContent)
	})

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodGet,
		"/orgs/acme/apps/payroll?access_token=secret-token&filter=customer",
		nil)

	resp, err := app.Test(req)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	var entries []map[string]any
	decoder := json.NewDecoder(strings.NewReader(logBuffer.String()))
	for decoder.More() {
		var entry map[string]any
		require.NoError(t, decoder.Decode(&entry))
		entries = append(entries, entry)
	}
	require.NotEmpty(t, entries)
	for _, entry := range entries {
		require.Equal(t, "/orgs/:orgSlug/apps/:appSlug", entry["http.route"])
		require.NotContains(t, entry, "url.query")
		require.NotContains(t, entry, "url.path")
	}

	logs := logBuffer.String()
	require.NotContains(t, logs, "acme")
	require.NotContains(t, logs, "payroll")
	require.NotContains(t, logs, "secret-token")
	require.NotContains(t, logs, "customer")
}

func TestRedactBody_RedactsSensitiveFieldNames(t *testing.T) {
	t.Parallel()

	redacted := redactBody([]byte(`{
		"api_key": "api-secret",
		"value": "plain-value-secret",
		"session_id": "session-secret",
		"csrf": "csrf-secret",
		"state": "oauth-state-secret",
		"authorization_url": "https://provider.example/oauth?state=oauth-state-secret",
		"nested": {
			"client_secret": "client-secret",
			"credentials": {
				"password": "password-secret"
			}
		},
		"token": {
			"value": "nested-token-secret"
		},
		"safe": "visible"
	}`))

	require.Contains(t, redacted, `"safe":"visible"`)
	require.Contains(t, redacted, `"api_key":"[REDACTED]"`)
	require.Contains(t, redacted, `"value":"[REDACTED]"`)
	require.Contains(t, redacted, `"session_id":"[REDACTED]"`)
	require.Contains(t, redacted, `"csrf":"[REDACTED]"`)
	require.Contains(t, redacted, `"state":"[REDACTED]"`)
	require.Contains(t, redacted, `"authorization_url":"[REDACTED]"`)
	require.Contains(t, redacted, `"client_secret":"[REDACTED]"`)
	require.Contains(t, redacted, `"credentials":"[REDACTED]"`)
	require.Contains(t, redacted, `"token":"[REDACTED]"`)
	require.NotContains(t, redacted, "api-secret")
	require.NotContains(t, redacted, "plain-value-secret")
	require.NotContains(t, redacted, "session-secret")
	require.NotContains(t, redacted, "csrf-secret")
	require.NotContains(t, redacted, "oauth-state-secret")
	require.NotContains(t, redacted, "client-secret")
	require.NotContains(t, redacted, "password-secret")
	require.NotContains(t, redacted, "nested-token-secret")
}

func TestHTTPMiddlewareNeverCapturesProofIntentsTargetsOrHeaders(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{"proof-observation-previews", "proof-observations", "proof-receipts", "proof-receipts/receipt"} {
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		app := fiber.New()
		app.Use(HTTPMiddleware(logger, true))
		path := "/organizations/:org/projects/:project/environments/:environment/" + suffix
		app.Post(path, func(c fiber.Ctx) error {
			c.Set("X-Private-Response", "private-response-header")
			return c.JSON(fiber.Map{"target": fiber.Map{"table_id": "private-table"}, "intent_token": "private-intent"})
		})
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/organizations/acme/projects/etl/environments/staging/"+suffix, strings.NewReader(`{"target":{"table_id":"private-request-table"},"intent_token":"private-request-intent"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Private-Request", "private-request-header")
		request.Header.Set("User-Agent", "private-user-agent")
		response, err := app.Test(request)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.NoError(t, response.Body.Close())
		require.Contains(t, logs.String(), "http.request.completed")
		for _, omitted := range []string{"private-", "request_body", "response_body", "request_headers", "response_headers", "user_agent.original"} {
			require.NotContains(t, logs.String(), omitted)
		}
	}
}
