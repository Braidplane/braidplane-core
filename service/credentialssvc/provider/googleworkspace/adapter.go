// Package googleworkspace owns Google Workspace consent plans and OAuth grants.
package googleworkspace

import (
	"context"
	"slices"
	"strings"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/googleoauth"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

const SelectedFiles = workspace.SelectedFiles

const driveFile = workspace.FileScope

type Config = googleoauth.Config

type Adapter struct{ client *googleoauth.Client }

func New(config Config) (*Adapter, error) {
	client, err := googleoauth.New(config)
	if err != nil {
		return nil, err
	}
	return &Adapter{client: client}, nil
}

func (*Adapter) OAuthScopePlanForInstall(params credentialssvc.OAuthScopePlanParams) credentialssvc.OAuthScopePlan {
	if params.AccessMode != "" && params.AccessMode != SelectedFiles {
		return credentialssvc.OAuthScopePlan{}
	}
	return credentialssvc.OAuthScopePlan{Required: []string{"openid", "email", driveFile}}
}

func (a *Adapter) AuthorizationURL(ctx context.Context, params credentialssvc.AuthorizationURLParams) (string, error) {
	if len(params.OptionalScopes) != 0 || !a.isConsentPlan(params.Scopes) {
		return "", credentialssvc.ErrInvalidInput.Detail("Google Workspace requires a supported file access consent plan")
	}
	return a.client.AuthorizationURL(ctx, googleoauth.AuthorizationURLParams{State: params.State, Scopes: params.Scopes})
}

func (a *Adapter) ExchangeCode(ctx context.Context, params credentialssvc.ExchangeCodeParams) (credentialssvc.OAuthTokenSet, error) {
	if !a.isConsentPlan(params.RequestedScopes) {
		return credentialssvc.OAuthTokenSet{}, credentialssvc.ErrInvalidInput.Detail("Google Workspace install has no supported consent plan")
	}
	return tokenSet(a.client.ExchangeCode(ctx, params.Code, params.RequestedScopes))
}

func (a *Adapter) Refresh(ctx context.Context, params credentialssvc.RefreshParams) (credentialssvc.OAuthTokenSet, error) {
	if !validGrantScopes(params.Scopes) {
		return credentialssvc.OAuthTokenSet{}, credentialssvc.ErrInvalidInput.Detail("Google Workspace grant has incomplete scopes")
	}
	// A refresh cannot choose a new access mode or repair a partial grant with broader scopes.
	return tokenSet(a.client.Refresh(ctx, params.RefreshToken, params.Scopes, params.Scopes))
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

func (a *Adapter) isConsentPlan(scopes []string) bool {
	return slices.Equal(scopes, a.OAuthScopePlanForInstall(credentialssvc.OAuthScopePlanParams{AccessMode: SelectedFiles}).Required)
}

func validGrantScopes(scopes []string) bool {
	identity := slices.Contains(scopes, "openid") &&
		(slices.Contains(scopes, "email") || slices.Contains(scopes, "https://www.googleapis.com/auth/userinfo.email"))
	return identity && workspace.ValidFileScopes(scopes)
}

func tokenSet(set googleoauth.TokenSet, err error) (credentialssvc.OAuthTokenSet, error) {
	if xerror.Is(err, googleoauth.ErrInvalidGrant) {
		return credentialssvc.OAuthTokenSet{}, credentialssvc.ErrProviderInvalidGrant
	}
	if err != nil {
		return credentialssvc.OAuthTokenSet{}, err
	}
	if !workspace.ValidFileScopes(set.Scopes) {
		return credentialssvc.OAuthTokenSet{}, credentialssvc.ErrInvalidInput.Detail("Reconnect Google Workspace with selected-file access")
	}
	return credentialssvc.OAuthTokenSet{AccessToken: set.AccessToken, RefreshToken: set.RefreshToken, ExpiresAt: set.ExpiresAt, Scopes: append([]string(nil), set.Scopes...), Account: credentialssvc.ProviderAccount{ID: set.Subject, Email: set.Email, Name: set.Name}}, nil
}

var _ credentialssvc.ContextualScopePlanningProvider = (*Adapter)(nil)
