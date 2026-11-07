// Package bigquery implements the provider-owned Google OAuth2 boundary for BigQuery.
package bigquery

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/googleoauth"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

type (
	Config  = googleoauth.Config
	Adapter struct{ client *googleoauth.Client }
)

func New(config Config) (*Adapter, error) {
	client, err := googleoauth.New(config)
	if err != nil {
		return nil, err
	}
	return &Adapter{client: client}, nil
}

func (a *Adapter) OAuthScopePlan() credentialssvc.OAuthScopePlan {
	return credentialssvc.OAuthScopePlan{Required: []string{"https://www.googleapis.com/auth/bigquery", "openid", "email"}}
}

func (a *Adapter) AuthorizationURL(ctx context.Context, params credentialssvc.AuthorizationURLParams) (string, error) {
	return a.client.AuthorizationURL(ctx, googleoauth.AuthorizationURLParams{State: params.State, Scopes: params.Scopes, OptionalScopes: params.OptionalScopes})
}

func (a *Adapter) ExchangeCode(ctx context.Context, params credentialssvc.ExchangeCodeParams) (credentialssvc.OAuthTokenSet, error) {
	return tokenSet(a.client.ExchangeCode(ctx, params.Code, a.OAuthScopePlan().Required))
}

func (a *Adapter) Refresh(ctx context.Context, params credentialssvc.RefreshParams) (credentialssvc.OAuthTokenSet, error) {
	return tokenSet(a.client.Refresh(ctx, params.RefreshToken, params.Scopes, a.OAuthScopePlan().Required))
}

func (a *Adapter) Revoke(ctx context.Context, params credentialssvc.RevokeProviderGrantParams) error {
	token := params.RefreshToken
	if strings.TrimSpace(token) == "" {
		token = params.AccessToken
	}
	err := a.client.Revoke(ctx, token)
	if xerror.Is(err, googleoauth.ErrInvalidGrant) {
		return credentialssvc.ErrProviderInvalidGrant
	}
	return err
}

func tokenSet(set googleoauth.TokenSet, err error) (credentialssvc.OAuthTokenSet, error) {
	if xerror.Is(err, googleoauth.ErrInvalidGrant) {
		return credentialssvc.OAuthTokenSet{}, credentialssvc.ErrProviderInvalidGrant
	}
	if err != nil {
		return credentialssvc.OAuthTokenSet{}, err
	}
	return credentialssvc.OAuthTokenSet{AccessToken: set.AccessToken, RefreshToken: set.RefreshToken, ExpiresAt: set.ExpiresAt, Scopes: append([]string(nil), set.Scopes...), Account: credentialssvc.ProviderAccount{ID: set.Subject, Email: set.Email, Name: set.Name}}, nil
}

var (
	_ credentialssvc.ProviderAdapter       = (*Adapter)(nil)
	_ credentialssvc.ScopePlanningProvider = (*Adapter)(nil)
)
