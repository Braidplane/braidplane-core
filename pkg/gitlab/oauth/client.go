// Package oauth implements GitLab's provider-owned OAuth2 protocol boundary.
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

const defaultBaseURL = "https://gitlab.com"

const maxProjectPages = 10

var ErrInvalidGrant = xerror.New(xerror.KindInvalidInput, "GITLAB_INVALID_GRANT", "GitLab OAuth invalid grant")

type Config struct {
	AllowPlaintext bool
	ClientID       string
	ClientSecret   string
	RedirectURI    string
	BaseURL        string
}

type TokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       []string
	AccountID    string
	AccountName  string
	AccountEmail string
}

// Project is safe GitLab project identity plus the provider-owned native Git endpoint.
type Project struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	PathWithNS    string `json:"path_with_namespace"`
	WebURL        string `json:"web_url"`
	HTTPURLToRepo string `json:"http_url_to_repo"`
	DefaultBranch string `json:"default_branch"`
}

type accountResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

type Client struct {
	clientID     string
	clientSecret string
	redirectURI  string
	baseURL      string
	client       *http.Client
}

func New(config Config) (*Client, error) {
	clientID := strings.TrimSpace(config.ClientID)
	clientSecret := strings.TrimSpace(config.ClientSecret)
	redirectURI := strings.TrimSpace(config.RedirectURI)
	if clientID == "" || clientSecret == "" || redirectURI == "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("GitLab OAuth client ID, client secret, and redirect URI are required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("GitLab OAuth base URL must be an absolute HTTP URL")
	}
	if err := transportsecurity.ValidateURL(parsed, config.AllowPlaintext); err != nil {
		return nil, err
	}

	return &Client{clientID: clientID, clientSecret: clientSecret, redirectURI: redirectURI, baseURL: baseURL, client: &http.Client{Timeout: 10 * time.Second, Transport: transportsecurity.HTTPTransport(nil, config.AllowPlaintext), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) AuthorizationURL(state string, scopes []string) (string, error) {
	u, err := url.Parse(c.baseURL + "/oauth/authorize")
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	query := u.Query()
	query.Set("client_id", c.clientID)
	query.Set("redirect_uri", c.redirectURI)
	query.Set("response_type", "code")
	query.Set("state", state)
	query.Set("scope", strings.Join(scopes, " "))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (c *Client) ExchangeCode(ctx context.Context, code string) (TokenSet, error) {
	return c.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.redirectURI}})
}

func (c *Client) Refresh(ctx context.Context, refreshToken string) (TokenSet, error) {
	return c.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}})
}

func (c *Client) Revoke(ctx context.Context, accessToken string) error {
	if strings.TrimSpace(accessToken) == "" {
		return nil
	}
	resp, err := c.postForm(ctx, "/oauth/revoke", url.Values{"token": {accessToken}})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		return providerError(resp.StatusCode, resp.Body)
	}
	return nil
}

// ListProjects returns a bounded list of projects visible to the authenticated grant.
func (c *Client) ListProjects(ctx context.Context, accessToken string) ([]Project, error) {
	projects := make([]Project, 0)
	for page := 1; page <= maxProjectPages; page++ {
		path := "/api/v4/projects?membership=true&order_by=path&sort=asc&simple=true&per_page=100&page=" + strconv.Itoa(page)
		resp, err := c.get(ctx, path, accessToken)
		if err != nil {
			return nil, err
		}
		var pageProjects []Project
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&pageProjects)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, xerror.Unavailable(xerror.DataSource).With(decodeErr)
		}
		projects = append(projects, pageProjects...)
		if resp.Header.Get("X-Next-Page") == "" {
			return projects, nil
		}
	}
	return nil, xerror.Unavailable(xerror.DataSource).Detail("GitLab project list exceeded its page limit")
}

// GetProject resolves one stable GitLab project ID through the authenticated grant.
func (c *Client) GetProject(ctx context.Context, accessToken, projectID string) (Project, error) {
	var project Project
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return project, xerror.InvalidInput(xerror.DataSource).Detail("GitLab project ID is required")
	}
	resp, err := c.get(ctx, "/api/v4/projects/"+url.PathEscape(projectID), accessToken)
	if err != nil {
		return project, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&project); err != nil {
		return project, xerror.Unavailable(xerror.DataSource).With(err)
	}
	return project, nil
}

func (c *Client) exchange(ctx context.Context, form url.Values) (TokenSet, error) {
	resp, err := c.postForm(ctx, "/oauth/token", form)
	if err != nil {
		return TokenSet{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		return TokenSet{}, providerError(resp.StatusCode, resp.Body)
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&token); err != nil {
		return TokenSet{}, xerror.Unavailable(xerror.DataSource).With(err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return TokenSet{}, xerror.Unavailable(xerror.DataSource).Detail("GitLab OAuth response did not include an access token")
	}
	account, err := c.account(ctx, token.AccessToken)
	if err != nil {
		return TokenSet{}, err
	}
	expiresAt := time.Time{}
	if token.ExpiresIn > 0 {
		expiresAt = time.Now().UTC().Add(time.Duration(token.ExpiresIn) * time.Second)
	}
	return TokenSet{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, ExpiresAt: expiresAt, Scopes: strings.Fields(token.Scope), AccountID: strconv.FormatInt(account.ID, 10), AccountName: account.Name, AccountEmail: account.Email}, nil
}

func (c *Client) postForm(ctx context.Context, path string, form url.Values) (*http.Response, error) {
	form.Set("client_id", c.clientID)
	form.Set("client_secret", c.clientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, xerror.Unavailable(xerror.DataSource).With(err)
	}
	return resp, nil
}

func (c *Client) get(ctx context.Context, path, accessToken string) (*http.Response, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, ErrInvalidGrant
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, xerror.Unavailable(xerror.DataSource).With(err)
	}
	if resp.StatusCode < http.StatusBadRequest {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrInvalidGrant
	}
	return nil, providerError(resp.StatusCode, resp.Body)
}

func (c *Client) account(ctx context.Context, accessToken string) (accountResponse, error) {
	var account accountResponse
	resp, err := c.get(ctx, "/api/v4/user", accessToken)
	if err != nil {
		return account, err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&account); err != nil {
		return account, xerror.Unavailable(xerror.DataSource).With(err)
	}
	return account, nil
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
		return xerror.InvalidInput(xerror.DataSource).Detail("GitLab OAuth provider rejected request")
	case http.StatusUnauthorized:
		return xerror.InvalidCredentials().Detail("GitLab OAuth provider rejected credentials")
	case http.StatusForbidden:
		return xerror.Forbidden(xerror.DataSource).Detail("GitLab OAuth provider forbids request")
	default:
		return xerror.Unavailable(xerror.DataSource).Detail("GitLab OAuth provider request failed")
	}
}
