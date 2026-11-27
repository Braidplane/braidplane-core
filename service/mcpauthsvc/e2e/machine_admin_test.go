package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	authstore "github.com/Braidplane/braidplane-core/postgres/authorization"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/mcpauthsvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
	transport "github.com/Braidplane/braidplane-core/transport/http"
)

type machineAdminAudit struct {
	workerproducer.EventProducer
	fail   atomic.Bool
	events chan eventssvc.CreateEventParams
}

func (a *machineAdminAudit) EnqueueRequired(ctx context.Context, _ postgres.DBTX, org slug.Slug, actor model.EventActor, event eventrecorder.Event) (workerproducer.EventRecordJob, error) {
	if a.fail.Load() {
		return workerproducer.EventRecordJob{}, xerror.Unexpected().Detail("synthetic audit failure")
	}
	params, err := eventrecorder.ParamsFor(org, actor, event)
	if err != nil {
		return workerproducer.EventRecordJob{}, err
	}
	params, err = eventssvc.PrepareCreateEventParams(ctx, params)
	if err != nil {
		return workerproducer.EventRecordJob{}, err
	}
	a.events <- params
	return workerproducer.EventRecordJob{}, nil
}

func TestMCPMachineAdministrationPinsKeyAndBrowserAuthority(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	db := e2edb.OpenIsolatedPostgresPool(t, "MCP machine administration")
	exec := func(query string) {
		_, err := db.Exec(t.Context(), query)
		require.NoError(t, err)
	}
	exec(`
INSERT INTO organizations (id,name,slug) VALUES (10,'First','first'),(20,'Second','second');
INSERT INTO roles (id,organization_id,name,slug) VALUES (13,10,'Admin','admin'),(14,10,'Reader','reader'),(23,20,'Reader','reader');
INSERT INTO users (id,slug,email,role,verified_at) VALUES
  (18,'admin','admin@example.test','super_admin',now()),(28,'reader','reader@example.test','user',now());
INSERT INTO organization_members (id,organization_id,user_id,role_id) VALUES (19,10,18,13),(29,10,28,14);
INSERT INTO permissions (resource,action) VALUES ('api_keys','update'),('api_keys','delete'),('organizations','read')
ON CONFLICT (resource,action) DO NOTHING;
INSERT INTO role_permissions (role_id,permission_id)
SELECT r.id,p.id FROM roles r CROSS JOIN permissions p WHERE
  (r.slug='admin' AND p.resource='api_keys' AND p.action IN ('update','delete')) OR
  (r.slug='reader' AND p.resource='organizations' AND p.action='read');
INSERT INTO api_keys (id,organization_id,slug,name,token_hash,token_prefix,role_id) VALUES
  (15,10,'worker','Worker','synthetic-worker','synthetic',14),
  (16,10,'other-key','Other key','synthetic-other','synthetic',14),
  (25,20,'worker','Foreign worker','synthetic-foreign','synthetic',23);
`)
	const (
		issuer   = "https://issuer.example.test/oauth"
		resource = "https://api.example.test/mcp"
		first    = "/oauth/organizations/first/api-keys/worker/machine-clients"
		second   = "/oauth/organizations/second/api-keys/worker/machine-clients"
		other    = "/oauth/organizations/first/api-keys/other-key/machine-clients"
	)
	txm := postgres.NewTransactionManager(db)
	deployment, err := mcpauthsvc.New(issuer, resource, nil)
	require.NoError(t, err)
	tokens, err := mcpauthsvc.NewTokenService(deployment, txm)
	require.NoError(t, err)
	policy, err := authorization.New(nil, func(ctx context.Context, org, user, key slug.Slug, target model.AuthorizationTarget) (model.AuthorizationFacts, error) {
		return authstore.Resolve(ctx, db, org, user, key, target)
	})
	require.NoError(t, err)
	store := session.NewStore(session.Config{Extractor: httpsecurity.SessionCookieExtractor(true), CookieSecure: true, CookiePath: "/"})
	audit := &machineAdminAudit{events: make(chan eventssvc.CreateEventParams, 8)}
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Post("/synthetic-login/:user/:method", func(c fiber.Ctx) error {
		return reqctx.ResetAndSetAuthenticatedUser(c, store, slug.Slug(c.Params("user")), 1, c.Params("method") == "password")
	})
	app.Get("/synthetic-csrf", func(c fiber.Ctx) error {
		csrf, err := httpsecurity.EnsureCSRFToken(c, store)
		if err != nil {
			return err
		}
		return c.SendString(csrf)
	})
	cfg := transport.MCPMachineAdministrationConfig{
		Tokens: tokens, Audit: audit, Sessions: store, Policy: policy, Admission: admissionsvc.New(txm, organizations.NewRepository()),
		AbuseLimiter: httpsecurity.NewMemoryAbuseLimiter(), RequestLimit: 100, RequestWindow: time.Minute,
	}
	require.NoError(t, transport.RegisterMCPMachineAdministrationRoutes(app, cfg))
	require.Error(t, transport.RegisterMCPMachineAdministrationRoutes(app, cfg))
	type browser struct {
		cookie *http.Cookie
		csrf   string
	}
	type response struct {
		headers http.Header
		cookies []*http.Cookie
		body    string
	}
	request := func(method, path string, browser browser, bearer, body string, status int) response {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		if browser.cookie != nil {
			req.AddCookie(browser.cookie)
		}
		if browser.csrf != "" {
			req.Header.Set(httpsecurity.CSRFHeader, browser.csrf)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, status, resp.StatusCode, "%s %s: %s", method, path, data)
		return response{headers: resp.Header, cookies: resp.Cookies(), body: string(data)}
	}
	login := func(user, method string) browser {
		t.Helper()
		cookies := request(http.MethodPost, "/synthetic-login/"+user+"/"+method, browser{}, "", "", http.StatusOK).cookies
		require.Len(t, cookies, 1)
		session := browser{cookie: cookies[0]}
		session.csrf = request(http.MethodGet, "/synthetic-csrf", session, "", "", http.StatusOK).body
		return session
	}
	admin := login("admin", "password")
	request(http.MethodPost, first, browser{}, "", "", http.StatusForbidden)
	request(http.MethodPost, first, browser{cookie: admin.cookie, csrf: "wrong"}, "", "", http.StatusForbidden)
	request(http.MethodPost, first, login("reader", "password"), "", "", http.StatusForbidden)
	request(http.MethodPost, first, login("admin", "oidc"), "", "", http.StatusForbidden)
	request(http.MethodPost, second, admin, "", "", http.StatusForbidden)
	request(http.MethodPost, first, admin, "synthetic-bearer", "", http.StatusForbidden)
	request(http.MethodPost, first, admin, "", `{"role":"admin"}`, http.StatusBadRequest)
	audit.fail.Store(true)
	request(http.MethodPost, first, admin, "", "", http.StatusInternalServerError)
	var records int
	require.NoError(t, db.QueryRow(t.Context(), `SELECT count(*) FROM mcp_oauth_machine_clients`).Scan(&records))
	require.Zero(t, records)
	audit.fail.Store(false)
	registered := request(http.MethodPost, first, admin, "", "", http.StatusCreated)
	require.Equal(t, "no-store", registered.headers.Get("Cache-Control"))
	var registration struct {
		ClientID     string    `json:"client_id"`
		ClientSecret string    `json:"client_secret"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	require.NoError(t, json.Unmarshal([]byte(registered.body), &registration))
	require.NotEmpty(t, registration.ClientID)
	require.NotEmpty(t, registration.ClientSecret)
	require.True(t, registration.ExpiresAt.After(time.Now()))
	var keyID int64
	var secretHash string
	require.NoError(t, db.QueryRow(t.Context(), `SELECT api_key_id,client_secret_hash FROM mcp_oauth_machine_clients WHERE client_id=$1`,
		registration.ClientID).Scan(&keyID, &secretHash))
	require.EqualValues(t, 15, keyID)
	require.Equal(t, oauthflow.HashState(registration.ClientSecret), secretHash)
	created := <-audit.events
	require.Equal(t, model.EventClassSecurityAudit, created.EventClass)
	require.Equal(t, model.EventActionCreate, created.Action)
	require.Equal(t, map[string]any{"mcp_client_id": registration.ClientID}, created.Metadata)

	credentials := mcpauthsvc.MachineClientCredentials{ClientID: registration.ClientID, ClientSecret: registration.ClientSecret}
	pair, err := tokens.IssueMachineTokens(t.Context(), credentials, resource)
	require.NoError(t, err)
	ctx, principal, err := tokens.AuthenticateDelegatedRequest(t.Context(), pair.AccessToken, resource, "first")
	require.NoError(t, err)
	require.Equal(t, slug.Slug("worker"), principal.APIKey)
	require.Empty(t, principal.User)
	org := slug.Slug("first")
	allowed, err := policy.Can(ctx, authorization.Input{
		Source: principal.Source(), APIKeySlug: principal.APIKey, OrgSlug: &org,
		Operation: "getOrganization", Resource: model.ResourceOrganizations, Action: model.Read,
	})
	require.NoError(t, err)
	require.True(t, allowed)
	request(http.MethodPost, first, admin, pair.AccessToken, "", http.StatusForbidden)
	request(http.MethodDelete, first+"/"+registration.ClientID, browser{}, pair.AccessToken, "", http.StatusForbidden)
	request(http.MethodDelete, second+"/"+registration.ClientID, admin, "", "", http.StatusForbidden)
	request(http.MethodDelete, other+"/"+registration.ClientID, admin, "", "", http.StatusNotFound)
	audit.fail.Store(true)
	request(http.MethodDelete, first+"/"+registration.ClientID, admin, "", "", http.StatusInternalServerError)
	_, err = tokens.AuthenticateMachineAccessToken(t.Context(), pair.AccessToken, resource, org)
	require.NoError(t, err)
	audit.fail.Store(false)
	request(http.MethodDelete, first+"/"+registration.ClientID, admin, "", "", http.StatusNoContent)
	request(http.MethodDelete, first+"/"+registration.ClientID, admin, "", "", http.StatusNoContent)
	for range 2 {
		revoked := <-audit.events
		require.Equal(t, model.EventActionRevoke, revoked.Action)
		require.Equal(t, created.Metadata, revoked.Metadata)
	}
	_, err = tokens.AuthenticateMachineAccessToken(t.Context(), pair.AccessToken, resource, org)
	require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)
	_, err = tokens.IssueMachineTokens(t.Context(), credentials, resource)
	require.ErrorIs(t, err, mcpauthsvc.ErrInvalidClient)

	exec(`UPDATE users SET authentication_version=2 WHERE slug='admin'`)
	request(http.MethodPost, first, admin, "", "", http.StatusForbidden)
	require.Empty(t, audit.events)
}
