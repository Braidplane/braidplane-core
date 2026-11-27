package mcpauthsvc

import (
	"context"
	"slices"
	"strconv"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/mcpauth"
	"github.com/Braidplane/braidplane-core/service/authorization"
)

// AuthenticateDelegatedRequest returns the principal and the context to use for every
// authorization check and database operation on this request.
func (s *TokenService) AuthenticateDelegatedRequest(ctx context.Context, token, resource string, org slug.Slug) (context.Context, authorization.DelegatedPrincipal, error) {
	hash, ok := credentialHash(token, accessCredential)
	if !ok || org == "" || !s.deployment.AcceptsResource(resource) || authorization.DelegatedSource(ctx) != "" {
		return ctx, authorization.DelegatedPrincipal{}, ErrInvalidAccessToken
	}
	issuance := s.issuance("")
	type authenticated struct {
		principal authorization.DelegatedPrincipal
		scopeKeys []string
	}
	read := func(ctx context.Context, db postgres.DBTX) (authenticated, error) {
		user, err := mcpauth.AuthenticateAccessToken(ctx, db, issuance.Issuer, issuance.Audience, hash)
		if err == nil {
			if user.Organization != org || user.MemberID == 0 {
				return authenticated{}, ErrInvalidAccessToken
			}
			return authenticated{
				principal: authorization.DelegatedPrincipal{Organization: org, User: user.User, PrincipalID: "member:" + strconv.FormatInt(int64(user.MemberID), 10)},
				scopeKeys: user.ScopeKeys,
			}, nil
		}
		if !xerror.Is(err, mcpauth.ErrNotFound) {
			return authenticated{}, err
		}
		machine, err := mcpauth.AuthenticateMachineAccessToken(ctx, db, issuance, org, hash)
		if xerror.Is(err, mcpauth.ErrNotFound) {
			return authenticated{}, ErrInvalidAccessToken
		}
		if err != nil {
			return authenticated{}, err
		}
		return authenticated{
			principal: authorization.DelegatedPrincipal{
				Organization: org, APIKey: machine.APIKey, PrincipalID: "api_key:" + strconv.FormatInt(machine.APIKeyID, 10),
			},
			scopeKeys: machine.ScopeKeys,
		}, nil
	}
	identity, err := postgres.InRead(postgres.WithoutTransactionChecks(ctx), s.txm, read)
	if err != nil {
		return ctx, authorization.DelegatedPrincipal{}, err
	}
	return authorization.WithDelegatedPrincipal(ctx, identity.principal, identity.scopeKeys, func(ctx context.Context, db postgres.DBTX) error {
		current, err := read(ctx, db)
		if xerror.Is(err, ErrInvalidAccessToken) || (err == nil && (current.principal != identity.principal || !slices.Equal(current.scopeKeys, identity.scopeKeys))) {
			return xerror.New(xerror.KindForbidden, "MCP_DELEGATION_CHANGED", "Delegated authorization changed before the operation")
		}
		return err
	}), identity.principal, nil
}
