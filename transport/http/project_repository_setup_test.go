package http

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
)

type repositorySetupFake struct {
	rejectGitHub   func(context.Context, slug.Slug, slug.Slug, model.EventActor, string, string) (*model.ProjectRepositorySetup, error)
	rejectGitLab   func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.EventActor, string, string) (*model.ProjectRepositorySetup, error)
	completeGitLab func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.EventActor, model.GitLabRepositoryAuthorization) (*model.ProjectRepositorySetup, error)
	attempt        func(context.Context, slug.Slug, slug.Slug, slug.Slug) (*model.ProjectRepositorySetup, error)
	abandon        func(context.Context, slug.Slug, slug.Slug, slug.Slug, bool, model.EventActor) (*model.ProjectRepositorySetup, error)
	complete       func(context.Context, slug.Slug, slug.Slug, model.EventActor, model.GitHubRepositoryAuthorization) (*model.ProjectRepositorySetup, error)
	get            func(context.Context, slug.Slug, slug.Slug) (*model.ProjectRepositorySetup, error)
	create         func(context.Context, slug.Slug, slug.Slug, model.CreateProjectRepository, model.EventActor) (*model.ProjectRepositorySetup, error)
}

func (f repositorySetupFake) RejectGitHubAuthorization(ctx context.Context, org, setup slug.Slug, actor model.EventActor, state, providerError string) (*model.ProjectRepositorySetup, error) {
	return f.rejectGitHub(ctx, org, setup, actor, state, providerError)
}

func (f repositorySetupFake) RejectGitLabAuthorization(ctx context.Context, org, project, setup slug.Slug, actor model.EventActor, state, providerError string) (*model.ProjectRepositorySetup, error) {
	return f.rejectGitLab(ctx, org, project, setup, actor, state, providerError)
}

func (f repositorySetupFake) CompleteGitLab(c context.Context, o, p, setup slug.Slug, actor model.EventActor, a model.GitLabRepositoryAuthorization) (*model.ProjectRepositorySetup, error) {
	return f.completeGitLab(c, o, p, setup, actor, a)
}

func (f repositorySetupFake) GetAttempt(c context.Context, o, p, setup slug.Slug) (*model.ProjectRepositorySetup, error) {
	return f.attempt(c, o, p, setup)
}

func (f repositorySetupFake) Abandon(c context.Context, o, p, setup slug.Slug, preserve bool, actor model.EventActor) (*model.ProjectRepositorySetup, error) {
	return f.abandon(c, o, p, setup, preserve, actor)
}

func (f repositorySetupFake) Get(c context.Context, o, p slug.Slug) (*model.ProjectRepositorySetup, error) {
	return f.get(c, o, p)
}

func (f repositorySetupFake) Create(c context.Context, o, p slug.Slug, r model.CreateProjectRepository, a model.EventActor) (*model.ProjectRepositorySetup, error) {
	return f.create(c, o, p, r, a)
}

func TestCreateRepositoryChecksEveryRequiredPermissionBeforeMutation(t *testing.T) {
	for _, denied := range []model.Resource{model.ResourceOrganizations, model.ResourceProjects, model.ResourceDataSources, model.ResourceUnknown} {
		t.Run(denied.String(), func(t *testing.T) {
			called := false
			var checked []model.Resource
			app, store := newTestApp(t, testServerConfig{
				repositorySetupService: repositorySetupFake{create: func(_ context.Context, o, p slug.Slug, r model.CreateProjectRepository, _ model.EventActor) (*model.ProjectRepositorySetup, error) {
					called = true
					require.Equal(t, slug.Slug("acme"), o)
					require.Equal(t, slug.Slug("default"), p)
					require.True(t, r.CreatePrivateRepository)
					require.Equal(t, model.DataSourceTypeGitlab, r.Provider)
					return &model.ProjectRepositorySetup{Slug: "repo-isolated", ProjectSlug: p, DataSourceSlug: "repo-isolated", Request: r, Status: model.ProjectRepositorySetupStatusAttached, RepositoryID: "42"}, nil
				}},
				authService: fakeAuthService{CanFn: func(_ context.Context, in authorization.Input) (bool, error) {
					checked = append(checked, in.Resource.(model.Resource))
					if in.Resource == model.ResourceDataSources {
						require.Equal(t, model.Create, in.Action)
					} else {
						require.Equal(t, model.Update, in.Action)
					}
					return in.Resource != denied, nil
				}},
			})
			req := jsonRequest(t, http.MethodPost, "/organizations/acme/projects/default/repository-setup", map[string]any{"provider": "gitlab", "owner": "alice", "repository_name": "pipelines", "create_private_repository": true})
			resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			if denied == model.ResourceUnknown {
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.True(t, called)
				require.Len(t, checked, 3)
			} else {
				require.Equal(t, http.StatusForbidden, resp.StatusCode)
				require.False(t, called)
			}
		})
	}
}

func TestGetRepositorySetupReturnsNoContentForUnstartedProject(t *testing.T) {
	app, store := newTestApp(t, testServerConfig{repositorySetupService: repositorySetupFake{get: func(_ context.Context, o, p slug.Slug) (*model.ProjectRepositorySetup, error) {
		require.Equal(t, slug.Slug("acme"), o)
		require.Equal(t, slug.Slug("default"), p)
		return nil, nil //nolint:nilnil // A missing setup is the documented 204 response.
	}}, authService: fakeAuthService{CanFn: func(_ context.Context, in authorization.Input) (bool, error) {
		require.Equal(t, model.Read, in.Action)
		require.Equal(t, model.ResourceProjects, in.Resource)
		return true, nil
	}}})
	resp := authenticatedRequest(t, app, store, must.Slug("johndoe"), jsonRequest(t, http.MethodGet, "/organizations/acme/projects/default/repository-setup", nil))
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestAbandonRepositorySetupChecksScopeAndPermissionsBeforeArchiving(t *testing.T) {
	for _, denied := range []model.Resource{model.ResourceOrganizations, model.ResourceProjects, model.ResourceUnknown} {
		t.Run(denied.String(), func(t *testing.T) {
			called := false
			app, store := newTestApp(t, testServerConfig{
				repositorySetupService: repositorySetupFake{abandon: func(_ context.Context, org, project, setup slug.Slug, preserve bool, _ model.EventActor) (*model.ProjectRepositorySetup, error) {
					called = true
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("default"), project)
					require.Equal(t, slug.Slug("repo-old"), setup)
					require.True(t, preserve)
					now := time.Now()
					return &model.ProjectRepositorySetup{Slug: setup, ProjectSlug: project, Status: model.ProjectRepositorySetupStatusPending, AbandonedAt: &now}, nil
				}},
				authService: fakeAuthService{CanFn: func(_ context.Context, in authorization.Input) (bool, error) {
					require.Equal(t, model.Update, in.Action)
					return in.Resource != denied, nil
				}},
			})
			req := jsonRequest(t, http.MethodPost, "/organizations/acme/projects/default/repository-setup/abandon", map[string]any{"setup_slug": "repo-old", "preserve_resources": true})
			response := authenticatedRequest(t, app, store, must.Slug("johndoe"), req)
			defer func() { _ = response.Body.Close() }()
			if denied == model.ResourceUnknown {
				require.Equal(t, http.StatusOK, response.StatusCode)
				require.True(t, called)
			} else {
				require.Equal(t, http.StatusForbidden, response.StatusCode)
				require.False(t, called)
			}
		})
	}
}

func TestGetRepositorySetupAttemptUsesProjectReadPermissionAndExactIdentity(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{true: "allowed", false: "forbidden"}[allowed], func(t *testing.T) {
			called := false
			app, store := newTestApp(t, testServerConfig{
				repositorySetupService: repositorySetupFake{attempt: func(_ context.Context, org, project, setup slug.Slug) (*model.ProjectRepositorySetup, error) {
					called = true
					require.Equal(t, slug.Slug("acme"), org)
					require.Equal(t, slug.Slug("default"), project)
					require.Equal(t, slug.Slug("repo-old"), setup)
					now := time.Now()
					return &model.ProjectRepositorySetup{Slug: setup, ProjectSlug: project, Status: model.ProjectRepositorySetupStatusPending, RepositoryID: "42", AbandonedAt: &now}, nil
				}},
				authService: fakeAuthService{CanFn: func(_ context.Context, in authorization.Input) (bool, error) {
					require.Equal(t, model.Read, in.Action)
					require.Equal(t, model.ResourceProjects, in.Resource)
					return allowed, nil
				}},
			})
			response := authenticatedRequest(t, app, store, must.Slug("johndoe"), jsonRequest(t, http.MethodGet, "/organizations/acme/projects/default/repository-setups/repo-old", nil))
			defer func() { _ = response.Body.Close() }()
			if allowed {
				require.Equal(t, http.StatusOK, response.StatusCode)
			} else {
				require.Equal(t, http.StatusForbidden, response.StatusCode)
			}
			require.Equal(t, allowed, called)
		})
	}
}

func (f repositorySetupFake) CompleteGitHub(ctx context.Context, org, setup slug.Slug, actor model.EventActor, auth model.GitHubRepositoryAuthorization) (*model.ProjectRepositorySetup, error) {
	return f.complete(ctx, org, setup, actor, auth)
}

func TestCompleteRepositoryGitHubSupportsUserPATAndChecksEveryPermission(t *testing.T) {
	for _, denied := range []model.Resource{model.ResourceOrganizations, model.ResourceProjects, model.ResourceDataSources, model.ResourceUnknown} {
		t.Run(denied.String(), func(t *testing.T) {
			called := false
			app, _ := newTestApp(t, testServerConfig{
				middleware: []fiber.Handler{func(c fiber.Ctx) error {
					reqctx.SetUserPrincipal(c, reqctx.UserPrincipal{Slug: "alice", Source: reqctx.UserPrincipalSourcePersonalAccessToken})
					return c.Next()
				}},
				repositorySetupService: repositorySetupFake{
					get: func(_ context.Context, org, project slug.Slug) (*model.ProjectRepositorySetup, error) {
						require.Equal(t, slug.Slug("acme"), org)
						require.Equal(t, slug.Slug("default"), project)
						return &model.ProjectRepositorySetup{Slug: "repo-consent", ProjectSlug: project}, nil
					},
					complete: func(_ context.Context, org, setup slug.Slug, actor model.EventActor, auth model.GitHubRepositoryAuthorization) (*model.ProjectRepositorySetup, error) {
						called = true
						require.Equal(t, slug.Slug("acme"), org)
						require.Equal(t, slug.Slug("repo-consent"), setup)
						require.Equal(t, slug.Slug("alice"), actor.UserSlugValue())
						require.Equal(t, int64(9), auth.InstallationID)
						return &model.ProjectRepositorySetup{Slug: setup, ProjectSlug: "default", DataSourceSlug: setup, Status: model.ProjectRepositorySetupStatusAttached}, nil
					},
				},
				authService: fakeAuthService{CanFn: func(_ context.Context, in authorization.Input) (bool, error) { return in.Resource != denied, nil }},
			})
			resp, err := app.Test(jsonRequest(t, http.MethodPost, "/organizations/acme/projects/default/repository-setup/github-authorization", map[string]any{"state": "isolated-state-with-at-least-32-characters", "code": "isolated-code", "installation_id": 9}))
			require.NoError(t, err)
			defer func() { require.NoError(t, resp.Body.Close()) }()
			if denied == model.ResourceUnknown {
				require.Equal(t, http.StatusOK, resp.StatusCode)
				require.True(t, called)
			} else {
				require.Equal(t, http.StatusForbidden, resp.StatusCode)
				require.False(t, called)
			}
		})
	}
}
