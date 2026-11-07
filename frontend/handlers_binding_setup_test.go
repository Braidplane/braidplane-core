package frontend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const bindingSetupHash = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func TestPipelineBindingSetupReturnScope(t *testing.T) {
	t.Parallel()
	base := "/orgs/acme-hq/projects/default/pipelines/http-customer-enrichment"
	deploy := views.PipelineDeployHref("acme-hq", "default", "prod", "http-customer-enrichment", bindingSetupHash)
	for _, target := range []string{
		deploy, base + "?environment_slug=prod&section=deployments#pipeline-bindings",
		"https://evil.test" + deploy, "//evil.test" + deploy,
		strings.Replace(deploy, "acme-hq", "foreign", 1), strings.Replace(deploy, "default", "foreign", 1),
		strings.Replace(deploy, "http-customer-enrichment", "foreign", 1), strings.Replace(deploy, "prod", "dev", 1),
		strings.Replace(deploy, url.QueryEscape(bindingSetupHash), "other-version", 1),
		"/\\evil.test", base + "/runs?environment_slug=prod",
	} {
		t.Run(target, func(t *testing.T) {
			withDatasourceFormContext(t, url.Values{}, func(c fiber.Ctx) {
				c.Request().URI().SetQueryString(url.Values{"binding_return": {target}}.Encode())
				got := pipelineBindingSetupReturnHref(c, "acme-hq", "default", "http-customer-enrichment", "prod", bindingSetupHash)
				if target == deploy || target == base+"?environment_slug=prod&section=deployments#pipeline-bindings" {
					require.Equal(t, target, got)
				} else {
					require.Equal(t, deploy, got)
				}
			})
		})
	}
}

func TestPipelineBindingSetupReturnsToScopedCreation(t *testing.T) {
	back := views.PipelineCreationProgressHref("acme-hq", "default", "creation-one", "prod")
	fallback := views.PipelineDeployHref("acme-hq", "default", "prod", "customer-sync", bindingSetupHash)
	for _, target := range []string{back + "&token=private", "https://evil.test" + back, strings.Replace(back, "acme-hq", "other", 1), strings.Replace(back, "default", "other", 1), strings.Replace(back, "prod", "dev", 1), strings.Replace(back, "creation-one", "creation-one/delete", 1)} {
		withDatasourceFormContext(t, url.Values{}, func(c fiber.Ctx) {
			c.Request().URI().SetQueryString(url.Values{"binding_return": {target}}.Encode())
			got := pipelineBindingSetupReturnHref(c, "acme-hq", "default", "customer-sync", "prod", bindingSetupHash)
			if target == back+"&token=private" {
				require.Equal(t, back, got)
			} else {
				require.Equal(t, fallback, got)
			}
		})
	}
}

func TestPipelineBindingSetupSelectedVersionAndFailures(t *testing.T) {
	t.Parallel()
	specBytes, err := os.ReadFile("testdata/binding-setup/customer-enrichment.json")
	require.NoError(t, err)
	var spec api.PipelineSpec
	require.NoError(t, json.Unmarshal(specBytes, &spec))
	pipeline := spec.Metadata.Slug
	prefix := "/api/organizations/acme-hq/projects/default"
	for _, scenario := range []string{"missing", "invalid", "bound", "disabled", "no target", "denied", "read-only", "load failure", "version failure", "version expired", "binding expired", "validation expired", "validation failure", "no requirements", "save", "save error", "save expired", "delete", "delete error"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			mutations, currentReads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/organizations/acme-hq/roles/admin" && (scenario == "denied" || scenario == "read-only") {
					permissions := []map[string]string{{"resource": "projects", "action": "read"}}
					if scenario == "read-only" {
						permissions = append(permissions, map[string]string{"resource": "pipelines", "action": "read"})
					}
					assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"slug": "admin", "permissions": permissions}))
					return
				}
				if handleTestAdminRolePermissions(w, r) {
					return
				}
				switch r.URL.Path {
				case prefix + "/pipelines/" + pipeline:
					currentReads++
					// Current V2 is intentionally a different definition and must never supply selected V1 requirements.
					writeJSON(w, `{"slug":"`+pipeline+`","current_version_spec_hash":"current-v2","spec":{"metadata":{"slug":"`+pipeline+`"},"steps":[]}}`)
				case prefix + "/pipelines/" + pipeline + "/versions/" + bindingSetupHash:
					if scenario == "version expired" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if scenario == "version failure" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					selected := spec
					if scenario == "no requirements" {
						selected.Steps = nil
					}
					assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineVersion{PipelineSlug: pipeline, SpecHash: bindingSetupHash, Spec: selected}))
				case prefix + "/data-source-refs":
					writeJSON(w, `{"data_source_refs":[{"pipeline_slug":"`+pipeline+`","slug":"orders-api","required_capabilities":["pipeline_source"]},{"pipeline_slug":"`+pipeline+`","slug":"customer-db","required_capabilities":["pipeline_source"]},{"pipeline_slug":"`+pipeline+`","slug":"enriched-export","required_capabilities":["pipeline_sink"]},{"pipeline_slug":"another-pipeline","slug":"orders-api","required_capabilities":["pipeline_sink"]}]}`)
				case prefix + "/environments/prod/data-source-bindings":
					if scenario == "binding expired" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if scenario == "load failure" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					bindings := []api.DataSourceBinding{{PipelineSlug: "another-pipeline", DataSourceRefSlug: "orders-api", DataSourceSlug: "foreign-source", DataSourceActive: true}}
					if scenario == "bound" || scenario == "invalid" {
						for ref, typ := range map[string]api.DataSourceType{"orders-api": api.DataSourceTypeHttp, "customer-db": api.DataSourceTypePostgres, "enriched-export": api.DataSourceTypeS3} {
							bindings = append(bindings, api.DataSourceBinding{PipelineSlug: pipeline, DataSourceRefSlug: ref, DataSourceSlug: "source-" + ref, DataSourceName: "Shared name", DataSourceType: typ, DataSourceActive: scenario == "bound"})
						}
					}
					assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data_source_bindings": bindings}))
				case prefix + "/environments/prod/pipeline-validation-results":
					if scenario == "validation expired" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if scenario == "validation failure" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					writeJSON(w, `{"validation_results":[{"spec_hash":"current-v2","valid":true,"environment_slug":"prod"}]}`)
				case prefix + "/pipelines/" + pipeline + "/environments/prod/data-source-bindings/orders-api":
					mutations++
					if scenario == "save expired" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if strings.HasSuffix(scenario, "error") {
						w.WriteHeader(http.StatusForbidden)
						writeJSON(w, `{"error":{"message":"Binding change denied"}}`)
						return
					}
					if r.Method == http.MethodDelete {
						w.WriteHeader(http.StatusNoContent)
					} else {
						writeJSON(w, `{}`)
					}
				default:
					writeProjectBindingReloadFixture(w, r)
				}
			}))
			defer server.Close()
			app, cookie := setupBindingActionTestApp(t, server.URL)
			environment := "prod"
			if scenario == "disabled" {
				environment = "disabled"
			}
			if scenario == "no target" {
				environment = ""
			}
			back := views.PipelineDeployHref("acme-hq", "default", environment, pipeline, bindingSetupHash)
			path := views.PipelineBindingSetupHref("acme-hq", "default", environment, pipeline, bindingSetupHash, back)
			method, form := http.MethodGet, url.Values{}
			if strings.HasPrefix(scenario, "save") || strings.HasPrefix(scenario, "delete") {
				action := "upsert"
				if strings.HasPrefix(scenario, "delete") {
					action = "delete"
				}
				u, err := url.Parse(path)
				require.NoError(t, err)
				u.Path = strings.TrimSuffix(u.Path, "/bindings") + "/environments/prod/data-source-bindings/orders-api/" + action
				q := u.Query()
				q.Set("binding_setup", "1")
				u.RawQuery = q.Encode()
				path = u.String()
				method = http.MethodPost
				form.Set("data_source_slug", "orders-source")
			}
			req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(form.Encode()))
			req.Header.Set("Cookie", cookie)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if method == http.MethodPost {
				req.Header.Set("HX-Request", "true")
				addCSRFToken(t, app, req)
			}
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			html := string(body)
			require.Zero(t, currentReads, "current V2 must not replace selected V1")
			switch scenario {
			case "denied":
				require.Equal(t, http.StatusForbidden, resp.StatusCode)
			case "save expired", "version expired", "binding expired", "validation expired":
				header := "HX-Redirect"
				if method == http.MethodGet {
					require.Equal(t, http.StatusSeeOther, resp.StatusCode)
					header = "Location"
				}
				target, err := url.Parse(resp.Header.Get(header))
				require.NoError(t, err)
				require.Equal(t, "/auth/login", target.Path)
				recovery, err := url.Parse(target.Query().Get("return_to"))
				require.NoError(t, err)
				require.True(t, strings.HasSuffix(recovery.Path, "/bindings"))
				require.Equal(t, bindingSetupHash, recovery.Query().Get("spec_hash"))
				require.Equal(t, back, recovery.Query().Get("binding_return"))
			case "save", "delete":
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.Equal(t, "true", resp.Header.Get("HX-Refresh"))
				target, err := url.Parse(resp.Header.Get("HX-Redirect"))
				require.NoError(t, err)
				require.Equal(t, bindingSetupHash, target.Query().Get("spec_hash"))
				require.Equal(t, back, target.Query().Get("binding_return"))
				require.Equal(t, "binding-cell-"+pipeline+"-orders-api-prod", target.Fragment)
			default:
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.Contains(t, html, bindingSetupHash)
				require.Contains(t, html, "Return to selected pipeline")
				require.Contains(t, html, "binding_return=")
				require.NotContains(t, html, "foreign-source")
				switch scenario {
				case "missing":
					require.Contains(t, html, "Review orders-api")
					require.Contains(t, html, "Review enriched-export")
				case "invalid":
					require.Contains(t, html, "bound data source is inactive")
				case "bound":
					require.Contains(t, html, "All required references are bound")
					require.NotContains(t, html, "Review orders-api")
				case "disabled":
					require.Contains(t, html, "not enabled for the project")
					require.Contains(t, html, `>disabled</span>`)
				case "no target":
					require.Contains(t, html, "Select a runtime target to see its binding attention.")
					require.NotContains(t, html, "not enabled for the project")
				case "read-only":
					require.Contains(t, html, "Read-only for your role.")
				case "load failure":
					require.Contains(t, html, "Binding state could not be loaded")
				case "version failure":
					require.Contains(t, html, "current definition has not been substituted")
					require.NotContains(t, html, "No data-source requirements")
				case "validation failure":
					require.Contains(t, html, "Validation history could not be loaded")
				case "no requirements":
					require.Contains(t, html, "No data-source requirements")
				case "save error", "delete error":
					require.Contains(t, html, "Binding change denied")
					require.Contains(t, html, "spec_hash=")
				}
				if scenario != "validation failure" && scenario != "disabled" && scenario != "no target" {
					require.Contains(t, html, "Stale validation")
				}
			}
			if method == http.MethodGet {
				require.Zero(t, mutations, "navigation must be read-only")
			} else {
				require.Equal(t, 1, mutations)
			}
		})
	}
}
