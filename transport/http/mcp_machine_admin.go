package http

import (
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/extractors"
	"github.com/gofiber/fiber/v3/middleware/session"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/httpsecurity"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/mcpauthsvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

type MCPMachineAdministrationConfig struct {
	Tokens        *mcpauthsvc.TokenService
	Audit         workerproducer.EventProducer
	Sessions      *session.Store
	Policy        authorization.Service
	Admission     admissionsvc.Service
	AbuseLimiter  httpsecurity.AbuseLimiter
	RequestLimit  int
	RequestWindow time.Duration
}

// RegisterMCPMachineAdministrationRoutes mounts same-origin browser management
// routes under the issuer. Core startup does not register them.
func RegisterMCPMachineAdministrationRoutes(app *fiber.App, cfg MCPMachineAdministrationConfig) error {
	if app == nil || cfg.Tokens == nil || cfg.Tokens.Deployment() == nil || cfg.Sessions == nil ||
		cfg.Sessions.Extractor.Source != extractors.SourceCookie || cfg.Policy == nil || cfg.Admission == nil ||
		cfg.AbuseLimiter == nil || cfg.RequestLimit <= 0 || cfg.RequestWindow <= 0 {
		return xerror.Unexpected().Detail("MCP machine administration requires browser, policy, admission and abuse-control dependencies")
	}
	admin, err := mcpauthsvc.NewMachineAdministration(cfg.Tokens, cfg.Audit)
	if err != nil {
		return err
	}
	_, metadata := cfg.Tokens.Deployment().IssuerDiscovery()
	issuer, err := url.Parse(metadata.Issuer)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	base, err := mcpOAuthRoutePaths(app, []string{issuer.JoinPath("organizations").String()})
	if err != nil {
		return err
	}
	path := strings.TrimSuffix(base[0], "/") + "/:slug/api-keys/:api_key_slug/machine-clients"
	clientPath := path + "/:client_id"
	for _, route := range app.GetRoutes(true) {
		if strings.EqualFold(strings.TrimSuffix(route.Path, "/"), path) || strings.EqualFold(strings.TrimSuffix(route.Path, "/"), clientPath) {
			return xerror.Unexpected().Detail("MCP machine administration paths must be distinct")
		}
	}
	app.Post(path, func(c fiber.Ctx) error {
		request, err := cfg.authorize(c, authorization.OperationRegisterMCPMachineClient, model.Update)
		if err != nil {
			return err
		}
		registration, err := admin.Register(c.Context(), request)
		if err != nil {
			return mapServiceError(err)
		}
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{
			"client_id": registration.ClientID, "client_secret": registration.ClientSecret, "expires_at": registration.ExpiresAt,
		})
	})
	app.Delete(clientPath, func(c fiber.Ctx) error {
		request, err := cfg.authorize(c, authorization.OperationDisconnectMCPMachineClient, model.Delete)
		if err != nil {
			return err
		}
		if err := admin.Disconnect(c.Context(), request, c.Params("client_id")); err != nil {
			if xerror.Is(err, mcpauthsvc.ErrInvalidClient) {
				return apierror.New(fiber.StatusNotFound, "MCP_MACHINE_CLIENT_NOT_FOUND", "Machine client was not found on this API key")
			}
			return mapServiceError(err)
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	return nil
}

func (cfg MCPMachineAdministrationConfig) authorize(c fiber.Ctx, operation string, action model.Action) (mcpauthsvc.MachineAdminRequest, error) {
	setMCPBrowserHeaders(c)
	browser := mcpBrowserIdentity(c, cfg.Sessions)
	if browser.User == "" {
		return mcpauthsvc.MachineAdminRequest{}, mapServiceError(mcpauthsvc.ErrInvalidMachineAdminSession)
	}
	if len(c.BodyRaw()) != 0 || len(c.Request().URI().QueryString()) != 0 {
		return mcpauthsvc.MachineAdminRequest{}, apierror.InvalidRequestBody()
	}
	if len(c.Request().Header.PeekAll(httpsecurity.CSRFHeader)) != 1 {
		return mcpauthsvc.MachineAdminRequest{}, apierror.Forbidden("api_keys", action.String())
	}
	csrfOK, err := httpsecurity.ValidateCSRFToken(c, cfg.Sessions)
	if err != nil {
		return mcpauthsvc.MachineAdminRequest{}, mapServiceError(err)
	}
	if !csrfOK {
		return mcpauthsvc.MachineAdminRequest{}, apierror.Forbidden("api_keys", action.String())
	}
	org, key, err := apiKeyPathSlugs(c.Params("slug"), c.Params("api_key_slug"))
	if err != nil {
		return mcpauthsvc.MachineAdminRequest{}, err
	}
	ctx := authorization.WithDecisionScope(c.Context())
	allowed, err := cfg.Policy.Can(ctx, authorization.Input{
		Operation: operation, Source: authorization.SourceSession, UserSlug: browser.User, OrgSlug: &org,
		Resource: model.ResourceAPIKeys, Action: action, Target: model.AuthorizationTarget{Kind: "api_keys", Slug: key},
	})
	if err != nil {
		return mcpauthsvc.MachineAdminRequest{}, mapServiceError(err)
	}
	if !allowed {
		return mcpauthsvc.MachineAdminRequest{}, apierror.Forbidden("api_keys", action.String())
	}
	decision, err := cfg.AbuseLimiter.Attempt(ctx, httpsecurity.AbuseLimit{
		Scope: "auth.mcp_machine_admin", Key: "organization:" + org.String(), Limit: cfg.RequestLimit, Window: cfg.RequestWindow,
	})
	if err != nil {
		return mcpauthsvc.MachineAdminRequest{}, apierror.InternalServerError()
	}
	if !decision.Allowed {
		setAbuseRetryAfter(c, decision.ResetAt)
		return mcpauthsvc.MachineAdminRequest{}, apierror.RateLimited()
	}
	if err := cfg.Admission.CheckWriteAccess(ctx, org); err != nil {
		return mcpauthsvc.MachineAdminRequest{}, mapServiceError(err)
	}
	c.SetContext(ctx)
	return mcpauthsvc.MachineAdminRequest{Browser: browser, Organization: org, APIKey: key}, nil
}
