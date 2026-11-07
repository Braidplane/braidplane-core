// Package oauth implements HubSpot's provider-owned OAuth2 protocol boundary.
package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	defaultAPIBaseURL           = "https://api.hubapi.com"
	defaultAuthorizationBaseURL = "https://app.hubspot.com"
)

var (
	// ErrInvalidGrant identifies rejected authorization codes and refresh tokens.
	ErrInvalidGrant = xerror.New(xerror.KindInvalidInput, "HUBSPOT_INVALID_GRANT", "HubSpot OAuth invalid grant")
	scopePlan       = ScopePlan{
		Required: []string{"oauth", "crm.objects.contacts.read"},
		Optional: []string{
			"crm.objects.companies.read",
			"crm.objects.deals.read",
			"crm.objects.custom.read",
			"crm.objects.contacts.write",
			"crm.objects.companies.write",
			"crm.objects.deals.write",
			"crm.objects.custom.write",
		},
	}
)

// Config contains deployment-owned HubSpot OAuth client settings.
type Config struct {
	AllowPlaintext bool
	ClientID       string
	ClientSecret   string
	RedirectURI    string
	BaseURL        string
}

// ScopePlan describes the required and optional scopes for a HubSpot grant.
type ScopePlan struct {
	Required []string
	Optional []string
}

// AuthorizationURLParams contains the state and scopes for a consent request.
type AuthorizationURLParams struct {
	State          string
	Scopes         []string
	OptionalScopes []string
}

// TokenSet is HubSpot's normalized OAuth token response.
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       []string
	AccountID    string
}

// Client performs HubSpot consent, token, refresh, and revocation requests.
type Client struct {
	clientID             string
	clientSecret         string
	redirectURI          string
	apiBaseURL           string
	authorizationBaseURL string
	client               *http.Client
}

// New validates deployment-owned provider configuration and constructs an OAuth client.
func New(config Config) (*Client, error) {
	clientID := strings.TrimSpace(config.ClientID)
	clientSecret := strings.TrimSpace(config.ClientSecret)
	redirectURI := strings.TrimSpace(config.RedirectURI)
	if clientID == "" || clientSecret == "" || redirectURI == "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("HubSpot OAuth client ID, client secret, and redirect URI are required")
	}

	apiBaseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if apiBaseURL == "" {
		apiBaseURL = defaultAPIBaseURL
	}
	parsed, err := url.Parse(apiBaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("HubSpot OAuth base URL must be an absolute HTTP URL")
	}

	if err := transportsecurity.ValidateURL(parsed, config.AllowPlaintext); err != nil {
		return nil, err
	}

	authorizationBaseURL := defaultAuthorizationBaseURL
	if strings.TrimSpace(config.BaseURL) != "" {
		authorizationBaseURL = apiBaseURL
	}

	return &Client{
		clientID:             clientID,
		clientSecret:         clientSecret,
		redirectURI:          redirectURI,
		apiBaseURL:           apiBaseURL,
		authorizationBaseURL: authorizationBaseURL,
		client:               &http.Client{Timeout: 10 * time.Second, Transport: transportsecurity.HTTPTransport(nil, config.AllowPlaintext), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

// ScopePlan returns HubSpot's mandatory OAuth scope and least-privilege CRM read/write plan.
func (*Client) ScopePlan() ScopePlan {
	return ScopePlan{
		Required: append([]string(nil), scopePlan.Required...),
		Optional: append([]string(nil), scopePlan.Optional...),
	}
}

// AuthorizationURL builds the HubSpot consent URL for the supplied state and scopes.
func (c *Client) AuthorizationURL(params AuthorizationURLParams) (string, error) {
	u, err := url.Parse(c.authorizationBaseURL + "/oauth/authorize")
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	query := u.Query()
	query.Set("client_id", c.clientID)
	query.Set("redirect_uri", c.redirectURI)
	query.Set("state", params.State)
	query.Set("scope", strings.Join(params.Scopes, " "))
	if len(params.OptionalScopes) > 0 {
		query.Set("optional_scopes", strings.Join(params.OptionalScopes, " "))
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// ExchangeCode exchanges a one-time authorization code for a normalized token set.
func (c *Client) ExchangeCode(ctx context.Context, code string) (TokenSet, error) {
	return c.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.redirectURI}})
}

// Refresh exchanges a refresh token while preserving HubSpot's granted scopes.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (TokenSet, error) {
	return c.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}})
}

// Revoke revokes a refresh token, accepting HubSpot's idempotent not-found response.
func (c *Client) Revoke(ctx context.Context, refreshToken string) error {
	token := strings.TrimSpace(refreshToken)
	if token == "" {
		return nil
	}
	form := url.Values{
		"client_id":       {c.clientID},
		"client_secret":   {c.clientSecret},
		"token":           {token},
		"token_type_hint": {"refresh_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBaseURL+"/oauth/2026-03/token/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		return xerror.Unavailable(xerror.DataSource).With(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		return providerError(resp.StatusCode, resp.Body)
	}
	return nil
}

// exchange sends an OAuth token grant and returns only the provider response fields callers need.
func (c *Client) exchange(ctx context.Context, form url.Values) (TokenSet, error) {
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBaseURL+"/oauth/2026-03/token", strings.NewReader(form.Encode()))
	if err != nil {
		return TokenSet{}, xerror.Unexpected().With(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		return TokenSet{}, xerror.Unavailable(xerror.DataSource).With(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		return TokenSet{}, providerError(resp.StatusCode, resp.Body)
	}

	var response tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&response); err != nil {
		return TokenSet{}, xerror.Unavailable(xerror.DataSource).With(err)
	}
	if strings.TrimSpace(response.AccessToken) == "" {
		return TokenSet{}, xerror.Unavailable(xerror.DataSource).Detail("HubSpot OAuth response did not include an access token")
	}

	return TokenSet{
		AccessToken:  response.AccessToken,
		RefreshToken: response.RefreshToken,
		ExpiresAt:    time.Now().UTC().Add(time.Duration(response.ExpiresIn) * time.Second),
		Scopes:       append([]string(nil), response.Scopes...),
		AccountID:    strconv.FormatInt(response.HubID, 10),
	}, nil
}

// providerError maps only HubSpot's invalid_grant response to the grant sentinel.
func providerError(status int, body io.Reader) error {
	var response struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(body, 64*1024)).Decode(&response)
	if strings.TrimSpace(response.Error) == "invalid_grant" {
		return ErrInvalidGrant
	}

	switch status {
	case http.StatusBadRequest:
		return xerror.InvalidInput(xerror.DataSource).Detail("HubSpot OAuth provider rejected request")
	case http.StatusUnauthorized:
		return xerror.InvalidCredentials().Detail("HubSpot OAuth provider rejected client credentials")
	case http.StatusForbidden:
		return xerror.Forbidden(xerror.DataSource).Detail("HubSpot OAuth provider forbids request")
	}
	return xerror.Unavailable(xerror.DataSource).Detail("HubSpot OAuth provider request failed")
}

type tokenResponse struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	ExpiresIn    int64    `json:"expires_in"`
	Scopes       []string `json:"scopes"`
	HubID        int64    `json:"hub_id"`
}
