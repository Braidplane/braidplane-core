package githubappsvc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	gogithub "github.com/google/go-github/v85/github"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestNewGitHubProviderValidatesConfiguration(t *testing.T) {
	t.Parallel()
	_, err := NewGitHubProvider(1, "", "secret", "https://app.example.test/github/callback", "/missing.pem")
	require.Error(t, err)
	_, err = NewGitHubProvider(1, "client", "secret", "not-a-url", "/missing.pem")
	require.Error(t, err)

	path := filepath.Join(t.TempDir(), "github-app.pem")
	require.NoError(t, os.WriteFile(path, []byte("not a private key"), 0o600))
	_, err = NewGitHubProvider(1, "client", "secret", "https://app.example.test/github/callback", path)
	require.Error(t, err)

	_, err = NewGitHubProvider(0, "client", "secret", "https://app.example.test/github/callback", "/missing.pem")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "/missing.pem")
}

func TestNewGitHubProviderWithoutConfigurationIsUnavailable(t *testing.T) {
	t.Parallel()
	provider, err := NewGitHubProvider(0, "", "", "", "")
	require.NoError(t, err)

	_, err = provider.AuthorizationURL("state")
	require.ErrorIs(t, err, xerror.Unavailable(xerror.Organization))
	_, err = provider.VerifyInstallation(t.Context(), "code", 1)
	require.ErrorIs(t, err, xerror.Unavailable(xerror.Organization))
	_, err = provider.VerifyAuthorizedInstallation(t.Context(), "code", "acme", 1)
	require.ErrorIs(t, err, xerror.Unavailable(xerror.Organization))
	_, _, err = provider.VerifyRepositoryInstallation(t.Context(), "code", 1, repositoryBootstrapRequest())
	require.ErrorIs(t, err, xerror.Unavailable(xerror.Organization))
	_, err = provider.InstallationToken(t.Context(), 1, []int64{2})
	require.ErrorIs(t, err, xerror.Unavailable(xerror.Organization))
}

func TestNewGitHubProviderAcceptsCompleteConfiguration(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "github-app.pem")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600))

	provider, err := NewGitHubProvider(1, "client", "secret", "https://app.example.test/github/callback", path)
	require.NoError(t, err)
	require.Equal(t, int64(1), provider.appID)
	require.NotNil(t, provider.privateKey)
}

func TestGitHubProviderAuthorizationURLUsesConfiguredCallbackAndState(t *testing.T) {
	t.Parallel()
	provider := &GitHubProvider{clientID: "client-id", callbackURL: "https://app.example.test/github/callback"}
	link, err := provider.AuthorizationURL("state-value")
	require.NoError(t, err)
	parsed, err := url.Parse(link)
	require.NoError(t, err)
	require.Equal(t, "https", parsed.Scheme)
	require.Equal(t, "github.com", parsed.Host)
	require.Equal(t, "/login/oauth/authorize", parsed.Path)
	require.Equal(t, "client-id", parsed.Query().Get("client_id"))
	require.Equal(t, provider.callbackURL, parsed.Query().Get("redirect_uri"))
	require.Equal(t, "state-value", parsed.Query().Get("state"))
}

func TestInstallationTokenResolvesPublishingBot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, app, bot       string
		appStatus, botStatus int
		check                func(*testing.T, InstallationToken, error)
	}{
		{"Verified", `{"id":3,"slug":"braidplane"}`, `{"id":77,"login":"braidplane[bot]","type":"Bot"}`, http.StatusOK, http.StatusOK, func(t *testing.T, token InstallationToken, err error) {
			require.NoError(t, err)
			require.Equal(t, int64(77), token.PrincipalID)
			require.Equal(t, "braidplane[bot]", token.PrincipalLogin)
			require.Equal(t, "write", token.PullRequestsPermission)
		}},
		{"ChangedApp", `{"id":4,"slug":"other"}`, `{}`, http.StatusOK, http.StatusOK, func(t *testing.T, _ InstallationToken, err error) { require.ErrorIs(t, err, ErrInvalidInstall) }},
		{"WrongBot", `{"id":3,"slug":"braidplane"}`, `{"id":2,"login":"acme","type":"Organization"}`, http.StatusOK, http.StatusOK, func(t *testing.T, _ InstallationToken, err error) { require.ErrorIs(t, err, ErrInvalidInstall) }},

		{"AppLookupUnavailable", `{}`, `{}`, http.StatusServiceUnavailable, http.StatusOK, requireTokenWithoutPublishingIdentity},
		{"BotLookupUnavailable", `{"id":3,"slug":"braidplane"}`, `{}`, http.StatusOK, http.StatusServiceUnavailable, requireTokenWithoutPublishingIdentity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/app/installations/9/access_tokens":
					assert.Equal(t, "POST", r.Method)
					assert.Equal(t, "Bearer app-jwt", r.Header.Get("Authorization"))
					var request struct {
						RepositoryIDs []int64 `json:"repository_ids"`
					}
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&request))
					assert.Equal(t, []int64{42}, request.RepositoryIDs)
					_, _ = io.WriteString(w, `{"token":"ephemeral","expires_at":"2099-01-01T00:00:00Z","permissions":{"contents":"write","pull_requests":"write"}}`)
				case "/app":
					assert.Equal(t, "Bearer app-jwt", r.Header.Get("Authorization"))
					w.WriteHeader(tc.appStatus)
					_, _ = io.WriteString(w, tc.app)
				case "/users/braidplane[bot]":
					w.WriteHeader(tc.botStatus)
					assert.Equal(t, "Bearer ephemeral", r.Header.Get("Authorization"))
					_, _ = io.WriteString(w, tc.bot)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := gogithub.NewClient(server.Client()).WithAuthToken("app-jwt")
			client.BaseURL, _ = url.Parse(server.URL + "/")
			token, err := (&GitHubProvider{appID: 3}).installationToken(t.Context(), client, 9, []int64{42})
			tc.check(t, token, err)
		})
	}
}

func requireTokenWithoutPublishingIdentity(t *testing.T, token InstallationToken, err error) {
	t.Helper()
	require.NoError(t, err)
	require.Equal(t, "ephemeral", token.Token)
	require.Zero(t, token.PrincipalID)
	require.Empty(t, token.PrincipalLogin)
}

type fakeRepositoryBootstrapper struct {
	result *gitprovider.RepositoryBootstrapResult
	err    error
	calls  int
}

func (b *fakeRepositoryBootstrapper) BootstrapRepository(_ context.Context, _ gitprovider.RepositoryBootstrapRequest) (*gitprovider.RepositoryBootstrapResult, error) {
	b.calls++
	return b.result, b.err
}

func TestVerifyRepositoryInstallationRejectsUnauthorizedInstallationBeforeBootstrap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		appID       int64
		owner       string
		accountType string
		contents    string
		suspendedAt any
	}{
		{name: "wrong app", appID: 4, owner: "acme", accountType: "Organization", contents: "write"},
		{name: "wrong owner", appID: 3, owner: "elsewhere", accountType: "Organization", contents: "write"},
		{name: "suspended", appID: 3, owner: "acme", accountType: "Organization", contents: "write", suspendedAt: "2026-10-20T00:00:00Z"},
		{name: "read only", appID: 3, owner: "acme", accountType: "Organization", contents: "read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bootstrapper := &fakeRepositoryBootstrapper{}
			repositoryListingCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user/installations":
					writeGitHubJSON(t, w, map[string]any{"installations": []any{map[string]any{"id": 9, "app_id": tc.appID, "account": map[string]any{"id": 8, "login": tc.owner, "type": tc.accountType}, "permissions": map[string]any{"contents": tc.contents}, "suspended_at": tc.suspendedAt}}})
				case "/user/installations/9/repositories":
					repositoryListingCalls++
					w.WriteHeader(http.StatusInternalServerError)
				default:
					t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
				}
			}))
			t.Cleanup(server.Close)

			verified, result, err := (&GitHubProvider{appID: 3}).verifyRepositoryInstallation(t.Context(), githubAPIClient(t, server), 9, repositoryBootstrapRequest(), bootstrapper)
			require.ErrorIs(t, err, ErrInvalidInstall)
			require.Empty(t, verified)
			require.Nil(t, result)
			require.Zero(t, bootstrapper.calls)
			require.Zero(t, repositoryListingCalls)
		})
	}
}

func TestVerifyRepositoryInstallationBindsExactCreatedRepository(t *testing.T) {
	t.Parallel()
	for _, accountType := range []string{"User", "Organization"} {
		t.Run(accountType, func(t *testing.T) {
			bootstrapper := &fakeRepositoryBootstrapper{result: bootstrapResult("42", "created-sha")}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user/installations":
					assert.Equal(t, "Bearer oauth-user-token", r.Header.Get("Authorization"))
					writeGitHubJSON(t, w, map[string]any{"installations": []any{installationJSON(3, "acme", accountType, "write")}})
				case "/user/installations/9/repositories":
					writeGitHubJSON(t, w, map[string]any{"total_count": 1, "repositories": []any{map[string]any{"id": 42, "full_name": "Acme/Pipelines", "name": "Pipelines", "owner": map[string]any{"login": "Acme"}}}})
				default:
					t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
				}
			}))
			t.Cleanup(server.Close)

			verified, result, err := (&GitHubProvider{appID: 3}).verifyRepositoryInstallation(t.Context(), githubAPIClient(t, server), 9, repositoryBootstrapRequest(), bootstrapper)
			require.NoError(t, err)
			require.Equal(t, accountType, verified.AccountType)
			require.Len(t, verified.Repositories, 1)
			require.Equal(t, "42", result.RepositoryID)
			require.Equal(t, "created-sha", result.HeadSHA)
			require.Equal(t, 1, bootstrapper.calls)
		})
	}
}

func TestVerifyRepositoryInstallationReturnsSafePartialResultAfterBootstrap(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		bootstrap  error
		listStatus int
		repoID     int64
	}{
		{name: "bootstrap error", bootstrap: assert.AnError},
		{name: "binding missing", repoID: 0},
		{name: "binding changed ID", repoID: 43},
		{name: "repository listing unavailable", listStatus: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bootstrapper := &fakeRepositoryBootstrapper{result: bootstrapResult("42", "created-sha"), err: tc.bootstrap}
			listCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/user/installations":
					writeGitHubJSON(t, w, map[string]any{"installations": []any{installationJSON(3, "acme", "Organization", "write")}})
				case "/user/installations/9/repositories":
					listCalls++
					if tc.listStatus != 0 {
						w.WriteHeader(tc.listStatus)
						writeGitHubJSON(t, w, map[string]any{"message": "listing failed"})
						return
					}
					repos := []any{}
					if tc.repoID != 0 {
						repos = append(repos, map[string]any{"id": tc.repoID, "full_name": "acme/pipelines", "name": "pipelines", "owner": map[string]any{"login": "acme"}})
					}
					writeGitHubJSON(t, w, map[string]any{"repositories": repos})
				default:
					t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
				}
			}))
			t.Cleanup(server.Close)

			_, result, err := (&GitHubProvider{appID: 3}).verifyRepositoryInstallation(t.Context(), githubAPIClient(t, server), 9, repositoryBootstrapRequest(), bootstrapper)
			require.Error(t, err)
			require.NotNil(t, result)
			require.Equal(t, "42", result.RepositoryID)
			require.Empty(t, result.HeadSHA)
			require.Equal(t, 1, bootstrapper.calls)
			if tc.bootstrap != nil {
				require.Zero(t, listCalls)
			} else {
				require.Equal(t, 1, listCalls)
			}
		})
	}
}

func TestVerifyAuthorizedInstallationSelectsOnlyRequestedAccount(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, account string
		callbackID    int64
		pages         map[string]any
		wantError     bool
	}{
		{name: "paginates to matching account", account: "acme", pages: map[string]any{
			"1": map[string]any{"installations": []any{installationJSON(4, "other", "Organization", "write")}},
			"2": map[string]any{"installations": []any{installationJSON(3, "AcMe", "Organization", "write")}},
		}},
		{name: "wrong app", account: "acme", pages: map[string]any{"1": map[string]any{"installations": []any{installationJSON(4, "acme", "Organization", "write")}}}, wantError: true},
		{name: "suspended", account: "acme", pages: map[string]any{"1": map[string]any{"installations": []any{map[string]any{"id": 9, "app_id": 3, "account": map[string]any{"id": 8, "login": "acme", "type": "Organization"}, "suspended_at": "2026-10-20T00:00:00Z"}}}}, wantError: true},
		{name: "ambiguous", account: "acme", pages: map[string]any{"1": map[string]any{"installations": []any{installationJSON(3, "acme", "Organization", "write"), map[string]any{"id": 10, "app_id": 3, "account": map[string]any{"id": 10, "login": "acme", "type": "Organization"}}}}}, wantError: true},
		{name: "callback mismatch", account: "acme", callbackID: 10, pages: map[string]any{"1": map[string]any{"installations": []any{installationJSON(3, "acme", "Organization", "write")}}}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/user/installations", r.URL.Path)
				page := r.URL.Query().Get("page")
				if page == "" {
					page = "1"
				}
				if page == "1" && tc.pages["2"] != nil {
					w.Header().Set("Link", "</user/installations?page=2>; rel=\"next\"")
				}
				writeGitHubJSON(t, w, tc.pages[page])
			}))
			t.Cleanup(server.Close)
			client := githubAPIClient(t, server)
			selected, selectErr := (&GitHubProvider{appID: 3}).authorizedInstallationForAccount(t.Context(), client, tc.account, tc.callbackID)
			if tc.wantError {
				require.ErrorIs(t, selectErr, ErrInvalidInstall)
				return
			}
			require.NoError(t, selectErr)
			require.Equal(t, int64(9), selected.GetID())
		})
	}
}

func githubAPIClient(t *testing.T, server *httptest.Server) *gogithub.Client {
	t.Helper()
	client := gogithub.NewClient(server.Client()).WithAuthToken("oauth-user-token")
	baseURL, err := url.Parse(server.URL + "/")
	require.NoError(t, err)
	client.BaseURL = baseURL
	return client
}

func repositoryBootstrapRequest() gitprovider.RepositoryBootstrapRequest {
	return gitprovider.RepositoryBootstrapRequest{Owner: "acme", Name: "pipelines", DefaultBranch: "main"}
}

func bootstrapResult(id, sha string) *gitprovider.RepositoryBootstrapResult {
	return &gitprovider.RepositoryBootstrapResult{RepositoryID: id, HeadSHA: sha}
}

func installationJSON(appID int64, owner, accountType, contents string) map[string]any {
	return map[string]any{"id": 9, "app_id": appID, "account": map[string]any{"id": 8, "login": owner, "type": accountType}, "permissions": map[string]any{"contents": contents}}
}

func writeGitHubJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(value))
}
