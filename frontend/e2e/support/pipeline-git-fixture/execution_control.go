package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

var executionControl *api.PipelineExecutionControl

func registerExecutionControlFixture(app *fiber.App) {
	app.Post("/_test/execution-control/:phase", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		switch c.Params("phase") {
		case "ready":
			imported, bound, deployed = true, true, true
			at := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
			executionControl = &api.PipelineExecutionControl{
				QueuedCount: 1, ActiveCount: 1, UncertainCount: 1,
				Attempts: []api.PipelineExecutionControlAttempt{
					{RunSlug: "writing", Phase: "active", Status: "running", SourceType: "manual", StartedAt: at, OutcomeUncertain: true},
					{RunSlug: "waiting", Phase: "queued", Status: "queued", SourceType: "schedule", StartedAt: at},
				},
			}
		case "drained":
			if executionControl == nil || !executionControl.Paused || executionControl.ActiveCount == 0 {
				return c.SendStatus(http.StatusConflict)
			}
			executionControl.Drained, executionControl.ActiveCount, executionControl.FinishedCount = true, 0, 1
			attempt := &executionControl.Attempts[0]
			attempt.Phase, attempt.Status = "finished", "failed"
		default:
			return c.SendStatus(http.StatusBadRequest)
		}
		return c.SendStatus(http.StatusNoContent)
	})
}

func handleExecutionControlAPI(w http.ResponseWriter, r *http.Request) bool {
	if executionControl == nil || r.URL.Path != prefix+"/environments/dev/pipelines/daily-sync/execution-control" {
		return false
	}
	if r.Method == http.MethodPut {
		var change api.PipelineExecutionControlUpdate
		if err := json.NewDecoder(r.Body).Decode(&change); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		if executionControl.Revision != change.ExpectedRevision {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"CONFLICT","message":"Reload status"}}`))
			return true
		}
		if executionControl.Paused != change.Paused {
			executionControl.Paused = change.Paused
			executionControl.Revision++
			if !change.Paused && executionControl.Drained {
				executionControl.Drained, executionControl.FinishedCount, executionControl.UncertainCount = false, 0, 0
				executionControl.Attempts = executionControl.Attempts[1:]
			}
		}
	}
	if err := json.NewEncoder(w).Encode(executionControl); err != nil {
		panic(err)
	}
	return true
}
