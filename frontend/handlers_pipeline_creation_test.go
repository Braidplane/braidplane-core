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

const firstPipelineQuery = `{"slug":"orders","name":"Orders","data_source_slug":"warehouse","updated_at":"2026-10-21T00:00:00Z","parameters":[{"name":"count","data_type":"integer","is_required":true}],"query_statement":{"type":"postgres","sql":"SELECT :count"}}`

func TestQueryPipelineSetup(t *testing.T) {
	for _, scenario := range []string{"create", "stale", "required parameter", "invalid slug", "no repository", "restricted", "project unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			creates := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if scenario != "restricted" && handleTestAdminRolePermissions(w, req) {
					return
				}
				switch req.URL.Path {
				case "/api/organizations":
					writeJSON(w, `{"organizations":[{"slug":"acme-hq","name":"Acme","member_role":"admin"}]}`)
				case "/api/organizations/acme-hq/roles/admin":
					writeJSON(w, `{"slug":"admin","permissions":[{"resource":"pipelines","action":"read"}]}`)
				case "/api/organizations/acme-hq/data-sources/warehouse/queries/orders":
					writeJSON(w, firstPipelineQuery)
				case "/api/organizations/acme-hq/projects":
					if req.URL.Query().Get("cursor") == "next" {
						writeJSON(w, `{"projects":[{"slug":"ops","name":"Ops"}],"has_more":false}`)
					} else {
						writeJSON(w, `{"projects":[{"slug":"other","name":"Other"}],"has_more":true,"next_cursor":"next"}`)
					}
				case "/api/organizations/acme-hq/projects/ops":
					switch scenario {
					case "project unavailable":
						w.WriteHeader(http.StatusForbidden)
					case "no repository":
						writeJSON(w, `{"slug":"ops","name":"Ops"}`)
					default:
						writeJSON(w, `{"slug":"ops","name":"Ops","pipeline_backend":{"data_source_slug":"git-source","repo":"https://example.test/repo.git","tracked_branch":"main"}}`)
					}
				case "/api/organizations/acme-hq/projects/ops/pipeline-creations":
					creates++
					var body api.PipelineSpec
					if !assert.NoError(t, json.NewDecoder(req.Body).Decode(&body)) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.Equal(t, "orders-pipeline", body.Metadata.Slug)
					query, err := body.Steps[0].AsPipelineQueryStep()
					assert.NoError(t, err)
					assert.Equal(t, "source", query.Query.DataSourceRef)
					assert.Equal(t, "sql", string(query.Query.OperationType))
					assert.Equal(t, "orders", *query.Query.OperationRef)
					assert.InDelta(t, 42, (*query.Query.Parameters)["count"], 0)
					assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineCreationProposal{Slug: "creation-one", ProjectSlug: "ops", PipelineSlug: "orders-pipeline", Spec: body}))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			router, err := NewRouter(server.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			path := "/orgs/:slug/data-sources/:dsSlug/queries/:querySlug/pipeline"
			app.Post(path, router.handleQueryPipelineSetup)
			form := url.Values{"project": {"ops"}, "name": {"Orders pipeline"}, "query_revision": {"2026-10-21T00:00:00Z"}, "exec_count": {"42"}}
			if scenario == "stale" {
				form.Set("query_revision", "old")
			}
			if scenario == "required parameter" {
				form.Del("exec_count")
			}
			if scenario == "invalid slug" {
				form.Set("pipeline_slug", "BAD SLUG")
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orgs/acme-hq/data-sources/warehouse/queries/orders/pipeline", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response, err := app.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, response.Body.Close()) }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			if scenario == "create" {
				require.Equal(t, http.StatusSeeOther, response.StatusCode, string(body))
				require.Equal(t, 1, creates)
				require.Equal(t, "/orgs/acme-hq/projects/ops/pipeline-creations/creation-one", response.Header.Get("Location"))
			} else {
				require.Zero(t, creates)
				if scenario == "restricted" {
					require.Equal(t, http.StatusForbidden, response.StatusCode)
				} else {
					require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode, string(body))
					require.Contains(t, string(body), "Ops")
					require.Contains(t, string(body), "Ops")
				}
			}
		})
	}
}

func TestPipelineCreationProgressUsesRecordedSpecAndExactVersion(t *testing.T) {
	for _, scenario := range []string{"missing", "restricted version", "exact", "wrong identity", "retry"} {
		t.Run(scenario, func(t *testing.T) {
			var spec api.PipelineSpec
			require.NoError(t, json.Unmarshal([]byte(`{"metadata":{"slug":"orders-pipeline","name":"Retained definition"},"steps":[]}`), &spec))
			record := api.PipelineCreationProposal{Slug: "creation-one", ProjectSlug: "ops", PipelineSlug: "orders-pipeline", SpecHash: strings.Repeat("a", 64), Spec: spec, Status: api.CreationPublishFailed}
			retries := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if handleTestAdminRolePermissions(w, req) {
					return
				}
				switch req.URL.Path {
				case "/api/organizations":
					writeJSON(w, `{"organizations":[{"slug":"acme-hq","name":"Acme","member_role":"admin"}]}`)
				case "/api/organizations/acme-hq/projects":
					writeJSON(w, `{"projects":[{"slug":"ops","name":"Ops"}]}`)
				case "/api/organizations/acme-hq/projects/ops/environments":
					writeJSON(w, `{"project_environments":[{"environment_slug":"dev","environment_name":"Development","is_default":true}]}`)
				case "/api/organizations/acme-hq/projects/ops/pipeline-creations/creation-one":
					assert.NoError(t, json.NewEncoder(w).Encode(record))
				case "/api/organizations/acme-hq/projects/ops/pipeline-creations":
					retries++
					var actual api.PipelineSpec
					if !assert.NoError(t, json.NewDecoder(req.Body).Decode(&actual)) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.Equal(t, spec, actual)
					assert.NoError(t, json.NewEncoder(w).Encode(record))
				case "/api/organizations/acme-hq/projects/ops/pipelines/orders-pipeline/versions/" + record.SpecHash:
					switch scenario {
					case "restricted version":
						w.WriteHeader(http.StatusForbidden)
					case "exact", "wrong identity":
						pipelineSlug := record.PipelineSlug
						if scenario == "wrong identity" {
							pipelineSlug = "unrelated"
						}
						assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineVersion{PipelineSlug: pipelineSlug, SpecHash: record.SpecHash, Spec: spec}))
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				default:
					assert.NotContains(t, req.URL.Path, "/queries/")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			router, err := NewRouter(server.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			app.Get("/orgs/:slug/projects/:projectSlug/pipeline-creations/:creationSlug", router.handlePipelineCreationProgress)
			app.Post("/orgs/:slug/projects/:projectSlug/pipeline-creations/:creationSlug", router.handlePipelineCreationProgress)
			method := http.MethodGet
			if scenario == "retry" {
				method = http.MethodPost
			}
			response, err := app.Test(httptest.NewRequestWithContext(t.Context(), method, "/orgs/acme-hq/projects/ops/pipeline-creations/creation-one?environment_slug=dev", nil))
			require.NoError(t, err)
			defer func() { require.NoError(t, response.Body.Close()) }()
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			switch scenario {
			case "retry":
				require.Equal(t, http.StatusSeeOther, response.StatusCode)
				require.Equal(t, 1, retries)
			case "exact":
				require.Contains(t, string(body), "Configure source bindings")
				require.Contains(t, string(body), "spec_hash="+record.SpecHash)
			case "restricted version", "wrong identity":
				require.True(t, strings.Contains(string(body), "could not be checked") || strings.Contains(string(body), "could not be verified"))
				require.NotContains(t, string(body), "Configure source bindings")
			default:
				require.NotContains(t, string(body), "Deploy this version")
				require.Contains(t, string(body), "Retry this proposal")
			}
		})
	}
}

func TestBuilderCreatePipeline(t *testing.T) {
	for _, scenario := range []string{"create", "restricted", "malformed", "draft slug", "identity mismatch", "repository denied"} {
		t.Run(scenario, func(t *testing.T) {
			creates := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if scenario != "restricted" && handleTestAdminRolePermissions(w, req) {
					return
				}
				switch req.URL.Path {
				case "/api/organizations":
					writeJSON(w, `{"organizations":[{"slug":"acme-hq","name":"Acme","member_role":"admin"}]}`)
				case "/api/organizations/acme-hq/roles/admin":
					writeJSON(w, `{"slug":"admin","permissions":[{"resource":"pipelines","action":"read"}]}`)
				case "/api/organizations/acme-hq/projects/ops/pipeline-creations":
					creates++
					assert.Equal(t, "session=test-session", req.Header.Get("Cookie"))
					var spec api.PipelineSpec
					if !assert.NoError(t, json.NewDecoder(req.Body).Decode(&spec)) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					assert.Equal(t, "acme-users-sync", spec.Metadata.Slug)
					assert.Len(t, spec.Steps, 2)
					if scenario == "repository denied" {
						w.WriteHeader(http.StatusForbidden)
						writeJSON(w, `{"error":{"message":"Repository proposal access is required"}}`)
						return
					}
					project := "ops"
					if scenario == "identity mismatch" {
						project = "other"
					}
					assert.NoError(t, json.NewEncoder(w).Encode(api.PipelineCreationProposal{Slug: "creation-one", ProjectSlug: project, PipelineSlug: spec.Metadata.Slug, Spec: spec}))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			router, err := NewRouter(server.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			app.Post("/orgs/:slug/projects/:projectSlug/pipeline-creations", router.handleBuilderCreatePipeline)
			state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", mustReadPipelineFixture(t, "single-query.pipeline.yaml"))
			require.NoError(t, err)
			if scenario == "draft slug" {
				state.PipelineSpec.Metadata.Slug = "draft-pipeline"
			}
			body, err := serializeBuilderStateCanonical(state)
			require.NoError(t, err)
			if scenario == "malformed" {
				body = []byte(`{"pipeline_spec":`)
			}
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/orgs/acme-hq/projects/ops/pipeline-creations?environment_slug=dev", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Cookie", "session=test-session")
			response, err := app.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, response.Body.Close()) }()
			result, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			switch scenario {
			case "create":
				require.Equal(t, http.StatusOK, response.StatusCode, string(result))
				require.Contains(t, string(result), "/orgs/acme-hq/projects/ops/pipeline-creations/creation-one?environment_slug=dev")
			case "restricted", "repository denied":
				require.Equal(t, http.StatusForbidden, response.StatusCode, string(result))
			case "identity mismatch":
				require.Equal(t, http.StatusBadGateway, response.StatusCode, string(result))
			default:
				require.Equal(t, http.StatusUnprocessableEntity, response.StatusCode, string(result))
			}
			if scenario == "create" || scenario == "identity mismatch" || scenario == "repository denied" {
				require.Equal(t, 1, creates)
			} else {
				require.Zero(t, creates)
			}
		})
	}
}
