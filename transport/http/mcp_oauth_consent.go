package http

import (
	"net/url"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
	"github.com/gofiber/fiber/v3/middleware/session"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/mcpauthsvc"
)

// NewMCPOAuthConsentHandler requires the issuer and browser login to share an origin.
func NewMCPOAuthConsentHandler(tokens *mcpauthsvc.TokenService, store *session.Store,
	render func(c fiber.Ctx, prompt model.MCPConsentPrompt, action, csrf string) error,
) (fiber.Handler, error) {
	if tokens == nil || tokens.Deployment() == nil || store == nil || store.Extractor.Source != extractors.SourceCookie || render == nil {
		return nil, xerror.Unexpected().Detail("MCP consent requires a browser session store and renderer")
	}
	_, issuer := tokens.Deployment().IssuerDiscovery()
	endpoint, err := url.Parse(issuer.AuthorizationEndpoint)
	if err != nil || endpoint.RawPath != "" || !mcpOAuthPathPattern.MatchString(endpoint.Path) {
		return nil, xerror.Unexpected().Detail("MCP consent requires a literal authorize path")
	}
	return func(c fiber.Ctx) error {
		setMCPBrowserHeaders(c)
		identity := mcpBrowserIdentity(c, store)
		switch c.Method() {
		case fiber.MethodGet:
			return mcpConsentPrompt(c, tokens, store, identity, endpoint.Path, render)
		case fiber.MethodPost:
			return mcpConsentDecision(c, tokens, store, identity)
		default:
			return c.SendStatus(fiber.StatusMethodNotAllowed)
		}
	}, nil
}

func setMCPBrowserHeaders(c fiber.Ctx) {
	c.Set(fiber.HeaderReferrerPolicy, "no-referrer")
	c.Set(fiber.HeaderContentSecurityPolicy, "default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	c.Set(fiber.HeaderCacheControl, "no-store")
	c.Set(fiber.HeaderPragma, "no-cache")
}

func mcpBrowserIdentity(c fiber.Ctx, store *session.Store) mcpauthsvc.BrowserIdentity {
	if !httpsecurity.HasSessionLookup(c, store) || len(c.Request().Header.PeekAll(fiber.HeaderAuthorization)) != 0 {
		return mcpauthsvc.BrowserIdentity{}
	}
	principal, ok := reqctx.GetUserPrincipal(c, store)
	if !ok || principal.Source != reqctx.UserPrincipalSourceSession {
		return mcpauthsvc.BrowserIdentity{}
	}
	sess, err := store.Get(c)
	if err != nil {
		return mcpauthsvc.BrowserIdentity{}
	}
	defer sess.Release()
	user, _ := sess.Get(reqctx.SessionUserKey).(string)
	version, _ := sess.Get(reqctx.SessionAuthenticationVersionKey).(int64)
	if user == "" || user != principal.Slug.String() || version <= 0 {
		return mcpauthsvc.BrowserIdentity{}
	}
	identity := mcpauthsvc.BrowserIdentity{
		User: principal.Slug, Organization: reqctx.GetActiveOrganizationSlug(c, store),
		SessionID: sess.ID(), AuthenticationVersion: version,
	}
	if nanos, ok := sess.Get(reqctx.SessionAuthenticatedAtNanosKey).(int64); ok && nanos > 0 {
		identity.AuthenticatedAt = time.Unix(0, nanos)
	}
	return identity
}

func mcpConsentPrompt(c fiber.Ctx, tokens *mcpauthsvc.TokenService, store *session.Store, identity mcpauthsvc.BrowserIdentity, action string,
	render func(fiber.Ctx, model.MCPConsentPrompt, string, string) error,
) error {
	query, ok := mcpOAuthValues(c.Request().URI().QueryString())
	if !ok {
		return mcpOAuthError(c, fiber.StatusBadRequest, mcpOAuthInvalidRequest)
	}
	if query.Get("scope") != "" {
		return mcpOAuthError(c, fiber.StatusBadRequest, "invalid_scope")
	}
	if identity.User == "" {
		resume := make(url.Values)
		for _, name := range []string{"client_id", "redirect_uri", "resource", "response_type", "code_challenge", "code_challenge_method", "state"} {
			if query.Has(name) {
				resume.Set(name, query.Get(name))
			}
		}
		return mcpConsentRedirect(c, "/auth/login?return_to="+url.QueryEscape(action+"?"+resume.Encode()))
	}
	csrf, err := httpsecurity.EnsureCSRFToken(c, store)
	if err != nil {
		return mcpOAuthServiceError(c, err)
	}
	prompt, err := tokens.BeginUserConsent(c.Context(), mcpauthsvc.AuthorizationRequest{
		ClientID: query.Get("client_id"), RedirectURI: query.Get("redirect_uri"), Resource: query.Get("resource"),
		ResponseType: query.Get("response_type"), Challenge: query.Get("code_challenge"), ChallengeMethod: query.Get("code_challenge_method"),
	}, query.Get("state"), identity)
	if xerror.Is(err, mcpauthsvc.ErrInvalidClient) {
		return mcpOAuthError(c, fiber.StatusBadRequest, mcpOAuthInvalidClient)
	}
	if err != nil {
		return mcpOAuthServiceError(c, err)
	}
	c.Type("html", "utf-8")
	return render(c, prompt, action, csrf)
}

func mcpConsentDecision(c fiber.Ctx, tokens *mcpauthsvc.TokenService, store *session.Store, identity mcpauthsvc.BrowserIdentity) error {
	if identity.User == "" {
		return mcpOAuthError(c, fiber.StatusForbidden, mcpOAuthInvalidRequest)
	}
	form, ok := mcpOAuthForm(c)
	decision := form.Get("decision")
	if !ok || len(form) != 3 || form.Get("consent_id") == "" || !form.Has(httpsecurity.CSRFFormField) ||
		c.Get(httpsecurity.CSRFHeader) != "" || (decision != "approve" && decision != "deny") {
		return mcpOAuthError(c, fiber.StatusBadRequest, mcpOAuthInvalidRequest)
	}
	valid, err := httpsecurity.ValidateCSRFToken(c, store)
	if err != nil {
		return mcpOAuthServiceError(c, err)
	}
	if !valid {
		return mcpOAuthError(c, fiber.StatusForbidden, mcpOAuthInvalidRequest)
	}
	callback, err := tokens.CompleteUserConsent(c.Context(), form.Get("consent_id"), identity, decision == "approve")
	if err != nil {
		return mcpOAuthServiceError(c, err)
	}
	return mcpConsentRedirect(c, callback)
}

func mcpConsentRedirect(c fiber.Ctx, location string) error {
	c.Set(fiber.HeaderLocation, location)
	return c.Status(fiber.StatusSeeOther).Send(nil)
}
