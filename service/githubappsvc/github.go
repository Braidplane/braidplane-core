package githubappsvc

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	gogithub "github.com/google/go-github/v85/github"
	"golang.org/x/oauth2"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/githubapps"
)

type GitHubProvider struct {
	appID                               int64
	clientID, clientSecret, callbackURL string
	privateKey                          *rsa.PrivateKey
}

const githubRequestTimeout = 30 * time.Second

func NewGitHubProvider(appID int64, clientID, clientSecret, callbackURL, privateKeyPath string) (*GitHubProvider, error) {
	values := []bool{appID > 0, strings.TrimSpace(clientID) != "", strings.TrimSpace(clientSecret) != "", strings.TrimSpace(callbackURL) != "", strings.TrimSpace(privateKeyPath) != ""}
	configured := 0
	for _, value := range values {
		if value {
			configured++
		}
	}
	if configured == 0 && appID == 0 {
		return &GitHubProvider{}, nil
	}
	if configured != len(values) {
		return nil, xerror.Unexpected().Detail("GitHub App provider configuration must be complete")
	}
	callback, err := url.Parse(callbackURL)
	if err != nil || callback.Hostname() == "" || (callback.Scheme != "http" && callback.Scheme != "https") {
		return nil, xerror.Unexpected().Detail("GitHub App callback URL is invalid")
	}
	key, err := parsePrivateKey(privateKeyPath)
	if err != nil {
		return nil, err
	}
	return &GitHubProvider{appID: appID, clientID: clientID, clientSecret: clientSecret, callbackURL: callbackURL, privateKey: key}, nil
}

func (p *GitHubProvider) AuthorizationURL(state string) (string, error) {
	if strings.TrimSpace(state) == "" || strings.TrimSpace(p.clientID) == "" || strings.TrimSpace(p.callbackURL) == "" {
		return "", xerror.Unavailable(xerror.Organization).Detail("GitHub App is not configured")
	}
	authorize, _ := url.Parse("https://github.com/login/oauth/authorize")
	query := authorize.Query()
	query.Set("client_id", p.clientID)
	query.Set("redirect_uri", p.callbackURL)
	query.Set("state", state)
	authorize.RawQuery = query.Encode()
	return authorize.String(), nil
}

func (p *GitHubProvider) userClient(ctx context.Context, code string) (*gogithub.Client, string, error) {
	if p.appID <= 0 || strings.TrimSpace(p.clientID) == "" || strings.TrimSpace(p.clientSecret) == "" || strings.TrimSpace(p.callbackURL) == "" {
		return nil, "", xerror.Unavailable(xerror.Organization).Detail("GitHub App is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, githubRequestTimeout)
	defer cancel()
	token, err := (&oauth2.Config{
		ClientID: p.clientID, ClientSecret: p.clientSecret, RedirectURL: p.callbackURL,
		// #nosec G101 -- This is GitHub's public OAuth token endpoint, not credential material.
		Endpoint: oauth2.Endpoint{TokenURL: "https://github.com/login/oauth/access_token"},
	}).Exchange(ctx, code)
	if err != nil {
		return nil, "", xerror.InvalidCredentials().Detail("GitHub authorization failed or expired; start the authorization again.")
	}
	httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(token))
	httpClient.Timeout = githubRequestTimeout
	client := gogithub.NewClient(httpClient)
	return client, token.AccessToken, nil
}

func (p *GitHubProvider) VerifyInstallation(ctx context.Context, code string, installationID int64) (githubapps.VerifiedInstallation, error) {
	client, _, err := p.userClient(ctx, code)
	if err != nil {
		return githubapps.VerifiedInstallation{}, err
	}
	selected, err := p.authorizedInstallation(ctx, client, installationID)
	if err != nil {
		return githubapps.VerifiedInstallation{}, err
	}
	return verifiedInstallation(ctx, client, selected)
}

func (p *GitHubProvider) VerifyAuthorizedInstallation(ctx context.Context, code, accountLogin string, callbackInstallationID int64) (githubapps.VerifiedInstallation, error) {
	client, _, err := p.userClient(ctx, code)
	if err != nil {
		return githubapps.VerifiedInstallation{}, err
	}
	selected, err := p.authorizedInstallationForAccount(ctx, client, accountLogin, callbackInstallationID)
	if err != nil {
		return githubapps.VerifiedInstallation{}, err
	}
	return verifiedInstallation(ctx, client, selected)
}

func (p *GitHubProvider) VerifyRepositoryInstallation(ctx context.Context, code string, installationID int64, request gitprovider.RepositoryBootstrapRequest) (githubapps.VerifiedInstallation, *gitprovider.RepositoryBootstrapResult, error) {
	client, token, err := p.userClient(ctx, code)
	if err != nil {
		return githubapps.VerifiedInstallation{}, nil, err
	}
	bootstrapper, err := githubprovider.New(ctx, gitprovider.Config{}, gitprovider.Credentials{Token: token}, gitprovider.RepositoryRef{Provider: repostore.ProviderGitHub, Owner: request.Owner, Name: request.Name, FullName: request.Owner + "/" + request.Name})
	if err != nil {
		return githubapps.VerifiedInstallation{}, nil, err
	}
	return p.verifyRepositoryInstallation(ctx, client, installationID, request, bootstrapper)
}

type repositoryBootstrapper interface {
	BootstrapRepository(context.Context, gitprovider.RepositoryBootstrapRequest) (*gitprovider.RepositoryBootstrapResult, error)
}

func (p *GitHubProvider) verifyRepositoryInstallation(ctx context.Context, client *gogithub.Client, installationID int64, request gitprovider.RepositoryBootstrapRequest, bootstrapper repositoryBootstrapper) (githubapps.VerifiedInstallation, *gitprovider.RepositoryBootstrapResult, error) {
	selected, err := p.authorizedInstallation(ctx, client, installationID)
	if err != nil {
		return githubapps.VerifiedInstallation{}, nil, err
	}
	account := selected.GetAccount()
	if account.GetID() <= 0 || !strings.EqualFold(account.GetLogin(), request.Owner) || (account.GetType() != "User" && account.GetType() != "Organization") || selected.SuspendedAt != nil {
		return githubapps.VerifiedInstallation{}, nil, ErrInvalidInstall.Detail("Select the GitHub App installation for the requested repository owner.")
	}
	if selected.GetPermissions().GetContents() != "write" {
		return githubapps.VerifiedInstallation{}, nil, ErrInvalidInstall.Detail("Authorize the GitHub App with contents write permission before creating a repository.")
	}
	result, err := bootstrapper.BootstrapRepository(ctx, request)
	if err != nil {
		return githubapps.VerifiedInstallation{}, incompleteRepositoryResult(result), err
	}
	if result == nil {
		return githubapps.VerifiedInstallation{}, nil, ErrInvalidInstall.Detail("GitHub creation returned no repository identity")
	}
	verified, err := verifiedInstallation(ctx, client, selected)
	if err != nil {
		return githubapps.VerifiedInstallation{}, incompleteRepositoryResult(result), err
	}
	for _, repo := range verified.Repositories {
		if strconv.FormatInt(repo.ID, 10) == result.RepositoryID && strings.EqualFold(repo.FullName, request.Owner+"/"+request.Name) && strings.EqualFold(repo.OwnerLogin, request.Owner) && strings.EqualFold(repo.Name, request.Name) {
			return verified, result, nil
		}
	}
	return githubapps.VerifiedInstallation{}, incompleteRepositoryResult(result), ErrInvalidInstall.Detail("Authorize the created repository in this GitHub App installation, then resume setup.")
}

func incompleteRepositoryResult(result *gitprovider.RepositoryBootstrapResult) *gitprovider.RepositoryBootstrapResult {
	if result == nil {
		return nil
	}
	incomplete := *result
	incomplete.HeadSHA = ""
	return &incomplete
}

func (p *GitHubProvider) authorizedInstallation(ctx context.Context, client *gogithub.Client, installationID int64) (*gogithub.Installation, error) {
	var selected *gogithub.Installation
	for page := 1; selected == nil; page++ {
		installations, response, err := client.Apps.ListUserInstallations(ctx, &gogithub.ListOptions{Page: page, PerPage: 100})
		if err != nil {
			return nil, xerror.Unavailable(xerror.Organization).With(err)
		}
		for _, installation := range installations {
			if installation.GetID() == installationID && installation.GetAppID() == p.appID {
				selected = installation
				break
			}
		}
		if response.NextPage == 0 {
			break
		}
	}
	if selected == nil {
		return nil, ErrInvalidInstall
	}

	return selected, nil
}

func (p *GitHubProvider) authorizedInstallationForAccount(ctx context.Context, client *gogithub.Client, accountLogin string, callbackInstallationID int64) (*gogithub.Installation, error) {
	var selected *gogithub.Installation
	for page := 1; ; page++ {
		installations, response, err := client.Apps.ListUserInstallations(ctx, &gogithub.ListOptions{Page: page, PerPage: 100})
		if err != nil {
			return nil, xerror.Unavailable(xerror.Organization).With(err)
		}
		for _, installation := range installations {
			if installation.GetAppID() != p.appID || installation.SuspendedAt != nil || !strings.EqualFold(installation.GetAccount().GetLogin(), accountLogin) {
				continue
			}
			if selected != nil || (callbackInstallationID > 0 && callbackInstallationID != installation.GetID()) {
				return nil, ErrInvalidInstall.Detail("GitHub App installation could not be matched to the requested account")
			}
			selected = installation
		}
		if response.NextPage == 0 {
			break
		}
	}
	if selected == nil {
		return nil, ErrInvalidInstall.Detail("Install the GitHub App for the requested account before connecting it")
	}
	return selected, nil
}

func verifiedInstallation(ctx context.Context, client *gogithub.Client, selected *gogithub.Installation) (githubapps.VerifiedInstallation, error) {
	installationID := selected.GetID()
	repos := make([]githubapps.RepositoryBinding, 0)
	for page := 1; ; page++ {
		listed, response, err := client.Apps.ListUserRepos(ctx, installationID, &gogithub.ListOptions{Page: page, PerPage: 100})
		if err != nil {
			return githubapps.VerifiedInstallation{}, xerror.Unavailable(xerror.Organization).With(err)
		}
		for _, repo := range listed.Repositories {
			repos = append(repos, githubapps.RepositoryBinding{
				ID: repo.GetID(), FullName: repo.GetFullName(), OwnerLogin: repo.GetOwner().GetLogin(), Name: repo.GetName(),
			})
		}
		if len(repos) > 1000 {
			return githubapps.VerifiedInstallation{}, ErrInvalidInstall.Detail("GitHub App repository selection exceeds the supported limit")
		}
		if response.NextPage == 0 {
			break
		}
	}
	account := selected.GetAccount()
	return githubapps.VerifiedInstallation{
		InstallationID: installationID, AccountID: account.GetID(),
		AccountLogin: account.GetLogin(), AccountType: account.GetType(),
		RepositorySelection: selected.GetRepositorySelection(), Repositories: repos,
	}, nil
}

func (p *GitHubProvider) InstallationToken(ctx context.Context, installationID int64, repositoryIDs []int64) (InstallationToken, error) {
	if p.appID <= 0 || p.privateKey == nil {
		return InstallationToken{}, xerror.Unavailable(xerror.Organization).Detail("GitHub App is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, githubRequestTimeout)
	defer cancel()
	jwt, err := p.appJWT()
	if err != nil {
		return InstallationToken{}, err
	}
	client := gogithub.NewClient(&http.Client{Timeout: githubRequestTimeout}).WithAuthToken(jwt)
	return p.installationToken(ctx, client, installationID, repositoryIDs)
}

//nolint:nilerr // Bot metadata is optional for Git reads; missing metadata blocks proposal publishing.
func (p *GitHubProvider) installationToken(ctx context.Context, client *gogithub.Client, installationID int64, repositoryIDs []int64) (InstallationToken, error) {
	token, _, err := client.Apps.CreateInstallationToken(ctx, installationID, &gogithub.InstallationTokenOptions{RepositoryIDs: repositoryIDs})
	if err != nil {
		return InstallationToken{}, xerror.Unavailable(xerror.Organization).With(err)
	}
	result := InstallationToken{
		Token: token.GetToken(), ExpiresAt: token.GetExpiresAt().Time,
		ContentsPermission:     token.GetPermissions().GetContents(),
		PullRequestsPermission: token.GetPermissions().GetPullRequests(),
	}
	if result.ContentsPermission == "write" && result.PullRequestsPermission == "write" {
		app, _, err := client.Apps.Get(ctx, "")
		if err != nil {
			return result, nil
		}
		if app.GetID() != p.appID || app.GetSlug() == "" {
			return InstallationToken{}, ErrInvalidInstall
		}
		botLogin := app.GetSlug() + "[bot]"
		// Reusing the App JWT transport would overwrite the installation token header.
		installationClient := gogithub.NewClient(&http.Client{Timeout: githubRequestTimeout}).WithAuthToken(result.Token)
		installationClient.BaseURL = client.BaseURL
		bot, _, err := installationClient.Users.Get(ctx, botLogin)
		if err != nil {
			return result, nil
		}
		if bot.GetID() <= 0 || bot.GetType() != "Bot" || bot.GetLogin() != botLogin {
			return InstallationToken{}, ErrInvalidInstall
		}
		result.PrincipalID, result.PrincipalLogin = bot.GetID(), bot.GetLogin()
	}
	return result, nil
}

func (p *GitHubProvider) appJWT() (string, error) {
	if p.privateKey == nil {
		return "", xerror.Unavailable(xerror.Organization).Detail("GitHub App is not configured")
	}
	now := time.Now()
	header, err := json.Marshal(struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}{Algorithm: "RS256", Type: "JWT"})
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	payload, err := json.Marshal(struct {
		IssuedAt  int64 `json:"iat"`
		ExpiresAt int64 `json:"exp"`
		Issuer    int64 `json:"iss"`
	}{IssuedAt: now.Add(-time.Minute).Unix(), ExpiresAt: now.Add(9 * time.Minute).Unix(), Issuer: p.appID})
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(nil, p.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func parsePrivateKey(path string) (*rsa.PrivateKey, error) {
	// #nosec G304 -- the path comes from deployment-owned validated configuration.
	keyBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, xerror.Unexpected().Detail("GitHub App private key could not be read")
	}
	block, _ := pem.Decode(keyBytes)
	if block == nil {
		return nil, xerror.Unexpected().Detail("GitHub App private key is invalid")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return nil, xerror.Unexpected().With(parseErr)
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, xerror.Unexpected().Detail("GitHub App private key must be RSA")
		}
	}
	return key, nil
}
