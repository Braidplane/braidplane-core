package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/service/pipelinetriggersvc"
	"github.com/Braidplane/braidplane-core/service/worker/consumer"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

const completionCrashReady = "BP_COMPLETION_CRASH_READY"

type completionCrashConfig struct {
	Phase, Root, Queue string
	EnvironmentID      int64
}

type completionCrashDriver struct {
	pool       *pgxpool.Pool
	config     completionCrashConfig
	dispatcher *pipelinetriggersvc.CompletionDispatcher
}

func completionCrashGate(ctx context.Context) error {
	fmt.Println(completionCrashReady)
	<-ctx.Done()
	return ctx.Err()
}

func (d *completionCrashDriver) Register(workers *river.Workers) error {
	return river.AddWorkerSafely(workers, consumer.NewCompletionDispatchWorker(d))
}

func (d *completionCrashDriver) Dispatch(ctx context.Context) error {
	if d.config.Phase == "after-completion-failure-persisted" {
		_, err := d.pool.Exec(ctx, `UPDATE pipeline_runs SET status='failed',failure_kind='data_source',ended_at=NOW() WHERE project_environment_id=$1 AND run_slug=$2`, d.config.EnvironmentID, d.config.Root)
		if err != nil {
			return err
		}
		return completionCrashGate(ctx)
	}
	if strings.Contains(d.config.Phase, "closure") {
		tx, err := d.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		// Persist a known synthetic terminal fact; no provider work is replayed.
		_, err = tx.Exec(ctx, `UPDATE pipeline_runs SET status='succeeded',ended_at=NOW() WHERE project_environment_id=$1 AND run_slug=$2`, d.config.EnvironmentID, d.config.Root)
		if err != nil {
			return err
		}
		if d.config.Phase == "before-closure-commit" {
			return completionCrashGate(ctx)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return completionCrashGate(ctx)
	}
	if err := d.dispatcher.Dispatch(ctx); err != nil {
		return err
	}
	return completionCrashGate(ctx)
}

type completionCrashAdmission struct {
	pipelinetriggersvc.CompletionRunExecutor
}

func (e completionCrashAdmission) AcceptCompletionRun(ctx context.Context, org, project, environment slug.Slug, snapshot *model.PipelineCompletionSnapshot) (jobs.PipelineRunExecutionArgs, error) {
	args, err := e.CompletionRunExecutor.AcceptCompletionRun(ctx, org, project, environment, snapshot)
	if err != nil {
		return args, err
	}
	return args, completionCrashGate(ctx)
}

// Run only in a subprocess against the parent's isolated PostgreSQL fixture.
func TestCompletionCrashWorkerProcess(t *testing.T) {
	if os.Getenv("BP_COMPLETION_CRASH_WORKER") != "1" {
		return
	}
	var config completionCrashConfig
	require.NoError(t, json.Unmarshal([]byte(os.Getenv("BP_COMPLETION_CRASH_CONFIG")), &config))
	pool := openIntegrationPool(t)
	txm := postgres.NewTransactionManager(pool)
	executor, producer := newCompletionTestExecutor(t, pool)
	checker := pipelinetriggersvc.New(true, txm, pipelinetriggers.NewRepository(), completionPersistenceValidator{}, eventtest.NewProducer(nil), nil)
	var admissions pipelinetriggersvc.CompletionRunExecutor = executor
	if config.Phase == "before-admission-commit" {
		admissions = completionCrashAdmission{executor}
	}
	dispatcher, err := pipelinetriggersvc.NewCompletionDispatcher(true, txm, pipelinetriggers.NewRepository(), checker, admissions, producer)
	require.NoError(t, err)
	driver := &completionCrashDriver{pool: pool, config: config, dispatcher: dispatcher}
	host, err := queue.NewWorkerHost(pool, &queue.WorkerHostConfig{
		Queues: map[string]river.QueueConfig{config.Queue: {MaxWorkers: 1}}, Registrars: []queue.WorkerRegistrar{driver},
	})
	require.NoError(t, err)
	require.NoError(t, host.Start(t.Context()))
	<-t.Context().Done()
}

func killCompletionWorkerAtGate(ctx context.Context, t *testing.T, config completionCrashConfig) {
	t.Helper()
	require.NotEmpty(t, os.Getenv("DATABASE_URL"), "subprocess must inherit the isolated fixture")
	encoded, err := json.Marshal(config)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	binary, err := os.Executable()
	require.NoError(t, err)
	// #nosec G204 G702 -- re-executes this test binary with fixed arguments and the isolated fixture.
	command := exec.CommandContext(ctx, binary, "-test.run=^TestCompletionCrashWorkerProcess$", "-test.timeout=30s")
	command.Env = append(os.Environ(), "BP_COMPLETION_CRASH_WORKER=1", "BP_COMPLETION_CRASH_CONFIG="+string(encoded))
	stderr, err := os.CreateTemp(t.TempDir(), "worker-stderr")
	require.NoError(t, err)
	defer func() { require.NoError(t, stderr.Close()) }()
	command.Stderr = stderr
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	defer func() { _ = command.Process.Kill() }()
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == completionCrashReady {
				ready <- nil
				return
			}
		}
		ready <- xerror.Unexpected().Detail("worker exited before crash gate").With(scanner.Err())
	}()
	select {
	case err := <-ready:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("worker did not reach crash gate")
	}
	require.NoError(t, command.Process.Kill())
	require.Error(t, command.Wait(), "the actual River worker must be killed")
}

func testCompletionWorkerCrashes(t *testing.T, pool *pgxpool.Pool, org, project, environment slug.Slug, env int64, actor model.EventActor, checker pipelinetriggersvc.Service) {
	t.Helper()
	ctx := t.Context()
	executor, producer := newCompletionTestExecutor(t, pool)
	dispatcher, err := pipelinetriggersvc.NewCompletionDispatcher(true, postgres.NewTransactionManager(pool), pipelinetriggers.NewRepository(), checker, executor, producer)
	require.NoError(t, err)
	client, err := queue.NewEnqueueOnly(pool)
	require.NoError(t, err)
	for _, phase := range []string{"before-closure-commit", "after-closure-commit", "before-admission-commit", "after-admission-commit"} {
		t.Run(phase, func(t *testing.T) {
			root := "crash-" + phase
			_, err := pool.Exec(ctx, `INSERT INTO pipeline_runs(project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,source_type,source_root_run_slug)
                SELECT project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,$2,source_environment_slug,'running',0,'fetch','{}','manual',$2 FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='template'`, env, root)
			require.NoError(t, err)
			if strings.Contains(phase, "admission") {
				_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET status='succeeded',ended_at=NOW() WHERE project_environment_id=$1 AND run_slug=$2`, env, root)
				require.NoError(t, err)
			}
			config := completionCrashConfig{Phase: phase, Root: root, Queue: fmt.Sprintf("crash-%d-%s", env, phase), EnvironmentID: env}
			err = postgres.NewTransactionManager(pool).RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
				_, err := queue.NewEnqueuer(client).EnqueueTx(ctx, db, queue.EnqueueRequest{Args: jobs.PipelineCompletionDispatchArgs{}, Queue: config.Queue})
				return err
			})
			require.NoError(t, err)
			killCompletionWorkerAtGate(ctx, t, config)
			var status string
			require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug=$2`, env, root).Scan(&status))
			if phase == "before-closure-commit" {
				require.Equal(t, "running", status)
				var closures int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_run_logical_closures WHERE project_environment_id=$1 AND root_run_slug=$2`, env, root).Scan(&closures))
				require.Zero(t, closures)
				_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET status='succeeded',ended_at=NOW() WHERE project_environment_id=$1 AND run_slug=$2`, env, root)
				require.NoError(t, err)
			} else {
				require.Equal(t, "succeeded", status)
			}
			var snapshotID int
			var state, child string
			require.NoError(t, pool.QueryRow(ctx, `SELECT id,state,COALESCE(child_run_slug,'') FROM pipeline_completion_snapshots WHERE project_environment_id=$1 AND root_run_slug=$2`, env, root).Scan(&snapshotID, &state, &child))
			if phase == "after-admission-commit" {
				require.Equal(t, "accepted", state)
				require.Equal(t, fmt.Sprintf("completion-%d", snapshotID), child)
				_, err = pool.Exec(ctx, `DELETE FROM river_job WHERE args->>'organization_slug'=$1 AND args->>'run_slug'=$2`, org, child)
				require.NoError(t, err)
			} else {
				require.Equal(t, "pending", state)
				require.Empty(t, child)
				var children, jobs int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug=$2`, env, fmt.Sprintf("completion-%d", snapshotID)).Scan(&children))
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE args->>'organization_slug'=$1 AND args->>'run_slug'=$2`, org, fmt.Sprintf("completion-%d", snapshotID)).Scan(&jobs))
				require.Zero(t, children)
				require.Zero(t, jobs)
			}
			require.NoError(t, dispatcher.Dispatch(ctx))
			require.NoError(t, dispatcher.Dispatch(ctx))
			var runs, executions int
			child = fmt.Sprintf("completion-%d", snapshotID)
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug=$2`, env, child).Scan(&runs))
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE args->>'organization_slug'=$1 AND args->>'run_slug'=$2`, org, child).Scan(&executions))
			require.Equal(t, 1, runs)
			require.Equal(t, 1, executions)
			require.NoError(t, pool.QueryRow(ctx, `SELECT state,child_run_slug FROM pipeline_completion_snapshots WHERE id=$1`, snapshotID).Scan(&state, &child))
			require.Equal(t, "accepted", state)
			require.Equal(t, fmt.Sprintf("completion-%d", snapshotID), child)
		})
	}
	t.Run("after completion failure before retry decision", func(t *testing.T) {
		trigger, err := checker.GetPipelineTrigger(ctx, org, project, environment, "after-upstream")
		require.NoError(t, err)
		retryPolicy := model.PipelineTriggerRetryPolicy{
			Enabled: true, MaxAttempts: 2, BackoffStrategy: model.PipelineTriggerBackoffStrategyNone,
			RetryableFailureKinds: []model.PipelineTriggerRetryFailureKind{model.PipelineTriggerRetryFailureKindDataSource},
		}
		_, err = checker.UpdatePipelineTrigger(ctx, org, project, environment, trigger.Slug, model.UpdatePipelineTrigger{
			Name: trigger.Name, Type: trigger.Type, Enabled: trigger.Enabled, PipelineSlug: trigger.PipelineSlug,
			VersionPolicy: trigger.VersionPolicy, RuntimeInputKeys: trigger.RuntimeInputKeys, RetryPolicy: retryPolicy,
			CompletionConfig: trigger.CompletionConfig,
		}, actor)
		require.NoError(t, err)
		parent := "crash-retry-decision-parent"
		_, err = pool.Exec(ctx, `INSERT INTO pipeline_runs(project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,run_slug,source_environment_slug,status,duration_ms,step_slug,timeline,source_type,source_root_run_slug)
            SELECT project_id,project_environment_id,pipeline_version_id,pipeline_version_spec_hash,api_version,pipeline_slug,$2,source_environment_slug,'running',0,'fetch','{}','manual',$2 FROM pipeline_runs WHERE project_environment_id=$1 AND run_slug='template'`, env, parent)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET status='succeeded',ended_at=NOW() WHERE project_environment_id=$1 AND run_slug=$2`, env, parent)
		require.NoError(t, err)
		require.NoError(t, dispatcher.Dispatch(ctx))

		var snapshotID int
		var child string
		require.NoError(t, pool.QueryRow(ctx, `SELECT id,child_run_slug FROM pipeline_completion_snapshots WHERE project_environment_id=$1 AND root_run_slug=$2 AND state='accepted'`, env, parent).Scan(&snapshotID, &child))
		config := completionCrashConfig{Phase: "after-completion-failure-persisted", Root: child, Queue: fmt.Sprintf("crash-%d-retry-decision", env), EnvironmentID: env}
		err = postgres.NewTransactionManager(pool).RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
			_, err := queue.NewEnqueuer(client).EnqueueTx(ctx, db, queue.EnqueueRequest{Args: jobs.PipelineCompletionDispatchArgs{}, Queue: config.Queue})
			return err
		})
		require.NoError(t, err)
		killCompletionWorkerAtGate(ctx, t, config)

		var obligation, retry, closure, retries int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_run_terminal_obligations WHERE project_environment_id=$1 AND root_run_slug=$2 AND attempt_run_slug=$2 AND attempt_status='failed' AND resolved_at IS NULL`, env, child).Scan(&obligation))
		require.Equal(t, 1, obligation)
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_trigger_retry_attempts WHERE project_environment_id=$1 AND root_run_slug=$2 AND retry_of_run_slug=$2`, env, child).Scan(&retry))
		require.Zero(t, retry, "the crash must leave the failed completion attempt without a retry decision")
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_run_logical_closures WHERE project_environment_id=$1 AND root_run_slug=$2`, env, child).Scan(&closure))
		require.Zero(t, closure, "a failed completion attempt cannot close before its retry decision")
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_runs WHERE project_environment_id=$1 AND source_root_run_slug=$2 AND source_attempt_number>1`, env, child).Scan(&retries))
		require.Zero(t, retries, "the crash cannot create a retry child")

		require.NoError(t, dispatcher.Dispatch(ctx))
		var retryRun string
		require.NoError(t, pool.QueryRow(ctx, `SELECT run_slug FROM pipeline_trigger_retry_attempts WHERE project_environment_id=$1 AND root_run_slug=$2 AND retry_of_run_slug=$2 AND attempt_number=2 AND status='queued'`, env, child).Scan(&retryRun))
		require.Equal(t, child+"-retry-2", retryRun)
		_, err = pool.Exec(ctx, `UPDATE pipeline_runs SET status='succeeded',ended_at=NOW() WHERE project_environment_id=$1 AND run_slug=$2`, env, retryRun)
		require.NoError(t, err)
		require.NoError(t, dispatcher.Dispatch(ctx))
		require.NoError(t, dispatcher.Dispatch(ctx))

		var root, final, state string
		require.NoError(t, pool.QueryRow(ctx, `SELECT root_run_slug,final_attempt_run_slug FROM pipeline_run_logical_closures WHERE project_environment_id=$1 AND root_run_slug=$2`, env, child).Scan(&root, &final))
		require.Equal(t, child, root)
		require.Equal(t, retryRun, final)
		require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM pipeline_completion_snapshots WHERE id=$1`, snapshotID).Scan(&state))
		require.Equal(t, "completed", state)
	})
}
