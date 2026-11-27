package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/mcpauthsvc"
	transport "github.com/Braidplane/braidplane-core/transport/http"
)

func TestOAuthHTTPTokenAndRevocation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL")
	}
	db := e2edb.OpenIsolatedPostgresPool(t, "MCP OAuth HTTP")
	_, err := db.Exec(t.Context(), `
INSERT INTO organizations (id,name,slug) VALUES (10,'First','first'),(20,'Second','second');
INSERT INTO roles (id,organization_id,name,slug) VALUES (13,10,'Reader','reader'),(23,20,'Reader','reader');
INSERT INTO users (id,slug,email,role,verified_at) VALUES (18,'member','member@example.test','user',now());
INSERT INTO organization_members (id,organization_id,user_id,role_id) VALUES (19,10,18,13),(29,20,18,23);
INSERT INTO api_keys (id,organization_id,slug,name,token_hash,token_prefix,role_id)
VALUES (15,10,'worker','Worker','synthetic-machine','synthetic',13);
`)
	require.NoError(t, err)
	const (
		issuer        = "https://auth.example.test/oauth"
		resource      = "https://core.example.test/mcp"
		resourceForm  = "resource"
		redirect      = "https://console.example.test/callback?destination=a%20b"
		clientID      = "console"
		verifier      = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		authorizePath = "/oauth/authorize"
		tokenPath     = "/oauth/token"
		revokePath    = "/oauth/revoke"
	)
	deployment, err := mcpauthsvc.New(issuer, resource, []mcpauthsvc.Client{
		{ID: clientID, Name: "Console <client>", RedirectURIs: []string{redirect}},
	})
	require.NoError(t, err)
	service, err := mcpauthsvc.NewTokenService(deployment, postgres.NewTransactionManager(db))
	require.NoError(t, err)
	app := fiber.New()
	store := session.NewStore(session.Config{Extractor: httpsecurity.SessionCookieExtractor(true), CookieSecure: true, CookiePath: "/"})
	app.Post("/synthetic-login", func(c fiber.Ctx) error {
		return reqctx.ResetAndSetAuthenticatedUser(c, store, "member", 1, true)
	})
	app.Post("/synthetic-select-first", func(c fiber.Ctx) error { return reqctx.SetActiveOrganizationSlug(c, store, "first") })
	app.Post("/synthetic-select-second", func(c fiber.Ctx) error { return reqctx.SetActiveOrganizationSlug(c, store, "second") })
	authorize, err := transport.NewMCPOAuthConsentHandler(service, store, func(c fiber.Ctx, prompt model.MCPConsentPrompt, action, csrf string) error {
		return views.MCPConsentPage(prompt, action, csrf).Render(c.Context(), c.Response().BodyWriter())
	})
	require.NoError(t, err)
	require.NoError(t, transport.RegisterMCPOAuthRoutes(app, transport.MCPOAuthRoutesConfig{
		Tokens: service, Authorize: authorize,
		AbuseLimiter: httpsecurity.NewMemoryAbuseLimiter(), RequestLimit: 100, RequestWindow: time.Minute,
	}))
	type outcome struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
	}
	post := func(t *testing.T, path string, form url.Values, basic *mcpauthsvc.MachineClientCredentials, status int) outcome {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if basic != nil {
			req.SetBasicAuth(url.QueryEscape(basic.ClientID), url.QueryEscape(basic.ClientSecret))
		}
		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()
		require.Equal(t, status, resp.StatusCode)
		require.Equal(t, "no-store", resp.Header.Get("Cache-Control"))
		var body outcome
		if path != revokePath || status != http.StatusOK {
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		}
		if path == tokenPath && status == http.StatusOK {
			require.NotEmpty(t, body.AccessToken)
			require.NotEmpty(t, body.RefreshToken)
			require.Equal(t, "Bearer", body.TokenType)
			require.Positive(t, body.ExpiresIn)
		}
		return body
	}

	type browserResponse struct {
		header  http.Header
		cookies []*http.Cookie
		body    string
	}
	browser := func(t *testing.T, method, path string, cookie *http.Cookie, form url.Values, status int) browserResponse {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(form.Encode()))
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := app.Test(req)
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, status, resp.StatusCode, "%s", body)
		return browserResponse{header: resp.Header, cookies: resp.Cookies(), body: string(body)}
	}
	login := func(t *testing.T) *http.Cookie {
		t.Helper()
		cookies := browser(t, http.MethodPost, "/synthetic-login", nil, nil, http.StatusOK).cookies
		require.Len(t, cookies, 1)
		browser(t, http.MethodPost, "/synthetic-select-first", cookies[0], nil, http.StatusOK)
		return cookies[0]
	}
	const state = "opaque state & checksum=synthetic"
	query := url.Values{
		"client_id": {clientID}, "redirect_uri": {redirect}, resourceForm: {resource}, "response_type": {"code"},
		"code_challenge": {oauthflow.S256Challenge(verifier)}, "code_challenge_method": {oauthflow.PKCES256}, "state": {state},
	}
	promptPath := authorizePath + "?" + query.Encode()
	requestConsent := func(t *testing.T, cookie *http.Cookie) url.Values {
		t.Helper()
		resp := browser(t, http.MethodGet, promptPath, cookie, nil, http.StatusOK)
		body := resp.body
		require.Contains(t, resp.header.Get("Content-Type"), "text/html")
		require.Equal(t, "no-store", resp.header.Get("Cache-Control"))
		require.Equal(t, "no-referrer", resp.header.Get("Referrer-Policy"))
		require.Contains(t, resp.header.Get("Content-Security-Policy"), "frame-ancestors 'none'")
		require.Contains(t, body, `action="/oauth/authorize"`)
		require.Contains(t, body, "Console &lt;client&gt;")
		require.Contains(t, body, "https://console.example.test")
		require.Contains(t, body, "Organization first")
		require.Contains(t, body, "Rename projects")
		require.Contains(t, body, "Read and make changes")
		require.Regexp(t, `name="access" value="read_only" checked`, body)
		require.NotContains(t, body, "Delete revoked API key records after separate approval")
		result := url.Values{"decision": {"approve"}, "access": {"read_only"}}
		for _, name := range []string{httpsecurity.CSRFFormField, "consent_id"} {
			match := regexp.MustCompile(`name="` + name + `" value="([A-Za-z0-9_-]+)"`).FindStringSubmatch(body)
			require.Len(t, match, 2)
			result.Set(name, match[1])
		}
		return result
	}
	callback := func(t *testing.T, resp browserResponse) url.Values {
		t.Helper()
		location := resp.header.Get("Location")
		require.True(t, strings.HasPrefix(location, redirect+"&"), "callback %q", location)
		uri, err := url.Parse(location)
		require.NoError(t, err)
		values := uri.Query()
		require.Equal(t, state, values.Get("state"))
		require.Equal(t, issuer, values.Get("iss"))
		return values
	}

	t.Run("browser consent to code redemption refresh and revoke", func(t *testing.T) {
		resp := browser(t, http.MethodGet, promptPath+"&ignored=discard", nil, nil, http.StatusSeeOther)
		loginURL, err := url.Parse(resp.header.Get("Location"))
		require.NoError(t, err)
		require.False(t, loginURL.IsAbs())
		require.Equal(t, "/auth/login", loginURL.Path)
		require.Equal(t, promptPath, loginURL.Query().Get("return_to"))
		resp = browser(t, http.MethodGet, promptPath+"&redirect_uri=https://untrusted.example.test", nil, nil, http.StatusBadRequest)
		require.Empty(t, resp.header.Get("Location"))

		first := login(t)
		second := login(t)
		consent := requestConsent(t, first)
		postConsent := func(cookie *http.Cookie, values url.Values, status int) browserResponse {
			resp := browser(t, http.MethodPost, authorizePath, cookie, values, status)
			if status != http.StatusSeeOther {
				require.Empty(t, resp.header.Get("Location"))
			}
			return resp
		}
		postConsent(nil, consent, http.StatusForbidden)
		consent.Del("access")
		postConsent(first, consent, http.StatusBadRequest)
		consent.Set("access", "all")
		postConsent(first, consent, http.StatusBadRequest)
		consent["access"] = []string{"read_only", "read_write"}
		postConsent(first, consent, http.StatusBadRequest)
		consent.Set("access", "read_only")
		csrf := consent.Get(httpsecurity.CSRFFormField)
		consent.Set(httpsecurity.CSRFFormField, "invalid")
		postConsent(first, consent, http.StatusForbidden)
		consent.Set(httpsecurity.CSRFFormField, requestConsent(t, second).Get(httpsecurity.CSRFFormField))
		postConsent(second, consent, http.StatusBadRequest)
		consent.Set(httpsecurity.CSRFFormField, csrf)
		consent.Set("redirect_uri", "https://untrusted.example.test")
		postConsent(first, consent, http.StatusBadRequest)
		consent.Del("redirect_uri")
		browser(t, http.MethodPost, "/synthetic-select-second", first, nil, http.StatusOK)
		postConsent(first, consent, http.StatusBadRequest)
		browser(t, http.MethodPost, "/synthetic-select-first", first, nil, http.StatusOK)
		code := callback(t, postConsent(first, consent, http.StatusSeeOther)).Get("code")
		require.NotEmpty(t, code)
		postConsent(first, consent, http.StatusBadRequest)

		denial := requestConsent(t, first)
		denial.Set("decision", "deny")
		denied := callback(t, postConsent(first, denial, http.StatusSeeOther))
		require.Equal(t, "access_denied", denied.Get("error"))
		require.Empty(t, denied.Get("code"))
		denial.Set("decision", "approve")
		postConsent(first, denial, http.StatusBadRequest)

		expired := requestConsent(t, first)
		_, err = db.Exec(t.Context(), `UPDATE mcp_oauth_pending_consents SET expires_at = now() - interval '1 second' WHERE nonce_hash = $1`,
			oauthflow.HashState(expired.Get("consent_id")))
		require.NoError(t, err)
		postConsent(first, expired, http.StatusBadRequest)

		form := url.Values{
			"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
			"redirect_uri": {redirect}, "code_verifier": {verifier},
		}
		require.Equal(t, "invalid_target", post(t, tokenPath, form, nil, http.StatusBadRequest).Error)
		form.Set(resourceForm, resource)
		form.Set("code_verifier", verifier+"x")
		require.Equal(t, "invalid_grant", post(t, tokenPath, form, nil, http.StatusBadRequest).Error)
		form.Set("code_verifier", verifier)
		pair := post(t, tokenPath, form, nil, http.StatusOK)
		delegatedContext, _, err := service.AuthenticateDelegatedRequest(t.Context(), pair.AccessToken, resource, "first")
		require.NoError(t, err)
		require.True(t, authorization.DelegatedOperationInScope(delegatedContext, "getProject"))
		require.False(t, authorization.DelegatedOperationInScope(delegatedContext, "updateProject"))
		refreshForm := url.Values{
			"grant_type": {"refresh_token"}, resourceForm: {resource}, "client_id": {clientID}, "refresh_token": {pair.RefreshToken},
		}
		rotated := post(t, tokenPath, refreshForm, nil, http.StatusOK)
		revocation := url.Values{"client_id": {clientID}, "token": {rotated.RefreshToken}}
		post(t, revokePath, revocation, nil, http.StatusOK)
		post(t, revokePath, revocation, nil, http.StatusOK)
		_, err = service.AuthenticateUserAccessToken(t.Context(), rotated.AccessToken, resource, "first")
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)
	})

	t.Run("confidential machine proof on issue refresh and revocation", func(t *testing.T) {
		registration, err := service.RegisterMachineClient(t.Context(), "first", "worker")
		require.NoError(t, err)
		credentials := registration.MachineClientCredentials
		form := url.Values{"grant_type": {"client_credentials"}, resourceForm: {resource}}
		pair := post(t, tokenPath, form, &credentials, http.StatusOK)
		refresh := url.Values{"grant_type": {"refresh_token"}, resourceForm: {resource}, "refresh_token": {pair.RefreshToken}}
		refresh.Set("client_id", credentials.ClientID)
		require.Equal(t, "invalid_grant", post(t, tokenPath, refresh, nil, http.StatusBadRequest).Error)
		refresh.Del("client_id")
		rotated := post(t, tokenPath, refresh, &credentials, http.StatusOK)
		bad := credentials
		bad.ClientSecret = "invalid-secret"
		require.Equal(t, "invalid_client", post(t, tokenPath, refresh, &bad, http.StatusUnauthorized).Error)
		_, err = service.AuthenticateMachineAccessToken(t.Context(), rotated.AccessToken, resource, "first")
		require.NoError(t, err)
		revocation := url.Values{"token": {rotated.AccessToken}}
		require.Equal(t, "invalid_client", post(t, revokePath, revocation, &bad, http.StatusUnauthorized).Error)
		post(t, revokePath, revocation, &credentials, http.StatusOK)
		_, err = service.AuthenticateMachineAccessToken(t.Context(), rotated.AccessToken, resource, "first")
		require.ErrorIs(t, err, mcpauthsvc.ErrInvalidAccessToken)
	})
}
