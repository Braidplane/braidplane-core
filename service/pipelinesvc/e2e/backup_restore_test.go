package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

// Logical recovery on a disposable server; no HTTP admission, queue workers or external sink.
func TestBackupRestorePreservesRunHistoryAndExecutesNewRun(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL and matching recovery clients")
	}
	ctx := t.Context()
	source := openIntegrationPool(t)
	org, environment := slug.Slug("backup-runs"), slug.Slug("prod")
	seedOrganization(t, source, org, "Synthetic recovery")
	seedPipelineDeploymentEnvironment(t, source, org, defaultProjectSlug, environment)
	t.Cleanup(func() {
		_, err := source.Exec(context.Background(), `DELETE FROM project_environments WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
		require.NoError(t, err)
		_, err = source.Exec(context.Background(), `DELETE FROM organizations WHERE slug=$1`, org)
		require.NoError(t, err)
	})
	_, err := source.Exec(ctx, `CREATE TABLE worker_loss_rehearsal_source(id integer PRIMARY KEY,label text NOT NULL); INSERT INTO worker_loss_rehearsal_source VALUES(1,'original'); CREATE TABLE worker_loss_rehearsal_reads(run_slug text PRIMARY KEY,reads integer NOT NULL)`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := source.Exec(context.Background(), `DROP TABLE worker_loss_rehearsal_source,worker_loss_rehearsal_reads`)
		require.NoError(t, err)
	})
	pipeline := nativeRepairPipeline()
	pipeline.Steps = pipeline.Steps[:1]
	txm := postgres.NewTransactionManager(source)
	specs, err := pipelinesvc.NewPipelineStore(source, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	materializePipelines(t, ctx, specs, org, defaultProjectSlug, pipeline)
	metadata, err := specs.FindSpec(ctx, org, defaultProjectSlug, pipeline.Metadata.Slug)
	require.NoError(t, err)
	require.NotNil(t, metadata.CurrentVersionID)
	execute := func(pool *pgxpool.Pool, name string) {
		manager := postgres.NewTransactionManager(pool)
		store := pipelineruntime.NewRunStore(manager, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New())
		sink := pipelineruntime.NewSinkRuntime(recoverySinkExecutor{calls: map[string]int{}}, testSinkEffectLedger(t, manager))
		runner, err := pipelineruntime.NewRunner(nativeRepairSource{pool: pool, run: name}, clock.New(), store, nativeRepairSources{}, sink)
		require.NoError(t, err)
		outcome, err := runner.Run(ctx, pipelineruntime.RunRequest{OrgSlug: org, ProjectSlug: defaultProjectSlug, EnvironmentSlug: environment, RunSlug: name, PipelineVersionID: *metadata.CurrentVersionID, Pipeline: pipeline})
		require.NoError(t, err)
		require.Equal(t, "succeeded", outcome.Metadata.Status)
		rows, ok := outcome.StepResults["read"].Execution().SQL()
		require.True(t, ok)
		require.Equal(t, []map[string]any{{"id": 1, "label": "original"}}, rows.Rows)
	}
	execute(source, "before-backup")
	history := pipelineruntime.NewRunStore(txm, source, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New())
	original, err := history.Get(ctx, org, defaultProjectSlug, environment, "before-backup")
	require.NoError(t, err)
	dump := filepath.Join(t.TempDir(), "database.dump")
	require.NoError(t, os.WriteFile(dump, nil, 0o600))
	client := func(name string, config *pgx.ConnConfig, arguments ...string) {
		t.Helper()
		command := exec.CommandContext(ctx, name, append([]string{"--host", config.Host, "--port", fmt.Sprint(config.Port), "--username", config.User, "--dbname", config.Database}, arguments...)...)
		command.Env = append(os.Environ(), "PGPASSWORD="+config.Password, "PGSSLMODE=disable")
		require.NoError(t, command.Run(), "synthetic recovery client failed; output suppressed to protect credentials")
	}
	client("pg_dump", source.Config().ConnConfig, "--format=custom", "--no-owner", "--no-acl", "--file", dump)
	execute(source, "lost-after-backup")
	adminConfig := source.Config().Copy()
	adminConfig.ConnConfig.Database = "postgres"
	admin, err := pgxpool.NewWithConfig(ctx, adminConfig)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	name := fmt.Sprintf("run_restore_%d", time.Now().UnixNano())
	_, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE template0")
	require.NoError(t, err)
	restoredConfig := source.Config().Copy()
	restoredConfig.ConnConfig.Database = name
	restored, err := pgxpool.NewWithConfig(ctx, restoredConfig)
	require.NoError(t, err)
	t.Cleanup(func() {
		restored.Close()
		_, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		require.NoError(t, err)
	})
	client("pg_restore", restoredConfig.ConnConfig, "--exit-on-error", "--no-owner", "--no-acl", dump)
	recoveredHistory := pipelineruntime.NewRunStore(postgres.NewTransactionManager(restored), restored, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New())
	recovered, err := recoveredHistory.Get(ctx, org, defaultProjectSlug, environment, "before-backup")
	require.NoError(t, err)
	require.Equal(t, original, recovered)
	_, err = recoveredHistory.Get(ctx, org, defaultProjectSlug, environment, "lost-after-backup")
	require.ErrorIs(t, err, pipelinerun.ErrTimelineNotFound)
	execute(restored, "after-restore")
	newHistory, err := recoveredHistory.Get(ctx, org, defaultProjectSlug, environment, "after-restore")
	require.NoError(t, err)
	require.Equal(t, "succeeded", newHistory.Status)
	retained, err := recoveredHistory.Get(ctx, org, defaultProjectSlug, environment, "before-backup")
	require.NoError(t, err)
	require.Equal(t, original, retained)
	t.Log("restored history unchanged; post-backup run absent; new run succeeded with frozen source oracle; logical same-server recovery only")
}
