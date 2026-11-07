package http

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/lifecycle"
	obslogging "github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestProbeRoutesHealthzIsShallow(t *testing.T) {
	t.Parallel()

	db := &fakeProbeDatabase{
		pingFn: func(context.Context) error {
			t.Fatal("liveness probe must not check database connectivity")
			return nil
		},
	}
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	RegisterProbeRoutes(app, ProbeRoutesConfig{Database: db})

	resp := doProbeRequest(t, app, nethttp.MethodGet, "/healthz")
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	require.Equal(t, nethttp.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))
	require.Equal(t, 0, db.calls)

	body := decodeProbeResponse(t, resp)
	require.Equal(t, "ok", body.Status)
	require.Equal(t, probeCheckResponse{Status: "ok"}, body.Checks["process"])
}

func TestProbeRoutesReadyzReportsHealthyDatabase(t *testing.T) {
	t.Parallel()

	db := &fakeProbeDatabase{pingFn: func(context.Context) error { return nil }}
	migrations := &fakeProbeMigrationPreflight{checkFn: func(context.Context) error { return nil }}
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	RegisterProbeRoutes(app, ProbeRoutesConfig{Database: db, Migrations: migrations})

	resp := doProbeRequest(t, app, nethttp.MethodGet, "/readyz")
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	require.Equal(t, nethttp.StatusOK, resp.StatusCode)
	require.Equal(t, 1, db.calls)
	require.Equal(t, 1, migrations.calls)

	body := decodeProbeResponse(t, resp)
	require.Equal(t, "ready", body.Status)
	require.Equal(t, probeCheckResponse{Status: "ok"}, body.Checks["process"])
	require.Equal(t, probeCheckResponse{Status: "ok"}, body.Checks["database"])
	require.Equal(t, probeCheckResponse{Status: "ok"}, body.Checks["migrations"])
}

func TestProbeRoutesReadyzReportsDrainingProcessBeforeDependencies(t *testing.T) {
	t.Parallel()

	drain := lifecycle.NewDrainLifecycle()
	require.True(t, drain.BeginDrain())

	db := &fakeProbeDatabase{
		pingFn: func(context.Context) error {
			t.Fatal("readiness must not check database connectivity after drain starts")
			return nil
		},
	}
	migrations := &fakeProbeMigrationPreflight{
		checkFn: func(context.Context) error {
			t.Fatal("readiness must not check migrations after drain starts")
			return nil
		},
	}
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	RegisterProbeRoutes(app, ProbeRoutesConfig{
		Database:   db,
		Migrations: migrations,
		Lifecycle:  drain,
	})

	resp := doProbeRequest(t, app, nethttp.MethodGet, "/readyz")
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	require.Equal(t, nethttp.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, 0, db.calls)
	require.Equal(t, 0, migrations.calls)

	body := decodeProbeResponse(t, resp)
	require.Equal(t, "not_ready", body.Status)
	require.Equal(t, probeCheckResponse{
		Status: "error",
		Error:  "process draining",
	}, body.Checks["process"])
	require.Equal(t, probeCheckResponse{Status: "ok"}, body.Checks["database"])
	require.Equal(t, probeCheckResponse{Status: "ok"}, body.Checks["migrations"])
}

func TestProbeRoutesReadyzReportsDatabaseFailure(t *testing.T) {
	t.Parallel()

	db := &fakeProbeDatabase{
		pingFn: func(context.Context) error {
			return xerror.Unexpected().Detail("password authentication failed for user app")
		},
	}
	migrations := &fakeProbeMigrationPreflight{checkFn: func(context.Context) error { return nil }}
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	RegisterProbeRoutes(app, ProbeRoutesConfig{Database: db, Migrations: migrations})

	resp := doProbeRequest(t, app, nethttp.MethodGet, "/readyz")
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	require.Equal(t, nethttp.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, 1, db.calls)
	require.Equal(t, 1, migrations.calls)

	body := decodeProbeResponse(t, resp)
	require.Equal(t, "not_ready", body.Status)
	require.Equal(t, probeCheckResponse{Status: "error", Error: "database unavailable"}, body.Checks["database"])
	require.Equal(t, probeCheckResponse{Status: "ok"}, body.Checks["migrations"])
}

func TestProbeRoutesReadyzReportsMigrationFailure(t *testing.T) {
	t.Parallel()

	db := &fakeProbeDatabase{pingFn: func(context.Context) error { return nil }}
	migrations := &fakeProbeMigrationPreflight{
		checkFn: func(context.Context) error {
			return xerror.Unexpected().Detail("schema_migrations is dirty")
		},
	}
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	RegisterProbeRoutes(app, ProbeRoutesConfig{Database: db, Migrations: migrations})

	resp := doProbeRequest(t, app, nethttp.MethodGet, "/readyz")
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	require.Equal(t, nethttp.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, 1, db.calls)
	require.Equal(t, 1, migrations.calls)

	body := decodeProbeResponse(t, resp)
	require.Equal(t, "not_ready", body.Status)
	require.Equal(t, probeCheckResponse{Status: "ok"}, body.Checks["database"])
	require.Equal(t, probeCheckResponse{Status: "error", Error: "migrations not current"}, body.Checks["migrations"])
}

func TestProbeRoutesBypassAuthRedirectsAndRequestLogging(t *testing.T) {
	t.Parallel()

	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuffer, &slog.HandlerOptions{Level: slog.LevelInfo}))
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	RegisterProbeRoutes(app, ProbeRoutesConfig{
		Database:   &fakeProbeDatabase{pingFn: func(context.Context) error { return nil }},
		Migrations: &fakeProbeMigrationPreflight{checkFn: func(context.Context) error { return nil }},
	})
	app.Use(obslogging.HTTPMiddleware(logger, false))
	app.Use(func(c fiber.Ctx) error {
		return c.Redirect().Status(fiber.StatusFound).To("/login")
	})

	for _, path := range []string{"/healthz", "/readyz"} {
		resp := doProbeRequest(t, app, nethttp.MethodGet, path)
		require.Equal(t, nethttp.StatusOK, resp.StatusCode)
		require.Empty(t, resp.Header.Get("Location"))
		require.NoError(t, resp.Body.Close())
	}
	require.Empty(t, logBuffer.String())
}

type fakeProbeDatabase struct {
	pingFn func(context.Context) error
	calls  int
}

func (f *fakeProbeDatabase) Ping(ctx context.Context) error {
	f.calls++
	return f.pingFn(ctx)
}

type fakeProbeMigrationPreflight struct {
	checkFn func(context.Context) error
	calls   int
}

func (f *fakeProbeMigrationPreflight) Check(ctx context.Context) error {
	f.calls++
	return f.checkFn(ctx)
}

type probeResponse struct {
	Status string                        `json:"status"`
	Checks map[string]probeCheckResponse `json:"checks"`
}

type probeCheckResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func doProbeRequest(
	t *testing.T,
	app *fiber.App,
	method string,
	path string,
) *nethttp.Response {
	t.Helper()

	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	return resp
}

func decodeProbeResponse(t *testing.T, resp *nethttp.Response) probeResponse {
	t.Helper()

	var body probeResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotNil(t, body.Checks)
	return body
}
