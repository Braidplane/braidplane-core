package http

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/apikeyssvc"
	"github.com/Braidplane/braidplane-core/service/patssvc"
)

func TestBearerAuthMiddleware_MapsInvalidTokenToUnauthorized(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(NewBearerAuthMiddleware(fakeAPIKeyAuthenticator{err: apikeyssvc.ErrInvalidToken}, fakePATAuthenticator{}))
	app.Get("/probe", func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequestWithContext(context.Background(), nethttp.MethodGet, "/probe", nil)
	req.Header.Set(fiber.HeaderAuthorization, "Bearer invalid")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, fiber.StatusUnauthorized, resp.StatusCode)
}

func TestBearerAuthMiddleware_MapsUnexpectedAuthErrorToServerError(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(NewBearerAuthMiddleware(fakeAPIKeyAuthenticator{
		err: xerror.Unexpected().Detail("database unavailable"),
	}, fakePATAuthenticator{}))
	app.Get("/probe", func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequestWithContext(context.Background(), nethttp.MethodGet, "/probe", nil)
	req.Header.Set(fiber.HeaderAuthorization, "Bearer valid-shaped-token")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, fiber.StatusInternalServerError, resp.StatusCode)
}

func TestBearerAuthMiddleware_IgnoresNonBearerAuthorization(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(NewBearerAuthMiddleware(fakeAPIKeyAuthenticator{
		err: xerror.Unexpected().Detail("authenticate should not be called"),
	}, fakePATAuthenticator{}))
	app.Get("/probe", func(c fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequestWithContext(context.Background(), nethttp.MethodGet, "/probe", nil)
	req.Header.Set(fiber.HeaderAuthorization, "Basic stale-client-header")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, fiber.StatusNoContent, resp.StatusCode)
}

func TestBearerAuthMiddleware_AuthenticatesPATAsUserPrincipal(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(NewBearerAuthMiddleware(
		fakeAPIKeyAuthenticator{err: xerror.Unexpected().Detail("api key auth should not be called")},
		fakePATAuthenticator{principal: &model.UserPrincipal{UserSlug: "alice"}},
	))
	app.Get("/probe", func(c fiber.Ctx) error {
		principal, ok := reqctx.GetUserPrincipal(c, nil)
		require.True(t, ok)
		require.Equal(t, slug.Slug("alice"), principal.Slug)
		require.Equal(t, reqctx.UserPrincipalSourcePersonalAccessToken, principal.Source)
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequestWithContext(context.Background(), nethttp.MethodGet, "/probe", nil)
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+patssvc.TokenPrefix+"test-token")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, fiber.StatusNoContent, resp.StatusCode)
}

func TestBearerAuthMiddleware_FallsBackToAPIKeyAuthForPATPrefixedAPIKey(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{ErrorHandler: apierror.Middleware()})
	app.Use(NewBearerAuthMiddleware(
		fakeAPIKeyAuthenticator{principal: &model.APIKeyPrincipal{
			Slug:    "pat-shaped-api-key",
			OrgSlug: "acme",
			Role:    "developer",
		}},
		fakePATAuthenticator{err: patssvc.ErrInvalidToken},
	))
	app.Get("/probe", func(c fiber.Ctx) error {
		principal, ok := reqctx.GetAPIKeyPrincipal(c)
		require.True(t, ok)
		require.Equal(t, slug.Slug("pat-shaped-api-key"), principal.Slug)
		require.Equal(t, slug.Slug("acme"), principal.OrgSlug)
		require.Equal(t, slug.Slug("developer"), principal.Role)
		return c.SendStatus(fiber.StatusNoContent)
	})

	req := httptest.NewRequestWithContext(context.Background(), nethttp.MethodGet, "/probe", nil)
	req.Header.Set(fiber.HeaderAuthorization, "Bearer "+patssvc.TokenPrefix+"valid-api-key-suffix")

	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, fiber.StatusNoContent, resp.StatusCode)
}

type fakeAPIKeyAuthenticator struct {
	principal *model.APIKeyPrincipal
	err       error
}

type fakePATAuthenticator struct {
	principal *model.UserPrincipal
	err       error
}

func (f fakePATAuthenticator) Authenticate(
	context.Context,
	string,
) (*model.UserPrincipal, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.principal != nil {
		return f.principal, nil
	}
	return &model.UserPrincipal{UserSlug: slug.Slug("test-user")}, nil
}

func (f fakeAPIKeyAuthenticator) Authenticate(
	context.Context,
	string,
) (*model.APIKeyPrincipal, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.principal != nil {
		return f.principal, nil
	}
	return &model.APIKeyPrincipal{
		Slug:    slug.Slug("automation-key"),
		OrgSlug: slug.Slug("acme"),
		Role:    slug.Slug("viewer"),
	}, nil
}
