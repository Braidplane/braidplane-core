package stripeoauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func fixtureConfig(mode string) Config {
	return Config{Mode: mode, InstallURL: "https://marketplace.stripe.com/oauth/v2/authorize?client_id=fixture&install_signature=fixture_sig", DeveloperKey: "sk_" + mode + "_appfixture", RedirectURI: "https://core.example.test/provider-connectors/stripe_" + mode + "/callback"}
}

func TestAppsOAuthAuthorizationExchangeAndRefresh(t *testing.T) {
	for _, mode := range []string{"test", "live"} {
		t.Run(mode, func(t *testing.T) {
			app := fixtureConfig(mode)
			c, err := New(app)
			require.NoError(t, err)
			link, err := c.AuthorizationURL("state +/=")
			require.NoError(t, err)
			u, err := url.Parse(link)
			require.NoError(t, err)
			require.Equal(t, "state +/=", u.Query().Get("state"))
			require.Equal(t, "fixture_sig", u.Query().Get("install_signature"))
			require.Equal(t, app.RedirectURI, u.Query().Get("redirect_uri"))
			require.NotContains(t, link, app.DeveloperKey)
			calls := 0
			c.http.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, http.MethodPost, req.Method)
				require.Equal(t, "https://api.stripe.com/v1/oauth/token", req.URL.String())
				key, password, ok := req.BasicAuth()
				require.True(t, ok)
				require.Equal(t, app.DeveloperKey, key)
				require.Empty(t, password)
				require.NoError(t, req.ParseForm())
				refresh := "refresh_one"
				if calls == 1 {
					require.Equal(t, url.Values{"grant_type": {"authorization_code"}, "code": {"code_fixture"}}, req.PostForm)
				} else {
					require.Equal(t, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"refresh_one"}}, req.PostForm)
					refresh = "refresh_two"
				}
				body, err := json.Marshal(map[string]any{"access_token": "oauth_fixture", "refresh_token": refresh, "livemode": mode == "live", "stripe_user_id": "acct_fixture", "scope": "stripe_apps", "token_type": "bearer"})
				require.NoError(t, err)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			first, err := c.ExchangeCode(t.Context(), "code_fixture")
			require.NoError(t, err)
			require.Equal(t, "acct_fixture", first.AccountID)
			next, err := c.Refresh(t.Context(), first.RefreshToken)
			require.NoError(t, err)
			require.Equal(t, "refresh_two", next.RefreshToken)
			require.Equal(t, 2, calls)
		})
	}
}

func TestGrantErrorsAreBoundedAndRedacted(t *testing.T) {
	const sentinel = "private_error_fixture"
	const good = `{"access_token":"at","refresh_token":"rt","stripe_user_id":"acct_fixture","token_type":"bearer","scope":"stripe_apps","livemode":false}`
	for _, tc := range []struct {
		label  string
		status int
		body   string
		want   error
	}{
		{"invalid grant", 400, `{"error":"invalid_grant","error_description":"private_error_fixture"}`, ErrInvalidGrant},
		{"invalid nested grant", 400, `{"error":{"code":"invalid_grant","message":"private_error_fixture"}}`, ErrInvalidGrant},
		{"operator auth", 401, `{"error":"invalid_client","error_description":"private_error_fixture"}`, xerror.Unavailable(xerror.DataSource)},
		{"rate limit", 429, sentinel, xerror.Unavailable(xerror.DataSource)},
		{"redirect", 307, sentinel, xerror.Unavailable(xerror.DataSource)},
		{"oversize", 200, strings.Repeat(sentinel, 5000), xerror.Unavailable(xerror.DataSource)},
		{"wrong mode", 200, strings.Replace(good, "false", "true", 1), xerror.Unavailable(xerror.DataSource)},
		{"missing mode", 200, strings.Replace(good, `,"livemode":false`, "", 1), xerror.Unavailable(xerror.DataSource)},
		{"missing account", 200, strings.Replace(good, "acct_fixture", "", 1), xerror.Unavailable(xerror.DataSource)},
		{"wrong scope", 200, strings.Replace(good, "stripe_apps", "read_write", 1), xerror.Unavailable(xerror.DataSource)},
		{"missing rotation", 200, strings.Replace(good, `"rt"`, `""`, 1), xerror.Unavailable(xerror.DataSource)},
		{"invalid JSON", 200, sentinel, xerror.Unavailable(xerror.DataSource)},
	} {
		t.Run(tc.label, func(t *testing.T) {
			app := fixtureConfig("test")
			c, err := New(app)
			require.NoError(t, err)
			calls := 0
			c.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": {"https://example.invalid"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			_, err = c.Refresh(t.Context(), sentinel)
			require.ErrorIs(t, err, tc.want)
			require.NotContains(t, err.Error(), sentinel)
			require.NotContains(t, err.Error(), app.DeveloperKey)
			require.Equal(t, 1, calls)
		})
	}
}

func TestCancellation(t *testing.T) {
	c, err := New(fixtureConfig("test"))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	c.http.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
		cancel()
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	_, err = c.ExchangeCode(ctx, "code")
	require.ErrorIs(t, err, context.Canceled)
}

func TestConfigRejectsMismatches(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.Mode = "sandbox" },
		func(c *Config) { c.DeveloperKey = "sk_live_fixture" },
		func(c *Config) { c.DeveloperKey = "rk_test_fixture" },
		func(c *Config) { c.InstallURL = "https://example.invalid/oauth/v2/authorize?client_id=ca_123" },
		func(c *Config) { c.InstallURL = "https://marketplace.stripe.com/other?client_id=ca_123" },
		func(c *Config) { c.RedirectURI = "https://app.example.test/provider-connectors/stripe_live/callback" },
		func(c *Config) {
			c.Mode = "live"
			c.DeveloperKey = "sk_live_fixture"
			c.RedirectURI = "http://localhost/provider-connectors/stripe_live/callback"
		},
	} {
		c := fixtureConfig("test")
		change(&c)
		require.Error(t, c.Validate())
	}
}
