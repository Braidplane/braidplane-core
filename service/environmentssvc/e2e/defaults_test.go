package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/checkpoints"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
	"github.com/Braidplane/braidplane-core/postgres/members"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/postgres/roles"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/memberssvc"
	"github.com/Braidplane/braidplane-core/service/orgsvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

func TestDefaultEnvironmentSeedingCreatesEditableProjectDefaultsAndPreservesTenantIsolation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres environment defaults E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "environment defaults service integration test")
	stack := newDefaultEnvironmentE2EStack(pool)

	orgASlug := uniqueEnvironmentDefaultsSlug("env-defaults-org-a")
	orgBSlug := uniqueEnvironmentDefaultsSlug("env-defaults-org-b")
	userASlug := uniqueEnvironmentDefaultsSlug("env-defaults-user-a")
	userBSlug := uniqueEnvironmentDefaultsSlug("env-defaults-user-b")
	userASlug = createDefaultEnvironmentE2EOrganization(t, ctx, pool, stack, userASlug, orgASlug, "Environment Defaults Org A")
	userBSlug = createDefaultEnvironmentE2EOrganization(t, ctx, pool, stack, userBSlug, orgBSlug, "Environment Defaults Org B")

	orgAEnvironments, err := stack.environments.ListEnvironments(ctx, orgASlug, model.ListEnvironmentsRequest{
		ListRequest: model.ListRequest{Limit: 10, SortBy: model.ListSortCreatedAt, Order: model.ListOrderAsc},
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []slug.Slug{
		model.DefaultDevelopmentEnvironmentSlug,
		model.DefaultStagingEnvironmentSlug,
		model.DefaultProductionEnvironmentSlug,
	}, environmentSlugs(orgAEnvironments.Items))

	renamedDescription := "Edited by an operator."
	renamed, err := stack.environments.UpdateEnvironment(
		ctx,
		orgASlug,
		model.DefaultDevelopmentEnvironmentSlug,
		model.UpdateEnvironment{Name: "Developer Sandbox", Description: &renamedDescription, Color: model.EnvironmentColorGreen},
		model.UserEventActor(userASlug),
	)
	require.NoError(t, err)
	require.Equal(t, "Developer Sandbox", renamed.Name)
	require.Equal(t, model.EnvironmentColorGreen, renamed.Color)

	require.NoError(t, stack.environments.CreateDefaultEnvironments(ctx, orgASlug))
	devAfterReseed, err := stack.environments.GetEnvironment(ctx, orgASlug, model.DefaultDevelopmentEnvironmentSlug)
	require.NoError(t, err)
	require.Equal(t, "Developer Sandbox", devAfterReseed.Name)
	require.NotNil(t, devAfterReseed.Description)
	require.Equal(t, renamedDescription, *devAfterReseed.Description)
	require.Equal(t, model.EnvironmentColorGreen, devAfterReseed.Color)

	projectA, err := stack.projects.CreateProject(
		ctx,
		orgASlug,
		model.NewProject{Name: "Operations", Slug: "operations"},
		model.UserEventActor(userASlug),
	)
	require.NoError(t, err)

	projectAEnvironments, err := stack.environments.ListProjectEnvironments(
		ctx,
		orgASlug,
		projectA.Slug,
		model.ListRequest{Limit: 10, SortBy: model.ListSortCreatedAt, Order: model.ListOrderAsc},
	)
	require.NoError(t, err)
	require.Len(t, projectAEnvironments.Items, 1)
	require.Equal(t, model.DefaultDevelopmentEnvironmentSlug, projectAEnvironments.Items[0].Environment.Slug)
	require.True(t, projectAEnvironments.Items[0].IsDefault)

	deleteReason := "not needed for this organization"
	confirmProduction := model.DefaultProductionEnvironmentSlug
	require.NoError(t, stack.environments.DeleteEnvironment(
		ctx,
		orgASlug,
		model.DefaultProductionEnvironmentSlug,
		model.DeleteEnvironment{
			Reason:                           &deleteReason,
			ProtectedEnvironmentConfirmation: &confirmProduction,
		},
		model.UserEventActor(userASlug),
	))
	_, err = stack.environments.GetEnvironment(ctx, orgASlug, model.DefaultProductionEnvironmentSlug)
	require.True(t, xerror.Is(err, environmentssvc.ErrEnvironmentNotFound))

	orgBEnvironments, err := stack.environments.ListEnvironments(ctx, orgBSlug, model.ListEnvironmentsRequest{
		ListRequest: model.ListRequest{Limit: 10, SortBy: model.ListSortCreatedAt, Order: model.ListOrderAsc},
	})
	require.NoError(t, err)
	require.ElementsMatch(t, []slug.Slug{
		model.DefaultDevelopmentEnvironmentSlug,
		model.DefaultStagingEnvironmentSlug,
		model.DefaultProductionEnvironmentSlug,
	}, environmentSlugs(orgBEnvironments.Items))

	projectB, err := stack.projects.CreateProject(
		ctx,
		orgBSlug,
		model.NewProject{Name: "Operations", Slug: "operations"},
		model.UserEventActor(userBSlug),
	)
	require.NoError(t, err)
	projectBEnvironments, err := stack.environments.ListProjectEnvironments(
		ctx,
		orgBSlug,
		projectB.Slug,
		model.ListRequest{Limit: 10, SortBy: model.ListSortCreatedAt, Order: model.ListOrderAsc},
	)
	require.NoError(t, err)
	require.Len(t, projectBEnvironments.Items, 1)
	require.Equal(t, model.DefaultDevelopmentEnvironmentSlug, projectBEnvironments.Items[0].Environment.Slug)
	require.True(t, projectBEnvironments.Items[0].IsDefault)
}

func TestDisableProjectEnvironmentSoftDeletesAndPreservesRunHistory(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres project environment soft-delete E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "project environment soft-delete service integration test")
	stack := newDefaultEnvironmentE2EStack(pool)

	orgSlug := uniqueEnvironmentDefaultsSlug("env-soft-delete-org")
	userSlug := uniqueEnvironmentDefaultsSlug("env-soft-delete-user")
	userSlug = createDefaultEnvironmentE2EOrganization(t, ctx, pool, stack, userSlug, orgSlug, "Environment Soft Delete Org")

	project, err := stack.projects.CreateProject(
		ctx,
		orgSlug,
		model.NewProject{Name: "Operations", Slug: "operations"},
		model.UserEventActor(userSlug),
	)
	require.NoError(t, err)

	runHistory := seedProjectEnvironmentRunHistory(
		t,
		ctx,
		pool,
		orgSlug,
		project.Slug,
		model.DefaultDevelopmentEnvironmentSlug,
	)

	err = stack.environments.DisableProjectEnvironment(
		ctx,
		orgSlug,
		project.Slug,
		model.DefaultDevelopmentEnvironmentSlug,
		nil,
		model.UserEventActor(userSlug),
	)
	require.NoError(t, err)

	projectEnvironments, err := stack.environments.ListProjectEnvironments(
		ctx,
		orgSlug,
		project.Slug,
		model.ListRequest{Limit: 10, SortBy: model.ListSortCreatedAt, Order: model.ListOrderAsc},
	)
	require.NoError(t, err)
	require.Empty(t, projectEnvironments.Items)
	assertProjectEnvironmentSoftDeleted(t, ctx, pool, runHistory.projectEnvironmentID)
	assertProjectEnvironmentRunHistoryRows(t, ctx, pool, runHistory.projectEnvironmentID, 1, 1)

	_, err = stack.environments.EnableProjectEnvironment(
		ctx,
		orgSlug,
		project.Slug,
		model.DefaultDevelopmentEnvironmentSlug,
		model.EnableProjectEnvironment{IsDefault: true},
		model.UserEventActor(userSlug),
	)
	require.NoError(t, err)

	projectEnvironments, err = stack.environments.ListProjectEnvironments(
		ctx,
		orgSlug,
		project.Slug,
		model.ListRequest{Limit: 10, SortBy: model.ListSortCreatedAt, Order: model.ListOrderAsc},
	)
	require.NoError(t, err)
	require.Len(t, projectEnvironments.Items, 1)
	require.True(t, projectEnvironments.Items[0].IsDefault)
	assertProjectEnvironmentRestored(t, ctx, pool, runHistory.projectEnvironmentID)
	assertProjectEnvironmentRunHistoryRows(t, ctx, pool, runHistory.projectEnvironmentID, 1, 1)

	deleteReason := "environment cleanup"
	err = stack.environments.DeleteEnvironment(
		ctx,
		orgSlug,
		model.DefaultDevelopmentEnvironmentSlug,
		model.DeleteEnvironment{Reason: &deleteReason},
		model.UserEventActor(userSlug),
	)
	require.NoError(t, err)
	assertProjectEnvironmentRunHistoryRows(t, ctx, pool, runHistory.projectEnvironmentID, 1, 1)
	assertDeletedEnvironmentRunHistoryVisible(t, ctx, pool, orgSlug, project.Slug, runHistory)
}

func TestEnvironmentSecretDeleteRecordsDeleteEventThroughCredentialLifecycle(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres environment secret lifecycle E2E test in short mode")
	}

	ctx := context.Background()
	pool := e2edb.OpenPostgresPool(t, "environment secret delete event service integration test")
	txm := postgres.NewTransactionManager(pool)
	eventRecorder := &recordingEventService{}
	environmentsRepo := environments.NewRepository()
	enc, err := crypto.NewCredentialKeyring("initial", map[string][]byte{"initial": []byte("0123456789abcdef0123456789abcdef")}, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	credentialsSvc, err := credentialssvc.New(
		txm,
		repocredentials.New(),
		enc,
		nil,
		clock.New(), false)
	require.NoError(t, err)
	environmentsSvc := environmentssvc.New(
		txm,
		environmentsRepo,
		eventtest.NewProducer(eventRecorder),
		environmentssvc.WithCredentialService(credentialsSvc),
	)

	orgSlug := uniqueEnvironmentDefaultsSlug("env-secret-event-org")
	environmentSlug := uniqueEnvironmentDefaultsSlug("env-secret-event-env")
	userSlug := uniqueEnvironmentDefaultsSlug("env-secret-event-user")
	seedEnvironmentSecretEventOwner(t, ctx, pool, orgSlug, environmentSlug)

	description := "GitHub token"
	_, err = environmentsSvc.UpsertEnvironmentSecret(
		ctx,
		orgSlug,
		environmentSlug,
		model.UpsertEnvironmentSecret{Key: "API_TOKEN", Value: "ghp_super_secret", Description: &description},
		model.UserEventActor(userSlug),
	)
	require.NoError(t, err)

	err = environmentsSvc.DeleteEnvironmentSecret(
		ctx,
		orgSlug,
		environmentSlug,
		"API_TOKEN",
		nil,
		model.UserEventActor(userSlug),
	)
	require.NoError(t, err)

	require.Len(t, eventRecorder.calls, 2)
	require.Equal(t, model.EventActionUpdate, eventRecorder.calls[0].Action)
	require.Equal(t, model.EventActionDelete, eventRecorder.calls[1].Action)
	encodedMetadata, err := json.Marshal(eventRecorder.calls[1].Metadata)
	require.NoError(t, err)
	require.Contains(t, string(encodedMetadata), "API_TOKEN")
	require.NotContains(t, string(encodedMetadata), "ghp_super_secret")
}

type defaultEnvironmentE2EStack struct {
	organizations orgsvc.Service
	environments  environmentssvc.Service
	projects      projectssvc.Service
	users         userssvc.UsersService
}

type recordingEventService struct {
	calls []eventssvc.CreateEventParams
}

func (r *recordingEventService) CreateEvent(
	_ context.Context,
	params eventssvc.CreateEventParams,
) (*model.Event, error) {
	r.calls = append(r.calls, params)
	return &model.Event{
		Slug:       params.Slug,
		ActorType:  params.ActorType,
		ActorSlug:  params.ActorSlug,
		EntityType: params.EntityType,
		EntitySlug: params.EntitySlug,
		Action:     params.Action,
		Metadata:   params.Metadata,
	}, nil
}

type projectEnvironmentRunHistory struct {
	projectEnvironmentID int64
	pipelineSlug         slug.Slug
	environmentSlug      slug.Slug
	runSlug              string
}

func newDefaultEnvironmentE2EStack(pool *pgxpool.Pool) defaultEnvironmentE2EStack {
	txm := postgres.NewTransactionManager(pool)
	eventProducer := eventtest.NewProducer(nil)
	emailProducer := &testutil.FakeEmailProducer{}
	memberInvitationsRepo := memberinvitations.NewRepository()
	membersRepo := members.NewRepository()
	organizationsRepo := organizations.NewRepository()
	usersSvc := userssvc.New(
		txm,
		&testutil.FakeHasher{},
		users.NewRepository(),
		emailProducer,
		eventProducer,
		"https://frontend.example.test",
	)
	rolesSvc := rolessvc.New(txm, roles.NewRepository(), permissions.NewRepository(), eventProducer)
	membersSvc := memberssvc.New(
		txm,
		membersRepo,
		memberInvitationsRepo,
		organizationsRepo,
		rolesSvc,
		usersSvc,
		emailProducer,
		eventProducer,
		"https://frontend.example.test",
		clock.New(),
	)
	environmentsRepo := environments.NewRepository()
	environmentsSvc := environmentssvc.New(txm, environmentsRepo, eventProducer)

	return defaultEnvironmentE2EStack{
		organizations: orgsvc.New(
			txm,
			organizationsRepo,
			environmentsSvc,
			membersSvc,
			rolesSvc,
			eventProducer,
			admissionsvc.New(txm, organizationsRepo),
		),
		environments: environmentsSvc,
		projects: projectssvc.New(
			txm,
			projects.NewRepository(),
			organizations.NewRepository(),
			environmentsRepo,
			nil,
			nil,
			nil,
			nil,
			eventProducer,
		),
		users: usersSvc,
	}
}

func createDefaultEnvironmentE2EOrganization(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	stack defaultEnvironmentE2EStack,
	userSlug, orgSlug slug.Slug,
	name string,
) slug.Slug {
	t.Helper()

	user, err := stack.users.CreateUser(ctx, model.NewUser{
		FullName: name + " Owner",
		Email:    userSlug.String() + "@example.test",
		Password: "password123",
		Role:     model.RoleUser,
		Locale:   "en",
	})
	require.NoError(t, err)

	_, err = stack.organizations.CreateOrganization(ctx, user.Slug, model.UserEventActor(user.Slug), model.NewOrganization{
		Name: name,
		Slug: orgSlug,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		_, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM projects WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, cleanupErr)

		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM users WHERE slug = $1`, user.Slug)
		require.NoError(t, cleanupErr)
	})
	return user.Slug
}

func environmentSlugs(items []model.Environment) []slug.Slug {
	slugs := make([]slug.Slug, 0, len(items))
	for _, item := range items {
		slugs = append(slugs, item.Slug)
	}
	return slugs
}

func uniqueEnvironmentDefaultsSlug(prefix string) slug.Slug {
	return must.Slug(fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()))
}

func seedEnvironmentSecretEventOwner(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	orgSlug, environmentSlug slug.Slug,
) {
	t.Helper()

	_, err := pool.Exec(ctx, `INSERT INTO organizations (name, slug) VALUES ($1, $2)`, "Environment Secret Event Org", orgSlug)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM projects WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug)
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, cleanupErr)
	})

	_, err = pool.Exec(ctx, `
INSERT INTO environments (organization_id, name, slug)
SELECT id, 'Environment Secret Event Environment', $2
FROM organizations
WHERE slug = $1`,
		orgSlug,
		environmentSlug,
	)
	require.NoError(t, err)
}

func seedProjectEnvironmentRunHistory(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) projectEnvironmentRunHistory {
	t.Helper()

	var projectID int64
	var projectEnvironmentID int64
	err := pool.QueryRow(ctx, `
		SELECT p.id, pe.id
		FROM projects p
		JOIN organizations o ON o.id = p.organization_id
		JOIN project_environments pe ON pe.project_id = p.id
		JOIN environments e ON e.id = pe.environment_id
		WHERE o.slug = $1
			AND p.slug = $2
			AND e.slug = $3
	`, orgSlug.String(), projectSlug.String(), environmentSlug.String()).Scan(&projectID, &projectEnvironmentID)
	require.NoError(t, err)

	pipelineSlug := must.Slug("history-pipeline")
	specHash := "history-spec-hash"
	_, err = pool.Exec(ctx, `
			INSERT INTO pipeline_specs (
				project_id, slug, name, spec_jsonb, spec_hash, sync_status,
				source_path, source_ref, last_applied_commit
			)
			VALUES (
				$1, $2, 'History Pipeline', '{}'::jsonb, $3, 'synced',
				'pipelines/history-pipeline.pipeline.json', 'refs/heads/main',
				'0123456789abcdef0123456789abcdef01234567'
			)
	`, projectID, pipelineSlug.String(), specHash)
	require.NoError(t, err)

	var versionID int64
	err = pool.QueryRow(ctx, `
		INSERT INTO pipeline_versions (project_id, pipeline_slug, spec_jsonb, spec_hash)
		VALUES ($1, $2, '{}'::jsonb, $3)
		RETURNING id
	`, projectID, pipelineSlug.String(), specHash).Scan(&versionID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		UPDATE pipeline_specs
		SET current_version_id = $1
		WHERE project_id = $2
			AND slug = $3
	`, versionID, projectID, pipelineSlug.String())
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO pipeline_runs (
			project_id,
			project_environment_id,
			pipeline_version_id,
			pipeline_version_spec_hash,
			api_version,
			pipeline_slug,
			run_slug,
			source_environment_slug,
			status,
			duration_ms,
			step_count,
			step_slug,
			timeline
		)
		VALUES (
			(SELECT project_id FROM project_environments WHERE id=$1),
			$1,
			$2,
			$3,
			'v1',
			$4,
			'history-run',
			$6::text,
			'succeeded',
			12,
			1,
				'extract',
				jsonb_build_object(
					'run_slug', 'history-run',
					'pipeline_slug', $5::text,
					'environment_slug', $6::text,
					'status', 'succeeded',
					'events', '[]'::jsonb
				)
			)
	`, projectEnvironmentID, versionID, specHash, pipelineSlug.String(), pipelineSlug.String(), environmentSlug.String())
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO pipeline_checkpoint (
			project_environment_id,
			run_slug,
			step_slug,
			pipeline_spec,
			step_results
		)
		VALUES ($1, 'history-run', 'extract', '{}'::jsonb, '{}'::jsonb)
	`, projectEnvironmentID)
	require.NoError(t, err)

	return projectEnvironmentRunHistory{
		projectEnvironmentID: projectEnvironmentID,
		pipelineSlug:         pipelineSlug,
		environmentSlug:      environmentSlug,
		runSlug:              "history-run",
	}
}

func assertProjectEnvironmentSoftDeleted(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectEnvironmentID int64,
) {
	t.Helper()

	var deleted bool
	var isDefault bool
	err := pool.QueryRow(ctx, `
		SELECT deleted_at IS NOT NULL, is_default
		FROM project_environments
		WHERE id = $1
	`, projectEnvironmentID).Scan(&deleted, &isDefault)
	require.NoError(t, err)
	require.True(t, deleted)
	require.False(t, isDefault)
}

func assertProjectEnvironmentRestored(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectEnvironmentID int64,
) {
	t.Helper()

	var deleted bool
	var isDefault bool
	err := pool.QueryRow(ctx, `
		SELECT deleted_at IS NOT NULL, is_default
		FROM project_environments
		WHERE id = $1
	`, projectEnvironmentID).Scan(&deleted, &isDefault)
	require.NoError(t, err)
	require.False(t, deleted)
	require.True(t, isDefault)
}

func assertProjectEnvironmentRunHistoryRows(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	projectEnvironmentID int64,
	wantRuns int,
	wantCheckpoints int,
) {
	t.Helper()

	var runRows int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM pipeline_runs
		WHERE project_environment_id = $1
	`, projectEnvironmentID).Scan(&runRows)
	require.NoError(t, err)
	require.Equal(t, wantRuns, runRows)

	var checkpointRows int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM pipeline_checkpoint
		WHERE project_environment_id = $1
	`, projectEnvironmentID).Scan(&checkpointRows)
	require.NoError(t, err)
	require.Equal(t, wantCheckpoints, checkpointRows)
}

func assertDeletedEnvironmentRunHistoryVisible(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	orgSlug, projectSlug slug.Slug,
	runHistory projectEnvironmentRunHistory,
) {
	t.Helper()

	runRepo := pipelineruns.New()
	runs, err := runRepo.ListRuns(
		ctx,
		pool,
		orgSlug,
		projectSlug,
		runHistory.environmentSlug,
		nil,
		model.ListRequest{Limit: 10, Order: model.ListOrderDesc},
	)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, runHistory.runSlug, runs[0].RunSlug)

	run, err := runRepo.FindRunBySlug(
		ctx,
		pool,
		orgSlug,
		projectSlug,
		runHistory.environmentSlug,
		runHistory.runSlug,
	)
	require.NoError(t, err)
	require.Equal(t, runHistory.pipelineSlug.String(), run.PipelineSlug)

	latest, err := runRepo.FindLatestRunByPipelineSlug(
		ctx,
		pool,
		orgSlug,
		projectSlug,
		runHistory.environmentSlug,
		runHistory.pipelineSlug,
	)
	require.NoError(t, err)
	require.Equal(t, runHistory.runSlug, latest.RunSlug)

	timelineStore := checkpoints.New(pool)
	timeline, err := timelineStore.Get(
		ctx,
		orgSlug,
		projectSlug,
		runHistory.environmentSlug,
		runHistory.runSlug,
	)
	require.NoError(t, err)
	require.Equal(t, runHistory.runSlug, timeline.RunSlug)

	checkpoint, err := timelineStore.GetCheckpoint(
		ctx,
		orgSlug,
		projectSlug,
		runHistory.environmentSlug,
		runHistory.runSlug,
	)
	require.NoError(t, err)
	require.Equal(t, runHistory.runSlug, checkpoint.RunSlug)
}
