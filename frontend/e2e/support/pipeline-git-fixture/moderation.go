package main

import (
	"encoding/json"
	"net/http"

	"github.com/gofiber/fiber/v3"
)

var moderationFixture bool

// One local fixture changes cases between participants without touching staging.
func registerModerationFixture(app *fiber.App) {
	app.Post("/_test/moderation/:state", func(c fiber.Ctx) error {
		state := c.Params("state")
		if state != "healthy" && state != "failed" && state != "unconfigured" {
			return c.SendStatus(http.StatusBadRequest)
		}
		mu.Lock()
		defer mu.Unlock()
		moderationFixture, imported = true, true
		bound, deployed = state != "unconfigured", state != "unconfigured"
		runInspectionEnabled = state != "unconfigured"
		runInspectionStatus = "succeeded"
		if state == "failed" {
			runInspectionStatus = "failed"
		}
		runInspectionFailure, runInspectionPhase = "", ""
		runInspectionHistory, runInspectionDeploymentChanged = false, false
		return c.SendStatus(http.StatusNoContent)
	})
}

func handleModerationAPI(w http.ResponseWriter, r *http.Request) bool {
	if !moderationFixture {
		return false
	}
	var response any
	switch r.URL.Path {
	case "/api/organizations/acme/roles/admin":
		response = map[string]any{"slug": "admin", "permissions": []any{map[string]string{"resource": "*", "action": "read"}}}
	case prefix + "/environments/dev/pipeline-deployments":
		deployments := []any{}
		if deployed {
			deployments = append(deployments, map[string]string{"pipeline_slug": "daily-sync", "environment_slug": "dev", "current_version_spec_hash": version, "current_version_short_spec_hash": version[:12]})
		}
		response = map[string]any{"deployments": deployments}
	default:
		return false
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		panic(err)
	}
	return true
}
