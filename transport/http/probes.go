package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v3"
)

const (
	readinessDependencyTimeout = 2 * time.Second
	probeStatusNotReady        = "not_ready"
)

// ProbeDatabase is the database dependency boundary used by readiness probes.
type ProbeDatabase interface {
	Ping(context.Context) error
}

// ProbeMigrationPreflight is the migration-state dependency boundary used by
// readiness probes.
type ProbeMigrationPreflight interface {
	Check(context.Context) error
}

// ProbeLifecycle is the process drain-state boundary used by readiness probes.
type ProbeLifecycle interface {
	IsDraining() bool
}

// ProbeRoutesConfig contains dependencies needed by the backend probe routes.
type ProbeRoutesConfig struct {
	Database   ProbeDatabase
	Migrations ProbeMigrationPreflight
	Lifecycle  ProbeLifecycle
}

type probeHTTPResponse struct {
	Status string                    `json:"status"`
	Checks map[string]probeHTTPCheck `json:"checks"`
}

type probeHTTPCheck struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// RegisterProbeRoutes registers unauthenticated backend liveness and readiness endpoints.
func RegisterProbeRoutes(app *fiber.App, cfg ProbeRoutesConfig) {
	app.Get("/healthz", func(c fiber.Ctx) error {
		return c.Status(http.StatusOK).JSON(probeHTTPResponse{
			Status: "ok",
			Checks: map[string]probeHTTPCheck{
				"process": {Status: "ok"},
			},
		}, fiber.MIMEApplicationJSON)
	})

	app.Get("/readyz", func(c fiber.Ctx) error {
		statusCode := http.StatusOK
		response := probeHTTPResponse{
			Status: "ready",
			Checks: map[string]probeHTTPCheck{
				"process":    {Status: "ok"},
				"database":   {Status: "ok"},
				"migrations": {Status: "ok"},
			},
		}

		if processDraining(cfg.Lifecycle) {
			response.Status = probeStatusNotReady
			response.Checks["process"] = probeHTTPCheck{
				Status: "error",
				Error:  "process draining",
			}
			return c.Status(http.StatusServiceUnavailable).JSON(response, fiber.MIMEApplicationJSON)
		}

		if !databaseReady(c.Context(), cfg.Database) {
			statusCode = http.StatusServiceUnavailable
			response.Status = probeStatusNotReady
			response.Checks["database"] = probeHTTPCheck{
				Status: "error",
				Error:  "database unavailable",
			}
		}
		if !migrationsReady(c.Context(), cfg.Migrations) {
			statusCode = http.StatusServiceUnavailable
			response.Status = probeStatusNotReady
			response.Checks["migrations"] = probeHTTPCheck{
				Status: "error",
				Error:  "migrations not current",
			}
		}

		return c.Status(statusCode).JSON(response, fiber.MIMEApplicationJSON)
	})
}

// processDraining reports whether lifecycle state should make readiness fail.
func processDraining(lifecycle ProbeLifecycle) bool {
	return lifecycle != nil && lifecycle.IsDraining()
}

// databaseReady checks PostgreSQL reachability with a bounded per-probe timeout.
func databaseReady(ctx context.Context, database ProbeDatabase) bool {
	if database == nil {
		return false
	}

	checkCtx, cancel := context.WithTimeout(ctx, readinessDependencyTimeout)
	defer cancel()

	return database.Ping(checkCtx) == nil
}

// migrationsReady checks schema migration state with a bounded per-probe
// timeout.
func migrationsReady(ctx context.Context, migrations ProbeMigrationPreflight) bool {
	if migrations == nil {
		return false
	}

	checkCtx, cancel := context.WithTimeout(ctx, readinessDependencyTimeout)
	defer cancel()

	return migrations.Check(checkCtx) == nil
}
