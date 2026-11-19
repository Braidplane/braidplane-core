// Package stripeoauth implements the Stripe Apps grant exchange.
package stripeoauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const Scope = "stripe_apps"

var (
	ErrInvalidGrant = xerror.New(xerror.KindInvalidInput, "STRIPE_INVALID_GRANT", "Stripe OAuth grant is invalid")
	accountID       = regexp.MustCompile(`^acct_[A-Za-z0-9_]+$`)
	developerKey    = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
)

type Config struct {
	Mode         string
	InstallURL   string
	DeveloperKey string
	RedirectURI  string
}

func (c Config) Enabled() bool {
	return c.InstallURL != "" || c.DeveloperKey != "" || c.RedirectURI != ""
}

func (c Config) Validate() error {
	if c.Mode != "test" && c.Mode != "live" {
		return xerror.ValidationFailed().Detail("Stripe OAuth mode must be test or live")
	}
	key, ok := strings.CutPrefix(c.DeveloperKey, "sk_"+c.Mode+"_")
	if !ok || !developerKey.MatchString(key) {
		return xerror.ValidationFailed().Detail("Stripe OAuth requires the app developer secret key matching its mode")
	}
	install, err := url.Parse(c.InstallURL)
	if err != nil || install.Scheme != "https" || install.Host != "marketplace.stripe.com" ||
		install.User != nil || install.Fragment != "" || install.Path != "/oauth/v2/authorize" ||
		install.Query().Get("client_id") == "" {
		return xerror.ValidationFailed().Detail("Stripe OAuth requires the mode-specific Marketplace OAuth install URL from the app Dashboard")
	}
	callback, err := url.Parse(c.RedirectURI)
	if err != nil || callback.Host == "" || callback.User != nil || callback.RawQuery != "" ||
		callback.Fragment != "" || callback.Path != "/provider-connectors/stripe_"+c.Mode+"/callback" ||
		(callback.Scheme != "https" && (c.Mode == "live" || callback.Scheme != "http")) {
		return xerror.ValidationFailed().Detail("Stripe OAuth requires the callback URI matching the configured mode")
	}
	return nil
}

type Client struct {
	config Config
	http   *http.Client
}

func New(config Config) (*Client, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Client{config: config, http: &http.Client{
		Timeout:       10 * time.Second,
		Transport:     &http.Transport{DialContext: hostguard.DialPublicHost, ForceAttemptHTTP2: true, IdleConnTimeout: 90 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) AuthorizationURL(state string) (string, error) {
	if strings.TrimSpace(state) == "" {
		return "", xerror.InvalidInput(xerror.DataSource).Detail("Stripe OAuth requires an install state")
	}
	u, err := url.Parse(c.config.InstallURL)
	if err != nil {
		return "", xerror.ValidationFailed().Detail("invalid Stripe OAuth install URL")
	}
	q := u.Query()
	q.Set("state", state)
	q.Set("redirect_uri", c.config.RedirectURI)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type TokenSet struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"stripe_user_id"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
	LiveMode     *bool  `json:"livemode"`
}

func (c *Client) ExchangeCode(ctx context.Context, code string) (TokenSet, error) {
	return c.exchange(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}})
}

func (c *Client) Refresh(ctx context.Context, token string) (TokenSet, error) {
	return c.exchange(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}})
}

func (c *Client) exchange(ctx context.Context, form url.Values) (TokenSet, error) {
	// stripe-go's OAuth service uses Connect's /oauth/token. Apps uses the v1 endpoint and HTTP Basic.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.stripe.com/v1/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return TokenSet{}, xerror.Unexpected().Detail("invalid Stripe token request")
	}
	req.SetBasicAuth(c.config.DeveloperKey, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return TokenSet{}, ctx.Err()
		}
		return TokenSet{}, xerror.Unavailable(xerror.DataSource).Detail("Stripe token endpoint is unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	const maxBytes = 64 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil || len(body) > maxBytes {
		return TokenSet{}, xerror.Unavailable(xerror.DataSource).Detail("invalid Stripe token response")
	}
	if resp.StatusCode != http.StatusOK {
		if (resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized) && invalidGrant(body) {
			return TokenSet{}, ErrInvalidGrant
		}
		return TokenSet{}, xerror.Unavailable(xerror.DataSource).Detailf("Stripe token endpoint returned HTTP %d", resp.StatusCode)
	}
	var tokens TokenSet
	if json.Unmarshal(body, &tokens) != nil || strings.TrimSpace(tokens.AccessToken) == "" || strings.TrimSpace(tokens.RefreshToken) == "" ||
		!accountID.MatchString(tokens.AccountID) || tokens.Scope != Scope || !strings.EqualFold(tokens.TokenType, "bearer") ||
		tokens.LiveMode == nil || *tokens.LiveMode != (c.config.Mode == "live") {
		return TokenSet{}, xerror.Unavailable(xerror.DataSource).Detail("Stripe returned an incomplete or mismatched grant")
	}
	return tokens, nil
}

func invalidGrant(body []byte) bool {
	var oauth struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &oauth) == nil {
		return oauth.Error == "invalid_grant"
	}
	var api struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &api) == nil && api.Error.Code == "invalid_grant"
}
