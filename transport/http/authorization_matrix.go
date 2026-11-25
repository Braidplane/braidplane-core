package http

import (
	"strings"

	"github.com/gofiber/fiber/v3"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

func routeOperation(c fiber.Ctx) string {
	method := c.Method()
	if method == fiber.MethodHead {
		method = fiber.MethodGet
	}
	return authorization.OperationForRoute(method, strings.TrimPrefix(c.Route().Path, "/api"))
}

// RequireMappedOperation rejects a new Core handler until it has an authority mapping.
func RequireMappedOperation(c fiber.Ctx, next fiber.Handler) error {
	if routeOperation(c) == "" {
		return apierror.Forbidden("operation", "unmapped")
	}
	return next(c)
}

func (s *server) authenticatedSource(c fiber.Ctx) authorization.Source {
	if _, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		return authorization.SourceAPIKey
	}
	user, ok := reqctx.GetUserPrincipal(c, s.sessionStore)
	if !ok {
		return ""
	}
	switch user.Source {
	case reqctx.UserPrincipalSourceSession:
		return authorization.SourceSession
	case reqctx.UserPrincipalSourcePersonalAccessToken:
		if user.CanManagePersonalAccessTokens {
			return authorization.SourceManagingPAT
		}
		return authorization.SourcePAT
	default:
		return ""
	}
}

func (s *server) authorizeOperation(c fiber.Ctx, input authorization.Input) (bool, error) {
	if input.Operation == "" {
		input.Operation = routeOperation(c)
	}
	input.Source = s.authenticatedSource(c)
	ctx := c.Context()
	if input.OrgSlug != nil {
		ctx = authorization.WithDecisionScope(ctx)
	}
	allowed, err := s.authorizationService.Can(ctx, input)
	if err == nil && allowed {
		c.SetContext(ctx)
	}
	return allowed, err
}
