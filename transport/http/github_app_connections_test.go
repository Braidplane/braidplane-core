package http

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/githubapps"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/githubappsvc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGitHubFailureCallbackGatesAndConsumesExactUserState(t *testing.T) {
	for _, scenario := range []string{"denied", "application-error", "org-only", "deny-org", "deny-project", "deny-source", "wrong-user", "missing-result", "ambiguous-result"} {
		t.Run(scenario, func(t *testing.T) {
			state := strings.Repeat("s", 40)
			repo := &callbackIntentRepository{stateHash: fmt.Sprintf("%x", sha256.Sum256([]byte(state))), intent: githubapps.Intent{ID: 1, Organization: "acme", RepositorySetupSlug: "setup-one", Actor: "alice", ExpiresAt: time.Now().Add(time.Minute)}}
			if scenario == "org-only" {
				repo.intent.RepositorySetupSlug = ""
			}
			provider := &blockedCallbackProvider{}
			svc, err := githubappsvc.New(testutil.FakeTransactionManager{}, repo, provider, eventtest.NewProducer(nil), "https://github.com/apps/fixture/installations/new")
			require.NoError(t, err)
			recorded := 0
			app, store := newTestApp(t, testServerConfig{
				githubAppService: svc,
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					assert.Equal(t, slug.Slug("acme"), *input.OrgSlug)
					denied := scenario == "deny-org" && input.Resource == model.ResourceOrganizations || scenario == "deny-project" && input.Resource == model.ResourceProjects || scenario == "deny-source" && input.Resource == model.ResourceDataSources
					return !denied, nil
				}},
				repositorySetupService: repositorySetupFake{rejectGitHub: func(ctx context.Context, org, setup slug.Slug, actor model.EventActor, callbackState, providerError string) (*model.ProjectRepositorySetup, error) {
					recorded++
					assert.Equal(t, slug.Slug("acme"), org)
					assert.Equal(t, slug.Slug("setup-one"), setup)
					assert.Equal(t, slug.Slug("alice"), actor.UserSlugValue())
					assert.Equal(t, state, callbackState)
					assert.Equal(t, scenario == "denied", providerError == "access_denied")
					if err := svc.ConsumeRejectedInstall(ctx, org, setup, actor, callbackState); err != nil {
						return nil, err
					}
					return &model.ProjectRepositorySetup{Slug: setup, ProjectSlug: "project-one", FailureCode: "authorization_denied"}, nil
				}},
			})
			path := "/github-app-connection/callback?state=" + state
			switch scenario {
			case "missing-result":
			case "application-error":
				path += "&error=application_suspended"
			default:
				path += "&error=access_denied&error_description=private-provider-detail"
			}
			if scenario == "ambiguous-result" {
				path += "&code=synthetic-code"
			}
			user := slug.Slug("alice")
			if scenario == "wrong-user" {
				user = "other"
			}
			response := authenticatedRequest(t, app, store, user, jsonRequest(t, http.MethodGet, path, nil))
			require.NoError(t, response.Body.Close())
			wantStatus := http.StatusBadRequest
			if strings.HasPrefix(scenario, "deny-") {
				wantStatus = http.StatusForbidden
			}
			require.Equal(t, wantStatus, response.StatusCode)
			accepted := scenario == "denied" || scenario == "application-error" || scenario == "org-only"
			require.Equal(t, accepted, repo.intent.Consumed)
			if scenario == "denied" || scenario == "application-error" {
				require.Equal(t, 1, recorded)
				require.Equal(t, "/organizations/acme/projects/project-one?section=repository&repository_path=create&repository_setup=failed", response.Header.Get("Location"))
			} else {
				require.Zero(t, recorded)
				require.Empty(t, response.Header.Get("Location"))
			}
			require.Zero(t, provider.calls)
			if accepted {
				replay := authenticatedRequest(t, app, store, user, jsonRequest(t, http.MethodGet, path, nil))
				require.NoError(t, replay.Body.Close())
				require.Equal(t, http.StatusBadRequest, replay.StatusCode)
				require.Empty(t, replay.Header.Get("Location"))
				require.LessOrEqual(t, recorded, 1)
				require.Zero(t, provider.calls)
			}
		})
	}
}

func TestAuthorizeExistingGitHubAppInstallationRequiresOrganizationUpdate(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(fmt.Sprintf("allowed=%t", allowed), func(t *testing.T) {
			repo := &callbackIntentRepository{}
			svc, err := githubappsvc.New(testutil.FakeTransactionManager{}, repo, &blockedCallbackProvider{}, eventtest.NewProducer(nil), "https://github.com/apps/fixture/installations/new")
			require.NoError(t, err)
			app, store := newTestApp(t, testServerConfig{
				githubAppService: svc,
				authService: fakeAuthService{CanFn: func(_ context.Context, input authorization.Input) (bool, error) {
					require.Equal(t, model.ResourceOrganizations, input.Resource)
					require.Equal(t, model.Update, input.Action)
					return allowed, nil
				}},
			})
			response := authenticatedRequest(t, app, store, "alice", jsonRequest(t, http.MethodPost, "/organizations/acme/github-app-connection/authorize", map[string]string{"account_login": "Acme"}))
			defer func() { require.NoError(t, response.Body.Close()) }()
			if allowed {
				require.Equal(t, http.StatusOK, response.StatusCode)
				return
			}
			require.Equal(t, http.StatusForbidden, response.StatusCode)
		})
	}
}

func TestGitHubCallbackOnlyAllowsMissingInstallationIDForExistingAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name             string
		requestedAccount string
		setup            slug.Slug
		providerCalls    int
	}{
		{name: "installation flow", providerCalls: 0},
		{name: "existing authorization", requestedAccount: "acme", providerCalls: 1},
		{name: "repository setup", setup: "setup-one", providerCalls: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := strings.Repeat("s", 40)
			repo := &callbackIntentRepository{stateHash: fmt.Sprintf("%x", sha256.Sum256([]byte(state))), intent: githubapps.Intent{ID: 1, Organization: "acme", Actor: "alice", RequestedAccountLogin: tc.requestedAccount, RepositorySetupSlug: tc.setup, ExpiresAt: time.Now().Add(time.Minute)}}
			provider := &blockedCallbackProvider{}
			svc, err := githubappsvc.New(testutil.FakeTransactionManager{}, repo, provider, eventtest.NewProducer(nil), "https://github.com/apps/fixture/installations/new")
			require.NoError(t, err)
			app, store := newTestApp(t, testServerConfig{githubAppService: svc, authService: fakeAuthService{CanFn: func(_ context.Context, _ authorization.Input) (bool, error) { return true, nil }}})
			response := authenticatedRequest(t, app, store, "alice", jsonRequest(t, http.MethodGet, "/github-app-connection/callback?state="+state+"&code=code", nil))
			defer func() { require.NoError(t, response.Body.Close()) }()
			require.Equal(t, http.StatusBadRequest, response.StatusCode)
			require.Equal(t, tc.providerCalls, provider.calls)
		})
	}
}

// Callback failure may only read and consume its nonce; other repository calls fail loudly.
type callbackIntentRepository struct {
	githubapps.Repository
	stateHash string
	intent    githubapps.Intent
}

func (r *callbackIntentRepository) CreateIntent(_ context.Context, _ postgres.DBTX, org slug.Slug, actor, _ string, expires time.Time, setup slug.Slug, requestedAccountLogin string) error {
	r.intent = githubapps.Intent{ID: 1, Organization: org, Actor: actor, ExpiresAt: expires, RepositorySetupSlug: setup, RequestedAccountLogin: requestedAccountLogin}
	return nil
}

func (r *callbackIntentRepository) GetIntent(_ context.Context, _ postgres.DBTX, hash string) (githubapps.Intent, error) {
	if hash != r.stateHash {
		return githubapps.Intent{}, githubapps.ErrNotFound
	}
	return r.intent, nil
}

func (r *callbackIntentRepository) LockIntent(ctx context.Context, db postgres.DBTX, hash string) (githubapps.Intent, error) {
	return r.GetIntent(ctx, db, hash)
}

func (r *callbackIntentRepository) ConsumeIntent(context.Context, postgres.DBTX, int) error {
	r.intent.Consumed = true
	return nil
}

type blockedCallbackProvider struct{ calls int }

func (*blockedCallbackProvider) AuthorizationURL(state string) (string, error) {
	return "https://github.com/login/oauth/authorize?state=" + state, nil
}

func (p *blockedCallbackProvider) VerifyInstallation(context.Context, string, int64) (githubapps.VerifiedInstallation, error) {
	p.calls++
	return githubapps.VerifiedInstallation{}, githubappsvc.ErrInvalidInstall
}

func (p *blockedCallbackProvider) VerifyAuthorizedInstallation(context.Context, string, string, int64) (githubapps.VerifiedInstallation, error) {
	p.calls++
	return githubapps.VerifiedInstallation{}, githubappsvc.ErrInvalidInstall
}

func (p *blockedCallbackProvider) VerifyRepositoryInstallation(context.Context, string, int64, gitprovider.RepositoryBootstrapRequest) (githubapps.VerifiedInstallation, *gitprovider.RepositoryBootstrapResult, error) {
	p.calls++
	return githubapps.VerifiedInstallation{}, nil, githubappsvc.ErrInvalidInstall
}

func (p *blockedCallbackProvider) InstallationToken(context.Context, int64, []int64) (githubappsvc.InstallationToken, error) {
	p.calls++
	return githubappsvc.InstallationToken{}, githubappsvc.ErrInvalidInstall
}
