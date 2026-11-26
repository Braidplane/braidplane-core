package http

import (
	"context"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

func TestRoleGrantAdministration(t *testing.T) {
	const path = "/organizations/acme/authorization/grants"
	body := api.NewRoleGrant{
		SubjectKind: "member", SubjectSlug: "alice", RoleSlug: "runner", Scope: "resource",
		TargetKind: new("pipelines"), TargetSlug: new("batch"), ProjectSlug: new("sales"),
	}
	stored := model.RoleGrant{Slug: "grant-a", NewRoleGrant: model.NewRoleGrant{
		SubjectKind: "member", SubjectSlug: "alice", RoleSlug: "runner", Scope: "resource",
		Target: model.AuthorizationTarget{Kind: "pipelines", Slug: "batch", ProjectSlug: "sales"},
	}}
	roles := &fakes.RolesService{
		CreateRoleGrantFn: func(_ context.Context, org slug.Slug, actor model.EventActor, grant model.NewRoleGrant) (*model.RoleGrant, error) {
			require.Equal(t, slug.Slug("acme"), org)
			assertUserEventActor(t, actor, "alice")
			require.Equal(t, stored.NewRoleGrant, grant)
			return &stored, nil
		},
		ListRoleGrantsFn: func(_ context.Context, org slug.Slug) ([]model.RoleGrant, error) {
			require.Equal(t, slug.Slug("acme"), org)
			return []model.RoleGrant{stored}, nil
		},
		RevokeRoleGrantFn: func(_ context.Context, org, grant slug.Slug, actor model.EventActor) error {
			require.Equal(t, slug.Slug("acme"), org)
			require.Equal(t, stored.Slug, grant)
			assertUserEventActor(t, actor, "alice")
			return nil
		},
	}
	binding := model.AuthorizationBinding{Scope: "organization", ScopeID: "1", Permissions: []string{"roles:read", "roles:update"}}
	auth, err := authorization.New(nil, func(_ context.Context, org, user, key slug.Slug, target model.AuthorizationTarget) (model.AuthorizationFacts, error) {
		require.Equal(t, slug.Slug("acme"), org)
		require.Zero(t, target)
		return model.AuthorizationFacts{Valid: true, OrganizationID: "1", Bindings: []model.AuthorizationBinding{binding}}, nil
	})
	require.NoError(t, err)
	app, _ := newTestApp(t, testServerConfig{rolesService: roles, middleware: []fiber.Handler{patPrincipalMiddleware(false)}, authService: fakeAuthService{CanFn: auth.Can}})
	created, err := app.Test(jsonRequest(t, http.MethodPost, path, body))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, created.StatusCode)
	require.Equal(t, roleGrantToHTTP(stored), parseResponse[api.RoleGrant](t, created))
	require.NoError(t, created.Body.Close())
	listed, err := app.Test(jsonRequest(t, http.MethodGet, path, nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, listed.StatusCode)
	require.Equal(t, []api.RoleGrant{roleGrantToHTTP(stored)}, parseResponse[api.RoleGrantList](t, listed).Grants)
	require.NoError(t, listed.Body.Close())
	revoked, err := app.Test(jsonRequest(t, http.MethodDelete, path+"/grant-a", nil))
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, revoked.StatusCode)
	require.NoError(t, revoked.Body.Close())

	keyApp, _ := newTestApp(t, testServerConfig{
		rolesService: roles, authService: fakeAuthService{CanFn: auth.Can},
		middleware: []fiber.Handler{apiKeyPrincipalMiddleware(reqctx.APIKeyPrincipal{OrgSlug: "acme", Slug: "machine", Role: "admin", CanManageAPIKeys: true})},
	})
	for _, tc := range []struct {
		method, url string
		body        any
	}{{http.MethodPost, path, body}, {http.MethodDelete, path + "/grant-a", nil}} {
		resp, err := keyApp.Test(jsonRequest(t, tc.method, tc.url, tc.body))
		require.NoError(t, err)
		require.Equal(t, http.StatusForbidden, resp.StatusCode)
		require.NoError(t, resp.Body.Close())
	}
	binding.Scope, binding.ScopeID = "project", "12"
	denied, err := app.Test(jsonRequest(t, http.MethodPost, path, body))
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, denied.StatusCode)
	require.NoError(t, denied.Body.Close())
}

func TestRoleGrantSelectors(t *testing.T) {
	for _, tc := range []struct {
		name, scope, kind, target, project string
		want                               model.AuthorizationTarget
	}{
		{"organization", "organization", "", "", "", model.AuthorizationTarget{Kind: "organizations", Slug: "acme"}},
		{"project", "project", "", "sales", "", model.AuthorizationTarget{Kind: "projects", Slug: "sales"}},
		{"environment", "environment", "", "prod", "", model.AuthorizationTarget{Kind: "environments", Slug: "prod"}},
		{"runtime key", "resource", "runtime_variable_definitions", "TABLE_NAME", "", model.AuthorizationTarget{Kind: "runtime_variable_definitions", Slug: "TABLE_NAME"}},
		{"qualified broad scope", "organization", "", "", "sales", model.AuthorizationTarget{}},
		{"foreign organization", "organization", "", "other", "", model.AuthorizationTarget{}},
		{"contradictory kind", "project", "pipelines", "sales", "", model.AuthorizationTarget{}},
		{"missing kind", "resource", "", "sales", "", model.AuthorizationTarget{}},
		{"missing target", "environment", "", "", "", model.AuthorizationTarget{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRoleGrant("acme", api.NewRoleGrant{
				SubjectKind: "member", SubjectSlug: "alice", RoleSlug: "runner",
				Scope: api.NewRoleGrantScope(tc.scope), TargetKind: optionalString(tc.kind), TargetSlug: optionalString(tc.target), ProjectSlug: optionalString(tc.project),
			})
			if tc.want.Kind == "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got.Target)
		})
	}
}
