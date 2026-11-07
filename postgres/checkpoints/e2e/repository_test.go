package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres/checkpoints"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestRepository_PersistsPinnedDeploymentResolution(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-Postgres checkpoint repository test in short mode")
	}

	ctx := t.Context()
	pool := e2edb.OpenPostgresPool(t, "checkpoint repository integration test")
	suffix := time.Now().UnixNano()
	org := slug.Slug(fmt.Sprintf("checkpoint-resolution-org-%d", suffix))
	project := slug.Slug(fmt.Sprintf("checkpoint-resolution-project-%d", suffix))
	environment := slug.Slug("staging")
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, org)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), `DELETE FROM organizations WHERE slug = $1`, org)
		require.NoError(t, err)
	})
	projectEnvironmentID := seedCheckpointRun(t, ctx, pool, org, project, environment, "checkpoint-run")
	_, err := pool.Exec(ctx, `
		INSERT INTO pipeline_runs (project_id, project_environment_id, pipeline_version_id, pipeline_version_spec_hash, api_version, pipeline_slug, run_slug, source_environment_slug, status, duration_ms, step_count, step_slug, started_at, ended_at, timeline)
		SELECT project_id, project_environment_id, pipeline_version_id, pipeline_version_spec_hash, api_version, pipeline_slug, 'legacy-checkpoint-run', source_environment_slug, status, duration_ms, step_count, step_slug, started_at, ended_at, timeline
		FROM pipeline_runs WHERE project_environment_id = $1 AND run_slug = 'checkpoint-run'`, projectEnvironmentID)
	require.NoError(t, err)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "checkpoint-pipeline", Name: "Checkpoint Pipeline"},
		Steps: []pipelinespec.Step{{Kind: "sink", Slug: "sink", Name: "Sink", Sink: &pipelinespec.SinkSpec{Config: map[string]any{
			"path":  pipelineruntime.LiteralString("exports/{{runtime.run.date}}/orders.json"),
			"mixed": pipelineruntime.LiteralTemplate{Text: "x{{customer}}/{{runtime.run.date}}", Ranges: [][2]int{{1, 13}}},
		}}}},
	}
	compiledPlan := []byte(`{"format_version":1,"logical_source_ref":"source-template"}`)
	deploymentPlan := []byte(`{"format_version":1,"bindings":[{"logical_source_ref":"source-template","data_source_slug":"source-physical-a"}]}`)
	repo := checkpoints.New(pool)
	require.NoError(t, repo.SaveCheckpoint(ctx, org, project, environment, "checkpoint-run", run.ReplayCheckpoint{
		RunSlug:                    "checkpoint-run",
		OrgSlug:                    org,
		ProjectSlug:                project,
		EnvironmentSlug:            environment,
		StepSlug:                   "sink",
		Pipeline:                   pipeline,
		Results:                    map[slug.Slug]*run.StepResult{},
		CompiledPlan:               compiledPlan,
		CompiledPlanDigest:         "sha256:compiled",
		DeploymentPlan:             deploymentPlan,
		DeploymentResolutionDigest: "sha256:resolution-a",
	}))

	checkpoint, err := repo.GetCheckpoint(ctx, org, project, environment, "checkpoint-run")
	require.NoError(t, err)
	require.Equal(t, compiledPlan, checkpoint.CompiledPlan)
	require.Equal(t, "sha256:compiled", checkpoint.CompiledPlanDigest)
	require.Equal(t, deploymentPlan, checkpoint.DeploymentPlan)
	require.Equal(t, "sha256:resolution-a", checkpoint.DeploymentResolutionDigest)
	require.Equal(t, pipelineruntime.LiteralString("exports/{{runtime.run.date}}/orders.json"), checkpoint.Pipeline.Steps[0].Sink.Config["path"])
	require.NoError(t, pipelineruntime.ResolvePipelineExecution(checkpoint.Pipeline, pipelineruntime.ExecutionContext{At: time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC)}))
	require.Equal(t, "x{{customer}}/2024-03-31", pipelineruntime.LiteralValue(checkpoint.Pipeline.Steps[0].Sink.Config["mixed"]))

	pipelineJSON, err := json.Marshal(pipeline)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO pipeline_checkpoint (project_environment_id, run_slug, step_slug, pipeline_spec, step_results)
		SELECT project_environment_id, run_slug, 'sink', $1, '{}'::jsonb
		FROM pipeline_runs WHERE project_environment_id = $2 AND run_slug = 'legacy-checkpoint-run'`, pipelineJSON, projectEnvironmentID)
	require.NoError(t, err)

	legacy, err := repo.GetCheckpoint(ctx, org, project, environment, "legacy-checkpoint-run")
	require.NoError(t, err)
	require.Empty(t, legacy.DeploymentPlan)
	require.Empty(t, legacy.DeploymentResolutionDigest)
	require.Equal(t, "exports/{{runtime.run.date}}/orders.json", legacy.Pipeline.Steps[0].Sink.Config["path"])
}

func seedCheckpointRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org, project, environment slug.Slug, runSlug string) int64 {
	t.Helper()

	var projectEnvironmentID int64
	err := pool.QueryRow(ctx, `
		WITH organization AS (
			INSERT INTO organizations (name, slug) VALUES ('Checkpoint Resolution Org', $1)
			ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
			RETURNING id
		), project AS (
			INSERT INTO projects (organization_id, name, slug)
			SELECT id, 'Checkpoint Resolution Project', $2 FROM organization
			ON CONFLICT (organization_id, slug) DO UPDATE SET name = EXCLUDED.name
			RETURNING id, organization_id
		), environment_row AS (
			INSERT INTO environments (organization_id, name, slug)
			SELECT organization_id, 'Staging', $3 FROM project
			ON CONFLICT (organization_id, slug) DO UPDATE SET name = EXCLUDED.name
			RETURNING id, organization_id
		)
		INSERT INTO project_environments (organization_id, project_id, environment_id)
		SELECT project.organization_id, project.id, environment_row.id FROM project JOIN environment_row ON environment_row.organization_id = project.organization_id
		ON CONFLICT (project_id, environment_id) DO UPDATE SET deleted_at = NULL
		RETURNING id`, org, project, environment).Scan(&projectEnvironmentID)
	require.NoError(t, err)
	var versionID int64
	err = pool.QueryRow(ctx, `
		WITH project AS (
			SELECT id FROM projects WHERE slug = $1 AND organization_id = (SELECT id FROM organizations WHERE slug = $2)
		), spec AS (
			INSERT INTO pipeline_specs (project_id, slug, name, spec_jsonb, spec_hash, source_path, source_ref, last_applied_commit)
			SELECT id, 'checkpoint-pipeline', 'Checkpoint Pipeline', '{}'::jsonb, 'sha256:checkpoint-spec', 'pipelines/checkpoint.json', 'refs/heads/main', repeat('0', 40) FROM project
			ON CONFLICT (project_id, slug) DO UPDATE SET name = EXCLUDED.name
		)
		INSERT INTO pipeline_versions (project_id, pipeline_slug, spec_jsonb, spec_hash)
		SELECT id, 'checkpoint-pipeline', '{}'::jsonb, 'sha256:checkpoint-version' FROM project
		RETURNING id`, project, org).Scan(&versionID)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO pipeline_runs (project_id, project_environment_id, pipeline_version_id, pipeline_version_spec_hash, api_version, pipeline_slug, run_slug, source_environment_slug, status, duration_ms, step_count, step_slug, started_at, ended_at, timeline)
		SELECT project_id, $1, $2, 'sha256:checkpoint-version', 'v1', 'checkpoint-pipeline', $3, $4, 'failed', 0, 1, 'sink', $5, $5, '{}'::jsonb
		FROM project_environments WHERE id = $1`, projectEnvironmentID, versionID, runSlug, environment, time.Now().UTC())
	require.NoError(t, err)

	return projectEnvironmentID
}
