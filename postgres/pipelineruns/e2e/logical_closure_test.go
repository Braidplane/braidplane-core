package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/postgres/retention"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestLogicalClosure_TerminalPersistenceAndOrigins(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL")
	}
	pool := openPipelineRunsE2EPool(t)
	ctx := context.Background()
	suffix := uuid.NewString()
	env := seedPipelineRunSourceEventRun(t, pool, must.Slug("closure-"+suffix), must.Slug("project-"+suffix), must.Slug("local"), must.Slug("pipeline"), must.Slug("template"), time.Now().UTC(), "succeeded")
	insert := func(db interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	}, name, source, status, mode string, replay *string, roots ...string,
	) error {
		root := name
		if len(roots) > 0 {
			root = roots[0]
		}
		attempt := 1
		var retryOf *string
		if len(roots) > 0 {
			attempt = 2
			retryOf = &root
		}
		_, err := db.Exec(ctx, `INSERT INTO pipeline_runs (project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,source_type,source_root_run_slug,run_mode,replay_of_run_slug,ended_at,source_attempt_number,source_retry_of_run_slug)
   SELECT project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,$2,source_environment_slug,$3,0,'fetch','{}',$4,$7,$5,$6,NOW(),$8,$9 FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='template'`, env, name, status, source, mode, replay, root, attempt, retryOf)
		return err
	}
	count := func(table, name string) int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE project_environment_id=$1 AND root_run_slug=$2", table), env, name).Scan(&n))
		return n
	}
	for _, tc := range []struct {
		name, source, status, mode string
		replay                     *string
		closed                     bool
	}{
		{"success", "schedule", "succeeded", "full", nil, true},
		{"manual-failure", "manual", "failed", "full", nil, true},
		{"awaiting-decision", "schedule", "failed", "full", nil, false},
		{"backfill", "manual", "succeeded", "backfill", nil, false},
		{"cancel", "manual", "canceled", "full", nil, false},
		{"replay", "manual", "succeeded", "full", ptrString("template"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, insert(pool, tc.name, tc.source, tc.status, tc.mode, tc.replay))
			require.Equal(t, 1, count("pipeline_run_terminal_obligations", tc.name))
			expected := 0
			if tc.closed {
				expected = 1
			}
			require.Equal(t, expected, count("pipeline_run_logical_closures", tc.name))
		})
	}

	require.NoError(t, insert(pool, "uncertain", "manual", "running", "full", nil))
	_, uncertainErr := pool.Exec(ctx, `UPDATE pipeline_runs SET status='failed',timeline='{"failure":{"outcome_uncertain":true}}' WHERE project_environment_id=$1 AND run_slug='uncertain'`, env)
	require.NoError(t, uncertainErr)
	require.Zero(t, count("pipeline_run_logical_closures", "uncertain"))
	var trigger int64
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO pipeline_triggers(project_id,project_environment_id,pipeline_id,pipeline_slug,slug,name,trigger_type,version_policy,schedule_cron,schedule_timezone,schedule_misfire_policy,created_by_actor_type,updated_by_actor_type)
 SELECT r.project_id,r.project_environment_id,ps.id,r.pipeline_slug,'closure-trigger','Closure trigger','schedule','latest_deployed','* * * * *','UTC','skip','system','system'
 FROM pipeline_runs r JOIN pipeline_specs ps ON ps.project_id=r.project_id AND ps.slug=r.pipeline_slug WHERE r.project_environment_id=$1 AND r.run_slug='template' RETURNING id`, env).Scan(&trigger))
	decision := func(root, status string) error {
		_, err := pool.Exec(ctx, `INSERT INTO pipeline_trigger_retry_attempts(project_environment_id,pipeline_trigger_id,attempt_slug,status,trigger_revision,pipeline_slug,version_policy,pipeline_version_spec_hash,source_type,source_summary,source_entrypoint,source_actor_type,root_run_slug,retry_of_run_slug,run_slug,attempt_number,scheduled_at,decision_reason)
  SELECT $1,$2,$3,$4,1,pipeline_slug,'latest_deployed',pipeline_version_spec_hash,'schedule','Scheduled run','scheduler','system',$5,$5,$3,2,NOW(),'test-explicit-decision' FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='template'`, env, trigger, root+"-retry-2", status, root)
		return err
	}
	require.NoError(t, decision("awaiting-decision", "queued"))
	require.Zero(t, count("pipeline_run_logical_closures", "awaiting-decision"))
	require.NoError(t, insert(pool, "retry-success", "schedule", "succeeded", "full", ptrString("awaiting-decision"), "awaiting-decision"))
	require.Equal(t, 1, count("pipeline_run_logical_closures", "awaiting-decision"))
	var final string
	require.NoError(t, pool.QueryRow(ctx, `SELECT final_attempt_run_slug FROM pipeline_run_logical_closures WHERE project_environment_id=$1 AND root_run_slug='awaiting-decision'`, env).Scan(&final))
	require.Equal(t, "retry-success", final)
	for _, status := range []string{"skipped", "exhausted"} {
		name := "failure-" + status
		require.NoError(t, insert(pool, name, "schedule", "failed", "full", nil))
		require.NoError(t, decision(name, status))
		require.Equal(t, 1, count("pipeline_run_logical_closures", name))
	}

	// Reconciliation can repair the explicit-decision/terminal-write ordering
	// gap, while a genuinely missing decision must remain open.
	require.NoError(t, decision("late-obligation", "exhausted"))
	require.NoError(t, insert(pool, "late-obligation", "schedule", "failed", "full", nil))
	require.NoError(t, insert(pool, "missing-decision", "schedule", "failed", "full", nil))
	require.NoError(t, pipelinetriggers.NewRepository().ReconcileLogicalRunFinalizationsForSystem(ctx, pool))
	require.Equal(t, 1, count("pipeline_run_logical_closures", "late-obligation"))
	require.Zero(t, count("pipeline_run_logical_closures", "missing-decision"))

	_, pendingErr := pool.Exec(ctx, `INSERT INTO pipeline_schedule_fires(project_environment_id,pipeline_trigger_id,fire_slug,trigger_revision,pipeline_slug,version_policy,schedule_cron,schedule_timezone,schedule_misfire_policy,scheduled_fire_at,actual_fire_at,status,misfire_result)
 SELECT project_environment_id,id,'missing-decision',revision,pipeline_slug,version_policy,schedule_cron,schedule_timezone,schedule_misfire_policy,NOW(),NOW(),'running','on_time' FROM pipeline_triggers WHERE id=$1`, trigger)
	require.NoError(t, pendingErr)
	pending, pendingErr := pipelinetriggers.NewRepository().ListPendingRunFinalizationsForSystem(ctx, pool)
	require.NoError(t, pendingErr)
	found := false
	for _, item := range pending {
		if item.OrgSlug.String() == "closure-"+suffix {
			found = true
			require.Equal(t, "closure-trigger", item.TriggerSlug.String())
			require.Empty(t, item.RetryAttemptSlug)
		}
	}
	require.True(t, found, "terminal source owner must remain repairable independently of River retention")
	_, pendingErr = pool.Exec(ctx, `UPDATE project_environments SET deleted_at=NOW() WHERE id=$1`, env)
	require.NoError(t, pendingErr)
	pending, pendingErr = pipelinetriggers.NewRepository().ListPendingRunFinalizationsForSystem(ctx, pool)
	require.NoError(t, pendingErr)
	for _, item := range pending {
		require.NotEqual(t, "closure-"+suffix, item.OrgSlug.String(), "unloadable deleted scopes must not occupy the bounded repair batch")
	}
	_, pendingErr = pool.Exec(ctx, `UPDATE project_environments SET deleted_at=NULL WHERE id=$1`, env)
	require.NoError(t, pendingErr)
	var delivery int64
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO pipeline_webhook_deliveries(organization_id,project_id,project_environment_id,pipeline_trigger_id,receiver_id,delivery_id,payload_digest,payload_size_bytes,key_version,trigger_revision,pipeline_slug,version_policy,run_slug,attempt_number,status,received_at)
 SELECT pe.organization_id,r.project_id,$1,$2,'receiver-'||$3,'delivery',repeat('a',64),0,1,1,r.pipeline_slug,'latest_deployed','webhook-final',2,'running',NOW() FROM pipeline_runs r JOIN project_environments pe ON pe.id=r.project_environment_id WHERE r.project_environment_id=$1 AND r.run_slug='template' RETURNING id`, env, trigger, suffix).Scan(&delivery))
	webhookRoot := "webhook-final"
	insertWebhook := func(name string, attempt int) error {
		_, err := pool.Exec(ctx, `INSERT INTO pipeline_runs(project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,source_type,source_root_run_slug,source_attempt_number,source_webhook_delivery_id,ended_at)
 SELECT project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,$2,source_environment_slug,'failed',0,'fetch','{}','webhook',$5,$3,$4,NOW() FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='template'`, env, name, attempt, delivery, webhookRoot)
		return err
	}
	require.NoError(t, insertWebhook("webhook-final-retry-2", 2))
	// Insert the older attempt last to catch accidental latest-row selection.
	require.NoError(t, insertWebhook("webhook-final", 1))
	_, err := pool.Exec(ctx, `UPDATE pipeline_webhook_deliveries SET status='failed',decision_reason='max_attempts_exhausted' WHERE id=$1`, delivery)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, `SELECT final_attempt_run_slug FROM pipeline_run_logical_closures WHERE project_environment_id=$1 AND root_run_slug='webhook-final'`, env).Scan(&final))
	require.Equal(t, "webhook-final-retry-2", final)
	webhookRoot = "webhook-expired"
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO pipeline_webhook_deliveries(organization_id,project_id,project_environment_id,pipeline_trigger_id,receiver_id,delivery_id,payload_digest,payload_size_bytes,key_version,trigger_revision,pipeline_slug,version_policy,run_slug,attempt_number,status,received_at)
 SELECT pe.organization_id,r.project_id,$1,$2,'expiry-'||$3,'delivery',repeat('a',64),0,1,1,r.pipeline_slug,'latest_deployed','webhook-expired',2,'accepted',NOW() FROM pipeline_runs r JOIN project_environments pe ON pe.id=r.project_environment_id WHERE r.project_environment_id=$1 AND r.run_slug='template' RETURNING id`, env, trigger, suffix).Scan(&delivery))
	require.NoError(t, insertWebhook("webhook-expired", 1))
	_, err = pool.Exec(ctx, `UPDATE pipeline_webhook_deliveries SET status='failed',decision_reason='execution_envelope_expired' WHERE id=$1`, delivery)
	require.NoError(t, err)
	require.NoError(t, pool.QueryRow(ctx, `SELECT final_attempt_run_slug FROM pipeline_run_logical_closures WHERE project_environment_id=$1 AND root_run_slug='webhook-expired'`, env).Scan(&final))
	require.Equal(t, "webhook-expired", final)
	var unresolved int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_run_terminal_obligations WHERE project_environment_id=$1 AND root_run_slug='awaiting-decision' AND resolved_at IS NULL`, env).Scan(&unresolved))
	require.Zero(t, unresolved)

	require.ErrorContains(t, decision("uncertain", "queued"), "uncertain run outcome")
	require.NoError(t, decision("uncertain", "exhausted"))
	require.Zero(t, count("pipeline_run_logical_closures", "uncertain"))
	require.ErrorContains(t, decision("success", "queued"), "closed logical run")
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, insert(tx, "rollback", "manual", "succeeded", "full", nil))
	require.NoError(t, tx.Rollback(ctx))
	require.Zero(t, count("pipeline_run_terminal_obligations", "rollback"))
	require.Zero(t, count("pipeline_run_logical_closures", "rollback"))
	_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET status='failed' WHERE project_environment_id=$1 AND run_slug='success'`, env)
	require.ErrorContains(t, err, "immutable")
	require.Equal(t, 1, count("pipeline_run_logical_closures", "success"))

	// A separate connection cannot persist a terminal fact while another owner
	// holds the root fence; rollback releases it without a partial closure.
	owner, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = owner.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("%d:blocked", env))
	require.NoError(t, err)
	waiter, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = waiter.Exec(ctx, `SET LOCAL lock_timeout='100ms'`)
	require.NoError(t, err)
	require.Error(t, insert(waiter, "blocked", "manual", "succeeded", "full", nil))
	require.NoError(t, waiter.Rollback(ctx))
	require.NoError(t, owner.Rollback(ctx))
	require.Zero(t, count("pipeline_run_logical_closures", "blocked"))
	require.NoError(t, insert(pool, "blocked", "manual", "succeeded", "full", nil))
	require.Equal(t, 1, count("pipeline_run_logical_closures", "blocked"))
	_, err = retention.New().DeletePipelineRunsBefore(ctx, pool, must.Slug("closure-"+suffix), time.Now().UTC().Add(time.Hour))
	require.NoError(t, err)
	var retained int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='missing-decision'`, env).Scan(&retained))
	require.Equal(t, 1, retained)
	require.Equal(t, 1, count("pipeline_run_logical_closures", "success"))
}

func ptrString(s string) *string { return &s }
