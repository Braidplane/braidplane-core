package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"
)

var completionInspectionScenario string

func registerCompletionInspectionFixture(app *fiber.App) {
	app.Post("/_test/completion/:scenario", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		switch c.Params("scenario") {
		case "retained", "large", "success", "final_failure", "retry_success", "waiting", "changed", "refused", "restricted", "none":
		default:
			return c.SendStatus(http.StatusBadRequest)
		}
		completionInspectionScenario = strings.Clone(c.Params("scenario"))
		runInspectionEnabled = true
		runInspectionStatus = "succeeded"
		imported = true
		return c.SendStatus(http.StatusOK)
	})
}

func handleCompletionInspectionAPI(w http.ResponseWriter, r *http.Request) bool {
	if completionInspectionScenario == "" {
		return false
	}

	encode := func(value any) {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			panic(err)
		}
	}
	const triggers = prefix + "/environments/dev/pipeline-triggers"
	config := map[string]any{"slug": "after-upstream", "name": "After upstream", "type": "completion", "enabled": completionInspectionScenario != "changed", "revision": 9, "pipeline_slug": "normalize-orders", "version_policy": map[string]any{"mode": "latest_deployed"}, "completion": map[string]any{"upstream_pipeline_slug": "daily-sync", "condition": "success", "execution_grant_enabled": true}}
	if r.URL.Path == triggers || r.URL.Path == triggers+"/after-upstream" {
		if completionInspectionScenario == "restricted" {
			w.WriteHeader(http.StatusForbidden)
			encode(map[string]any{"error": map[string]any{"message": "Forbidden"}})
			return true
		}
		if r.URL.Path == triggers {
			values := []any{config}
			if completionInspectionScenario == "none" {
				values = []any{}
			}
			encode(map[string]any{"pipeline_triggers": values, "has_more": false})
		} else {
			encode(config)
		}
		return true
	}
	const runs = prefix + "/environments/dev/runs/"
	if !strings.HasPrefix(r.URL.Path, runs) {
		return false
	}
	path := strings.TrimPrefix(r.URL.Path, runs)
	child := strings.HasPrefix(path, "completion-child")
	if completionInspectionScenario == "retained" && path == "run-inspection" {
		encode(map[string]any{"metadata": map[string]any{"pipeline_slug": "daily-sync", "environment_slug": "dev", "pipeline_version_spec_hash": "immutable-upstream-v1", "run_slug": "run-inspection", "status": "succeeded", "started_at": "2026-10-01T10:00:00Z", "source": map[string]any{"type": "manual", "summary": "Manual run with automatic retry", "entrypoint": "ui", "actor_type": "user", "actor_slug": "fixture-operator", "attempt_number": 2, "root_run_slug": "expired-root", "retry_of_run_slug": "expired-root"}}, "timeline": map[string]any{"events": []any{}, "snapshots": []any{}}, "incremental_diagnostics": map[string]any{"sources": []any{}}})
		return true
	}

	if path == "completion-child" || path == "parent-final" {
		source := map[string]any{"type": "manual", "summary": "Manual run", "attempt_number": 2, "root_run_slug": "run-inspection", "retry_of_run_slug": "run-inspection"}
		pipeline, root := "daily-sync", "parent-final"
		if child {
			pipeline = "normalize-orders"
			root = "completion-child"
			source = map[string]any{"type": "completion", "summary": "Completion-triggered run", "entrypoint": "scheduler", "actor_type": "system", "attempt_number": 1, "root_run_slug": root}
		}
		encode(map[string]any{"metadata": map[string]any{"pipeline_slug": pipeline, "environment_slug": "dev", "pipeline_version_spec_hash": map[bool]string{false: "immutable-upstream-v1", true: "immutable-selected-v2"}[child], "run_slug": root, "status": "succeeded", "started_at": "2026-10-01T10:00:00Z", "source": source}, "timeline": map[string]any{"events": []any{}, "snapshots": []any{}}, "incremental_diagnostics": map[string]any{"sources": []any{}}})
		return true
	}
	if path == "completion-child/trigger-retry-attempts" || path == "parent-final/trigger-retry-attempts" {
		encode(map[string]any{"retry_attempts": []any{}})
		return true
	}
	if path != "run-inspection/completion" && path != "completion-child/completion" && path != "parent-final/completion" && !strings.HasSuffix(path, "/connected") {
		return false
	}
	outcome := "succeeded"
	condition := "success"
	if completionInspectionScenario == "final_failure" {
		outcome = "failed"
		condition = "final_failure"
	}
	decision := map[string]any{"trigger_slug": "after-upstream", "trigger_revision": 7, "upstream_pipeline_slug": "daily-sync", "root_run_slug": "run-inspection", "final_attempt_run_slug": "parent-final", "downstream_pipeline_slug": "normalize-orders", "condition": condition, "state": "accepted", "decision_reason": nil, "child_run_slug": "completion-child", "child_status": "succeeded", "child_outcome": "succeeded", "selected_version_hash": "immutable-selected-v2", "selected_deployment_revision": 4, "selected_resolution_digest": "sha256:selected-v2"}
	if completionInspectionScenario == "changed" || completionInspectionScenario == "refused" {
		decision["state"] = "refused"
		decision["child_run_slug"] = nil
		decision["child_status"] = nil
		decision["child_outcome"] = nil
		decision["decision_reason"] = "trigger_changed"
		if completionInspectionScenario == "refused" {
			decision["decision_reason"] = "deployment_missing"
			decision["selected_version_hash"] = nil
			decision["selected_deployment_revision"] = nil
			decision["selected_resolution_digest"] = nil
		}
	}
	root := "run-inspection"
	final := any("parent-final")
	attempts := []any{map[string]any{"run_slug": "run-inspection", "attempt_number": 1, "status": "failed", "version_hash": "immutable-upstream-v1", "deployment_revision": 2}, map[string]any{"run_slug": "parent-final", "attempt_number": 2, "status": outcome, "version_hash": "immutable-upstream-v1", "deployment_revision": 2}}
	closed, waiting := true, false
	parent := any(nil)
	children := []any{decision}
	if child {
		root = "completion-child"
		final = "completion-child"
		parent = decision
		children = []any{}
		attempts = []any{map[string]any{"run_slug": root, "attempt_number": 1, "status": "succeeded", "version_hash": "immutable-selected-v2", "deployment_revision": 4}}
	}
	if completionInspectionScenario == "waiting" {
		attempts = attempts[:1]
		closed = false
		waiting = true
		final = nil
		children = []any{}
	}
	if completionInspectionScenario == "restricted" || completionInspectionScenario == "none" {
		parent = nil
		children = []any{}
	}
	body := map[string]any{"root_run_slug": root, "closed": closed, "waiting_for_retry": waiting, "outcome": outcome, "final_attempt_run_slug": final, "decision_reason": nil, "attempts": attempts, "parent": parent, "children": children}
	if !closed {
		body["outcome"] = nil
	}
	if strings.HasSuffix(path, "/connected") {
		decision["child_status"] = "failed"
		decision["child_outcome"] = "failed"
		// The connected fixture retains historical choices while current config changes.
		parentBody := map[string]any{"root_run_slug": "run-inspection", "closed": closed, "waiting_for_retry": waiting, "outcome": body["outcome"], "final_attempt_run_slug": final, "attempts": attempts, "children": children}
		if child {
			parentBody["closed"] = true
			parentBody["waiting_for_retry"] = false
			parentBody["outcome"] = "succeeded"
			parentBody["final_attempt_run_slug"] = "parent-final"
			parentBody["attempts"] = []any{map[string]any{"run_slug": "run-inspection", "attempt_number": 1, "status": "failed", "version_hash": "immutable-upstream-v1", "deployment_revision": 2}, map[string]any{"run_slug": "parent-final", "attempt_number": 2, "status": "succeeded", "version_hash": "immutable-upstream-v1", "deployment_revision": 2}}
			parentBody["children"] = []any{decision}
		}
		nodes := []any{map[string]any{"pipeline_slug": "daily-sync", "completion": parentBody}}
		if decision["child_run_slug"] != nil && completionInspectionScenario != "waiting" && completionInspectionScenario != "restricted" && completionInspectionScenario != "none" {
			nodes = append(nodes, map[string]any{"pipeline_slug": "normalize-orders", "completion": map[string]any{"root_run_slug": "completion-child", "closed": true, "waiting_for_retry": false, "outcome": "failed", "final_attempt_run_slug": "completion-child", "parent": decision, "children": []any{}, "attempts": []any{map[string]any{"run_slug": "completion-child", "attempt_number": 1, "status": "failed", "version_hash": "immutable-selected-v2", "deployment_revision": 4}}}})
		}
		if completionInspectionScenario == "retained" {
			nodes = []any{map[string]any{"pipeline_slug": "daily-sync", "completion": map[string]any{"root_run_slug": "expired-root", "closed": true, "outcome": "succeeded", "final_attempt_run_slug": "run-inspection", "children": []any{}, "attempts": []any{map[string]any{"run_slug": "run-inspection", "attempt_number": 2, "status": "succeeded", "version_hash": "immutable-upstream-v1", "deployment_revision": 2}}}}}
		}
		if completionInspectionScenario == "large" {
			nodes = []any{}
			for i := 0; i < 40; i++ {
				root := fmt.Sprintf("large-root-%d", i)
				if i == 0 {
					root = "run-inspection"
				}
				children := []any{}
				if i < 39 {
					children = append(children, map[string]any{"trigger_slug": fmt.Sprintf("next-%d", i), "trigger_revision": 7, "upstream_pipeline_slug": fmt.Sprintf("pipeline-%d", i), "root_run_slug": root, "final_attempt_run_slug": root, "downstream_pipeline_slug": fmt.Sprintf("pipeline-%d", i+1), "condition": "success", "state": "accepted", "child_run_slug": fmt.Sprintf("large-root-%d", i+1)})
				}
				nodes = append(nodes, map[string]any{"pipeline_slug": fmt.Sprintf("pipeline-%d", i), "completion": map[string]any{"root_run_slug": root, "closed": true, "outcome": "succeeded", "attempts": []any{map[string]any{"run_slug": root, "attempt_number": 1, "status": "succeeded", "version_hash": "immutable-large-v1"}}, "children": children}})
			}
		}
		encode(map[string]any{"selected_run_slug": strings.TrimSuffix(path, "/connected"), "nodes": nodes, "truncated": completionInspectionScenario == "large", "context_unavailable": completionInspectionScenario == "restricted"})
		return true
	}
	encode(body)
	return true
}
