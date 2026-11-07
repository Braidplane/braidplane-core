package frontend

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRouterHandleProofReceiptPageRendersMismatchAndLifecycleLinks(t *testing.T) {
	t.Parallel()
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		switch r.URL.Path {
		case "/api/organizations":
			_, _ = w.Write([]byte(`{"organizations":[{"name":"Acme","slug":"acme","member_role":"admin","created_at":"2026-09-02T00:00:00Z","updated_at":"2026-09-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
		case "/api/organizations/acme/github-app-connection":
			_, _ = w.Write([]byte(`{"repositories":[{"id":999,"full_name":"wrong/repo"},{"id":123,"full_name":"acme/pipelines"}]}`))
		case "/api/organizations/acme/projects":
			_, _ = w.Write([]byte(`{"projects":[{"name":"Ops","slug":"ops","created_at":"2026-09-02T00:00:00Z","updated_at":"2026-09-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
		case "/api/organizations/acme/projects/ops/environments":
			_, _ = w.Write([]byte(`{"project_environments":[{"environment_slug":"prod","environment_name":"Production","environment_description":null,"is_default":true,"enabled_at":"2026-09-02T00:00:00Z","updated_at":"2026-09-02T00:00:00Z"}],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
		case "/api/organizations/acme/projects/ops/environments/prod/proof-receipts/proof-123":
			_, _ = w.Write([]byte(`{"schema_version":1,"id":"proof-123","candidate":{"organization_slug":"acme","project_slug":"ops","environment_slug":"prod","provider_connection_slug":"github-prod","repository_id":"123","change_request_id":"42","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","target_ref":"main","pipeline_path":"pipelines/orders.yaml","spec_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","binding_fingerprint":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","capability_fingerprint":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","compiler_version":"v1","policy_version":"v1"},"claims":[{"identity":{"family":"access_capability","pipeline_version_hash":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","semantic_path":"/source"},"description":"provider access could not be verified","required_evidence_level":"live_read_only","actual_evidence_level":null,"observed_at":null,"expires_at":null,"verdict":"inconclusive","evidence":[],"diagnostics":[{"code":"PROVIDER_UNAVAILABLE","summary":"Provider connection failed","remediation":"Retry the provider connection test","retryable":true}]}],"aggregate_verdict":"inconclusive","supersedes_receipt_id":null,"created_at":"2026-09-02T00:00:00Z","retain_until":"2026-12-01T00:00:00Z","lifecycle_links":[{"kind":"deployment","receipt_id":"proof-123","identifier":"orders:old","linked_at":"2026-09-02T00:00:00Z","retain_until":"2026-12-01T00:00:00Z","status":"version_mismatch","pipeline_slug":"orders","run_slug":null,"current_version_spec_hash":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer apiServer.Close()

	store := session.NewStore()
	router, err := NewRouter(apiServer.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/orgs/acme/projects/ops/environments/prod/proof-receipts/proof-123", nil)
	req.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, req)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	html := string(body)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	for _, text := range []string{`href="https://github.com/acme/pipelines/pull/42"`, "View PR #42", "version mismatch", "Review claims and evidence", "Evidence scope", "Not observed", `href="/orgs/acme/projects/ops/pipelines/orders/deployment?environment_slug=prod"`} {
		require.Contains(t, html, text)
	}
	require.NotContains(t, html, "Provider connection failed")
	require.NotContains(t, html, "Retry the provider connection test")
}

func TestProofHistoryForwardsBoundedScopedPagination(t *testing.T) {
	t.Parallel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/organizations/acme/projects/ops/environments/stage/proof-receipts" {
			assert.Equal(t, "20", r.URL.Query().Get("limit"))
			assert.Equal(t, "page-token", r.URL.Query().Get("cursor"))
			assert.Equal(t, "prev", r.URL.Query().Get("direction"))
			_, _ = w.Write([]byte(`{"receipts":[],"has_more":false,"next_cursor":null,"prev_cursor":null}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(backend.Close)
	router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
	require.NoError(t, err)
	app := fiber.New()
	app.Get("/orgs/:slug/projects/:projectSlug/proof-receipts", router.handleProofReceiptHistoryPage)
	response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/orgs/acme/projects/ops/proof-receipts?environment_slug=stage&cursor=page-token&direction=prev&limit=999", nil))
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "No Proof receipts")
	require.Contains(t, string(body), "stage")
}

func TestProofChangeURLUnavailableMetadataKeepsLinkAbsent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"disconnected", http.StatusNoContent, ""},
		{"denied", http.StatusForbidden, "{}"},
		{"different repository", http.StatusOK, `{"repositories":[{"id":456,"full_name":"other/repo"}]}`},
		{"invalid repository name", http.StatusOK, `{"repositories":[{"id":123,"full_name":"../repo"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/organizations/acme/github-app-connection" {
					t.Errorf("unexpected metadata request path: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer backend.Close()
			router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), session.NewStore())
			require.NoError(t, err)
			app := fiber.New()
			app.Get("/", func(c fiber.Ctx) error {
				receipt := &api.ProofReceipt{Candidate: api.ProofCandidateIdentity{OrganizationSlug: "acme", RepositoryId: "123", ChangeRequestId: "42"}}
				require.Empty(t, router.proofChangeURL(c, receipt))
				return c.SendStatus(http.StatusOK)
			})
			response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.NoError(t, response.Body.Close())
		})
	}
}

func TestProofReceiptLinkRequiresAuthenticationAndBackendAuthorization(t *testing.T) {
	t.Parallel()
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		w.Header().Set("Content-Type", "application/json")
		if handleTestAdminRolePermissions(w, r) {
			return
		}
		if r.URL.Path == "/api/organizations" {
			_, _ = w.Write([]byte(`{"organizations":[{"name":"Acme","slug":"acme","member_role":"admin"}],"has_more":false}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"FORBIDDEN","message":"Access denied"}`))
	}))
	t.Cleanup(backend.Close)
	store := session.NewStore()
	router, err := NewRouter(backend.URL, slog.New(slog.DiscardHandler), store)
	require.NoError(t, err)
	app := fiber.New()
	registerTestSessionRoute(app, store)
	router.RegisterRoutes(app, CommunityPresentation{})
	path := "/orgs/acme/projects/ops/environments/prod/proof-receipts/proof-123"
	response, err := app.Test(httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, response.StatusCode)
	require.Contains(t, response.Header.Get("Location"), "/auth/login")
	require.NoError(t, response.Body.Close())
	require.Zero(t, backendCalls)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	request.Header.Set("Cookie", getAuthenticatedSessionCookie(t, app))
	addCSRFToken(t, app, request)
	response, err = app.Test(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	require.NoError(t, response.Body.Close())
}
