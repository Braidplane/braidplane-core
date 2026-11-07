package e2e

import (
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCompletionInspectionPersistenceAndIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx := t.Context()
	pool := openPipelineRunsE2EPool(t)
	suffix := uuid.NewString()
	org, project, environment := slug.Slug("inspect-"+suffix), slug.Slug("project-"+suffix), slug.Slug("local")
	env := seedPipelineRunSourceEventRun(t, pool, org, project, environment, "upstream", "template", time.Now().UTC(), "succeeded")
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, query, args...)
		require.NoError(t, err)
	}
	// A disabled dependency still has a persisted refusal at closure, without a child.
	exec(`INSERT INTO pipeline_triggers(project_id,project_environment_id,pipeline_id,pipeline_slug,slug,name,trigger_type,version_policy,created_by_actor_type,updated_by_actor_type,completion_config)
 SELECT pe.project_id,pe.id,ps.id,ps.slug,'after-upstream','After upstream','completion','latest_deployed','system','system',
 '{"upstream_pipeline_slug":"upstream","condition":"success","execution_grant_enabled":false}' FROM project_environments pe JOIN pipeline_specs ps ON ps.project_id=pe.project_id AND ps.slug='upstream' WHERE pe.id=$1`, env)
	exec(`INSERT INTO pipeline_runs(project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,source_type,source_root_run_slug,ended_at)
 SELECT project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,'closed-root',source_environment_slug,'succeeded',0,'fetch','{}','manual','closed-root',NOW() FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='template'`, env)
	repo := pipelineruns.New()
	evidence, err := repo.InspectLogicalRun(ctx, pool, org, project, environment, "closed-root")
	require.NoError(t, err)
	require.True(t, evidence.Closed)
	require.False(t, evidence.WaitingForRetry)
	require.Equal(t, "succeeded", *evidence.Outcome)
	require.Equal(t, slug.Slug("closed-root"), *evidence.FinalAttemptRunSlug)
	require.Len(t, evidence.Attempts, 1)
	decisions, err := repo.ListCompletionDecisions(ctx, pool, org, project, environment, "closed-root")
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.False(t, decisions[0].IsParent)
	require.Nil(t, decisions[0].ChildRunSlug)
	require.Nil(t, decisions[0].ChildStatus)
	require.Nil(t, decisions[0].SelectedVersionHash)
	require.Equal(t, "trigger_disabled", *decisions[0].DecisionReason)
	require.Equal(t, int64(1), decisions[0].TriggerRevision)
	exec(`UPDATE pipeline_triggers SET revision=revision+1,completion_config=jsonb_set(completion_config,'{condition}','"final_failure"') WHERE project_environment_id=$1`, env)
	historical, err := repo.ListCompletionDecisions(ctx, pool, org, project, environment, "closed-root")
	require.NoError(t, err)
	require.Equal(t, int64(1), historical[0].TriggerRevision)
	require.Equal(t, "success", historical[0].Condition.String())

	exec(`INSERT INTO pipeline_runs(project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,source_type,source_root_run_slug,ended_at)
 SELECT project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,'retry-root',source_environment_slug,'failed',0,'fetch','{}','schedule','retry-root',NOW() FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='template'`, env)
	exec(`INSERT INTO pipeline_trigger_retry_attempts(project_environment_id,pipeline_trigger_id,attempt_slug,status,trigger_revision,pipeline_slug,version_policy,pipeline_version_spec_hash,source_type,source_summary,source_entrypoint,source_actor_type,root_run_slug,retry_of_run_slug,run_slug,attempt_number,scheduled_at,decision_reason)
 SELECT pe.id,pt.id,'retry-root-attempt-2','queued',pt.revision,pr.pipeline_slug,'latest_deployed',pr.pipeline_version_spec_hash,'schedule','Scheduled run','scheduler','system','retry-root','retry-root','retry-root-2',2,NOW(),'retry_scheduled'
 FROM project_environments pe JOIN pipeline_triggers pt ON pt.project_environment_id=pe.id AND pt.slug='after-upstream' JOIN pipeline_runs pr ON pr.project_environment_id=pe.id AND pr.run_slug='template' WHERE pe.id=$1`, env)
	waiting, err := repo.InspectLogicalRun(ctx, pool, org, project, environment, "retry-root")
	require.NoError(t, err)
	require.True(t, waiting.WaitingForRetry)
	require.False(t, waiting.Closed)
	require.Nil(t, waiting.FinalAttemptRunSlug)
	exec(`INSERT INTO pipeline_runs(project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,source_type,source_root_run_slug,source_attempt_number,source_retry_of_run_slug,ended_at)
 SELECT project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,'retry-root-2',source_environment_slug,'succeeded',0,'fetch','{}','schedule','retry-root',2,'retry-root',NOW() FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='template'`, env)
	succeeded, err := repo.InspectLogicalRun(ctx, pool, org, project, environment, "retry-root-2")
	require.NoError(t, err)
	require.True(t, succeeded.Closed)
	require.False(t, succeeded.WaitingForRetry)
	require.Equal(t, slug.Slug("retry-root"), succeeded.RootRunSlug)
	require.Equal(t, slug.Slug("retry-root-2"), *succeeded.FinalAttemptRunSlug)
	require.Len(t, succeeded.Attempts, 2)
	require.Equal(t, "failed", succeeded.Attempts[0].Status)
	require.Equal(t, "succeeded", succeeded.Attempts[1].Status)
	exec(`DELETE FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='retry-root'`, env)
	retained, err := repo.InspectLogicalRun(ctx, pool, org, project, environment, "retry-root-2")
	require.NoError(t, err)
	require.Equal(t, slug.Slug("retry-root"), retained.RootRunSlug)
	require.Len(t, retained.Attempts, 1)
	require.Equal(t, slug.Slug("retry-root-2"), retained.Attempts[0].RunSlug)
	require.True(t, retained.Closed)

	for _, scope := range [][3]slug.Slug{{"other-org", project, environment}, {org, "other-project", environment}, {org, project, "other-environment"}} {
		_, err := repo.InspectLogicalRun(ctx, pool, scope[0], scope[1], scope[2], "closed-root")
		require.ErrorIs(t, err, pipelineruns.ErrRunNotFound)
		decisions, err := repo.ListCompletionDecisions(ctx, pool, scope[0], scope[1], scope[2], "closed-root")
		require.NoError(t, err)
		require.Empty(t, decisions)
	}
}

func TestCompletionInspectionBoundsRetainedDecisions(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL")
	}
	pool := openPipelineRunsE2EPool(t)
	org := slug.Slug("bounded-" + uuid.NewString())
	env := seedPipelineRunSourceEventRun(t, pool, org, "etl", "local", "upstream", "root", time.Now().UTC(), "succeeded")
	_, err := pool.Exec(t.Context(), `INSERT INTO pipeline_completion_snapshots(organization_id,project_environment_id,closure_id,trigger_id,trigger_revision,trigger_slug,root_run_slug,final_attempt_run_slug,downstream_pipeline_slug,causal_depth,state,decision_reason,completed_at,trigger_configuration)
 SELECT c.organization_id,c.project_environment_id,c.id,100000+n,1,'trigger-'||n,'root','root','downstream',1,'refused','trigger_disabled',NOW(),'{"completion_config":{"condition":"success"}}'::jsonb
 FROM pipeline_run_logical_closures c CROSS JOIN generate_series(1,105) n WHERE c.project_environment_id=$1 AND c.root_run_slug='root'`, env)
	require.NoError(t, err)
	repo := pipelineruns.New()
	evidence, err := repo.InspectLogicalRun(t.Context(), pool, org, "etl", "local", "root")
	require.NoError(t, err)
	require.Equal(t, slug.Slug("upstream"), evidence.PipelineSlug)
	decisions, err := repo.ListCompletionDecisions(t.Context(), pool, org, "etl", "local", "root")
	require.NoError(t, err)
	require.Len(t, decisions, 101, "one probe row lets the service report truncation")
	require.Equal(t, "trigger-1", decisions[0].TriggerSlug.String())
	require.Nil(t, decisions[0].ChildRunSlug)
	// Retained snapshots still mark unavailable context after the parent attempt expires.
	_, err = pool.Exec(t.Context(), `DELETE FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='root'`, env)
	require.NoError(t, err)
	// No selected run remains: inspection must fail rather than inventing a replacement.
	_, err = repo.InspectLogicalRun(t.Context(), pool, org, "etl", "local", "root")
	require.ErrorIs(t, err, pipelineruns.ErrRunNotFound)
}
