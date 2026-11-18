package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	obslogging "github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/tracing"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
)

func TestRequestLoggingCorrelatesHTTPAndServiceEvents(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := slog.New(
		slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelDebug}),
	)
	store := session.NewStore()
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(func(c fiber.Ctx) error {
		requestID := c.Get("X-Request-ID")
		c.Locals("requestid", requestID)
		if requestID != "" {
			c.SetContext(reqctx.WithRequestID(c.Context(), requestID))
		}
		return c.Next()
	})
	app.Use(obslogging.HTTPMiddleware(logger, false))

	projectsService := projectssvc.NewServiceWithLogging(
		&fakeProjectsService{
			GetProjectFn: func(
				_ context.Context,
				_, _ slug.Slug,
			) (*model.Project, error) {
				return &model.Project{
					Slug: must.Slug("internal-tools"),
					Name: "Internal Tools",
				}, nil
			},
		},
		logger,
	)

	api.RegisterHandlers(app, &server{
		usersService:         &fakeUsersService{},
		oidcService:          &fakeOIDCService{},
		sessionStore:         store,
		authorizationService: &fakeAuthService{CanFn: allowAuth},
		orgService:           &fakeOrgService{},
		projectsService:      projectsService,
		membersService:       &fakeMembersService{},
		rolesService:         &fakeRolesService{},
		datasourcesService:   &fakeDataSourcesService{},
		queriesService:       &fakeQueriesService{},
		pipelinesService:     &fakePipelinesService{},
		frontendURL:          "http://localhost:3000",
		googleRedirectURI:    "http://localhost:8080/auth/google/callback",
	})

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodGet,
		"/organizations/acme/projects/internal-tools?access_token=secret",
		nil)

	req.Header.Set("X-Request-ID", "req-123")
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	httpEntry := requireLogEntryAttrs(
		t,
		logBuffer.String(),
		map[string]string{
			"braidplane.event": "http.request.completed",
			"http.route":       "/organizations/:slug/projects/:project_slug",
		},
	)
	assert.Equal(t, "req-123", httpEntry["request_id"])
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", httpEntry["trace_id"])
	assert.Equal(t, "00f067aa0ba902b7", httpEntry["span_id"])
	assert.Equal(t, "/organizations/:slug/projects/:project_slug", httpEntry["http.route"])
	assert.NotContains(t, httpEntry, "url.path")
	assert.NotContains(t, httpEntry, "url.query")

	serviceEntry := requireLogEntry(
		t,
		logBuffer.String(),
		"braidplane.method",
		"service/projectssvc.Service.GetProject",
	)
	assert.Equal(t, "service.method.completed", serviceEntry["braidplane.event"])
	assert.Equal(t, "req-123", serviceEntry["request_id"])
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", serviceEntry["trace_id"])
	assert.Equal(t, "00f067aa0ba902b7", serviceEntry["span_id"])
	assert.Equal(t, "success", serviceEntry["outcome"])

	logs := logBuffer.String()
	assert.NotContains(t, logs, "access_token=secret")
}

func allowAuth(context.Context, authorization.Input) (bool, error) {
	return true, nil
}

func requireLogEntry(
	t *testing.T,
	logs string,
	key string,
	value string,
) map[string]any {
	t.Helper()

	decoder := json.NewDecoder(strings.NewReader(logs))
	for decoder.More() {
		var entry map[string]any
		require.NoError(t, decoder.Decode(&entry))
		if entry[key] == value {
			return entry
		}
	}
	require.Failf(t, "missing log entry", "expected log entry with %s=%q in logs:\n%s", key, value, logs)
	return nil
}

func requireLogEntryAttrs(
	t *testing.T,
	logs string,
	want map[string]string,
) map[string]any {
	t.Helper()

	decoder := json.NewDecoder(strings.NewReader(logs))
	for decoder.More() {
		var entry map[string]any
		require.NoError(t, decoder.Decode(&entry))
		matches := true
		for key, value := range want {
			if entry[key] != value {
				matches = false
				break
			}
		}
		if matches {
			return entry
		}
	}
	require.Failf(t, "missing log entry", "expected log entry with attrs %#v in logs:\n%s", want, logs)
	return nil
}

func TestHTTPFailureKeepsClassificationAndSafeCorrelation(t *testing.T) {
	const canary = "opaque-http-canary"
	var logs bytes.Buffer
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		require.NoError(t, provider.Shutdown(context.Background()))
	})

	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(reqctx.RequestIDMiddleware(), tracing.HTTPMiddleware("test"),
		obslogging.HTTPMiddleware(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})), true))
	app.Post("/failure", func(fiber.Ctx) error {
		return fmt.Errorf("%s: %w", canary, context.DeadlineExceeded)
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/failure?secret="+canary, strings.NewReader(canary))
	req.Header.Set(fiber.HeaderXRequestID, canary)
	req.Header.Set("X-Private", canary)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	requestID := resp.Header.Get(fiber.HeaderXRequestID)
	require.NotEmpty(t, requestID)
	require.NotEqual(t, canary, requestID)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NotContains(t, string(body), canary)
	var response apierror.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &response))
	require.Equal(t, requestID, response.Error.RequestID)
	require.Equal(t, apierror.CodeInternalServerError, response.Error.Code)

	logEntry := requireLogEntry(t, logs.String(), "braidplane.event", "http.request.completed")
	require.Equal(t, requestID, logEntry["request_id"])
	require.Equal(t, "deadline_exceeded", logEntry["error.type"])
	require.NotContains(t, logs.String(), canary)

	require.Len(t, recorder.Ended(), 1)
	span := recorder.Ended()[0]
	require.Equal(t, "deadline_exceeded", span.Status().Description)
	attrs, err := json.Marshal(span.Attributes())
	require.NoError(t, err)
	require.Contains(t, string(attrs), requestID)
	require.NotContains(t, string(attrs), canary)
	require.Empty(t, span.Events())
}
