package frontend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

type testBuilderConflictPayload struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
	ConflictDiff *struct {
		CanonicalComparableJSON string `json:"canonical_comparable_json"`
		DraftComparableJSON     string `json:"draft_comparable_json"`
		ComparableUnifiedDiff   string `json:"comparable_unified_diff"`
	} `json:"conflict_diff"`
}

func handleTestBuilderProjectRuntimeContext(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects":
		writeJSON(
			w,
			`{"projects":[{"slug":"default","name":"Default project","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
		)
		return true
	case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments":
		writeJSON(
			w,
			`{"project_environments":[{"environment_slug":"dev","environment_name":"Development","environment_description":"Developer target","is_default":true,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"},{"environment_slug":"prod","environment_name":"Production","environment_description":"Customer target","environment_protected":true,"is_default":false,"enabled_at":"2026-03-02T00:00:00Z","updated_at":"2026-03-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`,
		)
		return true
	default:
		return false
	}
}

func TestLoadBuilderStateFromExample(t *testing.T) {
	t.Parallel()

	t.Run("LoadsSingleQueryExample", func(t *testing.T) {
		t.Parallel()

		state, source, err := loadBuilderStateFromExample(builderExampleSingleQuery)
		require.NoError(t, err)
		expectedSource, err := os.ReadFile("../examples/pipelines/yaml/single-query.pipeline.yaml")
		require.NoError(t, err)
		require.Equal(t, string(expectedSource), source)

		require.Len(t, state.Graph.Nodes, 2)
		require.Len(t, state.Graph.Edges, 1)
		require.Len(t, state.Graph.Bindings, 1)
	})

	t.Run("RejectsUnknownExample", func(t *testing.T) {
		t.Parallel()

		_, _, err := loadBuilderStateFromExample("missing")
		require.Error(t, err)
		require.ErrorContains(t, err, "unknown pipeline example")
	})
}

func TestHandleBuilderBlueprintPage(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations" {
			writeJSON(
				w,
				`{"organizations":[{"slug":"acme-hq","name":"ACME HQ","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z","member_role":"admin"}],"next_cursor":""}`,
			)
			return
		}

		if handleTestAdminRolePermissions(w, r) {
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	t.Run("DraftBuilderKeepsRunDisabled", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/builder", nil)
		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		html := string(body)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, html, `id="builder-validate-btn"`)
		require.Contains(t, html, `id="builder-publish-btn"`)
		require.Contains(t, html, `id="builder-run-btn"`)
		require.Contains(t, strings.Split(strings.Split(html, `id="builder-run-btn"`)[1], ">")[0], "disabled")
		require.Contains(t, html, `id="builder-save-btn"`)
		require.Contains(t, html, `id="builder-mode-source-btn"`)
		require.NotContains(t, html, `id="builder-export-btn"`)
		require.NotContains(t, html, `id="builder-export-format"`)
		require.NotContains(t, html, `aria-label="Export format"`)
		require.Contains(
			t,
			html,
			`Run is unavailable until an active project is selected.`,
		)
		require.Contains(
			t,
			html,
			`Save Draft is available after a pipeline has been materialized from the project Git repository.`,
		)
		require.Contains(t, html, `data-save-url=""`)
		require.Contains(t, html, `id="builder-connector-descriptors"`)
		require.Contains(t, html, `hubspot`)
		require.Contains(t, html, `forced_read_only`)
	})

	t.Run("ImportedGraphEnablesRun", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/builder?pipeline_example=single-query", nil)
		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, string(body), `id="builder-run-btn"`)
		require.Contains(
			t,
			string(body),
			`Run is unavailable until an active project is selected.`,
		)
		require.Contains(
			t,
			string(body),
			`Save to Braidplane is unavailable until an active organization and project are selected.`,
		)
		require.Contains(t, string(body), `data-save-url=""`)
		require.Contains(t, string(body), `id="builder-run-btn"`)
		require.Contains(t, strings.Split(strings.Split(string(body), `id="builder-run-btn"`)[1], ">")[0], "disabled")
	})

	t.Run("DraftQueryParamsPrefillPipelineMetadata", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodGet,
			"/builder?target_org_slug=acme-hq&project_slug=default&draft_name=Customer+Sync&draft_slug=customer-sync",
			nil)

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, string(body), `data-pipeline-name="Customer Sync"`)
		require.Contains(t, string(body), `data-pipeline-slug="customer-sync"`)
		require.Contains(t, string(body), `data-save-url="/orgs/acme-hq/projects/default/pipelines/save"`)
		require.Contains(
			t,
			string(body),
			`Save Draft is available once the canvas has at least one node.`,
		)
	})
}

func TestHandleBuilderBlueprintPage_DeepLinkedPipelineCarriesVersionContext(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"slug":"acme-hq","name":"ACME HQ","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z","member_role":"admin"}],"next_cursor":""}`,
			)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync":
			writeJSON(
				w,
				`{"slug":"acme-users-sync","name":"ACME Users Sync","sync_status":"synced","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_spec_hash":"spec-hash-13","last_applied_commit":null,"source_path":"pipelines/test.pipeline.yaml","source_ref":"refs/heads/main","spec":{"metadata":{"slug":"acme-users-sync","name":"ACME Users Sync"},"steps":[{"kind":"query","slug":"fetch-users","name":"Fetch users","query":{"data_source_ref":"acme-postgres","operation_type":"sql","operation_ref":"list-active-users"}}]},"draft":{"pipeline_slug":"acme-users-sync","based_on_spec_hash":"spec-hash-12","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","spec":{"metadata":{"slug":"acme-users-sync","name":"ACME Users Sync"},"steps":[{"kind":"query","slug":"fetch-users","name":"Fetch users","query":{"data_source_ref":"acme-postgres","operation_type":"sql","operation_ref":"list-active-users"}}]}}}`,
			)
			return
		}

		if handleTestBuilderProjectRuntimeContext(w, r) {
			return
		}
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodGet,
		"/builder?org_slug=acme-hq&project_slug=default&pipeline_slug=acme-users-sync&target_environment_slug=prod",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	// Allow scheduling headroom during parallel coverage runs.
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 3 * time.Second, FailOnTimeout: true})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `data-pipeline-expected-version-id="spec-hash-12"`)
	require.Contains(t, string(body), `data-pipeline-current-version-id="spec-hash-13"`)
	require.Contains(t, string(body), `data-pipeline-draft-based-on-version-id="spec-hash-12"`)
	require.Contains(t, string(body), `data-target-environment-slug="prod"`)
	require.Contains(t, string(body), `Run in prod`)
	require.Contains(t, string(body), `Target: prod`)
	require.NotContains(t, string(body), `data-pipeline-compatibility-mode=`)
}

func TestHandleBuilderBlueprintPage_DefaultsRuntimeTargetForDeepLinkedPipelineRun(t *testing.T) {
	t.Parallel()
	for _, canExecute := range []bool{false, true} {
		t.Run(fmt.Sprint(canExecute), func(t *testing.T) {
			apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
					writeJSON(
						w,
						`{"organizations":[{"slug":"acme-hq","name":"ACME HQ","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z","member_role":"admin"}],"next_cursor":""}`,
					)
					return
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync":
					writeJSON(
						w,
						`{"slug":"acme-users-sync","name":"ACME Users Sync","sync_status":"synced","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_spec_hash":"spec-hash-13","last_applied_commit":null,"source_path":"pipelines/test.pipeline.yaml","source_ref":"refs/heads/main","spec":{"metadata":{"slug":"acme-users-sync","name":"ACME Users Sync"},"steps":[{"kind":"query","slug":"fetch-users","name":"Fetch users","query":{"data_source_ref":"acme-postgres","operation_type":"sql","operation_ref":"list-active-users"}}]}}`,
					)
					return
				}

				if handleTestBuilderProjectRuntimeContext(w, r) {
					return
				}
				if r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/roles/admin" {
					action := "read"
					if canExecute {
						action = "*"
					}
					writeJSON(w, fmt.Sprintf(`{"slug":"admin","name":"Test role","permissions":[{"resource":"*","action":%q}],"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z"}`, action))
					return
				}

				http.NotFound(w, r)
			}))
			t.Cleanup(apiServer.Close)

			store := session.NewStore()
			router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
			require.NoError(t, err)

			app := fiber.New()
			registerTestSessionRoute(app, store)
			router.RegisterRoutes(app, CommunityPresentation{})

			authCookie := getAuthenticatedSessionCookie(t, app)

			req := httptest.NewRequestWithContext(context.Background(),
				http.MethodGet,
				"/builder?org_slug=acme-hq&project_slug=default&pipeline_slug=acme-users-sync",
				nil)

			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)

			// Allow scheduling headroom during parallel coverage runs.
			resp, err := app.Test(req, fiber.TestConfig{Timeout: 3 * time.Second, FailOnTimeout: true})
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			html := string(body)

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Contains(t, html, `data-target-environment-slug="dev"`)
			require.Contains(t, html, `Run in dev`)
			require.Contains(t, html, `Target: dev`)
			require.Contains(t, html, `id="builder-run-btn"`)
			button := strings.Split(strings.Split(html, `id="builder-run-btn"`)[1], ">")[0]
			require.Equal(t, !canExecute, strings.Contains(button, "disabled"))
			backfill := strings.Split(strings.Split(html, `id="builder-backfill-btn"`)[1], ">")[0]
			require.Equal(t, !canExecute, strings.Contains(backfill, "disabled"))
			if !canExecute {
				require.Contains(t, html, "You don&#39;t have permission to execute pipelines.")
			}
		})
	}
}

func TestHandleBuilderBlueprintPage_DeepLinkedMaterializedPipelineEnablesDraftActions(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"slug":"acme-hq","name":"ACME HQ","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z","member_role":"admin"}],"next_cursor":""}`,
			)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync":
			writeJSON(
				w,
				`{"slug":"acme-users-sync","name":"ACME Users Sync","sync_status":"synced","has_draft":true,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_id":13,"current_version_spec_hash":"spec-hash-13","last_applied_commit":"abc123","source_path":"pipelines/acme-users-sync.pipeline.yaml","source_ref":"refs/heads/main","spec":{"metadata":{"slug":"acme-users-sync","name":"ACME Users Sync"},"steps":[{"kind":"query","slug":"fetch-users","name":"Fetch users","query":{"data_source_ref":"acme-postgres","operation_type":"sql","operation_ref":"list-active-users"}}]},"draft":{"pipeline_slug":"acme-users-sync","based_on_version_id":13,"based_on_spec_hash":"spec-hash-13","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","spec":{"metadata":{"slug":"acme-users-sync","name":"ACME Users Sync"},"steps":[{"kind":"query","slug":"fetch-users","name":"Fetch users","query":{"data_source_ref":"acme-postgres","operation_type":"sql","operation_ref":"list-active-users"}}]}}}`,
			)
			return
		}

		if handleTestBuilderProjectRuntimeContext(w, r) {
			return
		}
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodGet,
		"/builder?org_slug=acme-hq&project_slug=default&pipeline_slug=acme-users-sync",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	// Allow scheduling headroom during parallel coverage runs.
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 3 * time.Second, FailOnTimeout: true})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotContains(t, string(body), `data-pipeline-managed-by`)
	require.Contains(t, string(body), `id="builder-save-btn" type="button"`)
	require.NotContains(t, string(body), `id="builder-save-btn" type="button" disabled`)
	require.Contains(t, string(body), `id="builder-publish-btn" type="button"`)
	require.NotContains(t, string(body), `id="builder-publish-btn" type="button" disabled`)
	require.Contains(t, string(body), `data-add-node-kind="query"`)
	require.NotContains(t, string(body), `data-palette-item="true" type="button" disabled`)
	require.Contains(t, string(body), `id="builder-source-editor"`)
	require.NotContains(t, string(body), `id="builder-source-editor" readonly`)
	require.NotContains(t, string(body), `id="builder-source-apply-btn" type="button" disabled`)
}

func TestHandleBuilderBlueprintPage_DeepLinkedPipelineAllowsHashWithoutNumericVersionID(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(
				w,
				`{"organizations":[{"slug":"acme-hq","name":"ACME HQ","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-01T00:00:00Z","member_role":"admin"}],"next_cursor":""}`,
			)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync":
			writeJSON(
				w,
				`{"slug":"acme-users-sync","name":"ACME Users Sync","sync_status":"synced","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_spec_hash":"spec-hash-13","last_applied_commit":null,"source_path":"pipelines/test.pipeline.yaml","source_ref":"refs/heads/main","spec":{"metadata":{"slug":"acme-users-sync","name":"ACME Users Sync"},"steps":[{"kind":"query","slug":"fetch-users","name":"Fetch users","query":{"data_source_ref":"acme-postgres","operation_type":"sql","operation_ref":"list-active-users"}}]}}`,
			)
			return
		}

		if handleTestBuilderProjectRuntimeContext(w, r) {
			return
		}
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodGet,
		"/builder?org_slug=acme-hq&project_slug=default&pipeline_slug=acme-users-sync",
		nil)

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)

	// Allow scheduling headroom during parallel coverage runs.
	resp, err := app.Test(req, fiber.TestConfig{Timeout: 3 * time.Second, FailOnTimeout: true})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(body), `data-pipeline-current-version-id="spec-hash-13"`)
}

func TestHandleBuilderSave(t *testing.T) {
	t.Parallel()

	payload := mustReadPipelineFixture(t, "single-query.pipeline.yaml")
	state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", payload)
	require.NoError(t, err)
	raw, err := serializeBuilderStateCanonical(state)
	require.NoError(t, err)
	var brandNewPipelineDraftSaveCalls atomic.Int32

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync/draft":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request body: %v", err)
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			spec, ok := body["spec"].(map[string]any)
			if !ok {
				t.Error("spec field missing or invalid")
				http.Error(w, "invalid spec", http.StatusBadRequest)
				return
			}
			metadata, ok := spec["metadata"].(map[string]any)
			if !ok {
				t.Error("metadata field missing or invalid")
				http.Error(w, "invalid metadata", http.StatusBadRequest)
				return
			}
			if metadata["name"] != "ACME Users Sync" {
				t.Errorf("unexpected pipeline name: %v", metadata["name"])
				http.Error(w, "unexpected pipeline name", http.StatusBadRequest)
				return
			}
			writeJSON(
				w,
				`{"draft":{"pipeline_slug":"acme-users-sync","based_on_spec_hash":"spec-hash-12","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","spec":{"metadata":{"slug":"acme-users-sync","name":"ACME Users Sync"},"steps":[{"kind":"query","slug":"fetch-users","name":"Fetch users","query":{"data_source_ref":"acme-postgres","operation_type":"sql","operation_ref":"list-active-users"}}]}}}`,
			)
			return
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/brand-new-pipeline/draft":
			brandNewPipelineDraftSaveCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(w, `{"organizations":[],"next_cursor":""}`)
			return
		}

		if handleTestAdminRolePermissions(w, r) {
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	t.Run("SavesPipelineDraftAndReturnsDraftPayload", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/orgs/acme-hq/projects/default/pipelines/save",
			strings.NewReader(string(raw)))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, string(body), `"draft"`)
		require.Contains(t, string(body), `"pipeline_slug":"acme-users-sync"`)
		require.Contains(t, string(body), `"based_on_spec_hash":"spec-hash-12"`)
	})

	t.Run("RejectsDraftSaveBeforeGitMaterialization", func(t *testing.T) {
		t.Parallel()

		for _, pipelineSlug := range []string{"", "draft-pipeline"} {
			var draftState map[string]any
			require.NoError(t, json.Unmarshal(raw, &draftState))
			pipelineSpec, ok := draftState["pipeline_spec"].(map[string]any)
			require.True(t, ok)
			metadata, ok := pipelineSpec["metadata"].(map[string]any)
			require.True(t, ok)
			metadata["slug"] = pipelineSlug
			metadata["name"] = "Brand New Pipeline"

			draftRaw, err := json.Marshal(draftState)
			require.NoError(t, err)

			req := httptest.NewRequestWithContext(context.Background(),
				http.MethodPost,
				"/orgs/acme-hq/projects/default/pipelines/save",
				bytes.NewReader(draftRaw))

			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)
			req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

			resp, err := app.Test(req)
			require.NoError(t, err)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())

			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			require.Contains(t, resp.Header.Get("Content-Type"), fiber.MIMEApplicationJSON)

			var errResp apierror.ErrorResponse
			require.NoError(t, json.Unmarshal(body, &errResp))
			require.NotNil(t, errResp.Error)
			require.Equal(t, apierror.CodeValidationFailed, errResp.Error.Code)
			require.Equal(t, "builder state is invalid", errResp.Error.Message)
			require.Equal(
				t,
				"Save Draft is available after a pipeline has been materialized from the project Git repository.",
				errResp.Error.DetailedError,
			)
		}

		require.Zero(t, brandNewPipelineDraftSaveCalls.Load())
	})

	t.Run("RejectsMalformedBuilderStatePayload", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/orgs/acme-hq/projects/default/pipelines/save",
			strings.NewReader(`{"pipeline_spec":`))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, resp.Header.Get("Content-Type"), fiber.MIMEApplicationJSON)

		var errResp apierror.ErrorResponse
		require.NoError(t, json.Unmarshal(body, &errResp))
		require.NotNil(t, errResp.Error)
		require.Equal(t, apierror.CodeValidationFailed, errResp.Error.Code)
		require.Equal(t, "builder state is invalid", errResp.Error.Message)
		require.Equal(t, "builder state payload is malformed", errResp.Error.DetailedError)
		require.NotContains(t, errResp.Error.DetailedError, "unexpected end of JSON input")
	})
}

func TestHandleBuilderSave_UnauthorizedReturnsJSONError(t *testing.T) {
	t.Parallel()

	payload := mustReadPipelineFixture(t, "single-query.pipeline.yaml")
	state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", payload)
	require.NoError(t, err)
	raw, err := serializeBuilderStateCanonical(state)
	require.NoError(t, err)

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync/draft":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"UNAUTHORIZED","message":"session expired"}}`))
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(w, `{"organizations":[],"next_cursor":""}`)
			return
		}

		if handleTestAdminRolePermissions(w, r) {
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/projects/default/pipelines/save",
		strings.NewReader(string(raw)))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.Contains(t, string(body), `"message":"Authentication required"`)
	require.Contains(
		t,
		string(body),
		`"detailed_error":"Your session has expired. Sign in again and retry the save."`,
	)
}

func TestHandleBuilderSave_ConflictIncludesComparableDiff(t *testing.T) {
	t.Parallel()

	payload := mustReadPipelineFixture(t, "single-query.pipeline.yaml")
	state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", payload)
	require.NoError(t, err)

	expectedVersionID := "spec-hash-12"
	currentVersionID := "spec-hash-13"
	state.Context.ExpectedVersionID = &expectedVersionID
	state.Context.CurrentVersionID = &currentVersionID
	raw, err := serializeBuilderStateCanonical(state)
	require.NoError(t, err)

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync/draft":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"PIPELINE_DRAFT_CONFLICT","message":"pipeline draft conflict"},"pipeline_slug":"acme-users-sync","expected_spec_hash":"spec-hash-12","current_spec_hash":"spec-hash-13","draft_based_on_spec_hash":"spec-hash-12","draft_preserved":true,"can_overwrite":false}`,
				),
			)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync":
			writeJSON(
				w,
				`{"slug":"acme-users-sync","name":"ACME Users Sync","sync_status":"synced","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_spec_hash":"spec-hash-13","last_applied_commit":null,"source_path":"pipelines/test.pipeline.yaml","source_ref":"refs/heads/main","draft":null,"spec":{"metadata":{"slug":"acme-users-sync","name":"ACME Users Sync"},"steps":[{"kind":"query","slug":"fetch-users","name":"Fetch users from canonical","query":{"data_source_ref":"acme-postgres","operation_type":"sql","operation_ref":"list-active-users"}}]}}`,
			)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(w, `{"organizations":[],"next_cursor":""}`)
			return
		}

		if handleTestAdminRolePermissions(w, r) {
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/projects/default/pipelines/save",
		strings.NewReader(string(raw)))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var body testBuilderConflictPayload
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "PIPELINE_DRAFT_CONFLICT", body.Error.Code)
	require.NotNil(t, body.ConflictDiff)

	var canonicalComparable map[string]any
	require.NoError(
		t,
		json.Unmarshal([]byte(body.ConflictDiff.CanonicalComparableJSON), &canonicalComparable),
	)
	require.Contains(t, canonicalComparable, "pipeline_spec")
	require.Contains(t, canonicalComparable, "graph")
	require.NotContains(t, canonicalComparable, "context")

	var draftComparable map[string]any
	require.NoError(
		t,
		json.Unmarshal([]byte(body.ConflictDiff.DraftComparableJSON), &draftComparable),
	)
	require.Contains(t, draftComparable, "pipeline_spec")
	require.Contains(t, draftComparable, "graph")
	require.NotContains(t, draftComparable, "context")

	require.Contains(t, body.ConflictDiff.ComparableUnifiedDiff, "--- canonical")
	require.Contains(t, body.ConflictDiff.ComparableUnifiedDiff, "+++ draft")
	require.Contains(t, body.ConflictDiff.ComparableUnifiedDiff, "Fetch users from canonical")
	require.NotContains(t, body.ConflictDiff.CanonicalComparableJSON, "expected_version_id")
	require.NotContains(t, body.ConflictDiff.DraftComparableJSON, "current_version_id")
	require.NotContains(t, body.ConflictDiff.DraftComparableJSON, "source_format")
}

func TestHandleBuilderSave_RetriesWithCurrentVersionWhenComparableDiffIsEmpty(t *testing.T) {
	t.Parallel()

	payload := mustReadPipelineFixture(t, "single-query.pipeline.yaml")
	state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", payload)
	require.NoError(t, err)

	expectedVersionID := "spec-hash-12"
	currentVersionID := "spec-hash-13"
	state.Context.ExpectedVersionID = &expectedVersionID
	state.Context.CurrentVersionID = &currentVersionID
	raw, err := serializeBuilderStateCanonical(state)
	require.NoError(t, err)
	pipeline, err := builderStateToDraftPipeline(state)
	require.NoError(t, err)
	specJSON, err := json.Marshal(pipeline)
	require.NoError(t, err)
	var apiSpec api.PipelineSpec
	require.NoError(t, json.Unmarshal(specJSON, &apiSpec))

	var seenExpectedVersionIDs []*string

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync/draft":
			var req api.SavePipelineDraftJSONRequestBody
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode save draft request: %v", err)
			}
			seenExpectedVersionIDs = append(seenExpectedVersionIDs, req.ExpectedSpecHash)

			if len(seenExpectedVersionIDs) == 1 {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write(
					[]byte(
						`{"error":{"code":"PIPELINE_DRAFT_CONFLICT","message":"pipeline draft conflict"},"pipeline_slug":"acme-users-sync","expected_spec_hash":"spec-hash-12","current_spec_hash":"spec-hash-13","draft_based_on_spec_hash":"spec-hash-12","draft_preserved":false,"can_overwrite":false}`,
					),
				)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			specHash := "spec-hash-13"
			createdAt := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
			updatedAt := time.Date(2026, time.March, 2, 0, 0, 0, 0, time.UTC)
			if err := json.NewEncoder(w).Encode(api.PipelineDraftResponse{
				Draft: api.PipelineDraft{
					BasedOnSpecHash: &specHash,
					CreatedAt:       createdAt,
					PipelineSlug:    "acme-users-sync",
					Spec:            apiSpec,
					UpdatedAt:       updatedAt,
				},
			}); err != nil {
				t.Fatalf("encode save draft response: %v", err)
			}
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync":
			writeJSON(w, fmt.Sprintf(
				`{"slug":"acme-users-sync","name":"ACME Users Sync","sync_status":"synced","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_spec_hash":"spec-hash-13","last_applied_commit":null,"source_path":"pipelines/test.pipeline.yaml","source_ref":"refs/heads/main","draft":{"pipeline_slug":"acme-users-sync","based_on_spec_hash":"spec-hash-13","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","spec":%s},"spec":%s}`,
				string(specJSON),
				string(specJSON),
			))
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(w, `{"organizations":[],"next_cursor":""}`)
			return
		}

		if handleTestAdminRolePermissions(w, r) {
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/projects/default/pipelines/save",
		strings.NewReader(string(raw)))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, seenExpectedVersionIDs, 2)
	require.Equal(t, &expectedVersionID, seenExpectedVersionIDs[0])
	require.Equal(t, &currentVersionID, seenExpectedVersionIDs[1])
	require.NotContains(t, string(body), `"PIPELINE_DRAFT_CONFLICT"`)
}

func TestHandleBuilderPublishPipelineDraft_ConflictIncludesComparableDiff(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync/draft:publish":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write(
				[]byte(
					`{"error":{"code":"PIPELINE_DRAFT_CONFLICT","message":"pipeline draft conflict"},"pipeline_slug":"acme-users-sync","expected_version_id":12,"current_version_id":13,"draft_based_on_version_id":12,"draft_preserved":true,"can_overwrite":true}`,
				),
			)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync":
			writeJSON(
				w,
				`{"slug":"acme-users-sync","name":"ACME Users Sync","sync_status":"synced","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_id":13,"current_version_spec_hash":"spec-hash-13","last_applied_commit":null,"source_path":"pipelines/test.pipeline.yaml","source_ref":"refs/heads/main","spec":{"metadata":{"slug":"acme-users-sync","name":"ACME Users Sync"},"steps":[{"kind":"query","slug":"fetch-users","name":"Canonical query","query":{"data_source_ref":"acme-postgres","operation_type":"sql","operation_ref":"list-active-users"}}]},"draft":{"pipeline_slug":"acme-users-sync","based_on_version_id":12,"based_on_spec_hash":"spec-hash-12","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","spec":{"metadata":{"slug":"acme-users-sync","name":"ACME Users Sync"},"steps":[{"kind":"query","slug":"fetch-users","name":"Draft query","query":{"data_source_ref":"acme-postgres","operation_type":"sql","operation_ref":"list-active-users"}}]}}}`,
			)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(w, `{"organizations":[],"next_cursor":""}`)
			return
		}

		if handleTestAdminRolePermissions(w, r) {
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/orgs/acme-hq/projects/default/pipelines/acme-users-sync/publish",
		strings.NewReader(`{"expected_version_id":12}`))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	var body testBuilderConflictPayload
	require.Equal(t, http.StatusConflict, resp.StatusCode)
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "PIPELINE_DRAFT_CONFLICT", body.Error.Code)
	require.NotNil(t, body.ConflictDiff)
	require.Contains(t, body.ConflictDiff.CanonicalComparableJSON, "Canonical query")
	require.Contains(t, body.ConflictDiff.DraftComparableJSON, "Draft query")
	require.Contains(t, body.ConflictDiff.ComparableUnifiedDiff, "--- canonical")
	require.Contains(t, body.ConflictDiff.ComparableUnifiedDiff, "+++ draft")
	require.NotContains(t, body.ConflictDiff.CanonicalComparableJSON, "context")
	require.NotContains(t, body.ConflictDiff.DraftComparableJSON, "draft_based_on_version_id")
}

func TestHandleBuilderDiscardPipelineDraft(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsNoContent", func(t *testing.T) {
		t.Parallel()

		apiServer := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync/draft":
					w.WriteHeader(http.StatusNoContent)
					return
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
					writeJSON(w, `{"organizations":[],"next_cursor":""}`)
					return
				}

				if handleTestAdminRolePermissions(w, r) {
					return
				}

				http.NotFound(w, r)
			}),
		)
		t.Cleanup(apiServer.Close)

		store := session.NewStore()
		router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
		require.NoError(t, err)

		app := fiber.New()
		registerTestSessionRoute(app, store)
		router.RegisterRoutes(app, CommunityPresentation{})

		authCookie := getAuthenticatedSessionCookie(t, app)

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/orgs/acme-hq/projects/default/pipelines/acme-users-sync/draft/discard",
			nil)

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Accept", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusNoContent, resp.StatusCode)
	})

	t.Run("PropagatesJSONNotFound", func(t *testing.T) {
		t.Parallel()

		apiServer := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodDelete && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/acme-users-sync/draft":
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write(
						[]byte(
							`{"error":{"code":"PIPELINE_DRAFT_NOT_FOUND","message":"pipeline draft not found"}}`,
						),
					)
					return
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
					writeJSON(w, `{"organizations":[],"next_cursor":""}`)
					return
				}

				if handleTestAdminRolePermissions(w, r) {
					return
				}

				http.NotFound(w, r)
			}),
		)
		t.Cleanup(apiServer.Close)

		store := session.NewStore()
		router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
		require.NoError(t, err)

		app := fiber.New()
		registerTestSessionRoute(app, store)
		router.RegisterRoutes(app, CommunityPresentation{})

		authCookie := getAuthenticatedSessionCookie(t, app)

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/orgs/acme-hq/projects/default/pipelines/acme-users-sync/draft/discard",
			nil)

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Accept", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		require.Contains(t, string(body), `"code":"PIPELINE_DRAFT_NOT_FOUND"`)
		require.Contains(t, resp.Header.Get("Content-Type"), fiber.MIMEApplicationJSON)
	})

	t.Run("Returns400ForInvalidOrgSlug", func(t *testing.T) {
		t.Parallel()

		apiServer := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/api/organizations" {
					writeJSON(w, `{"organizations":[],"next_cursor":""}`)
					return
				}
				if handleTestAdminRolePermissions(w, r) {
					return
				}
				http.NotFound(w, r)
			}),
		)
		t.Cleanup(apiServer.Close)

		store := session.NewStore()
		router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
		require.NoError(t, err)

		app := fiber.New()
		registerTestSessionRoute(app, store)
		router.RegisterRoutes(app, CommunityPresentation{})

		authCookie := getAuthenticatedSessionCookie(t, app)
		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/orgs/INVALID!/projects/default/pipelines/acme-users-sync/draft/discard",
			nil)

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Equal(t, "invalid organization slug", strings.TrimSpace(string(body)))
	})

	t.Run("Returns400ForInvalidPipelineSlug", func(t *testing.T) {
		t.Parallel()

		apiServer := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet && r.URL.Path == "/api/organizations" {
					writeJSON(w, `{"organizations":[],"next_cursor":""}`)
					return
				}
				if handleTestAdminRolePermissions(w, r) {
					return
				}
				http.NotFound(w, r)
			}),
		)
		t.Cleanup(apiServer.Close)

		store := session.NewStore()
		router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
		require.NoError(t, err)

		app := fiber.New()
		registerTestSessionRoute(app, store)
		router.RegisterRoutes(app, CommunityPresentation{})

		authCookie := getAuthenticatedSessionCookie(t, app)
		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/orgs/acme-hq/projects/default/pipelines/INVALID!/draft/discard",
			nil)

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Equal(t, "invalid pipeline slug", strings.TrimSpace(string(body)))
	})
}

func TestValidatePipelineViaAPI(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsStructuredValidationResponse", func(t *testing.T) {
		t.Parallel()

		apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want %s", r.Method, http.MethodPost)
			}
			if r.URL.Path != "/api/organizations/acme-hq/projects/default/environments/prod/pipeline-validation" {
				t.Errorf("path = %s, want /api/organizations/acme-hq/projects/default/environments/prod/pipeline-validation", r.URL.Path)
			}
			writeJSON(w, `{"valid":true,"diagnostics":[]}`)
		}))
		t.Cleanup(apiServer.Close)

		router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
		require.NoError(t, err)

		result, status, errResp, err := router.validatePipelineViaAPI(
			context.Background(),
			"",
			"acme-hq",
			"default",
			"prod",
			api.PipelineValidationRequest{Spec: api.PipelineSpec{}},
		)

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Nil(t, errResp)
		require.NotNil(t, result)
		require.True(t, result.Valid)
		require.Empty(t, result.Diagnostics)
	})

	t.Run("ReturnsAPIErrorResponse", func(t *testing.T) {
		t.Parallel()

		apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			writeJSON(w, `{"error":{"code":"FORBIDDEN","message":"forbidden"}}`)
		}))
		t.Cleanup(apiServer.Close)

		router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
		require.NoError(t, err)

		result, status, errResp, err := router.validatePipelineViaAPI(
			context.Background(),
			"",
			"acme-hq",
			"default",
			"prod",
			api.PipelineValidationRequest{Spec: api.PipelineSpec{}},
		)

		require.Error(t, err)
		require.Nil(t, result)
		require.Equal(t, http.StatusForbidden, status)
		require.NotNil(t, errResp)
		require.NotNil(t, errResp.Error)
		require.Equal(t, "FORBIDDEN", errResp.Error.Code)
	})
}

func TestValidateBuilderPipelineTargetUsesValidAPIResult(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want %s", r.Method, http.MethodPost)
		}
		if r.URL.Path != "/api/organizations/acme-hq/projects/default/environments/prod/pipeline-validation" {
			t.Errorf("path = %s, want /api/organizations/acme-hq/projects/default/environments/prod/pipeline-validation", r.URL.Path)
		}
		writeJSON(w, `{"valid":true,"diagnostics":[]}`)
	}))
	t.Cleanup(apiServer.Close)

	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)

	payload := mustReadPipelineFixture(t, "single-query.pipeline.yaml")
	state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", payload)
	require.NoError(t, err)
	state.Context.TargetOrgSlug = "acme-hq"
	state.Context.TargetProjectSlug = "default"
	state.Context.TargetEnvironmentSlug = "prod"

	apiErr, status, errResp, validationErr := router.validateBuilderPipelineTarget(context.Background(), "", state)

	require.NoError(t, validationErr)
	require.Nil(t, apiErr)
	require.Equal(t, http.StatusOK, status)
	require.Nil(t, errResp)

	state.Context.TargetEnvironmentSlug = ""
	apiErr, status, _, validationErr = router.validateBuilderPipelineTarget(t.Context(), "", state)
	require.Error(t, validationErr)
	require.Equal(t, http.StatusBadRequest, status)
	require.Contains(t, apiErr.DetailedError, "runtime target")
}

func TestHandleBuilderExport(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations" {
			writeJSON(w, `{"organizations":[],"next_cursor":""}`)
			return
		}
		if r.Method == http.MethodPost &&
			r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/pipeline-validation" {
			writeJSON(w, `{
				"valid": false,
				"diagnostics": [
					{
						"severity": "error",
						"code": "PIPELINE_INVALID_INPUT",
						"field": "steps[0].query",
						"step_slug": "fetch-users",
						"message": "query target must set exactly one of operation_ref or operation_inline"
					}
				]
			}`)
			return
		}

		if handleTestAdminRolePermissions(w, r) {
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	t.Run("ExportsCanonicalPipelineYAML", func(t *testing.T) {
		t.Parallel()

		payload := mustReadPipelineFixture(t, "single-query.pipeline.yaml")
		state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", payload)
		require.NoError(t, err)

		raw, err := serializeBuilderStateCanonical(state)
		require.NoError(t, err)

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/builder/export",
			strings.NewReader(string(raw)))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, resp.Header.Get("Content-Type"), "application/yaml")
		require.Equal(
			t,
			`attachment; filename="acme-users-sync.pipeline.yaml"`,
			resp.Header.Get("Content-Disposition"),
		)
		require.Contains(t, string(body), "slug: fetch-users")
		require.Contains(t, string(body), "data_source_ref: acme-postgres")
		require.NotContains(t, string(body), "backend:")
	})

	t.Run("ExportsCanonicalPipelineJSON", func(t *testing.T) {
		t.Parallel()

		payload := mustReadPipelineFixture(t, "single-query.pipeline.yaml")
		state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", payload)
		require.NoError(t, err)
		jsonFmt := model.PipelineFormatJSON
		state.Context.ExportFormat = &jsonFmt

		raw, err := serializeBuilderStateCanonical(state)
		require.NoError(t, err)

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/builder/export",
			strings.NewReader(string(raw)))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, resp.Header.Get("Content-Type"), fiber.MIMEApplicationJSON)
		require.Equal(
			t,
			`attachment; filename="acme-users-sync.pipeline.json"`,
			resp.Header.Get("Content-Disposition"),
		)
		require.Contains(t, string(body), `"slug": "fetch-users"`)
		require.Contains(t, string(body), `"data_source_ref": "acme-postgres"`)
		require.NotContains(t, string(body), `"backend":`)
	})

	t.Run("RejectsInvalidBuilderState", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/builder/export",
			strings.NewReader(`{"pipeline_spec":{}}`))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, strings.TrimSpace(string(body)), "builder state is invalid")
	})

	t.Run("RejectsMalformedBuilderStatePayload", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/builder/export",
			strings.NewReader(`{"pipeline_spec":`))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, resp.Header.Get("Content-Type"), fiber.MIMEApplicationJSON)

		var errResp apierror.ErrorResponse
		require.NoError(t, json.Unmarshal(body, &errResp))
		require.NotNil(t, errResp.Error)
		require.Equal(t, apierror.CodeValidationFailed, errResp.Error.Code)
		require.Equal(t, "builder state is invalid", errResp.Error.Message)
		require.Equal(t, "builder state payload is malformed", errResp.Error.DetailedError)
		require.NotContains(t, errResp.Error.DetailedError, "unexpected end of JSON input")
	})

	t.Run("ReturnsValidationErrorsForDuplicateNodeSlug", func(t *testing.T) {
		t.Parallel()

		payload := `{
		  "pipeline_spec": {
		    "metadata": {
		      "slug": "test-pipeline",
		      "name": "Test Pipeline"
		    },
		    "steps": [
		      {
		        "kind": "query",
		        "slug": "node-1",
		        "query": {
		          "data_source_ref": "acme-postgres",
		          "operation_type": "sql",
		          "operation_ref": "list-users"
		        }
		      }
		    ]
		  },
		  "graph": {
		    "nodes": [
		      {"step_slug":"node-1","x":2,"y":1},
		      {"step_slug":"node-1","x":9,"y":1}
		    ],
		    "edges": [],
		    "bindings": []
		  }
		}`

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/builder/export",
			strings.NewReader(payload))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, resp.Header.Get("Content-Type"), fiber.MIMEApplicationJSON)

		var errResp apierror.ErrorResponse
		require.NoError(t, json.Unmarshal(body, &errResp))
		require.NotNil(t, errResp.Error)
		require.Equal(t, apierror.CodeValidationFailed, errResp.Error.Code)
		require.Equal(t, "builder state is invalid", errResp.Error.Message)
		require.NotEmpty(t, errResp.Error.ValidationErrors)
		require.Equal(t, "graph.nodes[1].step_slug", errResp.Error.ValidationErrors[0].Field)
		require.Equal(t, "DUPLICATE_GRAPH_NODE_STEP_SLUG", errResp.Error.ValidationErrors[0].Code)
		require.Contains(t, errResp.Error.ValidationErrors[0].Message, "duplicate graph node")
	})

	t.Run("RejectsSemanticallyInvalidPipelineStepInput", func(t *testing.T) {
		t.Parallel()

		payload := mustReadPipelineFixture(t, "single-query.pipeline.yaml")
		state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", payload)
		require.NoError(t, err)

		for i := range state.Graph.Bindings {
			if state.Graph.Bindings[i].FromPort == "rows" {
				state.Graph.Bindings[i].FromPort = "payload"
				break
			}
		}

		raw, err := json.Marshal(state)
		require.NoError(t, err)

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/builder/export",
			strings.NewReader(string(raw)))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var errResp apierror.ErrorResponse
		require.NoError(t, json.Unmarshal(body, &errResp))
		require.NotNil(t, errResp.Error)
		require.Equal(t, apierror.CodeValidationFailed, errResp.Error.Code)
		require.Equal(t, "builder state is invalid", errResp.Error.Message)
		require.NotEmpty(t, errResp.Error.ValidationErrors)
		require.Equal(t, "graph.bindings[0].from_port", errResp.Error.ValidationErrors[0].Field)
		require.Equal(t, "SINK_BINDING_SOURCE_PORT_INVALID", errResp.Error.ValidationErrors[0].Code)
		require.Contains(t, errResp.Error.ValidationErrors[0].Message, "expects source port")
	})

	t.Run("ExportsDefinitionAndSeparatelyChecksRuntimeTarget", func(t *testing.T) {
		t.Parallel()

		payload := mustReadPipelineFixture(t, "single-query.pipeline.yaml")
		state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", payload)
		require.NoError(t, err)
		state.Context.TargetOrgSlug = "acme-hq"
		state.Context.TargetProjectSlug = "default"
		state.Context.TargetEnvironmentSlug = "prod"

		raw, err := json.Marshal(state)
		require.NoError(t, err)

		localRequest := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/builder/export", strings.NewReader(string(raw)))
		localRequest.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, localRequest)
		localRequest.Header.Set("Content-Type", fiber.MIMEApplicationJSON)
		localResponse, err := app.Test(localRequest)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, localResponse.StatusCode)
		localBody, err := io.ReadAll(localResponse.Body)
		require.NoError(t, err)
		require.NoError(t, localResponse.Body.Close())
		require.Contains(t, string(localBody), "slug: fetch-users")
		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/builder/validate",
			strings.NewReader(string(raw)))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)

		var errResp apierror.ErrorResponse
		require.NoError(t, json.Unmarshal(body, &errResp))
		require.NotNil(t, errResp.Error)
		require.Equal(t, apierror.CodeValidationFailed, errResp.Error.Code)
		require.Equal(t, "builder state is invalid", errResp.Error.Message)
		require.NotEmpty(t, errResp.Error.ValidationErrors)
		require.Equal(t, "steps[0].query", errResp.Error.ValidationErrors[0].Field)
		require.Equal(t, "PIPELINE_INVALID_INPUT", errResp.Error.ValidationErrors[0].Code)
		require.Contains(
			t,
			errResp.Error.ValidationErrors[0].Message,
			"query target must set exactly one of operation_ref or operation_inline",
		)
	})
}

func TestHandleBuilderExportPreservesValidationAPIStatus(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost &&
			r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/pipeline-validation" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("validation service unavailable"))
			return
		}

		if handleTestAdminRolePermissions(w, r) {
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)
	payload := mustReadPipelineFixture(t, "single-query.pipeline.yaml")
	state, err := builderStateFromPipelineSpec("single-query.pipeline.yaml", payload)
	require.NoError(t, err)
	state.Context.TargetOrgSlug = "acme-hq"
	state.Context.TargetProjectSlug = "default"
	state.Context.TargetEnvironmentSlug = "prod"

	raw, err := json.Marshal(state)
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(context.Background(),
		http.MethodPost,
		"/builder/validate",
		strings.NewReader(string(raw)))

	req.Header.Set("Cookie", authCookie)
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	var errResp apierror.ErrorResponse
	require.NoError(t, json.Unmarshal(body, &errResp))
	require.NotNil(t, errResp.Error)
	require.Equal(t, apierror.CodeInternalServerError, errResp.Error.Code)
	require.Equal(t, "An unexpected error occurred", errResp.Error.Message)
}

func TestHandleBuilderImport(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/organizations" {
			writeJSON(w, `{"organizations":[],"next_cursor":""}`)
			return
		}

		if handleTestAdminRolePermissions(w, r) {
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	for _, tc := range []struct {
		name    string
		format  string
		fixture string
	}{
		{"ExplicitYAML", "yaml", "single-query.pipeline.yaml"},
		{"InferredYAML", "", "single-query.pipeline.yaml"},
		{"ExplicitJSON", "json", "single-query.pipeline.json"},
		{"InferredJSON", "", "single-query.pipeline.json"},
		{"UnknownFormatInfersJSON", "unknown", "single-query.pipeline.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			payload := map[string]string{
				"format": tc.format,
				"source": string(mustReadPipelineFixture(t, tc.fixture)),
			}

			raw, err := json.Marshal(payload)
			require.NoError(t, err)

			req := httptest.NewRequestWithContext(context.Background(),
				http.MethodPost,
				"/builder/import",
				strings.NewReader(string(raw)))

			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)
			req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			require.Equal(t, http.StatusOK, resp.StatusCode)
			require.Contains(t, resp.Header.Get("Content-Type"), fiber.MIMEApplicationJSON)

			var state builderState
			require.NoError(t, json.Unmarshal(body, &state))
			require.Equal(t, "acme-users-sync", state.PipelineSpec.Metadata.Slug.String())
			require.Equal(t, "ACME Users Sync", state.PipelineSpec.Metadata.Name)
			require.Len(t, state.Graph.Nodes, 2)
			require.Len(t, state.Graph.Edges, 1)
			require.Len(t, state.Graph.Bindings, 1)
		})
	}

	for _, tc := range []struct {
		name   string
		format string
		source string
	}{
		{"MalformedYAML", "yaml", "apiVersion: v1\nsteps:\n  - kind: ["},
		{"InferredJSONArray", "", "[]"},
		{"ExplicitJSONRejectsYAML", "json", string(mustReadPipelineFixture(t, "single-query.pipeline.yaml"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(map[string]string{"format": tc.format, "source": tc.source})
			require.NoError(t, err)

			req := httptest.NewRequestWithContext(context.Background(),
				http.MethodPost,
				"/builder/import",
				bytes.NewReader(raw))

			req.Header.Set("Cookie", authCookie)
			addCSRFToken(t, app, req)
			req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			require.Equal(t, http.StatusBadRequest, resp.StatusCode)
			require.Contains(t, resp.Header.Get("Content-Type"), fiber.MIMEApplicationJSON)

			var errResp apierror.ErrorResponse
			require.NoError(t, json.Unmarshal(body, &errResp))
			require.NotNil(t, errResp.Error)
			require.Equal(t, apierror.CodeValidationFailed, errResp.Error.Code)
			require.Equal(t, "pipeline source is invalid", errResp.Error.Message)
			require.Equal(
				t,
				"pipeline source could not be loaded into the builder",
				errResp.Error.DetailedError,
			)
			require.NotContains(t, errResp.Error.DetailedError, "load pipeline spec")
		})
	}
}

func TestHandleExecutePipelineSubmit(t *testing.T) {
	t.Parallel()

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/pipelines/acme-users-sync:execute":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(
				w,
				`{"metadata":{"pipeline_slug":"acme-users-sync","run_slug":"run-001","status":"queued","duration_ms":0,"step_count":0,"step_slug":"","started_at":"2026-03-16T11:00:00Z","ended_at":null,"created_at":"2026-03-16T11:00:00Z","request_id":"req-123"},"timeline":{"run_slug":"run-001","pipeline_slug":"acme-users-sync","status":"queued","events":[],"snapshots":[]}}`,
			)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(w, `{"organizations":[],"next_cursor":""}`)
			return
		}

		if handleTestAdminRolePermissions(w, r) {
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	t.Run("ExecutesPipelineAndReturnsRunRecord", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/orgs/acme-hq/projects/default/pipelines/acme-users-sync/execute?environment_slug=prod",
			strings.NewReader(`{}`))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusAccepted, resp.StatusCode)
		require.Contains(t, string(body), `"run_slug":"run-001"`)
	})

	t.Run("RejectsInvalidOrganizationSlug", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/orgs/INVALID!/projects/default/pipelines/acme-users-sync/execute",
			strings.NewReader(`{}`))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, string(body), "invalid organization slug")
	})

	t.Run("SurfacesPlainTextBackendExecutionError", func(t *testing.T) {
		t.Parallel()

		plainTextServer := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/pipelines/acme-users-sync:execute":
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = io.WriteString(w, "pipeline slug not found in organization")
					return
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
					writeJSON(w, `{"organizations":[],"next_cursor":""}`)
					return
				}

				if handleTestAdminRolePermissions(w, r) {
					return
				}

				http.NotFound(w, r)
			}),
		)
		t.Cleanup(plainTextServer.Close)

		store := session.NewStore()
		router, err := NewRouter(plainTextServer.URL, slog.New(slog.DiscardHandler), store)
		require.NoError(t, err)

		app := fiber.New()
		registerTestSessionRoute(app, store)
		router.RegisterRoutes(app, CommunityPresentation{})

		authCookie := getAuthenticatedSessionCookie(t, app)
		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/orgs/acme-hq/projects/default/pipelines/acme-users-sync/execute?environment_slug=prod",
			strings.NewReader(`{}`))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
		require.Contains(t, string(body), "pipeline slug not found in organization")
	})
}

func TestHandleGetCanonicalPipelineState(t *testing.T) {
	t.Parallel()

	const pipelineJSON = `{"slug":"customer-sync","name":"Customer Sync","sync_status":"synced","has_draft":false,"created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_id":12,"current_version_spec_hash":"spec-hash-12","spec":{"metadata":{"slug":"customer-sync","name":"Customer Sync"},"steps":[{"kind":"query","slug":"load-customers","name":"Load Customers","query":{"data_source_ref":"prod-db","operation_type":"sql","operation_ref":"list-customers"}},{"kind":"sink","slug":"customer-sync","sink":{"idempotency_class":"idempotent_with_key","input":"load-customers","data_source_ref":"prod-db","operation_type":"sql","operation_ref":"upsert-customers"}}]}}`

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestAdminRolePermissions(w, r) {
			return
		}

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/customer-sync":
			writeJSON(w, pipelineJSON)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(
				w,
				`{"error":{"code":"NOT_FOUND","message":"pipeline not found"}}`,
			)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
			writeJSON(w, `{"organizations":[],"next_cursor":""}`)
			return
		}

		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)

	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})

	authCookie := getAuthenticatedSessionCookie(t, app)

	t.Run("ReturnsBuilderStateForCanonicalPipeline", func(t *testing.T) {
		t.Parallel()

		// Canonical pipeline has no draft; the response should reflect the canonical spec only.
		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodGet,
			"/orgs/acme-hq/projects/default/pipelines/customer-sync/canonical-state",
			nil)

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, resp.Header.Get("Content-Type"), fiber.MIMEApplicationJSON)
		require.Contains(t, string(body), `"pipeline_spec"`)
		require.Contains(t, string(body), `"graph"`)
		require.Contains(t, string(body), `"customer-sync"`)
	})

	t.Run("Returns404WhenPipelineNotFound", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodGet,
			"/orgs/acme-hq/projects/default/pipelines/missing/canonical-state",
			nil)

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusNotFound, resp.StatusCode)
		require.Contains(t, string(body), "pipeline not found")
	})

	t.Run("Returns400ForInvalidOrgSlug", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodGet,
			"/orgs/INVALID!/projects/default/pipelines/customer-sync/canonical-state",
			nil)

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, string(body), "invalid organization slug")
	})

	t.Run("Returns400ForInvalidPipelineSlug", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodGet,
			"/orgs/acme-hq/projects/default/pipelines/INVALID!/canonical-state",
			nil)

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, string(body), "invalid pipeline slug")
	})

	t.Run("Returns400WhenCanonicalPipelineSourceIsInvalid", func(t *testing.T) {
		t.Parallel()

		invalidSpecServer := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if handleTestAdminRolePermissions(w, r) {
					return
				}

				switch {
				case r.Method == http.MethodGet &&
					r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/invalid-spec":
					writeJSON(
						w,
						`{"slug":"invalid-spec","name":"Invalid Spec","sync_status":"synced","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-02T00:00:00Z","current_version_id":5,"current_version_spec_hash":"spec-hash-5","spec":{"metadata":{},"steps":[]}}`,
					)
					return
				case r.Method == http.MethodGet && r.URL.Path == "/api/organizations":
					writeJSON(w, `{"organizations":[],"next_cursor":""}`)
					return
				}

				http.NotFound(w, r)
			}),
		)
		t.Cleanup(invalidSpecServer.Close)

		invalidSpecStore := session.NewStore()
		invalidSpecRouter, err := NewRouter(
			invalidSpecServer.URL,
			slog.New(slog.DiscardHandler),
			invalidSpecStore,
		)
		require.NoError(t, err)

		invalidSpecApp := fiber.New()
		registerTestSessionRoute(invalidSpecApp, invalidSpecStore)
		invalidSpecRouter.RegisterRoutes(invalidSpecApp, CommunityPresentation{})

		invalidSpecCookie := getAuthenticatedSessionCookie(t, invalidSpecApp)
		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodGet,
			"/orgs/acme-hq/projects/default/pipelines/invalid-spec/canonical-state",
			nil)

		req.Header.Set("Cookie", invalidSpecCookie)
		addCSRFToken(t, invalidSpecApp, req)

		resp, err := invalidSpecApp.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusBadRequest, resp.StatusCode)
		require.Contains(t, resp.Header.Get("Content-Type"), fiber.MIMEApplicationJSON)

		var errResp apierror.ErrorResponse
		require.NoError(t, json.Unmarshal(body, &errResp))
		require.NotNil(t, errResp.Error)
		require.Equal(t, apierror.CodeValidationFailed, errResp.Error.Code)
		require.Equal(t, "pipeline source is invalid", errResp.Error.Message)
		require.Equal(
			t,
			"canonical pipeline source could not be loaded into the builder",
			errResp.Error.DetailedError,
		)
		require.NotContains(t, errResp.Error.DetailedError, "load pipeline spec")
	})

	t.Run("Returns401WhenUnauthorized", func(t *testing.T) {
		t.Parallel()

		unauthorizedServer := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet &&
					r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/customer-sync" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(
						w,
						`{"error":{"code":"UNAUTHORIZED","message":"session expired"}}`,
					)
					return
				}
				if handleTestAdminRolePermissions(w, r) {
					return
				}
				http.NotFound(w, r)
			}),
		)
		t.Cleanup(unauthorizedServer.Close)

		unauthStore := session.NewStore()
		unauthRouter, err := NewRouter(
			unauthorizedServer.URL,
			slog.New(slog.DiscardHandler),
			unauthStore,
		)
		require.NoError(t, err)

		unauthApp := fiber.New()
		registerTestSessionRoute(unauthApp, unauthStore)
		unauthRouter.RegisterRoutes(unauthApp, CommunityPresentation{})

		unauthCookie := getAuthenticatedSessionCookie(t, unauthApp)
		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodGet,
			"/orgs/acme-hq/projects/default/pipelines/customer-sync/canonical-state",
			nil)

		req.Header.Set("Cookie", unauthCookie)

		resp, err := unauthApp.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}

func TestHandleBuilderPublishPipelineDraft(t *testing.T) {
	t.Parallel()

	const publishedPipelineJSON = `{"slug":"customer-sync","name":"Customer Sync","sync_status":"synced","created_at":"2026-03-01T00:00:00Z","updated_at":"2026-03-03T00:00:00Z","current_version_spec_hash":"spec-hash-13","spec":{"metadata":{"slug":"customer-sync","name":"Customer Sync"},"steps":[{"kind":"query","slug":"load-customers","query":{"data_source_ref":"prod-db","operation_type":"postgres","operation_ref":"top-users"}}]}}`
	const conflictJSON = `{"error":{"code":"PIPELINE_DRAFT_CONFLICT","message":"version conflict"},"pipeline_slug":"customer-sync","expected_spec_hash":"spec-hash-12","current_spec_hash":"spec-hash-13","draft_based_on_spec_hash":"spec-hash-12","draft_preserved":true,"can_overwrite":true}`

	t.Run("Returns200WithPublishedPipelineOnSuccess", func(t *testing.T) {
		t.Parallel()

		apiServer := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost &&
					r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/customer-sync/draft:publish" {
					writeJSON(w, publishedPipelineJSON)
					return
				}
				if handleTestAdminRolePermissions(w, r) {
					return
				}
				http.NotFound(w, r)
			}),
		)
		t.Cleanup(apiServer.Close)

		store := session.NewStore()
		router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
		require.NoError(t, err)

		app := fiber.New()
		registerTestSessionRoute(app, store)
		router.RegisterRoutes(app, CommunityPresentation{})

		authCookie := getAuthenticatedSessionCookie(t, app)
		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/orgs/acme-hq/projects/default/pipelines/customer-sync/publish",
			strings.NewReader(`{"expected_spec_hash":"spec-hash-12","force":false}`))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.Contains(t, string(body), `"slug":"customer-sync"`)
		require.Contains(t, string(body), `"spec-hash-13"`)
	})

	t.Run("Returns409WithConflictPayloadOnVersionConflict", func(t *testing.T) {
		t.Parallel()

		apiServer := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost &&
					r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/customer-sync/draft:publish" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusConflict)
					_, _ = io.WriteString(w, conflictJSON)
					return
				}
				if handleTestAdminRolePermissions(w, r) {
					return
				}
				http.NotFound(w, r)
			}),
		)
		t.Cleanup(apiServer.Close)

		store := session.NewStore()
		router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
		require.NoError(t, err)

		app := fiber.New()
		registerTestSessionRoute(app, store)
		router.RegisterRoutes(app, CommunityPresentation{})

		authCookie := getAuthenticatedSessionCookie(t, app)
		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/orgs/acme-hq/projects/default/pipelines/customer-sync/publish",
			strings.NewReader(`{"expected_version_id":12,"force":false}`))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusConflict, resp.StatusCode)
		require.Contains(t, string(body), `"draft_preserved":true`)
		require.Contains(t, string(body), `"can_overwrite":true`)
		require.Contains(t, string(body), `"current_spec_hash":"spec-hash-13"`)
	})

	t.Run("Returns401JSONErrorWhenSessionExpired", func(t *testing.T) {
		t.Parallel()

		apiServer := httptest.NewServer(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost &&
					r.URL.Path == "/api/organizations/acme-hq/projects/default/pipelines/customer-sync/draft:publish" {
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = io.WriteString(
						w,
						`{"error":{"code":"UNAUTHORIZED","message":"session expired"}}`,
					)
					return
				}
				if handleTestAdminRolePermissions(w, r) {
					return
				}
				http.NotFound(w, r)
			}),
		)
		t.Cleanup(apiServer.Close)

		store := session.NewStore()
		router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
		require.NoError(t, err)

		app := fiber.New()
		registerTestSessionRoute(app, store)
		router.RegisterRoutes(app, CommunityPresentation{})

		authCookie := getAuthenticatedSessionCookie(t, app)
		req := httptest.NewRequestWithContext(context.Background(),
			http.MethodPost,
			"/orgs/acme-hq/projects/default/pipelines/customer-sync/publish",
			strings.NewReader(`{}`))

		req.Header.Set("Cookie", authCookie)
		addCSRFToken(t, app, req)
		req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)

		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		require.Contains(t, string(body), "Sign in again and retry publish")
	})
}

func TestHandleBootstrapPipelineCheckpointsSubmit(t *testing.T) {
	t.Parallel()
	requests := make(chan api.BootstrapPipelineCheckpointsRequest, 1)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/organizations/acme-hq/projects/default/environments/prod/pipelines/users-sync:bootstrap-checkpoints" {
			var payload api.BootstrapPipelineCheckpointsRequest
			if json.NewDecoder(r.Body).Decode(&payload) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			requests <- payload
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)
	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/orgs/acme-hq/projects/default/pipelines/users-sync/bootstrap-checkpoints?environment_slug=prod", strings.NewReader(`{"request_slug":"bootstrap-001","source_seeds":{"source":{"value":9007199254740993}}}`))
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	req.Header.Set("Content-Type", fiber.MIMEApplicationJSON)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	payload := <-requests
	require.Equal(t, "bootstrap-001", payload.RequestSlug)
	require.Equal(t, `{"value":9007199254740993}`, string(payload.SourceSeeds["source"]))
}
