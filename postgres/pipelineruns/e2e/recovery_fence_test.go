package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/stretchr/testify/require"
)

func TestWorkerLossRecoveryRequiresTenantScopedQueueFence(t *testing.T) {
	pool := openPipelineRunsE2EPool(t)
	ctx := t.Context()
	org, project, environment := slug.Slug("repair-org"), slug.Slug("repair-project"), slug.Slug("local")
	pe := seedPipelineRunSourceEventRun(t, pool, org, project, environment, "export", "interrupted", time.Now().UTC(), "failed")
	repo := pipelineruns.New()
	require.ErrorIs(t, repo.LockRecoveryFence(ctx, pool, org, project, environment, "interrupted"), pipelineruns.ErrRunNotFound, "missing queue record is not a fence")
	var jobID int64
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO river_job(kind,queue,max_attempts,args) VALUES('repair_test','repair_test',1,'{}') RETURNING id`).Scan(&jobID))
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM river_job WHERE id=$1`, jobID)
		require.NoError(t, err)
	})
	_, err := pool.Exec(ctx, `UPDATE pipeline_runs SET worker_job_id=$1 WHERE project_environment_id=$2 AND run_slug='interrupted'`, jobID, pe)
	require.NoError(t, err)
	require.ErrorIs(t, repo.LockRecoveryFence(ctx, pool, org, project, environment, "interrupted"), pipelineruns.ErrRunNotFound, "an unstarted job does not prove ended ownership")
	for _, state := range []string{"running", "available", "retryable", "scheduled", "completed", "cancelled", "discarded"} {
		t.Run(state, func(t *testing.T) {
			_, err := pool.Exec(ctx, `UPDATE river_job SET state=$1::river_job_state, attempt=1, finalized_at=CASE WHEN $1 IN ('completed','cancelled','discarded') THEN now() ELSE NULL END WHERE id=$2`, state, jobID)
			require.NoError(t, err)
			err = repo.LockRecoveryFence(ctx, pool, org, project, environment, "interrupted")
			if state == "running" {
				require.ErrorIs(t, err, pipelineruns.ErrRunNotFound)
				return
			}
			require.NoError(t, err)
			for _, scope := range [][3]slug.Slug{{"other", project, environment}, {org, "other", environment}, {org, project, "other"}} {
				require.ErrorIs(t, repo.LockRecoveryFence(ctx, pool, scope[0], scope[1], scope[2], "interrupted"), pipelineruns.ErrRunNotFound)
			}
		})
	}
	row, err := repo.FindRunBySlug(ctx, pool, org, project, environment, "interrupted")
	require.NoError(t, err)
	_, err = repo.CreateRun(ctx, pool, org, project, environment, &pipelineruns.CreateRunParams{PipelineVersionID: row.PipelineVersionID, PipelineSlug: "export", RunSlug: "still-running", Status: "running", StartedAt: time.Now().UTC(), Timeline: []byte(`{"events":[]}`), WorkerJobID: &jobID})
	require.NoError(t, err)
	require.ErrorIs(t, repo.LockRecoveryFence(ctx, pool, org, project, environment, "still-running"), pipelineruns.ErrRunNotFound, "queue fence alone cannot recover a nonterminal run")
}
