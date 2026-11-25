package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	authstore "github.com/Braidplane/braidplane-core/postgres/authorization"
	"github.com/Braidplane/braidplane-core/service/authorization"
	transport "github.com/Braidplane/braidplane-core/transport/http"
)

type afterAllow struct {
	authorization.Service
	next func()
}

func (a *afterAllow) Can(ctx context.Context, in authorization.Input) (bool, error) {
	ok, err := a.Service.Can(ctx, in)
	if ok && a.next != nil {
		a.next()
		a.next = nil
	}
	return ok, err
}

// Check the same object through generated REST routing and the trusted Can boundary after each grant change.
func TestScopedRoleReadThroughRESTAndCan(t *testing.T) {
	ctx := t.Context()
	pool := e2edb.OpenIsolatedPostgresPool(t, "scoped role read")
	exec := func(sql string) { t.Helper(); _, err := pool.Exec(ctx, sql); require.NoError(t, err) }
	exec(`
INSERT INTO organizations (id,name,slug) VALUES (101,'Tenant','tenant'),(102,'Other','other');
INSERT INTO roles (id,organization_id,name,slug) VALUES
 (201,101,'Empty','empty'),(202,101,'Reader','reader'),(203,101,'Target','target'),(204,102,'Target','target');
INSERT INTO permissions (id,resource,action) VALUES (301,'roles','read');
INSERT INTO role_permissions(role_id,permission_id) VALUES (202,301);
INSERT INTO users(id,slug,email,role,verified_at) VALUES(401,'member','member@example.test','user',now());
INSERT INTO organization_members(id,organization_id,user_id,role_id) VALUES(501,101,401,201);
INSERT INTO api_keys(id,organization_id,name,slug,token_hash,token_prefix,role_id) VALUES
 (601,101,'Machine','machine','synthetic-hash','synthetic',201);
INSERT INTO authorization_scoped_grants(organization_id,member_id,api_key_id,role_id,scope,target_kind,target_id) VALUES
 (101,501,NULL,202,'resource','roles',203),(101,NULL,601,202,'resource','roles',203);`)
	auth, err := authorization.New(nil, func(ctx context.Context, org, user, key slug.Slug, target model.AuthorizationTarget) (model.AuthorizationFacts, error) {
		return authstore.Resolve(ctx, pool, org, user, key, target)
	})
	require.NoError(t, err)
	hook := &afterAllow{Service: auth}
	server := transport.NewServer(nil, nil, nil, session.NewStore(), hook, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		newRoleArchivalService(pool), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, "", "", false, false, nil)
	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	machine := false
	app.Use(func(c fiber.Ctx) error {
		if machine {
			reqctx.SetAPIKeyPrincipal(c, reqctx.APIKeyPrincipal{Slug: "machine", OrgSlug: "tenant", Role: "reader"})
		} else {
			reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{Slug: "member", Source: reqctx.UserPrincipalSourcePersonalAccessToken})
		}
		return c.Next()
	})
	api.RegisterHandlersWithOptions(app.Group("/api"), server, api.FiberServerOptions{HandlerMiddlewares: []api.HandlerMiddlewareFunc{transport.RequireMappedOperation}})
	check := func(org, role slug.Slug, want bool) {
		t.Helper()
		input := authorization.Input{Operation: "getRole", OrgSlug: &org, Resource: model.ResourceRoles, Action: model.Read, Target: model.AuthorizationTarget{Kind: "roles", Slug: role}}
		if machine {
			input.Source = authorization.SourceAPIKey
			input.APIKeySlug = "machine"
		} else {
			input.Source = authorization.SourcePAT
			input.UserSlug = "member"
		}
		allowed, err := auth.Can(ctx, input)
		require.NoError(t, err)
		require.Equal(t, want, allowed)
		resp, err := app.Test(httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/organizations/"+org.String()+"/roles/"+role.String(), nil))
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		if want {
			require.Equal(t, http.StatusOK, resp.StatusCode)
		} else {
			require.Equal(t, http.StatusForbidden, resp.StatusCode)
		}
	}
	for _, key := range []bool{false, true} {
		machine = key
		check("tenant", "target", true)
		check("tenant", "reader", false)
		check("other", "target", false)
	}
	exec("DELETE FROM authorization_scoped_grants WHERE api_key_id=601")
	check("tenant", "target", false)
	machine = false
	check("tenant", "target", true)
	// Replace the authorized role after Can returns but before the handler reads it.
	hook.next = func() {
		exec("DELETE FROM roles WHERE id=203; INSERT INTO roles(id,organization_id,name,slug) VALUES(205,101,'Replacement','target')")
	}
	resp, err := app.Test(httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/organizations/tenant/roles/target", nil))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	check("tenant", "target", false)
	exec("UPDATE authorization_scoped_grants SET target_id=205 WHERE member_id=501")
	check("tenant", "target", true)
	exec("DELETE FROM role_permissions WHERE role_id=202")
	check("tenant", "target", false)
}
