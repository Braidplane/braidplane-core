package e2e

import (
	"context"
	"encoding/json"
	"os"
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
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
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

func TestService_RunPersistenceIntegration(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres run persistence integration test in short mode")
	}

	pool := openIntegrationPool(t)
	ctx := context.Background()

	orgASlug := must.Slug("run-persistence-org-a")
	orgBSlug := must.Slug("run-persistence-org-b")
	runSlug := "run-persistence-001"
	replayOfRunSlug := "run-persistence-000"
	replayFromStep := "persist-users"
	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: must.Slug("users-sync"),
			Name: "Users Sync",
		},
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

	seedOrganization(t, pool, orgASlug, "Run Persistence Org A")
	seedOrganization(t, pool, orgBSlug, "Run Persistence Org B")
	seedPipelineDeploymentEnvironment(t, pool, orgASlug, defaultProjectSlug, must.Slug("prod"))
	seedPipelineDeploymentEnvironment(t, pool, orgBSlug, defaultProjectSlug, must.Slug("prod"))
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, `DELETE FROM project_environments WHERE organization_id IN (SELECT id FROM organizations WHERE slug = $1 OR slug = $2)`, orgASlug, orgBSlug)
		require.NoError(t, err)
		_, err = pool.Exec(
			ctx,
			`DELETE FROM organizations WHERE slug = $1 OR slug = $2`,
			orgASlug,
			orgBSlug,
		)
		require.NoError(t, err)
	})

	txm := postgres.NewTransactionManager(pool)
	pipelineStore, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	materializePipelines(t, ctx, pipelineStore, orgASlug, defaultProjectSlug, pipeline)
	specMeta, err := pipelineStore.FindSpec(ctx, orgASlug, defaultProjectSlug, pipeline.Metadata.Slug)
	require.NoError(t, err)
	require.NotNil(t, specMeta.CurrentVersionID)

	runStore := pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New())
	svc, err := pipelinesvc.New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		pipelineStore,
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

	startedAt := time.Date(2026, time.April, 12, 8, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(30 * time.Second)
	runResult := &pipelinerun.RunResult{
		Metadata: pipelinerun.RunMetadata{
			EnvironmentSlug:   must.Slug("prod"),
			RunSlug:           runSlug,
			Status:            "succeeded",
			DurationMs:        30000,
			StepCount:         2,
			PipelineVersionID: *specMeta.CurrentVersionID,
			ReplayOfRunSlug:   &replayOfRunSlug,
			ReplayFromStep:    &replayFromStep,
		},
		Timeline: pipelinerun.RunTimeline{
			RunSlug:         runSlug,
			PipelineSlug:    pipeline.Metadata.Slug,
			EnvironmentSlug: must.Slug("prod"),
			Status:          "succeeded",
			ReplayOfRunSlug: &replayOfRunSlug,
			Events: []pipelinerun.TimelineEvent{
				{At: startedAt, Type: "run_started", Status: "running"},
				{At: endedAt, Type: "run_completed", Status: "succeeded"},
			},
		},
	}

	err = runStore.PersistRun(ctx, orgASlug, defaultProjectSlug, must.Slug("prod"), pipeline, runResult)
	require.NoError(t, err)

	checkpoint := pipelinerun.ReplayCheckpoint{
		CompiledPlan: []byte(`{"exact":"snapshot bytes"}`), CompiledPlanDigest: "sha256:checkpoint-test",
		RunSlug:         runSlug,
		OrgSlug:         orgASlug,
		EnvironmentSlug: must.Slug("prod"),
		StepSlug:        must.Slug(replayFromStep),
		Pipeline:        pipeline,
		Results:         map[slug.Slug]*pipelinerun.StepResult{},
	}
	err = runStore.SaveCheckpoint(ctx, orgASlug, defaultProjectSlug, must.Slug("prod"), runSlug, checkpoint)
	require.NoError(t, err)

	t.Run("OrgASeesPersistedRun", func(t *testing.T) {
		t.Parallel()
		listResp, err := svc.ListRuns(ctx, orgASlug, defaultProjectSlug, must.Slug("prod"), nil, model.ListRequest{Limit: 10})
		require.NoError(t, err)
		require.False(t, listResp.HasMore)
		require.Len(t, listResp.Items, 1)
		require.Equal(t, runSlug, listResp.Items[0].RunSlug)
		require.Equal(t, string(pipeline.Metadata.Slug), listResp.Items[0].PipelineSlug)
		require.Equal(t, "succeeded", listResp.Items[0].Status)
		require.Equal(t, int32(2), listResp.Items[0].StepCount)
		require.Equal(t, "persist-users", listResp.Items[0].StepSlug)
		require.True(t, listResp.Items[0].StartedAt.Equal(startedAt))
		require.NotNil(t, listResp.Items[0].EndedAt)
		require.True(t, listResp.Items[0].EndedAt.Equal(endedAt))

		run, err := svc.GetRun(ctx, orgASlug, defaultProjectSlug, must.Slug("prod"), runSlug)
		require.NoError(t, err)
		require.Equal(t, runSlug, run.Metadata.RunSlug)
		require.Equal(t, string(pipeline.Metadata.Slug), run.Metadata.PipelineSlug)
		require.Equal(t, "persist-users", run.Metadata.StepSlug)
		require.NotNil(t, run.Metadata.ReplayOfRunSlug)
		require.Equal(t, replayOfRunSlug, *run.Metadata.ReplayOfRunSlug)
		require.NotNil(t, run.Metadata.ReplayFromStep)
		require.Equal(t, replayFromStep, *run.Metadata.ReplayFromStep)
		require.NotNil(t, run.Timeline.ReplayOfRunSlug)
		require.Equal(t, replayOfRunSlug, *run.Timeline.ReplayOfRunSlug)
		require.Len(t, run.Timeline.Events, 2)
		require.True(t, run.Timeline.Events[0].At.Equal(startedAt))

		persistedCheckpoint, err := runStore.GetCheckpoint(ctx, orgASlug, defaultProjectSlug, must.Slug("prod"), runSlug)
		require.NoError(t, err)
		require.Equal(t, runSlug, persistedCheckpoint.RunSlug)
		require.Equal(t, orgASlug, persistedCheckpoint.OrgSlug)
		require.Equal(t, must.Slug(replayFromStep), persistedCheckpoint.StepSlug)
		require.NotNil(t, persistedCheckpoint.Pipeline)
		require.Equal(t, pipeline.Metadata.Slug, persistedCheckpoint.Pipeline.Metadata.Slug)
		require.Empty(t, persistedCheckpoint.Results)
		require.Equal(t, checkpoint.CompiledPlan, persistedCheckpoint.CompiledPlan)
		require.Equal(t, checkpoint.CompiledPlanDigest, persistedCheckpoint.CompiledPlanDigest)
	})

	t.Run("OrgBCannotSeePersistedRun", func(t *testing.T) {
		t.Parallel()
		listResp, err := svc.ListRuns(ctx, orgBSlug, defaultProjectSlug, must.Slug("prod"), nil, model.ListRequest{Limit: 10})
		require.NoError(t, err)
		require.Empty(t, listResp.Items)

		_, err = svc.GetRun(ctx, orgBSlug, defaultProjectSlug, must.Slug("prod"), runSlug)
		require.ErrorIs(t, err, pipelinesvc.ErrPipelineRunNotFound)

		_, err = runStore.GetCheckpoint(ctx, orgBSlug, defaultProjectSlug, must.Slug("prod"), runSlug)
		require.ErrorIs(t, err, pipelinerun.ErrReplayCheckpointNotFound)
	})
}

func TestService_EventInputRunHistoryPersistence(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres event input run persistence integration test in short mode")
	}

	pool := openIntegrationPool(t)
	ctx := context.Background()
	orgSlug := must.Slug("event-input-run-history-org")
	projectSlug := defaultProjectSlug
	environmentSlug := must.Slug("prod")
	runSlug := "event-input-run-history-001"
	contract := &payload.Contract{
		Name:           "event",
		Representation: model.ContractRepresentationJSON,
		RootShape:      model.ContractRootShapeObject,
		Fields:         []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueString, Required: true}},
	}
	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "event-input-run-history", Name: "Event Input Run History"},
		Steps: []pipelinespec.Step{
			{
				Kind:  "input",
				Slug:  "event",
				Input: &pipelinespec.InputSpec{OutputContract: contract},
			},
			{
				Kind: "sink",
				Slug: "persist-event",
				Sink: &pipelinespec.SinkSpec{
					Input:            &pipelinespec.StepInput{FromStep: "event"},
					InputContract:    contract,
					DataSourceSlug:   "event-sink",
					OperationType:    "http",
					OperationRef:     "persist-event",
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
				},
			},
		},
	}

	seedOrganization(t, pool, orgSlug, "Event Input Run History Org")
	seedPipelineDeploymentEnvironment(t, pool, orgSlug, projectSlug, environmentSlug)
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, err)
	})

	txm := postgres.NewTransactionManager(pool)
	pipelineStore, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	materializePipelines(t, ctx, pipelineStore, orgSlug, projectSlug, pipeline)
	specMeta, err := pipelineStore.FindSpec(ctx, orgSlug, projectSlug, pipeline.Metadata.Slug)
	require.NoError(t, err)
	require.NotNil(t, specMeta.CurrentVersionID)

	eventInput, err := pipelinerun.NewVerifiedJSONEventInput("event", json.RawMessage(`{"id":"evt-1","secret":"raw-value"}`))
	require.NoError(t, err)
	summary := eventInput.Summary()
	now := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	runResult := &pipelinerun.RunResult{
		Metadata: pipelinerun.RunMetadata{
			EnvironmentSlug:   environmentSlug,
			RunSlug:           runSlug,
			Status:            "succeeded",
			StepCount:         2,
			PipelineVersionID: *specMeta.CurrentVersionID,
		},
		Timeline: pipelinerun.RunTimeline{
			RunSlug:         runSlug,
			PipelineSlug:    pipeline.Metadata.Slug,
			EnvironmentSlug: environmentSlug,
			Status:          "succeeded",
			Events:          []pipelinerun.TimelineEvent{{At: now, Type: "run_completed", Status: "succeeded"}},
			Snapshots: []pipelinerun.StepSnapshot{{
				Step: "event",
				Kind: "input",
				Input: pipelinerun.StepInputSnapshot{
					OrganizationSlug: orgSlug,
					ProjectSlug:      projectSlug,
					EnvironmentSlug:  environmentSlug,
					ParameterKeys:    []string{},
					EventInput:       summary,
				},
			}},
		},
		StepResults: map[slug.Slug]*pipelinerun.StepResult{
			"event": pipelinerun.NewRunInputStepResult(eventInput.Payload()),
		},
	}

	runStore := pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New())
	require.NoError(t, runStore.PersistRun(ctx, orgSlug, projectSlug, environmentSlug, pipeline, runResult))
	persisted, err := runStore.Get(ctx, orgSlug, projectSlug, environmentSlug, runSlug)
	require.NoError(t, err)
	require.Equal(t, summary, persisted.Snapshots[0].Input.EventInput)
	persistedJSON, err := json.Marshal(persisted)
	require.NoError(t, err)
	require.NotContains(t, string(persistedJSON), "raw-value")
}

func openIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connString := "postgres://postgres:postgres@localhost:35432/braidplane?sslmode=disable"
	if envConnString := os.Getenv("DATABASE_URL"); envConnString != "" {
		connString = envConnString
	}

	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("required real-Postgres run persistence integration test: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("required real-Postgres run persistence integration test: %v", err)
	}

	t.Cleanup(pool.Close)
	return pool
}

func seedOrganization(t *testing.T, pool *pgxpool.Pool, orgSlug slug.Slug, name string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(
		ctx,
		`INSERT INTO organizations (name, slug) VALUES ($1, $2) ON CONFLICT (slug) DO NOTHING`,
		name,
		orgSlug,
	)
	require.NoError(t, err)

	_, err = pool.Exec(
		ctx,
		`INSERT INTO projects (organization_id, name, slug)
		SELECT id, 'Default Project', 'default'
		FROM organizations
		WHERE slug = $1
		ON CONFLICT (organization_id, slug) DO NOTHING`,
		orgSlug,
	)
	require.NoError(t, err)
}
