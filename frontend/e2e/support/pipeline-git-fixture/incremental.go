package main

import (
	"encoding/json"
	"net/http"

	"github.com/gofiber/fiber/v3"
)

var incrementalEnabled bool

func registerIncrementalFixture(app *fiber.App) {
	app.Post("/_test/incremental", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		incrementalEnabled, imported, bound, deployed = true, true, true, true
		return c.SendStatus(http.StatusOK)
	})
}

func handleIncrementalAPI(w http.ResponseWriter, r *http.Request) bool {
	encode := func(value any) {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			panic(err)
		}
	}
	switch r.URL.Path {
	case prefix + "/pipelines/daily-sync", prefix + "/pipelines/daily-sync/versions/" + version:
		var document map[string]any
		if err := json.Unmarshal([]byte(workspacePipeline), &document); err != nil {
			panic(err)
		}
		spec := document["spec"].(map[string]any)
		query := spec["steps"].([]any)[0].(map[string]any)["query"].(map[string]any)
		query["incremental"] = map[string]any{"checkpoint_kind": "watermark", "field": "email"}
		if r.URL.Path == prefix+"/pipelines/daily-sync" {
			encode(document)
		} else {
			encode(map[string]any{"pipeline_slug": "daily-sync", "spec_hash": version, "spec": spec})
		}
	case prefix + "/environments/dev/pipelines/daily-sync:bootstrap-checkpoints":
		var request struct {
			RequestSlug  string                     `json:"request_slug"`
			SourceSeeds  map[string]json.RawMessage `json:"source_seeds"`
			Confirmation string                     `json:"protected_environment_confirmation"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.RequestSlug == "" || len(request.SourceSeeds) != 1 {
			w.WriteHeader(http.StatusBadRequest)
			encode(map[string]any{"error": map[string]string{"code": "INVALID_SEEDS", "message": "Expected one bootstrap seed"}})
			return true
		}
		if request.Confirmation != "dev" {
			w.WriteHeader(http.StatusBadRequest)
			encode(map[string]any{"error": map[string]string{"code": "PROTECTED_ENVIRONMENT_CONFIRMATION_REQUIRED", "message": "protected environment confirmation required"}})
			return true
		}
		w.WriteHeader(http.StatusNoContent)
	case prefix + "/environments/dev/pipelines/daily-sync:execute":
		w.WriteHeader(http.StatusAccepted)
		encode(map[string]any{"metadata": map[string]any{"pipeline_slug": "daily-sync", "run_slug": "backfill-ui-001", "status": "queued", "environment_slug": "dev", "incremental": map[string]any{"mode": "backfill", "checkpoint_status": "skipped"}}})
	default:
		return false
	}
	return true
}
