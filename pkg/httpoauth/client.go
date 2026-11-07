// Package httpoauth implements the protocol boundary for customer-configured HTTP OAuth apps.
package httpoauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/oauthflow"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var (
	ErrConfig   = xerror.New(xerror.KindInvalidInput, "HTTP_OAUTH_CONFIG_INVALID", "HTTP OAuth configuration is invalid")
	ErrGrant    = xerror.New(xerror.KindInvalidInput, "HTTP_OAUTH_INVALID_GRANT", "HTTP OAuth grant was rejected")
	ErrProvider = xerror.New(xerror.KindUnavailable, "HTTP_OAUTH_PROVIDER_UNAVAILABLE", "HTTP OAuth provider request failed")
)

// Config contains public app settings; client secrets are supplied separately from encrypted storage.
type Config struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RevocationEndpoint    string   `json:"revocation_endpoint,omitempty"`
	ClientID              string   `json:"client_id"`
	GrantType             string   `json:"grant_type"`
	Scopes                []string `json:"scopes,omitempty"`
	Audience              string   `json:"audience,omitempty"`
}

type TokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       []string
}

type Client struct {
	config      Config
	secret      string
	redirectURI string
	client      *http.Client
}

// New applies deployment private-host policy to the pinned transport, with no proxy or redirects.
func New(config Config, secret, redirectURI, destination string, allowPrivateHosts bool) (*Client, error) {
	if strings.TrimSpace(config.ClientID) == "" {
		return nil, ErrConfig.Detail("client_id is required")
	}
	if config.GrantType != "authorization_code" && config.GrantType != "client_credentials" {
		return nil, ErrConfig.Detail("unsupported grant_type")
	}
	for _, endpoint := range []string{config.Issuer, config.TokenEndpoint} {
		if _, err := endpointURL(endpoint, allowPrivateHosts); err != nil {
			return nil, err
		}
	}
	for _, endpoint := range []string{config.AuthorizationEndpoint, config.RevocationEndpoint} {
		if endpoint != "" {
			if _, err := endpointURL(endpoint, allowPrivateHosts); err != nil {
				return nil, err
			}
		}
	}
	if config.GrantType == "authorization_code" {
		if config.AuthorizationEndpoint == "" {
			return nil, ErrConfig.Detail("authorization_endpoint is required")
		}
		if _, err := endpointURL(redirectURI, true); err != nil {
			return nil, ErrConfig.Detail("invalid redirect URI")
		}
	} else if secret == "" {
		return nil, ErrConfig.Detail("client_credentials requires a client secret")
	}
	target, err := endpointURL(destination, allowPrivateHosts)
	if err != nil {
		return nil, err
	}
	if target.RawQuery != "" {
		return nil, ErrConfig.Detail("destination must not contain a query")
	}
	if err := ValidateDestination(destination, destination); err != nil {
		return nil, err
	}
	for _, scope := range config.Scopes {
		if scope == "" || strings.IndexFunc(scope, func(r rune) bool { return r < 0x21 || r > 0x7e || r == '"' || r == '\\' }) >= 0 {
			return nil, ErrConfig.Detail("invalid scope")
		}
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, ErrConfig.Detail("default HTTP transport has unexpected type")
	}
	transport = transport.Clone()
	transport.Proxy = nil
	transport.DialContext = hostguard.NewDestinationPolicy(allowPrivateHosts).DialContext
	config.Scopes = append([]string(nil), config.Scopes...)
	return &Client{config: config, secret: secret, redirectURI: redirectURI, client: &http.Client{
		Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func endpointURL(raw string, allowPrivateHosts bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrConfig.Detail("OAuth endpoints and destinations must be absolute HTTPS URLs without userinfo or fragments")
	}
	if !allowPrivateHosts && hostguard.IsBlockedHostnameLiteral(u.Hostname()) {
		return nil, ErrConfig.Detail("private OAuth destination is not allowed")
	}
	return u, nil
}

// ValidateDestination binds bearer use to the configured origin and base path.
func ValidateDestination(destination, raw string) error {
	baseURL, err := endpointURL(destination, true)
	if err != nil {
		return err
	}
	if baseURL.RawQuery != "" {
		return ErrConfig.Detail("OAuth destination must not contain a query")
	}
	u, err := endpointURL(raw, true)
	if err != nil {
		return err
	}
	base := strings.TrimRight(baseURL.EscapedPath(), "/")
	if !strings.EqualFold(u.Host, baseURL.Host) || u.Scheme != baseURL.Scheme ||
		(u.EscapedPath() != base && !strings.HasPrefix(u.EscapedPath(), base+"/")) {
		return ErrConfig.Detail("request is outside the OAuth destination")
	}
	// Escaped separators and dot segments must not bypass the base-path boundary at the provider.
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || strings.ContainsAny(segment, "\\%") {
			return ErrConfig.Detail("unsafe OAuth destination path")
		}
	}
	if strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") {
		return ErrConfig.Detail("unsafe OAuth destination path")
	}
	return nil
}

// AuthorizationURL requires a per-install verifier which the credential lifecycle must store encrypted.
func (c *Client) AuthorizationURL(state, verifier string) (string, error) {
	if c.config.GrantType != "authorization_code" || state == "" || !validVerifier(verifier) {
		return "", ErrConfig.Detail("authorization requires state and a PKCE verifier")
	}
	u, _ := url.Parse(c.config.AuthorizationEndpoint)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", c.config.ClientID)
	q.Set("redirect_uri", c.redirectURI)
	q.Set("state", state)
	challenge := sha256.Sum256([]byte(verifier))
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	q.Set("code_challenge_method", "S256")
	c.addScopeAudience(q)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func validVerifier(verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, r := range verifier {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune("-._~", r):
		default:
			return false
		}
	}
	return true
}

// Exchange verifies the callback issuer against the stored app before sending a code.
func (c *Client) Exchange(ctx context.Context, code, verifier, issuer string) (TokenSet, error) {
	if c.config.GrantType != "authorization_code" || code == "" || !validVerifier(verifier) || issuer != c.config.Issuer {
		return TokenSet{}, ErrGrant
	}
	return c.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {c.redirectURI}}, c.config.Scopes)
}

func (c *Client) ClientCredentials(ctx context.Context) (TokenSet, error) {
	if c.config.GrantType != "client_credentials" {
		return TokenSet{}, ErrConfig.Detail("client_credentials is not configured")
	}
	values := url.Values{"grant_type": {"client_credentials"}}
	c.addScopeAudience(values)
	return c.token(ctx, values, c.config.Scopes)
}

func (c *Client) Refresh(ctx context.Context, refreshToken string, grantedScopes []string) (TokenSet, error) {
	if c.config.GrantType == "client_credentials" {
		return c.ClientCredentials(ctx)
	}
	if refreshToken == "" {
		return TokenSet{}, ErrGrant
	}
	values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}}
	if len(grantedScopes) > 0 {
		values.Set("scope", strings.Join(grantedScopes, " "))
	}
	return c.token(ctx, values, grantedScopes)
}

func (c *Client) addScopeAudience(values url.Values) {
	if len(c.config.Scopes) > 0 {
		values.Set("scope", strings.Join(c.config.Scopes, " "))
	}
	if c.config.Audience != "" {
		values.Set("audience", c.config.Audience)
	}
}

func (c *Client) token(ctx context.Context, values url.Values, defaultScopes []string) (TokenSet, error) {
	body, err := c.post(ctx, c.config.TokenEndpoint, values)
	if err != nil {
		return TokenSet{}, err
	}
	var response struct {
		AccessToken  string  `json:"access_token"`
		RefreshToken string  `json:"refresh_token"`
		TokenType    string  `json:"token_type"`
		ExpiresIn    int64   `json:"expires_in"`
		Scope        *string `json:"scope"`
	}
	if json.Unmarshal(body, &response) != nil || response.AccessToken == "" || !strings.EqualFold(response.TokenType, "Bearer") || response.ExpiresIn <= 0 || response.ExpiresIn > 31536000 || strings.ContainsAny(response.AccessToken, "\r\n") {
		return TokenSet{}, ErrProvider
	}
	scopes := append([]string(nil), defaultScopes...)
	if response.Scope != nil {
		scopes = strings.Fields(*response.Scope)
	}
	granted := make(map[string]bool, len(scopes))
	for _, scope := range scopes {
		granted[scope] = true
	}
	for _, scope := range c.config.Scopes {
		if !granted[scope] {
			return TokenSet{}, ErrGrant.Detail("provider did not grant required scopes")
		}
	}
	return TokenSet{AccessToken: response.AccessToken, RefreshToken: response.RefreshToken, ExpiresAt: time.Now().Add(time.Duration(response.ExpiresIn) * time.Second), Scopes: scopes}, nil
}

func (c *Client) Revoke(ctx context.Context, token string) error {
	if c.config.RevocationEndpoint == "" {
		return nil
	}
	if token == "" {
		return ErrGrant
	}
	_, err := c.post(ctx, c.config.RevocationEndpoint, url.Values{"token": {token}})
	return err
}

func (c *Client) post(ctx context.Context, endpoint string, values url.Values) ([]byte, error) {
	if c.secret == "" {
		values.Set("client_id", c.config.ClientID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, ErrProvider
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.secret != "" {
		req.SetBasicAuth(url.QueryEscape(c.config.ClientID), url.QueryEscape(c.secret))
	}
	response, err := c.client.Do(req)
	// Transport errors can contain URLs and provider-controlled text; never retain them.
	if err != nil {
		return nil, ErrProvider
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		return nil, ErrProvider
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &failure) == nil && (failure.Error == "invalid_grant" || failure.Error == "access_denied") {
			return nil, ErrGrant
		}
		return nil, ErrProvider
	}
	return body, nil
}

// NewVerifier uses the same cryptographic randomness as OAuth state, meeting RFC 7636's minimum length.
func NewVerifier() (string, error) { return oauthflow.RandomState() }
