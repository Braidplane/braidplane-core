package e2e

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

// integrationPipeline returns a minimal valid pipeline spec for integration tests.
func integrationPipeline(pipelineSlug slug.Slug) *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: "Integration Test Pipeline",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-data"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("test-ds"),
					OperationType:  "sql",
					OperationRef:   must.Slug("list-items"),
				},
			},
		},
	}
}

// newIntegrationPipelineStore creates a pipelinesvc.PipelineStore backed by a real Postgres pool.
func newIntegrationPipelineStore(t *testing.T) pipelinesvc.PipelineStore {
	t.Helper()
	pool := openIntegrationPool(t)
	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	return store
}

func seedLifecycleIntegrationOrg(t *testing.T, pool *pgxpool.Pool, orgSlug slug.Slug, name string) {
	t.Helper()
	seedOrganization(t, pool, orgSlug, name)
	t.Cleanup(func() {
		ctx := context.Background()
		_, err := pool.Exec(ctx, `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, err)
	})
}

type lifecycleRepoStore struct {
	listFilesFn func(context.Context, string) ([]repostore.FileEntry, error)
	readFileFn  func(context.Context, string) ([]byte, error)
}

func (s *lifecycleRepoStore) ReadFile(ctx context.Context, path string, _ int64) ([]byte, error) {
	return s.readFileFn(ctx, path)
}

func (s *lifecycleRepoStore) WriteFile(context.Context, string, []byte, string) error {
	return nil
}

func (s *lifecycleRepoStore) DeleteFile(context.Context, string, string) error {
	return nil
}

func (s *lifecycleRepoStore) ListFiles(ctx context.Context, dir string) ([]repostore.FileEntry, error) {
	return s.listFilesFn(ctx, dir)
}

func (s *lifecycleRepoStore) FileExists(ctx context.Context, path string) (bool, error) {
	_, err := s.ReadFile(ctx, path, 0)
	if err != nil {
		return false, err
	}
	return true, nil
}

func TestPipelineStore_SpecLifecycle_Integration(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres pipeline store lifecycle integration test in short mode")
	}

	pool := openIntegrationPool(t)

	t.Run("ReconcileFromGit_OverwritesDrifted_ResetsToSynced", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		store := newIntegrationPipelineStore(t)
		orgSlug := must.Slug("lifecycle-integration-reset-synced")
		seedLifecycleIntegrationOrg(t, pool, orgSlug, "Lifecycle Integration Org Reset Synced")
		pipelineSlug := must.Slug("lifecycle-reset-synced")

		pipelineYAML := `metadata:
  slug: lifecycle-reset-synced
  name: Integration Test Pipeline
steps:
  - kind: query
    slug: fetch-data
    query:
      data_source_ref: test-ds
      operation_type: sql
      operation_ref: list-items
`
		gitRepoStore := &lifecycleRepoStore{
			listFilesFn: func(_ context.Context, _ string) ([]repostore.FileEntry, error) {
				return []repostore.FileEntry{
					{Path: "pipelines/lifecycle-reset-synced.pipeline.yaml"},
				}, nil
			},
			readFileFn: func(_ context.Context, _ string) ([]byte, error) {
				return []byte(pipelineYAML), nil
			},
		}

		// Initial git reconcile.
		sourceRef := "refs/heads/main"
		commitSHA := "0123456789abcdef0123456789abcdef01234567"
		_, err := store.ReconcileFromGit(ctx, orgSlug, defaultProjectSlug, gitRepoStore, pipelinesvc.ReconcileFromGitParams{
			PipelineDir:       "pipelines",
			SourceRef:         &sourceRef,
			LastAppliedCommit: &commitSHA,
		})
		require.NoError(t, err)

		driftedPipeline := integrationPipeline(pipelineSlug)
		driftedPipeline.Metadata.Name = "Drifted Pipeline"
		driftedSpecJSON := must.Fn(json.Marshal(driftedPipeline))
		_, err = pool.Exec(
			ctx,
			`UPDATE pipeline_specs
				SET name = $1, spec_jsonb = $2, spec_hash = $3, sync_status = 'drifted'
				WHERE project_id = (
					SELECT p.id
					FROM projects p
					JOIN organizations o ON p.organization_id = o.id
					WHERE o.slug = $4
						AND p.slug = $5
				)
					AND slug = $6`,
			driftedPipeline.Metadata.Name,
			driftedSpecJSON,
			"drifted-hash",
			orgSlug,
			defaultProjectSlug,
			pipelineSlug,
		)
		require.NoError(t, err)

		spec, err := store.FindSpec(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
		require.NoError(t, err)
		require.Equal(t, pipelinesrepo.SyncStatusDrifted, spec.SyncStatus)

		// Re-reconcile from git resets sync_status to synced.
		result, err := store.ReconcileFromGit(ctx, orgSlug, defaultProjectSlug, gitRepoStore, pipelinesvc.ReconcileFromGitParams{
			PipelineDir:       "pipelines",
			SourceRef:         &sourceRef,
			LastAppliedCommit: &commitSHA,
		})
		require.NoError(t, err)
		require.Equal(t, 1, result.ResetDrifted)
		require.Len(t, result.ResetDriftedPipelines, 1)
		require.Equal(t, pipelineSlug.String(), result.ResetDriftedPipelines[0].PipelineSlug)
		require.Equal(t, "drifted-hash", result.ResetDriftedPipelines[0].PreviousSpecHash)

		spec, err = store.FindSpec(ctx, orgSlug, defaultProjectSlug, pipelineSlug)
		require.NoError(t, err)
		require.Equal(t, pipelinesrepo.SyncStatusSynced, spec.SyncStatus)
	})
}

func TestPipelineStore_TenantIsolation_Integration(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip(
			"skipping real-Postgres pipeline store tenant isolation integration test in short mode",
		)
	}

	pool := openIntegrationPool(t)
	ctx := context.Background()

	orgASlug := must.Slug("isolation-org-a")
	orgBSlug := must.Slug("isolation-org-b")
	seedOrganization(t, pool, orgASlug, "Isolation Org A")
	seedOrganization(t, pool, orgBSlug, "Isolation Org B")
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, `DELETE FROM project_environments WHERE organization_id IN (SELECT id FROM organizations WHERE slug = $1 OR slug = $2)`, orgASlug, orgBSlug)
		require.NoError(t, err)
		_, err = pool.Exec(
			ctx,
			`DELETE FROM organizations WHERE slug = $1 OR slug = $2`,
			orgASlug,
			orgBSlug,
		)
		require.NoError(t, err)
	})

	store := newIntegrationPipelineStore(t)
	pipelineSlug := must.Slug("tenant-isolated-pipeline")

	// Materialize the pipeline under org A before running isolation sub-tests so that all
	// sub-tests start from a deterministic state regardless of execution order.
	pipeline := integrationPipeline(pipelineSlug)
	materializePipelines(t, ctx, store, orgASlug, defaultProjectSlug, pipeline)

	t.Run("OrgACanFindMaterializedSpec", func(t *testing.T) {
		t.Parallel()
		spec, err := store.FindSpec(ctx, orgASlug, defaultProjectSlug, pipelineSlug)
		require.NoError(t, err)
		require.Equal(t, pipelineSlug, spec.Slug)
	})

	t.Run("OrgBCannotFindOrgAPipelineSpec", func(t *testing.T) {
		t.Parallel()
		_, err := store.FindSpec(ctx, orgBSlug, defaultProjectSlug, pipelineSlug)
		require.ErrorIs(t, err, pipelinesvc.ErrPipelineNotFound)
	})

	t.Run("OrgBListReturnsEmptyForOrgAPipelines", func(t *testing.T) {
		t.Parallel()
		specs, err := store.List(ctx, orgBSlug, defaultProjectSlug, model.ListRequest{})
		require.NoError(t, err)
		for _, s := range specs {
			require.NotEqual(t, pipelineSlug, s.Slug,
				"org B must not see org A's pipeline %s in list", pipelineSlug)
		}
	})
}

func TestPipelineStore_List_ZeroRequestReturnsAllSpecsNewestFirst_Integration(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres pipeline store list integration test in short mode")
	}

	pool := openIntegrationPool(t)
	ctx := context.Background()

	orgSlug := must.Slug("list-zero-request-org")
	seedOrganization(t, pool, orgSlug, "List Zero Request Org")
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `DELETE FROM organizations WHERE slug = $1`, orgSlug)
		require.NoError(t, err)
	})

	store := newIntegrationPipelineStore(t)
	pipelineSlugs := []slug.Slug{
		must.Slug("list-zero-request-a"),
		must.Slug("list-zero-request-b"),
		must.Slug("list-zero-request-c"),
	}

	pipelines := make([]*pipelinespec.Pipeline, 0, len(pipelineSlugs))
	for _, pipelineSlug := range pipelineSlugs {
		pipelines = append(pipelines, integrationPipeline(pipelineSlug))
	}
	materializePipelines(t, ctx, store, orgSlug, defaultProjectSlug, pipelines...)

	specs, err := store.List(ctx, orgSlug, defaultProjectSlug, model.ListRequest{})
	require.NoError(t, err)
	require.Len(t, specs, len(pipelineSlugs))

	returnedSlugs := make([]string, 0, len(specs))
	for i, spec := range specs {
		returnedSlugs = append(returnedSlugs, spec.Slug.String())
		if i == len(specs)-1 {
			continue
		}

		nextSpec := specs[i+1]
		if spec.CreatedAt.Equal(nextSpec.CreatedAt) {
			require.Greater(t, spec.Slug.String(), nextSpec.Slug.String())
			continue
		}

		require.True(t, spec.CreatedAt.After(nextSpec.CreatedAt))
	}

	require.ElementsMatch(
		t,
		[]string{
			pipelineSlugs[0].String(),
			pipelineSlugs[1].String(),
			pipelineSlugs[2].String(),
		},
		returnedSlugs,
	)
}
