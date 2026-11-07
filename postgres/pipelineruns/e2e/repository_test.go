package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
)

// TestRepository_UpdateRunSourceEventStoresEventSlugBeforeEventRowExists verifies the run-event link can be staged before the event row is durable.
func TestRepository_UpdateRunSourceEventStoresEventSlugBeforeEventRowExists(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres pipeline run repository E2E test in short mode")
	}

	ctx := context.Background()
	pool := openPipelineRunsE2EPool(t)
	repo := pipelineruns.New()

	orgSlug := must.Slug("pipeline-run-source-event-org")
	projectSlug := must.Slug("pipeline-run-source-event-project")
	environmentSlug := must.Slug("prod")
	pipelineSlug := must.Slug("pipeline-run-source-event-pipeline")
	runSlug := must.Slug("pipeline-run-source-event-run")
	sourceEventSlug := must.Slug("evt-pipeline-run-source-001")
	startedAt := time.Date(2026, time.May, 20, 10, 0, 0, 0, time.UTC)

	projectEnvironmentID := seedPipelineRunSourceEventRun(
		t,
		pool,
		orgSlug,
		projectSlug,
		environmentSlug,
		pipelineSlug,
		runSlug,
		startedAt,
		"succeeded",
	)

	err := repo.UpdateRunSourceEvent(
		ctx,
		pool,
		orgSlug,
		projectSlug,
		environmentSlug,
		runSlug.String(),
		sourceEventSlug,
	)
	require.NoError(t, err)

	var got string
	err = pool.QueryRow(
		ctx,
		`SELECT source_event_slug FROM pipeline_runs WHERE project_environment_id = $1 AND run_slug = $2`,
		projectEnvironmentID,
		runSlug,
	).Scan(&got)
	require.NoError(t, err)
	require.Equal(t, sourceEventSlug.String(), got)
}

// openPipelineRunsE2EPool opens the shared Postgres dependency used by pipeline run repository E2E coverage.
func openPipelineRunsE2EPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	connString := "postgres://postgres:postgres@localhost:35432/braidplane?sslmode=disable"
	if envConnString := os.Getenv("DATABASE_URL"); envConnString != "" {
		connString = envConnString
	}

	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		t.Fatalf("required real-Postgres pipeline run repository E2E test: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("required real-Postgres pipeline run repository E2E test: %v", err)
	}

	t.Cleanup(pool.Close)
	return pool
}

// seedPipelineRunSourceEventRun creates the minimum persisted run graph needed to update source event linkage.
func seedPipelineRunSourceEventRun(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, environmentSlug, pipelineSlug, runSlug slug.Slug,
	startedAt time.Time,
	status string,
) int64 {
	t.Helper()

	ctx := context.Background()

	var orgID int64
	err := pool.QueryRow(
		ctx,
		`INSERT INTO organizations (name, slug)
		VALUES ($1, $2)
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
		RETURNING id`,
		"Pipeline Run Source Event Org",
		orgSlug,
	).Scan(&orgID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, err)
	})

	var projectID int64
	err = pool.QueryRow(
		ctx,
		`INSERT INTO projects (organization_id, name, slug)
		VALUES ($1, $2, $3)
		RETURNING id`,
		orgID,
		"Pipeline Run Source Event Project",
		projectSlug,
	).Scan(&projectID)
	require.NoError(t, err)

	var environmentID int64
	err = pool.QueryRow(
		ctx,
		`INSERT INTO environments (organization_id, name, slug)
		VALUES ($1, $2, $3)
		RETURNING id`,
		orgID,
		"Production",
		environmentSlug,
	).Scan(&environmentID)
	require.NoError(t, err)

	var projectEnvironmentID int64
	err = pool.QueryRow(
		ctx,
		`INSERT INTO project_environments (organization_id, project_id, environment_id)
			VALUES ($3, $1, $2)
			RETURNING id`,
		projectID,
		environmentID,
		orgID,
	).Scan(&projectEnvironmentID)
	require.NoError(t, err)

	_, err = pool.Exec(
		ctx,
		`INSERT INTO pipeline_specs (
			project_id,
			slug,
			name,
			spec_jsonb,
			spec_hash,
			source_path,
			source_ref,
			last_applied_commit
		)
		VALUES ($1, $2, $3, '{}'::jsonb, $4, $5, $6, $7)`,
		projectID,
		pipelineSlug,
		"Pipeline Run Source Event Pipeline",
		"sha256:source-event-spec",
		"pipelines/pipeline-run-source-event.pipeline.json",
		"refs/heads/main",
		"0000000000000000000000000000000000000000",
	)
	require.NoError(t, err)

	var versionID int64
	err = pool.QueryRow(
		ctx,
		`INSERT INTO pipeline_versions (project_id, pipeline_slug, spec_jsonb, spec_hash)
		VALUES ($1, $2, '{}'::jsonb, $3)
		RETURNING id`,
		projectID,
		pipelineSlug,
		"sha256:source-event-version",
	).Scan(&versionID)
	require.NoError(t, err)

	_, err = pool.Exec(
		ctx,
		`INSERT INTO pipeline_runs (
			project_id,
			project_environment_id,
			pipeline_version_id,
			pipeline_version_spec_hash,
			api_version,
			pipeline_slug,
			run_slug,
			source_environment_slug,
			status,
			duration_ms,
			step_count,
			step_slug,
			started_at,
			ended_at,
			timeline
		)
		VALUES ((SELECT project_id FROM project_environments WHERE id=$1), $1, $2, $3, 'v1', $4, $5, $6, $8::text, 10, 1, 'fetch-users', $7::timestamptz, CASE WHEN $8::text = 'running' THEN NULL ELSE $7::timestamptz END, '{}'::jsonb)`,
		projectEnvironmentID,
		versionID,
		"sha256:source-event-version",
		pipelineSlug,
		runSlug,
		environmentSlug,
		startedAt,
		status,
	)
	require.NoError(t, err)

	return projectEnvironmentID
}
