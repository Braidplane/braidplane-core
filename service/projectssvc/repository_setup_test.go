package projectssvc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/postgres/repositorysetups"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	gitconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/git"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

func TestNormalizeRepositorySetupRequestRequiresGitLabConsentAndCanonicalTemplateInputs(t *testing.T) {
	t.Parallel()
	request, err := normalizeRepositorySetupRequest(model.CreateProjectRepository{Provider: model.DataSourceTypeGitlab, Owner: "Acme/Platform", RepositoryName: "Pipelines", TrackedBranch: " main ", PipelineDir: `definitions\\pipelines`, CreatePrivateRepository: true})
	require.NoError(t, err)
	require.Equal(t, "main", request.TrackedBranch)
	require.Equal(t, "definitions/pipelines", request.PipelineDir)

	_, err = normalizeRepositorySetupRequest(model.CreateProjectRepository{Provider: model.DataSourceTypeGitlab, Owner: "acme", RepositoryName: "pipelines", TrackedBranch: "main", PipelineDir: "pipelines"})
	require.ErrorContains(t, err, "consent")
	_, err = normalizeRepositorySetupRequest(model.CreateProjectRepository{Provider: model.DataSourceTypeGit, Owner: "acme", RepositoryName: "pipelines", TrackedBranch: "main", PipelineDir: "pipelines", CreatePrivateRepository: true})
	require.ErrorContains(t, err, "GitLab")
	defaults, err := normalizeRepositorySetupRequest(model.CreateProjectRepository{Provider: model.DataSourceTypeGitlab, Owner: "acme", RepositoryName: "pipelines", CreatePrivateRepository: true})
	require.NoError(t, err)
	require.Equal(t, "main", defaults.TrackedBranch)
	require.Equal(t, "pipelines", defaults.PipelineDir)
}

func TestRepositoryAuthorizationFailureIsDurableAndRetryStartsFreshConsent(t *testing.T) {
	for _, provider := range []model.DataSourceType{model.DataSourceTypeGithub, model.DataSourceTypeGitlab} {
		for _, providerError := range []string{"access_denied", "private-provider-error"} {
			t.Run(provider.String()+"/"+providerError, func(t *testing.T) {
				f := newRepositorySetupFixture(t)
				request := githubSetupRequest()
				if provider == model.DataSourceTypeGitlab {
					request = setupRequest()
					f.credentials.connection.Credential.OAuth.Scopes = []string{"read_api"}
				}
				actor := model.UserEventActor("alice")
				pending, err := f.service.Create(t.Context(), "acme", "project", request, actor)
				require.NoError(t, err)
				require.NotEmpty(t, pending.AuthorizationURL)
				f.github.intentSetup = pending.Slug
				var failed *model.ProjectRepositorySetup
				if provider == model.DataSourceTypeGithub {
					failed, err = f.service.RejectGitHubAuthorization(t.Context(), "acme", pending.Slug, actor, "synthetic-state", providerError)
				} else {
					failed, err = f.service.RejectGitLabAuthorization(t.Context(), "acme", "project", pending.Slug, actor, "synthetic-state", providerError)
				}
				require.NoError(t, err)
				want := "authorization_failed"
				if providerError == "access_denied" {
					want = "authorization_denied"
				}
				require.Equal(t, want, failed.FailureCode)
				stored, err := f.service.Get(t.Context(), "acme", "project")
				require.NoError(t, err)
				require.Equal(t, want, stored.FailureCode)
				require.Empty(t, f.repo.leaseOwner)
				require.Empty(t, f.bootstrap.requests)
				require.Zero(t, f.github.completeCalls)
				require.Zero(t, f.credentials.completeCalls)
				require.Zero(t, f.projects.sets)
				resumed, err := f.service.Create(t.Context(), "acme", "project", request, actor)
				require.NoError(t, err)
				require.Equal(t, pending.Slug, resumed.Slug)
				require.Empty(t, resumed.FailureCode)
				require.NotEmpty(t, resumed.AuthorizationURL)
			})
		}
	}
}

func TestRejectedGitLabConsentCannotChangeStaleOrAttachedSetup(t *testing.T) {
	for _, scenario := range []string{"wrong-user", "wrong-org", "wrong-project", "wrong-setup", "consumed", "busy", "attached", "archived", "consume-error"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRepositorySetupFixture(t)
			f.credentials.connection.Credential.OAuth.Scopes = []string{"read_api"}
			pending, err := f.service.Create(t.Context(), "acme", "project", setupRequest(), model.UserEventActor("alice"))
			require.NoError(t, err)
			now := time.Now()
			switch scenario {
			case "wrong-user":
				f.credentials.install.Actor = model.UserEventActor("other")
			case "wrong-org":
				f.credentials.install.Owner.OrgSlug = "other"
			case "wrong-project":
				f.credentials.install.RepositorySetupProjectSlug = "other"
			case "wrong-setup":
				f.credentials.install.RepositorySetupSlug = "other"
			case "consumed":
				f.credentials.install.ConsumedAt = &now
			case "busy":
				f.repo.busy = true
			case "attached":
				f.repo.row.Status = model.ProjectRepositorySetupStatusAttached.String()
			case "archived":
				f.repo.row.AbandonedAt = &now
			case "consume-error":
				f.credentials.consumeErr = xerror.Unexpected().Detail("fixture consume failure")
			}
			_, err = f.service.RejectGitLabAuthorization(t.Context(), "acme", "project", pending.Slug, model.UserEventActor("alice"), "synthetic-state", "access_denied")
			require.Error(t, err)
			require.Empty(t, f.repo.row.FailureCode)
			require.Empty(t, f.repo.leaseOwner)
			require.Equal(t, map[bool]int{true: 1, false: 0}[scenario == "consume-error"], f.credentials.consumeCalls)
			require.Zero(t, f.credentials.completeCalls)
			require.Zero(t, f.bootstrap.calls)
		})
	}
}

func TestGitLabRepositorySetupObtainsMissingWriteConsentAndReturnsToExactAttempt(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"missing", "read only", "revoked"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx := newRepositorySetupFixture(t)
			switch name {
			case "missing":
				fx.credentials.getErr = credentialssvc.ErrCredentialNotFound
			case "read only":
				fx.credentials.connection.Credential.OAuth.Scopes = []string{"read_repository"}
			case "revoked":
				fx.credentials.connection.Credential.Status = credentialssvc.CredentialStatusRevoked
			}
			actor := model.UserEventActor("alice")
			pending, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), actor)
			require.NoError(t, err)
			require.Contains(t, pending.AuthorizationURL, "https://gitlab.com/oauth/authorize")
			require.Equal(t, model.ProjectRepositorySetupStatusPending, pending.Status)
			require.Zero(t, fx.bootstrap.calls)
			require.Zero(t, fx.sources.creates)
			require.Len(t, fx.credentials.starts, 1)
			start := fx.credentials.starts[0]
			require.Equal(t, pending.Slug, start.RepositorySetupSlug)
			require.Equal(t, slug.Slug("project"), start.RepositorySetupProjectSlug)
			require.Equal(t, actor, start.Actor)
			require.True(t, start.IncludeWriteScopes)
			if name == "missing" {
				require.Equal(t, credentialssvc.OAuthInstallIntentCreate, start.Intent)
			} else {
				require.Equal(t, credentialssvc.OAuthInstallIntentRotate, start.Intent)
			}
			fx.credentials.onComplete = func() {
				_, err := fx.service.Abandon(t.Context(), "acme", "project", pending.Slug, true, actor)
				require.ErrorIs(t, err, repositorysetups.ErrBusy)
			}
			fx.bootstrap.results = []*gitprovider.RepositoryBootstrapResult{bootstrapResult("42", "head")}
			completed, err := fx.service.CompleteGitLab(t.Context(), "acme", "project", pending.Slug, actor, model.GitLabRepositoryAuthorization{State: "state", Code: "transient-code"})
			require.NoError(t, err)
			require.Equal(t, pending.Slug, completed.Slug)
			require.Empty(t, completed.AuthorizationURL)
			require.Equal(t, model.ProjectRepositorySetupStatusAttached, completed.Status)
			require.Equal(t, 1, fx.credentials.completeCalls)
			require.Equal(t, 1, fx.bootstrap.calls)
			require.Equal(t, 1, fx.sources.creates)
			require.Empty(t, fx.repo.leaseOwner)
			_, err = fx.service.CompleteGitLab(t.Context(), "acme", "project", pending.Slug, actor, model.GitLabRepositoryAuthorization{State: "state", Code: "transient-code"})
			require.Error(t, err)
			require.Equal(t, 1, fx.credentials.completeCalls)
		})
	}
}

func TestGitLabRepositorySetupRefusesMachineConsentAndStaleUserContextBeforeExchange(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	fx.credentials.getErr = credentialssvc.ErrCredentialNotFound
	_, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
	require.ErrorContains(t, err, "personal access token")
	require.Empty(t, fx.credentials.starts)

	pending, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.UserEventActor("alice"))
	require.NoError(t, err)
	for _, tc := range []struct{ org, project, setup, user slug.Slug }{
		{"other-org", "project", pending.Slug, "alice"},
		{"acme", "other-project", pending.Slug, "alice"},
		{"acme", "project", "other-setup", "alice"},
		{"acme", "project", pending.Slug, "bob"},
	} {
		_, err = fx.service.CompleteGitLab(t.Context(), tc.org, tc.project, tc.setup, model.UserEventActor(tc.user), model.GitLabRepositoryAuthorization{State: "state", Code: "code"})
		require.ErrorIs(t, err, credentialssvc.ErrOAuthStateInvalid)
	}
	require.Zero(t, fx.credentials.completeCalls)
	require.Zero(t, fx.bootstrap.calls)
	oldInstall := fx.credentials.install
	_, err = fx.service.Abandon(t.Context(), "acme", "project", pending.Slug, true, model.UserEventActor("alice"))
	require.NoError(t, err)
	newRequest := setupRequest()
	newRequest.RepositoryName = "another-target"
	next, err := fx.service.Create(t.Context(), "acme", "project", newRequest, model.UserEventActor("alice"))
	require.NoError(t, err)
	fx.credentials.install = oldInstall
	_, err = fx.service.CompleteGitLab(t.Context(), "acme", "project", pending.Slug, model.UserEventActor("alice"), model.GitLabRepositoryAuthorization{State: "state", Code: "code"})
	require.ErrorIs(t, err, repositorysetups.ErrBusy)
	require.Zero(t, fx.credentials.completeCalls)
	require.Equal(t, next.Slug, fx.repo.row.Slug)
}

func TestGitLabRepositoryConsentFailureReleasesLeaseWithoutCreatingRepository(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	fx.credentials.getErr = credentialssvc.ErrCredentialNotFound
	pending, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.UserEventActor("alice"))
	require.NoError(t, err)
	fx.credentials.completeErr = credentialssvc.ErrProviderUnavailable
	_, err = fx.service.CompleteGitLab(t.Context(), "acme", "project", pending.Slug, model.UserEventActor("alice"), model.GitLabRepositoryAuthorization{State: "state", Code: "code"})
	require.ErrorIs(t, err, credentialssvc.ErrProviderUnavailable)
	require.Zero(t, fx.bootstrap.calls)
	require.Empty(t, fx.repo.leaseOwner)
	resumed, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.UserEventActor("alice"))
	require.NoError(t, err)
	require.Equal(t, pending.Slug, resumed.Slug)
	require.Len(t, fx.credentials.starts, 2)
}

func TestRepositoryReconsentClearsOldFailureAndRequiresLease(t *testing.T) {
	t.Parallel()
	for _, provider := range []model.DataSourceType{model.DataSourceTypeGithub, model.DataSourceTypeGitlab} {
		t.Run(provider.String(), func(t *testing.T) {
			fx := newRepositorySetupFixture(t)
			request := setupRequest()
			request.Provider = provider
			if provider == model.DataSourceTypeGithub {
				request.Owner = "acme"
			} else {
				fx.credentials.getErr = credentialssvc.ErrCredentialNotFound
			}
			actor := model.UserEventActor("alice")
			_, err := fx.service.Create(t.Context(), "acme", "project", request, actor)
			require.NoError(t, err)
			fx.repo.row.FailureCode = "repository_bootstrap_failed"
			fx.repo.busy = true
			_, err = fx.service.Create(t.Context(), "acme", "project", request, actor)
			require.ErrorIs(t, err, repositorysetups.ErrBusy)
			require.Equal(t, "repository_bootstrap_failed", fx.repo.row.FailureCode)
			fx.repo.busy = false
			retry, err := fx.service.Create(t.Context(), "acme", "project", request, actor)
			require.NoError(t, err)
			require.NotEmpty(t, retry.AuthorizationURL)
			require.Empty(t, retry.FailureCode)
			require.Empty(t, fx.repo.row.FailureCode)
			require.Zero(t, fx.bootstrap.calls)
			require.Zero(t, fx.sources.creates)
		})
	}
}

func TestRepositorySetupRequestAndBootstrapResultRequireExactTargetIdentity(t *testing.T) {
	t.Parallel()
	a := model.CreateProjectRepository{Provider: model.DataSourceTypeGitlab, Owner: "Acme/Platform", RepositoryName: "Pipelines", TrackedBranch: "main", PipelineDir: "pipelines", CreatePrivateRepository: true}
	b := a
	b.Owner, b.RepositoryName = "acme/platform", "pipelines"
	require.True(t, sameRepositorySetupRequest(a, b))
	b.TrackedBranch = "trunk"
	require.False(t, sameRepositorySetupRequest(a, b))

	require.True(t, validRepositoryIdentity(&gitprovider.RepositoryBootstrapResult{Repository: gitprovider.RepositoryRef{Provider: "gitlab", Owner: "acme/platform", Name: "pipelines"}, RepositoryID: "42"}, a))
	require.False(t, validRepositoryIdentity(&gitprovider.RepositoryBootstrapResult{Repository: gitprovider.RepositoryRef{Provider: "gitlab", Owner: "acme/platform", Name: "pipelines"}, RepositoryID: "0"}, a))
}

func TestRepositorySetupServiceRetriesAttachedSetupWithoutFurtherMutation(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	fx.bootstrap.results = []*gitprovider.RepositoryBootstrapResult{bootstrapResult("42", "first")}
	first, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
	require.NoError(t, err)
	require.Equal(t, model.ProjectRepositorySetupStatusAttached, first.Status)
	_, err = fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
	require.NoError(t, err)
	require.Equal(t, 1, fx.bootstrap.calls)
	require.Equal(t, 1, fx.sources.creates)
	require.Equal(t, 1, fx.projects.sets)
}

func TestRepositorySetupRequiresSuccessfulRetestBeforeAttachingSavedSource(t *testing.T) {
	t.Parallel()
	for _, stage := range []model.ProjectRepositorySetupStatus{model.ProjectRepositorySetupStatusRepositoryCreated, model.ProjectRepositorySetupStatusSourceCreated} {
		t.Run(stage.String(), func(t *testing.T) {
			fx := newRepositorySetupFixture(t)
			request, err := json.Marshal(setupRequest())
			require.NoError(t, err)
			result, err := json.Marshal(setupResult{RepositoryID: "42", RepositoryURL: "https://gitlab.com/acme/platform/pipelines.git", FirstCommitSHA: "first"})
			require.NoError(t, err)
			fx.repo.row = &repositorysetups.Row{Slug: "repo-saved", Request: request, Result: result, Status: stage.String()}
			config, err := json.Marshal(gitconnector.ProviderConfig{Repository: gitconnector.ProviderRepository{ID: "42"}})
			require.NoError(t, err)
			fx.sources.ds = &datasourcessvc.DataSource{Slug: "repo-saved", Type: model.DataSourceTypeGitlab, Config: config, IsActive: false}
			pending, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.UserEventActor("alice"))
			require.ErrorIs(t, err, ErrRepositorySetupSourceTestRequired)
			require.Equal(t, "source_test_required", pending.FailureCode)
			require.Equal(t, "source_test_required", fx.repo.row.FailureCode)
			require.False(t, fx.sources.ds.IsActive)
			require.Zero(t, fx.projects.sets)
			require.Zero(t, fx.bootstrap.calls)
			require.Zero(t, fx.sources.creates)
			fx.sources.ds.IsActive = true
			completed, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.UserEventActor("alice"))
			require.NoError(t, err)
			require.Equal(t, model.ProjectRepositorySetupStatusAttached, completed.Status)
			require.Empty(t, completed.FailureCode)
			require.Zero(t, fx.bootstrap.calls)
			require.Zero(t, fx.sources.creates)
		})
	}
}

func TestRepositorySetupServiceResumesPartialProviderFailureWithSameID(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	fx.bootstrap.results = []*gitprovider.RepositoryBootstrapResult{bootstrapResult("42", ""), bootstrapResult("42", "first")}
	fx.bootstrap.errs = []error{xerror.Unexpected().Detail("timed out"), nil}
	partial, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
	require.Error(t, err)
	require.Equal(t, "42", partial.RepositoryID)
	require.Equal(t, model.ProjectRepositorySetupStatusPending, partial.Status)
	completed, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
	require.NoError(t, err)
	require.Equal(t, model.ProjectRepositorySetupStatusAttached, completed.Status)
	require.Equal(t, 2, fx.bootstrap.calls)
	require.Empty(t, fx.bootstrap.requests[0].ExpectedRepositoryID)
	require.Equal(t, "42", fx.bootstrap.requests[1].ExpectedRepositoryID)
}

func TestRepositorySetupAbandonPreservesPartialResourcesAndRequiresFreshIdentity(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	fx.bootstrap.results = []*gitprovider.RepositoryBootstrapResult{bootstrapResult("42", "")}
	fx.bootstrap.errs = []error{xerror.Unexpected().Detail("interrupted")}
	partial, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
	require.Error(t, err)
	archived, err := fx.service.Abandon(t.Context(), "acme", "project", partial.Slug, true, model.SystemEventActor())
	require.NoError(t, err)
	require.NotNil(t, archived.AbandonedAt)
	require.Equal(t, "42", archived.RepositoryID)
	require.Equal(t, partial.Request, archived.Request)
	require.Equal(t, partial.RepositoryURL, archived.RepositoryURL)
	require.Equal(t, 1, fx.bootstrap.calls)
	require.Zero(t, fx.sources.creates)
	require.Zero(t, fx.projects.sets)

	active, err := fx.service.Get(t.Context(), "acme", "project")
	require.NoError(t, err)
	require.Nil(t, active)
	request := githubSetupRequest()
	request.RepositoryName = "another-target"
	next, err := fx.service.Create(t.Context(), "acme", "project", request, model.UserEventActor("alice"))
	require.NoError(t, err)
	require.NotEqual(t, archived.Slug, next.Slug)
	require.Empty(t, next.RepositoryID)
	require.NotEmpty(t, next.AuthorizationURL)

	// A stale in-flight request cannot claim the new active attempt.
	_, err = fx.service.runSetup(t.Context(), "acme", "project", partial, model.SystemEventActor(), nil)
	require.ErrorIs(t, err, repositorysetups.ErrBusy)
	require.Equal(t, 1, fx.bootstrap.calls)
	_, err = fx.service.Abandon(t.Context(), "acme", "project", archived.Slug, true, model.SystemEventActor())
	require.NoError(t, err)
	require.Equal(t, next.Slug, fx.repo.row.Slug)
	retained, err := fx.service.GetAttempt(t.Context(), "acme", "project", archived.Slug)
	require.NoError(t, err)
	require.Equal(t, "42", retained.RepositoryID)
}

func TestRepositorySetupAbandonRejectsUnconsentedRunningAndAttachedAttempts(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"consent missing", "running", "attached"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx := newRepositorySetupFixture(t)
			pending, err := fx.service.Create(t.Context(), "acme", "project", githubSetupRequest(), model.UserEventActor("alice"))
			require.NoError(t, err)
			fx.repo.busy = name == "running"
			if name == "attached" {
				fx.projects.project.PipelineBackend = &model.ProjectPipelineBackend{DataSourceSlug: "connected"}
			}
			_, err = fx.service.Abandon(t.Context(), "acme", "project", pending.Slug, name != "consent missing", model.UserEventActor("alice"))
			require.Error(t, err)
			require.NotNil(t, fx.repo.row)
			require.Nil(t, fx.repo.row.AbandonedAt)
			require.Zero(t, fx.github.completeCalls)
			require.Zero(t, fx.bootstrap.calls)
		})
	}
}

func TestAbandonedGitHubConsentCannotExchangeCodeForAnotherAttempt(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	old, err := fx.service.Create(t.Context(), "acme", "project", githubSetupRequest(), model.UserEventActor("alice"))
	require.NoError(t, err)
	_, err = fx.service.Abandon(t.Context(), "acme", "project", old.Slug, true, model.UserEventActor("alice"))
	require.NoError(t, err)
	next, err := fx.service.Create(t.Context(), "acme", "project", githubSetupRequest(), model.UserEventActor("alice"))
	require.NoError(t, err)
	fx.github.intentOrg, fx.github.intentSetup = "acme", old.Slug
	_, err = fx.service.CompleteGitHub(t.Context(), "acme", old.Slug, model.UserEventActor("alice"), model.GitHubRepositoryAuthorization{State: "old-state", Code: "old-code", InstallationID: 9})
	require.Error(t, err)
	require.Equal(t, next.Slug, fx.repo.row.Slug)
	require.Zero(t, fx.github.completeCalls)
}

func TestRepositorySetupServiceCompletesAttachAfterAttachedPersistFailure(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	fx.bootstrap.results = []*gitprovider.RepositoryBootstrapResult{bootstrapResult("42", "first")}
	fx.repo.failAttached = true
	_, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
	require.Error(t, err)
	fx.repo.failAttached = false
	got, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
	require.NoError(t, err)
	require.Equal(t, model.ProjectRepositorySetupStatusAttached, got.Status)
	require.Equal(t, 1, fx.bootstrap.calls)
	require.Equal(t, 1, fx.projects.sets)
}

func TestRepositorySetupServiceRejectsBusyAndUnrelatedBackendBeforeProviderCall(t *testing.T) {
	t.Parallel()
	t.Run("busy", func(t *testing.T) {
		fx := newRepositorySetupFixture(t)
		fx.repo.busy = true
		_, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
		require.ErrorIs(t, err, repositorysetups.ErrBusy)
		require.Zero(t, fx.bootstrap.calls)
	})
	t.Run("unrelated backend", func(t *testing.T) {
		fx := newRepositorySetupFixture(t)
		fx.projects.project.PipelineBackend = &model.ProjectPipelineBackend{DataSourceSlug: "other", TrackedBranch: "main", PipelineDir: "pipelines"}
		_, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
		require.Error(t, err)
		require.Zero(t, fx.bootstrap.calls)
	})
}

func TestRepositorySetupServiceFailsClosedForNilBootstrapResult(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	fx.bootstrap.results = []*gitprovider.RepositoryBootstrapResult{nil}
	_, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
	require.ErrorContains(t, err, "incomplete repository provenance")
	require.Equal(t, "repository_bootstrap_failed", fx.repo.row.FailureCode)
	require.Zero(t, fx.sources.creates)
}

func TestRepositorySetupServiceGitHubCreateStartsAuthorizationWithoutProviderMutation(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	actor := model.UserEventActor("member")
	pending, err := fx.service.Create(t.Context(), "acme", "project", githubSetupRequest(), actor)
	require.NoError(t, err)
	require.Equal(t, model.ProjectRepositorySetupStatusPending, pending.Status)
	require.Contains(t, pending.AuthorizationURL, "github.com")
	require.Equal(t, 1, fx.github.startCalls)
	require.Zero(t, fx.github.completeCalls)
	require.Zero(t, fx.bootstrap.calls)
	require.Zero(t, fx.sources.creates)
}

func TestRepositorySetupServiceGitHubCreateRejectsAPIKeyBeforeDurableMutation(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	_, err := fx.service.Create(t.Context(), "acme", "project", githubSetupRequest(), model.APIKeyEventActor("key"))
	require.ErrorContains(t, err, "user session")
	require.Nil(t, fx.repo.row)
	require.Zero(t, fx.github.startCalls)
	require.Zero(t, fx.github.completeCalls)
	require.Zero(t, fx.bootstrap.calls)
}

func TestRepositorySetupServiceGitHubRejectsMismatchedAuthorizationIntentWithoutMutation(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	fx.github.intentOrg, fx.github.intentSetup = "other", "other-setup"
	_, err := fx.service.CompleteGitHub(t.Context(), "acme", "repo-setup", model.UserEventActor("member"), model.GitHubRepositoryAuthorization{State: "state", Code: "code", InstallationID: 9})
	require.ErrorContains(t, err, "does not match")
	require.Nil(t, fx.repo.row)
	require.Zero(t, fx.github.completeCalls)
	require.Zero(t, fx.bootstrap.calls)
	require.Zero(t, fx.sources.creates)
}

func TestRepositorySetupServiceGitHubCompletesWithExactProviderBinding(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	fx.github.results = []*gitprovider.RepositoryBootstrapResult{githubBootstrapResult("42", "head")}
	actor := model.UserEventActor("member")
	pending, err := fx.service.Create(t.Context(), "acme", "project", githubSetupRequest(), actor)
	require.NoError(t, err)
	fx.github.intentSetup = pending.Slug

	completed, err := fx.service.CompleteGitHub(t.Context(), "acme", pending.Slug, actor, model.GitHubRepositoryAuthorization{State: "state", Code: "code", InstallationID: 9})
	require.NoError(t, err)
	require.Equal(t, model.ProjectRepositorySetupStatusAttached, completed.Status)
	require.Equal(t, "42", completed.RepositoryID)
	require.Equal(t, 1, fx.github.completeCalls)
	require.Equal(t, pending.Slug.String(), fx.github.requests[0].Marker)
	require.Empty(t, fx.github.requests[0].ExpectedRepositoryID)
	require.Zero(t, fx.bootstrap.calls)
	require.Equal(t, model.DataSourceTypeGithub, fx.sources.params[0].Type)
	var config map[string]string
	require.NoError(t, json.Unmarshal(fx.sources.params[0].ConfigJSON, &config))
	require.Equal(t, map[string]string{"repository_id": "42"}, config)
	require.Equal(t, "https://github.com/acme/pipelines.git", fx.projects.project.PipelineBackend.Repo)
}

func TestRepositorySetupServiceGitHubPartialFailureStartsNewAuthorizationWithoutSource(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	fx.github.results = []*gitprovider.RepositoryBootstrapResult{githubBootstrapResult("42", "")}
	fx.github.errs = []error{xerror.Unexpected().Detail("interrupted")}
	actor := model.UserEventActor("member")
	pending, err := fx.service.Create(t.Context(), "acme", "project", githubSetupRequest(), actor)
	require.NoError(t, err)
	fx.github.intentSetup = pending.Slug

	partial, err := fx.service.CompleteGitHub(t.Context(), "acme", pending.Slug, actor, model.GitHubRepositoryAuthorization{State: "state", Code: "code", InstallationID: 9})
	require.Error(t, err)
	require.Equal(t, model.ProjectRepositorySetupStatusPending, partial.Status)
	require.Equal(t, "42", partial.RepositoryID)
	require.Zero(t, fx.sources.creates)

	retry, err := fx.service.Create(t.Context(), "acme", "project", githubSetupRequest(), actor)
	require.NoError(t, err)
	require.Equal(t, pending.Slug, retry.Slug)
	require.Equal(t, 2, fx.github.startCalls)
	require.Equal(t, 1, fx.github.completeCalls)
	require.Zero(t, fx.sources.creates)
}

func TestRepositorySetupServiceGitHubResumesAfterAttachedPersistFailureWithoutNewAuthorization(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	fx.github.results = []*gitprovider.RepositoryBootstrapResult{githubBootstrapResult("42", "head")}
	fx.repo.failAttached = true
	actor := model.UserEventActor("member")
	pending, err := fx.service.Create(t.Context(), "acme", "project", githubSetupRequest(), actor)
	require.NoError(t, err)
	fx.github.intentSetup = pending.Slug
	_, err = fx.service.CompleteGitHub(t.Context(), "acme", pending.Slug, actor, model.GitHubRepositoryAuthorization{State: "state", Code: "code", InstallationID: 9})
	require.Error(t, err)

	fx.repo.failAttached = false
	resumed, err := fx.service.Create(t.Context(), "acme", "project", githubSetupRequest(), actor)
	require.NoError(t, err)
	require.Equal(t, model.ProjectRepositorySetupStatusAttached, resumed.Status)
	require.Equal(t, 1, fx.github.startCalls)
	require.Equal(t, 1, fx.github.completeCalls)
	require.Equal(t, 1, fx.sources.creates)
	require.Equal(t, 1, fx.projects.sets)
}

func TestRepositorySetupServiceGitHubAttachedRetryDoesNotCreateOrAuthorizeAgain(t *testing.T) {
	t.Parallel()
	fx := newRepositorySetupFixture(t)
	fx.github.results = []*gitprovider.RepositoryBootstrapResult{githubBootstrapResult("42", "head")}
	actor := model.UserEventActor("member")
	pending, err := fx.service.Create(t.Context(), "acme", "project", githubSetupRequest(), actor)
	require.NoError(t, err)
	fx.github.intentSetup = pending.Slug
	_, err = fx.service.CompleteGitHub(t.Context(), "acme", pending.Slug, actor, model.GitHubRepositoryAuthorization{State: "state", Code: "code", InstallationID: 9})
	require.NoError(t, err)

	_, err = fx.service.Create(t.Context(), "acme", "project", githubSetupRequest(), actor)
	require.NoError(t, err)
	require.Equal(t, 1, fx.github.startCalls)
	require.Equal(t, 1, fx.github.completeCalls)
	require.Equal(t, 1, fx.sources.creates)
	require.Equal(t, 1, fx.projects.sets)
}

type repositorySetupFixture struct {
	service     *RepositorySetupService
	policy      *fakePreparationPolicy
	repo        *fakeSetupRepo
	projects    *fakeSetupProjects
	sources     *fakeSetupSources
	bootstrap   *fakeSetupBootstrap
	github      *fakeSetupGitHub
	credentials *fakeSetupCredentials
}

func newRepositorySetupFixture(t *testing.T) repositorySetupFixture {
	t.Helper()
	repo := &fakeSetupRepo{}
	projects := &fakeSetupProjects{project: &model.Project{Slug: "project"}}
	sources := &fakeSetupSources{}
	bootstrap := &fakeSetupBootstrap{}
	github := &fakeSetupGitHub{intentOrg: "acme", intentSetup: "repo-setup"}
	credentials := &fakeSetupCredentials{connection: credentialssvc.OrganizationGitLabConnection{OrganizationSlug: "acme", Credential: credentialssvc.CredentialMetadata{Status: credentialssvc.CredentialStatusActive, OAuth: credentialssvc.CredentialOAuthMetadata{Scopes: []string{"api"}}}}}
	policy := &fakePreparationPolicy{}
	svc, err := NewRepositorySetupService(fakeSetupTXM{}, repo, projects, sources, bootstrap, github, credentials, fakeSetupEvents{}, policy)
	require.NoError(t, err)
	return repositorySetupFixture{svc, policy, repo, projects, sources, bootstrap, github, credentials}
}

func setupRequest() model.CreateProjectRepository {
	return model.CreateProjectRepository{Provider: model.DataSourceTypeGitlab, Owner: "acme/platform", RepositoryName: "pipelines", TrackedBranch: "main", PipelineDir: "pipelines", CreatePrivateRepository: true}
}

func githubSetupRequest() model.CreateProjectRepository {
	return model.CreateProjectRepository{Provider: model.DataSourceTypeGithub, Owner: "acme", RepositoryName: "pipelines", TrackedBranch: "main", PipelineDir: "pipelines", CreatePrivateRepository: true}
}

func bootstrapResult(id, sha string) *gitprovider.RepositoryBootstrapResult {
	return &gitprovider.RepositoryBootstrapResult{Repository: gitprovider.RepositoryRef{Provider: "gitlab", Owner: "acme/platform", Name: "pipelines"}, RepositoryID: id, RemoteURL: "https://gitlab.com/acme/platform/pipelines.git", HeadSHA: sha}
}

func githubBootstrapResult(id, sha string) *gitprovider.RepositoryBootstrapResult {
	return &gitprovider.RepositoryBootstrapResult{Repository: gitprovider.RepositoryRef{Provider: "github", Owner: "acme", Name: "pipelines"}, RepositoryID: id, RemoteURL: "https://github.com/acme/pipelines.git", HeadSHA: sha}
}

type fakeSetupTXM struct{}

func (fakeSetupTXM) RunInTx(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	return fn(ctx, nil)
}

func (fakeSetupTXM) RunRead(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	return fn(ctx, nil)
}

type fakeSetupRepo struct {
	row                *repositorysetups.Row
	archived           map[slug.Slug]*repositorysetups.Row
	leaseOwner         string
	busy, failAttached bool
}

func (r *fakeSetupRepo) Create(_ context.Context, _ postgres.DBTX, org, project, setup slug.Slug, request json.RawMessage) (bool, error) {
	if r.row != nil {
		return false, nil
	}
	r.row = &repositorysetups.Row{Slug: setup, Request: request, Status: model.ProjectRepositorySetupStatusPending.String()}
	return true, nil
}

func (r *fakeSetupRepo) Get(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repositorysetups.Row, error) {
	if r.row == nil {
		return nil, repositorysetups.ErrNotFound
	}
	c := *r.row
	return &c, nil
}

func (r *fakeSetupRepo) Claim(ctx context.Context, _ postgres.DBTX, org, project, setup slug.Slug, owner string) (*repositorysetups.Row, error) {
	if r.busy || r.leaseOwner != "" || r.row == nil || r.row.Slug != setup {
		return nil, repositorysetups.ErrBusy
	}
	r.leaseOwner = owner
	return r.Get(ctx, nil, org, project)
}

func (r *fakeSetupRepo) GetAttempt(ctx context.Context, db postgres.DBTX, org, project, setup slug.Slug) (*repositorysetups.Row, error) {
	if r.row != nil && r.row.Slug == setup {
		return r.Get(ctx, db, org, project)
	}
	if old := r.archived[setup]; old != nil {
		row := *old
		return &row, nil
	}
	return nil, repositorysetups.ErrNotFound
}

func (r *fakeSetupRepo) Abandon(_ context.Context, _ postgres.DBTX, _, _, setup slug.Slug, _ string) (*repositorysetups.Row, error) {
	if r.row == nil || r.row.Slug != setup {
		return nil, repositorysetups.ErrLeaseLost
	}
	row := *r.row
	now := time.Now()
	row.AbandonedAt = &now
	if r.archived == nil {
		r.archived = map[slug.Slug]*repositorysetups.Row{}
	}
	r.archived[setup] = &row
	r.row = nil
	r.leaseOwner = ""
	return &row, nil
}

func (r *fakeSetupRepo) Update(_ context.Context, _ postgres.DBTX, _, _ slug.Slug, _ string, row *repositorysetups.Row) error {
	if r.failAttached && row.Status == model.ProjectRepositorySetupStatusAttached.String() {
		return xerror.Unexpected().Detail("persist failed")
	}
	r.row.Result = row.Result
	r.row.Status = row.Status
	r.row.FailureCode = row.FailureCode
	return nil
}

func (r *fakeSetupRepo) Release(_ context.Context, _ postgres.DBTX, _, _ slug.Slug, owner string) error {
	if owner != r.leaseOwner {
		return repositorysetups.ErrLeaseLost
	}
	r.leaseOwner = ""
	return nil
}

type fakeSetupCredentials struct {
	connection                    credentialssvc.OrganizationGitLabConnection
	getErr, startErr, completeErr error
	install                       credentialssvc.CredentialInstall
	starts                        []credentialssvc.StartOAuthInstallParams
	completeCalls                 int
	onComplete                    func()
	consumeCalls                  int
	consumeErr                    error
}

func (f *fakeSetupCredentials) ConsumeOAuthInstall(context.Context, credentialssvc.ConsumeOAuthInstallParams) (credentialssvc.CredentialInstall, error) {
	f.consumeCalls++
	if f.consumeErr != nil {
		return credentialssvc.CredentialInstall{}, f.consumeErr
	}
	now := time.Now()
	f.install.ConsumedAt = &now
	return f.install, nil
}

func (f *fakeSetupCredentials) GetOrganizationGitLabConnection(context.Context, slug.Slug) (credentialssvc.OrganizationGitLabConnection, error) {
	return f.connection, f.getErr
}

func (f *fakeSetupCredentials) CreateOrganizationGitLabConnection(context.Context, credentialssvc.CreateOrganizationGitLabConnectionParams) (credentialssvc.OrganizationGitLabConnection, error) {
	if f.getErr != nil {
		f.connection = credentialssvc.OrganizationGitLabConnection{OrganizationSlug: "acme", Credential: credentialssvc.CredentialMetadata{Status: credentialssvc.CredentialStatusPending}}
		f.getErr = nil
	}
	return f.connection, nil
}

func (f *fakeSetupCredentials) StartOAuthInstall(_ context.Context, params credentialssvc.StartOAuthInstallParams) (credentialssvc.ProviderConnectorInstall, error) {
	f.starts = append(f.starts, params)
	f.install = credentialssvc.CredentialInstall{Owner: params.Owner, Provider: params.Provider, Intent: params.Intent, Actor: params.Actor, RepositorySetupSlug: params.RepositorySetupSlug, RepositorySetupProjectSlug: params.RepositorySetupProjectSlug}
	return credentialssvc.ProviderConnectorInstall{AuthorizationURL: "https://gitlab.com/oauth/authorize?state=isolated"}, f.startErr
}

func (f *fakeSetupCredentials) FindOAuthInstall(context.Context, credentialssvc.FindOAuthInstallParams) (credentialssvc.CredentialInstall, error) {
	return f.install, nil
}

func (f *fakeSetupCredentials) CompleteOAuthCallback(context.Context, credentialssvc.CompleteOAuthCallbackParams) (credentialssvc.CompleteOAuthCallbackResult, error) {
	f.completeCalls++
	if f.onComplete != nil {
		f.onComplete()
	}
	if f.completeErr == nil {
		f.connection.Credential.Status = credentialssvc.CredentialStatusActive
		f.connection.Credential.OAuth.Scopes = []string{"api"}
	}
	return credentialssvc.CompleteOAuthCallbackResult{Owner: f.install.Owner}, f.completeErr
}

type fakeSetupProjects struct {
	project *model.Project
	sets    int
}

func (p *fakeSetupProjects) GetProject(context.Context, slug.Slug, slug.Slug) (*model.Project, error) {
	return p.project, nil
}

func (p *fakeSetupProjects) SetProjectPipelineBackend(_ context.Context, _, _ slug.Slug, v model.SetProjectPipelineBackend) (*model.Project, error) {
	p.sets++
	p.project.PipelineBackend = &model.ProjectPipelineBackend{Repo: v.ExpectedRepository, DataSourceSlug: v.DataSourceSlug, TrackedBranch: v.TrackedBranch, PipelineDir: v.PipelineDir}
	return p.project, nil
}

type fakeSetupSources struct {
	ds      *datasourcessvc.DataSource
	creates int
	params  []*datasourcessvc.CreateParams
}

func (s *fakeSetupSources) FindBySlug(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error) {
	if s.ds == nil {
		return nil, datasourcessvc.ErrDataSourceNotFound
	}
	return s.ds, nil
}

func (s *fakeSetupSources) Create(_ context.Context, _ slug.Slug, _ model.EventActor, p *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
	s.creates++
	saved := *p
	saved.ConfigJSON = append(json.RawMessage(nil), p.ConfigJSON...)
	s.params = append(s.params, &saved)
	var selection map[string]string
	if err := json.Unmarshal(p.ConfigJSON, &selection); err != nil {
		return nil, err
	}
	id := selection["project_id"]
	if id == "" {
		id = selection["repository_id"]
	}
	config, err := json.Marshal(gitconnector.ProviderConfig{Repository: gitconnector.ProviderRepository{ID: id, Name: p.Name, FullName: "acme/platform/" + p.Name, WebURL: "https://gitlab.com/acme/platform/" + p.Name}})
	if err != nil {
		return nil, err
	}
	s.ds = &datasourcessvc.DataSource{Slug: p.Slug, Type: p.Type, Config: config, IsActive: true, ReadOnly: false}
	return s.ds, nil
}

type fakeSetupBootstrap struct {
	results  []*gitprovider.RepositoryBootstrapResult
	errs     []error
	calls    int
	requests []gitprovider.RepositoryBootstrapRequest
}

func (b *fakeSetupBootstrap) BootstrapGitLabRepository(_ context.Context, _ slug.Slug, request gitprovider.RepositoryBootstrapRequest) (*gitprovider.RepositoryBootstrapResult, error) {
	i := b.calls
	b.calls++
	b.requests = append(b.requests, request)
	var r *gitprovider.RepositoryBootstrapResult
	var e error
	if i < len(b.results) {
		r = b.results[i]
	}
	if i < len(b.errs) {
		e = b.errs[i]
	}
	return r, e
}

type fakeSetupEvents struct{}

func (fakeSetupEvents) EnqueueRecord(context.Context, postgres.DBTX, slug.Slug, model.EventActor, eventrecorder.Event) (workerproducer.EventRecordJob, bool) {
	return workerproducer.EventRecordJob{}, true
}

func (fakeSetupEvents) EnqueueRequired(context.Context, postgres.DBTX, slug.Slug, model.EventActor, eventrecorder.Event) (workerproducer.EventRecordJob, error) {
	return workerproducer.EventRecordJob{}, nil
}

func TestRepositorySetupRefusesChangedIdentityAndUnsafeMetadata(t *testing.T) {
	for _, mutate := range []func(*gitprovider.RepositoryBootstrapResult){
		func(r *gitprovider.RepositoryBootstrapResult) { r.RepositoryID = "99" },
		func(r *gitprovider.RepositoryBootstrapResult) {
			r.RemoteURL = "https://user:isolated-secret@gitlab.com/acme/platform/pipelines.git"
		},
		func(r *gitprovider.RepositoryBootstrapResult) { r.RemoteURL = "https://gitlab.com/acme/other.git" },
	} {
		fx := newRepositorySetupFixture(t)
		first := bootstrapResult("42", "")
		second := bootstrapResult("42", "head")
		mutate(second)
		fx.bootstrap.results = []*gitprovider.RepositoryBootstrapResult{first, second}
		fx.bootstrap.errs = []error{xerror.Unexpected().Detail("interrupted"), nil}
		_, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
		require.Error(t, err)
		_, err = fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
		require.Error(t, err)
		got, err := fx.service.Get(t.Context(), "acme", "project")
		require.NoError(t, err)
		require.Equal(t, "42", got.RepositoryID)
		require.NotContains(t, string(fx.repo.row.Result), "isolated-secret")
		require.Zero(t, fx.sources.creates)
	}
}

func TestAttachedRepositorySetupNeverRecreatesMissingSource(t *testing.T) {
	fx := newRepositorySetupFixture(t)
	fx.bootstrap.results = []*gitprovider.RepositoryBootstrapResult{bootstrapResult("42", "head")}
	_, err := fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
	require.NoError(t, err)
	fx.sources.ds = nil
	_, err = fx.service.Create(t.Context(), "acme", "project", setupRequest(), model.SystemEventActor())
	require.Error(t, err)
	require.Equal(t, 1, fx.sources.creates)
	require.Equal(t, 1, fx.bootstrap.calls)
}

func TestRepositorySetupCannotOverwriteBackendConfiguredDuringProviderCall(t *testing.T) {
	svc := &service{txm: fakeSetupTXM{}, projectsRepo: &fakeProjectsRepo{
		findBySlugFn: func(_ context.Context, _ postgres.DBTX, org, project slug.Slug) (*projects.Project, error) {
			require.Equal(t, slug.Slug("acme"), org)
			require.Equal(t, slug.Slug("project"), project)
			return &projects.Project{PipelineBackendDataSourceSlug: ptr.Ptr(slug.Slug("other-backend"))}, nil
		},
		setBackendFn: func(context.Context, postgres.DBTX, *projects.SetPipelineBackendParams) error {
			t.Fatal("existing backend must not be replaced")
			return nil
		},
	}}
	err := svc.persistProjectPipelineBackendIdentity(t.Context(), "acme", "project", resolvedProjectPipelineBackend{requireUnconfigured: true, dataSourceSlug: "new-source"})
	require.ErrorContains(t, err, "configured during repository setup")
}

func (r *fakeSetupRepo) ProjectForSetup(_ context.Context, _ postgres.DBTX, _, setup slug.Slug) (slug.Slug, error) {
	if r.row == nil || r.row.Slug != setup {
		return "", repositorysetups.ErrNotFound
	}
	return "project", nil
}

type fakeSetupGitHub struct {
	intentOrg, intentSetup    slug.Slug
	startURL                  string
	startErr, intentErr       error
	results                   []*gitprovider.RepositoryBootstrapResult
	errs                      []error
	startCalls, completeCalls int
	requests                  []gitprovider.RepositoryBootstrapRequest
	consumeCalls              int
	consumeErr                error
}

func (g *fakeSetupGitHub) ConsumeRejectedInstall(context.Context, slug.Slug, slug.Slug, model.EventActor, string) error {
	g.consumeCalls++
	return g.consumeErr
}

func (g *fakeSetupGitHub) StartRepositorySetup(_ context.Context, org slug.Slug, _ model.EventActor, setup slug.Slug) (string, error) {
	g.startCalls++
	if g.startErr != nil {
		return "", g.startErr
	}
	if g.startURL != "" {
		return g.startURL, nil
	}
	return "https://github.com/apps/test/installations/new?state=isolated", nil
}

func (g *fakeSetupGitHub) RepositorySetupForState(context.Context, model.EventActor, string) (slug.Slug, slug.Slug, error) {
	if g.intentErr != nil {
		return "", "", g.intentErr
	}
	return g.intentOrg, g.intentSetup, nil
}

func (g *fakeSetupGitHub) CompleteRepositorySetup(_ context.Context, _, _ slug.Slug, _ model.EventActor, _, _ string, _ int64, request gitprovider.RepositoryBootstrapRequest) (*gitprovider.RepositoryBootstrapResult, error) {
	i := g.completeCalls
	g.completeCalls++
	g.requests = append(g.requests, request)
	var result *gitprovider.RepositoryBootstrapResult
	var err error
	if i < len(g.results) {
		result = g.results[i]
	}
	if i < len(g.errs) {
		err = g.errs[i]
	}
	return result, err
}

func TestRepositorySetupRejectsInvalidGitTargetsBeforeProvisioning(t *testing.T) {
	for _, branch := range []string{"-main", "main.lock", "release//v1", "release/../main", "release\nmain", "main@{1}", "main~1"} {
		request := setupRequest()
		request.TrackedBranch = branch
		_, err := normalizeRepositorySetupRequest(request)
		require.Error(t, err, branch)
	}
	for _, dir := range []string{".git", "pipeline/.GiT/templates", "pipeline\nfiles"} {
		request := setupRequest()
		request.PipelineDir = dir
		_, err := normalizeRepositorySetupRequest(request)
		require.Error(t, err, dir)
	}
	request := setupRequest()
	request.Provider = model.DataSourceTypeGithub
	request.Owner = "customer"
	request.RepositoryName = ".github"
	request.TrackedBranch = "release/v1"
	_, err := normalizeRepositorySetupRequest(request)
	require.NoError(t, err)
}

func TestRepositorySetupDelegatesSourceCreationToPolicy(t *testing.T) {
	t.Parallel()
	f := newRepositorySetupFixture(t)
	f.bootstrap.results = []*gitprovider.RepositoryBootstrapResult{bootstrapResult("42", "head")}
	setup, err := f.service.Create(t.Context(), "acme", "project", setupRequest(), model.UserEventActor("operator"))
	require.NoError(t, err)
	require.Equal(t, model.ProjectRepositorySetupStatusAttached, setup.Status)
	require.Len(t, f.sources.params, 1)
	require.Equal(t, 1, f.policy.creates)
	require.Equal(t, slug.Slug("acme"), f.policy.org)
	require.Equal(t, slug.Slug("project"), f.policy.project)
	require.Equal(t, model.UserEventActor("operator"), f.policy.actor)
	require.Equal(t, f.sources.params[0], f.policy.params)
}

func TestPreparationRefusalPreventsRepositorySideEffects(t *testing.T) {
	t.Parallel()
	f := newRepositorySetupFixture(t)
	f.policy.checkError = xerror.ValidationFailed().Detail("synthetic preparation refusal")
	_, err := f.service.Create(t.Context(), "acme", "project", setupRequest(), model.UserEventActor("operator"))
	require.ErrorIs(t, err, f.policy.checkError)
	require.Zero(t, f.bootstrap.calls)
	require.Zero(t, f.sources.creates)
}

func TestRepositorySetupRequiresPreparationPolicy(t *testing.T) {
	t.Parallel()
	f := newRepositorySetupFixture(t)
	_, err := NewRepositorySetupService(fakeSetupTXM{}, f.repo, f.projects, f.sources, f.bootstrap, f.github, f.credentials, fakeSetupEvents{}, nil)
	require.Error(t, err)
}

type fakePreparationPolicy struct {
	checkError   error
	creates      int
	org, project slug.Slug
	actor        model.EventActor
	params       *datasourcessvc.CreateParams
}

func (f *fakePreparationPolicy) CheckSetup(context.Context, slug.Slug, slug.Slug) error {
	return f.checkError
}

func (f *fakePreparationPolicy) CreateRepositorySource(ctx context.Context, sources RepositorySourceService, org, project slug.Slug, actor model.EventActor, params *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
	f.creates++
	f.org, f.project, f.actor, f.params = org, project, actor, params
	return sources.Create(ctx, org, actor, params)
}

func (*fakeSetupSources) CreateProvisioned(context.Context, slug.Slug, model.EventActor, *datasourcessvc.CreateParams, datasourcessvc.SourceProvisioner) (*datasourcessvc.DataSource, error) {
	return nil, xerror.Unexpected().Detail("unexpected trusted provisioning")
}
