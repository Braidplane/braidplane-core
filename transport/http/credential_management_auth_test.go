package http

import (
	"context"
	"net/http"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/patssvc"
)

func TestAPIKeyManagerListsOnlyItsRole(t *testing.T) {
	t.Parallel()

	managerRole := slug.Slug("viewer")
	app, _ := newTestApp(t, testServerConfig{
		middleware: []fiber.Handler{apiKeyPrincipalMiddleware(reqctx.APIKeyPrincipal{
			Slug:             "automation-key",
			OrgSlug:          "acme",
			Role:             managerRole,
			CanManageAPIKeys: true,
		})},
		apiKeysService: &fakeAPIKeysService{
			ListAPIKeysFn: func(
				_ context.Context,
				orgSlug slug.Slug,
				roleFilter *slug.Slug,
				_ model.ListRequest,
			) (model.ListResponse[model.APIKey], error) {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				require.NotNil(t, roleFilter)
				require.Equal(t, managerRole, *roleFilter)
				return model.ListResponse[model.APIKey]{}, nil
			},
		},
		authService: allowAuthorizationService(),
	})

	req := jsonRequest(t, http.MethodGet, "/organizations/acme/api-keys", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAPIKeyManagerCreatesNonDelegatingKeyForSameRole(t *testing.T) {
	t.Parallel()

	app, _ := newTestApp(t, testServerConfig{
		middleware: []fiber.Handler{apiKeyPrincipalMiddleware(reqctx.APIKeyPrincipal{
			Slug:             "automation-key",
			OrgSlug:          "acme",
			Role:             "viewer",
			CanManageAPIKeys: true,
		})},
		apiKeysService: &fakeAPIKeysService{
			CreateAPIKeyFn: func(
				_ context.Context,
				_ slug.Slug,
				actor model.EventActor,
				newAPIKey model.NewAPIKey,
			) (*model.APIKeyWithToken, error) {
				require.Equal(t, model.EventActorTypeAPIKey, actor.Type)
				require.NotNil(t, actor.Slug)
				require.Equal(t, slug.Slug("automation-key"), *actor.Slug)
				require.Equal(t, slug.Slug("viewer"), newAPIKey.Role)
				require.False(t, newAPIKey.CanManageAPIKeys)
				return testAPIKeyWithToken("child-key"), nil
			},
		},
		authService: allowAuthorizationService(),
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/api-keys", map[string]any{
		"name": "Child key",
		"slug": "child-key",
		"role": "viewer",
	})
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestAPIKeyManagerCannotDelegateCapabilityOrCrossRoles(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{
			name: "capability field present even when false",
			body: map[string]any{"name": "Child", "role": "viewer", "can_manage_api_keys": false},
		},
		{
			name: "different role",
			body: map[string]any{"name": "Child", "role": "admin"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			app, _ := newTestApp(t, testServerConfig{
				middleware: []fiber.Handler{apiKeyPrincipalMiddleware(reqctx.APIKeyPrincipal{
					Slug:             "automation-key",
					OrgSlug:          "acme",
					Role:             "viewer",
					CanManageAPIKeys: true,
				})},
				apiKeysService: &fakeAPIKeysService{},
				authService:    allowAuthorizationService(),
			})

			req := jsonRequest(t, http.MethodPost, "/organizations/acme/api-keys", tc.body)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusForbidden, resp.StatusCode)
			assertErrorResponse(t, resp, apierror.Forbidden(model.ResourceAPIKeys.String(), model.Create.String()))
		})
	}
}

func TestAPIKeyManagerCannotReadDifferentRoleOrPermanentlyDelete(t *testing.T) {
	t.Parallel()

	principal := reqctx.APIKeyPrincipal{
		Slug:             "automation-key",
		OrgSlug:          "acme",
		Role:             "viewer",
		CanManageAPIKeys: true,
	}
	app, _ := newTestApp(t, testServerConfig{
		middleware: []fiber.Handler{apiKeyPrincipalMiddleware(principal)},
		apiKeysService: &fakeAPIKeysService{
			GetAPIKeyFn: func(context.Context, slug.Slug, slug.Slug) (*model.APIKey, error) {
				key := testAPIKey("admin-key")
				key.Role = "admin"
				return &key, nil
			},
		},
		authService: allowAuthorizationService(),
	})

	getReq := jsonRequest(t, http.MethodGet, "/organizations/acme/api-keys/admin-key", nil)
	getResp, err := app.Test(getReq)
	require.NoError(t, err)
	defer func() { _ = getResp.Body.Close() }()
	require.Equal(t, http.StatusForbidden, getResp.StatusCode)

	deleteReq := jsonRequest(t, http.MethodPost, "/organizations/acme/api-keys/admin-key/delete", nil)
	deleteResp, err := app.Test(deleteReq)
	require.NoError(t, err)
	defer func() { _ = deleteResp.Body.Close() }()
	require.Equal(t, http.StatusUnauthorized, deleteResp.StatusCode)
	assertErrorResponse(t, deleteResp, apierror.Unauthorized())
}

func TestPATManagerUpdatesMetadataButCannotToggleCapability(t *testing.T) {
	t.Parallel()

	app, _ := newTestApp(t, testServerConfig{
		middleware: []fiber.Handler{patPrincipalMiddleware(true)},
		patsService: &fakePATsService{
			UpdatePersonalAccessTokenFn: func(
				_ context.Context,
				userSlug, patSlug slug.Slug,
				update model.UpdatePersonalAccessToken,
			) (*model.PersonalAccessToken, error) {
				require.Equal(t, slug.Slug("alice"), userSlug)
				require.Equal(t, slug.Slug("developer-cli"), patSlug)
				require.Nil(t, update.CanManagePersonalAccessTokens)
				return &model.PersonalAccessToken{Slug: patSlug, Name: update.Name, UserSlug: userSlug}, nil
			},
		},
		authService: allowAuthorizationService(),
	})

	allowedReq := jsonRequest(t, http.MethodPut, "/users/profile/personal-access-tokens/developer-cli", map[string]any{
		"name": "Renamed",
	})
	allowedResp, err := app.Test(allowedReq)
	require.NoError(t, err)
	defer func() { _ = allowedResp.Body.Close() }()
	require.Equal(t, http.StatusOK, allowedResp.StatusCode)

	forbiddenReq := jsonRequest(t, http.MethodPut, "/users/profile/personal-access-tokens/developer-cli", map[string]any{
		"name":                              "Renamed",
		"can_manage_personal_access_tokens": false,
	})
	forbiddenResp, err := app.Test(forbiddenReq)
	require.NoError(t, err)
	defer func() { _ = forbiddenResp.Body.Close() }()
	require.Equal(t, http.StatusForbidden, forbiddenResp.StatusCode)
}

func TestPersonalAccessTokenUpdateFailsClosedWhenPrincipalLookupDisappears(t *testing.T) {
	t.Parallel()

	var requestCtx fiber.Ctx
	updateCalled := false
	app, _ := newTestApp(t, testServerConfig{
		middleware: []fiber.Handler{func(c fiber.Ctx) error {
			requestCtx = c
			reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{
				Slug:   "alice",
				Source: reqctx.UserPrincipalSourceSession,
			})
			return c.Next()
		}},
		patsService: &fakePATsService{
			UpdatePersonalAccessTokenFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
				model.UpdatePersonalAccessToken,
			) (*model.PersonalAccessToken, error) {
				updateCalled = true
				return &model.PersonalAccessToken{}, nil
			},
		},
		authService: fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) {
			require.NotNil(t, requestCtx)
			requestCtx.Locals("user_principal", nil)
			return true, nil
		}},
	})

	req := jsonRequest(t, http.MethodPut, "/users/profile/personal-access-tokens/developer-cli", map[string]any{
		"name": "Renamed",
	})
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.Forbidden(model.ResourceProfile.String(), model.Update.String()))
	require.False(t, updateCalled)
}

func TestPATManagerCannotPermanentlyDelete(t *testing.T) {
	t.Parallel()

	app, _ := newTestApp(t, testServerConfig{
		middleware:  []fiber.Handler{patPrincipalMiddleware(true)},
		patsService: &fakePATsService{},
		authService: allowAuthorizationService(),
	})

	req := jsonRequest(t, http.MethodPost, "/users/profile/personal-access-tokens/developer-cli/delete", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.UserPrincipalRequired())
}

func TestBearerAuthMiddlewarePropagatesCredentialManagementCapabilities(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(NewBearerAuthMiddleware(
		fakeAPIKeyAuthenticator{err: context.Canceled},
		fakePATAuthenticator{principal: &model.UserPrincipal{
			UserSlug:                      "alice",
			PersonalAccessTokenSlug:       "manager-pat",
			CanManagePersonalAccessTokens: true,
		}},
	))
	app.Get("/probe", func(c fiber.Ctx) error {
		principal, ok := reqctx.GetUserPrincipal(c, nil)
		require.True(t, ok)
		require.Equal(t, slug.Slug("manager-pat"), principal.PersonalAccessTokenSlug)
		require.True(t, principal.CanManagePersonalAccessTokens)
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := jsonRequest(t, http.MethodGet, "/probe", nil)
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+patssvc.TokenPrefix+"test-token")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func apiKeyPrincipalMiddleware(principal reqctx.APIKeyPrincipal) fiber.Handler {
	return func(c fiber.Ctx) error {
		reqctx.SetAPIKeyPrincipal(c, principal)
		return c.Next()
	}
}

func patPrincipalMiddleware(canManage bool) fiber.Handler {
	return func(c fiber.Ctx) error {
		reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{
			Slug:                          "alice",
			Source:                        reqctx.UserPrincipalSourcePersonalAccessToken,
			PersonalAccessTokenSlug:       "manager-pat",
			CanManagePersonalAccessTokens: canManage,
		})
		return c.Next()
	}
}

func allowAuthorizationService() fakeAuthService {
	return fakeAuthService{CanFn: func(context.Context, authorization.Input) (bool, error) {
		return true, nil
	}}
}
