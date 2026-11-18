package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/gofiber/fiber/v3/middleware/static"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

var (
	proofHandoffMissingDeployment, proofHandoffMissingRun bool
	proofHistoryOlderEmpty                                bool
)

const receiptPrefix = "/api/organizations/acme/projects/ops/environments/prod/proof-receipts/"

type gitLabConnectionFixture struct {
	mu               sync.Mutex
	refuseConnect    bool
	refuseDisconnect bool
	disconnected     bool
}

func main() {
	gitLab := &gitLabConnectionFixture{}
	apiServer := httptest.NewServer(http.HandlerFunc(gitLab.handleAPI))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := frontend.NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	if err != nil {
		panic(err)
	}

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Set("Content-Security-Policy", "form-action 'self'")
		return c.Next()
	})
	app.Post("/_test/gitlab/refuse-actions", func(c fiber.Ctx) error {
		gitLab.mu.Lock()
		defer gitLab.mu.Unlock()
		gitLab.refuseConnect = true
		gitLab.refuseDisconnect = true
		return c.SendStatus(http.StatusNoContent)
	})
	app.Post("/_test/proof/handoff", func(c fiber.Ctx) error {
		proofMu.Lock()
		defer proofMu.Unlock()
		proofHandoffMissingDeployment = fiber.Query[bool](c, "missing_deployment")
		proofHandoffMissingRun = fiber.Query[bool](c, "missing_run")
		return c.SendStatus(http.StatusNoContent)
	})
	app.Post("/_test/proof/history/prune-older", func(c fiber.Ctx) error {
		proofMu.Lock()
		defer proofMu.Unlock()
		proofHistoryOlderEmpty = true
		return c.SendStatus(http.StatusNoContent)
	})
	app.Post("/_test/proof/reset", func(c fiber.Ctx) error {
		proofMu.Lock()
		proofSettings = initialProofSettings()
		proofSetupFails = fiber.Query[bool](c, "setup_failure")
		if c.Query("provider") == "gitlab" {
			proofSettings.Provider = "gitlab"
			proofSettings.DataSourceSlug = "gitlab"
		}
		if proofSetupFails {
			proofSettings.Enabled = true
			proofSettings.WebhookSetupRequired = true
			proofSettings.Status = "GitLab webhook setup failed."
			proofSettings.Targets = []api.ProofAssessmentTarget{{ProjectSlug: "ops", EnvironmentSlug: "prod", TargetRef: "refs/heads/main", PipelinePath: "pipelines/orders.yaml"}}
		}
		proofMu.Unlock()
		return c.SendStatus(204)
	})
	app.Post("/_test/proof/replace", func(c fiber.Ctx) error {
		proofMu.Lock()
		proofSettings.RepositoryId = "456"
		proofMu.Unlock()
		return c.SendStatus(204)
	})
	app.Get("/_test/session", func(c fiber.Ctx) error {
		return reqctx.SetUserSlug(c, store, slug.Slug("test-user"))
	})
	app.Get("/assets/icons/*", static.New("brand/icons"))
	app.Get("/assets/*", static.New("frontend/assets"))
	router.RegisterRoutes(app, frontend.CommunityPresentation{})

	listener, err := new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	fmt.Println("http://" + listener.Addr().String())
	if err := app.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
		panic(err)
	}
}

func (gitLab *gitLabConnectionFixture) handleAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/users/profile":
		write(w, `{"slug":"test-user"}`)
	case "/api/organizations/acme/gitlab-connection":
		gitLab.mu.Lock()
		defer gitLab.mu.Unlock()
		const revoked = `{"kind":"oauth2_grant","status":"revoked","updated_at":"2026-09-27T00:00:00Z","oauth":{"provider":"gitlab"}}`
		if r.Method == http.MethodDelete {
			if gitLab.refuseDisconnect {
				gitLab.refuseDisconnect = false
				w.WriteHeader(http.StatusConflict)
				return
			}
			gitLab.disconnected = true
		}
		if gitLab.disconnected {
			write(w, revoked)
			return
		}
		write(w, `{"kind":"oauth2_grant","status":"active","updated_at":"2026-09-27T00:00:00Z","oauth":{"provider":"gitlab","account":{"name":"Acme GitLab"},"scopes":["read_user","read_api","read_repository"]}}`)
	case "/api/organizations/acme/gitlab-connection/authorization":
		gitLab.mu.Lock()
		defer gitLab.mu.Unlock()
		if gitLab.refuseConnect {
			gitLab.refuseConnect = false
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
		write(w, `{"slug":"install","provider":"gitlab","authorization_url":"https://gitlab.com/oauth/authorize?state=fixture","expires_at":"2026-09-27T00:10:00Z","intent":"rotate_grant","requested_scopes":["read_user","read_api","read_repository"]}`)
	case "/api/organizations/acme/roles/admin":
		write(w, `{"slug":"admin","name":"Admin","permissions":[{"resource":"*","action":"*"}]}`)
	case "/api/organizations/acme/data-sources/missing/proof":
		w.WriteHeader(http.StatusNotFound)
		write(w, `{"error":{"code":"NOT_FOUND","message":"Data source not found."}}`)
	case "/api/organizations/acme/data-sources/restricted/proof":
		w.WriteHeader(http.StatusForbidden)
		write(w, `{"error":{"code":"FORBIDDEN","message":"Access denied."}}`)
	case "/api/organizations/acme/proof-configurations", "/api/organizations/acme/data-sources/github/proof", "/api/organizations/acme/data-sources/gitlab/proof":
		proofMu.Lock()
		defer proofMu.Unlock()
		if r.Method == http.MethodPut {
			var settings api.DataSourceProofSettings
			if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
				w.WriteHeader(400)
				return
			}
			if settings.Enabled && settings.ExpectedRepositoryId != proofSettings.RepositoryId {
				w.WriteHeader(400)
				write(w, `{"error":{"code":"PROOF_CONFIGURATION_INVALID","message":"Proof configuration is invalid","detailed_error":"Repository changed. Reload and confirm the new repository."}}`)
				return
			}
			if settings.Enabled && len(settings.Targets) == 0 {
				w.WriteHeader(400)
				write(w, `{"error":{"code":"PROOF_CONFIGURATION_INVALID","message":"Select an explicit assessment target and pipeline path."}}`)
				return
			}
			if proofSetupFails {
				proofSetupFails = false
				w.WriteHeader(http.StatusServiceUnavailable)
				write(w, `{"error":{"code":"PROVIDER_UNAVAILABLE","message":"GitLab webhook setup failed. Retry setup."}}`)
				return
			}
			proofSettings.WebhookSetupRequired = false
			proofSettings.Enabled = settings.Enabled
			proofSettings.PublishChecks = settings.Enabled && settings.PublishChecks
			proofSettings.Status = "Proof is disabled. Repository operations remain available."
			if settings.Enabled {
				proofSettings.Status = "Proof is enabled. Live connector evidence requires separate environment authorization."
			}
			if settings.Enabled {
				proofSettings.Targets = settings.Targets
			}
		}
		if strings.HasSuffix(r.URL.Path, "/proof-configurations") {
			if err := json.NewEncoder(w).Encode([]api.DataSourceProofConfiguration{proofSettings}); err != nil {
				panic(err)
			}
		} else {
			if err := json.NewEncoder(w).Encode(proofSettings); err != nil {
				panic(err)
			}
		}

	case "/api/organizations/disconnected":
		write(w, `{"name":"Disconnected","slug":"disconnected"}`)
	case "/api/organizations/acme":
		write(w, `{"name":"Acme","slug":"acme"}`)
	case "/api/organizations/connected":
		write(w, `{"name":"Acme","slug":"connected"}`)
	case "/api/organizations/disconnected/github-app-connection":
		w.WriteHeader(http.StatusNoContent)
	case "/api/organizations/disconnected/github-app-connection/authorize":
		var input api.AuthorizeOrganizationGitHubAppConnectionJSONRequestBody
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.AccountLogin != "synthetic-valid" {
			w.WriteHeader(http.StatusNotFound)
			write(w, `{"error":{"code":"NOT_FOUND","message":"App installation not found."}}`)
			return
		}
		write(w, `{"authorization_url":"https://github.com/login/oauth/authorize?state=fixture"}`)
	case "/api/organizations/acme/github-app-connection":
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusForbidden)
			write(w, `{"error":{"code":"FORBIDDEN","message":"Organization management permission is required."}}`)
			return
		}
		write(w, `{"account_login":"acme","repositories":[{"id":123,"full_name":"acme/pipelines"}]}`)
	case "/api/organizations/connected/github-app-connection":
		write(w, `{"account_login":"acme","repositories":[]}`)
	case "/api/organizations/disconnected/github-app-connection/install", "/api/organizations/acme/github-app-connection/install", "/api/organizations/connected/github-app-connection/install":
		write(w, `{"authorization_url":"https://github.com/apps/fixture/installations/new?state=fixture"}`)
	case "/api/organizations":
		write(w, `{"organizations":[{"name":"Acme","slug":"acme","member_role":"admin","created_at":"2026-09-02T00:00:00Z","updated_at":"2026-09-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	case "/api/organizations/acme/projects":
		write(w, `{"projects":[{"name":"Ops","slug":"ops","created_at":"2026-09-02T00:00:00Z","updated_at":"2026-09-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	case "/api/organizations/acme/projects/ops/environments":
		write(w, `{"project_environments":[{"environment_slug":"prod","environment_name":"Production","is_default":true},{"environment_slug":"empty","environment_name":"Empty","is_default":false},{"environment_slug":"denied","environment_name":"Denied","is_default":false}],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	case "/api/organizations/acme/projects/ops/environments/prod/runs/recorded-run":
		proofMu.Lock()
		defer proofMu.Unlock()
		if proofHandoffMissingRun {
			w.WriteHeader(http.StatusNotFound)
			write(w, `{"error":{"code":"RUN_NOT_FOUND","message":"The recorded run is no longer retained."}}`)
			return
		}
		write(w, `{"metadata":{"run_slug":"recorded-run","pipeline_slug":"orders","environment_slug":"prod","status":"succeeded","pipeline_version_spec_hash":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","source":{"type":"manual","attempt_number":2,"root_run_slug":"parent-run","retry_of_run_slug":"parent-run"}},"timeline":{"run_slug":"recorded-run","pipeline_slug":"orders","status":"succeeded","events":[],"snapshots":[]},"proof_link":{"kind":"run","receipt_id":"operational-available","identifier":"recorded-run","status":"matched"}}`)
	case "/api/organizations/acme/projects/ops/pipelines/orders":
		write(w, `{"slug":"orders","name":"Orders","current_version_spec_hash":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}`)
	case "/api/organizations/acme/projects/ops/environments/prod/pipelines/orders/deployment":
		proofMu.Lock()
		defer proofMu.Unlock()
		if proofHandoffMissingDeployment {
			w.WriteHeader(http.StatusNotFound)
			write(w, `{"error":{"code":"DEPLOYMENT_NOT_FOUND","message":"The current deployment is unavailable."}}`)
			return
		}
		write(w, `{"pipeline_slug":"orders","project_slug":"ops","environment_slug":"prod","current_version_spec_hash":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","current_version_short_spec_hash":"dddddddd","revision":9,"proof_link":{"kind":"deployment","receipt_id":"mismatch-partial","identifier":"orders:old","status":"version_mismatch"}}`)
	case "/api/organizations/acme/projects/ops/environments/prod/proof-receipts":
		proofMu.Lock()
		defer proofMu.Unlock()
		if r.URL.Query().Get("cursor") == "invalid" {
			w.WriteHeader(http.StatusBadRequest)
			write(w, `{"error":{"code":"VALIDATION_FAILED","message":"Invalid cursor."}}`)
			return
		}
		if r.URL.Query().Get("cursor") != "" && r.URL.Query().Get("direction") == "next" {
			if proofHistoryOlderEmpty {
				write(w, `{"receipts":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
				return
			}
			denied := strings.NewReplacer(`"no-evidence"`, `"denied"`, `"change_request_id":"42"`, `"change_request_id":"41"`).Replace(noEvidenceReceipt)
			write(w, `{"receipts":[`+staleReceipt+`,`+strings.ReplaceAll(noEvidenceReceipt, `"unverified"`, `"superseded"`)+`,`+denied+`],"has_more":false,"next_cursor":null,"prev_cursor":"newer"}`)
		} else {
			write(w, `{"receipts":[`+metadataReceipt+`],"has_more":true,"next_cursor":"older","prev_cursor":null}`)
		}
	case "/api/organizations/acme/projects/ops/environments/empty/proof-receipts":
		write(w, `{"receipts":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`)
	case "/api/organizations/acme/projects/ops/environments/denied/proof-receipts":
		w.WriteHeader(http.StatusForbidden)
		write(w, `{"error":{"code":"FORBIDDEN","message":"You do not have permission to view receipts."}}`)
	default:
		if !strings.HasPrefix(r.URL.Path, receiptPrefix) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		receiptSlug := strings.TrimPrefix(r.URL.Path, receiptPrefix)
		switch receiptSlug {
		case "mismatch-partial":
			write(w, mismatchPartialReceipt)
		case "stale":
			write(w, staleReceipt)
		case "no-evidence":
			write(w, noEvidenceReceipt)
		case "metadata":
			write(w, metadataReceipt)
		case "operational-pruned", "operational-expired", "operational-available", "operational-changed":
			var receipt api.ProofReceipt
			if err := json.Unmarshal([]byte(metadataReceipt), &receipt); err != nil {
				panic(err)
			}
			receipt.Id = receiptSlug
			receipt.Claims[0].Description = "Retained structural conformance"
			receipt.Claims[0].RequiredEvidenceLevel = api.ProofEvidenceLevelOperational
			receipt.Claims[0].ActualEvidenceLevel = new(api.ProofEvidenceLevelOperational)
			receipt.OperationalAdmissionEnabled = new(true)
			status := strings.TrimPrefix(receiptSlug, "operational-")
			verdict := api.ProofVerdictUnverified
			switch status {
			case "pruned":
				status = "missing"
			case "expired":
				verdict = api.ProofVerdictStale
			case "available":
				verdict = receipt.AggregateVerdict
			case "changed":
				verdict = api.ProofVerdictInconclusive
			}
			receipt.OperationalEvidenceStatus = new(api.ProofReceiptOperationalEvidenceStatus(status))
			receipt.OperationalCurrentVerdict = &verdict
			receipt.Operational = &api.ProofOperationalCapture{SourceReceiptId: "static-receipt", PolicyVersion: "operational-policy-v1", RecordDigest: "sha256:captured-record", ExpiresAt: receipt.CreatedAt, Selection: api.ProofOperationalSelection{Run: "recorded-run", Attempt: 2, Step: "store", Input: "orders", Origin: "operation", Phase: "pre_sink", Declaration: "operation/pre_sink", ProjectionDigest: "sha256:recorded-projection", CompiledPlanDigest: "sha256:recorded-plan", DeploymentResolutionDigest: "sha256:recorded-resolution"}}
			if status == "available" || status == "changed" {
				receipt.CreatedAt = proofFixtureAt
				receipt.RetainUntil = receipt.CreatedAt.Add(30 * 24 * time.Hour)
				receipt.Operational.ExpiresAt = receipt.CreatedAt.Add(7 * 24 * time.Hour)
			}
			if err := json.NewEncoder(w).Encode(receipt); err != nil {
				panic(err)
			}
		case "live":
			write(w, liveReceipt)
		case "mixed":
			var metadata, stale api.ProofReceipt
			if err := json.Unmarshal([]byte(metadataReceipt), &metadata); err != nil {
				panic(err)
			}
			if err := json.Unmarshal([]byte(staleReceipt), &stale); err != nil {
				panic(err)
			}
			metadata.Id = "mixed"
			metadata.AggregateVerdict = api.ProofVerdictStale
			metadata.Claims = append(metadata.Claims, stale.Claims...)
			if err := json.NewEncoder(w).Encode(metadata); err != nil {
				panic(err)
			}
		case "superseded":
			write(w, strings.NewReplacer(`"no-evidence"`, `"superseded"`, `"unverified"`, `"superseded"`).Replace(noEvidenceReceipt))
		case "denied":
			w.WriteHeader(http.StatusForbidden)
			write(w, `{"error":{"code":"FORBIDDEN","message":"You do not have permission to view this receipt."}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			write(w, `{"error":{"code":"NOT_FOUND","message":"Proof receipt not found."}}`)
		}
	}
}

func write(w http.ResponseWriter, body string) { _, _ = w.Write([]byte(body)) }

const candidate = `"candidate":{"organization_slug":"acme","project_slug":"ops","environment_slug":"prod","provider_connection_slug":"github-prod","repository_id":"123","change_request_id":"42","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","target_ref":"main","pipeline_path":"pipelines/operations/reporting/very-long-path-for-orders-and-inventory-with-detailed-business-identifiers/orders.yaml","spec_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","binding_fingerprint":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","capability_fingerprint":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","compiler_version":"v1","policy_version":"v1"}`

const mismatchPartialReceipt = `{"schema_version":1,"id":"mismatch-partial",` + candidate + `,"claims":[{"identity":{"family":"access_capability","pipeline_version_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","semantic_path":"/source"},"description":"provider access could not be verified","required_evidence_level":"live_read_only","actual_evidence_level":null,"observed_at":null,"expires_at":null,"verdict":"inconclusive","evidence":[],"diagnostics":[{"code":"PROVIDER_UNAVAILABLE","summary":"Provider connection failed","remediation":"Retry the provider connection test","retryable":true}]}],"aggregate_verdict":"inconclusive","supersedes_receipt_id":null,"created_at":"2026-09-02T00:00:00Z","retain_until":"2026-12-01T00:00:00Z","lifecycle_links":[{"kind":"deployment","receipt_id":"mismatch-partial","identifier":"orders:old","linked_at":"2026-09-02T00:00:00Z","retain_until":"2026-12-01T00:00:00Z","status":"version_mismatch","pipeline_slug":"orders","run_slug":null,"current_version_spec_hash":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}]}`

const staleReceipt = `{"schema_version":1,"id":"stale",` + candidate + `,"claims":[{"identity":{"family":"fields_types","pipeline_version_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","semantic_path":"/source"},"description":"schema evidence remains current","required_evidence_level":"live_read_only","actual_evidence_level":"cached_observed","observed_at":"2026-08-01T00:00:00Z","expires_at":"2026-08-02T00:00:00Z","verdict":"stale","evidence":[{"kind":"introspection","digest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}],"diagnostics":[{"code":"EVIDENCE_STALE","summary":"Schema evidence expired","remediation":"Refresh introspection","retryable":true}]}],"aggregate_verdict":"stale","supersedes_receipt_id":null,"created_at":"2026-08-01T00:00:00Z","retain_until":"2026-12-01T00:00:00Z","lifecycle_links":[]}`

const noEvidenceReceipt = `{"schema_version":1,"id":"no-evidence",` + candidate + `,"claims":[],"aggregate_verdict":"unverified","supersedes_receipt_id":null,"created_at":"2026-09-02T00:00:00Z","retain_until":"2026-12-01T00:00:00Z","lifecycle_links":[]}`

var (
	proofMu         sync.Mutex
	proofSetupFails bool
	proofSettings   = initialProofSettings()
	proofFixtureAt  = time.Now().UTC().Truncate(time.Second)
)

func initialProofSettings() api.DataSourceProofConfiguration {
	return api.DataSourceProofConfiguration{DataSourceSlug: "github", Name: "Pipeline repository", Provider: "github", RepositoryId: "123", Status: "Proof is disabled. Repository operations remain available.", Targets: []api.ProofAssessmentTarget{}, AvailableTargets: []api.ProofTargetContext{{ProjectSlug: "ops", EnvironmentSlug: "prod", TargetRef: "refs/heads/main", PipelineDirectory: "pipelines"}, {ProjectSlug: "ops", EnvironmentSlug: "staging", TargetRef: "refs/heads/main", PipelineDirectory: "pipelines"}}}
}

const metadataReceipt = `{"schema_version":1,"id":"metadata",` + candidate + `,"claims":[{"identity":{"family":"fields_types","pipeline_version_hash":"sha256:static","semantic_path":"/source"},"description":"Metadata compatibility","required_evidence_level":"static","actual_evidence_level":"static","observed_at":null,"expires_at":null,"verdict":"passed","evidence":[],"diagnostics":[]}],"aggregate_verdict":"passed","created_at":"2026-09-02T00:00:00Z","retain_until":"2026-12-01T00:00:00Z","lifecycle_links":[]}`

var liveReceipt = strings.NewReplacer(
	`"metadata"`, `"live"`,
	`"Metadata compatibility"`, `"Read-only connection assessment"`,
	`"static"`, `"live_read_only"`,
	`"observed_at":null`, `"observed_at":"2026-09-02T00:00:00Z"`,
	`"evidence":[]`, `"evidence":[{"kind":"validation","digest":"sha256:read-only-observation"}]`,
).Replace(metadataReceipt)
