package githubappsvc

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/githubapps"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

func TestInstallStateIsActorOrganizationBoundAndSingleUse(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{}
	provider := &fakeProvider{verified: githubapps.VerifiedInstallation{
		InstallationID: 9, AccountID: 2,
		AccountLogin: "acme", AccountType: "Organization", RepositorySelection: "selected",
		Repositories: []githubapps.RepositoryBinding{{ID: 42, FullName: "acme/pipelines", OwnerLogin: "acme", Name: "pipelines"}},
	}}
	events := &fakeEvents{}
	svc, err := New(testutil.FakeTransactionManager{}, repo, provider, events, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	actor := model.UserEventActor(must.Slug("alice"))

	installURL, err := svc.Start(t.Context(), must.Slug("acme"), actor)
	require.NoError(t, err)
	parsed, err := url.Parse(installURL)
	require.NoError(t, err)
	state := parsed.Query().Get("state")
	require.NotEmpty(t, state)
	require.Equal(t, "alice", repo.intent.Actor)
	require.Equal(t, must.Slug("acme"), repo.intent.Organization)

	_, err = svc.Complete(t.Context(), model.UserEventActor(must.Slug("mallory")), state, "code", 9)
	require.ErrorIs(t, err, ErrInvalidInstall)
	require.Zero(t, provider.calls)

	connection, err := svc.Complete(t.Context(), actor, state, "code", 9)
	require.NoError(t, err)
	require.Equal(t, must.Slug("acme"), connection.Organization)
	require.Equal(t, int64(42), connection.Repositories[0].ID)
	require.Equal(t, 1, repo.prunes)
	require.True(t, repo.intent.Consumed)
	require.Equal(t, 1, events.required)

	_, err = svc.Complete(t.Context(), actor, state, "code", 9)
	require.ErrorIs(t, err, ErrInvalidInstall)
	require.Equal(t, 1, provider.calls)

	installURL, err = svc.Start(t.Context(), must.Slug("acme"), actor)
	require.NoError(t, err)
	parsed, err = url.Parse(installURL)
	require.NoError(t, err)
	_, err = svc.Complete(t.Context(), actor, parsed.Query().Get("state"), "code", 9)
	require.NoError(t, err)
	require.Equal(t, 2, events.required)
	require.NoError(t, svc.Disconnect(t.Context(), must.Slug("acme"), actor))
	require.Equal(t, 3, events.required)
}

func TestRejectedInstallConsumesOnlyMatchingLiveUserStateWithoutProviderWork(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong-user", "wrong-org", "wrong-setup", "expired", "consumed"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now()
			repo := &fakeRepository{intent: githubapps.Intent{ID: 1, Organization: "acme", RepositorySetupSlug: "setup-one", Actor: "alice", ExpiresAt: now.Add(time.Minute)}}
			provider := &fakeProvider{}
			svc, err := New(testutil.FakeTransactionManager{}, repo, provider, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
			require.NoError(t, err)
			svc.now = func() time.Time { return now }
			actor, org, setup := model.UserEventActor("alice"), slug.Slug("acme"), slug.Slug("setup-one")
			switch scenario {
			case "wrong-user":
				actor = model.UserEventActor("other")
			case "wrong-org":
				org = "other"
			case "wrong-setup":
				setup = "other"
			case "expired":
				repo.intent.ExpiresAt = now
			case "consumed":
				repo.intent.Consumed = true
			}
			err = svc.ConsumeRejectedInstall(t.Context(), org, setup, actor, "synthetic-state")
			if scenario == "valid" {
				require.NoError(t, err)
				require.True(t, repo.intent.Consumed)
				require.ErrorIs(t, svc.ConsumeRejectedInstall(t.Context(), org, setup, actor, "synthetic-state"), ErrInvalidInstall)
			} else {
				require.ErrorIs(t, err, ErrInvalidInstall)
				require.Equal(t, scenario == "consumed", repo.intent.Consumed)
			}
			require.Zero(t, provider.calls)
			require.Zero(t, provider.tokenCalls)
			require.Zero(t, repo.verified.InstallationID)
		})
	}
}

func TestTokenRejectsRepositoryOutsideOrganizationBinding(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{
		installation: &githubapps.Installation{InstallationID: 9},
		repositories: []githubapps.RepositoryBinding{{ID: 42}},
	}
	provider := &fakeProvider{}
	svc, err := New(testutil.FakeTransactionManager{}, repo, provider, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)

	_, err = svc.Token(t.Context(), must.Slug("acme"), []int64{99})
	require.ErrorIs(t, err, ErrInvalidInstall)
	require.Zero(t, provider.tokenCalls)
}

func TestTokenPreservesProviderContentsPermission(t *testing.T) {
	t.Parallel()
	provider := &fakeProvider{token: InstallationToken{Token: "installation-token", ExpiresAt: time.Now().Add(time.Hour), ContentsPermission: "write", PullRequestsPermission: "write", PrincipalID: 77, PrincipalLogin: "braidplane[bot]"}}
	svc, err := New(testutil.FakeTransactionManager{}, &fakeRepository{
		installation: &githubapps.Installation{InstallationID: 9, AccountID: 2}, repositories: []githubapps.RepositoryBinding{{ID: 42}},
	}, provider, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)

	token, err := svc.Token(t.Context(), must.Slug("acme"), []int64{42})
	require.NoError(t, err)
	expected := provider.token
	expected.InstallationID = 9
	expected.AccountID = 2
	require.Equal(t, expected, token)
}

func TestCompleteRejectsExpiredStateAndInstallationMismatch(t *testing.T) {
	t.Parallel()
	actor := model.UserEventActor(must.Slug("alice"))
	for _, tc := range []struct {
		name           string
		installationID int64
		expired        bool
	}{
		{name: "expired state", installationID: 9, expired: true},
		{name: "installation mismatch", installationID: 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := &fakeRepository{}
			provider := &fakeProvider{verified: githubapps.VerifiedInstallation{InstallationID: tc.installationID}}
			svc, err := New(testutil.FakeTransactionManager{}, repo, provider, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
			require.NoError(t, err)
			now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
			svc.now = func() time.Time { return now }
			installURL, err := svc.Start(t.Context(), must.Slug("acme"), actor)
			require.NoError(t, err)
			parsed, err := url.Parse(installURL)
			require.NoError(t, err)
			if tc.expired {
				now = now.Add(installTTL + time.Second)
			}
			_, err = svc.Complete(t.Context(), actor, parsed.Query().Get("state"), "code", 9)
			require.ErrorIs(t, err, ErrInvalidInstall)
		})
	}
}

func TestExistingInstallationAuthorizationBindsOnlyRequestedAccount(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{}
	provider := &fakeProvider{verified: githubapps.VerifiedInstallation{InstallationID: 9, AccountID: 2, AccountLogin: "acme", AccountType: "Organization", RepositorySelection: "selected"}}
	svc, err := New(testutil.FakeTransactionManager{}, repo, provider, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)

	link, err := svc.StartExistingAuthorization(t.Context(), "acme", model.UserEventActor("alice"), "Acme")
	require.NoError(t, err)
	parsed, err := url.Parse(link)
	require.NoError(t, err)
	require.Equal(t, "https", parsed.Scheme)
	require.Equal(t, "acme", repo.intent.RequestedAccountLogin)

	connection, err := svc.Complete(t.Context(), model.UserEventActor("alice"), parsed.Query().Get("state"), "code", 0)
	require.NoError(t, err)
	require.Equal(t, int64(9), connection.InstallationID)
	require.Equal(t, "acme", provider.authorizedAccount)
	require.True(t, repo.intent.Consumed)
	require.Zero(t, repo.prunes, "a user-scoped repository list must not remove other installation bindings")
}

func TestExistingInstallationAuthorizationRejectsInvalidAccountAndOldCallbackWithoutID(t *testing.T) {
	t.Parallel()
	svc, err := New(testutil.FakeTransactionManager{}, &fakeRepository{}, &fakeProvider{}, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)
	_, err = svc.StartExistingAuthorization(t.Context(), "acme", model.UserEventActor("alice"), "https://github.com/acme")
	require.ErrorIs(t, err, ErrInvalidInstall)

	link, err := svc.Start(t.Context(), "acme", model.UserEventActor("alice"))
	require.NoError(t, err)
	parsed, err := url.Parse(link)
	require.NoError(t, err)
	_, err = svc.Complete(t.Context(), model.UserEventActor("alice"), parsed.Query().Get("state"), "code", 0)
	require.ErrorIs(t, err, ErrInvalidInstall)
}

func TestTokenRejectsInactiveInstallation(t *testing.T) {
	t.Parallel()
	now := time.Now()
	for _, installation := range []*githubapps.Installation{{InstallationID: 9, SuspendedAt: &now}, {InstallationID: 9, DeletedAt: &now}} {
		provider := &fakeProvider{}
		svc, err := New(testutil.FakeTransactionManager{}, &fakeRepository{installation: installation, repositories: []githubapps.RepositoryBinding{{ID: 42}}}, provider, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
		require.NoError(t, err)
		_, err = svc.Token(t.Context(), must.Slug("acme"), []int64{42})
		require.ErrorIs(t, err, ErrInvalidInstall)
		require.Zero(t, provider.tokenCalls)
	}
}

func TestNewRejectsNonGitHubInstallationURL(t *testing.T) {
	t.Parallel()
	_, err := New(testutil.FakeTransactionManager{}, &fakeRepository{}, &fakeProvider{}, &fakeEvents{}, "https://example.test/apps/braidplane/installations/new")
	require.Error(t, err)
}

func TestIntentLookupPropagatesRepositoryErrors(t *testing.T) {
	t.Parallel()
	repoErr := xerror.Unexpected().Detail("database unavailable")
	repo := &fakeRepository{intentErr: repoErr}
	svc, err := New(testutil.FakeTransactionManager{}, repo, &fakeProvider{}, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)
	actor := model.UserEventActor(must.Slug("alice"))
	_, err = svc.OrganizationForState(t.Context(), actor, "state")
	require.ErrorIs(t, err, repoErr)
	_, err = svc.Complete(t.Context(), actor, "state", "code", 9)
	require.ErrorIs(t, err, repoErr)
}

func TestCompleteRejectsInstallationBoundToAnotherOrganization(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{upsertErr: &pgconn.PgError{Code: "23505", ConstraintName: "github_app_installations_installation_id_key"}}
	provider := &fakeProvider{verified: githubapps.VerifiedInstallation{InstallationID: 9, AccountID: 2, AccountLogin: "acme", AccountType: "Organization", RepositorySelection: "selected"}}
	svc, err := New(testutil.FakeTransactionManager{}, repo, provider, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)
	actor := model.UserEventActor(must.Slug("alice"))
	installURL, err := svc.Start(t.Context(), must.Slug("acme"), actor)
	require.NoError(t, err)
	parsed, err := url.Parse(installURL)
	require.NoError(t, err)

	_, err = svc.Complete(t.Context(), actor, parsed.Query().Get("state"), "code", 9)
	require.ErrorIs(t, err, ErrInvalidInstall)
	require.False(t, repo.intent.Consumed)
}

func TestCompleteRejectsIncompleteProviderIdentity(t *testing.T) {
	t.Parallel()
	actor := model.UserEventActor(must.Slug("alice"))
	repo := &fakeRepository{}
	svc, err := New(testutil.FakeTransactionManager{}, repo, &fakeProvider{verified: githubapps.VerifiedInstallation{InstallationID: 9}}, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)
	installURL, err := svc.Start(t.Context(), must.Slug("acme"), actor)
	require.NoError(t, err)
	parsed, err := url.Parse(installURL)
	require.NoError(t, err)

	_, err = svc.Complete(t.Context(), actor, parsed.Query().Get("state"), "code", 9)
	require.ErrorIs(t, err, ErrInvalidInstall)
	require.False(t, repo.intent.Consumed)
}

func TestDisconnectDoesNotAuditMissingConnection(t *testing.T) {
	t.Parallel()
	events := &fakeEvents{}
	svc, err := New(testutil.FakeTransactionManager{}, &fakeRepository{disconnectErr: githubapps.ErrNotFound}, &fakeProvider{}, events, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)

	err = svc.Disconnect(t.Context(), must.Slug("acme"), model.UserEventActor(must.Slug("alice")))
	require.ErrorIs(t, err, githubapps.ErrNotFound)
	require.Zero(t, events.required)
}

func TestApplyLifecycleAuditsConnectedOrganizationAsSystem(t *testing.T) {
	t.Parallel()
	repo := &fakeRepository{organization: must.Slug("acme")}
	events := &fakeEvents{}
	svc, err := New(testutil.FakeTransactionManager{}, repo, &fakeProvider{}, events, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)

	err = svc.ApplyLifecycle(t.Context(), githubprovider.InstallationLifecycleEvent{Action: "suspend", InstallationID: 9})
	require.NoError(t, err)
	require.Equal(t, 1, repo.lifecycleUpdates)
	require.Equal(t, 1, events.required)
	require.Equal(t, must.Slug("acme"), events.organization)
	require.Equal(t, model.SystemEventActor(), events.actor)
}

func TestApplyLifecycleIgnoresUnconnectedInstallation(t *testing.T) {
	t.Parallel()
	events := &fakeEvents{}
	svc, err := New(testutil.FakeTransactionManager{}, &fakeRepository{organizationErr: githubapps.ErrNotFound}, &fakeProvider{}, events, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)

	require.NoError(t, svc.ApplyLifecycle(t.Context(), githubprovider.InstallationLifecycleEvent{Action: "deleted", InstallationID: 9}))
	require.Zero(t, events.required)
}

func TestApplyLifecycleIgnoresAlreadyRemovedRepository(t *testing.T) {
	t.Parallel()
	events := &fakeEvents{}
	repo := &fakeRepository{organization: must.Slug("acme"), removeErr: githubapps.ErrNotFound}
	svc, err := New(testutil.FakeTransactionManager{}, repo, &fakeProvider{}, events, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)

	require.NoError(t, svc.ApplyLifecycle(t.Context(), githubprovider.InstallationLifecycleEvent{Action: "removed", InstallationID: 9, Removed: []int64{42}}))
	require.Zero(t, events.required)
}

func TestApplyLifecycleIgnoresRepeatedTransition(t *testing.T) {
	t.Parallel()
	events := &fakeEvents{}
	repo := &fakeRepository{organization: must.Slug("acme"), updateErr: githubapps.ErrNotFound}
	svc, err := New(testutil.FakeTransactionManager{}, repo, &fakeProvider{}, events, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)

	require.NoError(t, svc.ApplyLifecycle(t.Context(), githubprovider.InstallationLifecycleEvent{Action: "suspend", InstallationID: 9}))
	require.Zero(t, events.required)
}

func TestTokenPropagatesConnectionReadError(t *testing.T) {
	t.Parallel()
	repoErr := xerror.Unexpected().Detail("database unavailable")
	svc, err := New(testutil.FakeTransactionManager{}, &fakeRepository{installationErr: repoErr}, &fakeProvider{}, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)

	_, err = svc.Token(t.Context(), must.Slug("acme"), []int64{42})
	require.ErrorIs(t, err, repoErr)
}

type fakeProvider struct {
	verified          githubapps.VerifiedInstallation
	token             InstallationToken
	calls             int
	tokenCalls        int
	authorizedAccount string
}

func (*fakeProvider) AuthorizationURL(state string) (string, error) {
	return "https://github.com/login/oauth/authorize?state=" + state, nil
}

func (p *fakeProvider) VerifyInstallation(context.Context, string, int64) (githubapps.VerifiedInstallation, error) {
	p.calls++
	return p.verified, nil
}

func (p *fakeProvider) VerifyAuthorizedInstallation(_ context.Context, _, account string, _ int64) (githubapps.VerifiedInstallation, error) {
	p.calls++
	p.authorizedAccount = account
	return p.verified, nil
}

func (p *fakeProvider) InstallationToken(context.Context, int64, []int64) (InstallationToken, error) {
	p.tokenCalls++
	if p.token.Token != "" {
		return p.token, nil
	}
	return InstallationToken{Token: "token", ExpiresAt: time.Now(), ContentsPermission: "write"}, nil
}

type fakeRepository struct {
	prunes           int
	intent           githubapps.Intent
	verified         githubapps.VerifiedInstallation
	installation     *githubapps.Installation
	installationErr  error
	repositories     []githubapps.RepositoryBinding
	intentErr        error
	upsertErr        error
	disconnectErr    error
	organization     slug.Slug
	organizationErr  error
	lifecycleUpdates int
	updateErr        error
	removeErr        error
}

func (r *fakeRepository) CreateIntent(_ context.Context, _ postgres.DBTX, org slug.Slug, actor, _ string, expires time.Time, setup slug.Slug, requestedAccountLogin string) error {
	r.intent = githubapps.Intent{RepositorySetupSlug: setup, RequestedAccountLogin: requestedAccountLogin, ID: 1, OrganizationID: 1, Organization: org, Actor: actor, ExpiresAt: expires}
	return nil
}

func (r *fakeRepository) LockIntent(context.Context, postgres.DBTX, string) (githubapps.Intent, error) {
	return r.intent, r.intentErr
}

func (r *fakeRepository) GetIntent(context.Context, postgres.DBTX, string) (githubapps.Intent, error) {
	return r.intent, r.intentErr
}

func (r *fakeRepository) ConsumeIntent(_ context.Context, _ postgres.DBTX, _ int) error {
	r.intent.Consumed = true
	return nil
}

func (r *fakeRepository) UpsertInstallation(_ context.Context, _ postgres.DBTX, _ githubapps.Intent, verified githubapps.VerifiedInstallation) (int64, error) {
	r.verified = verified
	return 1, r.upsertErr
}

func (*fakeRepository) UpsertRepository(context.Context, postgres.DBTX, int32, int64, githubapps.RepositoryBinding) error {
	return nil
}

func (r *fakeRepository) RemoveMissingRepositories(context.Context, postgres.DBTX, int32, int64, []int64) error {
	r.prunes++
	return nil
}

func (r *fakeRepository) GetInstallation(context.Context, postgres.DBTX, slug.Slug) (*githubapps.Installation, error) {
	return r.installation, r.installationErr
}

func (r *fakeRepository) ListRepositories(context.Context, postgres.DBTX, slug.Slug) ([]githubapps.RepositoryBinding, error) {
	return r.repositories, nil
}

func (r *fakeRepository) Disconnect(context.Context, postgres.DBTX, slug.Slug) error {
	return r.disconnectErr
}

func (r *fakeRepository) OrganizationForInstallation(context.Context, postgres.DBTX, int64) (slug.Slug, error) {
	return r.organization, r.organizationErr
}

func (*fakeRepository) Resolve(context.Context, postgres.DBTX, int64, int64) (slug.Slug, error) {
	return "acme", nil
}

func (r *fakeRepository) UpdateLifecycle(context.Context, postgres.DBTX, string, int64) error {
	r.lifecycleUpdates++
	return r.updateErr
}

func (*fakeRepository) AddRepository(context.Context, postgres.DBTX, int64, githubapps.RepositoryBinding) error {
	return nil
}

func (r *fakeRepository) RemoveRepository(context.Context, postgres.DBTX, int64, int64) error {
	return r.removeErr
}

type fakeEvents struct {
	required     int
	organization slug.Slug
	actor        model.EventActor
}

func (*fakeEvents) EnqueueRecord(context.Context, postgres.DBTX, slug.Slug, model.EventActor, eventrecorder.Event) (workerproducer.EventRecordJob, bool) {
	return workerproducer.EventRecordJob{}, true
}

func (e *fakeEvents) EnqueueRequired(_ context.Context, _ postgres.DBTX, organization slug.Slug, actor model.EventActor, _ eventrecorder.Event) (workerproducer.EventRecordJob, error) {
	e.required++
	e.organization = organization
	e.actor = actor
	return workerproducer.EventRecordJob{}, nil
}

func (p *fakeProvider) VerifyRepositoryInstallation(ctx context.Context, code string, id int64, request gitprovider.RepositoryBootstrapRequest) (githubapps.VerifiedInstallation, *gitprovider.RepositoryBootstrapResult, error) {
	verified, err := p.VerifyInstallation(ctx, code, id)
	return verified, &gitprovider.RepositoryBootstrapResult{Repository: gitprovider.RepositoryRef{Provider: "github", Owner: request.Owner, Name: request.Name}, RepositoryID: "42", HeadSHA: "head", RemoteURL: "https://github.com/" + request.Owner + "/" + request.Name + ".git"}, err
}

func TestRepositoryInstallIntentBindsActorOrganizationAndSetup(t *testing.T) {
	repo := &fakeRepository{}
	provider := &fakeProvider{verified: githubapps.VerifiedInstallation{InstallationID: 9, AccountID: 2, AccountLogin: "acme", AccountType: "Organization", RepositorySelection: "selected", Repositories: []githubapps.RepositoryBinding{{ID: 42, FullName: "acme/pipelines", OwnerLogin: "acme", Name: "pipelines"}}}}
	svc, err := New(testutil.FakeTransactionManager{}, repo, provider, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)
	actor := model.UserEventActor("alice")
	authURL, err := svc.StartRepositorySetup(t.Context(), "acme", actor, "repo-isolated")
	require.NoError(t, err)
	parsed, err := url.Parse(authURL)
	require.NoError(t, err)
	state := parsed.Query().Get("state")
	org, setup, err := svc.RepositorySetupForState(t.Context(), actor, state)
	require.NoError(t, err)
	require.Equal(t, slug.Slug("acme"), org)
	require.Equal(t, slug.Slug("repo-isolated"), setup)
	request := gitprovider.RepositoryBootstrapRequest{Owner: "acme", Name: "pipelines", Marker: "repo-isolated"}
	_, err = svc.Complete(t.Context(), actor, state, "code", 9)
	require.ErrorIs(t, err, ErrInvalidInstall)
	require.Zero(t, provider.calls)
	for _, target := range []struct {
		org, setup slug.Slug
		actor      model.EventActor
	}{
		{"other", "repo-isolated", actor}, {"acme", "wrong-setup", actor}, {"acme", "repo-isolated", model.UserEventActor("other")},
	} {
		_, err = svc.CompleteRepositorySetup(t.Context(), target.org, target.setup, target.actor, state, "code", 9, request)
		require.ErrorIs(t, err, ErrInvalidInstall)
		require.Zero(t, provider.calls)
	}
	result, err := svc.CompleteRepositorySetup(t.Context(), "acme", "repo-isolated", actor, state, "code", 9, request)
	require.NoError(t, err)
	require.Equal(t, "42", result.RepositoryID)
	require.Equal(t, "head", result.HeadSHA)
	require.True(t, repo.intent.Consumed)
	require.Zero(t, repo.prunes)
	_, err = svc.CompleteRepositorySetup(t.Context(), "acme", "repo-isolated", actor, state, "code", 9, request)
	require.ErrorIs(t, err, ErrInvalidInstall)
	require.Equal(t, 1, provider.calls)
}

func TestRepositoryInstallCannotReplaceAnotherInstallation(t *testing.T) {
	repo := &fakeRepository{installation: &githubapps.Installation{InstallationID: 88}}
	provider := &fakeProvider{}
	svc, err := New(testutil.FakeTransactionManager{}, repo, provider, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)
	actor := model.UserEventActor("alice")
	link, err := svc.StartRepositorySetup(t.Context(), "acme", actor, "repo-isolated")
	require.NoError(t, err)
	parsed, err := url.Parse(link)
	require.NoError(t, err)
	_, err = svc.CompleteRepositorySetup(t.Context(), "acme", "repo-isolated", actor, parsed.Query().Get("state"), "code", 9, gitprovider.RepositoryBootstrapRequest{Marker: "repo-isolated"})
	require.ErrorContains(t, err, "another GitHub installation")
	require.Zero(t, provider.calls)
	require.False(t, repo.intent.Consumed)
}

func TestRepositoryInstallBindingFailureRetainsIncompleteProviderIdentity(t *testing.T) {
	repo := &fakeRepository{upsertErr: xerror.Unexpected().Detail("isolated database failure")}
	provider := &fakeProvider{verified: githubapps.VerifiedInstallation{InstallationID: 9, AccountID: 2, AccountLogin: "acme", AccountType: "Organization", RepositorySelection: "selected"}}
	svc, err := New(testutil.FakeTransactionManager{}, repo, provider, &fakeEvents{}, "https://github.com/apps/braidplane/installations/new")
	require.NoError(t, err)
	actor := model.UserEventActor("alice")
	link, err := svc.StartRepositorySetup(t.Context(), "acme", actor, "repo-isolated")
	require.NoError(t, err)
	parsed, err := url.Parse(link)
	require.NoError(t, err)
	result, err := svc.CompleteRepositorySetup(t.Context(), "acme", "repo-isolated", actor, parsed.Query().Get("state"), "code", 9, gitprovider.RepositoryBootstrapRequest{Owner: "acme", Name: "pipelines", Marker: "repo-isolated"})
	require.Error(t, err)
	require.Equal(t, "42", result.RepositoryID)
	require.Empty(t, result.HeadSHA)
	require.False(t, repo.intent.Consumed)
}
