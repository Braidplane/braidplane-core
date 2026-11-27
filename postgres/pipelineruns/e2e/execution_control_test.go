package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/postgres/pipelinewebhooks"
)

func TestPipelineExecutionControlFencesClaimsAndPreservesEffects(t *testing.T) {
	if testing.Short() {
		t.Skip("requires the disposable PostgreSQL fixture")
	}
	ctx := t.Context()
	pool := openPipelineRunsE2EPool(t)
	target := pipelineruns.ExecutionScope{Org: "pause-tenant", Project: "etl", Environment: "prod", Pipeline: "orders"}
	otherTenant := target
	otherTenant.Org = "other-pause-tenant"
	now := time.Now().UTC()
	envID := seedPipelineRunSourceEventRun(t, pool, target.Org, target.Project, target.Environment, target.Pipeline, "initial", now, "queued")
	seedPipelineRunSourceEventRun(t, pool, otherTenant.Org, otherTenant.Project, otherTenant.Environment, otherTenant.Pipeline, "initial", now, "queued")
	otherEnv := target
	otherEnv.Environment = "dev"
	otherPipeline := target
	otherPipeline.Pipeline = "customers"
	_, err := pool.Exec(ctx, `
		WITH scope AS (SELECT * FROM project_environments WHERE id = $1),
		environment AS (
			INSERT INTO environments (organization_id, name, slug)
			SELECT organization_id, 'Dev', 'dev' FROM scope RETURNING *
		), assignment AS (
			INSERT INTO project_environments (organization_id, project_id, environment_id)
			SELECT s.organization_id, s.project_id, e.id FROM scope s CROSS JOIN environment e RETURNING *
		), pipeline AS (
			INSERT INTO pipeline_specs (project_id, slug, name, spec_jsonb, spec_hash, source_path, source_ref, last_applied_commit)
			SELECT project_id, 'customers', 'Customers', '{}'::jsonb, 'customers', 'customers.yaml', 'refs/heads/main', repeat('a',40)
			FROM scope RETURNING *
		)
		INSERT INTO pipeline_runs (project_id, project_environment_id, api_version, pipeline_slug, run_slug, source_environment_slug, status, duration_ms, step_slug, timeline)
		SELECT project_id, id, 'v1', 'orders', 'initial', 'dev', 'queued', 0, '', '{}'::jsonb FROM assignment
		UNION ALL
		SELECT project_id, $1, 'v1', 'customers', 'sibling', 'prod', 'queued', 0, '', '{}'::jsonb FROM pipeline`, envID)
	require.NoError(t, err)

	repo := pipelineruns.New()
	actor := model.UserEventActor("operator")
	mark := func(ctx context.Context, db pgx.Tx, scope pipelineruns.ExecutionScope, run string) error {
		_, err := repo.MarkRunRunning(ctx, db, scope.Org, scope.Project, scope.Environment, scope.Pipeline, run, time.Now().UTC(), []byte(`{}`))
		return err
	}
	state, err := pipelineruns.InspectExecutionControl(ctx, pool, target)
	require.NoError(t, err)
	require.False(t, state.Paused)
	require.EqualValues(t, 1, state.QueuedCount)

	// The half-finished claim must hold the pause until its active row is visible.
	worker, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = worker.Rollback(context.Background()) }()
	require.NoError(t, mark(ctx, worker, target, "initial"))
	operator, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = operator.Rollback(context.Background()) }()
	operatorPID := operator.Conn().PgConn().PID()
	require.NoError(t, pipelineruns.EnsureExecutionControl(ctx, operator, target))
	pauseCtx, cancelPause := context.WithCancel(ctx)
	pauseDone := make(chan struct{})
	var pauseErr error
	go func() {
		defer close(pauseDone)
		_, pauseErr = pipelineruns.SetExecutionControl(pauseCtx, operator, target, 0, true, actor)
	}()
	defer func() {
		cancelPause()
		<-pauseDone
	}()
	require.Eventually(t, func() bool {
		var blocked bool
		return pool.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, operatorPID).Scan(&blocked) == nil && blocked
	}, 5*time.Second, 25*time.Millisecond)
	require.NoError(t, worker.Commit(ctx))
	<-pauseDone
	require.NoError(t, pauseErr)
	require.NoError(t, operator.Commit(ctx))
	state, err = pipelineruns.InspectExecutionControl(ctx, pool, target)
	require.NoError(t, err)
	require.True(t, state.Paused)
	require.EqualValues(t, 1, state.Revision)
	require.NotNil(t, state.ChangedAt)
	require.Equal(t, "operator", *state.ChangedBySlug)
	require.EqualValues(t, 1, state.ActiveCount)

	// One effect was confirmed and another reached the provider boundary with no confirmation.
	_, err = pool.Exec(ctx, `
		INSERT INTO pipeline_sink_effects (project_environment_id, run_slug, sink_step_slug, idempotency_key,
			idempotency_class, replay_method, replay_decision, state, succeeded_at)
		VALUES ($1, 'initial', 'first', 'fixture-one', 'natural', 'none', 'execute', 'succeeded', now()),
		       ($1, 'initial', 'second', 'fixture-two', 'natural', 'none', 'execute', 'in_progress', NULL)`, envID)
	require.NoError(t, err)
	state, err = pipelineruns.InspectExecutionControl(ctx, pool, target)
	require.NoError(t, err)
	require.True(t, state.Attempts[0].KnownCompletedWrite)
	require.True(t, state.Attempts[0].OutcomeUncertain)
	require.EqualValues(t, 1, state.UncertainCount)

	// Existing trigger intake and new authenticated automated arrivals can wait.
	for _, source := range []model.PipelineRunSourceType{model.PipelineRunSourceTypeSchedule, model.PipelineRunSourceTypeWebhook, model.PipelineRunSourceTypeCompletion} {
		_, err := repo.CreateRun(ctx, pool, target.Org, target.Project, target.Environment, &pipelineruns.CreateRunParams{
			PipelineSlug: target.Pipeline.String(), RunSlug: source.String() + "-held", Status: "queued",
			Source:    pipelinerun.RunSourceSummary{Type: source, AttemptNumber: 2, RootRunSlug: "initial"},
			StartedAt: now, Timeline: []byte(`{}`),
		})
		require.NoError(t, err)
	}
	var version int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT pipeline_version_id FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='initial'`, envID).Scan(&version))
	for _, status := range []string{"queued", "running"} {
		_, err := repo.CreateRun(ctx, pool, target.Org, target.Project, target.Environment, &pipelineruns.CreateRunParams{
			PipelineVersionID: version, PipelineSlug: target.Pipeline.String(), RunSlug: "operator-" + status,
			Status: status, Source: pipelinerun.RunSourceSummary{Type: model.PipelineRunSourceTypeManual},
			StartedAt: now, Timeline: []byte(`{}`),
		})
		require.ErrorIs(t, err, pipelineruns.ErrExecutionPaused)
	}
	// Simulate two deliveries across worker instances, including the completion worker's repeatable-read claim.
	for _, level := range []pgx.TxIsoLevel{pgx.ReadCommitted, pgx.RepeatableRead} {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: level})
		require.NoError(t, err)
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		require.ErrorIs(t, mark(ctx, tx, target, "schedule-held"), pipelineruns.ErrExecutionPaused)
		require.NoError(t, tx.Rollback(ctx))
	}
	for _, other := range []struct {
		scope pipelineruns.ExecutionScope
		run   string
	}{{otherTenant, "initial"}, {otherEnv, "initial"}, {otherPipeline, "sibling"}} {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		require.NoError(t, mark(ctx, tx, other.scope, other.run))
		require.NoError(t, tx.Commit(ctx))
	}

	_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET status='failed', ended_at=$2 WHERE project_environment_id=$1 AND run_slug='initial'`, envID, now.Add(-time.Minute))
	require.NoError(t, err)
	state, err = pipelineruns.InspectExecutionControl(ctx, pool, target)
	require.NoError(t, err)
	require.EqualValues(t, 0, state.ActiveCount)
	require.EqualValues(t, 3, state.QueuedCount)
	require.EqualValues(t, 1, state.FinishedCount)
	finished := state.Attempts[len(state.Attempts)-1]
	require.Equal(t, "finished", finished.Phase)
	require.True(t, finished.OutcomeUncertain)
	require.EqualValues(t, 1, state.UncertainCount)

	changed, err := pipelineruns.SetExecutionControl(ctx, pool, target, 0, false, actor)
	require.NoError(t, err)
	require.False(t, changed, "a delayed resume must not override a newer pause")
	changed, err = pipelineruns.SetExecutionControl(ctx, pool, target, 1, false, actor)
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = pipelineruns.SetExecutionControl(ctx, pool, target, 1, false, actor)
	require.NoError(t, err)
	require.False(t, changed)
	// A completion worker can already have a repeatable-read snapshot when the pause commits.
	stale, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	require.NoError(t, err)
	defer func() { _ = stale.Rollback(context.Background()) }()
	_, err = pipelineruns.InspectExecutionControl(ctx, stale, target)
	require.NoError(t, err)
	direct, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	require.NoError(t, err)
	defer func() { _ = direct.Rollback(context.Background()) }()
	_, err = pipelineruns.InspectExecutionControl(ctx, direct, target)
	require.NoError(t, err)
	changed, err = pipelineruns.SetExecutionControl(ctx, pool, target, 2, true, actor)
	require.NoError(t, err)
	require.True(t, changed)
	require.ErrorIs(t, mark(ctx, stale, target, "schedule-held"), pipelineruns.ErrExecutionControlRevision)
	require.NoError(t, stale.Rollback(ctx))
	_, err = repo.CreateRun(ctx, direct, target.Org, target.Project, target.Environment, &pipelineruns.CreateRunParams{
		PipelineVersionID: version, PipelineSlug: target.Pipeline.String(), RunSlug: "stale-direct-start",
		Status: "running", Source: pipelinerun.RunSourceSummary{Type: model.PipelineRunSourceTypeManual},
		StartedAt: now, Timeline: []byte(`{}`),
	})
	require.ErrorIs(t, err, pipelineruns.ErrExecutionControlRevision)
	require.NoError(t, direct.Rollback(ctx))
	changed, err = pipelineruns.SetExecutionControl(ctx, pool, target, 3, false, actor)
	require.NoError(t, err)
	require.True(t, changed)

	for _, expected := range []error{nil, pipelineruns.ErrRunNotFound} {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		require.ErrorIs(t, mark(ctx, tx, target, "schedule-held"), expected)
		require.NoError(t, tx.Commit(ctx))
	}

	// Recovery can finish in a repeatable-read transaction that began before a pause.
	finish, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	require.NoError(t, err)
	defer func() { _ = finish.Rollback(context.Background()) }()
	_, err = pipelineruns.InspectExecutionControl(ctx, finish, target)
	require.NoError(t, err)
	changed, err = pipelineruns.SetExecutionControl(ctx, pool, target, 4, true, actor)
	require.NoError(t, err)
	require.True(t, changed)
	_, err = finish.Exec(ctx, `UPDATE pipeline_runs SET status='failed', ended_at=$2
		WHERE project_environment_id=$1 AND run_slug='schedule-held'`, envID, now.Add(-time.Minute))
	require.NoError(t, err)
	require.NoError(t, finish.Commit(ctx))
	state, err = pipelineruns.InspectExecutionControl(ctx, pool, target)
	require.NoError(t, err)
	require.EqualValues(t, 1, state.FinishedCount)
	require.Equal(t, "schedule-held", state.Attempts[len(state.Attempts)-1].RunSlug)
}

func TestWebhookPreflightResetReturnsRunToPauseFence(t *testing.T) {
	if testing.Short() {
		t.Skip("requires the disposable PostgreSQL fixture")
	}
	ctx := t.Context()
	pool := openPipelineRunsE2EPool(t)
	scope := pipelineruns.ExecutionScope{Org: "webhook-pause-org", Project: "etl", Environment: "prod", Pipeline: "orders"}
	env := seedPipelineRunSourceEventRun(t, pool, scope.Org, scope.Project, scope.Environment, scope.Pipeline, "template", time.Now().UTC(), "queued")
	_, err := pool.Exec(ctx, `
		WITH trigger AS (
			INSERT INTO pipeline_triggers(project_id, project_environment_id, pipeline_id, pipeline_slug, slug, name, trigger_type, enabled,
				version_policy, receiver_id, webhook_verification_strategy, webhook_input_mappings, created_by_actor_type, updated_by_actor_type)
			SELECT ps.project_id, pe.id, ps.id, ps.slug, 'orders-webhook', 'Orders webhook', 'webhook', true,
				'latest_deployed', 'pause-receiver', 'hmac_sha256', '[]', 'system', 'system'
			FROM project_environments pe JOIN pipeline_specs ps ON ps.project_id = pe.project_id WHERE pe.id = $1
			RETURNING *
		), delivery AS (
			INSERT INTO pipeline_webhook_deliveries(organization_id, project_id, project_environment_id, pipeline_trigger_id,
				receiver_id, delivery_id, payload_digest, payload_size_bytes, key_version, trigger_revision, pipeline_slug,
				version_policy, run_slug, status, received_at, encrypted_envelope)
			SELECT pe.organization_id, t.project_id, pe.id, t.id, t.receiver_id, 'preflight', repeat('a',64), 1, 1, t.revision,
				t.pipeline_slug, 'latest_deployed', 'webhook-run', 'running', now(), 'fixture'
			FROM trigger t JOIN project_environments pe ON pe.id = t.project_environment_id
			RETURNING *
		)
		INSERT INTO pipeline_runs(project_id, project_environment_id, api_version, pipeline_slug, run_slug, source_environment_slug,
			status, duration_ms, step_slug, timeline, source_type, source_webhook_delivery_id)
		SELECT project_id, project_environment_id, 'v1', pipeline_slug, run_slug, 'prod', 'running', 0, '',
			'{"status":"running","events":[{"type":"run_queued"},{"type":"run_started"}]}'::jsonb, 'webhook', id FROM delivery`, env)
	require.NoError(t, err)

	webhooks := pipelinewebhooks.New()
	runs := pipelineruns.New()
	actor := model.SystemEventActor()
	changed, err := pipelineruns.SetExecutionControl(ctx, pool, scope, 0, true, actor)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, webhooks.ResetPreflight(ctx, pool, "pause-receiver", "preflight", 1))

	run, err := runs.FindRunBySlug(ctx, pool, scope.Org, scope.Project, scope.Environment, "webhook-run")
	require.NoError(t, err)
	require.Equal(t, "queued", run.Status)
	require.JSONEq(t, `{"status":"queued","events":[{"type":"run_queued"}]}`, string(run.Timeline))

	claim := func() error {
		return postgres.NewTransactionManager(pool).RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
			if err := webhooks.MarkRunning(ctx, db, "pause-receiver", "preflight", 1, time.Now().UTC()); err != nil {
				return err
			}
			_, err := runs.MarkRunRunning(ctx, db, scope.Org, scope.Project, scope.Environment, scope.Pipeline, "webhook-run", time.Now().UTC(), []byte(`{"status":"running"}`))
			return err
		})
	}
	require.ErrorIs(t, claim(), pipelineruns.ErrExecutionPaused)
	var state string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM pipeline_webhook_deliveries WHERE receiver_id='pause-receiver' AND delivery_id='preflight'`).Scan(&state))
	require.Equal(t, "accepted", state)
	changed, err = pipelineruns.SetExecutionControl(ctx, pool, scope, 1, false, actor)
	require.NoError(t, err)
	require.True(t, changed)
	require.NoError(t, claim())
	_, err = pool.Exec(ctx, `UPDATE pipeline_runs pr SET timeline = pr.timeline || '{"revision":1}'::jsonb,
		pipeline_version_id = seed.pipeline_version_id, pipeline_version_spec_hash = seed.pipeline_version_spec_hash
		FROM pipeline_runs seed WHERE seed.project_environment_id=$1 AND seed.run_slug='template'
			AND pr.project_environment_id=$1 AND pr.run_slug='webhook-run'`, env)
	require.NoError(t, err)
	require.ErrorIs(t, webhooks.ResetPreflight(ctx, pool, "pause-receiver", "preflight", 1), pipelinewebhooks.ErrDeliveryNotFound)
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM pipeline_webhook_deliveries WHERE receiver_id='pause-receiver' AND delivery_id='preflight'`).Scan(&state))
	require.Equal(t, "running", state)

	// Recreate the old split preflight state without a retained River job.
	_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET timeline = timeline - 'revision'
		WHERE project_environment_id=$1 AND run_slug='webhook-run'`, env)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE pipeline_webhook_deliveries SET status='accepted', decision_reason='execution_preflight_retry'
		WHERE receiver_id='pause-receiver' AND delivery_id='preflight'`)
	require.NoError(t, err)
	migration, err := os.ReadFile("../../migrations/000014_pipeline_execution_pause_indexes.up.sql")
	require.NoError(t, err)
	_, repair, found := strings.Cut(string(migration), "WITH preflight AS (")
	require.True(t, found)
	_, err = pool.Exec(ctx, "WITH preflight AS ("+repair)
	require.NoError(t, err)
	run, err = runs.FindRunBySlug(ctx, pool, scope.Org, scope.Project, scope.Environment, "webhook-run")
	require.NoError(t, err)
	require.Equal(t, "failed", run.Status)
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM pipeline_webhook_deliveries WHERE receiver_id='pause-receiver' AND delivery_id='preflight'`).Scan(&state))
	require.Equal(t, "failed", state)
}
