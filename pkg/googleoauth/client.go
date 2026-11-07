// Package googleoauth implements Google's provider-owned OAuth2 protocol boundary.
package googleoauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	defaultAuthorizationURL = "https://accounts.google.com/o/oauth2/v2/auth"
	// #nosec G101 -- Public OAuth endpoint, not credential material.
	defaultTokenURL    = "https://oauth2.googleapis.com/token"
	defaultRevokeURL   = "https://oauth2.googleapis.com/revoke"
	defaultUserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
	maxTokenLifetime   = 24 * time.Hour
)

var ErrInvalidGrant = xerror.New(xerror.KindInvalidInput, "GOOGLE_INVALID_GRANT", "Google OAuth invalid grant")

type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
}

type AuthorizationURLParams struct {
	State          string
	Scopes         []string
	OptionalScopes []string
}

type TokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       []string
	Subject      string
	Email        string
	Name         string
}

type Client struct {
	clientID, clientSecret, redirectURI                string
	authorizationURL, tokenURL, revokeURL, userInfoURL string
	client                                             *http.Client
}

func New(config Config) (*Client, error) {
	id, secret, redirect := strings.TrimSpace(config.ClientID), strings.TrimSpace(config.ClientSecret), strings.TrimSpace(config.RedirectURI)
	if id == "" || secret == "" || redirect == "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("Google OAuth client ID, client secret, and redirect URI are required")
	}
	parsed, err := url.Parse(redirect)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("Google OAuth redirect URI must be an absolute HTTP URL")
	}
	return &Client{
		clientID: id, clientSecret: secret, redirectURI: redirect,
		authorizationURL: defaultAuthorizationURL, tokenURL: defaultTokenURL,
		revokeURL: defaultRevokeURL, userInfoURL: defaultUserInfoURL,
		client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func (c *Client) AuthorizationURL(ctx context.Context, params AuthorizationURLParams) (string, error) {
	u, err := url.Parse(c.authorizationURL)
	if err != nil {
		logging.LogError(ctx, "build Google OAuth authorization URL", err, c.clientSecret, params.State)
		return "", xerror.Unexpected().Detail("Google OAuth authorization endpoint is invalid")
	}
	q := u.Query()
	q.Set("client_id", c.clientID)
	q.Set("redirect_uri", c.redirectURI)
	q.Set("response_type", "code")
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("state", params.State)
	scopes := append(append([]string(nil), params.Scopes...), params.OptionalScopes...)
	q.Set("scope", strings.Join(scopes, " "))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (c *Client) ExchangeCode(ctx context.Context, code string, requiredScopes []string) (TokenSet, error) {
	return c.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.redirectURI}}, true, nil, requiredScopes)
}

func (c *Client) Refresh(ctx context.Context, refreshToken string, grantedScopes, requiredScopes []string) (TokenSet, error) {
	return c.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}, false, grantedScopes, requiredScopes)
}

func (c *Client) Revoke(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	form := url.Values{"token": {token}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		logging.LogError(ctx, "create Google OAuth revoke request", err, c.clientSecret, token)
		return xerror.Unexpected().Detail("Google OAuth revoke request could not be created")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		logging.LogError(ctx, "Google OAuth revoke request failed", err, c.clientSecret, token)
		return xerror.Unavailable(xerror.DataSource).Detail("Google OAuth provider request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return providerError(resp.StatusCode, resp.Body)
	}
	return nil
}

func (c *Client) exchange(ctx context.Context, form url.Values, requireRefresh bool, grantedScopes, requiredScopes []string) (TokenSet, error) {
	if len(requiredScopes) == 0 {
		return TokenSet{}, xerror.InvalidInput(xerror.DataSource).Detail("Google OAuth required scopes must be specified")
	}
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		logging.LogError(ctx, "create Google OAuth token request", err, c.clientSecret, form.Get("code"), form.Get("refresh_token"))
		return TokenSet{}, xerror.Unexpected().Detail("Google OAuth token request could not be created")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		logging.LogError(ctx, "Google OAuth token request failed", err, c.clientSecret, form.Get("code"), form.Get("refresh_token"))
		return TokenSet{}, xerror.Unavailable(xerror.DataSource).Detail("Google OAuth provider request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return TokenSet{}, providerError(resp.StatusCode, resp.Body)
	}
	var token tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&token); err != nil {
		logging.LogError(ctx, "decode Google OAuth token response", err, c.clientSecret, token.AccessToken, token.RefreshToken, form.Get("code"), form.Get("refresh_token"))
		return TokenSet{}, xerror.Unavailable(xerror.DataSource).Detail("Google OAuth provider returned an invalid response")
	}
	if !strings.EqualFold(strings.TrimSpace(token.TokenType), "bearer") || token.ExpiresIn <= 0 || token.ExpiresIn > int64(maxTokenLifetime/time.Second) {
		return TokenSet{}, xerror.Unavailable(xerror.DataSource).Detail("Google OAuth provider returned invalid token metadata")
	}
	scopes := strings.Fields(token.Scope)
	// Refresh responses may omit unchanged scopes; only reuse the stored grant.
	if !requireRefresh && len(scopes) == 0 {
		scopes = append([]string(nil), grantedScopes...)
	}
	if strings.TrimSpace(token.AccessToken) == "" || (requireRefresh && strings.TrimSpace(token.RefreshToken) == "") || !hasRequiredScopes(scopes, requiredScopes) {
		return TokenSet{}, xerror.Unavailable(xerror.DataSource).Detail("Google OAuth response did not include required token material")
	}
	set := TokenSet{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, ExpiresAt: time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second), Scopes: scopes}
	if err := c.populateIdentity(ctx, &set); err != nil {
		return TokenSet{}, err
	}
	return set, nil
}

func (c *Client) populateIdentity(ctx context.Context, set *TokenSet) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.userInfoURL, nil)
	if err != nil {
		logging.LogError(ctx, "create Google identity request", err, set.AccessToken, set.RefreshToken)
		return xerror.Unavailable(xerror.DataSource).Detail("Google identity provider request failed")
	}
	req.Header.Set("Authorization", "Bearer "+set.AccessToken)
	resp, err := c.client.Do(req)
	if err != nil {
		logging.LogError(ctx, "Google identity request failed", err, set.AccessToken, set.RefreshToken)
		return xerror.Unavailable(xerror.DataSource).Detail("Google identity provider request failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return xerror.InvalidCredentials().Detail("Google identity provider rejected the access token")
	}
	var info userInfoResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&info); err != nil {
		logging.LogError(ctx, "decode Google identity response", err, set.AccessToken, set.RefreshToken)
		return xerror.Unavailable(xerror.DataSource).Detail("Google identity provider returned an invalid response")
	}
	if strings.TrimSpace(info.Subject) == "" || strings.TrimSpace(info.Email) == "" || !info.EmailVerified {
		return xerror.InvalidCredentials().Detail("Google identity provider returned incomplete identity")
	}
	set.Subject, set.Email, set.Name = info.Subject, info.Email, info.Name
	return nil
}

func hasRequiredScopes(scopes, required []string) bool {
	got := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		got[strings.TrimSpace(scope)] = true
	}
	for _, scope := range required {
		if scope == "email" && got["https://www.googleapis.com/auth/userinfo.email"] {
			continue
		}
		if strings.TrimSpace(scope) == "" || !got[scope] {
			return false
		}
	}
	return true
}

func providerError(status int, body io.Reader) error {
	var response struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(body, 64*1024)).Decode(&response)
	if response.Error == "invalid_grant" {
		return ErrInvalidGrant
	}
	switch status {
	case http.StatusBadRequest:
		return xerror.InvalidInput(xerror.DataSource).Detail("Google OAuth provider rejected request")
	case http.StatusUnauthorized:
		return xerror.InvalidCredentials().Detail("Google OAuth provider rejected client credentials")
	case http.StatusForbidden:
		return xerror.Forbidden(xerror.DataSource).Detail("Google OAuth provider forbids request")
	default:
		return xerror.Unavailable(xerror.DataSource).Detail("Google OAuth provider request failed")
	}
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}
type userInfoResponse struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
}
