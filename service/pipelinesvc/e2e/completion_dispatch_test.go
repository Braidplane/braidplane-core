package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"
	"github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/pipelinetriggersvc"
	"github.com/Braidplane/braidplane-core/service/worker/consumer"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	"github.com/Braidplane/braidplane-core/service/worker/producer"
)

type failCompletionAdmission struct {
	pipelinetriggersvc.CompletionRunExecutor
	id int
}

type denyCompletionRetryCapacity struct {
	pipelinetriggersvc.CompletionRunExecutor
}

type staleCompletionFinalization struct {
	pipelinetriggers.Repository
	ref pipelinetriggers.CompletionDispatchRef
}

func (r staleCompletionFinalization) ListCompletionRunFinalizationsForSystem(context.Context, postgres.DBTX) ([]pipelinetriggers.CompletionDispatchRef, error) {
	return []pipelinetriggers.CompletionDispatchRef{r.ref}, nil
}

type gatedCompletionAdmission struct {
	pipelinetriggersvc.CompletionRunExecutor
	entered chan struct{}
	release <-chan struct{}
}

func (e gatedCompletionAdmission) AcceptCompletionRun(ctx context.Context, org, project, environment slug.Slug, snapshot *model.PipelineCompletionSnapshot) (jobs.PipelineRunExecutionArgs, error) {
	args, err := e.CompletionRunExecutor.AcceptCompletionRun(ctx, org, project, environment, snapshot)
	if err != nil {
		return args, err
	}
	close(e.entered)
	select {
	case <-e.release:
		return args, nil
	case <-ctx.Done():
		return args, ctx.Err()
	}
}

func (denyCompletionRetryCapacity) AcceptCompletionRetryRun(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot, slug.Slug, pipelinerun.RunSourceSummary, time.Time) (jobs.PipelineRunExecutionArgs, error) {
	return jobs.PipelineRunExecutionArgs{}, admissionsvc.ErrAdmissionDenied
}

func (f failCompletionAdmission) AcceptCompletionRun(ctx context.Context, org, project, environment slug.Slug, snapshot *model.PipelineCompletionSnapshot) (jobs.PipelineRunExecutionArgs, error) {
	args, err := f.CompletionRunExecutor.AcceptCompletionRun(ctx, org, project, environment, snapshot)
	if err == nil && snapshot.ID == f.id {
		return args, xerror.InvalidInput(xerror.PipelineRun).Detail("injected failure after enqueue")
	}
	return args, err
}

func newCompletionTestExecutor(t *testing.T, pool *pgxpool.Pool) (pipelinesvc.Service, producer.PipelineRunProducer) {
	t.Helper()
	txm := postgres.NewTransactionManager(pool)
	client, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	producer, err := producer.NewPipelineRunProducer(queue.NewEnqueuer(client), pipelineruns.New())
	require.NoError(t, err)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelines.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	executor, err := pipelinesvc.New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		store, pool, txm, pipelineruns.New(), incrementalcheckpoints.New(),
		pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()), driftimpact.New(),
		noopQueryExecutor{}, allowAuthorizationService{}, &fakes.AdmissionService{}, &fakes.DataSourceService{},
		&fakes.ProjectsService{}, &ownershipGitSourceResolver{}, clock.New(), producer, noopPipelineGitProducer{},
		eventtest.NewProducer(nil), noopPipelineRuntimeResolver{}, testSinkEffectLedger(t, txm), newTestProofLinks(), testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)
	return executor, producer
}

func testCompletionDispatcherAdmission(t *testing.T, pool *pgxpool.Pool, org, project, environment slug.Slug, env int64, snapshotID, failureID, revokedID, retiredID int, checker pipelinetriggersvc.Service) {
	t.Helper()
	ctx := t.Context()
	txm := postgres.NewTransactionManager(pool)
	executor, producer := newCompletionTestExecutor(t, pool)
	repo := pipelinetriggers.NewRepository()
	disabled, err := pipelinetriggersvc.NewCompletionDispatcher(false, txm, repo, checker, executor, producer)
	require.NoError(t, err)
	require.NoError(t, disabled.Dispatch(ctx))
	var child string
	require.NoError(t, pool.QueryRow(ctx, `SELECT COALESCE(child_run_slug,'') FROM pipeline_completion_snapshots WHERE id=$1`, snapshotID).Scan(&child))
	require.Empty(t, child)
	dispatcher, err := pipelinetriggersvc.NewCompletionDispatcher(true, txm, repo, checker, executor, producer)
	require.NoError(t, err)
	failing, err := pipelinetriggersvc.NewCompletionDispatcher(true, txm, repo, checker, failCompletionAdmission{CompletionRunExecutor: executor, id: failureID}, producer)
	require.NoError(t, err)
	require.ErrorContains(t, failing.Dispatch(ctx), "injected failure after enqueue")
	var failureState, failureReason string
	require.NoError(t, pool.QueryRow(ctx, `SELECT state,decision_reason,COALESCE(child_run_slug,'') FROM pipeline_completion_snapshots WHERE id=$1`, failureID).Scan(&failureState, &failureReason, &child))
	require.Equal(t, "refused", failureState)
	require.Equal(t, "dispatch_failed", failureReason)
	require.Empty(t, child, "a failed dispatch must not invent a child")
	var failedRuns, failedJobs int
	failureChild := fmt.Sprintf("completion-%d", failureID)
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug=$2`, env, failureChild).Scan(&failedRuns))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE args->>'organization_slug'=$1 AND args->>'run_slug'=$2`, org, failureChild).Scan(&failedJobs))
	require.Zero(t, failedRuns)
	require.Zero(t, failedJobs)
	require.NoError(t, dispatcher.Dispatch(ctx))
	require.NoError(t, dispatcher.Dispatch(ctx))
	require.NoError(t, pool.QueryRow(ctx, `SELECT child_run_slug FROM pipeline_completion_snapshots WHERE id=$1 AND state='accepted'`, snapshotID).Scan(&child))
	require.Equal(t, fmt.Sprintf("completion-%d", snapshotID), child)
	var raw []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT execution_args FROM pipeline_completion_snapshots WHERE id=$1`, snapshotID).Scan(&raw))
	var args jobs.PipelineRunExecutionArgs
	require.NoError(t, json.Unmarshal(raw, &args))
	require.Equal(t, snapshotID, args.CompletionSnapshotID)
	require.Equal(t, model.PipelineRunSourceTypeCompletion, args.Source.Type)
	require.Equal(t, slug.Slug(child), args.Source.RootRunSlug)
	require.Equal(t, "Completion-triggered run", args.Source.Summary)
	causal, err := checker.CheckCompletionSnapshot(ctx, org, project, environment, snapshotID)
	require.NoError(t, err)
	require.Equal(t, slug.Slug("runtime-parent"), causal.RootRunSlug)
	require.Equal(t, slug.Slug("runtime-parent"), causal.FinalAttemptRunSlug)
	require.Equal(t, args.RunSlug, causal.ChildRunSlug)
	var count int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='pipeline_run_execution' AND args->>'organization_slug'=$1 AND args->>'run_slug'=$2`, org, child).Scan(&count))
	require.Equal(t, 1, count)
	var snooze *river.JobSnoozeError
	require.ErrorAs(t, disabled.ExecuteQueuedPipelineRun(ctx, args), &snooze)
	require.ErrorContains(t, executor.ExecuteQueuedPipelineRun(ctx, args), "authority revalidation")
	tampered := args
	tampered.PipelineVersionHash = "replacement"
	require.ErrorContains(t, dispatcher.ExecuteQueuedPipelineRun(ctx, tampered), "does not match its accepted snapshot")
	var status, sourceType string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug=$2`, env, child).Scan(&status))
	require.Equal(t, "queued", status, "a substituted job must not claim the accepted run")
	var revokedRaw []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT execution_args FROM pipeline_completion_snapshots WHERE id=$1`, revokedID).Scan(&revokedRaw))
	var revokedArgs jobs.PipelineRunExecutionArgs
	require.NoError(t, json.Unmarshal(revokedRaw, &revokedArgs))
	_, err = pool.Exec(ctx, `DELETE FROM role_permissions WHERE role_id=(SELECT r.id FROM roles r JOIN organizations o ON o.id=r.organization_id WHERE o.slug=$1 AND r.slug='completion-owner')`, org)
	require.NoError(t, err)
	require.NoError(t, dispatcher.ExecuteQueuedPipelineRun(ctx, revokedArgs))
	var revokedStatus, revokedReason string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status,error_summary FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug=$2`, env, revokedArgs.RunSlug).Scan(&revokedStatus, &revokedReason))
	require.Equal(t, "failed", revokedStatus)
	require.Contains(t, revokedReason, "authority_revoked")
	require.NoError(t, dispatcher.ExecuteQueuedPipelineRun(ctx, revokedArgs), "revoked redelivery cannot overwrite a terminal child")
	_, err = pool.Exec(ctx, `INSERT INTO role_permissions(role_id,permission_id) SELECT r.id,p.id FROM roles r JOIN organizations o ON o.id=r.organization_id CROSS JOIN permissions p WHERE o.slug=$1 AND r.slug='completion-owner' AND p.resource='*' AND p.action='*'`, org)
	require.NoError(t, err)
	// Synthetic SQL snapshot bytes pass the persistence-only checker; the real
	// executor must record their preflight refusal as an accepted child failure.
	require.NoError(t, dispatcher.ExecuteQueuedPipelineRun(ctx, args))
	require.NoError(t, pool.QueryRow(ctx, `SELECT status,source_type FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug=$2`, env, child).Scan(&status, &sourceType))
	require.Equal(t, "failed", status)
	require.Equal(t, "completion", sourceType)
	require.NoError(t, dispatcher.ExecuteQueuedPipelineRun(ctx, args), "redelivery cannot execute an already terminal child")
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='runtime-parent'`, env).Scan(&status))
	require.Equal(t, "succeeded", status)
	var retiredRaw []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT execution_args FROM pipeline_completion_snapshots WHERE id=$1`, retiredID).Scan(&retiredRaw))
	var retired jobs.PipelineRunExecutionArgs
	require.NoError(t, json.Unmarshal(retiredRaw, &retired))
	_, err = pool.Exec(ctx, `UPDATE project_environments SET deleted_at=NOW() WHERE id=$1`, env)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE pipeline_specs SET deleted_at=NOW() WHERE project_id=(SELECT project_id FROM project_environments WHERE id=$1) AND slug='downstream'`, env)
	require.NoError(t, err)
	defer func() {
		_, err := pool.Exec(context.WithoutCancel(ctx), `UPDATE project_environments SET deleted_at=NULL WHERE id=$1`, env)
		require.NoError(t, err)
		_, err = pool.Exec(context.WithoutCancel(ctx), `UPDATE pipeline_specs SET deleted_at=NULL WHERE project_id=(SELECT project_id FROM project_environments WHERE id=$1) AND slug='downstream'`, env)
		require.NoError(t, err)
	}()
	require.NoError(t, dispatcher.ExecuteQueuedPipelineRun(ctx, retired))
	retiredRun, err := executor.GetRun(ctx, org, project, environment, retired.RunSlug.String())
	require.NoError(t, err, "retirement cannot hide an accepted child's terminal evidence")
	require.Equal(t, "failed", retiredRun.Metadata.Status)
	require.Contains(t, *retiredRun.Metadata.ErrorSummary, "environment_missing")
	require.NoError(t, dispatcher.Dispatch(ctx))
	require.NoError(t, dispatcher.Dispatch(ctx))
	var retiredState string
	require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM pipeline_completion_snapshots WHERE id=$1`, retiredID).Scan(&retiredState))
	require.Equal(t, "completed", retiredState)
}

func testSuccessfulCompletionRuntime(ctx context.Context, t *testing.T, pool *pgxpool.Pool, executor pipelinesvc.Service, runProducer producer.PipelineRunProducer, org, project, environment slug.Slug, resolution string, calls func() int, failNext func()) {
	t.Helper()
	txm := postgres.NewTransactionManager(pool)
	repo := pipelinetriggers.NewRepository()
	checker := pipelinetriggersvc.New(true, txm, repo, executor, eventtest.NewProducer(nil), nil)
	actorSlug := slug.Slug(org.String() + "-completion-owner")
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, query, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO users(slug,email,role,verified_at) VALUES($1,$2,'user',NOW())`, actorSlug, actorSlug.String()+"@example.test")
	t.Cleanup(func() {
		_, err := pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE slug=$1`, actorSlug)
		require.NoError(t, err)
	})
	exec(`INSERT INTO roles(organization_id,slug,name) SELECT id,'completion-owner','Completion owner' FROM organizations WHERE slug=$1`, org)
	exec(`INSERT INTO organization_members(organization_id,user_id,role_id) SELECT o.id,u.id,r.id FROM organizations o JOIN roles r ON r.organization_id=o.id CROSS JOIN users u WHERE o.slug=$1 AND u.slug=$2 AND r.slug='completion-owner'`, org, actorSlug)
	exec(`INSERT INTO permissions(resource,action) VALUES('*','*') ON CONFLICT DO NOTHING`)
	exec(`INSERT INTO role_permissions(role_id,permission_id) SELECT r.id,p.id FROM roles r JOIN organizations o ON o.id=r.organization_id CROSS JOIN permissions p WHERE o.slug=$1 AND r.slug='completion-owner' AND p.resource='*' AND p.action='*'`, org)
	input := model.NewPipelineTrigger{
		Name: "Runtime completion", Slug: "runtime-follow", Type: model.PipelineTriggerTypeCompletion, Enabled: true, PipelineSlug: "sync",
		VersionPolicy:    model.PipelineTriggerVersionPolicy{Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed},
		RetryPolicy:      model.PipelineTriggerRetryPolicy{MaxAttempts: 1, BackoffStrategy: model.PipelineTriggerBackoffStrategyNone},
		CompletionConfig: &model.PipelineTriggerCompletionConfig{UpstreamPipelineSlug: "upstream", Condition: model.PipelineCompletionConditionSuccess, ExecutionGrantEnabled: true},
	}
	trigger, err := checker.CreatePipelineTrigger(ctx, org, project, environment, input, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	closeUpstream := func(root string) {
		exec(`INSERT INTO pipeline_runs(project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,source_type,source_root_run_slug,ended_at)
        SELECT p.id,pe.id,v.id,v.spec_hash,'v1','upstream',$4::text,$3::text,'succeeded',0,'fetch','{}','manual',$4::text,NOW()
        FROM organizations o JOIN projects p ON p.organization_id=o.id JOIN project_environments pe ON pe.project_id=p.id
        JOIN environments e ON e.id=pe.environment_id JOIN pipeline_specs ps ON ps.project_id=p.id AND ps.slug='upstream'
        JOIN pipeline_versions v ON v.id=ps.current_version_id WHERE o.slug=$1 AND p.slug=$2 AND e.slug=$3::text`, org, project, environment, root)
	}
	closeUpstream("completion-upstream")
	closeUpstream("claimed-upstream")
	dispatcher, err := pipelinetriggersvc.NewCompletionDispatcher(true, txm, repo, checker, executor, runProducer)
	require.NoError(t, err)
	require.NoError(t, dispatcher.Dispatch(ctx))
	var raw []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT snapshot.execution_args FROM pipeline_completion_snapshots snapshot JOIN organizations o ON o.id=snapshot.organization_id WHERE o.slug=$1 AND snapshot.root_run_slug='completion-upstream'`, org).Scan(&raw))
	var args jobs.PipelineRunExecutionArgs
	require.NoError(t, json.Unmarshal(raw, &args))
	require.Equal(t, resolution, *args.DeploymentResolutionDigest)
	var claimedRaw []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT snapshot.execution_args FROM pipeline_completion_snapshots snapshot JOIN organizations o ON o.id=snapshot.organization_id WHERE o.slug=$1 AND snapshot.root_run_slug='claimed-upstream'`, org).Scan(&claimedRaw))
	var claimed jobs.PipelineRunExecutionArgs
	require.NoError(t, json.Unmarshal(claimedRaw, &claimed))
	require.NoError(t, txm.RunInTx(postgres.WithConsistentSnapshot(ctx), func(ctx context.Context, db postgres.DBTX) error {
		if _, err := checker.CheckCompletionSnapshot(ctx, org, project, environment, claimed.CompletionSnapshotID); err != nil {
			return err
		}
		_, err := pipelineruns.New().MarkRunRunning(ctx, db, org, project, environment, claimed.PipelineSlug, claimed.RunSlug.String(), time.Now().UTC(), []byte(`{}`))
		return err
	}))
	exec(`DELETE FROM river_job WHERE args->>'organization_slug'=$1 AND args->>'run_slug'=$2`, org, claimed.RunSlug)
	beforeClaimedRedelivery := calls()
	require.NoError(t, dispatcher.ExecuteQueuedPipelineRun(ctx, claimed))
	require.Equal(t, beforeClaimedRedelivery, calls(), "a lost worker lease cannot reclaim a running run")
	require.NoError(t, dispatcher.Dispatch(ctx))
	var claimedJobs int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE args->>'organization_slug'=$1 AND args->>'run_slug'=$2`, org, claimed.RunSlug).Scan(&claimedJobs))
	require.Zero(t, claimedJobs, "repair must leave ambiguous running work intact")
	require.NoError(t, checker.DeletePipelineTrigger(ctx, org, project, environment, trigger.Slug, model.UserEventActor(actorSlug)))
	exec(`DELETE FROM river_job WHERE args->>'organization_slug'=$1 AND args->>'run_slug'=$2`, org, args.RunSlug)
	require.NoError(t, dispatcher.Dispatch(ctx), "accepted queued children recover their saved job even after trigger deletion")
	runWorker := func() {
		t.Helper()
		worker := &completionRuntimeWorker{dispatcher: dispatcher, completed: make(chan error, 1)}
		host, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
			Queues: map[string]river.QueueConfig{"completion-" + org.String(): {MaxWorkers: 1}}, Registrars: []queue.WorkerRegistrar{worker},
		})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, host.Stop(context.WithoutCancel(ctx))) })
		require.NoError(t, host.Start(ctx))
		select {
		case err := <-worker.completed:
			require.NoError(t, err, "accepted children survive trigger deletion")
		// Killed worker hosts leave the River leader lease until expiry.
		case <-time.After(45 * time.Second):
			t.Fatal("completion River worker did not finish the saved child")
		}
		require.NoError(t, host.Stop(context.WithoutCancel(ctx)))
	}
	before := calls()
	runWorker()
	require.Equal(t, before+2, calls(), "both saved operations execute")
	run, err := executor.GetRun(ctx, org, project, environment, args.RunSlug.String())
	require.NoError(t, err)
	require.Equal(t, "succeeded", run.Metadata.Status)
	require.Equal(t, args.PipelineVersionHash, run.Metadata.PipelineVersionSpecHash)
	require.Equal(t, resolution, run.Timeline.DeploymentResolutionDigest)
	require.Equal(t, model.PipelineRunSourceTypeCompletion, run.Metadata.Source.Type)
	require.NoError(t, dispatcher.Dispatch(ctx))
	var state string
	require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM pipeline_completion_snapshots WHERE id=$1`, args.CompletionSnapshotID).Scan(&state))
	require.Equal(t, "completed", state)
	require.NoError(t, dispatcher.ExecuteQueuedPipelineRun(ctx, args))
	require.Equal(t, before+2, calls(), "redelivery after closure cannot repeat provider operations")

	input.RetryPolicy = model.PipelineTriggerRetryPolicy{
		Enabled: true, MaxAttempts: 3,
		BackoffStrategy:       model.PipelineTriggerBackoffStrategyNone,
		RetryableFailureKinds: []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindDataSource, model.PipelineTriggerRetryFailureKindPlatform},
	}
	trigger, err = checker.CreatePipelineTrigger(ctx, org, project, environment, input, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	closeUpstream("retry-upstream")
	require.NoError(t, dispatcher.Dispatch(ctx))
	require.NoError(t, pool.QueryRow(ctx, `SELECT snapshot.execution_args FROM pipeline_completion_snapshots snapshot JOIN organizations o ON o.id=snapshot.organization_id WHERE o.slug=$1 AND snapshot.root_run_slug='retry-upstream'`, org).Scan(&raw))
	var initial jobs.PipelineRunExecutionArgs
	require.NoError(t, json.Unmarshal(raw, &initial))
	require.NoError(t, checker.DeletePipelineTrigger(ctx, org, project, environment, trigger.Slug, model.UserEventActor(actorSlug)))
	before = calls()
	failNext()
	runWorker()
	failed, err := executor.GetRun(ctx, org, project, environment, initial.RunSlug.String())
	require.NoError(t, err)
	require.Equal(t, "failed", failed.Metadata.Status)
	blocked, err := pipelinetriggersvc.NewCompletionDispatcher(true, txm, repo, checker, denyCompletionRetryCapacity{CompletionRunExecutor: executor}, runProducer)
	require.NoError(t, err)
	require.NoError(t, blocked.Dispatch(ctx))
	var capacityReason string
	require.NoError(t, pool.QueryRow(ctx, `SELECT obligation.finalization_reason FROM pipeline_run_terminal_obligations obligation JOIN organizations o ON o.id=obligation.organization_id WHERE o.slug=$1 AND obligation.attempt_run_slug=$2 AND obligation.resolved_at IS NULL`, org, initial.RunSlug).Scan(&capacityReason))
	require.Equal(t, "capacity_waiting", capacityReason)
	var pendingDecisions int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_trigger_retry_attempts retry JOIN project_environments pe ON pe.id=retry.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND retry.root_run_slug=$2`, org, initial.RunSlug).Scan(&pendingDecisions))
	require.Zero(t, pendingDecisions, "capacity refusal must preserve finalization without accepting a retry")
	exec(`UPDATE pipeline_run_terminal_obligations obligation SET next_evaluation_at=NOW() FROM organizations o WHERE o.id=obligation.organization_id AND o.slug=$1 AND obligation.attempt_run_slug=$2`, org, initial.RunSlug)
	dispatcher, err = pipelinetriggersvc.NewCompletionDispatcher(true, txm, repo, checker, executor, runProducer)
	require.NoError(t, err, "a fresh dispatcher must recover without in-memory retry state")
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- dispatcher.Dispatch(ctx) }()
	}
	for range 2 {
		if err := <-results; err != nil {
			var serialization *pgconn.PgError
			require.ErrorAs(t, err, &serialization)
			require.Equal(t, "40001", serialization.Code)
		}
	}
	stale, err := pipelinetriggersvc.NewCompletionDispatcher(true, txm, staleCompletionFinalization{
		Repository: repo,
		ref: pipelinetriggers.CompletionDispatchRef{
			ID: initial.CompletionSnapshotID, OrgSlug: org, ProjectSlug: project,
			EnvironmentSlug: environment, FailedRunSlug: initial.RunSlug,
		},
	}, checker, executor, runProducer)
	require.NoError(t, err)
	require.NoError(t, stale.Dispatch(ctx), "a candidate listed before another dispatcher committed must not accept the retry again")
	var retryRaw []byte
	require.NoError(t, pool.QueryRow(ctx, `SELECT retry.completion_execution_args FROM pipeline_trigger_retry_attempts retry JOIN project_environments pe ON pe.id=retry.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND retry.root_run_slug=$2 AND retry.attempt_number=2`, org, initial.RunSlug).Scan(&retryRaw))
	var retry jobs.PipelineRunExecutionArgs
	require.NoError(t, json.Unmarshal(retryRaw, &retry))
	require.Equal(t, initial.CompletionSnapshotID, retry.CompletionSnapshotID)
	require.Equal(t, initial.RunSlug, retry.Source.RootRunSlug)
	require.Equal(t, &initial.RunSlug, retry.Source.RetryOfRunSlug)
	require.Equal(t, initial.PipelineVersionHash, retry.PipelineVersionHash)
	require.Equal(t, initial.DeploymentResolutionDigest, retry.DeploymentResolutionDigest)
	var acceptedRetries int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_runs run JOIN project_environments pe ON pe.id=run.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND run.source_root_run_slug=$2 AND run.source_attempt_number=2`, org, initial.RunSlug).Scan(&acceptedRetries))
	require.Equal(t, 1, acceptedRetries, "concurrent policy finalization accepts one retry")
	_, err = pool.Exec(ctx, `UPDATE pipeline_trigger_retry_attempts retry SET completion_execution_args='{}' FROM project_environments pe JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE retry.project_environment_id=pe.id AND o.slug=$1 AND retry.run_slug=$2 AND retry.source_type='completion'`, org, retry.RunSlug)
	require.ErrorContains(t, err, "immutable")
	tamperedRetry := retry
	tamperedRetry.Source.RootRunSlug = "another-root"
	require.ErrorContains(t, dispatcher.ExecuteQueuedPipelineRun(ctx, tamperedRetry), "does not match its accepted snapshot")
	var closed int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_run_logical_closures closure JOIN organizations o ON o.id=closure.organization_id WHERE o.slug=$1 AND closure.root_run_slug=$2`, org, initial.RunSlug).Scan(&closed))
	require.Zero(t, closed, "a failed child cannot close while its automatic retry is pending")
	exec(`DELETE FROM river_job WHERE args->>'organization_slug'=$1 AND args->>'run_slug'=$2`, org, retry.RunSlug)
	require.NoError(t, dispatcher.Dispatch(ctx))
	var retryJobID int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM river_job WHERE args->>'organization_slug'=$1 AND args->>'run_slug'=$2`, org, retry.RunSlug).Scan(&retryJobID))
	retryStarted := make(chan struct{})
	releaseRetry := make(chan struct{})
	retryWorker := &completionRuntimeWorker{dispatcher: dispatcher, completed: make(chan error, 1), entered: retryStarted, release: releaseRetry}
	retryHost, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Queues: map[string]river.QueueConfig{"completion-" + org.String(): {MaxWorkers: 1}}, Registrars: []queue.WorkerRegistrar{retryWorker},
	})
	require.NoError(t, err)
	require.NoError(t, retryHost.Start(ctx))
	stopDone := make(chan error, 1)
	t.Cleanup(func() {
		select {
		case <-releaseRetry:
		default:
			close(releaseRetry)
		}
		select {
		case err := <-stopDone:
			require.NoError(t, err)
		default:
			stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer stopCancel()
			require.NoError(t, retryHost.Stop(stopCtx))
		}
	})
	select {
	case <-retryStarted:
	// Prior hosts can leave the shared River leader lease until expiry.
	case <-time.After(45 * time.Second):
		var state, queueName string
		var scheduledAt time.Time
		require.NoError(t, pool.QueryRow(ctx, `SELECT state::text, queue, scheduled_at FROM river_job WHERE id=$1`, retryJobID).Scan(&state, &queueName, &scheduledAt))
		t.Fatalf("retry River job %d did not start: state=%s queue=%s scheduled_at=%s", retryJobID, state, queueName, scheduledAt)
	}
	go func() { stopDone <- retryHost.Stop(context.WithoutCancel(ctx)) }()
	select {
	case err := <-stopDone:
		t.Fatalf("graceful retry River stop returned before retry dispatch: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(releaseRetry)
	select {
	case err := <-stopDone:
		require.NoError(t, err)
	case <-time.After(8 * time.Second):
		t.Fatal("graceful retry River stop did not finish after retry dispatch")
	}
	select {
	case err := <-retryWorker.completed:
		require.NoError(t, err)
	case <-time.After(8 * time.Second):
		t.Fatal("admitted retry did not complete")
	}
	var retryJobState string
	var retryJobAttempt int
	require.NoError(t, pool.QueryRow(ctx, `SELECT state::text, attempt::int FROM river_job WHERE id=$1`, retryJobID).Scan(&retryJobState, &retryJobAttempt))
	require.Equal(t, "completed", retryJobState)
	require.Equal(t, 1, retryJobAttempt)
	succeeded, err := executor.GetRun(ctx, org, project, environment, retry.RunSlug.String())
	require.NoError(t, err)
	require.Equal(t, "succeeded", succeeded.Metadata.Status)
	require.Equal(t, initial.RunSlug, succeeded.Metadata.Source.RootRunSlug)
	require.Equal(t, 2, succeeded.Metadata.Source.AttemptNumber)
	require.Equal(t, resolution, succeeded.Timeline.DeploymentResolutionDigest)
	require.Equal(t, before+3, calls(), "one failed operation and two pinned retry operations execute")
	require.NoError(t, dispatcher.Dispatch(ctx))
	var finalAttempt string
	require.NoError(t, pool.QueryRow(ctx, `SELECT closure.final_attempt_run_slug FROM pipeline_run_logical_closures closure JOIN organizations o ON o.id=closure.organization_id WHERE o.slug=$1 AND closure.root_run_slug=$2`, org, initial.RunSlug).Scan(&finalAttempt))
	require.Equal(t, retry.RunSlug.String(), finalAttempt)
	require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM pipeline_completion_snapshots WHERE id=$1`, initial.CompletionSnapshotID).Scan(&state))
	require.Equal(t, "completed", state)
	diagnostics, err := repo.ListTriggerRetryAttemptsForRun(ctx, pool, org, project, environment, initial.RunSlug)
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusSucceeded, diagnostics[0].Status)
	require.Equal(t, input.Slug, diagnostics[0].TriggerSlug, "retry history survives trigger deletion")

	trigger, err = checker.CreatePipelineTrigger(ctx, org, project, environment, input, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	closeUpstream("exhausted-upstream")
	require.NoError(t, dispatcher.Dispatch(ctx))
	require.NoError(t, pool.QueryRow(ctx, `SELECT snapshot.execution_args FROM pipeline_completion_snapshots snapshot JOIN organizations o ON o.id=snapshot.organization_id WHERE o.slug=$1 AND snapshot.root_run_slug='exhausted-upstream'`, org).Scan(&raw))
	var exhausted jobs.PipelineRunExecutionArgs
	require.NoError(t, json.Unmarshal(raw, &exhausted))
	_, err = checker.UpdatePipelineTrigger(ctx, org, project, environment, trigger.Slug, model.UpdatePipelineTrigger{
		Name: input.Name, Type: input.Type, Enabled: false, PipelineSlug: input.PipelineSlug, VersionPolicy: input.VersionPolicy,
		CompletionConfig: input.CompletionConfig, RetryPolicy: model.PipelineTriggerRetryPolicy{MaxAttempts: 1, BackoffStrategy: model.PipelineTriggerBackoffStrategyNone},
	}, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	closeUpstream("disabled-upstream")
	var disabledState, disabledReason, disabledChild string
	require.NoError(t, pool.QueryRow(ctx, `SELECT snapshot.state,snapshot.decision_reason,COALESCE(snapshot.child_run_slug,'') FROM pipeline_completion_snapshots snapshot JOIN organizations o ON o.id=snapshot.organization_id WHERE o.slug=$1 AND snapshot.root_run_slug='disabled-upstream'`, org).Scan(&disabledState, &disabledReason, &disabledChild))
	require.Equal(t, "refused", disabledState)
	require.Equal(t, "trigger_disabled", disabledReason)
	require.Empty(t, disabledChild)
	before = calls()
	for range 3 {
		failNext()
		runWorker()
		require.NoError(t, dispatcher.Dispatch(ctx))
	}
	require.NoError(t, dispatcher.Dispatch(ctx))
	require.Equal(t, before+3, calls(), "the captured three-attempt policy survives disable/edit")
	var exhaustedStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT closure.status,closure.final_attempt_run_slug FROM pipeline_run_logical_closures closure JOIN organizations o ON o.id=closure.organization_id WHERE o.slug=$1 AND closure.root_run_slug=$2`, org, exhausted.RunSlug).Scan(&exhaustedStatus, &finalAttempt))
	require.Equal(t, "failed", exhaustedStatus)
	require.Equal(t, exhausted.RunSlug.String()+"-retry-3", finalAttempt)
	var upstreamStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT run.status FROM pipeline_runs run JOIN project_environments pe ON pe.id=run.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND run.run_slug='exhausted-upstream'`, org).Scan(&upstreamStatus))
	require.Equal(t, "succeeded", upstreamStatus, "an exhausted child does not change its successful upstream")
	diagnostics, err = repo.ListTriggerRetryAttemptsForRun(ctx, pool, org, project, environment, exhausted.RunSlug)
	require.NoError(t, err)
	require.Len(t, diagnostics, 3)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusExhausted, diagnostics[2].Status)
	require.Equal(t, "max_attempts_exhausted", *diagnostics[2].DecisionReason)
	require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM pipeline_completion_snapshots WHERE id=$1`, exhausted.CompletionSnapshotID).Scan(&state))
	require.Equal(t, "completed", state)
	require.NoError(t, dispatcher.Dispatch(ctx))
	require.Equal(t, before+3, calls())

	maxElapsed := 3600
	input.RetryPolicy.MaxElapsedSeconds = &maxElapsed
	_, err = checker.UpdatePipelineTrigger(ctx, org, project, environment, trigger.Slug, model.UpdatePipelineTrigger{
		Name: input.Name, Type: input.Type, Enabled: true, PipelineSlug: input.PipelineSlug, VersionPolicy: input.VersionPolicy,
		CompletionConfig: input.CompletionConfig, RetryPolicy: input.RetryPolicy,
	}, model.UserEventActor(actorSlug))
	require.NoError(t, err)
	closeUpstream("elapsed-upstream")
	require.NoError(t, dispatcher.Dispatch(ctx))
	require.NoError(t, pool.QueryRow(ctx, `SELECT snapshot.execution_args FROM pipeline_completion_snapshots snapshot JOIN organizations o ON o.id=snapshot.organization_id WHERE o.slug=$1 AND snapshot.root_run_slug='elapsed-upstream'`, org).Scan(&raw))
	var elapsed jobs.PipelineRunExecutionArgs
	require.NoError(t, json.Unmarshal(raw, &elapsed))
	failNext()
	runWorker()
	require.NoError(t, dispatcher.Dispatch(ctx))
	require.NoError(t, pool.QueryRow(ctx, `SELECT retry.completion_execution_args FROM pipeline_trigger_retry_attempts retry JOIN project_environments pe ON pe.id=retry.project_environment_id JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND retry.root_run_slug=$2 AND retry.attempt_number=2`, org, elapsed.RunSlug).Scan(&retryRaw))
	var expired jobs.PipelineRunExecutionArgs
	require.NoError(t, json.Unmarshal(retryRaw, &expired))
	// Move the isolated fixture's first-attempt clock past its elapsed budget.
	exec(`UPDATE pipeline_runs run SET started_at=NOW()-INTERVAL '2 hours' FROM project_environments pe JOIN projects p ON p.id=pe.project_id JOIN organizations o ON o.id=p.organization_id WHERE run.project_environment_id=pe.id AND o.slug=$1 AND run.run_slug=$2`, org, elapsed.RunSlug)
	before = calls()
	runWorker()
	require.Equal(t, before, calls(), "an expired queued retry cannot start provider operations")
	expiredRun, err := executor.GetRun(ctx, org, project, environment, expired.RunSlug.String())
	require.NoError(t, err)
	require.Equal(t, "failed", expiredRun.Metadata.Status)
	require.Contains(t, *expiredRun.Metadata.ErrorSummary, "elapsed-time cap exceeded")
	require.NoError(t, dispatcher.Dispatch(ctx))
	diagnostics, err = repo.ListTriggerRetryAttemptsForRun(ctx, pool, org, project, environment, elapsed.RunSlug)
	require.NoError(t, err)
	require.Len(t, diagnostics, 2)
	require.Equal(t, model.PipelineTriggerRetryAttemptStatusExhausted, diagnostics[1].Status)
	require.Equal(t, "max_elapsed_exhausted", *diagnostics[1].DecisionReason)
}

type completionRuntimeWorker struct {
	dispatcher *pipelinetriggersvc.CompletionDispatcher
	completed  chan error
	entered    chan<- struct{}
	release    <-chan struct{}
}

func (w *completionRuntimeWorker) Register(workers *river.Workers) error {
	return river.AddWorkerSafely(workers, consumer.NewPipelineRunExecutionWorker(w, nil))
}

func (w *completionRuntimeWorker) ExecuteQueuedPipelineRun(ctx context.Context, args jobs.PipelineRunExecutionArgs) error {
	if w.entered != nil {
		select {
		case w.entered <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-w.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	err := w.dispatcher.ExecuteQueuedPipelineRun(ctx, args)
	w.completed <- err
	return err
}

func testCompletionDispatchPersistence(t *testing.T, pool *pgxpool.Pool, org, project, environment slug.Slug, env int64, snapshotID int, checker pipelinetriggersvc.Service) {
	t.Helper()
	ctx := t.Context()
	repo := pipelinetriggers.NewRepository()
	txm := postgres.NewTransactionManager(pool)
	executor, producer := newCompletionTestExecutor(t, pool)
	dispatcher, err := pipelinetriggersvc.NewCompletionDispatcher(true, txm, repo, checker, executor, producer)
	require.NoError(t, err)
	child := slug.Slug(fmt.Sprintf("completion-%d", snapshotID))
	counts := func(wantRuns, wantJobs int) {
		t.Helper()
		var runs, jobs int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug=$2`, env, child).Scan(&runs))
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind='pipeline_run_execution' AND args->>'organization_slug'=$1 AND args->>'run_slug'=$2`, org, child).Scan(&jobs))
		require.Equal(t, wantRuns, runs)
		require.Equal(t, wantJobs, jobs)
	}
	var state string
	assertState := func(want string) {
		t.Helper()
		require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM pipeline_completion_snapshots WHERE id=$1`, snapshotID).Scan(&state))
		require.Equal(t, want, state)
	}
	assertState("pending")
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			results <- dispatcher.Dispatch(ctx)
		}()
	}
	close(start)
	committed := 0
	for range 2 {
		err := <-results
		if err == nil {
			committed++
			continue
		}
		var serialization *pgconn.PgError
		require.ErrorAs(t, err, &serialization)
		require.Equal(t, "40001", serialization.Code, "the losing repeatable-read transaction must retry")
	}
	require.Positive(t, committed)
	require.NoError(t, dispatcher.Dispatch(ctx))
	counts(1, 1)
	assertState("accepted")
	inspected, err := pipelineruns.New().ListCompletionDecisions(ctx, pool, org, project, environment, child)
	require.NoError(t, err)
	require.Len(t, inspected, 1)
	require.True(t, inspected[0].IsParent)
	require.Equal(t, slug.Slug("dispatch-parent"), inspected[0].RootRunSlug)
	require.Equal(t, slug.Slug("dispatch-parent"), inspected[0].FinalAttemptRunSlug)
	require.Equal(t, child, *inspected[0].ChildRunSlug)
	require.Equal(t, "queued", *inspected[0].ChildStatus)
	require.Nil(t, inspected[0].ChildOutcome)
	require.Equal(t, "hash-v2", *inspected[0].SelectedVersionHash)
	require.Equal(t, int64(1), *inspected[0].SelectedDeploymentRevision)

	require.Error(t, repo.AcceptCompletionSnapshot(ctx, pool, "other-organization", snapshotID, "other-child", []byte(`{}`)))
	_, err = pool.Exec(ctx, `UPDATE pipeline_completion_snapshots SET execution_args='{}' WHERE id=$1`, snapshotID)
	require.ErrorContains(t, err, "immutable")

	repairs, err := repo.ListCompletionRepairsForSystem(ctx, pool)
	require.NoError(t, err)
	for _, repair := range repairs {
		require.NotEqual(t, snapshotID, repair.ID, "a live job must not be repaired")
	}
	_, err = pool.Exec(ctx, `DELETE FROM river_job WHERE args->>'organization_slug'=$1 AND args->>'run_slug'=$2`, org, child)
	require.NoError(t, err)
	counts(1, 0)
	repairs, err = repo.ListCompletionRepairsForSystem(ctx, pool)
	require.NoError(t, err)
	found := false
	for _, repair := range repairs {
		if repair.ID != snapshotID {
			continue
		}
		found = true
		var args jobs.PipelineRunExecutionArgs
		require.NoError(t, json.Unmarshal(repair.ExecutionArgs, &args))
		require.Equal(t, child, args.RunSlug)
		require.Equal(t, "hash-v2", args.PipelineVersionHash)
		require.Equal(t, "resolution-v2", *args.DeploymentResolutionDigest)

	}
	require.True(t, found, "job retention must not erase the reserved execution identity")
	require.NoError(t, dispatcher.Dispatch(ctx))
	counts(1, 1)
	_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET status='running' WHERE project_environment_id=$1 AND run_slug=$2`, env, child)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM river_job WHERE args->>'organization_slug'=$1 AND args->>'run_slug'=$2`, org, child)
	require.NoError(t, err)
	repairs, err = repo.ListCompletionRepairsForSystem(ctx, pool)
	require.NoError(t, err)
	for _, repair := range repairs {
		require.NotEqual(t, snapshotID, repair.ID, "a running child cannot be restarted after worker loss")
	}
	_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET status='failed',ended_at=NOW() WHERE project_environment_id=$1 AND run_slug=$2`, env, child)
	require.NoError(t, err)
	require.NoError(t, dispatcher.Dispatch(ctx))
	require.NoError(t, dispatcher.Dispatch(ctx))
	assertState("completed")
	var parentStatus string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='dispatch-parent'`, env).Scan(&parentStatus))
	require.Equal(t, "succeeded", parentStatus, "child failure cannot change its parent's outcome")
}
