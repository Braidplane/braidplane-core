package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/gofiber/fiber/v3"
)

var (
	runInspectionRevision          int64 = 1
	runInspectionPhase             string
	runInspectionStartedAt         time.Time
	runInspectionEndedAt           time.Time
	runInspectionWorker            string
	runInspectionHistory           bool
	runInspectionDeploymentChanged bool
	runInspectionEnabled           bool
	runInspectionTopology          string
	runInspectionContractFailure   bool
	runInspectionProtectedRecovery bool
	runInspectionCheckpoint        string
	runInspectionStatus            = "succeeded"
	runInspectionFailure           string
	runInspectionDelivery          string
	runInspectionDefinitionPruned  bool
	runInspectionTimezone          string
	runInspectionS3Statement       = json.RawMessage(`{"type":"s3","operation":"write","key":"exports/orders.csv","format":"csv","content":"{{input}}","overwrite":false,"metadata":{"source":"fixture"}}`)
)

func registerRunInspectionFixture(app *fiber.App) {
	app.Post("/_test/progress/:state", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		runInspectionWorker = strings.Clone(c.Params("state"))
		return c.SendStatus(http.StatusOK)
	})

	app.Post("/_test/run/:status", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		phase := strings.Clone(c.Query("phase"))
		if phase != "" && phase != "fetch" && phase != "normalize" && phase != "store" {
			return c.SendStatus(http.StatusBadRequest)
		}
		if phase != "" && (runInspectionPhase == "" || runInspectionStatus != "running") {
			runInspectionStartedAt = time.Now().UTC().Add(-30 * time.Second)
		}
		if phase != "" && c.Params("status") != "running" {
			runInspectionEndedAt = time.Now().UTC()
		}
		switch c.Params("status") {
		case "succeeded", "failed", "running", "queued":
			runInspectionStatus = strings.Clone(c.Params("status"))
		default:
			return c.SendStatus(http.StatusBadRequest)
		}
		runInspectionHistory = c.Query("history") == "true"
		runInspectionDeploymentChanged = c.Query("deployment") == "changed"
		runInspectionTopology = strings.Clone(c.Query("topology"))
		runInspectionContractFailure = c.Query("contract_failure") == "true"
		runInspectionProtectedRecovery = c.Query("protected_recovery") == "true"
		runInspectionCheckpoint = strings.Clone(c.Query("checkpoint"))
		runInspectionFailure = strings.Clone(c.Query("failure"))
		runInspectionDelivery = strings.Clone(c.Query("delivery"))
		runInspectionDefinitionPruned = c.Query("definition") == "pruned"
		runInspectionTimezone = strings.Clone(c.Query("timezone", "Europe/Warsaw"))
		runInspectionPhase = phase
		runInspectionRevision++
		runInspectionEnabled = true
		imported = true
		return c.SendStatus(http.StatusOK)
	})
}

func handleRunInspectionAPI(w http.ResponseWriter, r *http.Request) bool {
	if handleCompletionInspectionAPI(w, r) {
		return true
	}
	encode := func(value any) {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			panic(err)
		}
	}
	if runInspectionFailure == "s3-provider" {
		query := map[string]any{"slug": "write-object", "name": "Write S3 object", "data_source_slug": "warehouse", "query_statement": runInspectionS3Statement, "parameters": []any{}, "row_limit": 100, "timeout_seconds": 30}
		source := map[string]any{"slug": "warehouse", "name": "S3 storage", "type": "s3", "is_active": true, "read_only": false}
		switch r.URL.Path {
		case "/api/organizations/acme/authorization/permissions":
			encode(map[string]any{"slug": "admin", "permissions": []any{map[string]string{"resource": "pipelines", "action": "read"}, map[string]string{"resource": "datasources", "action": "read"}, map[string]string{"resource": "queries", "action": "read"}, map[string]string{"resource": "queries", "action": "update"}}})
			return true
		case "/api/organizations/acme/data-sources":
			encode(map[string]any{"data_sources": []any{source}})
			return true
		case "/api/organizations/acme/data-sources/warehouse":
			encode(source)
			return true
		case "/api/organizations/acme/data-sources/warehouse/queries":
			encode(map[string]any{"queries": []any{query}})
			return true
		case "/api/organizations/acme/data-sources/warehouse/queries/write-object":
			if r.Method == http.MethodPut {
				var body struct {
					Statement json.RawMessage `json:"query_statement"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || !json.Valid(body.Statement) {
					w.WriteHeader(http.StatusBadRequest)
					return true
				}
				runInspectionS3Statement = body.Statement
				query["query_statement"] = runInspectionS3Statement
			}
			encode(query)
			return true
		}
	}
	const runs = prefix + "/environments/dev/runs"
	metadata := map[string]any{
		"pipeline_slug": "daily-sync", "environment_slug": "dev", "pipeline_version_spec_hash": version, "run_slug": "run-inspection", "status": runInspectionStatus,
		"started_at": "2026-10-01T10:00:00Z", "duration_ms": 2500, "error_summary": "",
		"source": map[string]any{"type": "manual", "summary": "Manual run", "attempt_number": 1},
	}
	if completionInspectionScenario != "" {
		metadata["pipeline_version_spec_hash"] = "immutable-upstream-v1"
	}
	if runInspectionCheckpoint != "" {
		metadata["incremental"] = api.PipelineRunIncrementalSummary{
			Mode:                api.Incremental,
			CheckpointStatus:    api.PipelineRunCheckpointStatusBlocked,
			BlockedReason:       "The sink did not confirm delivery.",
			ReplayBlockedReason: "Verify the provider outcome before retrying.",
		}
	}
	timeline := map[string]any{
		"compiled_plan_digest":         "sha256:executed-plan",
		"deployment_resolution_digest": "sha256:recorded-resolution",
		"structural_checks": []any{map[string]any{
			"id": "check-orders", "step": "store", "input": "orders", "origin": "operation", "phase": "pre_sink", "declaration": "operation/pre_sink", "projection_digest": "sha256:projection", "compiled_plan_digest": "sha256:executed-plan", "deployment_resolution_digest": "sha256:recorded-resolution", "attempt_number": 1, "evaluator": "structural-v1", "policy": "policy-v1", "target": "original-warehouse", "status": "passed", "reason_code": "conformant", "window": map[string]any{"kind": "full", "has_lower_bound": false, "has_upper_bound": false, "source_steps": []string{"fetch"}},
		}},
		"events":    []any{map[string]any{"at": "2026-10-01T10:00:00Z", "type": "step_succeeded", "step": "store", "status": "succeeded"}},
		"snapshots": []any{map[string]any{"step": "store", "kind": "sink", "input": map[string]any{"data_source_slug": "warehouse", "operation_type": "sql", "operation_ref": "upsert-customers", "parameter_keys": []string{"tenant_id"}, "sink_delivery_outcome": "confirmed", "sink_replay_strategy": "naturally_idempotent"}}},
	}
	if runInspectionTopology == "branched" {
		metadata["step_count"], metadata["ended_at"], metadata["duration_ms"] = 6, "2026-10-01T10:00:03Z", 3000
		timeline["structural_checks"] = []any{}
		start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
		events := make([]any, 0, 12)
		for _, step := range []struct {
			slug          string
			start, finish time.Duration
		}{
			{"read-0", 0, 300 * time.Millisecond},
			{"normalize-0", 300 * time.Millisecond, 1100 * time.Millisecond},
			{"read-1", 100 * time.Millisecond, 500 * time.Millisecond},
			{"normalize-1", 500 * time.Millisecond, 1500 * time.Millisecond},
			{"combine", 1500 * time.Millisecond, 2500 * time.Millisecond},
			{"store", 2500 * time.Millisecond, 3000 * time.Millisecond},
		} {
			events = append(events,
				map[string]any{"at": start.Add(step.start), "type": "step_started", "step": step.slug, "status": "running"},
				map[string]any{"at": start.Add(step.finish), "type": "step_succeeded", "step": step.slug, "status": "succeeded"},
			)
		}
		timeline["events"] = events
	}
	if runInspectionStatus == "failed" {
		metadata["ended_at"] = "2026-10-01T10:00:02Z"
		metadata["duration_ms"] = 2000
		metadata["step_slug"] = "store"
		metadata["error_summary"] = "Provider acknowledgement unavailable"
		metadata["source"] = map[string]any{"type": "manual", "summary": "Manual retry", "attempt_number": 2, "root_run_slug": "run-root", "retry_of_run_slug": "run-root"}
		timeline["failure"] = map[string]any{"step": "store", "side": "sink", "kind": "data_source", "reason_code": "sink_delivery_unknown", "error": "Provider acknowledgement unavailable", "at": "2026-10-01T10:00:02Z"}
		timeline["events"] = []any{
			map[string]any{"at": "2026-10-01T10:00:00Z", "type": "step_started", "step": "normalize", "status": "running"},
			map[string]any{"at": "2026-10-01T10:00:02Z", "type": "step_failed", "step": "store", "status": "failed"},
		}
		timeline["snapshots"].([]any)[0].(map[string]any)["input"].(map[string]any)["sink_delivery_outcome"] = "unknown"
	}
	switch runInspectionDelivery {
	case "acknowledged", "no_op", "failed":
		snapshot := timeline["snapshots"].([]any)[0].(map[string]any)
		input := snapshot["input"].(map[string]any)
		if runInspectionDelivery == "acknowledged" {
			snapshot["output"] = map[string]any{"result_type": "SQL", "vendor_type": "postgres", "duration_ms": 125, "details": map[string]any{"kind": "rows", "row_count": 3}}
		} else {
			input["sink_delivery_outcome"] = runInspectionDelivery
			if failure, ok := timeline["failure"].(map[string]any); ok && runInspectionDelivery == "failed" {
				metadata["error_summary"] = "Sink configuration was rejected before dispatch"
				failure["error"] = metadata["error_summary"]
				delete(failure, "reason_code")
			}
		}
	}
	if runInspectionContractFailure {
		metadata["source"] = map[string]any{"type": "manual", "summary": "Manual run", "attempt_number": 1}
		timeline["structural_checks"].([]any)[0].(map[string]any)["status"] = "failed"
		timeline["structural_checks"].([]any)[0].(map[string]any)["reason_code"] = "contract_violation"
		timeline["structural_checks"].([]any)[0].(map[string]any)["contract_failure"] = map[string]any{
			"contract_name": "orders", "field_name": "product_code", "rule": "required", "violation_count": 1, "reason_code": "contract_missing_required_field",
		}
		timeline["failure"] = nil
		timeline["snapshots"] = []any{}
		metadata["error_summary"] = ""
	}
	if runInspectionDelivery == "unavailable" {
		timeline["snapshots"], timeline["events"], timeline["structural_checks"] = []any{}, []any{}, []any{}
	}
	if runInspectionFailure != "" {
		step, side, field, rule, reason, phase := "fetch", "source", "customer_id", "required", "source_result_shape_mismatch", "producer_output"
		events := []any{map[string]any{"at": "2026-10-25T00:59:59.125Z", "type": "step_started", "step": "fetch", "status": "running"}}
		if runInspectionFailure == "transform" {
			step, side, field, rule, reason, phase = "normalize", "transform", "amount_cents", "type", "transform_contract_mismatch", "normalized_input"
			events = append(events, map[string]any{"at": "2026-10-25T00:59:59.200Z", "type": "step_succeeded", "step": "fetch", "status": "succeeded"}, map[string]any{"at": "2026-10-25T00:59:59.210Z", "type": "step_succeeded", "step": "customers", "status": "succeeded"}, map[string]any{"at": "2026-10-25T00:59:59.250Z", "type": "step_started", "step": "normalize", "status": "running"})
		}
		events = append(events, map[string]any{"at": "2026-10-25T01:00:00.125Z", "type": "step_failed", "step": step, "status": "failed"})
		diagnostic := map[string]any{"contract_name": "", "field_name": field, "rule": rule, "violation_count": 1, "reason_code": reason}
		check := map[string]any{"id": "failed-boundary", "step": step, "phase": phase, "status": "failed", "contract_failure": diagnostic}
		if phase == "normalized_input" {
			check["input"], check["target"] = "orders", "fetch"
		}
		timeline["events"], timeline["snapshots"], timeline["structural_checks"] = events, []any{}, []any{check}
		timeline["failure"] = map[string]any{"step": step, "side": side, "kind": "platform", "reason_code": reason, "error": field + " violates " + rule + "; wrapped diagnostic: " + field + " violates " + rule, "at": "2026-10-25T01:00:00.125Z", "contract": diagnostic, "action_hint": "Review the recorded field and executed contract before changing the current configuration."}
		metadata["started_at"], metadata["ended_at"], metadata["step_slug"] = "2026-10-25T00:59:59.125Z", "2026-10-25T01:00:00.125Z", step
		if runInspectionFailure == "source-type" {
			failure := timeline["failure"].(map[string]any)
			delete(failure, "contract")
			failure["error"] = "operation output obligation (post_query): query output contract does not match cached schema structure_type"
			failure["action_hint"] = "Review the reported contract field and validation rule, then fix the upstream data or contract declaration before retrying."
			delete(check, "contract_failure")
		}
		if runInspectionFailure == "s3" || runInspectionFailure == "s3-provider" {
			diagnostic := "s3 object sink write failed"
			if runInspectionFailure == "s3-provider" {
				diagnostic = "S3 AccessDenied: access to the requested operation was denied."
			}
			timeline["failure"] = map[string]any{"step": "store", "side": "sink", "kind": "data_source", "reason_code": "s3_object_write_failed", "error": diagnostic, "action_hint": "The data source returned an unexpected error. Check the data source status and credentials."}
			timeline["structural_checks"] = []any{}
			timeline["snapshots"] = []any{map[string]any{"step": "store", "kind": "sink", "input": map[string]any{"data_source_slug": "warehouse", "operation_type": "s3", "operation_ref": "write-object", "sink_delivery_outcome": "unknown", "sink_replay_strategy": "unsafe"}}}
			timeline["events"] = []any{map[string]any{"at": "2026-10-25T00:59:59.125Z", "type": "step_started", "step": "store", "status": "running"}, map[string]any{"at": "2026-10-25T01:00:00.125Z", "type": "step_failed", "step": "store", "status": "failed"}}
			metadata["step_slug"] = "store"
		}
	}

	if runInspectionPhase != "" {
		metadata["source"] = map[string]any{"type": "manual", "summary": "Manual run", "attempt_number": 1, "root_run_slug": "run-inspection"}
		metadata["step_count"], metadata["started_at"] = 3, runInspectionStartedAt
		timeline["snapshots"], timeline["structural_checks"], timeline["failure"] = []any{}, []any{}, nil
		events := []any{map[string]any{"at": runInspectionStartedAt, "type": "run_started", "status": "running"}}
		for i, step := range []string{"fetch", "normalize", "store"} {
			events = append(events, map[string]any{"at": runInspectionStartedAt.Add(time.Duration(2*i+1) * time.Second), "type": "step_started", "step": step, "status": "running"})
			if runInspectionStatus == "running" && step == runInspectionPhase {
				break
			}
			status, typ, at := "succeeded", "step_succeeded", runInspectionStartedAt.Add(time.Duration(2*i+2)*time.Second)
			if runInspectionStatus == "failed" && step == runInspectionPhase {
				status, typ, at = "failed", "step_failed", runInspectionEndedAt
			}
			events = append(events, map[string]any{"at": at, "type": typ, "step": step, "status": status})
			if status == "failed" {
				break
			}
		}
		if runInspectionStatus != "running" {
			events = append(events, map[string]any{"at": runInspectionEndedAt, "type": "run_completed", "status": runInspectionStatus})
			metadata["ended_at"], metadata["duration_ms"] = runInspectionEndedAt, runInspectionEndedAt.Sub(runInspectionStartedAt).Milliseconds()
		}
		if runInspectionStatus == "failed" {
			side := map[string]string{"fetch": "source", "normalize": "transform", "store": "sink"}[runInspectionPhase]
			metadata["error_summary"], metadata["step_slug"] = "Synthetic step failed", runInspectionPhase
			timeline["failure"] = map[string]any{"step": runInspectionPhase, "side": side, "kind": "platform", "error": "Synthetic step failed", "at": runInspectionEndedAt}
		}
		timeline["events"] = events
	}

	if runInspectionStatus == "queued" {
		metadata["pipeline_version_spec_hash"] = ""
		metadata["step_count"], metadata["duration_ms"] = 0, 0
		delete(metadata, "ended_at")
		timeline["events"] = []any{map[string]any{"at": runInspectionStartedAt, "type": "run_queued", "status": "queued"}}
		timeline["snapshots"], timeline["structural_checks"] = []any{}, []any{}
		delete(timeline, "compiled_plan_digest")
		delete(timeline, "deployment_resolution_digest")
	}
	timeline["revision"] = runInspectionRevision
	if r.URL.Path == runs+"/run-inspection/progress" {
		if runInspectionWorker == "disconnected" {
			w.WriteHeader(http.StatusServiceUnavailable)
			encode(map[string]any{"error": map[string]any{"code": "UNAVAILABLE", "message": "progress unavailable"}})
			return true
		}
		events := timeline["events"].([]any)
		after, _ := strconv.Atoi(r.URL.Query().Get("after_event"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > 100 {
			limit = 100
		}
		if after < 0 || after > len(events) {
			w.WriteHeader(http.StatusBadRequest)
			encode(map[string]any{"error": map[string]any{"code": "INVALID_CURSOR", "message": "resynchronize"}})
			return true
		}
		next := min(after+limit, len(events))
		liveness := "terminal"
		observed := time.Now().UTC()
		completed := 0
		failed := 0
		states := map[string]string{}
		for _, raw := range events {
			event := raw.(map[string]any)
			step, _ := event["step"].(string)
			if step != "" {
				states[step], _ = event["status"].(string)
			}
		}
		for _, state := range states {
			if state == "succeeded" {
				completed++
			}
			if state == "failed" {
				failed++
			}
		}
		var current *string
		if runInspectionStatus == "running" {
			liveness = "recent"
			if runInspectionPhase != "" {
				current = &runInspectionPhase
			}
		}
		if runInspectionWorker == "stale" {
			liveness = "stale"
			observed = observed.Add(-30 * time.Second)
		}
		progress := map[string]any{"compiled_plan_digest": "sha256:executed-plan", "deployment_resolution_digest": "sha256:recorded-resolution", "metadata": metadata, "revision": runInspectionRevision, "events": events[after:next], "after_event": after, "next_after_event": next, "event_count": len(events), "has_more": next < len(events), "worker_observed_at": observed, "worker_liveness": liveness, "observed_at": observed, "server_time": time.Now().UTC(), "current_step": current, "completed_steps": completed, "failed_steps": failed, "pending_steps": max(0, 3-len(states)), "interrupted_steps": 0}
		if runInspectionStatus == "queued" {
			progress["compiled_plan_digest"], progress["deployment_resolution_digest"], progress["worker_observed_at"] = nil, nil, nil
			progress["worker_liveness"], progress["pending_steps"] = "unavailable", 0
			progress["observed_at"] = runInspectionStartedAt
		}
		encode(progress)
		return true
	}
	switch r.URL.Path {
	case prefix + "/pipelines/daily-sync":
		if runInspectionFailure == "" {
			return false
		}
		p := runInspectionPipeline()
		p["spec"].(map[string]any)["steps"].([]any)[2].(map[string]any)["transform"].(map[string]any)["sql"] = "SELECT amount_cents FROM orders /* current authoring */"
		encode(p)
	case "/api/users/profile":
		encode(map[string]any{"slug": "test-user", "timezone": runInspectionTimezone})
	case prefix + "/environments/dev/pipeline-deployments":
		if runInspectionDeploymentChanged {
			encode(map[string]any{"deployments": []any{map[string]any{"pipeline_slug": "daily-sync", "environment_slug": "dev", "current_version_spec_hash": strings.Repeat("b", 64), "current_version_short_spec_hash": strings.Repeat("b", 12)}}})
		} else {
			encode(map[string]any{"deployments": []any{}})
		}
	case prefix + "/contracts/orders/revisions/v1":
		encode(map[string]any{"slug": "v1", "digest": "sha256:original", "schema_objects": []any{}, "document": map[string]any{"id": "orders"}})
	case prefix + "/contracts/orders/impact":
		var request api.ODCSRevisionImpactRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.FromRevisionSlug != "v1" || request.ToRevisionSlug != "v2" || request.FromPlan == nil || request.ToPlan == nil || request.FromPlan.Digest != "sha256:"+strings.Repeat("a", 64) || request.ToPlan.Digest != "sha256:"+strings.Repeat("b", 64) {
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		encode(api.ODCSRevisionImpact{FromRevisionSlug: "v1", ToRevisionSlug: "v2", FromDigest: "sha256:original", ToDigest: "sha256:updated", FromPlan: request.FromPlan, ToPlan: request.ToPlan, ExternalConsumers: "unknown", Objects: []api.ODCSObjectImpact{{SchemaObjectId: "row", Comparison: "supported", ComparisonScope: new(api.ODCSObjectImpactComparisonScope("selected_projection")), Changes: []api.ODCSImpactChange{{Kind: "data_path_changed", Path: `$["renamed"]`, Before: `$["id"]`, After: `$["renamed"]`, Severity: "breaking", ReasonCode: "structural_data_path_changed"}}}}, Consumers: []api.ODCSConsumerImpact{{PipelineSlug: "daily-sync", StepSlug: "normalize", Port: "input:orders", Comparison: "supported", Origin: "deployed"}}})
	case runs:
		if runInspectionHistory || r.URL.Query().Get("cursor") == "page-2" {
			old := map[string]any{}
			for key, value := range metadata {
				old[key] = value
			}
			old["run_slug"] = "run-older"
			old["status"] = "failed"
			old["error_summary"] = "Historical failure"
			if runInspectionHistory {
				encode(map[string]any{"runs": []any{metadata, old}, "has_more": false})
			} else {
				encode(map[string]any{"runs": []any{old}, "has_more": false, "prev_cursor": "page-1"})
			}
			return true
		}
		encode(map[string]any{"runs": []any{metadata}, "has_more": true, "next_cursor": "page-2"})
	case runs + "/run-older":
		metadata["run_slug"] = "run-older"
		metadata["status"] = "failed"
		metadata["error_summary"] = "Historical failure"
		metadata["source"] = map[string]any{"type": "manual", "summary": "Manual retry", "attempt_number": 2, "root_run_slug": "run-root", "retry_of_run_slug": "run-root"}
		encode(map[string]any{"metadata": metadata, "timeline": timeline})
	case runs + "/run-inspection":
		record := map[string]any{"metadata": metadata, "timeline": timeline, "composition": map[string]any{
			"status": "available", "template_digest": "sha256:portable-template", "compiled_plan_digest": "sha256:executed-plan", "deployment_resolution_digest": "sha256:recorded-resolution", "source_commit": version,
			"pins":       []any{map[string]any{"step": "store", "port": "input:orders", "origin": "operation", "contract_slug": "orders", "revision_slug": "historical-v1", "schema_object_id": "row", "digest": "sha256:exact-pin", "compiler_id": "compiler-v1", "representation": "rows", "root_shape": ""}},
			"operations": []any{map[string]any{"step": "store", "role": "sink", "operation_slug": "upsert-customers", "revision": "old-operation", "digest": "sha256:operation", "resource_scope": map[string]any{"policy_id": "s3/relative-key/v1", "data_source": "sink", "operation": "upsert-customers", "contract": "customers", "revision": "v1", "object_id": "row", "representation": "rows"}}},
			"targets":    []any{map[string]any{"step": "store", "role": "sink", "reference": "warehouse", "data_source_slug": "original-warehouse", "binding_revision": "old-binding"}},
		}}
		if runInspectionCheckpoint == "recorded" {
			record["incremental_diagnostics"] = api.PipelineRunIncrementalDiagnostics{
				Sources: []api.PipelineRunIncrementalSourceDiagnostic{
					{
						SourceStepSlug:     "fetch-partitioned-customer-orders-from-primary-warehouse",
						CheckpointKind:     "watermark",
						PreviousCheckpoint: api.PipelineRunCheckpointSummary{Summary: "2026-10-01"},
						Window:             api.PipelineRunSourceWindowSummary{Summary: "2026-10-01 to 2026-10-02"},
						ObservedCheckpoint: api.PipelineRunCheckpointSummary{Summary: strings.Repeat("synthetic", 8)},
						CommitResult:       api.PipelineRunCheckpointCommitResult{Status: api.PipelineRunCheckpointStatusBlocked, BlockedByStep: "store", BlockedReason: "Sink acknowledgement is unavailable."},
					},
					{
						SourceStepSlug:     "fetch-updates",
						CheckpointKind:     "cursor",
						PreviousCheckpoint: api.PipelineRunCheckpointSummary{Summary: "redacted cursor"},
						Window:             api.PipelineRunSourceWindowSummary{Summary: "bounded source"},
						ObservedCheckpoint: api.PipelineRunCheckpointSummary{Summary: "not observed"},
						CommitResult:       api.PipelineRunCheckpointCommitResult{Status: api.PipelineRunCheckpointStatusSkipped},
					},
				},
			}
		}
		encode(record)
	case runs + "/run-inspection/trigger-retry-attempts":
		encode(map[string]any{"retry_attempts": []any{}})
	case runs + "/run-inspection:recover":
		if runInspectionProtectedRecovery {
			var request api.RecoverPipelineRunRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return true
			}
			if request.ProtectedEnvironmentConfirmation == nil || *request.ProtectedEnvironmentConfirmation != "dev" {
				w.WriteHeader(http.StatusForbidden)
				encode(map[string]any{"error": map[string]any{"code": "protected_environment_confirmation_required", "message": "Protected environment confirmation required"}})
				return true
			}
			encode(recoveredRunInspectionRecord(metadata))
			return true
		}
		w.WriteHeader(http.StatusConflict)
		encode(map[string]any{"error": map[string]any{"code": "replay_unavailable", "message": "Retained inputs were pruned; recovery refused."}})
	case runs + "/run-recovered":
		if !runInspectionProtectedRecovery {
			return false
		}
		encode(recoveredRunInspectionRecord(metadata))
	case prefix + "/pipelines/daily-sync/versions/" + version:
		if runInspectionDelivery == "unavailable" || runInspectionDefinitionPruned {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		p := runInspectionPipeline()
		encode(map[string]any{"pipeline_slug": "daily-sync", "spec_hash": version, "spec": p["spec"]})
	default:
		return false
	}
	return true
}

func recoveredRunInspectionRecord(metadata map[string]any) map[string]any {
	return map[string]any{
		"metadata": map[string]any{
			"pipeline_slug": metadata["pipeline_slug"], "environment_slug": metadata["environment_slug"],
			"pipeline_version_spec_hash": metadata["pipeline_version_spec_hash"],
			"run_slug":                   "run-recovered", "status": "queued", "replay_of_run_slug": "run-inspection", "replay_from_step": "store",
		},
		"timeline": map[string]any{"events": []any{}, "snapshots": []any{}},
	}
}

func runInspectionPipeline() map[string]any {
	var p map[string]any
	if err := json.Unmarshal([]byte(workspacePipeline), &p); err != nil {
		panic(err)
	}
	historical := p["spec"].(map[string]any)
	historical["steps"].([]any)[1].(map[string]any)["transform"].(map[string]any)["sql"] = "SELECT email FROM input /* executed V1 */"
	if runInspectionFailure != "" {
		steps := historical["steps"].([]any)
		fields := []any{map[string]any{"name": "customer_id", "type": "string", "required": true}, map[string]any{"name": "amount_cents", "type": "integer", "required": true}}
		steps[0].(map[string]any)["query"].(map[string]any)["output_contract"].(map[string]any)["fields"] = fields
		steps[1].(map[string]any)["transform"].(map[string]any)["input_contract"].(map[string]any)["fields"] = fields
		steps[1].(map[string]any)["transform"].(map[string]any)["inputs"] = map[string]string{"orders": "fetch", "customers": "customers"}
		steps[1].(map[string]any)["transform"].(map[string]any)["sql"] = "SELECT amount_cents FROM orders /* executed V1 */"
		customers := map[string]any{"kind": "query", "slug": "customers", "name": "Read customers", "query": map[string]any{"data_source_ref": "source", "operation_type": "sql", "operation_ref": "customers"}}
		historical["steps"] = append([]any{steps[0], customers}, steps[1:]...)
	}
	branchCount := 0
	switch runInspectionTopology {
	case "branched":
		branchCount = 2
	case "large":
		branchCount = 10
	}
	if branchCount > 0 {
		steps := make([]any, 0, 2*branchCount+2)
		inputs := make(map[string]string, branchCount)
		branches := make([]string, branchCount)
		for i := range branches {
			read, normalize, input := fmt.Sprintf("read-%d", i), fmt.Sprintf("normalize-%d", i), fmt.Sprintf("supplier_%d", i)
			steps = append(steps,
				map[string]any{"kind": "query", "slug": read, "name": fmt.Sprintf("Read supplier %d", i+1), "query": map[string]any{"data_source_ref": "supplier-files", "operation_type": "http", "operation_ref": read}},
				map[string]any{"kind": "transform", "slug": normalize, "name": fmt.Sprintf("Normalize supplier %d", i+1), "transform": map[string]any{"inputs": map[string]string{"input": read}, "sql": "SELECT id FROM input", "output_kind": "rows"}},
			)
			inputs[input] = normalize
			branches[i] = "SELECT id FROM " + input
		}
		steps = append(steps,
			map[string]any{"kind": "transform", "slug": "combine", "name": "Combine supplier offers", "transform": map[string]any{"inputs": inputs, "sql": strings.Join(branches, " UNION ALL "), "output_kind": "rows"}},
			map[string]any{"kind": "sink", "slug": "store", "name": "Store supplier offers", "sink": map[string]any{"input": "combine", "data_source_ref": "warehouse", "operation_type": "sql", "operation_ref": "upsert-offers", "idempotency_class": "naturally_idempotent"}},
		)
		historical["steps"] = steps
	}
	return p
}
