package http

import (
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/service/mcpauthsvc"
)

const (
	oauthTestIssuer   = "https://issuer.example.test/auth"
	oauthTestResource = "https://resource.example.test/product/mcp"
	oauthTestToken    = "/auth/token"
)

func oauthTestConfig(t *testing.T, issuer string) MCPOAuthRoutesConfig {
	t.Helper()
	deployment, err := mcpauthsvc.New(issuer, oauthTestResource, nil)
	require.NoError(t, err)
	service, err := mcpauthsvc.NewTokenService(deployment, testutil.FakeTransactionManager{})
	require.NoError(t, err)
	return MCPOAuthRoutesConfig{
		Tokens: service, Authorize: func(c fiber.Ctx) error { return c.SendString("browser handler") },
		AbuseLimiter: httpsecurity.NewMemoryAbuseLimiter(), RequestLimit: 100, RequestWindow: time.Minute,
	}
}

func TestMCPOAuthDiscoveryUsesConfiguredURLs(t *testing.T) {
	app := fiber.New()
	cfg := oauthTestConfig(t, oauthTestIssuer)
	require.NoError(t, RegisterMCPOAuthRoutes(app, cfg))
	for _, tc := range []struct {
		path, field, want string
	}{
		{"/.well-known/oauth-protected-resource/product/mcp", "resource", oauthTestResource},
		{"/.well-known/oauth-authorization-server/auth", "issuer", oauthTestIssuer},
		{"/.well-known/oauth-authorization-server/auth", "token_endpoint", oauthTestIssuer + "/token"},
		{"/.well-known/oauth-authorization-server/auth", "authorization_endpoint", oauthTestIssuer + "/authorize"},
		{"/.well-known/oauth-authorization-server/auth", "revocation_endpoint", oauthTestIssuer + "/revoke"},
	} {
		req := httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, "https://different.example.test"+tc.path, nil)
		req.Header.Set("X-Forwarded-Host", "not-the-issuer.example.test")
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, nethttp.StatusOK, resp.StatusCode)
		var result map[string]any
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
		require.NoError(t, resp.Body.Close())
		require.Equal(t, tc.want, result[tc.field])
	}
	resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, "/auth/authorize", nil))
	require.NoError(t, err)
	require.Equal(t, nethttp.StatusOK, resp.StatusCode)
	require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
	require.NoError(t, resp.Body.Close())
}

func TestMCPOAuthMountRequiresConsentLimiterAndLiteralDistinctPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *fiber.App, *MCPOAuthRoutesConfig)
	}{
		{"missing consent", func(_ *testing.T, _ *fiber.App, cfg *MCPOAuthRoutesConfig) { cfg.Authorize = nil }},
		{"missing limiter", func(_ *testing.T, _ *fiber.App, cfg *MCPOAuthRoutesConfig) { cfg.AbuseLimiter = nil }},
		{"route conflict", func(_ *testing.T, app *fiber.App, _ *MCPOAuthRoutesConfig) {
			app.Post("/AUTH/token/", func(c fiber.Ctx) error { return c.SendStatus(nethttp.StatusNoContent) })
		}},
		{"parameter route conflict", func(_ *testing.T, app *fiber.App, _ *MCPOAuthRoutesConfig) {
			app.Post("/auth/:endpoint", func(c fiber.Ctx) error { return c.SendStatus(nethttp.StatusNoContent) })
		}},
		{"parameter path", func(t *testing.T, _ *fiber.App, cfg *MCPOAuthRoutesConfig) {
			*cfg = oauthTestConfig(t, "https://issuer.example.test/:path")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := fiber.New()
			cfg := oauthTestConfig(t, oauthTestIssuer)
			tc.edit(t, app, &cfg)
			require.Error(t, RegisterMCPOAuthRoutes(app, cfg))
			resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, "/.well-known/oauth-authorization-server/auth", nil))
			require.NoError(t, err)
			require.Equal(t, nethttp.StatusNotFound, resp.StatusCode)
			require.NoError(t, resp.Body.Close())
		})
	}
}

func TestMCPOAuthRejectsAmbiguousOrUnsupportedTokenRequests(t *testing.T) {
	app := fiber.New()
	cfg := oauthTestConfig(t, oauthTestIssuer)
	require.NoError(t, RegisterMCPOAuthRoutes(app, cfg))
	for _, tc := range []struct {
		name, path, body, oauthError string
		status                       int
		edit                         func(*nethttp.Request)
	}{
		{"repeated grant", oauthTestToken, "grant_type=refresh_token&grant_type=client_credentials", mcpOAuthInvalidRequest, 400, nil},
		{"repeated audience", oauthTestToken, "grant_type=refresh_token&resource=one&resource=two", mcpOAuthInvalidRequest, 400, nil},
		{"query credential", oauthTestToken + "?code=synthetic", "grant_type=authorization_code&client_id=console", mcpOAuthInvalidRequest, 400, nil},
		{"malformed form", oauthTestToken, "grant_type=refresh_token&refresh_token=%ZZ", mcpOAuthInvalidRequest, 400, nil},
		{"large form", oauthTestToken, strings.Repeat("x", maxMCPOAuthFormBytes+1), mcpOAuthInvalidRequest, 400, nil},
		{"unsupported encoding", oauthTestToken, "grant_type=refresh_token", mcpOAuthInvalidRequest, 400, func(r *nethttp.Request) {
			r.Header.Set("Content-Encoding", "gzip")
		}},
		{"JSON", oauthTestToken, "{}", mcpOAuthInvalidRequest, 400, func(r *nethttp.Request) {
			r.Header.Set("Content-Type", "application/json")
		}},
		{"body secret", oauthTestToken, "grant_type=client_credentials&client_id=console&client_secret=synthetic", mcpOAuthInvalidClient, 401, nil},
		{"machine without proof", oauthTestToken, "grant_type=client_credentials&client_id=console", mcpOAuthInvalidClient, 401, nil},
		{"multiple auth headers", oauthTestToken, "grant_type=client_credentials", mcpOAuthInvalidClient, 401, func(r *nethttp.Request) {
			r.SetBasicAuth("console", "synthetic")
			r.Header.Add("Authorization", "Bearer synthetic")
		}},
		{"Basic and body identity", oauthTestToken, "grant_type=client_credentials&client_id=second", mcpOAuthInvalidClient, 401, func(r *nethttp.Request) {
			r.SetBasicAuth("console", "synthetic")
		}},
		{"Bearer is not client auth", oauthTestToken, "grant_type=refresh_token", mcpOAuthInvalidClient, 401, func(r *nethttp.Request) {
			r.Header.Set("Authorization", "Bearer synthetic")
		}},
		{"scope escalation", oauthTestToken, "grant_type=refresh_token&client_id=console&scope=admin", "invalid_scope", 400, nil},
		{"unsupported grant", oauthTestToken, "grant_type=password&client_id=console", "unsupported_grant_type", 400, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), nethttp.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", fiber.MIMEApplicationForm)
			if tc.edit != nil {
				tc.edit(req)
			}
			res, err := app.Test(req)
			require.NoError(t, err)
			var response map[string]string
			require.NoError(t, json.NewDecoder(res.Body).Decode(&response))
			require.NoError(t, res.Body.Close())
			require.Equal(t, tc.status, res.StatusCode)
			require.Equal(t, map[string]string{"error": tc.oauthError}, response)
			require.Equal(t, "no-store", res.Header.Get("Cache-Control"))
			if tc.status == nethttp.StatusUnauthorized {
				require.Equal(t, `Basic realm="MCP OAuth"`, res.Header.Get("WWW-Authenticate"))
			}
		})
	}
}

func TestMCPOAuthRoutesChargeSharedAbuseBudget(t *testing.T) {
	app := fiber.New()
	cfg := oauthTestConfig(t, oauthTestIssuer)
	cfg.RequestLimit = 1
	require.NoError(t, RegisterMCPOAuthRoutes(app, cfg))
	for _, path := range []string{"/auth/authorize", "/auth/token"} {
		resp, err := app.Test(httptest.NewRequestWithContext(t.Context(), nethttp.MethodPost, path, nil))
		require.NoError(t, err)
		if path == oauthTestToken {
			require.Equal(t, nethttp.StatusTooManyRequests, resp.StatusCode)
			require.NotEmpty(t, resp.Header.Get("Retry-After"))
		} else {
			require.Equal(t, nethttp.StatusOK, resp.StatusCode)
		}
		require.NoError(t, resp.Body.Close())
	}
}
