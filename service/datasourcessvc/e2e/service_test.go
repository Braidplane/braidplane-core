package e2e

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/testutil/datasourcee2e"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/githubapps"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	sqlconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/sql"
	"github.com/Braidplane/braidplane-core/service/githubappsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestConnectionOutcomesPersistInPostgres(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres data source connection outcome E2E test in short mode")
	}

	pool := e2edb.OpenPostgresPool(t, "data source connection outcome service integration test")

	tests := []struct {
		name       string
		testErr    error
		assertCall func(*testing.T, *datasourcessvc.DataSource, error)
		assertDB   func(*testing.T, *repods.DataSource)
	}{
		{
			name: "Success",
			assertCall: func(t *testing.T, created *datasourcessvc.DataSource, err error) {
				t.Helper()
				require.NoError(t, err)
				require.NotNil(t, created)
				require.True(t, created.IsActive)
				require.Nil(t, created.TestError)
				require.NotNil(t, created.LastTestedAt)
				require.NotNil(t, created.Credential)
				require.Equal(t, credentialssvc.CredentialKindStaticSecret, created.Credential.Kind)
				require.Equal(t, credentialssvc.CredentialStatusActive, created.Credential.Status)
			},
			assertDB: func(t *testing.T, stored *repods.DataSource) {
				t.Helper()
				require.True(t, stored.IsActive)
				require.Nil(t, stored.TestError)
				require.NotNil(t, stored.LastTestedAt)
				require.EqualValues(t, 2, stored.ConnectionTestRevision)
			},
		},
		{
			name:    "Failure",
			testErr: datasourcessvc.ErrConnectionFailed.WithPublicDetail("database endpoint unavailable"),
			assertCall: func(t *testing.T, created *datasourcessvc.DataSource, err error) {
				t.Helper()
				require.Nil(t, created)
				require.ErrorIs(t, err, datasourcessvc.ErrConnectionFailed)
			},
			assertDB: func(t *testing.T, stored *repods.DataSource) {
				t.Helper()
				require.False(t, stored.IsActive)
				require.NotNil(t, stored.TestError)
				require.Equal(t, "database endpoint unavailable", *stored.TestError)
				require.NotNil(t, stored.LastTestedAt)
				require.EqualValues(t, 2, stored.ConnectionTestRevision)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgSlug := datasourcee2e.UniqueSlug(t, "ds-outcome-org")
			dsSlug := datasourcee2e.UniqueSlug(t, "ds-outcome")
			datasourcee2e.SeedOrganization(t, pool, orgSlug)
			stack := datasourcee2e.NewStack(t, pool)
			svc := stack.Service(t, datasourcee2e.ConnectionTestExecutorFunc(func(context.Context, datasourcessvc.ConnectionTestRequest) error {
				return tt.testErr
			}))

			created, err := svc.Create(
				t.Context(),
				orgSlug,
				model.ExternalEventActor(),
				datasourcee2e.NewPostgresDataSourceParams(dsSlug, "Connection Outcome"),
			)
			tt.assertCall(t, created, err)
			tt.assertDB(t, stack.FindDataSource(t, orgSlug, dsSlug))
		})
	}
}

func TestConcurrentEditDoesNotBlockAndRejectsStaleConnectionCompletion(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres data source connection conflict E2E test in short mode")
	}

	pool := e2edb.OpenPostgresPool(t, "data source connection conflict service integration test")
	orgSlug := datasourcee2e.UniqueSlug(t, "ds-conflict-org")
	dsSlug := datasourcee2e.UniqueSlug(t, "ds-conflict")
	datasourcee2e.SeedOrganization(t, pool, orgSlug)
	stack := datasourcee2e.NewStack(t, pool)

	seedService := stack.Service(t, datasourcee2e.ConnectionTestExecutorFunc(func(context.Context, datasourcessvc.ConnectionTestRequest) error {
		return nil
	}))
	_, err := seedService.Create(
		t.Context(),
		orgSlug,
		model.ExternalEventActor(),
		datasourcee2e.NewPostgresDataSourceParams(dsSlug, "Original"),
	)
	require.NoError(t, err)
	before := stack.FindDataSource(t, orgSlug, dsSlug)
	require.NotNil(t, before.LastTestedAt)

	testStarted := make(chan struct{})
	releaseTest := make(chan struct{})
	var signalStarted sync.Once
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseTest) })
	blockingService := stack.Service(t, datasourcee2e.ConnectionTestExecutorFunc(func(ctx context.Context, _ datasourcessvc.ConnectionTestRequest) error {
		signalStarted.Do(func() { close(testStarted) })
		select {
		case <-releaseTest:
			return nil
		case <-ctx.Done():
			return datasourcessvc.ErrConnectionFailed.With(ctx.Err())
		}
	}))

	type updateResult struct {
		dataSource *datasourcessvc.DataSource
		err        error
	}
	updateCtx, cancelUpdate := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelUpdate()
	updateResultCh := make(chan updateResult, 1)
	go func() {
		updated, updateErr := blockingService.Update(
			updateCtx,
			orgSlug,
			dsSlug,
			model.ExternalEventActor(),
			&datasourcessvc.UpdateParams{Name: "Prepared Update"},
		)
		updateResultCh <- updateResult{dataSource: updated, err: updateErr}
	}()

	select {
	case <-testStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("connection test did not start")
	}

	prepared := stack.FindDataSource(t, orgSlug, dsSlug)
	require.Equal(t, "Prepared Update", prepared.Name)
	concurrentActive := false
	concurrentCtx, cancelConcurrent := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelConcurrent()
	err = stack.Txm.RunInTx(concurrentCtx, func(ctx context.Context, tx postgres.DBTX) error {
		_, updateErr := stack.Repo.Update(ctx, tx, orgSlug, dsSlug, &repods.UpdateParams{
			Name:       "Concurrent Edit",
			ConfigJSON: json.RawMessage(`{"host":"concurrent.example","port":5432,"database":"app","ssl_mode":"verify-full"}`),
			IsActive:   &concurrentActive,
			ReadOnly:   prepared.ReadOnly,
		})
		return updateErr
	})
	require.NoError(t, err, "concurrent edit must not wait for connector network work")
	releaseOnce.Do(func() { close(releaseTest) })

	var result updateResult
	select {
	case result = <-updateResultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("stale connection test did not finish")
	}
	require.Nil(t, result.dataSource)
	require.ErrorIs(t, result.err, datasourcessvc.ErrConnectionTestConflict)

	stored := stack.FindDataSource(t, orgSlug, dsSlug)
	require.Equal(t, "Concurrent Edit", stored.Name)
	require.JSONEq(
		t,
		`{"host":"concurrent.example","port":5432,"database":"app","ssl_mode":"verify-full"}`,
		string(stored.Config),
	)
	require.False(t, stored.IsActive)
	require.NotNil(t, stored.LastTestedAt)
	require.Equal(t, before.LastTestedAt.UTC(), stored.LastTestedAt.UTC())
	require.Equal(t, before.ConnectionTestRevision+2, stored.ConnectionTestRevision)
}

func TestCallerTimeoutStillPersistsConnectionFailure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres data source connection timeout E2E test in short mode")
	}

	pool := e2edb.OpenPostgresPool(t, "data source connection timeout service integration test")
	orgSlug := datasourcee2e.UniqueSlug(t, "ds-timeout-org")
	dsSlug := datasourcee2e.UniqueSlug(t, "ds-timeout")
	datasourcee2e.SeedOrganization(t, pool, orgSlug)
	stack := datasourcee2e.NewStack(t, pool)

	seedService := stack.Service(t, datasourcee2e.ConnectionTestExecutorFunc(func(context.Context, datasourcessvc.ConnectionTestRequest) error {
		return nil
	}))
	_, err := seedService.Create(
		t.Context(),
		orgSlug,
		model.ExternalEventActor(),
		datasourcee2e.NewPostgresDataSourceParams(dsSlug, "Timeout"),
	)
	require.NoError(t, err)

	timeoutService := stack.Service(t, datasourcee2e.ConnectionTestExecutorFunc(func(ctx context.Context, _ datasourcessvc.ConnectionTestRequest) error {
		<-ctx.Done()
		return datasourcessvc.ErrConnectionFailed.
			With(ctx.Err()).
			WithPublicDetail("connection test timed out")
	}))
	timeoutCtx, cancelTimeout := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancelTimeout()
	err = timeoutService.TestExistingConnection(timeoutCtx, orgSlug, dsSlug)
	require.ErrorIs(t, err, datasourcessvc.ErrConnectionFailed)
	require.Equal(t, context.DeadlineExceeded, timeoutCtx.Err())

	stored := stack.FindDataSource(t, orgSlug, dsSlug)
	require.False(t, stored.IsActive)
	require.NotNil(t, stored.LastTestedAt)
	require.NotNil(t, stored.TestError)
	require.Equal(t, "connection test timed out", *stored.TestError)
	require.EqualValues(t, 3, stored.ConnectionTestRevision)
}

func TestUpdateCredentialsPersistsReplacementBeforeConnectionTestCompletion(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres data source credential update E2E test in short mode")
	}

	pool := e2edb.OpenPostgresPool(t, "data source credential update service integration test")

	t.Run("FailurePersistsReplacementAndSafeDiagnostic", func(t *testing.T) {
		t.Parallel()
		orgSlug := datasourcee2e.UniqueSlug(t, "ds-credential-failure-org")
		dsSlug := datasourcee2e.UniqueSlug(t, "ds-credential-failure")
		datasourcee2e.SeedOrganization(t, pool, orgSlug)
		stack := datasourcee2e.NewStack(t, pool)

		seedService := stack.Service(t, datasourcee2e.ConnectionTestExecutorFunc(func(
			context.Context,
			datasourcessvc.ConnectionTestRequest,
		) error {
			return nil
		}))
		_, err := seedService.Create(
			t.Context(),
			orgSlug,
			model.ExternalEventActor(),
			datasourcee2e.NewPostgresDataSourceParams(dsSlug, "Credential Failure"),
		)
		require.NoError(t, err)
		before := stack.FindDataSource(t, orgSlug, dsSlug)
		require.NotNil(t, before.LastTestedAt)
		oldCredentialSlug := datasourcee2e.StoredDataSourceCredentialSlug(t, pool, orgSlug, dsSlug)

		failureService := stack.Service(t, datasourcee2e.ConnectionTestExecutorFunc(func(
			context.Context,
			datasourcessvc.ConnectionTestRequest,
		) error {
			return datasourcessvc.ErrConnectionFailed.
				WithPublicDetail("credential verification failed")
		}))
		updated, err := failureService.UpdateCredentials(
			t.Context(),
			orgSlug,
			dsSlug,
			model.ExternalEventActor(),
			datasourcee2e.NewPostgresCredentialUpdateParams(),
		)
		require.Nil(t, updated)
		require.ErrorIs(t, err, datasourcessvc.ErrInvalidInput)
		require.ErrorIs(t, err, datasourcessvc.ErrConnectionFailed)

		newCredentialSlug := datasourcee2e.StoredDataSourceCredentialSlug(t, pool, orgSlug, dsSlug)
		require.NotEqual(t, oldCredentialSlug, newCredentialSlug)
		datasourcee2e.AssertStoredCredentialState(
			t,
			pool,
			orgSlug,
			oldCredentialSlug,
			credentialssvc.CredentialStatusRotated.String(),
			0,
		)
		datasourcee2e.AssertStoredCredentialHasPayload(
			t,
			pool,
			orgSlug,
			newCredentialSlug,
			credentialssvc.CredentialStatusActive.String(),
		)

		stored := stack.FindDataSource(t, orgSlug, dsSlug)
		require.False(t, stored.IsActive)
		require.NotNil(t, stored.TestError)
		require.Equal(t, "credential verification failed", *stored.TestError)
		require.NotNil(t, stored.LastTestedAt)
		require.True(t, stored.LastTestedAt.After(*before.LastTestedAt))
		require.Equal(t, before.ConnectionTestRevision+2, stored.ConnectionTestRevision)
	})

	t.Run("ConcurrentEditIsUnblockedAndRejectsStaleFailure", func(t *testing.T) {
		t.Parallel()
		orgSlug := datasourcee2e.UniqueSlug(t, "ds-credential-conflict-org")
		dsSlug := datasourcee2e.UniqueSlug(t, "ds-credential-conflict")
		datasourcee2e.SeedOrganization(t, pool, orgSlug)
		stack := datasourcee2e.NewStack(t, pool)

		seedService := stack.Service(t, datasourcee2e.ConnectionTestExecutorFunc(func(
			context.Context,
			datasourcessvc.ConnectionTestRequest,
		) error {
			return nil
		}))
		_, err := seedService.Create(
			t.Context(),
			orgSlug,
			model.ExternalEventActor(),
			datasourcee2e.NewPostgresDataSourceParams(dsSlug, "Credential Conflict"),
		)
		require.NoError(t, err)
		before := stack.FindDataSource(t, orgSlug, dsSlug)
		require.NotNil(t, before.LastTestedAt)
		oldCredentialSlug := datasourcee2e.StoredDataSourceCredentialSlug(t, pool, orgSlug, dsSlug)

		testStarted := make(chan struct{})
		releaseTest := make(chan struct{})
		var signalStarted sync.Once
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(releaseTest) })
		blockingService := stack.Service(t, datasourcee2e.ConnectionTestExecutorFunc(func(
			ctx context.Context,
			_ datasourcessvc.ConnectionTestRequest,
		) error {
			signalStarted.Do(func() { close(testStarted) })
			select {
			case <-releaseTest:
				return datasourcessvc.ErrConnectionFailed.
					WithPublicDetail("credential verification failed")
			case <-ctx.Done():
				return datasourcessvc.ErrConnectionFailed.With(ctx.Err())
			}
		}))

		type updateCredentialsResult struct {
			dataSource *datasourcessvc.DataSource
			err        error
		}
		updateCtx, cancelUpdate := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancelUpdate()
		updateResultCh := make(chan updateCredentialsResult, 1)
		go func() {
			updated, updateErr := blockingService.UpdateCredentials(
				updateCtx,
				orgSlug,
				dsSlug,
				model.ExternalEventActor(),
				datasourcee2e.NewPostgresCredentialUpdateParams(),
			)
			updateResultCh <- updateCredentialsResult{dataSource: updated, err: updateErr}
		}()

		select {
		case <-testStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("credential connection test did not start")
		}

		prepared := stack.FindDataSource(t, orgSlug, dsSlug)
		require.Equal(t, before.ConnectionTestRevision+1, prepared.ConnectionTestRevision)
		newCredentialSlug := datasourcee2e.StoredDataSourceCredentialSlug(t, pool, orgSlug, dsSlug)
		require.NotEqual(t, oldCredentialSlug, newCredentialSlug)
		datasourcee2e.AssertStoredCredentialState(
			t,
			pool,
			orgSlug,
			oldCredentialSlug,
			credentialssvc.CredentialStatusRotated.String(),
			0,
		)
		datasourcee2e.AssertStoredCredentialHasPayload(
			t,
			pool,
			orgSlug,
			newCredentialSlug,
			credentialssvc.CredentialStatusActive.String(),
		)

		concurrentActive := true
		concurrentCtx, cancelConcurrent := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancelConcurrent()
		err = stack.Txm.RunInTx(concurrentCtx, func(ctx context.Context, tx postgres.DBTX) error {
			_, updateErr := stack.Repo.Update(ctx, tx, orgSlug, dsSlug, &repods.UpdateParams{
				Name:       "Concurrent Credential Edit",
				ConfigJSON: json.RawMessage(`{"host":"concurrent.example","port":5432,"database":"app","ssl_mode":"verify-full"}`),
				IsActive:   &concurrentActive,
				ReadOnly:   prepared.ReadOnly,
			})
			return updateErr
		})
		require.NoError(t, err, "concurrent edit must not wait for credential connection network work")
		releaseOnce.Do(func() { close(releaseTest) })

		var result updateCredentialsResult
		select {
		case result = <-updateResultCh:
		case <-time.After(5 * time.Second):
			t.Fatal("stale credential connection test did not finish")
		}
		require.Nil(t, result.dataSource)
		require.ErrorIs(t, result.err, datasourcessvc.ErrConnectionTestConflict)

		stored := stack.FindDataSource(t, orgSlug, dsSlug)
		require.Equal(t, "Concurrent Credential Edit", stored.Name)
		require.JSONEq(
			t,
			`{"host":"concurrent.example","port":5432,"database":"app","ssl_mode":"verify-full"}`,
			string(stored.Config),
		)
		require.True(t, stored.IsActive)
		require.Nil(t, stored.TestError)
		require.NotNil(t, stored.LastTestedAt)
		require.Equal(t, before.LastTestedAt.UTC(), stored.LastTestedAt.UTC())
		require.Equal(t, before.ConnectionTestRevision+2, stored.ConnectionTestRevision)
		require.Equal(t, newCredentialSlug, datasourcee2e.StoredDataSourceCredentialSlug(t, pool, orgSlug, dsSlug))
		datasourcee2e.AssertStoredCredentialState(
			t,
			pool,
			orgSlug,
			oldCredentialSlug,
			credentialssvc.CredentialStatusRotated.String(),
			0,
		)
		datasourcee2e.AssertStoredCredentialHasPayload(
			t,
			pool,
			orgSlug,
			newCredentialSlug,
			credentialssvc.CredentialStatusActive.String(),
		)
	})
}

func TestDataSourcePublicLifecyclePersistsInPostgres(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres data source public lifecycle E2E test in short mode")
	}

	pool := e2edb.OpenPostgresPool(t, "data source public lifecycle service integration test")
	orgSlug := datasourcee2e.UniqueSlug(t, "ds-lifecycle-org")
	dsSlug := datasourcee2e.UniqueSlug(t, "ds-lifecycle")
	datasourcee2e.SeedOrganization(t, pool, orgSlug)
	stack := datasourcee2e.NewStack(t, pool)
	var gitBranchRequest datasourcessvc.GitBranchRequest
	svc := stack.ServiceWithConfig(t, datasourcee2e.ServiceConfig{
		ConnectionTester: datasourcee2e.ConnectionTestExecutorFunc(func(context.Context, datasourcessvc.ConnectionTestRequest) error {
			return nil
		}),
		GitBranchExecutor: datasourcee2e.GitBranchExecutorFunc(func(
			_ context.Context,
			request datasourcessvc.GitBranchRequest,
		) ([]datasourcessvc.DataSourceBranch, error) {
			gitBranchRequest = request
			return []datasourcessvc.DataSourceBranch{
				{Name: "main", IsDefault: true},
				{Name: "feature/coverage"},
			}, nil
		}),
	})
	actor := model.ExternalEventActor()

	created, err := svc.Create(t.Context(), orgSlug, actor, datasourcee2e.NewPostgresDataSourceParams(dsSlug, "Lifecycle"))
	require.NoError(t, err)
	require.Equal(t, dsSlug, created.Slug)

	found, err := svc.FindBySlug(t.Context(), orgSlug, dsSlug)
	require.NoError(t, err)
	require.Equal(t, "Lifecycle", found.Name)
	require.Equal(t, datasourcessvc.SchemaTypeSQL, found.Schema.SchemaType())

	listed, err := svc.ListByOrg(t.Context(), orgSlug, model.ListRequest{
		Limit:  1,
		SortBy: model.ListSortName,
		Order:  "asc",
	})
	require.NoError(t, err)
	require.Len(t, listed.Items, 1)
	require.Equal(t, dsSlug, listed.Items[0].Slug)
	require.False(t, listed.HasMore)

	require.NoError(t, svc.TestConnection(t.Context(), orgSlug, &datasourcessvc.TestConnectionParams{
		Type:        model.DataSourceTypePostgres,
		ConfigJSON:  json.RawMessage(`{"host":"draft.example","port":5432,"database":"app","ssl_mode":"verify-full"}`),
		Credentials: map[string]any{"username": "draft", "password": "test-only"},
	}))

	active := true
	updated, err := svc.Update(t.Context(), orgSlug, dsSlug, actor, &datasourcessvc.UpdateParams{
		Name:        "Lifecycle Updated",
		ConfigJSON:  json.RawMessage(`{"host":"updated.example","port":5432,"database":"app","ssl_mode":"verify-full"}`),
		IsActive:    &active,
		ReadOnly:    true,
		ReadOnlySet: true,
	})
	require.NoError(t, err)
	require.Equal(t, "Lifecycle Updated", updated.Name)
	require.True(t, updated.ReadOnly)

	updated, err = svc.UpdateCredentials(
		t.Context(),
		orgSlug,
		dsSlug,
		actor,
		datasourcee2e.NewPostgresCredentialUpdateParams(),
	)
	require.NoError(t, err)
	require.True(t, updated.IsActive)

	validated, err := svc.ValidateCredentialLifecycle(
		t.Context(),
		orgSlug,
		dsSlug,
		actor,
		model.EventActionRotate,
	)
	require.NoError(t, err)
	require.True(t, validated.IsActive)
	require.NoError(t, svc.RecordCredentialLifecycleEvent(
		t.Context(),
		orgSlug,
		dsSlug,
		actor,
		model.EventActionRevoke,
	))

	require.NoError(t, svc.Delete(t.Context(), orgSlug, dsSlug, actor))
	_, err = svc.FindBySlug(t.Context(), orgSlug, dsSlug)
	require.ErrorIs(t, err, datasourcessvc.ErrDataSourceNotFound)
	require.NoError(t, svc.Restore(t.Context(), orgSlug, dsSlug, actor))
	restored, err := svc.FindBySlug(t.Context(), orgSlug, dsSlug)
	require.NoError(t, err)
	require.Equal(t, "Lifecycle Updated", restored.Name)

	gitSlug := datasourcee2e.UniqueSlug(t, "ds-lifecycle-git")
	_, err = svc.Create(t.Context(), orgSlug, actor, &datasourcessvc.CreateParams{
		Name:       "Lifecycle Repository",
		Slug:       gitSlug,
		Type:       model.DataSourceTypeGit,
		ConfigJSON: json.RawMessage(`{"repo_url":"https://github.com/example/repository.git","default_branch":"main"}`),
		Credentials: map[string]any{
			"auth_type": "https",
			"token":     "test-only-token",
		},
	})
	require.NoError(t, err)
	executionAuth, err := svc.ResolveGitExecutionAuth(t.Context(), orgSlug, gitSlug)
	require.NoError(t, err)
	require.Equal(t, model.DataSourceTypeGit, executionAuth.DataSourceType)
	require.JSONEq(t, `{"auth_type":"https","token":"test-only-token"}`, string(executionAuth.CredentialsJSON))
	branches, err := svc.ListBranches(t.Context(), orgSlug, gitSlug)
	require.NoError(t, err)
	require.Equal(t, orgSlug, gitBranchRequest.OrganizationSlug)
	require.Len(t, branches, 2)
	require.Equal(t, "main", branches[0].Name)
}

func TestManagedGitHubRepositorySelectionPersistsSafeMetadata(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres managed GitHub data source E2E test in short mode")
	}

	pool := e2edb.OpenPostgresPool(t, "managed GitHub data source service integration test")
	orgSlug := datasourcee2e.UniqueSlug(t, "ds-github-org")
	dsSlug := datasourcee2e.UniqueSlug(t, "ds-github")
	datasourcee2e.SeedOrganization(t, pool, orgSlug)
	stack := datasourcee2e.NewStack(t, pool)
	githubApps := datasourcee2e.GitHubAppsStub{Organization: orgSlug, Connection: &githubappsvc.Connection{
		Organization: orgSlug,
		Repositories: []githubapps.RepositoryBinding{
			{ID: 42, Name: "pipelines", FullName: "acme/pipelines", OwnerLogin: "acme"},
			{ID: 43, Name: "warehouse", FullName: "acme/warehouse", OwnerLogin: "acme"},
		},
	}}
	var requests []datasourcessvc.ConnectionTestRequest
	svc := stack.ServiceWithConfig(t, datasourcee2e.ServiceConfig{
		GitHubApps: githubApps,
		ConnectionTester: datasourcee2e.ConnectionTestExecutorFunc(func(_ context.Context, request datasourcessvc.ConnectionTestRequest) error {
			requests = append(requests, request)
			return nil
		}),
	})

	created, err := svc.Create(t.Context(), orgSlug, model.ExternalEventActor(), &datasourcessvc.CreateParams{
		Name: "Managed GitHub", Slug: dsSlug, Type: model.DataSourceTypeGithub,
		ConfigJSON: json.RawMessage(`{"repository_id":"42","base_path":"data"}`), Credentials: map[string]any{}, ReadOnly: true,
	})
	require.NoError(t, err)
	require.True(t, created.IsActive)
	require.Len(t, requests, 1)
	require.JSONEq(t, `{"repo_url":"https://github.com/acme/pipelines.git","base_path":"data"}`, string(requests[0].ConfigJSON))
	require.JSONEq(t, `{"auth_type":"https","username":"x-access-token","token":"installation-token"}`, string(requests[0].CredentialsJSON))

	storedCredential, err := stack.Credentials.ResolveRuntimeCredential(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{
		Owner: credentialssvc.CredentialOwner{OrgSlug: orgSlug, DataSourceSlug: dsSlug},
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"auth_type":"https","username":"x-access-token"}`, string(storedCredential.PlaintextJSON))

	repositories, err := svc.ListProviderRepositories(t.Context(), orgSlug, dsSlug)
	require.NoError(t, err)
	require.Len(t, repositories, 2)
	require.Equal(t, "acme/warehouse", repositories[1].FullName)

	selected, err := svc.SelectProviderRepository(t.Context(), orgSlug, dsSlug, model.ExternalEventActor(), datasourcessvc.SelectProviderRepositoryParams{RepositoryID: "43"})
	require.NoError(t, err)
	require.True(t, selected.IsActive)
	require.Len(t, requests, 2)
	require.JSONEq(t, `{"repo_url":"https://github.com/acme/warehouse.git","base_path":"data"}`, string(requests[1].ConfigJSON))

	auth, err := svc.ResolveGitExecutionAuth(t.Context(), orgSlug, dsSlug)
	require.NoError(t, err)
	require.Equal(t, model.DataSourceTypeGithub, auth.DataSourceType)
	require.JSONEq(t, `{"repo_url":"https://github.com/acme/warehouse.git","base_path":"data"}`, string(auth.ConfigJSON))
	require.JSONEq(t, `{"auth_type":"https","username":"x-access-token","token":"installation-token"}`, string(auth.CredentialsJSON))
}

func TestProviderOAuthConnectionFinalizationPersistsInPostgres(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres provider OAuth callback E2E test in short mode")
	}

	pool := e2edb.OpenPostgresPool(t, "provider OAuth callback service integration test")
	tests := []struct {
		name       string
		testErr    error
		wantActive bool
		wantErr    error
	}{
		{name: "Success", wantActive: true},
		{
			name:       "Failure",
			testErr:    datasourcessvc.ErrConnectionFailed.WithPublicDetail("provider credential verification failed"),
			wantActive: false,
			wantErr:    datasourcessvc.ErrInvalidInput,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgSlug := datasourcee2e.UniqueSlug(t, "ds-provider-org")
			dsSlug := datasourcee2e.UniqueSlug(t, "ds-provider")
			datasourcee2e.SeedOrganization(t, pool, orgSlug)
			provider := &dataSourceOAuthProviderStub{
				authorizationURL: "https://provider.example/oauth/authorize",
				exchange: credentialssvc.OAuthTokenSet{
					AccessToken:  "test-access-token",
					RefreshToken: "test-refresh-token",
					ExpiresAt:    time.Now().Add(time.Hour),
					Scopes:       []string{"crm.objects.contacts.read"},
					Account:      credentialssvc.ProviderAccount{ID: "account-1"},
				},
			}
			stack := datasourcee2e.NewStack(t, pool, credentialssvc.ProviderRegistry{model.DataSourceTypeHubSpot.String(): provider})
			svc := stack.Service(t, datasourcee2e.ConnectionTestExecutorFunc(func(context.Context, datasourcessvc.ConnectionTestRequest) error {
				return tt.testErr
			}))

			pending, err := svc.StartProviderDataSourceInstall(
				t.Context(),
				orgSlug,
				model.ExternalEventActor(),
				model.DataSourceTypeHubSpot.String(),
				&datasourcessvc.StartProviderDataSourceInstallParams{
					Name:       "Provider CRM",
					Slug:       dsSlug,
					Type:       model.DataSourceTypeHubSpot,
					ConfigJSON: json.RawMessage(`{"timeout_seconds":5}`),
				},
			)
			require.NoError(t, err)
			require.Equal(t, credentialssvc.CredentialStatusPending, pending.DataSource.Credential.Status)
			require.Contains(t, pending.Install.AuthorizationURL, pending.Install.State)

			completed, err := svc.CompleteProviderOAuthCallback(
				t.Context(),
				credentialssvc.CompleteOAuthCallbackParams{
					Provider: model.DataSourceTypeHubSpot.String(),
					State:    pending.Install.State,
					Code:     "provider-code",
				},
			)
			if tt.wantErr == nil {
				require.NoError(t, err)
				require.Equal(t, credentialssvc.CredentialStatusActive, completed.Credential.Status)
			} else {
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorIs(t, err, datasourcessvc.ErrConnectionFailed)
			}

			stored := stack.FindDataSource(t, orgSlug, dsSlug)
			require.Equal(t, tt.wantActive, stored.IsActive)
			require.NotNil(t, stored.LastTestedAt)
			if tt.testErr == nil {
				require.Nil(t, stored.TestError)
			} else {
				require.Equal(t, "provider credential verification failed", *stored.TestError)
			}
		})
	}
}

func TestSchemaImportLifecyclePersistsInPostgres(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres data source schema import lifecycle E2E test in short mode")
	}

	pool := e2edb.OpenPostgresPool(t, "data source schema import lifecycle service integration test")
	orgSlug := datasourcee2e.UniqueSlug(t, "ds-schema-org")
	dsSlug := datasourcee2e.UniqueSlug(t, "ds-schema")
	datasourcee2e.SeedOrganization(t, pool, orgSlug)
	stack := datasourcee2e.NewStack(t, pool)
	producer := &capturingSchemaImportProducer{}
	driftReports := driftimpactrepo.New()
	introspectionCalls := 0
	factory := func(
		context.Context,
		model.DataSourceType,
		[]byte,
		[]byte,
	) (connector.Connector, error) {
		introspectionCalls++
		conn := testutil.NewFakeSQLConnector()
		conn.IntrospectSchemaFn = func(context.Context) (*sqlconnector.Schema, error) {
			switch introspectionCalls {
			case 1:
				return &sqlconnector.Schema{Tables: []sqlconnector.Table{{
					Name: "users",
					Columns: []sqlconnector.Column{
						{Name: "id", Type: "integer"},
						{Name: "email", Type: "text"},
					},
				}}}, nil
			case 2:
				return &sqlconnector.Schema{Tables: []sqlconnector.Table{{
					Name:    "users",
					Columns: []sqlconnector.Column{{Name: "id", Type: "bigint"}},
				}}}, nil
			default:
				return nil, datasourcessvc.ErrSchemaFetchFailed.
					WithPublicDetail("schema provider temporarily unavailable")
			}
		}
		return conn, nil
	}
	svc := stack.ServiceWithConfig(t, datasourcee2e.ServiceConfig{
		ConnectionTester: datasourcee2e.ConnectionTestExecutorFunc(func(context.Context, datasourcessvc.ConnectionTestRequest) error {
			return nil
		}),
		ConnectorFactory:     factory,
		DriftReports:         driftReports,
		SchemaImportProducer: producer,
	})
	_, err := svc.Create(
		t.Context(),
		orgSlug,
		model.ExternalEventActor(),
		datasourcee2e.NewPostgresDataSourceParams(dsSlug, "Schema Lifecycle"),
	)
	require.NoError(t, err)

	cold, err := svc.IntrospectSchema(t.Context(), orgSlug, dsSlug)
	require.NoError(t, err)
	coldSQL, ok := cold.(datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload])
	require.True(t, ok)
	require.Empty(t, coldSQL.Payload.Tables)

	first := refreshAndExecuteSchemaImport(t, svc, producer, orgSlug, dsSlug)
	require.Equal(t, model.DataSourceSchemaImportStatusReady, first.Status)
	cached, err := svc.IntrospectSchema(t.Context(), orgSlug, dsSlug)
	require.NoError(t, err)
	cachedSQL, ok := cached.(datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload])
	require.True(t, ok)
	require.Len(t, cachedSQL.Payload.Tables, 1)
	require.Len(t, cachedSQL.Payload.Tables[0].Columns, 2)

	second := refreshAndExecuteSchemaImport(t, svc, producer, orgSlug, dsSlug)
	require.Equal(t, model.DataSourceSchemaImportStatusReady, second.Status)
	dsSlugFilter := dsSlug
	var reports []*driftimpactrepo.Report
	err = stack.Txm.RunRead(t.Context(), func(ctx context.Context, db postgres.DBTX) error {
		var listErr error
		reports, listErr = driftReports.List(ctx, db, orgSlug, driftimpactrepo.ListFilters{
			DataSourceSlug: &dsSlugFilter,
		})
		return listErr
	})
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, "breaking", reports[0].Severity.String())
	require.Contains(t, string(reports[0].ChangesJSONB), "tables.users.email")

	failed := refreshAndExecuteSchemaImport(t, svc, producer, orgSlug, dsSlug)
	require.Equal(t, model.DataSourceSchemaImportStatusFailed, failed.Status)
	require.NotNil(t, failed.Diagnostic)
	require.Equal(t, datasourcessvc.ErrSchemaFetchFailed.Code(), failed.Diagnostic.Code)
	require.Equal(t, "schema provider temporarily unavailable", failed.Diagnostic.Message)
	require.Equal(t, 3, introspectionCalls)
}

type capturingSchemaImportProducer struct {
	calls []jobs.DataSourceSchemaImportArgs
}

func (p *capturingSchemaImportProducer) EnqueueDataSourceSchemaImport(
	_ context.Context,
	_ postgres.DBTX,
	args jobs.DataSourceSchemaImportArgs,
) (queue.QueuedJob, error) {
	p.calls = append(p.calls, args)
	return queue.QueuedJob{
		Kind:  args.Kind(),
		Queue: jobs.DataSourceSchemaImportQueue,
		State: "queued",
	}, nil
}

func refreshAndExecuteSchemaImport(
	t *testing.T,
	svc datasourcessvc.Service,
	producer *capturingSchemaImportProducer,
	orgSlug, dsSlug slug.Slug,
) *datasourcessvc.SchemaImportResult {
	t.Helper()

	beforeCalls := len(producer.calls)
	queued, err := svc.RefreshSchema(t.Context(), orgSlug, dsSlug, model.ExternalEventActor())
	require.NoError(t, err)
	require.Equal(t, model.DataSourceSchemaImportStatusImporting, queued.Status)
	require.True(t, queued.Enqueued)
	require.Len(t, producer.calls, beforeCalls+1)

	result, err := svc.ExecuteQueuedSchemaImport(t.Context(), producer.calls[beforeCalls])
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Skipped)
	return result
}

type dataSourceOAuthProviderStub struct {
	authorizationURL string
	exchange         credentialssvc.OAuthTokenSet
}

func (*dataSourceOAuthProviderStub) OAuthScopePlan() credentialssvc.OAuthScopePlan {
	return credentialssvc.OAuthScopePlan{Required: []string{"crm.objects.contacts.read"}}
}

func (p *dataSourceOAuthProviderStub) AuthorizationURL(
	_ context.Context,
	params credentialssvc.AuthorizationURLParams,
) (string, error) {
	return p.authorizationURL + "?state=" + params.State, nil
}

func (p *dataSourceOAuthProviderStub) ExchangeCode(
	context.Context,
	credentialssvc.ExchangeCodeParams,
) (credentialssvc.OAuthTokenSet, error) {
	return p.exchange, nil
}

func (p *dataSourceOAuthProviderStub) Refresh(
	context.Context,
	credentialssvc.RefreshParams,
) (credentialssvc.OAuthTokenSet, error) {
	return credentialssvc.OAuthTokenSet{}, xerror.Unexpected().Detail("unexpected provider refresh")
}

func (*dataSourceOAuthProviderStub) Revoke(
	context.Context,
	credentialssvc.RevokeProviderGrantParams,
) error {
	return nil
}
