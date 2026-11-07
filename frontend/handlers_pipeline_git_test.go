package frontend

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPipelineGitWorkflow(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"preview", "apply", "stale", "missing preview", "changed directory", "deploy", "protected", "bindings", "deployment conflict", "disabled target", "unauthorized", "cross tenant", "csrf", "pipeline named sync", "compile projection", "invalid projection", "compile scopes", "invalid scopes"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if handleTestAdminRolePermissions(w, req) {
					return
				}
				path := req.URL.Path
				switch {
				case path == "/api/organizations":
					role := "admin"
					if scenario == "unauthorized" {
						role = "viewer"
					}
					_, _ = w.Write([]byte(`{"organizations":[{"slug":"acme-hq","name":"Acme","member_role":"` + role + `"}]}`))
				case path == "/api/organizations/acme-hq/projects":
					_, _ = w.Write([]byte(`{"projects":[{"slug":"default","name":"Default"}]}`))
				case path == "/api/organizations/acme-hq/projects/default/pipelines/sync":
					calls++
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"error":{"message":"Pipeline unavailable"}}`))
				case path == "/api/organizations/acme-hq/projects/default":
					_, _ = w.Write([]byte(`{"slug":"default","name":"Default","pipeline_backend":{"data_source_slug":"git-source","repo":"https://example.com/repo.git","tracked_branch":"main"}}`))
				case path == "/api/organizations/acme-hq/projects/default/environments":
					_, _ = w.Write([]byte(`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","is_default":true,"environment_protected":true}]}`))
				case strings.HasSuffix(path, "/pipelines/daily-sync/versions/exact-version/compiled-plans"):
					calls++
					assert.Equal(t, "dev", req.URL.Query().Get("environment_slug"))
					var body api.PipelineCompilationRequest
					if !assert.NoError(t, json.NewDecoder(req.Body).Decode(&body)) || !assert.NotNil(t, body.Projection) || !assert.Len(t, body.Projection.Inputs, 1) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.Equal(t, "mapping/v1", body.Projection.Inputs[0].Mapping.Id)
					assert.Len(t, body.Projection.Inputs[0].Mapping.Types, 5)
					if scenario == "compile scopes" {
						if !assert.NotNil(t, body.ResourceScopes) || !assert.Len(t, *body.ResourceScopes, 1) {
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						assert.Equal(t, "source", (*body.ResourceScopes)[0].Scope.DataSource)
					}
					_, _ = w.Write([]byte(`{"pipeline_slug":"daily-sync","spec_hash":"exact-version","digest":"sha256:retained","status":"available","admission_enabled":true,"pins":[],"operations":[]}`))
				case strings.HasSuffix(path, "/pipelines/daily-sync/versions/exact-version"):
					_, _ = w.Write([]byte(`{"pipeline_slug":"daily-sync","spec_hash":"exact-version","git_commit_sha":"abc123","composition_admission_enabled":true}`))
				case strings.HasSuffix(path, "/pipelines:reconcile"):
					calls++
					var body api.PipelineReconcileRequest
					if !assert.NoError(t, json.NewDecoder(req.Body).Decode(&body)) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if !assert.NotNil(t, body.DryRun) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.Equal(t, &[]api.Slug{"daily-sync"}, body.PipelineSlugs)
					if scenario == "preview" {
						assert.True(t, *body.DryRun)
						assert.Nil(t, body.ExpectedSource)
					} else {
						assert.False(t, *body.DryRun)
						if !assert.NotNil(t, body.ExpectedSource) {
							return
						}
						assert.Equal(t, "abc123", body.ExpectedSource.Commit)
						assert.Equal(t, "git-source", body.ExpectedSource.DataSourceSlug)
					}
					if scenario == "stale" {
						w.WriteHeader(http.StatusConflict)
						_, _ = w.Write([]byte(`{"error":{"message":"changed"}}`))
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"pipeline_dir": "pipelines", "dry_run": *body.DryRun, "source": map[string]string{"data_source_slug": "git-source", "repo": "https://example.com/repo.git", "ref": "refs/heads/main", "commit": "abc123"},
						"summary":  map[string]int{"synced": 1, "failed": 1},
						"synced":   []map[string]string{{"pipeline_slug": "daily-sync", "source_path": "pipelines/daily-sync.yaml", "spec_hash": "exact-version"}},
						"failures": []map[string]string{{"pipeline_path": "pipelines/broken.yaml", "stage": "load", "message": "secret provider error MUST NOT RENDER"}},
					})
				case strings.HasSuffix(path, "/pipelines/daily-sync/deployment") || strings.HasSuffix(path, "/pipelines/daily-sync:deploy"):
					if req.Method == http.MethodGet {
						_, _ = w.Write([]byte(`{"pipeline_slug":"daily-sync","environment_slug":"dev","current_version_spec_hash":"old-version","revision":7}`))
						return
					}
					calls++
					var body api.PipelineDeploymentRequest
					if !assert.NoError(t, json.NewDecoder(req.Body).Decode(&body)) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.Equal(t, "exact-version", body.SpecHash)
					if !assert.NotNil(t, body.ExpectedCurrentSpecHash) {
						return
					}
					assert.Equal(t, "old-version", *body.ExpectedCurrentSpecHash)
					assert.NotNil(t, body.ExpectedRevision)
					if body.ExpectedRevision != nil {
						assert.EqualValues(t, 7, *body.ExpectedRevision)
					}
					if scenario == "bindings" || scenario == "protected" {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":{"message":"Missing binding or protected environment confirmation"}}`))
						return
					}
					if scenario == "deployment conflict" {
						w.WriteHeader(http.StatusConflict)
						_, _ = w.Write([]byte(`{"error":{"message":"Deployment conflict"}}`))
						return
					}
					if !assert.NotNil(t, body.ProtectedEnvironmentConfirmation) {
						return
					}
					assert.Equal(t, "dev", *body.ProtectedEnvironmentConfirmation)
					_, _ = w.Write([]byte(`{"pipeline_slug":"daily-sync","environment_slug":"dev","current_version_spec_hash":"exact-version"}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			store := session.NewStore()
			router, err := NewRouter(server.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)
			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})
			cookie := getAuthenticatedSessionCookie(t, app)
			path := "/orgs/acme-hq/projects/default/pipeline-sync"
			form := url.Values{"pipeline_dir": {"pipelines"}, "action": {"apply"}, "pipeline_slug": {"daily-sync"}, "environment_slug": {"dev"}, "data_source_slug": {"git-source"}, "repo": {"https://example.com/repo.git"}, "ref": {"refs/heads/main"}, "commit": {"abc123"}}
			switch scenario {
			case "preview":
				form.Set("action", "preview")
			case "missing preview":
				form.Del("commit")
			case "changed directory":
				form.Set("pipeline_dir", "previous-directory")
			case "cross tenant":
				path = "/orgs/other/projects/default/pipeline-sync"
			case "deploy", "protected", "bindings", "deployment conflict", "disabled target", "compile projection", "invalid projection", "compile scopes", "invalid scopes":
				path = "/orgs/acme-hq/projects/default/pipelines/daily-sync/deploy"
				form = url.Values{"spec_hash": {"exact-version"}, "environment_slug": {"dev"}, "expected_current_spec_hash": {"old-version"}, "expected_revision": {"7"}, "protected_environment_confirmation": {"dev"}}
				if scenario == "compile projection" || scenario == "invalid projection" || scenario == "compile scopes" || scenario == "invalid scopes" {
					form.Set("action", "compile")
					form.Set("projection", `{"inputs":[{"contract_slug":"orders","revision_slug":"v1","schema_object_id":"row","digest":"sha256:exact","mapping":{"id":"mapping/v1","representation":"json","root_shape":"object","types":["object","integer","instant","vector","number"]}}]}`)
					if scenario == "compile scopes" {
						form.Set("resource_scopes", `[{"step":"fetch","role":"query","scope":{"policy_id":"s3/relative-key/v1","data_source":"source","operation":"orders","contract":"orders","revision":"v1","object_id":"row","representation":"rows","s3":{"endpoint":"https://storage.example","region":"test","bucket":"fixed-bucket","prefix":"imports","dynamic_key":true,"force_path_style":false}}}]`)
					}
					if scenario == "invalid scopes" {
						form.Set("resource_scopes", `[{"credential":"private"}]`)
					}
					if scenario == "invalid projection" {
						form.Set("projection", `{"document":"private-body"}`)
					}
				}
				if scenario == "disabled target" {
					form.Set("environment_slug", "disabled")
				}
				if scenario == "protected" {
					form.Del("protected_environment_confirmation")
				}
			}
			method := http.MethodPost
			if scenario == "pipeline named sync" {
				method = http.MethodGet
				path = "/orgs/acme-hq/projects/default/pipelines/sync"
			}
			req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			req.Header.Set("Cookie", cookie)
			if scenario != "csrf" {
				addCSRFToken(t, app, req)
			}
			response, err := app.Test(req, fiber.TestConfig{Timeout: 0})
			require.NoError(t, err)
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			html := string(body)
			switch scenario {
			case "compile projection", "compile scopes":
				require.Equal(t, 1, calls)
				require.Contains(t, html, "sha256:retained")
				require.Contains(t, html, "Rich projection selection")
				require.Contains(t, html, "vector")
				require.Contains(t, html, "Compile selected version")
				require.NotContains(t, html, "Deployed exact-version")
			case "invalid scopes":
				require.Equal(t, 0, calls)
				require.Contains(t, html, "Enter one valid resource scope JSON array")
			case "invalid projection":
				require.Zero(t, calls)
				require.Contains(t, html, "Enter one valid projection selection JSON object")
			case "pipeline named sync":
				require.Equal(t, 1, calls)
				require.Contains(t, html, "Pipeline details")
			case "unauthorized", "cross tenant", "csrf":
				require.Equal(t, http.StatusForbidden, response.StatusCode)
				require.Zero(t, calls)
			case "changed directory":
				require.Contains(t, html, "Git directory changed since preview")
				require.Zero(t, calls)
			case "missing preview":
				require.Contains(t, html, "Preview Git changes before applying")
				require.Zero(t, calls)
			case "disabled target":
				require.Contains(t, html, "Choose an environment enabled")
				require.Zero(t, calls)
			case "stale":
				require.Contains(t, html, "Git source changed since preview")
				require.NotContains(t, html, "Apply Git changes")
			case "preview":
				require.Contains(t, html, "Sync preview")
				require.Contains(t, html, `name="commit" value="abc123"`)
				require.NotContains(t, html, ">Deploy version</a>")
			case "apply":
				require.Contains(t, html, "Sync completed with failures")
				require.Contains(t, html, "spec_hash=exact-version")
				require.Contains(t, html, "pipelines/broken.yaml")
				require.NotContains(t, html, "MUST NOT RENDER")
			case "deploy":
				require.Contains(t, html, "Deployed exact-version to dev")
				require.Contains(t, html, "Open pipeline to run")
				require.Contains(t, html, `href="/builder?org_slug=acme-hq&amp;pipeline_slug=daily-sync&amp;project_slug=default&amp;target_environment_slug=dev"`)
			case "deployment conflict":
				require.Contains(t, html, "Refresh deployment state")
				require.NotContains(t, html, ">Deploy to dev</button>")
			default:
				require.Contains(t, html, "Missing binding or protected environment confirmation")
				require.Contains(t, html, `value="exact-version"`)
				require.NotContains(t, html, "Deployed exact-version")
			}
		})
	}
}
