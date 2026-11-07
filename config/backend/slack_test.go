package backend

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSlackOAuthConfiguration(t *testing.T) {
	valid := SlackConfig{OAuthClientID: "test-client", OAuthClientSecret: "test-secret", OAuthRedirectURI: "https://app.example.com/provider-connectors/slack/callback"}
	require.NoError(t, validateSlackOAuth(SlackConfig{}, "staging"))
	require.NoError(t, validateSlackOAuth(valid, "staging"))
	whitespace := valid
	whitespace.OAuthRedirectURI = " " + valid.OAuthRedirectURI + " "
	require.NoError(t, validateSlackOAuth(whitespace, "staging"))
	for _, mutate := range []func(*SlackConfig){
		func(c *SlackConfig) { c.OAuthClientSecret = "" },
		func(c *SlackConfig) { c.OAuthRedirectURI = "http://localhost/provider-connectors/slack/callback" },
		func(c *SlackConfig) { c.OAuthRedirectURI = "https://app.example.com/wrong" },
		func(c *SlackConfig) { c.OAuthRedirectURI += "?secret=no" },
		func(c *SlackConfig) { c.OAuthRedirectURI = "https://localhost/provider-connectors/slack/callback" },
	} {
		c := valid
		mutate(&c)
		require.Error(t, validateSlackOAuth(c, "staging"))
	}
}
