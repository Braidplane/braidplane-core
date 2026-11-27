package http

import (
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/mcpauthsvc"
)

// NewMCPDelegatedOperationAuth adapts an OAuth grant to the shared Core operation
// handlers. Use it only at the operation boundary, after route parameters are available.
func NewMCPDelegatedOperationAuth(tokens *mcpauthsvc.TokenService) (func(fiber.Ctx, fiber.Handler) error, error) {
	if tokens == nil || tokens.Deployment() == nil {
		return nil, xerror.Unexpected().Detail("MCP operation authentication requires the deployment token service")
	}
	_, metadata := tokens.Deployment().ProtectedResourceDiscovery()
	return func(c fiber.Ctx, next fiber.Handler) error {
		c.Set(fiber.HeaderCacheControl, "private, no-store")
		headers := c.Request().Header.PeekAll(fiber.HeaderAuthorization)
		if len(headers) != 1 || len(headers[0]) > maxMCPOAuthAuthBytes {
			return apierror.Unauthorized()
		}
		parts := strings.Fields(string(headers[0]))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return apierror.Unauthorized()
		}
		operation := routeOperation(c)
		org, err := slug.New(c.Params("slug"))
		if err != nil || operation == "" {
			return apierror.Forbidden("operation", "delegated")
		}
		ctx, principal, err := tokens.AuthenticateDelegatedRequest(c.Context(), parts[1], metadata.Resource, org)
		if xerror.Is(err, mcpauthsvc.ErrInvalidAccessToken) {
			return apierror.Unauthorized()
		}
		if err != nil {
			return apierror.InternalServerError()
		}
		if !authorization.DelegatedOperationInScope(ctx, operation) {
			return apierror.Forbidden("operation", "delegated")
		}
		if _, authenticated := reqctx.GetAPIKeyPrincipal(c); authenticated {
			return apierror.Unauthorized()
		}
		c.SetContext(ctx)
		if principal.User != "" {
			reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{Slug: principal.User, Source: reqctx.UserPrincipalSourceDelegated})
		} else {
			reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{})
			reqctx.SetAPIKeyPrincipal(c, reqctx.APIKeyPrincipal{
				Slug: principal.APIKey, OrgSlug: principal.Organization,
			})
		}
		return next(c)
	}, nil
}
