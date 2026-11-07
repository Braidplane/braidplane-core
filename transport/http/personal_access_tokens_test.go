package http

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/patssvc"
)

func TestServer_CreatePersonalAccessTokenReturnsRawTokenForSessionUser(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	var gotUserSlug slug.Slug
	var gotNewPAT model.NewPersonalAccessToken
	app, store := newTestApp(t, testServerConfig{
		patsService: &fakePATsService{
			CreatePersonalAccessTokenFn: func(
				_ context.Context,
				userSlug slug.Slug,
				newPAT model.NewPersonalAccessToken,
			) (*model.PersonalAccessTokenWithToken, error) {
				gotUserSlug = userSlug
				gotNewPAT = newPAT
				return &model.PersonalAccessTokenWithToken{
					PersonalAccessToken: model.PersonalAccessToken{
						Slug:        "developer-cli",
						Name:        newPAT.Name,
						Description: newPAT.Description,
						TokenPrefix: "xto_pat_abcd123",
						UserSlug:    userSlug,
						ExpiresAt:   newPAT.ExpiresAt,
						Timestamps: model.Timestamps{
							CreatedAt: now,
							UpdatedAt: now,
						},
					},
					Token: "xto_pat_raw-token",
				}, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPost,
		"/users/profile/personal-access-tokens",
		map[string]any{
			"name":        "Developer CLI",
			"slug":        "developer-cli",
			"description": "Draft workflow token",
		},
	)
	resp := authenticatedRequest(t, app, store, "alice", req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	body := parseResponse[api.PersonalAccessTokenWithToken](t, resp)
	require.Equal(t, api.Slug("developer-cli"), body.Slug)
	require.Equal(t, api.Name("Developer CLI"), body.Name)
	require.NotNil(t, body.Description)
	require.Equal(t, "Draft workflow token", *body.Description)
	require.Equal(t, "xto_pat_raw-token", body.Token)
	require.Equal(t, slug.Slug("alice"), gotUserSlug)
	require.Equal(t, slug.Slug("developer-cli"), gotNewPAT.Slug)
	require.Equal(t, "Developer CLI", gotNewPAT.Name)
}

func TestServer_ListPersonalAccessTokensReturnsProfileTokens(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	description := "Draft workflow token"
	nextCursor := "next-token"
	prevCursor := "prev-token"
	var gotUserSlug slug.Slug
	var gotReq model.ListRequest
	app, store := newTestApp(t, testServerConfig{
		patsService: &fakePATsService{
			ListPersonalAccessTokensFn: func(
				_ context.Context,
				userSlug slug.Slug,
				req model.ListRequest,
			) (model.ListResponse[model.PersonalAccessToken], error) {
				gotUserSlug = userSlug
				gotReq = req
				return model.ListResponse[model.PersonalAccessToken]{
					Items: []model.PersonalAccessToken{
						{
							Slug:        "developer-cli",
							Name:        "Developer CLI",
							Description: &description,
							TokenPrefix: "xto_pat_abcd123",
							UserSlug:    userSlug,
							Timestamps: model.Timestamps{
								CreatedAt: now,
								UpdatedAt: now,
							},
						},
					},
					HasMore:    true,
					NextCursor: &nextCursor,
					PrevCursor: &prevCursor,
				}, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodGet,
		"/users/profile/personal-access-tokens?limit=2&cursor=cursor-prev&sort_by=personal_access_token_name&order=asc&direction=prev",
		nil,
	)
	resp := authenticatedRequest(t, app, store, "alice", req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := parseResponse[api.PersonalAccessTokenListResponse](t, resp)
	require.True(t, body.HasMore)
	require.Equal(t, &nextCursor, body.NextCursor)
	require.Equal(t, &prevCursor, body.PrevCursor)
	require.Len(t, body.PersonalAccessTokens, 1)
	require.Equal(t, api.Slug("developer-cli"), body.PersonalAccessTokens[0].Slug)
	require.Equal(t, api.Name("Developer CLI"), body.PersonalAccessTokens[0].Name)
	require.Equal(t, "xto_pat_abcd123", body.PersonalAccessTokens[0].TokenPrefix)
	require.Equal(t, slug.Slug("alice"), gotUserSlug)
	require.Equal(t, model.ListRequest{
		Limit:     2,
		Cursor:    "cursor-prev",
		SortBy:    model.ListSortName,
		Order:     model.ListOrderAsc,
		Direction: model.ListDirectionPrev,
	}, gotReq)
}

func TestServer_GetPersonalAccessTokenReturnsProfileToken(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	var gotUserSlug slug.Slug
	var gotPATSlug slug.Slug
	app, store := newTestApp(t, testServerConfig{
		patsService: &fakePATsService{
			GetPersonalAccessTokenFn: func(
				_ context.Context,
				userSlug, patSlug slug.Slug,
			) (*model.PersonalAccessToken, error) {
				gotUserSlug = userSlug
				gotPATSlug = patSlug
				return &model.PersonalAccessToken{
					Slug:        patSlug,
					Name:        "Developer CLI",
					TokenPrefix: "xto_pat_abcd123",
					UserSlug:    userSlug,
					Timestamps: model.Timestamps{
						CreatedAt: now,
						UpdatedAt: now,
					},
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodGet, "/users/profile/personal-access-tokens/developer-cli", nil)
	resp := authenticatedRequest(t, app, store, "alice", req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := parseResponse[api.PersonalAccessToken](t, resp)
	require.Equal(t, api.Slug("developer-cli"), body.Slug)
	require.Equal(t, api.Name("Developer CLI"), body.Name)
	require.Equal(t, "xto_pat_abcd123", body.TokenPrefix)
	require.Equal(t, slug.Slug("alice"), gotUserSlug)
	require.Equal(t, slug.Slug("developer-cli"), gotPATSlug)
}

func TestServer_UpdatePersonalAccessTokenUpdatesProfileTokenMetadata(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	expiresAt := now.Add(24 * time.Hour)
	description := "Updated local CLI token"
	var gotUserSlug slug.Slug
	var gotPATSlug slug.Slug
	var gotUpdate model.UpdatePersonalAccessToken
	app, store := newTestApp(t, testServerConfig{
		patsService: &fakePATsService{
			UpdatePersonalAccessTokenFn: func(
				_ context.Context,
				userSlug, patSlug slug.Slug,
				update model.UpdatePersonalAccessToken,
			) (*model.PersonalAccessToken, error) {
				gotUserSlug = userSlug
				gotPATSlug = patSlug
				gotUpdate = update
				return &model.PersonalAccessToken{
					Slug:        patSlug,
					Name:        update.Name,
					Description: update.Description.Value,
					TokenPrefix: "xto_pat_abcd123",
					UserSlug:    userSlug,
					ExpiresAt:   update.ExpiresAt.Value,
					Timestamps: model.Timestamps{
						CreatedAt: now,
						UpdatedAt: now,
					},
				}, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPut,
		"/users/profile/personal-access-tokens/developer-cli",
		api.UpdatePersonalAccessToken{
			Name:        "Developer CLI renamed",
			Description: &description,
			ExpiresAt:   &expiresAt,
		},
	)
	resp := authenticatedRequest(t, app, store, "alice", req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := parseResponse[api.PersonalAccessToken](t, resp)
	require.Equal(t, api.Slug("developer-cli"), body.Slug)
	require.Equal(t, api.Name("Developer CLI renamed"), body.Name)
	require.NotNil(t, body.Description)
	require.Equal(t, "Updated local CLI token", *body.Description)
	require.Equal(t, "xto_pat_abcd123", body.TokenPrefix)
	require.Equal(t, slug.Slug("alice"), gotUserSlug)
	require.Equal(t, slug.Slug("developer-cli"), gotPATSlug)
	require.Equal(t, "Developer CLI renamed", gotUpdate.Name)
	require.True(t, gotUpdate.Description.Present)
	require.Equal(t, "Updated local CLI token", *gotUpdate.Description.Value)
	require.True(t, gotUpdate.ExpiresAt.Present)
	require.NotNil(t, gotUpdate.ExpiresAt.Value)
	require.True(t, gotUpdate.ExpiresAt.Value.Equal(expiresAt))
}

func TestServer_UpdatePersonalAccessTokenPreservesOmittedNullableMetadata(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	var gotUpdate model.UpdatePersonalAccessToken
	app, store := newTestApp(t, testServerConfig{
		patsService: &fakePATsService{
			UpdatePersonalAccessTokenFn: func(
				_ context.Context,
				userSlug, patSlug slug.Slug,
				update model.UpdatePersonalAccessToken,
			) (*model.PersonalAccessToken, error) {
				gotUpdate = update
				return &model.PersonalAccessToken{
					Slug:        patSlug,
					Name:        update.Name,
					TokenPrefix: "xto_pat_abcd123",
					UserSlug:    userSlug,
					Timestamps: model.Timestamps{
						CreatedAt: now,
						UpdatedAt: now,
					},
				}, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPut,
		"/users/profile/personal-access-tokens/developer-cli",
		map[string]any{"name": "Developer CLI renamed"},
	)
	resp := authenticatedRequest(t, app, store, "alice", req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "Developer CLI renamed", gotUpdate.Name)
	require.False(t, gotUpdate.Description.Present)
	require.Nil(t, gotUpdate.Description.Value)
	require.False(t, gotUpdate.ExpiresAt.Present)
	require.Nil(t, gotUpdate.ExpiresAt.Value)
}

func TestServer_UpdatePersonalAccessTokenClearsExplicitNullMetadata(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	var gotUpdate model.UpdatePersonalAccessToken
	app, store := newTestApp(t, testServerConfig{
		patsService: &fakePATsService{
			UpdatePersonalAccessTokenFn: func(
				_ context.Context,
				userSlug, patSlug slug.Slug,
				update model.UpdatePersonalAccessToken,
			) (*model.PersonalAccessToken, error) {
				gotUpdate = update
				return &model.PersonalAccessToken{
					Slug:        patSlug,
					Name:        update.Name,
					TokenPrefix: "xto_pat_abcd123",
					UserSlug:    userSlug,
					Timestamps: model.Timestamps{
						CreatedAt: now,
						UpdatedAt: now,
					},
				}, nil
			},
		},
	})

	req := jsonRequest(
		t,
		http.MethodPut,
		"/users/profile/personal-access-tokens/developer-cli",
		map[string]any{
			"name":        "Developer CLI renamed",
			"description": nil,
			"expires_at":  nil,
		},
	)
	resp := authenticatedRequest(t, app, store, "alice", req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "Developer CLI renamed", gotUpdate.Name)
	require.True(t, gotUpdate.Description.Present)
	require.Nil(t, gotUpdate.Description.Value)
	require.True(t, gotUpdate.ExpiresAt.Present)
	require.Nil(t, gotUpdate.ExpiresAt.Value)
}

func TestServer_UpdatePersonalAccessTokenRejectsTopLevelNullBody(t *testing.T) {
	t.Parallel()

	updateCalled := false
	app, store := newTestApp(t, testServerConfig{
		patsService: &fakePATsService{
			UpdatePersonalAccessTokenFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
				model.UpdatePersonalAccessToken,
			) (*model.PersonalAccessToken, error) {
				updateCalled = true
				return nil, patssvc.ErrInvalidInput
			},
		},
	})

	req := jsonRequest(t, http.MethodPut, "/users/profile/personal-access-tokens/developer-cli", "placeholder")
	req.Body = io.NopCloser(strings.NewReader("null"))
	req.ContentLength = int64(len("null"))
	resp := authenticatedRequest(t, app, store, "alice", req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.False(t, updateCalled)
	assertErrorResponse(t, resp, apierror.InvalidRequestBody())
}

func TestServer_DeletePersonalAccessTokenRevokesProfileToken(t *testing.T) {
	t.Parallel()

	var gotUserSlug slug.Slug
	var gotPATSlug slug.Slug
	app, store := newTestApp(t, testServerConfig{
		patsService: &fakePATsService{
			RevokePersonalAccessTokenFn: func(_ context.Context, userSlug, patSlug slug.Slug) error {
				gotUserSlug = userSlug
				gotPATSlug = patSlug
				return nil
			},
		},
	})

	req := jsonRequest(t, http.MethodDelete, "/users/profile/personal-access-tokens/developer-cli", nil)
	resp := authenticatedRequest(t, app, store, "alice", req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, slug.Slug("alice"), gotUserSlug)
	require.Equal(t, slug.Slug("developer-cli"), gotPATSlug)
}

func TestServer_DeleteRevokedPersonalAccessTokenDeletesProfileToken(t *testing.T) {
	t.Parallel()

	var gotUserSlug slug.Slug
	var gotPATSlug slug.Slug
	app, store := newTestApp(t, testServerConfig{
		patsService: &fakePATsService{
			DeleteRevokedPersonalAccessTokenFn: func(_ context.Context, userSlug, patSlug slug.Slug) error {
				gotUserSlug = userSlug
				gotPATSlug = patSlug
				return nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/users/profile/personal-access-tokens/developer-cli/delete", nil)
	resp := authenticatedRequest(t, app, store, "alice", req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusNoContent, resp.StatusCode)
	require.Equal(t, slug.Slug("alice"), gotUserSlug)
	require.Equal(t, slug.Slug("developer-cli"), gotPATSlug)
}

func TestServer_DeleteRevokedPersonalAccessTokenRejectsActiveToken(t *testing.T) {
	t.Parallel()

	app, store := newTestApp(t, testServerConfig{
		patsService: &fakePATsService{
			DeleteRevokedPersonalAccessTokenFn: func(context.Context, slug.Slug, slug.Slug) error {
				return patssvc.ErrPersonalAccessTokenNotRevoked
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/users/profile/personal-access-tokens/developer-cli/delete", nil)
	resp := authenticatedRequest(t, app, store, "alice", req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body := parseResponse[apierror.ErrorResponse](t, resp)
	require.NotNil(t, body.Error)
	require.Equal(t, "PERSONAL_ACCESS_TOKEN_INVALID_INPUT", body.Error.Code)
	require.Equal(t, "Personal access token must be revoked before deletion.", body.Error.DetailedError)
}

func TestServer_RotatePersonalAccessTokenReturnsNewRawToken(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Second)
	var gotUserSlug slug.Slug
	var gotPATSlug slug.Slug
	app, store := newTestApp(t, testServerConfig{
		patsService: &fakePATsService{
			RotatePersonalAccessTokenFn: func(
				_ context.Context,
				userSlug, patSlug slug.Slug,
			) (*model.PersonalAccessTokenWithToken, error) {
				gotUserSlug = userSlug
				gotPATSlug = patSlug
				return &model.PersonalAccessTokenWithToken{
					PersonalAccessToken: model.PersonalAccessToken{
						Slug:        patSlug,
						Name:        "Developer CLI",
						TokenPrefix: "xto_pat_rotated",
						UserSlug:    userSlug,
						Timestamps: model.Timestamps{
							CreatedAt: now,
							UpdatedAt: now,
						},
					},
					Token: "xto_pat_new-raw-token",
				}, nil
			},
		},
	})

	req := jsonRequest(t, http.MethodPost, "/users/profile/personal-access-tokens/developer-cli/rotate", nil)
	resp := authenticatedRequest(t, app, store, "alice", req)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := parseResponse[api.PersonalAccessTokenWithToken](t, resp)
	require.Equal(t, api.Slug("developer-cli"), body.Slug)
	require.Equal(t, "xto_pat_new-raw-token", body.Token)
	require.Equal(t, "xto_pat_rotated", body.TokenPrefix)
	require.Equal(t, slug.Slug("alice"), gotUserSlug)
	require.Equal(t, slug.Slug("developer-cli"), gotPATSlug)
}

func TestServer_CreatePersonalAccessTokenRejectsPATPrincipalWithoutManagementCapability(t *testing.T) {
	t.Parallel()

	app, _ := newTestApp(t, testServerConfig{
		middleware: []fiber.Handler{
			func(c fiber.Ctx) error {
				reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{
					Slug:   "alice",
					Source: reqctx.UserPrincipalSourcePersonalAccessToken,
				})
				return c.Next()
			},
		},
		patsService: &fakePATsService{},
	})

	req := jsonRequest(
		t,
		http.MethodPost,
		"/users/profile/personal-access-tokens",
		map[string]any{"name": "Developer CLI"},
	)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.Forbidden(model.ResourceProfile.String(), model.Update.String()))
}

func TestServer_RotatePersonalAccessTokenRejectsPATPrincipalWithoutManagementCapability(t *testing.T) {
	t.Parallel()

	app, _ := newTestApp(t, testServerConfig{
		middleware: []fiber.Handler{
			func(c fiber.Ctx) error {
				reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{
					Slug:   "alice",
					Source: reqctx.UserPrincipalSourcePersonalAccessToken,
				})
				return c.Next()
			},
		},
		patsService: &fakePATsService{},
	})

	req := jsonRequest(t, http.MethodPost, "/users/profile/personal-access-tokens/developer-cli/rotate", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.Forbidden(model.ResourceProfile.String(), model.Update.String()))
}

func TestServer_UpdatePersonalAccessTokenRejectsPATPrincipalWithoutManagementCapability(t *testing.T) {
	t.Parallel()

	app, _ := newTestApp(t, testServerConfig{
		middleware: []fiber.Handler{
			func(c fiber.Ctx) error {
				reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{
					Slug:   "alice",
					Source: reqctx.UserPrincipalSourcePersonalAccessToken,
				})
				return c.Next()
			},
		},
		patsService: &fakePATsService{},
	})

	req := jsonRequest(
		t,
		http.MethodPut,
		"/users/profile/personal-access-tokens/developer-cli",
		api.UpdatePersonalAccessToken{Name: "Developer CLI renamed"},
	)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.Forbidden(model.ResourceProfile.String(), model.Update.String()))
}

func TestServer_DeleteRevokedPersonalAccessTokenRejectsPATPrincipal(t *testing.T) {
	t.Parallel()

	app, _ := newTestApp(t, testServerConfig{
		middleware: []fiber.Handler{
			func(c fiber.Ctx) error {
				reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{
					Slug:   "alice",
					Source: reqctx.UserPrincipalSourcePersonalAccessToken,
				})
				return c.Next()
			},
		},
		patsService: &fakePATsService{},
	})

	req := jsonRequest(t, http.MethodPost, "/users/profile/personal-access-tokens/developer-cli/delete", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	assertErrorResponse(t, resp, apierror.UserPrincipalRequired())
}
