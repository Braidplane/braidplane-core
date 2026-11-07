package http

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	obslogging "github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
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
