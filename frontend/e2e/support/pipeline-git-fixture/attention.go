package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"
)

var (
	attentionEnabled, attentionRestricted bool
	attentionWrites                       int
)

func registerAttentionFixture(app *fiber.App) {
	app.Post("/_test/attention/:role", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		attentionEnabled, notificationFixtureEnabled = true, true
		attentionRestricted = c.Params("role") == "restricted"
		notificationDestinations = []map[string]any{{"slug": "ops-email", "name": "Operations email", "type": "email", "non_secret_config": map[string]any{"recipients": []string{"synthetic@example.com"}}, "enabled": true}}
		notificationPolicies = []map[string]any{{"slug": "retry-policy", "name": "Recorded retry policy", "scope": "project_environment", "project_slug": "ops", "environment_slug": "dev", "notification_type": "pipeline.retry_exhausted", "severity": "warning", "lifecycle_state": "", "destination_slugs": []string{"ops-email"}, "enabled": true}}
		return c.SendStatus(http.StatusOK)
	})
	app.Get("/_test/attention-stats", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		return c.JSON(fiber.Map{"writes": attentionWrites})
	})
}

// Synthetic persisted evidence only; navigating this fixture can never contact a provider.
func handleAttentionAPI(w http.ResponseWriter, r *http.Request) bool {
	const base = "/api/organizations/acme"
	encode := func(value any) {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			panic(err)
		}
	}
	if r.Method != http.MethodGet {
		attentionWrites++
		w.WriteHeader(http.StatusForbidden)
		return true
	}
	if r.URL.Path == base+"/roles/admin" {
		permissions := []any{map[string]any{"resource": "*", "action": "*"}}
		if attentionRestricted {
			permissions = []any{map[string]any{"resource": "notifications", "action": "read"}}
		}
		encode(map[string]any{"slug": "admin", "permissions": permissions})
		return true
	}
	switch r.URL.Path {
	case base + "/notifications":
		records := []any{}
		if r.URL.Query().Get("lifecycle_state") != "resolved" && r.URL.Query().Get("severity") != "info" {
			records = append(records, map[string]any{"slug": "retry-notice", "summary": "Retries exhausted for daily-sync", "notification_type": "pipeline.retry_exhausted", "severity": "warning", "lifecycle_state": "open", "occurrence_count": 1, "first_seen_at": "2026-10-19T00:00:00Z", "last_seen_at": "2026-10-19T00:00:00Z", "last_occurrence_at": "2026-10-19T00:00:00Z", "source_entity_type": "pipeline_run", "source_action": "execute", "source_refs": map[string]string{"project_slug": "ops", "environment_slug": "dev", "run_slug": "run-inspection", "pipeline_slug": "daily-sync"}, "dispatch_statuses": []any{map[string]any{"destination_slug": "ops-email", "status": "queued", "queued_at": "2026-10-19T00:00:00Z"}}})
		}
		encode(map[string]any{"notifications": records, "has_more": false})
	case base + "/alert-delivery-attempts":
		encode(map[string]any{"alert_delivery_attempts": []any{map[string]any{"slug": "recorded-attempt", "notification_record_slug": "retry-notice", "alert_policy_slug": "retry-policy", "destination_slug": "ops-email", "destination_name": "Operations email", "destination_type": "email", "status": "failed", "provider_attempt": 2, "error_message": "Synthetic provider failure", "queued_at": "2026-10-19T00:00:00Z", "failed_at": "2026-10-19T00:00:01Z"}}})
	case base + "/alert-policies/retry-policy":
		encode(notificationPolicies[0])
	case base + "/alert-destinations/ops-email":
		if attentionRestricted {
			w.WriteHeader(http.StatusForbidden)
		} else {
			encode(notificationDestinations[0])
		}
	case base + "/alert-policy-source-metadata":
		encode(map[string]any{"sources": []any{map[string]any{"source_kind": "pipeline_run", "source_actions": []string{"execute"}}}, "source_refs": []any{}})
	default:
		if attentionRestricted && (strings.Contains(r.URL.Path, "/alert-policies") || strings.Contains(r.URL.Path, "/alert-destinations")) {
			w.WriteHeader(http.StatusForbidden)
			return true
		}
		return false
	}
	return true
}
