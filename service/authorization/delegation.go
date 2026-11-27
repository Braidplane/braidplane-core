package authorization

import (
	"context"
	"slices"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
)

// DelegatedPrincipal identifies the membership or key verified by the OAuth issuer.
type DelegatedPrincipal struct {
	Organization, User, APIKey slug.Slug
	PrincipalID                string
}

func (p DelegatedPrincipal) Source() Source {
	switch {
	case p.User != "" && p.APIKey == "":
		return SourceDelegatedUser
	case p.User == "" && p.APIKey != "":
		return SourceDelegatedMachine
	default:
		return ""
	}
}

type delegatedPrincipalKey struct{}

type delegatedIdentity struct {
	principal DelegatedPrincipal
	scopeKeys []string
}

// WithDelegatedPrincipal carries verified authentication and checks it again inside the protected database snapshot.
// Only the issuer's token verifier should bind a principal.
func WithDelegatedPrincipal(ctx context.Context, principal DelegatedPrincipal, scopeKeys []string, revalidate func(context.Context, postgres.DBTX) error) context.Context {
	if revalidate == nil {
		return ctx
	}
	ctx = WithDecisionScope(ctx)
	postgres.AddTransactionCheck(ctx, revalidate)
	return context.WithValue(ctx, delegatedPrincipalKey{}, delegatedIdentity{principal: principal, scopeKeys: slices.Clone(scopeKeys)})
}

func DelegatedSource(ctx context.Context) Source {
	identity, _ := ctx.Value(delegatedPrincipalKey{}).(delegatedIdentity)
	return identity.principal.Source()
}

// DelegatedOperationInScope checks the verified grant before dispatching to an operation.
// Callers still need Can for each action and target used by the operation.
func DelegatedOperationInScope(ctx context.Context, operation string) bool {
	identity, _ := ctx.Value(delegatedPrincipalKey{}).(delegatedIdentity)
	return identity.permitsOperation(operation)
}

func (identity delegatedIdentity) permitsOperation(operation string) bool {
	key := delegatedScopeKeys[identity.principal.Source()][operation]
	return key != "" && slices.Contains(identity.scopeKeys, key)
}

func (source Source) delegated() bool {
	return source == SourceDelegatedUser || source == SourceDelegatedMachine
}

func delegationMatchesInput(ctx context.Context, input Input) (DelegatedPrincipal, bool) {
	identity, delegated := ctx.Value(delegatedPrincipalKey{}).(delegatedIdentity)
	principal := identity.principal
	if !delegated || !input.Source.delegated() {
		return principal, !delegated && !input.Source.delegated()
	}
	return principal, identity.permitsOperation(input.Operation) && principal.Source() == input.Source && principal.PrincipalID != "" &&
		input.OrgSlug != nil && principal.Organization == *input.OrgSlug && principal.Organization != "" &&
		principal.User == input.UserSlug && principal.APIKey == input.APIKeySlug
}
