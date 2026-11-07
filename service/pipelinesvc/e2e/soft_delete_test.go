package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"

	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"

	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/postgres"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

func TestService_GitSourceRemovalAndRematerializationPreserveRunsDraftsAndCheckpoints_Integration(
	t *testing.T,
) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres pipeline soft-delete service integration test in short mode")
	}

	ctx := context.Background()
	pool := openIntegrationPool(t)
	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	runStore := pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New())
	svc, err := pipelinesvc.New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		store,
		pool,
		txm,
		pipelineruns.New(), incrementalcheckpoints.New(),
		runStore,
		driftimpactrepo.New(),
		noopQueryExecutor{},
		allowAuthorizationService{},
		&fakes.AdmissionService{},
		&fakes.DataSourceService{},
		&fakes.ProjectsService{},
		&ownershipGitSourceResolver{},
		clock.New(),
		noopPipelineRunProducer{},
		noopPipelineGitProducer{},
		eventtest.NewProducer(nil),
		noopPipelineRuntimeResolver{},
		testSinkEffectLedger(t, txm),
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	unique := time.Now().UnixNano()
	orgSlug := must.Slug(fmt.Sprintf("pipeline-service-soft-delete-org-%d", unique))
	actorSlug := must.Slug(fmt.Sprintf("pipeline-service-soft-delete-user-%d", unique))
	seedPipelineSoftDeleteOrgMember(t, pool, orgSlug, actorSlug)

	pipelineSlug := must.Slug("pipeline-service-soft-delete")
	runSlug := fmt.Sprintf("pipeline-service-soft-delete-run-%d", unique)
	replayFromStep := "persist-users"
	pipeline := softDeleteIntegrationPipeline(pipelineSlug)

	materializePipelines(t, ctx, store, orgSlug, defaultProjectSlug, pipeline)
	seedPipelineDeploymentEnvironment(t, pool, orgSlug, defaultProjectSlug, must.Slug("prod"))
	specMeta, err := store.FindSpec(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
	require.NoError(t, err)
	require.NotNil(t, specMeta.CurrentVersionID)
	require.NotNil(t, specMeta.CurrentVersionSpecHash)

	draftPipeline := softDeleteIntegrationPipeline(pipelineSlug)
	draftPipeline.Metadata.Name = "Recovered Draft"
	err = store.SaveDraft(ctx, orgSlug, defaultProjectSlug, actorSlug, draftPipeline, specMeta.CurrentVersionSpecHash)
	require.NoError(t, err)
	persistSoftDeleteIntegrationRun(
		t,
		ctx,
		runStore,
		orgSlug,
		pipeline,
		*specMeta.CurrentVersionID,
		runSlug,
		replayFromStep,
	)

	materializePipelines(t, ctx, store, orgSlug, defaultProjectSlug)

	_, err = svc.Get(ctx, orgSlug, defaultProjectSlug, pipelineSlug, actorSlug)
	require.ErrorIs(t, err, pipelinesvc.ErrPipelineNotFound)
	actor := model.UserEventActor(actorSlug)
	_, err = svc.Execute(
		ctx,
		orgSlug,
		defaultProjectSlug,
		must.Slug("prod"),
		pipelineSlug,
		nil,
		nil,
		actor,
		e2eRunSource(actor),
		nil,
	)
	require.ErrorIs(t, err, pipelinesvc.ErrPipelineNotFound)
	_, err = svc.GetDraft(ctx, orgSlug, defaultProjectSlug, pipelineSlug, actorSlug)
	require.ErrorIs(t, err, pipelinesvc.ErrPipelineDraftNotFound)
	_, err = svc.ListVersions(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
	require.ErrorIs(t, err, pipelinesvc.ErrPipelineNotFound)
	runs, err := svc.ListRuns(
		ctx,
		orgSlug,
		defaultProjectSlug,
		must.Slug("prod"),
		&pipelinesvc.RunFilters{PipelineSlug: &pipelineSlug},
		model.ListRequest{Limit: 20},
	)
	require.NoError(t, err)
	require.Empty(t, runs.Items)
	_, err = svc.GetRun(ctx, orgSlug, defaultProjectSlug, must.Slug("prod"), runSlug)
	require.ErrorIs(t, err, pipelinesvc.ErrPipelineRunNotFound)
	_, err = runStore.Get(ctx, orgSlug, defaultProjectSlug, must.Slug("prod"), runSlug)
	require.ErrorIs(t, err, pipelinerun.ErrTimelineNotFound)
	_, err = runStore.GetCheckpoint(ctx, orgSlug, defaultProjectSlug, must.Slug("prod"), runSlug)
	require.ErrorIs(t, err, pipelinerun.ErrReplayCheckpointNotFound)

	materializePipelines(t, ctx, store, orgSlug, defaultProjectSlug, pipeline)

	restored, err := svc.Get(ctx, orgSlug, defaultProjectSlug, pipelineSlug, actorSlug)
	require.NoError(t, err)
	require.Equal(t, pipelineSlug, restored.Summary.Slug)
	require.NotNil(t, restored.Draft)
	require.Equal(t, "Recovered Draft", restored.Draft.Spec.Metadata.Name)

	versions, err := svc.ListVersions(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
	require.NoError(t, err)
	require.Len(t, versions, 1)

	runs, err = svc.ListRuns(
		ctx,
		orgSlug,
		defaultProjectSlug,
		must.Slug("prod"),
		&pipelinesvc.RunFilters{PipelineSlug: &pipelineSlug},
		model.ListRequest{Limit: 20},
	)
	require.NoError(t, err)
	require.Len(t, runs.Items, 1)
	require.Equal(t, runSlug, runs.Items[0].RunSlug)

	run, err := svc.GetRun(ctx, orgSlug, defaultProjectSlug, must.Slug("prod"), runSlug)
	require.NoError(t, err)
	require.Equal(t, runSlug, run.Metadata.RunSlug)

	checkpoint, err := runStore.GetCheckpoint(ctx, orgSlug, defaultProjectSlug, must.Slug("prod"), runSlug)
	require.NoError(t, err)
	require.Equal(t, runSlug, checkpoint.RunSlug)
	require.Equal(t, must.Slug(replayFromStep), checkpoint.StepSlug)
	require.Equal(t, pipelineSlug, checkpoint.Pipeline.Metadata.Slug)
}

func seedPipelineSoftDeleteOrgMember(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug slug.Slug,
	actorSlug slug.Slug,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, `
		INSERT INTO organizations (name, slug)
		VALUES ($1, $2)
	`, "Pipeline Soft Delete Org", orgSlug.String())
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO projects (organization_id, name, slug)
		SELECT id, 'Default project', 'default'
		FROM organizations
		WHERE slug = $1
	`, orgSlug.String())
	require.NoError(t, err)

	userEmail := fmt.Sprintf("%s@example.com", actorSlug.String())
	_, err = pool.Exec(ctx, `
		WITH org_row AS (
			SELECT id FROM organizations WHERE slug = $1
		),
		user_row AS (
			INSERT INTO users (slug, email, full_name, role, locale)
			VALUES ($2, $3, 'Pipeline Soft Delete User', 'user', 'en')
			RETURNING id
		),
		role_row AS (
			INSERT INTO roles (organization_id, slug, name, is_default)
			SELECT org_row.id, 'owner', 'Owner', true FROM org_row
			RETURNING id
		)
		INSERT INTO organization_members (organization_id, user_id, role_id)
		SELECT org_row.id, user_row.id, role_row.id
		FROM org_row, user_row, role_row
	`, orgSlug.String(), actorSlug.String(), userEmail)
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()

		_, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug.String())
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, orgSlug.String())
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM users WHERE slug = $1`, actorSlug.String())
		require.NoError(t, cleanupErr)
	})
}

func softDeleteIntegrationPipeline(pipelineSlug slug.Slug) *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: pipelineSlug, Name: "Soft Delete Pipeline"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("acme-postgres"),
					OperationType:  "sql",
					OperationRef:   must.Slug("list-users"),
					OutputContract: pipelineFixtureContract(),
				},
			},
			{
				Kind: "sink",
				Slug: must.Slug("persist-users"),
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
					Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
					InputContract:    pipelineFixtureContract(),
					DataSourceSlug:   must.Slug("acme-postgres"),
					OperationType:    "sql",
					OperationRef:     must.Slug("persist-users"),
				},
			},
		},
	}
}

func persistSoftDeleteIntegrationRun(
	t *testing.T,
	ctx context.Context,
	runStore pipelinerun.RunStore,
	orgSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	versionID int64,
	runSlug string,
	replayFromStep string,
) {
	t.Helper()

	startedAt := time.Date(2026, time.May, 13, 12, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(15 * time.Second)
	runResult := &pipelinerun.RunResult{
		Metadata: pipelinerun.RunMetadata{
			EnvironmentSlug:   must.Slug("prod"),
			RunSlug:           runSlug,
			Status:            "succeeded",
			DurationMs:        15000,
			StepCount:         len(pipeline.Steps),
			PipelineVersionID: versionID,
			ReplayFromStep:    &replayFromStep,
		},
		Timeline: pipelinerun.RunTimeline{
			RunSlug:         runSlug,
			PipelineSlug:    pipeline.Metadata.Slug,
			EnvironmentSlug: must.Slug("prod"),
			Status:          "succeeded",
			Events: []pipelinerun.TimelineEvent{
				{At: startedAt, Type: "run_started", Status: "running"},
				{At: endedAt, Type: "run_completed", Status: "succeeded"},
			},
		},
	}
	err := runStore.PersistRun(ctx, orgSlug, defaultProjectSlug, must.Slug("prod"), pipeline, runResult)
	require.NoError(t, err)

	checkpoint := pipelinerun.ReplayCheckpoint{
		RunSlug:         runSlug,
		OrgSlug:         orgSlug,
		EnvironmentSlug: must.Slug("prod"),
		StepSlug:        must.Slug(replayFromStep),
		Pipeline:        pipeline,
		Results:         map[slug.Slug]*pipelinerun.StepResult{},
	}
	err = runStore.SaveCheckpoint(ctx, orgSlug, defaultProjectSlug, must.Slug("prod"), runSlug, checkpoint)
	require.NoError(t, err)
}
