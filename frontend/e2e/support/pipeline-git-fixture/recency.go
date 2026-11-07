package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/gofiber/fiber/v3"
)

var (
	recencyFixture    bool
	recencyCapability = true
	recencyRestricted bool
	recencyState      api.SuccessfulRunRecency
)

func registerRecencyFixture(app *fiber.App) {
	app.Post("/_test/recency/:state", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		recencyFixture, notificationFixtureEnabled = true, true
		imported, bound, deployed = true, true, true
		state := c.Params("state")
		recencyCapability = state != "unavailable"
		recencyRestricted = state == "restricted"
		if state == "new" {
			recencyState = api.SuccessfulRunRecency{CapabilityEnabled: true, Outcome: api.SuccessfulRunRecencyOutcomeUnavailable, Episodes: []api.SuccessfulRunRecencyEpisode{}, EvaluatedAt: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
			revision := int64(7)
			recencyState.DeploymentRevision = &revision
			identity := strings.Repeat("a", 64)
			recencyState.VersionSpecHash = &identity
		}
		return c.SendStatus(http.StatusOK)
	})
	app.Post("/_test/recency-evidence", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
		ended := at.Add(time.Hour)
		run, reason := "recency-recovery", "qualifying_success"
		currentRun := "current-success"
		age := int64(3600)
		recencyState.MaximumAgeSeconds = &age
		recencyState.Configured, recencyState.Enabled = true, true
		recencyState.Revision = 3
		recencyState.EnabledAt = &at
		recencyState.DeployedAt = &at
		recencyState.Outcome = api.SuccessfulRunRecencyOutcomeHealthy
		recencyState.UnavailableReason = nil
		recencyState.EvaluatedAt = ended
		recencyState.SucceededAt = &ended
		recencyState.SuccessRunSlug = &currentRun
		deadline := ended.Add(time.Hour)
		recencyState.DeadlineAt = &deadline
		recencyState.Episodes = []api.SuccessfulRunRecencyEpisode{{NotificationSlug: "recorded-recency-episode", ExpectationRevision: 1, MaximumAgeSeconds: 3600, EnabledAt: at, DeploymentRevision: 6, VersionSpecHash: strings.Repeat("b", 64), DeployedAt: at, DeadlineAt: ended, BreachedAt: ended, ResolvedAt: &ended, ResolutionReason: &reason, RecoveryRunSlug: &run, RecoverySucceededAt: &ended}}
		return c.SendStatus(http.StatusOK)
	})
}

// Synthetic API state drives the real SSR forms; no observer/provider acceptance.
func handleRecencyAPI(w http.ResponseWriter, r *http.Request) bool {
	encode := func(v any) {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			panic(err)
		}
	}
	const org = "/api/organizations/acme"
	switch r.URL.Path {
	case org + "/pipeline-operational-capabilities":
		types := []api.NotificationType{api.NotificationTypePipelineRunFailed}
		if recencyCapability {
			types = append(types, api.NotificationTypePipelineFreshnessSLABreached)
		}
		encode(api.PipelineOperationalCapabilities{SuccessfulRunRecency: recencyCapability, NotificationTypes: types})
	case org + "/roles/admin":
		permissions := []map[string]string{{"resource": "*", "action": "*"}}
		if recencyRestricted {
			permissions = []map[string]string{{"resource": "pipelines", "action": "read"}}
		}
		encode(map[string]any{"slug": "admin", "permissions": permissions})
	case org + "/notifications":
		encode(map[string]any{"notifications": []map[string]any{{"slug": "recorded-recency-episode", "notification_type": "pipeline.freshness_sla_breached", "severity": "warning", "lifecycle_state": "resolved", "summary": "Deployed pipeline exceeded its successful-run recency window.", "source_entity_type": "pipeline", "source_action": "execute", "source_refs": map[string]string{"project_slug": "ops", "environment_slug": "dev", "pipeline_slug": "daily-sync"}, "occurrence_count": 1}}, "has_more": false})
	case org + "/alert-delivery-attempts":
		encode(map[string]any{"alert_delivery_attempts": []any{}, "has_more": false})
	case prefix + "/environments/dev/pipelines/daily-sync/successful-run-recency":
		if r.Method == http.MethodPut {
			if !recencyCapability {
				w.WriteHeader(404)
				return true
			}
			if recencyRestricted {
				w.WriteHeader(403)
				return true
			}
			var input api.SuccessfulRunRecencyUpdate
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				w.WriteHeader(400)
				return true
			}
			if input.ExpectedRevision != recencyState.Revision {
				w.WriteHeader(409)
				return true
			}
			if input.MaximumAgeSeconds < 1 {
				w.WriteHeader(400)
				return true
			}
			recencyState.Revision++
			recencyState.Configured = true
			recencyState.Enabled = input.Enabled
			recencyState.MaximumAgeSeconds = &input.MaximumAgeSeconds
			at := recencyState.EvaluatedAt
			recencyState.EnabledAt = &at
			deadline := at.Add(time.Duration(input.MaximumAgeSeconds) * time.Second)
			recencyState.DeadlineAt = &deadline
			recencyState.Outcome = api.SuccessfulRunRecencyOutcomeGrace
			recencyState.UnavailableReason = nil
			if !input.Enabled {
				reason := "expectation_disabled"
				recencyState.Outcome = api.SuccessfulRunRecencyOutcomeUnavailable
				recencyState.UnavailableReason = &reason
			}
		}
		result := recencyState
		result.CapabilityEnabled = recencyCapability
		if !recencyCapability {
			reason := "capability_disabled"
			result.Outcome = api.SuccessfulRunRecencyOutcomeUnavailable
			result.UnavailableReason = &reason
		}
		encode(result)
	default:
		if strings.Contains(r.URL.Path, "/successful-run-recency") {
			w.WriteHeader(404)
			return true
		}
		return false
	}
	return true
}
