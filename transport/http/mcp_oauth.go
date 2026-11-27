package http

import (
	"mime"
	nethttp "net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/mcpauthsvc"
)

const (
	maxMCPOAuthFormBytes = 8 * 1024
	maxMCPOAuthAuthBytes = 4 * 1024

	mcpOAuthInvalidRequest = "invalid_request"
	mcpOAuthInvalidClient  = "invalid_client"
	mcpOAuthUnavailable    = "temporarily_unavailable"
)

var mcpOAuthPathPattern = regexp.MustCompile(`^/(?:[A-Za-z0-9._~-]+/)*[A-Za-z0-9._~-]+/?$`)

type MCPOAuthRoutesConfig struct {
	Tokens        *mcpauthsvc.TokenService
	Authorize     fiber.Handler
	AbuseLimiter  httpsecurity.AbuseLimiter
	RequestLimit  int
	RequestWindow time.Duration
}

// RegisterMCPOAuthRoutes mounts the issuer protocol separately from the product API.
// Authorize owns browser authentication and consent. Runtime composition must
// place the configured public origins behind trusted HTTPS ingress.
func RegisterMCPOAuthRoutes(app *fiber.App, cfg MCPOAuthRoutesConfig) error {
	if app == nil || cfg.Tokens == nil || cfg.Tokens.Deployment() == nil || cfg.Authorize == nil ||
		cfg.AbuseLimiter == nil || cfg.RequestLimit <= 0 || cfg.RequestWindow <= 0 {
		return xerror.Unexpected().Detail("MCP OAuth routes require token, consent and abuse-control dependencies")
	}
	resourceURL, resource := cfg.Tokens.Deployment().ProtectedResourceDiscovery()
	issuerURL, issuer := cfg.Tokens.Deployment().IssuerDiscovery()
	paths, err := mcpOAuthRoutePaths(app, []string{
		resourceURL, issuerURL, issuer.AuthorizationEndpoint, issuer.TokenEndpoint, issuer.RevocationEndpoint,
	})
	if err != nil {
		return err
	}
	app.Get(paths[0], func(c fiber.Ctx) error { return c.JSON(resource) })
	app.Get(paths[1], func(c fiber.Ctx) error { return c.JSON(issuer) })
	limited := func(c fiber.Ctx) error {
		c.Set(fiber.HeaderCacheControl, "no-store")
		c.Set(fiber.HeaderPragma, "no-cache")
		decision, err := cfg.AbuseLimiter.Attempt(c.Context(), httpsecurity.AbuseLimit{
			Scope: "auth.mcp_oauth", Key: "ip:" + c.IP(),
			Limit: cfg.RequestLimit, Window: cfg.RequestWindow,
		})
		if err != nil {
			return mcpOAuthError(c, fiber.StatusServiceUnavailable, mcpOAuthUnavailable)
		}
		if !decision.Allowed {
			setAbuseRetryAfter(c, decision.ResetAt)
			return mcpOAuthError(c, fiber.StatusTooManyRequests, mcpOAuthUnavailable)
		}
		return c.Next()
	}
	app.Add([]string{fiber.MethodGet, fiber.MethodPost}, paths[2], limited, cfg.Authorize)
	app.Post(paths[3], limited, func(c fiber.Ctx) error { return mcpOAuthToken(c, cfg.Tokens) })
	app.Post(paths[4], limited, func(c fiber.Ctx) error { return mcpOAuthRevoke(c, cfg.Tokens, resource.Resource) })
	return nil
}

func mcpOAuthRoutePaths(app *fiber.App, urls []string) ([]string, error) {
	normalize := func(path string) string { return strings.ToLower(strings.TrimSuffix(path, "/")) }
	occupied := make(map[string]bool)
	routes := app.GetRoutes(true)
	config := app.Config()
	paths := make([]string, 0, len(urls))
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil || u.RawPath != "" || !mcpOAuthPathPattern.MatchString(u.Path) || occupied[normalize(u.Path)] {
			return nil, xerror.Unexpected().Detail("MCP OAuth paths must be literal and distinct")
		}
		for _, route := range routes {
			if fiber.RoutePatternMatch(u.Path, route.Path, config) {
				return nil, xerror.Unexpected().Detail("MCP OAuth path overlaps an existing route")
			}
		}
		paths = append(paths, u.Path)
		occupied[normalize(u.Path)] = true
	}
	return paths, nil
}

func mcpOAuthForm(c fiber.Ctx) (url.Values, bool) {
	mediaType, _, err := mime.ParseMediaType(c.Get(fiber.HeaderContentType))
	if err != nil || mediaType != fiber.MIMEApplicationForm || c.Get(fiber.HeaderContentEncoding) != "" ||
		len(c.Request().URI().QueryString()) != 0 {
		return nil, false
	}
	return mcpOAuthValues(c.BodyRaw())
}

func mcpOAuthValues(raw []byte) (url.Values, bool) {
	if len(raw) > maxMCPOAuthFormBytes {
		return nil, false
	}
	form, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil, false
	}
	for _, values := range form {
		if len(values) != 1 {
			return nil, false
		}
	}
	return form, true
}

func mcpOAuthClient(c fiber.Ctx, form url.Values) (mcpauthsvc.MachineClientCredentials, bool) {
	headers := c.Request().Header.PeekAll(fiber.HeaderAuthorization)
	if form.Has("client_secret") || form.Has("client_assertion") || form.Has("client_assertion_type") {
		return mcpauthsvc.MachineClientCredentials{}, false
	}
	if len(headers) == 0 {
		id := form.Get("client_id")
		return mcpauthsvc.MachineClientCredentials{ClientID: id}, id != ""
	}
	if len(headers) != 1 || len(headers[0]) > maxMCPOAuthAuthBytes || form.Has("client_id") {
		return mcpauthsvc.MachineClientCredentials{}, false
	}
	request := nethttp.Request{Header: nethttp.Header{fiber.HeaderAuthorization: {string(headers[0])}}}
	id, secret, ok := request.BasicAuth()
	if !ok {
		return mcpauthsvc.MachineClientCredentials{}, false
	}
	id, idErr := url.QueryUnescape(id)
	secret, secretErr := url.QueryUnescape(secret)
	return mcpauthsvc.MachineClientCredentials{ClientID: id, ClientSecret: secret},
		idErr == nil && secretErr == nil && id != "" && secret != ""
}

func mcpOAuthToken(c fiber.Ctx, service *mcpauthsvc.TokenService) error {
	form, ok := mcpOAuthForm(c)
	if !ok || form.Get("grant_type") == "" {
		return mcpOAuthError(c, fiber.StatusBadRequest, mcpOAuthInvalidRequest)
	}
	client, ok := mcpOAuthClient(c, form)
	if !ok {
		return mcpOAuthError(c, fiber.StatusUnauthorized, mcpOAuthInvalidClient)
	}
	if form.Get("scope") != "" {
		// Consent currently establishes only the configured audience and tenant.
		return mcpOAuthError(c, fiber.StatusBadRequest, "invalid_scope")
	}
	resource := form.Get("resource")
	var pair mcpauthsvc.TokenPair
	var err error
	switch form.Get("grant_type") {
	case "authorization_code":
		if client.ClientSecret != "" {
			return mcpOAuthError(c, fiber.StatusUnauthorized, mcpOAuthInvalidClient)
		}
		request := mcpauthsvc.CodeRedemption{
			Code: form.Get("code"), ClientID: client.ClientID, RedirectURI: form.Get("redirect_uri"),
			Resource: resource, Verifier: form.Get("code_verifier"),
		}
		if request.Code == "" || request.RedirectURI == "" || request.Verifier == "" {
			return mcpOAuthError(c, fiber.StatusBadRequest, mcpOAuthInvalidRequest)
		}
		pair, err = service.RedeemUserCode(c.Context(), request)
	case "refresh_token":
		refresh := form.Get("refresh_token")
		if refresh == "" {
			return mcpOAuthError(c, fiber.StatusBadRequest, mcpOAuthInvalidRequest)
		}
		if client.ClientSecret == "" {
			pair, err = service.RotateUserRefreshToken(c.Context(), refresh, client.ClientID, resource)
		} else {
			pair, err = service.RotateMachineRefreshToken(c.Context(), client, refresh, resource)
		}
	case "client_credentials":
		if client.ClientSecret == "" {
			return mcpOAuthError(c, fiber.StatusUnauthorized, mcpOAuthInvalidClient)
		}
		pair, err = service.IssueMachineTokens(c.Context(), client, resource)
	default:
		return mcpOAuthError(c, fiber.StatusBadRequest, "unsupported_grant_type")
	}
	if err != nil {
		return mcpOAuthServiceError(c, err)
	}
	return c.JSON(fiber.Map{
		"access_token": pair.AccessToken, "refresh_token": pair.RefreshToken, "token_type": "Bearer",
		"expires_in": max(0, int64(time.Until(pair.AccessExpiresAt)/time.Second)),
	})
}

func mcpOAuthRevoke(c fiber.Ctx, service *mcpauthsvc.TokenService, audience string) error {
	form, ok := mcpOAuthForm(c)
	if !ok || form.Get("token") == "" {
		return mcpOAuthError(c, fiber.StatusBadRequest, mcpOAuthInvalidRequest)
	}
	client, ok := mcpOAuthClient(c, form)
	if !ok {
		return mcpOAuthError(c, fiber.StatusUnauthorized, mcpOAuthInvalidClient)
	}
	if resource := form.Get("resource"); resource != "" {
		audience = resource
	}
	var err error
	if client.ClientSecret == "" {
		err = service.RevokeUserToken(c.Context(), form.Get("token"), client.ClientID, audience)
	} else {
		err = service.RevokeMachineToken(c.Context(), client, form.Get("token"), audience)
	}
	if err != nil {
		return mcpOAuthServiceError(c, err)
	}
	return c.Status(fiber.StatusOK).Send(nil)
}

func mcpOAuthServiceError(c fiber.Ctx, err error) error {
	switch {
	case xerror.Is(err, mcpauthsvc.ErrInvalidClient):
		return mcpOAuthError(c, fiber.StatusUnauthorized, mcpOAuthInvalidClient)
	case xerror.Is(err, mcpauthsvc.ErrInvalidGrant):
		return mcpOAuthError(c, fiber.StatusBadRequest, "invalid_grant")
	case xerror.Is(err, mcpauthsvc.ErrInvalidTarget):
		return mcpOAuthError(c, fiber.StatusBadRequest, "invalid_target")
	case xerror.Is(err, mcpauthsvc.ErrInvalidRequest):
		return mcpOAuthError(c, fiber.StatusBadRequest, mcpOAuthInvalidRequest)
	default:
		return mcpOAuthError(c, fiber.StatusServiceUnavailable, mcpOAuthUnavailable)
	}
}

func mcpOAuthError(c fiber.Ctx, status int, code string) error {
	if status == fiber.StatusUnauthorized {
		c.Set(fiber.HeaderWWWAuthenticate, `Basic realm="MCP OAuth"`)
	}
	return c.Status(status).JSON(fiber.Map{"error": code})
}
