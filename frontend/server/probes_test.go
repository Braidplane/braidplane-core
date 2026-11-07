package server

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

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/lifecycle"
	obslogging "github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestFrontendProbeRoutesHealthzIsShallow(t *testing.T) {
	t.Parallel()

	db := &fakeFrontendReadinessProbe{
		checkFn: func(context.Context) error {
			t.Fatal("frontend liveness probe must not check database connectivity")
			return nil
		},
	}
	backend := &fakeFrontendReadinessProbe{
		checkFn: func(context.Context) error {
			t.Fatal("frontend liveness probe must not check backend readiness")
			return nil
		},
	}
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	registerFrontendProbeRoutes(app, frontendProbeRoutesConfig{
		DatabaseCheck: db.Check,
		BackendCheck:  backend.Check,
	})

	resp := doFrontendProbeRequest(t, app, http.MethodGet, "/healthz")
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	require.Equal(t, 0, db.calls)
	require.Equal(t, 0, backend.calls)

	body := decodeFrontendProbeResponse(t, resp)
	require.Equal(t, "ok", body.Status)
	require.Equal(t, frontendProbeCheckResponse{Status: "ok"}, body.Checks["process"])
}

func TestFrontendProbeRoutesReadyzReportsHealthyDependencies(t *testing.T) {
	t.Parallel()

	db := &fakeFrontendReadinessProbe{checkFn: func(context.Context) error { return nil }}
	backend := &fakeFrontendReadinessProbe{checkFn: func(context.Context) error { return nil }}
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	registerFrontendProbeRoutes(app, frontendProbeRoutesConfig{
		DatabaseCheck: db.Check,
		BackendCheck:  backend.Check,
	})

	resp := doFrontendProbeRequest(t, app, http.MethodGet, "/readyz")
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 1, db.calls)
	require.Equal(t, 1, backend.calls)

	body := decodeFrontendProbeResponse(t, resp)
	require.Equal(t, "ready", body.Status)
	require.Equal(t, frontendProbeCheckResponse{Status: "ok"}, body.Checks["process"])
	require.Equal(t, frontendProbeCheckResponse{Status: "ok"}, body.Checks["database"])
	require.Equal(t, frontendProbeCheckResponse{Status: "ok"}, body.Checks["backend"])
}

func TestFrontendProbeRoutesReadyzReportsDependencyFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		databaseErr error
		backendErr  error
		wantChecks  map[string]frontendProbeCheckResponse
	}{
		{
			name:        "DatabaseUnavailable",
			databaseErr: xerror.Unexpected().Detail("database down"),
			wantChecks: map[string]frontendProbeCheckResponse{
				"process":  {Status: "ok"},
				"database": {Status: "error", Error: "database unavailable"},
				"backend":  {Status: "ok"},
			},
		},
		{
			name:       "BackendNotReady",
			backendErr: xerror.Unexpected().Detail("backend not ready"),
			wantChecks: map[string]frontendProbeCheckResponse{
				"process":  {Status: "ok"},
				"database": {Status: "ok"},
				"backend":  {Status: "error", Error: "backend not ready"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db := &fakeFrontendReadinessProbe{
				checkFn: func(context.Context) error { return tt.databaseErr },
			}
			backend := &fakeFrontendReadinessProbe{
				checkFn: func(context.Context) error { return tt.backendErr },
			}
			app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
			registerFrontendProbeRoutes(app, frontendProbeRoutesConfig{
				DatabaseCheck: db.Check,
				BackendCheck:  backend.Check,
			})

			resp := doFrontendProbeRequest(t, app, http.MethodGet, "/readyz")
			t.Cleanup(func() {
				require.NoError(t, resp.Body.Close())
			})

			require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
			require.Equal(t, 1, db.calls)
			require.Equal(t, 1, backend.calls)

			body := decodeFrontendProbeResponse(t, resp)
			require.Equal(t, "not_ready", body.Status)
			require.Equal(t, tt.wantChecks, body.Checks)
		})
	}
}

func TestFrontendProbeRoutesReadyzReportsDrainingProcessBeforeDependencies(t *testing.T) {
	t.Parallel()

	drain := lifecycle.NewDrainLifecycle()
	require.True(t, drain.BeginDrain())

	db := &fakeFrontendReadinessProbe{
		checkFn: func(context.Context) error {
			t.Fatal("frontend readiness must not check database connectivity after drain starts")
			return nil
		},
	}
	backend := &fakeFrontendReadinessProbe{
		checkFn: func(context.Context) error {
			t.Fatal("frontend readiness must not check backend readiness after drain starts")
			return nil
		},
	}
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	registerFrontendProbeRoutes(app, frontendProbeRoutesConfig{
		DatabaseCheck: db.Check,
		BackendCheck:  backend.Check,
		Lifecycle:     drain,
	})

	resp := doFrontendProbeRequest(t, app, http.MethodGet, "/readyz")
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, 0, db.calls)
	require.Equal(t, 0, backend.calls)

	body := decodeFrontendProbeResponse(t, resp)
	require.Equal(t, "not_ready", body.Status)
	require.Equal(t, frontendProbeCheckResponse{
		Status: "error",
		Error:  "process draining",
	}, body.Checks["process"])
	require.Equal(t, frontendProbeCheckResponse{Status: "ok"}, body.Checks["database"])
	require.Equal(t, frontendProbeCheckResponse{Status: "ok"}, body.Checks["backend"])
}

func TestFrontendProbeRoutesBypassAuthRedirectsAndRequestLogging(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	registerFrontendProbeRoutes(app, frontendProbeRoutesConfig{
		DatabaseCheck: (&fakeFrontendReadinessProbe{
			checkFn: func(context.Context) error { return nil },
		}).Check,
		BackendCheck: (&fakeFrontendReadinessProbe{
			checkFn: func(context.Context) error { return nil },
		}).Check,
	})
	app.Use(obslogging.HTTPMiddleware(logger, false))
	app.Use(func(c fiber.Ctx) error {
		return c.Redirect().Status(fiber.StatusFound).To("/auth/login")
	})

	for _, path := range []string{"/healthz", "/readyz"} {
		resp := doFrontendProbeRequest(t, app, http.MethodGet, path)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Empty(t, resp.Header.Get("Location"))
		require.NoError(t, resp.Body.Close())
	}
	require.Empty(t, logBuffer.String())
}

type fakeFrontendReadinessProbe struct {
	checkFn func(context.Context) error
	calls   int
}

func (f *fakeFrontendReadinessProbe) Check(ctx context.Context) error {
	f.calls++
	return f.checkFn(ctx)
}

type frontendProbeResponse struct {
	Status string                                `json:"status"`
	Checks map[string]frontendProbeCheckResponse `json:"checks"`
}

type frontendProbeCheckResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func doFrontendProbeRequest(
	t *testing.T,
	app *fiber.App,
	method string,
	path string,
) *http.Response {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	return resp
}

func decodeFrontendProbeResponse(t *testing.T, resp *http.Response) frontendProbeResponse {
	t.Helper()

	var body frontendProbeResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotNil(t, body.Checks)
	return body
}
