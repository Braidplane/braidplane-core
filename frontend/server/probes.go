package server

import (
	"context"
	nethttp "net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	frontendReadinessDependencyTimeout = 2 * time.Second
	frontendProbeStatusNotReady        = "not_ready"
)

// frontendProbeLifecycle is the process drain-state boundary used by frontend readiness probes.
type frontendProbeLifecycle interface {
	IsDraining() bool
}

// frontendProbeRoutesConfig contains dependencies needed by the frontend probe routes.
type frontendProbeRoutesConfig struct {
	DatabaseCheck func(context.Context) error
	BackendCheck  func(context.Context) error
	Lifecycle     frontendProbeLifecycle
}

type frontendProbeHTTPResponse struct {
	Status string                        `json:"status"`
	Checks map[string]frontendProbeCheck `json:"checks"`
}

type frontendProbeCheck struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type frontendBackendReadinessProbe struct {
	readyURL string
}

// newFrontendBackendReadinessProbe creates a bounded checker for the backend root readiness route.
func newFrontendBackendReadinessProbe(backendAPIBaseURL string) *frontendBackendReadinessProbe {
	return &frontendBackendReadinessProbe{
		readyURL: strings.TrimRight(backendAPIBaseURL, "/") + "/readyz",
	}
}

// registerFrontendProbeRoutes registers unauthenticated frontend liveness and readiness endpoints.
func registerFrontendProbeRoutes(app *fiber.App, cfg frontendProbeRoutesConfig) {
	app.Get("/healthz", func(c fiber.Ctx) error {
		return c.Status(nethttp.StatusOK).JSON(frontendProbeHTTPResponse{
			Status: "ok",
			Checks: map[string]frontendProbeCheck{
				"process": {Status: "ok"},
			},
		}, fiber.MIMEApplicationJSON)
	})

	app.Get("/readyz", func(c fiber.Ctx) error {
		statusCode := nethttp.StatusOK
		response := frontendProbeHTTPResponse{
			Status: "ready",
			Checks: map[string]frontendProbeCheck{
				"process":  {Status: "ok"},
				"database": {Status: "ok"},
				"backend":  {Status: "ok"},
			},
		}

		if frontendProcessDraining(cfg.Lifecycle) {
			response.Status = frontendProbeStatusNotReady
			response.Checks["process"] = frontendProbeCheck{
				Status: "error",
				Error:  "process draining",
			}
			return c.Status(nethttp.StatusServiceUnavailable).JSON(response, fiber.MIMEApplicationJSON)
		}
		if !frontendDependencyReady(c.Context(), cfg.DatabaseCheck) {
			statusCode = nethttp.StatusServiceUnavailable
			response.Status = frontendProbeStatusNotReady
			response.Checks["database"] = frontendProbeCheck{
				Status: "error",
				Error:  "database unavailable",
			}
		}
		if !frontendDependencyReady(c.Context(), cfg.BackendCheck) {
			statusCode = nethttp.StatusServiceUnavailable
			response.Status = frontendProbeStatusNotReady
			response.Checks["backend"] = frontendProbeCheck{
				Status: "error",
				Error:  "backend not ready",
			}
		}

		return c.Status(statusCode).JSON(response, fiber.MIMEApplicationJSON)
	})
}

// Check checks backend root readiness and maps any non-200 response to a dependency error.
func (p *frontendBackendReadinessProbe) Check(ctx context.Context) error {
	if p == nil || p.readyURL == "" {
		return xerror.Unexpected().Detail("backend readiness probe is required")
	}

	// #nosec G107 -- backend_api_url is operator-managed service configuration, not user input.
	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, p.readyURL, nil)
	if err != nil {
		return xerror.Unexpected().Detailf("create backend readiness request: %w", err)
	}
	resp, err := nethttp.DefaultClient.Do(req)
	if err != nil {
		return xerror.Unexpected().Detailf("check backend readiness: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != nethttp.StatusOK {
		return xerror.Unexpected().Detailf(
			"backend readiness returned HTTP %d",
			resp.StatusCode,
		)
	}

	return nil
}

// frontendProcessDraining reports whether lifecycle state should make readiness fail.
func frontendProcessDraining(lifecycle frontendProbeLifecycle) bool {
	return lifecycle != nil && lifecycle.IsDraining()
}

// frontendDependencyReady checks one dependency with a bounded per-probe timeout.
func frontendDependencyReady(ctx context.Context, check func(context.Context) error) bool {
	if check == nil {
		return false
	}

	checkCtx, cancel := context.WithTimeout(ctx, frontendReadinessDependencyTimeout)
	defer cancel()

	return check(checkCtx) == nil
}
