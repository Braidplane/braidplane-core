// Package slack implements Slack's OAuth and incoming-webhook protocol boundary.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	authorizationURL = "https://slack.com/oauth/v2/authorize"
	accessURL        = "https://slack.com/api/oauth.v2.access"
	revokeURL        = "https://slack.com/api/auth.revoke"
	webhookHost      = "hooks.slack.com"
	maxResponseBytes = 64 * 1024
)

var (
	// ErrInvalidGrant identifies a rejected or expired OAuth authorization code.
	ErrInvalidGrant = xerror.New(xerror.KindInvalidInput, "SLACK_INVALID_GRANT", "Slack OAuth grant was rejected")
	// ErrRateLimited identifies a Slack response that may be retried after the returned delay.
	ErrRateLimited = xerror.New(xerror.KindUnavailable, "SLACK_RATE_LIMITED", "Slack request was rate limited")
)

// Config contains deployment-owned Slack OAuth settings.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	HTTPClient   *http.Client
}

// Install contains the fields from a successful incoming-webhook OAuth installation.
// WebhookURL is a credential and must only be persisted in secret storage.
type Install struct {
	AccessToken string
	Scope       string
	TeamID      string
	TeamName    string
	ChannelID   string
	ChannelName string
	WebhookURL  string
}

// RateLimitError is safe to return to callers and preserves Slack's retry instruction.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string { return ErrRateLimited.Error() }

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// WebhookClient sends messages to Slack incoming-webhook URLs.
type WebhookClient struct {
	client *http.Client
}

// NewWebhookClient constructs a bounded client that never follows redirects.
func NewWebhookClient(client *http.Client) *WebhookClient {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	clientCopy := *client
	if clientCopy.Timeout <= 0 {
		clientCopy.Timeout = 10 * time.Second
	}
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &WebhookClient{client: &clientCopy}
}

// Client performs Slack OAuth and revocation requests.
type Client struct {
	*WebhookClient
	clientID     string
	clientSecret string
	redirectURL  string
}

// New validates deployment configuration and creates a client that never follows redirects.
func New(config Config) (*Client, error) {
	clientID := strings.TrimSpace(config.ClientID)
	clientSecret := strings.TrimSpace(config.ClientSecret)
	redirectURL := strings.TrimSpace(config.RedirectURL)
	if clientID == "" || clientSecret == "" || redirectURL == "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("Slack client ID, client secret, and redirect URL are required")
	}
	redirect, err := url.Parse(redirectURL)
	if err != nil || redirect.Scheme != "https" || redirect.Host == "" || redirect.User != nil || redirect.Fragment != "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("Slack redirect URL must be an absolute HTTPS URL")
	}

	return &Client{WebhookClient: NewWebhookClient(config.HTTPClient), clientID: clientID, clientSecret: clientSecret, redirectURL: redirectURL}, nil
}

// AuthorizationURL builds a consent URL requesting only Slack's incoming-webhook scope.
func (c *Client) AuthorizationURL(state string) string {
	u, _ := url.Parse(authorizationURL)
	query := u.Query()
	query.Set("client_id", c.clientID)
	query.Set("redirect_uri", c.redirectURL)
	query.Set("scope", "incoming-webhook")
	query.Set("state", state)
	u.RawQuery = query.Encode()
	return u.String()
}

// ExchangeCode exchanges an OAuth authorization code for its Slack installation data.
func (c *Client) ExchangeCode(ctx context.Context, code string) (Install, error) {
	if strings.TrimSpace(code) == "" {
		return Install{}, ErrInvalidGrant
	}
	form := url.Values{
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
		"code":          {code},
		"redirect_uri":  {c.redirectURL},
	}
	response, err := c.postForm(ctx, accessURL, form, "")
	if err != nil {
		return Install{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return Install{}, providerError(response.StatusCode, response.Header)
	}

	var payload oauthResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&payload); err != nil {
		logging.LogError(ctx, "decode Slack OAuth response", err, c.clientSecret, code, payload.AccessToken, payload.IncomingWebhook.URL)
		return Install{}, xerror.Unavailable(xerror.DataSource).Detail("Slack OAuth provider returned an invalid response")
	}
	if !payload.OK {
		return Install{}, oauthError(payload.Error)
	}
	if strings.TrimSpace(payload.AccessToken) == "" || !hasIncomingWebhookScope(payload.Scope) || strings.TrimSpace(payload.Team.ID) == "" || strings.TrimSpace(payload.IncomingWebhook.ChannelID) == "" || strings.TrimSpace(payload.IncomingWebhook.URL) == "" {
		return Install{}, xerror.Unavailable(xerror.DataSource).Detail("Slack OAuth response omitted required installation data")
	}
	if err := validateWebhookURL(payload.IncomingWebhook.URL); err != nil {
		return Install{}, xerror.Unavailable(xerror.DataSource).Detail("Slack OAuth response contained an invalid webhook URL")
	}
	return Install{
		AccessToken: payload.AccessToken,
		Scope:       payload.Scope,
		TeamID:      payload.Team.ID,
		TeamName:    payload.Team.Name,
		ChannelID:   payload.IncomingWebhook.ChannelID,
		ChannelName: payload.IncomingWebhook.Channel,
		WebhookURL:  payload.IncomingWebhook.URL,
	}, nil
}

// SendWebhook posts plain text to a webhook returned by Slack's OAuth response.
func (c *WebhookClient) SendWebhook(ctx context.Context, webhookURL, text string) error {
	if err := validateWebhookURL(webhookURL); err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		Text string `json:"text"`
	}{Text: strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(req)
	if err != nil {
		logging.LogError(ctx, "Slack webhook request failed", err, webhookURL, text)
		return xerror.Unavailable(xerror.DataSource).Detail("Slack webhook request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return providerError(response.StatusCode, response.Header)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	return nil
}

// Revoke revokes an access token. Slack's invalid_auth response is already the desired state.
func (c *Client) Revoke(ctx context.Context, accessToken string) error {
	if strings.TrimSpace(accessToken) == "" {
		return nil
	}
	response, err := c.postForm(ctx, revokeURL, nil, accessToken)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return providerError(response.StatusCode, response.Header)
	}
	var payload struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&payload); err != nil {
		logging.LogError(ctx, "decode Slack revoke response", err, c.clientSecret, accessToken)
		return xerror.Unavailable(xerror.DataSource).Detail("Slack revoke provider returned an invalid response")
	}
	if payload.OK || payload.Error == "invalid_auth" {
		return nil
	}
	return oauthError(payload.Error)
}

func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values, authorization string) (*http.Response, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if authorization != "" {
		req.Header.Set("Authorization", "Bearer "+authorization)
	}
	response, err := c.client.Do(req)
	if err != nil {
		logging.LogError(ctx, "Slack provider request failed", err, c.clientSecret, authorization, form.Get("code"))
		return nil, xerror.Unavailable(xerror.DataSource).Detail("Slack provider request failed")
	}
	return response, nil
}

func oauthError(code string) error {
	switch strings.TrimSpace(code) {
	case "invalid_code", "invalid_grant":
		return ErrInvalidGrant
	case "invalid_client":
		return xerror.InvalidCredentials().Detail("Slack provider rejected client credentials")
	case "access_denied", "missing_scope":
		return xerror.Forbidden(xerror.DataSource).Detail("Slack provider denied request")
	default:
		return xerror.Unavailable(xerror.DataSource).Detail("Slack provider rejected request")
	}
}

func providerError(status int, header http.Header) error {
	if status == http.StatusTooManyRequests {
		return &RateLimitError{RetryAfter: retryAfter(header.Get("Retry-After"))}
	}
	switch status {
	case http.StatusBadRequest:
		return xerror.InvalidInput(xerror.DataSource).Detail("Slack provider rejected request")
	case http.StatusUnauthorized, http.StatusNotFound, http.StatusGone:
		return xerror.InvalidCredentials().Detail("Slack provider rejected credentials")
	case http.StatusForbidden:
		return xerror.Forbidden(xerror.DataSource).Detail("Slack provider denied request")
	default:
		return xerror.Unavailable(xerror.DataSource).Detail("Slack provider request failed")
	}
}

func retryAfter(value string) time.Duration {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds < 0 {
		return 0
	}
	const maxDuration = time.Duration(1<<63 - 1)
	if seconds > int64(maxDuration/time.Second) {
		return maxDuration
	}
	return time.Duration(seconds) * time.Second
}

func hasIncomingWebhookScope(scope string) bool {
	for _, item := range strings.Split(scope, ",") {
		if strings.TrimSpace(item) == "incoming-webhook" {
			return true
		}
	}
	return false
}

func validateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() != webhookHost || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return xerror.InvalidInput(xerror.DataSource).Detail("Slack webhook URL must be a documented HTTPS endpoint")
	}
	parts := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[0] != "services" || !validWebhookPart(parts[1], 'T') || !validWebhookPart(parts[2], 'B') || !validWebhookSecret(parts[3]) {
		return xerror.InvalidInput(xerror.DataSource).Detail("Slack webhook URL must be a documented HTTPS endpoint")
	}
	return nil
}

func validWebhookPart(part string, prefix byte) bool {
	return len(part) > 1 && part[0] == prefix && validWebhookSecret(part[1:])
}

func validWebhookSecret(part string) bool {
	if part == "" || strings.Contains(part, "%") {
		return false
	}
	for _, r := range part {
		if (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

type oauthResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error"`
	AccessToken string `json:"access_token"`
	Scope       string `json:"scope"`
	Team        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"team"`
	IncomingWebhook struct {
		Channel   string `json:"channel"`
		ChannelID string `json:"channel_id"`
		URL       string `json:"url"`
	} `json:"incoming_webhook"`
}
