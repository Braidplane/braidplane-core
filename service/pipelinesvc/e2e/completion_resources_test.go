package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/postgres/retention"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/pipelinetriggersvc"
)

type completionPersistenceValidator struct{}

func (completionPersistenceValidator) ValidateCompletionExecutionContext(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot) error {
	return nil
}

func TestCompletionResourcesSnapshotsRevocationAndRetention(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL")
	}
	pool := openIntegrationPool(t)
	ctx := t.Context()
	suffix := uuid.NewString()
	org, project, environment := slug.Slug("completion-"+suffix), defaultProjectSlug, slug.Slug("local")
	env := seedCompletionSource(t, pool, org, project, environment, "upstream")
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, query, args...)
		require.NoError(t, err)
	}
	actorSlug := slug.Slug("completion-author-" + suffix)
	exec(`INSERT INTO users(slug,email,role,verified_at) VALUES($1,$2,'user',NOW())`, actorSlug, string(actorSlug)+"@example.test")
	t.Cleanup(func() {
		_, err := pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE slug=$1`, actorSlug)
		require.NoError(t, err)
	})
	exec(`INSERT INTO roles(organization_id,slug,name) SELECT id,'completion-owner','Completion owner' FROM organizations WHERE slug=$1`, org)
	exec(`INSERT INTO organization_members(organization_id,user_id,role_id) SELECT o.id,u.id,r.id FROM organizations o JOIN roles r ON r.organization_id=o.id CROSS JOIN users u WHERE o.slug=$1 AND u.slug=$2 AND r.slug='completion-owner'`, org, actorSlug)
	exec(`INSERT INTO permissions(resource,action) VALUES('*','*') ON CONFLICT DO NOTHING`)
	exec(`INSERT INTO role_permissions(role_id,permission_id) SELECT r.id,p.id FROM roles r JOIN organizations o ON o.id=r.organization_id CROSS JOIN permissions p WHERE o.slug=$1 AND r.slug='completion-owner' AND p.resource='*' AND p.action='*'`, org)
	exec(`INSERT INTO pipeline_specs(project_id,slug,name,spec_hash,spec_jsonb,source_path,source_ref,last_applied_commit) SELECT project_id,'downstream','Downstream','hash-v2','{}'::jsonb,'pipelines/downstream.json','refs/heads/main',repeat('0',40) FROM project_environments WHERE id=$1`, env)
	exec(`INSERT INTO pipeline_versions(project_id,pipeline_slug,spec_hash) SELECT project_id,'downstream','hash-v2' FROM project_environments WHERE id=$1`, env)
	// These bytes isolate SQL snapshot identity. Actual materialization validation
	// is covered with the real pipeline service in deployment_resolution_test.
	exec(`INSERT INTO pipeline_compiled_plans(project_id,pipeline_slug,digest,authored_spec_hash,format_version,materialization) SELECT project_id,'downstream','compiled-v2','hash-v2',1,'compiled'::bytea FROM project_environments WHERE id=$1`, env)
	exec(`INSERT INTO pipeline_deployment_resolutions(project_id,project_environment_id,pipeline_slug,compiled_digest,digest,materialization) SELECT project_id,id,'downstream','compiled-v2','resolution-v2','materialized-v2'::bytea FROM project_environments WHERE id=$1`, env)
	exec(`INSERT INTO pipeline_deployments(project_id,project_environment_id,pipeline_slug,current_version_id,current_resolution_digest,deployed_by_actor_type) SELECT pe.project_id,pe.id,'downstream',v.id,'resolution-v2','user' FROM project_environments pe JOIN pipeline_versions v ON v.project_id=pe.project_id AND v.pipeline_slug='downstream' AND v.spec_hash='hash-v2' WHERE pe.id=$1`, env)
	txm := postgres.NewTransactionManager(pool)
	svc := pipelinetriggersvc.New(true, txm, pipelinetriggers.NewRepository(), completionPersistenceValidator{}, eventtest.NewProducer(nil), nil)
	actor := model.UserEventActor(actorSlug)
	input := model.NewPipelineTrigger{
		Name: "After upstream", Slug: "after-upstream", Type: model.PipelineTriggerTypeCompletion, Enabled: true, PipelineSlug: "downstream",
		VersionPolicy:    model.PipelineTriggerVersionPolicy{Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed},
		RetryPolicy:      model.PipelineTriggerRetryPolicy{MaxAttempts: 1, BackoffStrategy: model.PipelineTriggerBackoffStrategyNone},
		CompletionConfig: &model.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: "upstream", Condition: model.PipelineCompletionConditionSuccess, ExecutionGrantEnabled: true},
	}
	trigger, err := svc.CreatePipelineTrigger(ctx, org, project, environment, input, actor)
	require.NoError(t, err)
	require.EqualValues(t, 1, trigger.Revision)
	require.Equal(t, input.CompletionConfig, trigger.CompletionConfig)
	point, err := svc.GetPipelineTrigger(ctx, org, project, environment, trigger.Slug)
	require.NoError(t, err)
	require.Equal(t, trigger.CompletionConfig, point.CompletionConfig)
	closeRoot := func(root string) int {
		t.Helper()
		exec(`INSERT INTO pipeline_runs(project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,source_type,source_root_run_slug,ended_at)
            SELECT project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,$2,source_environment_slug,'succeeded',0,'fetch','{}','manual',$2,NOW() FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='template'`, env, root)
		var id int
		require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM pipeline_completion_snapshots WHERE project_environment_id=$1 AND root_run_slug=$2 AND trigger_slug='after-upstream'`, env, root).Scan(&id))
		return id
	}
	t.Run("capacity preserves terminal obligations and recovers", func(t *testing.T) {
		for i := range 100 {
			closeRoot(fmt.Sprintf("capacity-%d", i))
		}
		var count int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_completion_capacity capacity JOIN organizations o ON o.id=capacity.organization_id WHERE o.slug=$1`, org).Scan(&count))
		require.Equal(t, 100, count)
		exec(`INSERT INTO pipeline_runs(project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,source_type,source_root_run_slug,ended_at)
            SELECT project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,'capacity-waiting',source_environment_slug,'succeeded',0,'fetch','{}','manual','capacity-waiting',NOW() FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='template'`, env)
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_run_terminal_obligations WHERE project_environment_id=$1 AND attempt_run_slug='capacity-waiting' AND resolved_at IS NULL`, env).Scan(&count))
		require.Equal(t, 1, count, "capacity pressure cannot roll back a terminal run or resolve its obligation")
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_run_logical_closures WHERE project_environment_id=$1 AND root_run_slug='capacity-waiting'`, env).Scan(&count))
		require.Zero(t, count)
		exec(`UPDATE pipeline_completion_snapshots SET state='refused',decision_reason='test_release',completed_at=NOW() WHERE project_environment_id=$1 AND root_run_slug='capacity-0'`, env)
		exec(`UPDATE pipeline_run_terminal_obligations SET next_evaluation_at=NOW() WHERE project_environment_id=$1 AND root_run_slug='capacity-waiting'`, env)
		require.NoError(t, pipelinetriggers.NewRepository().ReconcileLogicalRunFinalizationsForSystem(ctx, pool))
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_completion_snapshots WHERE project_environment_id=$1 AND root_run_slug='capacity-waiting' AND state='pending'`, env).Scan(&count))
		require.Equal(t, 1, count)
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_run_terminal_obligations WHERE project_environment_id=$1 AND attempt_run_slug='capacity-waiting' AND resolved_at IS NULL`, env).Scan(&count))
		require.Zero(t, count)
		exec(`UPDATE pipeline_completion_snapshots SET state='refused',decision_reason='test_release',completed_at=NOW() WHERE project_environment_id=$1 AND state='pending'`, env)
	})
	t.Run("renaming a pipeline preserves its pending dispatch identity", func(t *testing.T) {
		id := closeRoot("renamed-trigger-parent")
		exec(`UPDATE pipeline_specs SET name='Upstream renamed' WHERE project_id=(SELECT project_id FROM project_environments WHERE id=$1) AND slug='upstream'`, env)

		executor, producer := newCompletionTestExecutor(t, pool)
		dispatcher, err := pipelinetriggersvc.NewCompletionDispatcher(true, txm, pipelinetriggers.NewRepository(), svc, executor, producer)
		require.NoError(t, err)
		require.NoError(t, dispatcher.Dispatch(ctx))
		require.NoError(t, dispatcher.Dispatch(ctx))

		accepted, err := svc.CheckCompletionSnapshot(ctx, org, project, environment, id)
		require.NoError(t, err)
		require.Equal(t, trigger.Slug, accepted.TriggerSlug)
		require.EqualValues(t, 1, accepted.TriggerRevision)
		require.Equal(t, slug.Slug("renamed-trigger-parent"), accepted.RootRunSlug)
		require.Equal(t, slug.Slug("renamed-trigger-parent"), accepted.FinalAttemptRunSlug)
		require.Equal(t, slug.Slug(fmt.Sprintf("completion-%d", id)), accepted.ChildRunSlug)
		var snapshots, children int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*), count(child_run_slug) FROM pipeline_completion_snapshots WHERE project_environment_id=$1 AND root_run_slug='renamed-trigger-parent' AND trigger_slug='after-upstream'`, env).Scan(&snapshots, &children))
		require.Equal(t, 1, snapshots)
		require.Equal(t, 1, children)
	})
	t.Run("shared source without a completion trigger creates no child", func(t *testing.T) {
		exec(`INSERT INTO pipeline_specs(project_id,slug,name,spec_hash,spec_jsonb,source_path,source_ref,last_applied_commit)
            SELECT project_id,'shared-source','Shared source','shared-source-hash',spec_jsonb,'pipelines/shared-source.json',source_ref,last_applied_commit
            FROM pipeline_specs WHERE project_id=(SELECT project_id FROM project_environments WHERE id=$1) AND slug='upstream'`, env)
		exec(`INSERT INTO pipeline_versions(project_id,pipeline_slug,spec_hash)
            SELECT project_id,'shared-source','shared-source-hash' FROM project_environments WHERE id=$1`, env)
		exec(`INSERT INTO pipeline_runs(project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,source_type,source_root_run_slug,ended_at)
            SELECT pe.project_id,pe.id,pv.id,pv.spec_hash,'v1',ps.slug,'shared-source-root',$2,'succeeded',0,'fetch','{}','manual','shared-source-root',NOW()
			FROM project_environments pe JOIN pipeline_specs ps ON ps.project_id=pe.project_id AND ps.slug='shared-source' JOIN pipeline_versions pv ON pv.project_id=ps.project_id AND pv.pipeline_slug=ps.slug WHERE pe.id=$1`, env, environment)
		var closures, snapshots, children int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_run_logical_closures WHERE project_environment_id=$1 AND root_run_slug='shared-source-root'`, env).Scan(&closures))
		require.Equal(t, 1, closures)
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*), count(child_run_slug) FROM pipeline_completion_snapshots WHERE project_environment_id=$1 AND root_run_slug='shared-source-root'`, env).Scan(&snapshots, &children))
		require.Zero(t, snapshots)
		require.Zero(t, children)
	})
	t.Run("dispatch persistence", func(t *testing.T) {
		testCompletionDispatchPersistence(t, pool, org, project, environment, env, closeRoot("dispatch-parent"), svc)
	})
	t.Run("runtime admission", func(t *testing.T) {
		testCompletionDispatcherAdmission(t, pool, org, project, environment, env, closeRoot("runtime-parent"), closeRoot("dispatch-failed"), closeRoot("revoked-before-execution"), closeRoot("retired-before-execution"), svc)
	})
	t.Run("worker process crashes", func(t *testing.T) {
		testCompletionWorkerCrashes(t, pool, org, project, environment, env, actor, svc)
	})
	t.Run("acceptance serializes trigger edits", func(t *testing.T) {
		id := closeRoot("acceptance-wins")
		executor, producer := newCompletionTestExecutor(t, pool)
		entered, release := make(chan struct{}), make(chan struct{})
		dispatcher, err := pipelinetriggersvc.NewCompletionDispatcher(true, txm, pipelinetriggers.NewRepository(), svc, gatedCompletionAdmission{executor, entered, release}, producer)
		require.NoError(t, err)
		result := make(chan error, 1)
		go func() { result <- dispatcher.Dispatch(ctx) }()
		// Always release the transaction, including assertion failures.
		released := false
		defer func() {
			if !released {
				close(release)
			}
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("admission did not reach its transaction fence")
		}
		update := model.UpdatePipelineTrigger{Name: input.Name, Type: input.Type, Enabled: false, PipelineSlug: input.PipelineSlug, VersionPolicy: input.VersionPolicy, RetryPolicy: input.RetryPolicy, CompletionConfig: input.CompletionConfig}
		blocked, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		_, err = svc.UpdatePipelineTrigger(blocked, org, project, environment, trigger.Slug, update, actor)
		cancel()
		require.Error(t, err, "a trigger edit cannot pass an uncommitted acceptance")
		close(release)
		released = true
		require.NoError(t, <-result)
		_, err = svc.UpdatePipelineTrigger(ctx, org, project, environment, trigger.Slug, update, actor)
		require.NoError(t, err)
		accepted, err := svc.CheckCompletionSnapshot(ctx, org, project, environment, id)
		require.NoError(t, err)
		require.Equal(t, "accepted", accepted.State, "an edit after acceptance cannot cancel the accepted child")
		update.Enabled = true
		_, err = svc.UpdatePipelineTrigger(ctx, org, project, environment, trigger.Slug, update, actor)
		require.NoError(t, err)
	})
	t.Run("overlapping roots finalizing in reverse order retain exact parents", func(t *testing.T) {
		branch := input
		branch.Slug = "second-branch"
		created, err := svc.CreatePipelineTrigger(ctx, org, project, environment, branch, actor)
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, svc.DeletePipelineTrigger(context.WithoutCancel(ctx), org, project, environment, created.Slug, actor))
		})
		for _, root := range []string{"fanout-first", "fanout-second"} {
			exec(`INSERT INTO pipeline_runs(project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,source_type,source_root_run_slug)
                SELECT project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,$2,source_environment_slug,'running',0,'fetch','{}','manual',$2 FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='template'`, env, root)
		}
		// Close in reverse order, then suppress only the second branch before admission.
		for _, root := range []string{"fanout-second", "fanout-first"} {
			exec(`UPDATE pipeline_runs SET status='succeeded',ended_at=NOW() WHERE project_environment_id=$1 AND run_slug=$2`, env, root)
		}
		_, err = svc.UpdatePipelineTrigger(ctx, org, project, environment, created.Slug, model.UpdatePipelineTrigger{Name: branch.Name, Type: branch.Type, Enabled: false, PipelineSlug: branch.PipelineSlug, VersionPolicy: branch.VersionPolicy, RetryPolicy: branch.RetryPolicy, CompletionConfig: branch.CompletionConfig}, actor)
		require.NoError(t, err)
		executor, producer := newCompletionTestExecutor(t, pool)
		dispatcher, err := pipelinetriggersvc.NewCompletionDispatcher(true, txm, pipelinetriggers.NewRepository(), svc, executor, producer)
		require.NoError(t, err)
		require.NoError(t, dispatcher.Dispatch(ctx))
		children := map[string]bool{}
		for _, root := range []string{"fanout-first", "fanout-second"} {
			var id int
			var closureRoot, child, final, state, reason string
			require.NoError(t, pool.QueryRow(ctx, `SELECT root_run_slug,final_attempt_run_slug FROM pipeline_run_logical_closures WHERE project_environment_id=$1 AND root_run_slug=$2`, env, root).Scan(&closureRoot, &final))
			require.Equal(t, root, closureRoot)
			require.Equal(t, root, final)
			require.NoError(t, pool.QueryRow(ctx, `SELECT id,child_run_slug,final_attempt_run_slug FROM pipeline_completion_snapshots WHERE project_environment_id=$1 AND root_run_slug=$2 AND trigger_slug='after-upstream' AND state='accepted'`, env, root).Scan(&id, &child, &final))
			require.Equal(t, root, final)
			require.False(t, children[child], "distinct roots cannot share an initial child")
			children[child] = true
			require.NoError(t, pool.QueryRow(ctx, `SELECT state,decision_reason,COALESCE(child_run_slug,'') FROM pipeline_completion_snapshots WHERE project_environment_id=$1 AND root_run_slug=$2 AND trigger_slug='second-branch'`, env, root).Scan(&state, &reason, &child))
			require.Equal(t, "refused", state)
			require.Equal(t, "trigger_disabled", reason)
			require.Empty(t, child)
			accepted, err := svc.CheckCompletionSnapshot(ctx, org, project, environment, id)
			require.NoError(t, err)
			require.Equal(t, slug.Slug(root), accepted.RootRunSlug)
		}
	})
	t.Run("eight edge runtime guard", func(t *testing.T) {
		executor, producer := newCompletionTestExecutor(t, pool)
		dispatcher, err := pipelinetriggersvc.NewCompletionDispatcher(true, txm, pipelinetriggers.NewRepository(), svc, executor, producer)
		require.NoError(t, err)
		upstream := slug.Slug("downstream")
		for depth := 2; depth <= 9; depth++ {
			target := slug.Slug(fmt.Sprintf("hop-%d", depth))
			exec(`INSERT INTO pipeline_specs(project_id,slug,name,spec_hash,spec_jsonb,source_path,source_ref,last_applied_commit) SELECT project_id,$2,$2,'hash-v2','{}','pipelines/hop.json','refs/heads/main',repeat('0',40) FROM project_environments WHERE id=$1`, env, target)
			exec(`INSERT INTO pipeline_versions(project_id,pipeline_slug,spec_hash) SELECT project_id,$2,'hash-v2' FROM project_environments WHERE id=$1`, env, target)
			exec(`INSERT INTO pipeline_compiled_plans(project_id,pipeline_slug,digest,authored_spec_hash,format_version,materialization) SELECT project_id,$2,'compiled-v2','hash-v2',1,'compiled'::bytea FROM project_environments WHERE id=$1`, env, target)
			exec(`INSERT INTO pipeline_deployment_resolutions(project_id,project_environment_id,pipeline_slug,compiled_digest,digest,materialization) SELECT project_id,id,$2,'compiled-v2','resolution-v2','materialized-v2'::bytea FROM project_environments WHERE id=$1`, env, target)
			exec(`INSERT INTO pipeline_deployments(project_id,project_environment_id,pipeline_slug,current_version_id,current_resolution_digest,deployed_by_actor_type) SELECT pe.project_id,pe.id,$2,v.id,'resolution-v2','user' FROM project_environments pe JOIN pipeline_versions v ON v.project_id=pe.project_id AND v.pipeline_slug=$2 WHERE pe.id=$1`, env, target)
			chain := input
			chain.Slug, chain.PipelineSlug = target, target
			chain.CompletionConfig = &model.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: upstream, Condition: model.PipelineCompletionConditionSuccess, ExecutionGrantEnabled: true}
			created, err := svc.CreatePipelineTrigger(ctx, org, project, environment, chain, actor)
			require.NoError(t, err)
			t.Cleanup(func() {
				require.NoError(t, svc.DeletePipelineTrigger(context.WithoutCancel(ctx), org, project, environment, created.Slug, actor))
			})
			upstream = target
		}
		id := closeRoot("depth-root")
		root := "depth-root"
		for depth := 1; depth <= 9; depth++ {
			var state, reason, child string
			var actualDepth int
			require.NoError(t, pool.QueryRow(ctx, `SELECT id,state,COALESCE(decision_reason,''),causal_depth FROM pipeline_completion_snapshots WHERE project_environment_id=$1 AND root_run_slug=$2`, env, root).Scan(&id, &state, &reason, &actualDepth))
			require.Equal(t, depth, actualDepth)
			if depth == 9 {
				require.Equal(t, "refused", state)
				require.Equal(t, "causal_depth_exceeded", reason)
				break
			}
			require.NoError(t, dispatcher.Dispatch(ctx))
			require.NoError(t, pool.QueryRow(ctx, `SELECT child_run_slug FROM pipeline_completion_snapshots WHERE id=$1 AND state='accepted'`, id).Scan(&child))
			// SQL fixture materializations test admission and closure, not provider execution.
			exec(`UPDATE pipeline_runs SET status='succeeded',ended_at=NOW() WHERE project_environment_id=$1 AND run_slug=$2`, env, child)
			root = child
		}
	})
	selected := closeRoot("select-v2")
	waiting := closeRoot("waiting")
	accepted := closeRoot("accepted-root")
	exec(`UPDATE pipeline_completion_snapshots SET state='accepted',child_run_slug='accepted-child' WHERE id=$1`, accepted)
	exec(`INSERT INTO pipeline_versions(project_id,pipeline_slug,spec_hash) SELECT project_id,'downstream','hash-v3' FROM project_environments WHERE id=$1`, env)
	exec(`INSERT INTO pipeline_compiled_plans(project_id,pipeline_slug,digest,authored_spec_hash,format_version,materialization) SELECT project_id,'downstream','compiled-v3','hash-v3',1,'compiled-v3'::bytea FROM project_environments WHERE id=$1`, env)
	exec(`INSERT INTO pipeline_deployment_resolutions(project_id,project_environment_id,pipeline_slug,compiled_digest,digest,materialization) SELECT project_id,id,'downstream','compiled-v3','resolution-v3','materialized-v3'::bytea FROM project_environments WHERE id=$1`, env)
	exec(`UPDATE pipeline_deployments SET current_version_id=(SELECT id FROM pipeline_versions WHERE project_id=pipeline_deployments.project_id AND pipeline_slug='downstream' AND spec_hash='hash-v3'),current_resolution_digest='resolution-v3',revision=revision+1 WHERE project_environment_id=$1 AND pipeline_slug='downstream'`, env)
	snapshot, err := svc.CheckCompletionSnapshot(ctx, org, project, environment, selected)
	require.NoError(t, err)
	require.Equal(t, "hash-v2", snapshot.VersionHash)
	require.Equal(t, "resolution-v2", snapshot.ResolutionDigest)
	require.Equal(t, []byte("materialized-v2"), snapshot.Materialization)
	require.EqualValues(t, 1, snapshot.DeploymentRevision)
	inspected, err := pipelineruns.New().ListCompletionDecisions(ctx, pool, org, project, environment, "select-v2")
	require.NoError(t, err)
	require.Len(t, inspected, 1)
	require.Equal(t, "hash-v2", *inspected[0].SelectedVersionHash)
	require.Equal(t, "resolution-v2", *inspected[0].SelectedResolutionDigest)
	require.Equal(t, int64(1), *inspected[0].SelectedDeploymentRevision)

	exec(`UPDATE pipeline_completion_snapshots SET state='completed',completed_at=NOW() WHERE id=$1`, selected)
	_, err = svc.CheckCompletionSnapshot(ctx, "other-organization", project, environment, selected)
	require.Error(t, err)
	_, err = svc.CheckCompletionSnapshot(ctx, org, "other-project", environment, selected)
	require.Error(t, err)
	_, err = svc.CheckCompletionSnapshot(ctx, org, project, "other-environment", selected)
	require.Error(t, err)
	owner, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = pipelinetriggers.NewRepository().LoadCompletionSnapshot(ctx, owner, org, project, environment, accepted)
	require.NoError(t, err)
	revoker, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = revoker.Exec(ctx, `SET LOCAL lock_timeout='100ms'`)
	require.NoError(t, err)
	_, err = revoker.Exec(ctx, `DELETE FROM role_permissions WHERE role_id=(SELECT r.id FROM roles r JOIN organizations o ON o.id=r.organization_id WHERE o.slug=$1 AND r.slug='completion-owner')`, org)
	require.ErrorContains(t, err, "lock timeout", "permission revocation must serialize with admission/execution authority")
	require.NoError(t, revoker.Rollback(ctx))
	require.NoError(t, owner.Rollback(ctx))
	exec(`DELETE FROM pipeline_deployments WHERE project_environment_id=$1 AND pipeline_slug='downstream'`, env)
	missingDeployment := closeRoot("missing-deployment")
	exec(`INSERT INTO pipeline_deployments(project_id,project_environment_id,pipeline_slug,current_version_id,current_resolution_digest,deployed_by_actor_type) SELECT pe.project_id,pe.id,'downstream',v.id,'resolution-v3','user' FROM project_environments pe JOIN pipeline_versions v ON v.project_id=pe.project_id AND v.pipeline_slug='downstream' AND v.spec_hash='hash-v3' WHERE pe.id=$1`, env)
	refused, err := svc.CheckCompletionSnapshot(ctx, org, project, environment, missingDeployment)
	require.ErrorContains(t, err, "deployment_missing")
	require.Equal(t, "refused", refused.State, "a later deployment cannot revive a closure refusal")
	missingEvidence, err := pipelineruns.New().ListCompletionDecisions(ctx, pool, org, project, environment, "missing-deployment")
	require.NoError(t, err)
	require.Len(t, missingEvidence, 1)
	require.Equal(t, "deployment_missing", *missingEvidence[0].DecisionReason)
	require.Nil(t, missingEvidence[0].ChildRunSlug)
	require.Nil(t, missingEvidence[0].SelectedVersionHash)
	require.Nil(t, missingEvidence[0].SelectedDeploymentRevision)

	exec(`UPDATE environments SET protected=true WHERE id=(SELECT environment_id FROM project_environments WHERE id=$1)`, env)
	protectedRoot := closeRoot("protected-target")
	exec(`UPDATE environments SET protected=false WHERE id=(SELECT environment_id FROM project_environments WHERE id=$1)`, env)
	_, err = svc.CheckCompletionSnapshot(ctx, org, project, environment, protectedRoot)
	require.ErrorContains(t, err, "protected_environment")
	// Permission restoration must never supply authority missing at closure.
	exec(`DELETE FROM role_permissions WHERE role_id=(SELECT r.id FROM roles r JOIN organizations o ON o.id=r.organization_id WHERE o.slug=$1 AND r.slug='completion-owner')`, org)
	noAuthority := closeRoot("no-authority")
	exec(`INSERT INTO role_permissions(role_id,permission_id) SELECT r.id,p.id FROM roles r JOIN organizations o ON o.id=r.organization_id CROSS JOIN permissions p WHERE o.slug=$1 AND r.slug='completion-owner' AND p.resource='*' AND p.action='*'`, org)
	_, err = svc.CheckCompletionSnapshot(ctx, org, project, environment, noAuthority)
	require.ErrorContains(t, err, "authority_revoked")
	currentAuthority := closeRoot("current-authority")
	exec(`UPDATE users SET deleted_at=NOW() WHERE slug=$1`, actorSlug)
	deletedActor := closeRoot("deleted-actor")
	_, err = svc.CheckCompletionSnapshot(ctx, org, project, environment, currentAuthority)
	require.ErrorContains(t, err, "authority_revoked", "soft deletion revokes current execution authority")
	exec(`UPDATE users SET deleted_at=NULL WHERE slug=$1`, actorSlug)
	_, err = svc.CheckCompletionSnapshot(ctx, org, project, environment, deletedActor)
	require.ErrorContains(t, err, "authority_revoked", "restoration cannot replace authority missing at closure")
	update := model.UpdatePipelineTrigger{Name: input.Name, Type: input.Type, Enabled: input.Enabled, PipelineSlug: input.PipelineSlug, VersionPolicy: input.VersionPolicy, RetryPolicy: input.RetryPolicy, CompletionConfig: input.CompletionConfig}
	update.CompletionConfig.ExecutionGrantEnabled = false
	_, err = svc.UpdatePipelineTrigger(ctx, org, project, environment, trigger.Slug, update, actor)
	require.NoError(t, err)
	var state, reason string
	require.NoError(t, pool.QueryRow(ctx, `SELECT state,decision_reason FROM pipeline_completion_snapshots WHERE id=$1`, waiting).Scan(&state, &reason))
	require.Equal(t, "refused", state)
	require.Equal(t, "grant_revoked", reason)
	_, err = svc.CheckCompletionSnapshot(ctx, org, project, environment, accepted)
	require.ErrorContains(t, err, "selected_deployment_missing", "the original deployment was deleted; trigger edits alone cannot revoke an accepted child")
	require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM pipeline_completion_snapshots WHERE id=$1`, accepted).Scan(&state))
	require.Equal(t, "accepted", state, "revocation cannot cancel an accepted child")
	_, err = pool.Exec(ctx, `UPDATE pipeline_completion_trigger_revisions SET configuration='{}' WHERE project_environment_id=$1`, env)
	require.ErrorContains(t, err, "immutable")
	_, err = pool.Exec(ctx, `UPDATE pipeline_completion_snapshots SET selected_version_hash='replacement' WHERE id=$1`, selected)
	require.ErrorContains(t, err, "immutable")
	require.NoError(t, retention.New().PruneCompletionDetailsBefore(ctx, pool, org, time.Now().Add(time.Hour)))
	var detailed, tombstones int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_completion_snapshots WHERE id=$1`, selected).Scan(&detailed))
	require.Zero(t, detailed)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_completion_tombstones tombstone JOIN pipeline_run_logical_closures closure ON closure.id=tombstone.closure_id WHERE closure.project_environment_id=$1 AND closure.root_run_slug='select-v2'`, env).Scan(&tombstones))
	require.Equal(t, 1, tombstones)
	exec(`INSERT INTO pipeline_completion_snapshots(organization_id,project_environment_id,closure_id,trigger_id,trigger_revision,trigger_slug,root_run_slug,final_attempt_run_slug,downstream_pipeline_slug,causal_depth,state)
        SELECT tombstone.organization_id,closure.project_environment_id,closure.id,tombstone.trigger_id,tombstone.trigger_revision,'after-upstream','select-v2',closure.final_attempt_run_slug,'downstream',1,'pending'
        FROM pipeline_completion_tombstones tombstone JOIN pipeline_run_logical_closures closure ON closure.id=tombstone.closure_id WHERE closure.project_environment_id=$1 AND closure.root_run_slug='select-v2'`, env)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_completion_snapshots WHERE project_environment_id=$1 AND root_run_slug='select-v2'`, env).Scan(&detailed))
	require.Zero(t, detailed, "redelivery cannot recreate detail or pending work after retention")
	var detail []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT trigger_configuration FROM pipeline_completion_snapshots WHERE id=$1`, accepted).Scan(&detail))
	require.NotEmpty(t, detail, "unresolved accepted work survives retention")
	_, err = retention.New().DeletePipelineRunsBefore(ctx, pool, org, time.Now().Add(time.Hour))
	require.NoError(t, err)
	var retained int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='accepted-root'`, env).Scan(&retained))
	require.Equal(t, 1, retained)
	require.NoError(t, svc.DeletePipelineTrigger(ctx, org, project, environment, trigger.Slug, actor))
	input.Enabled = false
	input.CompletionConfig.ExecutionGrantEnabled = true
	_, err = svc.CreatePipelineTrigger(ctx, org, project, environment, input, actor)
	require.NoError(t, err, "a recreated slug has a distinct resource identity")
}

func TestCompletionClosureDoesNotDeadlockConcurrentRunInserts(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL")
	}
	pool := openIntegrationPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	org := slug.Slug("closure-lock-" + uuid.NewString())
	env := seedCompletionSource(t, pool, org, defaultProjectSlug, "local", "upstream")
	start := make(chan struct{})
	results := make(chan error, 2)
	// Run inserts hold foreign-key key-share locks before closing their roots.
	for i := 0; i < 2; i++ {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
		_, err = tx.Exec(ctx, `SET LOCAL lock_timeout='5s'`)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `SELECT id FROM project_environments WHERE id=$1 FOR KEY SHARE`, env)
		require.NoError(t, err)
		go func(root string) {
			<-start
			_, err := tx.Exec(ctx, `INSERT INTO pipeline_runs(project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,source_type,source_root_run_slug,ended_at)
                SELECT project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,$2,source_environment_slug,'succeeded',0,'fetch','{}','manual',$2,NOW() FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='template'`, env, root)
			if err == nil {
				err = tx.Commit(ctx)
			}
			results <- err
		}(fmt.Sprintf("concurrent-root-%d", i))
	}
	close(start)
	first, second := <-results, <-results
	require.NoError(t, first)
	require.NoError(t, second)
}

func TestCompletionConcurrentTopologyEditsRejectDisabledCycle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL")
	}
	pool := openIntegrationPool(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	org := slug.Slug("graph-" + uuid.NewString())
	env := seedCompletionSource(t, pool, org, defaultProjectSlug, "local", "a")
	_, err := pool.Exec(ctx, `INSERT INTO pipeline_specs(project_id,slug,name,spec_hash,spec_jsonb,source_path,source_ref,last_applied_commit) SELECT project_id,'b','B','hash-b','{}'::jsonb,'pipelines/b.json','refs/heads/main',repeat('0',40) FROM project_environments WHERE id=$1`, env)
	require.NoError(t, err)
	svc := pipelinetriggersvc.New(true, postgres.NewTransactionManager(pool), pipelinetriggers.NewRepository(), completionPersistenceValidator{}, eventtest.NewProducer(nil), nil)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			from, to := slug.Slug("a"), slug.Slug("b")
			if i == 1 {
				from, to = to, from
			}
			_, err := svc.CreatePipelineTrigger(ctx, org, defaultProjectSlug, "local", model.NewPipelineTrigger{
				Name: fmt.Sprintf("Edge %d", i), Slug: slug.Slug(fmt.Sprintf("edge-%d", i)), Type: model.PipelineTriggerTypeCompletion, PipelineSlug: to,
				VersionPolicy: model.PipelineTriggerVersionPolicy{Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed}, RetryPolicy: model.PipelineTriggerRetryPolicy{MaxAttempts: 1, BackoffStrategy: model.PipelineTriggerBackoffStrategyNone},
				CompletionConfig: &model.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: from, Condition: model.PipelineCompletionConditionSuccess},
			}, model.SystemEventActor())
			results <- err
		}(i)
	}
	close(start)
	first, second := <-results, <-results
	require.NotEqual(t, first == nil, second == nil, "exactly one concurrent edge must commit")
	if first != nil {
		require.ErrorContains(t, first, "cycle")
	}
	if second != nil {
		require.ErrorContains(t, second, "cycle")
	}
}

func seedCompletionSource(t *testing.T, pool *pgxpool.Pool, org, project, environment, pipeline slug.Slug) int64 {
	t.Helper()
	ctx := t.Context()
	seedPipelineDeploymentOrg(t, pool, org)
	seedPipelineDeploymentEnvironment(t, pool, org, project, environment)
	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelines.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	materializePipelines(t, ctx, store, org, project, pipelineDeploymentIntegrationPipeline(pipeline, "Completion source"))
	var env int64
	err = pool.QueryRow(ctx, `SELECT pe.id FROM project_environments pe JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id JOIN environments e ON e.id=pe.environment_id WHERE o.slug=$1 AND p.slug=$2 AND e.slug=$3`, org, project, environment).Scan(&env)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO pipeline_runs(project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,ended_at)
        SELECT pe.project_id,pe.id,pv.id,pv.spec_hash,'v1',pv.pipeline_slug,'template',$2,'succeeded',0,'fetch','{}',NOW()
        FROM project_environments pe JOIN pipeline_specs ps ON ps.project_id=pe.project_id AND ps.slug=$3 JOIN pipeline_versions pv ON pv.id=ps.current_version_id WHERE pe.id=$1`, env, environment, pipeline)
	require.NoError(t, err)
	return env
}
