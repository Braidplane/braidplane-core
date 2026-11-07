package slack

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestAuthorizationURLRequestsOnlyIncomingWebhook(t *testing.T) {
	client := newClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("unexpected request")
		return nil, xerror.Unexpected()
	})
	raw := client.AuthorizationURL("state value")
	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "https", u.Scheme)
	assert.Equal(t, "slack.com", u.Host)
	assert.Equal(t, "/oauth/v2/authorize", u.Path)
	assert.Equal(t, "client-id", u.Query().Get("client_id"))
	assert.Empty(t, u.Query().Get("client_secret"))
	assert.Equal(t, "https://app.example.com/slack/callback", u.Query().Get("redirect_uri"))
	assert.Equal(t, "incoming-webhook", u.Query().Get("scope"))
	assert.Equal(t, "state value", u.Query().Get("state"))
}

func TestExchangeCodeReturnsInstallationWithoutLeakingSecrets(t *testing.T) {
	client := newClient(t, func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "slack.com", request.URL.Host)
		assert.Equal(t, "/api/oauth.v2.access", request.URL.Path)
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		form, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		assert.Equal(t, "client-id", form.Get("client_id"))
		assert.Equal(t, "secret-value", form.Get("client_secret"))
		assert.Equal(t, "code-value", form.Get("code"))
		return response(http.StatusOK, `{"ok":true,"access_token":"token-value","scope":"incoming-webhook","team":{"id":"T123","name":"Workspace"},"incoming_webhook":{"channel":"#alerts","channel_id":"C123","url":"https://hooks.slack.com/services/T123/B123/webhook-secret"}}`, nil), nil
	})

	install, err := client.ExchangeCode(context.Background(), "code-value")
	require.NoError(t, err)
	assert.Equal(t, Install{AccessToken: "token-value", Scope: "incoming-webhook", TeamID: "T123", TeamName: "Workspace", ChannelID: "C123", ChannelName: "#alerts", WebhookURL: "https://hooks.slack.com/services/T123/B123/webhook-secret"}, install)
}

func TestProviderErrorsAreClassifiedAndSafe(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		headers    http.Header
		body       string
		want       error
		retryAfter time.Duration
	}{
		{name: "invalid code", status: http.StatusOK, body: `{"ok":false,"error":"invalid_code"}`, want: ErrInvalidGrant},
		{name: "invalid client", status: http.StatusUnauthorized, body: `token-value private-response`, want: xerror.InvalidCredentials()},
		{name: "rate limited", status: http.StatusTooManyRequests, headers: http.Header{"Retry-After": {"7"}}, body: `token-value private-response`, want: ErrRateLimited, retryAfter: 7 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newClient(t, func(*http.Request) (*http.Response, error) { return response(tt.status, tt.body, tt.headers), nil })
			_, err := client.ExchangeCode(t.Context(), "code-value")
			require.Error(t, err)
			require.ErrorIs(t, err, tt.want)
			assert.NotContains(t, err.Error(), "token-value")
			assert.NotContains(t, err.Error(), "private-response")
			var rateLimit *RateLimitError
			if xerror.As(err, &rateLimit) {
				assert.Equal(t, tt.retryAfter, rateLimit.RetryAfter)
			}
		})
	}
}

func TestSendWebhookValidatesDestinationAndDoesNotFollowRedirects(t *testing.T) {
	called := false
	client := newClient(t, func(request *http.Request) (*http.Response, error) {
		called = true
		assert.Equal(t, "hooks.slack.com", request.URL.Host)
		assert.Equal(t, "/services/T123/B123/webhook-secret", request.URL.Path)
		assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{"text":"hello"}`, string(body))
		return response(http.StatusOK, "ok", nil), nil
	})
	require.NoError(t, client.SendWebhook(t.Context(), "https://hooks.slack.com/services/T123/B123/webhook-secret", "hello"))
	assert.True(t, called)

	called = false
	err := client.SendWebhook(t.Context(), "https://hooks.slack.com/services/T123/B123/webhook-secret?redirect=https://evil.example", "hello")
	require.Error(t, err)
	assert.False(t, called)

	client = newClient(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusFound, "", http.Header{"Location": {"https://evil.example"}}), nil
	})
	err = client.SendWebhook(t.Context(), "https://hooks.slack.com/services/T123/B123/webhook-secret", "hello")
	require.Error(t, err)
	assert.True(t, xerror.Is(err, xerror.Unavailable(xerror.DataSource)))
}

func TestRevokeUsesAuthorizationAndAcceptsAlreadyRevokedToken(t *testing.T) {
	client := newClient(t, func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "slack.com", request.URL.Host)
		assert.Equal(t, "/api/auth.revoke", request.URL.Path)
		assert.Equal(t, "Bearer token-value", request.Header.Get("Authorization"))
		return response(http.StatusOK, `{"ok":false,"error":"invalid_auth"}`, nil), nil
	})
	require.NoError(t, client.Revoke(t.Context(), "token-value"))
}

func TestWebhookEscapesSlackMentionsAndLinks(t *testing.T) {
	client := newClient(t, func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{"text":"&lt;!channel&gt; &amp; &lt;https://example.com|link&gt;"}`, string(body))
		return response(http.StatusOK, "ok", nil), nil
	})
	require.NoError(t, client.SendWebhook(t.Context(), "https://hooks.slack.com/services/T123/B123/webhook-secret", "<!channel> & <https://example.com|link>"))
}

func TestRetryAfterBounds(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  time.Duration
	}{
		{"42", 42 * time.Second},
		{"-1", 0},
		{"invalid", 0},
		{"9223372036854775807", time.Duration(1<<63 - 1)},
	} {
		t.Run(tt.value, func(t *testing.T) { assert.Equal(t, tt.want, retryAfter(tt.value)) })
	}
}

func TestNewRejectsInvalidRedirectURL(t *testing.T) {
	_, err := New(Config{ClientID: "client", ClientSecret: "secret", RedirectURL: "http://app.example.com/callback"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret")
}

func TestProviderTransportErrorsDoNotLeakCredentials(t *testing.T) {
	secretURL := "https://hooks.slack.com/services/T123/B123/webhook-secret"
	client := newClient(t, func(request *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Post", URL: request.URL.String(), Err: xerror.Unexpected().Detail("code-value token-value secret-value")}
	})
	err := client.SendWebhook(t.Context(), secretURL, "hello")
	require.Error(t, err)
	for _, secret := range []string{"webhook-secret", "code-value", "token-value", "secret-value"} {
		assert.NotContains(t, fmt.Sprintf("%+v", err), secret)
	}

	_, err = client.ExchangeCode(t.Context(), "code-value")
	require.Error(t, err)
	for _, secret := range []string{"code-value", "token-value", "secret-value"} {
		assert.NotContains(t, err.Error(), secret)
	}
}

func TestExchangeCodeRejectsIncompleteInstallation(t *testing.T) {
	client := newClient(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"ok":true,"access_token":"token-value","scope":"chat:write","team":{"id":""},"incoming_webhook":{"channel_id":"","url":"https://hooks.slack.com/services/T123/B123/webhook-secret"}}`, nil), nil
	})
	_, err := client.ExchangeCode(t.Context(), "code-value")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "token-value")
}

func TestNewBoundsSuppliedClientTimeout(t *testing.T) {
	client, err := New(Config{
		ClientID:     "client",
		ClientSecret: "secret",
		RedirectURL:  "https://app.example.com/callback",
		HTTPClient:   &http.Client{},
	})
	require.NoError(t, err)
	assert.Equal(t, 10*time.Second, client.client.Timeout)
}

func TestNewWebhookClientSendsWithoutOAuthSettings(t *testing.T) {
	called := false
	client := NewWebhookClient(&http.Client{Transport: roundTripper(func(request *http.Request) (*http.Response, error) {
		called = true
		assert.Equal(t, "hooks.slack.com", request.URL.Host)
		return response(http.StatusOK, "ok", nil), nil
	})})
	require.NoError(t, client.SendWebhook(t.Context(), "https://hooks.slack.com/services/T123/B123/webhook-secret", "hello"))
	assert.True(t, called)
}

func newClient(t *testing.T, transport roundTripper) *Client {
	t.Helper()
	client, err := New(Config{
		ClientID:     "client-id",
		ClientSecret: "secret-value",
		RedirectURL:  "https://app.example.com/slack/callback",
		HTTPClient:   &http.Client{Transport: transport},
	})
	require.NoError(t, err)
	return client
}

func response(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}
