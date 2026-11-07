package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

var (
	notificationConsentOrigin  string
	notificationAppOrigin      string
	notificationFixtureEnabled bool
	notificationConnection     map[string]any
	notificationDestinations   = []map[string]any{}
	notificationPolicies       = []map[string]any{}
)

func registerNotificationFixture(app *fiber.App) {
	app.Post("/_test/notifications", func(c fiber.Ctx) error {
		mu.Lock()
		defer mu.Unlock()
		notificationFixtureEnabled = true
		return c.SendStatus(http.StatusOK)
	})
}

// This fixture models safe API replies, never provider OAuth or delivery acceptance.
func handleNotificationAPI(w http.ResponseWriter, r *http.Request) bool {
	encode := func(v any) {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			panic(err)
		}
	}
	read := func() map[string]any {
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			panic(err)
		}
		if _, supplied := input["slug"]; !supplied {
			name, _ := input["name"].(string)
			generated, err := slug.Slug("").ValidateOrGenerate(name)
			if err != nil {
				panic(err)
			}
			input["slug"] = generated.String()
		}
		return input
	}
	const base = "/api/organizations/acme"
	switch r.URL.Path {
	case "/_test/slack-consent":
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<h1>Synthetic Slack channel picker</h1><p>UI fixture only; no Slack account or credentials.</p><form method="post" action="/_test/slack-authorize"><label>Channel<select name="channel"><option>#operations</option></select></label><button>Allow access</button></form>`))
	case "/_test/slack-authorize":
		notificationConnection["status"] = "active"
		notificationConnection["metadata"] = map[string]any{"workspace_id": "TTEST", "workspace_name": "Synthetic workspace", "channel_id": "CTEST", "channel_name": "#operations"}
		http.Redirect(w, r, notificationAppOrigin+"/orgs/acme/alerts/connections?provider_oauth=success", http.StatusSeeOther)
	case base + "/roles/admin":
		encode(map[string]any{"slug": "admin", "permissions": []any{map[string]any{"resource": "*", "action": "*"}}})
	case base + "/notification-connections":
		switch {
		case r.Method == http.MethodPost:
			notificationConnection = read()
			if notificationConnection["provider"] != "slack" {
				w.WriteHeader(http.StatusBadRequest)
				return true
			}
			notificationConnection["status"] = "pending"
			notificationConnection["metadata"] = map[string]any{"workspace_id": "", "workspace_name": "", "channel_id": "", "channel_name": ""}
			notificationConnection["updated_at"] = "2026-10-19T00:00:00Z"
			w.WriteHeader(http.StatusCreated)
			encode(notificationConnection)
		case notificationConnection == nil:
			encode([]any{})
		default:
			encode([]any{notificationConnection})
		}
	case base + "/notification-connections/ops-slack/connect":
		w.WriteHeader(http.StatusCreated)
		encode(map[string]any{"authorization_url": notificationConsentOrigin + "/_test/slack-consent"})
	case base + "/notification-connections/ops-slack/disconnect":
		notificationConnection["status"] = "revoked"
		encode(map[string]any{"kind": "oauth2_grant", "status": "revoked"})
	case base + "/alert-destinations":
		if r.Method == http.MethodPost {
			destination := read()
			destination["enabled"] = true
			destination["created_at"] = "2026-10-19T00:00:00Z"
			destination["updated_at"] = "2026-10-19T00:00:00Z"
			notificationDestinations = append(notificationDestinations, destination)
			w.WriteHeader(http.StatusCreated)
			encode(destination)
		} else {
			encode(map[string]any{"alert_destinations": notificationDestinations, "has_more": false})
		}
	case base + "/alert-destinations/ops-alerts/test":
		w.WriteHeader(http.StatusAccepted)
		encode(map[string]any{"destination_slug": "ops-alerts", "destination_type": "slack", "recipient_count": 1, "diagnostics_persisted": false, "enqueued_jobs": []any{map[string]any{"kind": "notification_delivery", "queue": "notification_delivery", "state": "queued"}}})
	case base + "/alert-policies":
		if r.Method == http.MethodPost {
			policy := read()
			policy["enabled"] = true
			policy["created_at"] = "2026-10-19T00:00:00Z"
			policy["updated_at"] = "2026-10-19T00:00:00Z"
			notificationPolicies = append(notificationPolicies, policy)
			w.WriteHeader(http.StatusCreated)
			encode(policy)
		} else {
			encode(map[string]any{"alert_policies": notificationPolicies, "has_more": false})
		}
	default:
		if strings.HasSuffix(r.URL.Path, "/alert-policy-source-metadata") {
			encode(map[string]any{"notification_types": []string{"pipeline.retry_exhausted", "pipeline.run_succeeded", "pipeline.contract_failure", "pipeline.run_failed", "pipeline.scheduled_run_delayed", "pipeline.scheduled_run_missing"}, "sources": []any{map[string]any{"source_kind": "pipeline", "source_actions": []string{"validate"}}, map[string]any{"source_kind": "pipeline_run", "source_actions": []string{"execute"}}}, "source_refs": []any{}})
			return true
		}
		return false
	}
	return true
}
