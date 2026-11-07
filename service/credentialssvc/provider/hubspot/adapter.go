// Package hubspot implements the provider-owned OAuth2 exchange boundary for HubSpot.
package hubspot

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/hubspot/oauth"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

// Config is the deployment-owned OAuth client configuration.
type Config = oauth.Config

// Adapter exchanges HubSpot grants without exposing provider payloads outside this boundary.
type Adapter struct {
	client *oauth.Client
}

// New validates deployment-owned provider configuration and constructs an adapter.
func New(config Config) (*Adapter, error) {
	client, err := oauth.New(config)
	if err != nil {
		return nil, err
	}
	return &Adapter{client: client}, nil
}

// OAuthScopePlan returns HubSpot's mandatory OAuth scope and least-privilege read plan.
func (a *Adapter) OAuthScopePlan() credentialssvc.OAuthScopePlan {
	return a.OAuthScopePlanForInstall(credentialssvc.OAuthScopePlanParams{})
}

// OAuthScopePlanForInstall includes write scopes only for an explicitly writable data source.
func (a *Adapter) OAuthScopePlanForInstall(params credentialssvc.OAuthScopePlanParams) credentialssvc.OAuthScopePlan {
	plan := a.client.ScopePlan()
	optional := make([]string, 0, len(plan.Optional))
	for _, scope := range plan.Optional {
		if !params.IncludeWriteScopes && strings.HasSuffix(scope, ".write") {
			continue
		}
		optional = append(optional, scope)
	}
	return credentialssvc.OAuthScopePlan{Required: plan.Required, Optional: optional}
}

// AuthorizationURL builds the HubSpot consent URL with provider-owned required and optional scopes.
func (a *Adapter) AuthorizationURL(_ context.Context, params credentialssvc.AuthorizationURLParams) (string, error) {
	return a.client.AuthorizationURL(oauth.AuthorizationURLParams{State: params.State, Scopes: params.Scopes, OptionalScopes: params.OptionalScopes})
}

// ExchangeCode exchanges a one-time authorization code for a safe normalized token set.
func (a *Adapter) ExchangeCode(ctx context.Context, params credentialssvc.ExchangeCodeParams) (credentialssvc.OAuthTokenSet, error) {
	tokenSet, err := a.client.ExchangeCode(ctx, params.Code)
	return credentialTokenSet(tokenSet, err)
}

// Refresh exchanges an existing refresh token while preserving HubSpot's actual granted scopes.
func (a *Adapter) Refresh(ctx context.Context, params credentialssvc.RefreshParams) (credentialssvc.OAuthTokenSet, error) {
	tokenSet, err := a.client.Refresh(ctx, params.RefreshToken)
	return credentialTokenSet(tokenSet, err)
}

// Revoke revokes a HubSpot refresh token when one is available.
func (a *Adapter) Revoke(ctx context.Context, params credentialssvc.RevokeProviderGrantParams) error {
	err := a.client.Revoke(ctx, params.RefreshToken)
	if xerror.Is(err, oauth.ErrInvalidGrant) {
		return credentialssvc.ErrProviderInvalidGrant
	}
	return err
}

// credentialTokenSet maps neutral provider tokens into credential-lifecycle types.
func credentialTokenSet(tokenSet oauth.TokenSet, err error) (credentialssvc.OAuthTokenSet, error) {
	if xerror.Is(err, oauth.ErrInvalidGrant) {
		return credentialssvc.OAuthTokenSet{}, credentialssvc.ErrProviderInvalidGrant
	}
	if err != nil {
		return credentialssvc.OAuthTokenSet{}, err
	}
	return credentialssvc.OAuthTokenSet{
		AccessToken:  tokenSet.AccessToken,
		RefreshToken: tokenSet.RefreshToken,
		ExpiresAt:    tokenSet.ExpiresAt,
		Scopes:       append([]string(nil), tokenSet.Scopes...),
		Account:      credentialssvc.ProviderAccount{ID: tokenSet.AccountID},
	}, nil
}

var (
	_ credentialssvc.ScopePlanningProvider           = (*Adapter)(nil)
	_ credentialssvc.ContextualScopePlanningProvider = (*Adapter)(nil)
)
