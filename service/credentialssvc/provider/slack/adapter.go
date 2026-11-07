package slack

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/slack"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
)

type Config = slack.Config

type Adapter struct{ client *slack.Client }

func New(config Config) (*Adapter, error) {
	client, err := slack.New(config)
	if err != nil {
		return nil, err
	}
	return &Adapter{client: client}, nil
}

func (*Adapter) OAuthScopePlan() credentialssvc.OAuthScopePlan {
	return credentialssvc.OAuthScopePlan{Required: []string{"incoming-webhook"}}
}

func (a *Adapter) AuthorizationURL(_ context.Context, params credentialssvc.AuthorizationURLParams) (string, error) {
	return a.client.AuthorizationURL(params.State), nil
}

func (a *Adapter) ExchangeCode(ctx context.Context, params credentialssvc.ExchangeCodeParams) (credentialssvc.OAuthTokenSet, error) {
	install, err := a.client.ExchangeCode(ctx, params.Code)
	if xerror.Is(err, slack.ErrInvalidGrant) {
		return credentialssvc.OAuthTokenSet{}, credentialssvc.ErrProviderInvalidGrant
	}
	if err != nil {
		return credentialssvc.OAuthTokenSet{}, err
	}
	return credentialssvc.OAuthTokenSet{
		AccessToken: install.AccessToken, SlackWebhookURL: install.WebhookURL,
		Scopes:  strings.Split(install.Scope, ","),
		Account: credentialssvc.ProviderAccount{ID: install.TeamID, Name: install.TeamName, ChannelID: install.ChannelID, ChannelName: install.ChannelName},
	}, nil
}

func (*Adapter) Refresh(context.Context, credentialssvc.RefreshParams) (credentialssvc.OAuthTokenSet, error) {
	return credentialssvc.OAuthTokenSet{}, credentialssvc.ErrProviderInvalidGrant.Detail("Reconnect Slack to replace this installation")
}

func (*Adapter) Revoke(context.Context, credentialssvc.RevokeProviderGrantParams) error {
	// Slack tokens may be shared by channel installations; disconnect only tombstones this local grant.
	return nil
}

var _ credentialssvc.ScopePlanningProvider = (*Adapter)(nil)
