package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	authstore "github.com/Braidplane/braidplane-core/postgres/authorization"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/mcpauthsvc"
	"github.com/Braidplane/braidplane-core/service/orgsvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
	transport "github.com/Braidplane/braidplane-core/transport/http"
)

type projectAudit struct {
	workerproducer.EventProducer
	actors []model.EventActor
}

func (a *projectAudit) EnqueueRequired(_ context.Context, _ postgres.DBTX, _ slug.Slug, actor model.EventActor, _ eventrecorder.Event) (workerproducer.EventRecordJob, error) {
	a.actors = append(a.actors, actor)
	return workerproducer.EventRecordJob{}, nil
}

type observedAdmission struct {
	admissionsvc.Service
	afterWriteCheck func(context.Context) error
}

func (a *observedAdmission) CheckWriteAccess(ctx context.Context, org slug.Slug) error {
	if err := a.Service.CheckWriteAccess(ctx, org); err != nil {
		return err
	}
	if a.afterWriteCheck != nil {
		return a.afterWriteCheck(ctx)
	}
	return nil
}

func TestDelegatedCoreOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	db := e2edb.OpenIsolatedPostgresPool(t, "delegated Core operations")
	_, err := db.Exec(t.Context(), `
INSERT INTO organizations (id,name,slug) VALUES (10,'First','first'),(20,'Second','second');
INSERT INTO projects (id,organization_id,name,slug)
VALUES (11,10,'Visible first','visible'),(12,10,'Hidden first','hidden'),(21,20,'Visible second','visible');
INSERT INTO roles (id,organization_id,name,slug)
VALUES (13,10,'Visitor','visitor'),(14,10,'Project editor','editor'),(23,20,'Project editor','editor');
INSERT INTO permissions (resource,action)
VALUES ('organizations','read'),('projects','read'),('projects','update'),('projects','delete'),('api_keys','create')
ON CONFLICT (resource,action) DO NOTHING;
INSERT INTO role_permissions (role_id,permission_id)
SELECT r.id,p.id FROM roles r CROSS JOIN permissions p
WHERE (r.slug='visitor' AND (p.resource,p.action) IN (('organizations','read'),('api_keys','create'))) OR
      (r.slug='editor' AND p.resource='projects' AND p.action IN ('read','update','delete'));
INSERT INTO users (id,slug,email,role,verified_at) VALUES (18,'member','member@example.test','super_admin',now());
INSERT INTO organization_members (id,organization_id,user_id,role_id) VALUES (19,10,18,13),(29,20,18,23);
INSERT INTO api_keys (id,organization_id,slug,name,token_hash,token_prefix,role_id,can_manage_api_keys)
VALUES (15,10,'worker','Worker','synthetic-first','synthetic',13,true),(25,20,'worker','Worker','synthetic-second','synthetic',23,true);
INSERT INTO authorization_scoped_grants (organization_id,member_id,api_key_id,role_id,scope,target_kind,target_id)
VALUES (10,19,NULL,14,'resource','projects',11),(10,NULL,15,14,'resource','projects',11);
`)
	require.NoError(t, err)
	const (
		issuer      = "https://issuer.example.test/oauth"
		resource    = "https://api.example.test/mcp"
		clientID    = "console"
		redirect    = "https://client.example.test/callback"
		verifier    = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		first       = "/organizations/first"
		second      = "/organizations/second"
		projectPath = "/projects/visible"
	)
	txm := postgres.NewTransactionManager(db)
	deployment, err := mcpauthsvc.New(issuer, resource, []mcpauthsvc.Client{{ID: clientID, Name: "Console", RedirectURIs: []string{redirect}}})
	require.NoError(t, err)
	tokens, err := mcpauthsvc.NewTokenService(deployment, txm)
	require.NoError(t, err)
	issueUserToken := func(t *testing.T, org slug.Slug, decision mcpauthsvc.ConsentDecision) mcpauthsvc.TokenPair {
		t.Helper()
		browser := mcpauthsvc.BrowserIdentity{User: "member", Organization: org, SessionID: "synthetic-session", AuthenticationVersion: 1}
		prompt, err := tokens.BeginUserConsent(t.Context(), mcpauthsvc.AuthorizationRequest{
			ClientID: clientID, RedirectURI: redirect, Resource: resource, ResponseType: "code",
			Challenge: oauthflow.S256Challenge(verifier), ChallengeMethod: oauthflow.PKCES256,
		}, "", browser)
		require.NoError(t, err)
		if org == "" {
			require.Empty(t, prompt.ReadOperations)
			require.Empty(t, prompt.ChangeOperations)
		}
		callback, err := tokens.CompleteUserConsent(t.Context(), prompt.ID, browser, decision)
		require.NoError(t, err)
		uri, err := url.Parse(callback)
		require.NoError(t, err)
		pair, err := tokens.RedeemUserCode(t.Context(), mcpauthsvc.CodeRedemption{
			Code: uri.Query().Get("code"), ClientID: clientID, RedirectURI: redirect, Resource: resource, Verifier: verifier,
		})
		require.NoError(t, err)
		return pair
	}
	userToken, accountToken := issueUserToken(t, "first", mcpauthsvc.ConsentReadWrite).AccessToken, issueUserToken(t, "", mcpauthsvc.ConsentReadOnly).AccessToken
	machine, err := tokens.RegisterMachineClient(t.Context(), "first", "worker")
	require.NoError(t, err)
	machineTokens, err := tokens.IssueMachineTokens(t.Context(), machine.MachineClientCredentials, resource)
	require.NoError(t, err)
	machineToken := machineTokens.AccessToken
	policy, err := authorization.New(nil, func(ctx context.Context, org, user, key slug.Slug, target model.AuthorizationTarget) (model.AuthorizationFacts, error) {
		if target.Slug == "policy-unavailable" {
			return model.AuthorizationFacts{}, xerror.Unexpected().Detail("synthetic policy facts error")
		}
		return authstore.Resolve(ctx, db, org, user, key, target)
	})
	require.NoError(t, err)
	orgRepo, projectRepo := organizations.NewRepository(), projects.NewRepository()
	admission := &observedAdmission{Service: admissionsvc.New(txm, orgRepo)}
	audit := &projectAudit{}
	orgService := orgsvc.New(txm, orgRepo, nil, nil, nil, nil, admission)
	projectService := projectssvc.New(txm, projectRepo, nil, nil, nil, nil, nil, nil, audit)
	server := transport.NewServer(nil, nil, nil, nil, policy, orgService, admission, projectService,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		"", "", false, false, nil)
	authenticate, err := transport.NewMCPDelegatedOperationAuth(tokens)
	require.NoError(t, err)
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	api.RegisterHandlersWithOptions(app, server, api.FiberServerOptions{
		HandlerMiddlewares: []api.HandlerMiddlewareFunc{transport.RequireMappedOperation, authenticate},
	})
	request := func(t *testing.T, method, path, token, body string, wantStatus int) string {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()
		data, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, wantStatus, resp.StatusCode, "%s %s: %s", method, path, data)
		return string(data)
	}
	for _, subject := range []struct{ label, token string }{{"user", userToken}, {"machine", machineToken}} {
		t.Run(subject.label+" scoped permissions", func(t *testing.T) {
			require.Contains(t, request(t, http.MethodGet, first, subject.token, "", http.StatusOK), `"slug":"first"`)
			require.Contains(t, request(t, http.MethodGet, first+projectPath, subject.token, "", http.StatusOK), "Visible first")
			request(t, http.MethodGet, first+"/projects/hidden", subject.token, "", http.StatusForbidden)
			request(t, http.MethodGet, first+"/projects", subject.token, "", http.StatusForbidden)
			request(t, http.MethodGet, second+projectPath, subject.token, "", http.StatusUnauthorized)
			request(t, http.MethodDelete, first+projectPath, subject.token, "", http.StatusForbidden)
			request(t, http.MethodPost, first+"/api-keys", subject.token, `{}`, http.StatusForbidden)
			request(t, http.MethodGet, "/organizations", subject.token, "", http.StatusForbidden)
			request(t, http.MethodGet, first+"/projects/policy-unavailable", subject.token, "", http.StatusInternalServerError)
		})
	}
	request(t, http.MethodGet, first+projectPath, accountToken, "", http.StatusUnauthorized)
	request(t, http.MethodGet, first+projectPath, "", "", http.StatusUnauthorized)
	request(t, http.MethodGet, first+projectPath, machineTokens.RefreshToken, "", http.StatusUnauthorized)
	t.Run("read-only consent cannot rename before or after refresh", func(t *testing.T) {
		grant := issueUserToken(t, "first", mcpauthsvc.ConsentReadOnly)
		rotated, err := tokens.RotateUserRefreshToken(t.Context(), grant.RefreshToken, clientID, resource)
		require.NoError(t, err)
		for _, token := range []string{grant.AccessToken, rotated.AccessToken} {
			request(t, http.MethodGet, first+projectPath, token, "", http.StatusOK)
			request(t, http.MethodPut, first+projectPath, token, `{"name":"Unapproved rename"}`, http.StatusForbidden)
		}
	})
	t.Run("existing user grants retain their scope through refresh", func(t *testing.T) {
		olderGrant := issueUserToken(t, "first", mcpauthsvc.ConsentReadWrite)
		changed, err := db.Exec(t.Context(), `UPDATE mcp_oauth_grants g SET operation_scope_keys =
ARRAY(SELECT k FROM unnest(g.operation_scope_keys) k WHERE k LIKE 'getProject:%') || ARRAY['updateProject:old-mapping']
FROM mcp_oauth_credentials c WHERE g.grant_id=c.grant_id AND c.credential_hash=$1`, oauthflow.HashState(olderGrant.AccessToken))
		require.NoError(t, err)
		require.EqualValues(t, 1, changed.RowsAffected())
		rotated, err := tokens.RotateUserRefreshToken(t.Context(), olderGrant.RefreshToken, clientID, resource)
		require.NoError(t, err)
		for _, token := range []string{olderGrant.AccessToken, rotated.AccessToken} {
			request(t, http.MethodGet, first+projectPath, token, "", http.StatusOK)
			request(t, http.MethodGet, first, token, "", http.StatusForbidden)
			request(t, http.MethodPut, first+projectPath, token, `{"name":"Unapproved rename"}`, http.StatusForbidden)
		}
	})
	t.Run("new and refreshed machine tokens inherit the registered scope", func(t *testing.T) {
		olderRegistration, err := tokens.RegisterMachineClient(t.Context(), "first", "worker")
		require.NoError(t, err)
		changed, err := db.Exec(t.Context(), `UPDATE mcp_oauth_machine_clients SET operation_scope_keys =
ARRAY(SELECT k FROM unnest(operation_scope_keys) k WHERE k LIKE 'getProject:%') WHERE client_id=$1`, olderRegistration.ClientID)
		require.NoError(t, err)
		require.EqualValues(t, 1, changed.RowsAffected())
		firstPair, err := tokens.IssueMachineTokens(t.Context(), olderRegistration.MachineClientCredentials, resource)
		require.NoError(t, err)
		rotated, err := tokens.RotateMachineRefreshToken(t.Context(), olderRegistration.MachineClientCredentials, firstPair.RefreshToken, resource)
		require.NoError(t, err)
		newPair, err := tokens.IssueMachineTokens(t.Context(), olderRegistration.MachineClientCredentials, resource)
		require.NoError(t, err)
		for _, token := range []string{firstPair.AccessToken, rotated.AccessToken, newPair.AccessToken} {
			request(t, http.MethodGet, first+projectPath, token, "", http.StatusOK)
			request(t, http.MethodPut, first+projectPath, token, `{"name":"Unapproved rename"}`, http.StatusForbidden)
		}
	})
	_, err = db.Exec(t.Context(), `INSERT INTO role_permissions (role_id,permission_id) SELECT 13,id FROM permissions WHERE resource='projects' AND action='read'`)
	require.NoError(t, err)
	for _, token := range []string{userToken, machineToken} {
		list := request(t, http.MethodGet, first+"/projects", token, "", http.StatusOK)
		require.Contains(t, list, "Visible first")
		require.Contains(t, list, "Hidden first")
		require.NotContains(t, list, "Visible second")
	}
	_, err = db.Exec(t.Context(), `DELETE FROM role_permissions rp USING permissions p WHERE rp.role_id=13 AND rp.permission_id=p.id AND p.resource='projects' AND p.action='read'`)
	require.NoError(t, err)
	require.Contains(t, request(t, http.MethodPut, first+projectPath, userToken, `{"name":"Renamed by user"}`, http.StatusOK), "Renamed by user")
	require.Contains(t, request(t, http.MethodPut, first+projectPath, machineToken, `{"name":"Renamed by machine"}`, http.StatusOK), "Renamed by machine")
	require.Equal(t, []model.EventActor{model.UserEventActor("member"), model.APIKeyEventActor("worker")}, audit.actors)

	_, err = db.Exec(t.Context(), `DELETE FROM authorization_scoped_grants WHERE organization_id=10 AND api_key_id=15`)
	require.NoError(t, err)
	request(t, http.MethodGet, first+projectPath, machineToken, "", http.StatusForbidden)

	admission.afterWriteCheck = func(ctx context.Context) error {
		return tokens.RevokeUserToken(postgres.WithoutTransactionChecks(ctx), userToken, clientID, resource)
	}
	require.Contains(t, request(t, http.MethodPut, first+projectPath, userToken, `{"name":"Must remain unchanged"}`, http.StatusForbidden), "MCP_DELEGATION_CHANGED")
	request(t, http.MethodGet, first+projectPath, userToken, "", http.StatusUnauthorized)
	changedScope := issueUserToken(t, "first", mcpauthsvc.ConsentReadWrite)
	admission.afterWriteCheck = func(ctx context.Context) error {
		_, err := db.Exec(ctx, `UPDATE mcp_oauth_grants g SET operation_scope_keys='{}'
FROM mcp_oauth_credentials c WHERE g.grant_id=c.grant_id AND c.credential_hash=$1`, oauthflow.HashState(changedScope.AccessToken))
		return err
	}
	require.Contains(t, request(t, http.MethodPut, first+projectPath, changedScope.AccessToken, `{"name":"Must remain unchanged"}`, http.StatusForbidden), "MCP_DELEGATION_CHANGED")
	var projectName string
	require.NoError(t, db.QueryRow(t.Context(), `SELECT name FROM projects WHERE id=11`).Scan(&projectName))
	require.Equal(t, "Renamed by machine", projectName)
	require.Len(t, audit.actors, 2)
}
