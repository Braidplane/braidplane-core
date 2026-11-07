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

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/gofiber/fiber/v3/middleware/static"

	apiwire "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const prefix = "/api/organizations/acme/projects/ops"

var (
	mu                        sync.Mutex
	imported, bound, deployed bool
	richCompiled              bool
	scopedCompiled            bool
)

const (
	version  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commit   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pipeline = workspacePipeline
)

func main() {
	api := httptest.NewServer(http.HandlerFunc(handleAPI))
	defer api.Close()
	notificationConsentOrigin = api.URL

	store := session.NewStore()
	router, err := frontend.NewRouter(api.URL, slog.New(slog.DiscardHandler), store)
	if err != nil {
		panic(err)
	}

	app := fiber.New()
	app.Use(func(c fiber.Ctx) error {
		c.Set("Content-Security-Policy", "form-action 'self'")
		return c.Next()
	})
	app.Get("/_test/session", func(c fiber.Ctx) error {
		return reqctx.SetUserSlug(c, store, slug.Slug("test-user"))
	})
	app.Get("/assets/icons/*", static.New("brand/icons"))
	app.Get("/assets/*", static.New("frontend/assets"))
	registerRunInspectionFixture(app)
	registerCompletionInspectionFixture(app)
	registerNotificationFixture(app)
	registerRecencyFixture(app)
	registerAttentionFixture(app)
	registerIncrementalFixture(app)
	registerBuilderReferenceFixture(app)
	registerRepositorySetupFixture(app)
	registerFirstPipelineFixture(app)
	registerModerationFixture(app)
	router.RegisterRoutes(app, frontend.CommunityPresentation{})

	listener, err := new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	notificationAppOrigin = "http://" + listener.Addr().String()
	fmt.Println(notificationAppOrigin)
	if err := app.Listener(listener, fiber.ListenConfig{DisableStartupMessage: true}); err != nil {
		panic(err)
	}
}

// Each browser test owns a fresh process; state only models the API contract behind the real SSR UI.
func handleAPI(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	write := func(body string) { _, _ = w.Write([]byte(body)) }
	if handleModerationAPI(w, r) {
		return
	}
	if firstPipelineEnabled && handleFirstPipelineAPI(w, r) {
		return
	}
	if recencyFixture && handleRecencyAPI(w, r) {
		return
	}
	if builderReferencesEnabled && handleBuilderReferenceAPI(w, r) {
		return
	}
	if attentionEnabled && handleAttentionAPI(w, r) {
		return
	}
	if notificationFixtureEnabled && handleNotificationAPI(w, r) {
		return
	}
	if incrementalEnabled && handleIncrementalAPI(w, r) {
		return
	}
	if runInspectionEnabled && handleRunInspectionAPI(w, r) {
		return
	}
	if repositorySetupEnabled && handleRepositorySetupAPI(w, r) {
		return
	}
	compiledDigest := "sha256:" + strings.Repeat("b", 64)
	if r.URL.Path == prefix+"/pipelines/daily-sync/versions/"+version+"/compiled-plans" || r.URL.Path == prefix+"/pipelines/daily-sync/compiled-plans/"+compiledDigest {
		if r.Method == http.MethodPost && r.URL.Query().Get("environment_slug") != "dev" {
			http.Error(w, "select the compilation environment", http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodPost {
			var body apiwire.PipelineCompilationRequest
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				http.Error(w, "invalid compilation selection", http.StatusBadRequest)
				return
			}
			if body.ResourceScopes != nil {
				if len(*body.ResourceScopes) != 1 || (*body.ResourceScopes)[0].Step != "fetch" || (*body.ResourceScopes)[0].Scope.DataSource != "source" || (*body.ResourceScopes)[0].Scope.S3.Bucket != "fixed-bucket" {
					http.Error(w, "unexpected resource scope selection", http.StatusBadRequest)
					return
				}
				scopedCompiled = true
			}
			if body.Projection != nil {
				if len(body.Projection.Inputs) != 1 || body.Projection.Inputs[0].Mapping.Id != "fixture/json/v1" {
					http.Error(w, "unexpected projection selection", http.StatusBadRequest)
					return
				}
				richCompiled = true
			}
		}
		projections := ""
		if richCompiled {
			projections = `,"projections":[{"input":0,"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row","digest":"sha256:revision","resolved_digest":"sha256:resolved","mapping_id":"fixture/json/v1","representation":"json","root_shape":"object"}]`
		}
		operations := `[]`
		if scopedCompiled {
			operations = `[{"step":"fetch","role":"query","operation_slug":"orders","revision":"retained","digest":"sha256:operation","resource_scope":{"policy_id":"s3/relative-key/v1","data_source":"source","operation":"orders","contract":"orders","revision":"v1","object_id":"row","representation":"rows"}}]`
		}
		w.Header().Set("Cache-Control", "no-store")
		write(`{"digest":"` + compiledDigest + `","spec_hash":"` + version + `","pipeline_slug":"daily-sync","status":"available","admission_enabled":true,"source_commit":"` + commit + `","source_path":"pipelines/daily-sync.yaml","pins":[],"operations":` + operations + projections + `}`)
		return
	}
	switch r.URL.Path {
	case "/api/organizations":
		write(`{"organizations":[{"name":"Acme","slug":"acme","member_role":"admin"}]}`)
	case "/api/organizations/acme":
		write(`{"name":"Acme","slug":"acme","member_role":"admin"}`)
	case "/api/organizations/acme/roles/admin":
		write(`{"slug":"admin","permissions":[{"resource":"pipelines","action":"read"},{"resource":"pipelines","action":"update"},{"resource":"pipelines","action":"execute"},{"resource":"projects","action":"read"},{"resource":"projects","action":"update"},{"resource":"datasources","action":"read"},{"resource":"datasources","action":"update"},{"resource":"data_sources","action":"update"}]}`)
	case "/api/organizations/acme/projects":
		write(`{"projects":[{"name":"Ops","slug":"ops"}]}`)
	case prefix:
		write(`{"name":"Ops","slug":"ops","pipeline_backend":{"data_source_slug":"git-source","repo":"https://example.com/pipelines.git","tracked_branch":"main","pipeline_dir":"pipelines"}}`)
	case prefix + "/environments":
		write(`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","is_default":true,"environment_protected":true}]}`)
	case prefix + "/pipelines":
		if imported {
			write(`{"pipelines":[` + pipeline + `]}`)
		} else {
			write(`{"pipelines":[]}`)
		}
	case prefix + "/pipelines/daily-sync":
		if !imported {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		write(pipeline)
	case prefix + "/pipelines:reconcile":
		var req struct {
			DryRun         bool `json:"dry_run"`
			ExpectedSource *struct {
				Commit string `json:"commit"`
			} `json:"expected_source"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !req.DryRun {
			if req.ExpectedSource == nil || req.ExpectedSource.Commit != commit {
				w.WriteHeader(http.StatusConflict)
				write(`{"error":{"code":"PIPELINE_RECONCILE_SOURCE_CHANGED","message":"Git source changed"}}`)
				return
			}
			imported = true
		}
		report := map[string]any{
			"dry_run": req.DryRun, "pipeline_dir": "pipelines",
			"source":   map[string]string{"data_source_slug": "git-source", "repo": "https://example.com/pipelines.git", "ref": "refs/heads/main", "commit": commit},
			"summary":  map[string]int{"synced": 1, "failed": 0},
			"synced":   []map[string]string{{"pipeline_slug": "daily-sync", "source_path": "pipelines/daily-sync.yaml", "spec_hash": version}},
			"failures": []map[string]string{},
		}
		if err := json.NewEncoder(w).Encode(report); err != nil {
			http.Error(w, "encode fixture report", http.StatusInternalServerError)
		}
	case prefix + "/pipelines/daily-sync/versions/" + version:
		if !imported {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var definition struct {
			Spec json.RawMessage `json:"spec"`
		}
		if err := json.Unmarshal([]byte(workspacePipeline), &definition); err != nil {
			panic(err)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"pipeline_slug": "daily-sync", "spec_hash": version, "git_commit_sha": commit, "spec": definition.Spec, "composition_admission_enabled": true}); err != nil {
			panic(err)
		}
	case prefix + "/environments/dev/pipelines/daily-sync/deployment":
		if !deployed {
			w.WriteHeader(http.StatusNotFound)
			write(`{"error":{"message":"Not deployed"}}`)
			return
		}
		write(`{"pipeline_slug":"daily-sync","environment_slug":"dev","current_version_spec_hash":"` + version + `"}`)
	case prefix + "/environments/dev/pipelines/daily-sync:deploy":
		var req struct {
			SpecHash           string  `json:"spec_hash"`
			Confirmation       string  `json:"protected_environment_confirmation"`
			ExpectedRevision   *int64  `json:"expected_revision"`
			CompiledPlanDigest *string `json:"compiled_plan_digest"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.SpecHash != version || req.Confirmation != "dev" || req.ExpectedRevision == nil || *req.ExpectedRevision != 0 || req.CompiledPlanDigest == nil || *req.CompiledPlanDigest != compiledDigest {
			w.WriteHeader(http.StatusBadRequest)
			write(`{"error":{"message":"Invalid version or protected environment confirmation"}}`)
			return
		}
		if !bound {
			w.WriteHeader(http.StatusBadRequest)
			write(`{"error":{"message":"Required data-source binding is missing"}}`)
			return
		}
		deployed = true
		write(`{"pipeline_slug":"daily-sync","environment_slug":"dev","current_version_spec_hash":"` + version + `"}`)
	case prefix + "/pipelines/daily-sync/environments/dev/data-source-bindings/source":
		if r.Method != http.MethodPut {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		bound = true
		write(`{}`)
	case prefix + "/data-source-refs":
		write(`{"data_source_refs":[{"pipeline_slug":"daily-sync","slug":"source","name":"Source","data_source_type":"postgres","required_capabilities":["pipeline_source","pipeline_sink"]}]}`)
	case "/api/organizations/acme/data-sources":
		write(`{"data_sources":[{"slug":"warehouse","name":"Warehouse","type":"postgres","is_active":true,"read_only":false},{"slug":"git-source","name":"Pipeline repository","type":"github","is_active":true,"read_only":false,"config":{"repository":{"id":"42","full_name":"acme/pipelines","web_url":"https://github.com/acme/pipelines"}}}]}`)
	case "/api/organizations/acme/data-sources/git-source/branches":
		write(`{"branches":[{"name":"main","is_default":true}]}`)
	case prefix + "/environments/dev/data-source-bindings":
		if bound {
			write(`{"data_source_bindings":[{"pipeline_slug":"daily-sync","environment_slug":"dev","data_source_ref_slug":"source","data_source_slug":"warehouse","data_source_name":"Warehouse","data_source_type":"postgres","data_source_active":true}]}`)
		} else {
			write(`{"data_source_bindings":[]}`)
		}
	case prefix + "/environments/dev/pipeline-validation-results":
		write(`{"validation_results":[]}`)
	default:
		if strings.HasSuffix(r.URL.Path, "/pipeline-deployments") {
			write(`{"deployments":[]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/runs") {
			write(`{"runs":[]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}
}

const workspacePipeline = `{
 "slug":"daily-sync","name":"Daily customer sync with a long operational name for regional warehouse reporting",
 "sync_status":"synced","current_version_spec_hash":"` + version + `",
 "source_ref":"refs/heads/main","source_path":"pipelines/customers/daily-sync.yaml","last_applied_commit":"` + commit + `",
 "spec":{"metadata":{"slug":"daily-sync","name":"Daily sync"},"steps":[
  {"kind":"query","slug":"fetch","name":"Fetch customers","query":{"data_source_ref":"source","operation_type":"sql","operation_ref":"customers","output_contract":{"name":"customers","representation":"rows","fields":[{"name":"email","type":"string","required":true}]}}},
  {"kind":"transform","slug":"normalize","name":"Normalize customers","transform":{"inputs":{"input":"fetch"},"sql":"SELECT email FROM input","output_kind":"rows","input_contract":{"name":"raw-customers","representation":"rows","fields":[{"name":"email","type":"string","required":true}]},"output_contract":{"name":"clean-customers","representation":"rows","fields":[{"name":"email","type":"string","required":true}]}}},
  {"kind":"sink","slug":"store","name":"Store customers","sink":{"input":"normalize","data_source_ref":"source","operation_type":"sql","operation_ref":"upsert-customers","idempotency_class":"deduplicated_by_ledger","input_contract":{"name":"stored-customers","representation":"rows","fields":[{"name":"email","type":"string","required":true}]}}}
 ]}}
`
