package http

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/apikeyssvc"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

func TestCreateAPIKeyPassesAuthenticatedActor(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("johndoe")
	orgSlug := must.Slug("acme")
	apiKeySlug := api.Slug("deploy-key")
	app, store := newTestApp(t, testServerConfig{
		apiKeysService: &fakeAPIKeysService{
			CreateAPIKeyFn: func(
				_ context.Context,
				gotOrgSlug slug.Slug,
				actor model.EventActor,
				newAPIKey model.NewAPIKey,
			) (*model.APIKeyWithToken, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				assertUserEventActor(t, actor, userSlug)
				require.Equal(t, must.Slug("deploy-key"), newAPIKey.Slug)
				return testAPIKeyWithToken("deploy-key"), nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Equal(t, userSlug, input.UserSlug)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/api-keys", api.NewAPIKey{
		Name: "Deploy key",
		Role: "viewer",
		Slug: &apiKeySlug,
	})
	resp := authenticatedRequest(t, app, store, userSlug, req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestUpdateAPIKeyPassesAuthenticatedActor(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("johndoe")
	orgSlug := must.Slug("acme")
	apiKeySlug := must.Slug("deploy-key")
	app, store := newTestApp(t, testServerConfig{
		apiKeysService: &fakeAPIKeysService{
			UpdateAPIKeyFn: func(
				_ context.Context,
				gotOrgSlug slug.Slug,
				gotAPIKeySlug slug.Slug,
				actor model.EventActor,
				update model.UpdateAPIKey,
			) (*model.APIKey, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, apiKeySlug, gotAPIKeySlug)
				assertUserEventActor(t, actor, userSlug)
				require.Equal(t, "Deploy key renamed", update.Name)
				key := testAPIKey("deploy-key")
				key.Name = update.Name
				return &key, nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Equal(t, userSlug, input.UserSlug)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPut, "/organizations/acme/api-keys/deploy-key", api.UpdateAPIKey{
		Name: "Deploy key renamed",
	})
	resp := authenticatedRequest(t, app, store, userSlug, req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestDeleteAPIKeyPassesAuthenticatedActor(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("johndoe")
	orgSlug := must.Slug("acme")
	apiKeySlug := must.Slug("deploy-key")
	app, store := newTestApp(t, testServerConfig{
		apiKeysService: &fakeAPIKeysService{
			RevokeAPIKeyFn: func(
				_ context.Context,
				gotOrgSlug slug.Slug,
				gotAPIKeySlug slug.Slug,
				actor model.EventActor,
			) error {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, apiKeySlug, gotAPIKeySlug)
				assertUserEventActor(t, actor, userSlug)
				return nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Equal(t, userSlug, input.UserSlug)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodDelete, "/organizations/acme/api-keys/deploy-key", nil)
	resp := authenticatedRequest(t, app, store, userSlug, req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestDeleteRevokedAPIKeyPassesAuthenticatedActor(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("johndoe")
	orgSlug := must.Slug("acme")
	apiKeySlug := must.Slug("deploy-key")
	app, store := newTestApp(t, testServerConfig{
		apiKeysService: &fakeAPIKeysService{
			DeleteRevokedAPIKeyFn: func(
				_ context.Context,
				gotOrgSlug slug.Slug,
				gotAPIKeySlug slug.Slug,
				actor model.EventActor,
			) error {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, apiKeySlug, gotAPIKeySlug)
				assertUserEventActor(t, actor, userSlug)
				return nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Equal(t, userSlug, input.UserSlug)
				require.Equal(t, model.Delete, input.Action)
				require.Equal(t, model.ResourceAPIKeys, input.Resource)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/api-keys/deploy-key/delete", nil)
	resp := authenticatedRequest(t, app, store, userSlug, req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestDeleteRevokedAPIKeyRejectsActiveKey(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("johndoe")
	app, store := newTestApp(t, testServerConfig{
		apiKeysService: &fakeAPIKeysService{
			DeleteRevokedAPIKeyFn: func(context.Context, slug.Slug, slug.Slug, model.EventActor) error {
				return apikeyssvc.ErrAPIKeyNotRevoked
			},
		},
		authService: fakeAuthService{
			CanFn: func(context.Context, authorization.Input) (bool, error) {
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/api-keys/deploy-key/delete", nil)
	resp := authenticatedRequest(t, app, store, userSlug, req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body := parseResponse[apierror.ErrorResponse](t, resp)
	require.NotNil(t, body.Error)
	assert.Equal(t, "API_KEY_INVALID_INPUT", body.Error.Code)
	assert.Equal(t, "API key must be revoked before deletion.", body.Error.DetailedError)
}

func TestRotateAPIKeyPassesAuthenticatedActor(t *testing.T) {
	t.Parallel()

	userSlug := must.Slug("johndoe")
	orgSlug := must.Slug("acme")
	apiKeySlug := must.Slug("deploy-key")
	app, store := newTestApp(t, testServerConfig{
		apiKeysService: &fakeAPIKeysService{
			RotateAPIKeyFn: func(
				_ context.Context,
				gotOrgSlug slug.Slug,
				gotAPIKeySlug slug.Slug,
				actor model.EventActor,
			) (*model.APIKeyWithToken, error) {
				require.Equal(t, orgSlug, gotOrgSlug)
				require.Equal(t, apiKeySlug, gotAPIKeySlug)
				assertUserEventActor(t, actor, userSlug)
				return testAPIKeyWithToken("deploy-key"), nil
			},
		},
		authService: fakeAuthService{
			CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
				require.Equal(t, userSlug, input.UserSlug)
				return true, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/organizations/acme/api-keys/deploy-key/rotate", nil)
	resp := authenticatedRequest(t, app, store, userSlug, req)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func testAPIKey(apiKeySlug slug.Slug) model.APIKey {
	return model.APIKey{
		Slug:        apiKeySlug,
		Name:        "Deploy key",
		TokenPrefix: "xto_example",
		Role:        "viewer",
	}
}

func testAPIKeyWithToken(apiKeySlug slug.Slug) *model.APIKeyWithToken {
	return &model.APIKeyWithToken{
		APIKey: testAPIKey(apiKeySlug),
		Token:  "xto_raw_token",
	}
}
