// Package stripe adapts a mode-specific Stripe App to the shared credential lifecycle.
package stripe

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/stripeoauth"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

type Adapter struct {
	client *stripeoauth.Client
}

func New(config stripeoauth.Config) (*Adapter, error) {
	client, err := stripeoauth.New(config)
	if err != nil {
		return nil, err
	}
	return &Adapter{client: client}, nil
}

func (*Adapter) OAuthScopePlan() credentialssvc.OAuthScopePlan {
	return credentialssvc.OAuthScopePlan{Required: []string{stripeoauth.Scope}}
}

func (a *Adapter) AuthorizationURL(_ context.Context, p credentialssvc.AuthorizationURLParams) (string, error) {
	return a.client.AuthorizationURL(p.State)
}

func (a *Adapter) ExchangeCode(ctx context.Context, p credentialssvc.ExchangeCodeParams) (credentialssvc.OAuthTokenSet, error) {
	return credentialTokens(a.client.ExchangeCode(ctx, p.Code))
}

func (a *Adapter) Refresh(ctx context.Context, p credentialssvc.RefreshParams) (credentialssvc.OAuthTokenSet, error) {
	return credentialTokens(a.client.Refresh(ctx, p.RefreshToken))
}

func credentialTokens(tokens stripeoauth.TokenSet, err error) (credentialssvc.OAuthTokenSet, error) {
	if xerror.Is(err, stripeoauth.ErrInvalidGrant) {
		return credentialssvc.OAuthTokenSet{}, credentialssvc.ErrProviderInvalidGrant
	}
	if err != nil {
		return credentialssvc.OAuthTokenSet{}, err
	}
	return credentialssvc.OAuthTokenSet{AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, ExpiresAt: time.Now().UTC().Add(time.Hour), Scopes: []string{tokens.Scope}, Account: credentialssvc.ProviderAccount{ID: tokens.AccountID}}, nil
}

func (*Adapter) Revoke(context.Context, credentialssvc.RevokeProviderGrantParams) error {
	// Stripe Apps are uninstalled in the account's Dashboard. Core disconnects the stored grant.
	return nil
}

var _ credentialssvc.ScopePlanningProvider = (*Adapter)(nil)
