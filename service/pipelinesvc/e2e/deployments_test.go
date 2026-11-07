package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"

	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"

	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/postgres"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

func TestService_PipelineDeploymentsAreEnvironmentScoped_Integration(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres pipeline deployment integration test in short mode")
	}

	ctx := context.Background()
	pool := openIntegrationPool(t)
	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	svc := newPipelineDeploymentIntegrationService(t, pool, txm, store)

	unique := time.Now().UnixNano()
	orgASlug := must.Slug(fmt.Sprintf("pipeline-deployments-org-a-%d", unique))
	orgBSlug := must.Slug(fmt.Sprintf("pipeline-deployments-org-b-%d", unique))
	memberSlug := must.Slug(fmt.Sprintf("pipeline-deployments-user-%d", unique))
	seedPipelineDeploymentOrg(t, pool, orgASlug)
	seedPipelineDeploymentOrg(t, pool, orgBSlug)
	seedPipelineDeploymentEnvironment(t, pool, orgASlug, defaultProjectSlug, must.Slug("dev"))
	seedPipelineDeploymentEnvironment(t, pool, orgASlug, defaultProjectSlug, must.Slug("prod"))
	seedPipelineDeploymentEnvironment(t, pool, orgBSlug, defaultProjectSlug, must.Slug("dev"))

	pipelineSlug := must.Slug("environment-scoped-sync")
	v1 := pipelineDeploymentIntegrationPipeline(pipelineSlug, "Initial Pipeline")
	materializePipelines(t, ctx, store, orgASlug, defaultProjectSlug, v1)
	v1Spec, err := store.FindSpec(ctx, orgASlug, defaultProjectSlug, pipelineSlug)
	require.NoError(t, err)
	require.NotNil(t, v1Spec.CurrentVersionSpecHash)

	v2 := pipelineDeploymentIntegrationPipeline(pipelineSlug, "Updated Pipeline")
	materializePipelines(t, ctx, store, orgASlug, defaultProjectSlug, v2)
	v2Spec, err := store.FindSpec(ctx, orgASlug, defaultProjectSlug, pipelineSlug)
	require.NoError(t, err)
	require.NotNil(t, v2Spec.CurrentVersionSpecHash)
	require.NotEqual(t, *v1Spec.CurrentVersionSpecHash, *v2Spec.CurrentVersionSpecHash)

	actor := model.UserEventActor(memberSlug)
	devDeployment, err := svc.Deploy(
		ctx,
		orgASlug,
		defaultProjectSlug,
		must.Slug("dev"),
		pipelineSlug,
		actor,
		pipelinesvc.DeployPipelineRequest{SpecHash: *v1Spec.CurrentVersionSpecHash},
	)
	require.NoError(t, err)
	require.Equal(t, *v1Spec.CurrentVersionSpecHash, devDeployment.CurrentVersionSpecHash)

	prodDeployment, err := svc.Deploy(
		ctx,
		orgASlug,
		defaultProjectSlug,
		must.Slug("prod"),
		pipelineSlug,
		actor,
		pipelinesvc.DeployPipelineRequest{SpecHash: *v2Spec.CurrentVersionSpecHash},
	)
	require.NoError(t, err)
	require.Equal(t, *v2Spec.CurrentVersionSpecHash, prodDeployment.CurrentVersionSpecHash)

	devDeployment, err = svc.GetDeployment(ctx, orgASlug, defaultProjectSlug, must.Slug("dev"), pipelineSlug)
	require.NoError(t, err)
	require.Equal(t, *v1Spec.CurrentVersionSpecHash, devDeployment.CurrentVersionSpecHash)
	prodDeployment, err = svc.GetDeployment(ctx, orgASlug, defaultProjectSlug, must.Slug("prod"), pipelineSlug)
	require.NoError(t, err)
	require.Equal(t, *v2Spec.CurrentVersionSpecHash, prodDeployment.CurrentVersionSpecHash)

	devDeployments, err := svc.ListDeployments(ctx, orgASlug, defaultProjectSlug, must.Slug("dev"))
	require.NoError(t, err)
	require.Len(t, devDeployments, 1)
	require.Equal(t, pipelineSlug, devDeployments[0].PipelineSlug)

	missingDeployments, err := svc.ListDeployments(ctx, orgASlug, defaultProjectSlug, must.Slug("missing"))
	require.ErrorIs(t, err, pipelinesvc.ErrProjectEnvironmentNotFound)
	require.Nil(t, missingDeployments)

	expectedDevSpecHash := (*v1Spec.CurrentVersionSpecHash)[:12]
	devDeployment, err = svc.Deploy(
		ctx,
		orgASlug,
		defaultProjectSlug,
		must.Slug("dev"),
		pipelineSlug,
		actor,
		pipelinesvc.DeployPipelineRequest{
			SpecHash:                *v2Spec.CurrentVersionSpecHash,
			ExpectedCurrentSpecHash: &expectedDevSpecHash,
		},
	)
	require.NoError(t, err)
	require.Equal(t, *v2Spec.CurrentVersionSpecHash, devDeployment.CurrentVersionSpecHash)
	require.NotNil(t, devDeployment.PreviousVersionSpecHash)
	require.Equal(t, *v1Spec.CurrentVersionSpecHash, *devDeployment.PreviousVersionSpecHash)

	_, err = svc.Deploy(
		ctx,
		orgASlug,
		defaultProjectSlug,
		must.Slug("prod"),
		pipelineSlug,
		actor,
		pipelinesvc.DeployPipelineRequest{
			SpecHash:                *v1Spec.CurrentVersionSpecHash,
			ExpectedCurrentSpecHash: &expectedDevSpecHash,
		},
	)
	require.ErrorIs(t, err, pipelinesvc.ErrPipelineDeploymentConflict)

	_, err = svc.GetDeployment(ctx, orgBSlug, defaultProjectSlug, must.Slug("dev"), pipelineSlug)
	require.ErrorIs(t, err, pipelinesvc.ErrPipelineDeploymentNotFound)
}

func newPipelineDeploymentIntegrationService(
	t *testing.T,
	pool *pgxpool.Pool,
	txm postgres.TransactionManager,
	store pipelinesvc.PipelineStore,
) pipelinesvc.Service {
	t.Helper()

	svc, err := pipelinesvc.New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		store,
		pool,
		txm,
		pipelineruns.New(), incrementalcheckpoints.New(),
		pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		driftimpactrepo.New(),
		noopQueryExecutor{},
		allowAuthorizationService{},
		&fakes.AdmissionService{},
		&fakes.DataSourceService{},
		&fakes.ProjectsService{},
		&ownershipGitSourceResolver{},
		clock.New(),
		noopPipelineRunProducer{},
		noopPipelineGitProducer{},
		eventtest.NewProducer(nil),
		noopPipelineRuntimeResolver{},
		testSinkEffectLedger(t, txm),
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)
	return svc
}

func seedPipelineDeploymentOrg(t *testing.T, pool *pgxpool.Pool, orgSlug slug.Slug) {
	t.Helper()

	seedOrganization(t, pool, orgSlug, "Pipeline Deployment Org")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()

		_, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM projects WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug.String())
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug.String())
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, orgSlug.String())
		require.NoError(t, cleanupErr)
	})
}

func seedPipelineDeploymentEnvironment(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, `
		WITH org_row AS (
			SELECT id AS organization_id FROM organizations WHERE slug = $1
		),
		project_row AS (
			SELECT p.id AS project_id, org_row.organization_id
			FROM projects p
			JOIN org_row ON p.organization_id = org_row.organization_id
			WHERE p.slug = $2
		),
		environment_row AS (
			INSERT INTO environments (organization_id, slug, name)
			SELECT organization_id, $3, $4 FROM org_row
			ON CONFLICT (organization_id, slug) DO UPDATE SET deleted_at = NULL
			RETURNING id AS environment_id
		)
		INSERT INTO project_environments (organization_id, project_id, environment_id, is_default)
		SELECT project_row.organization_id, project_id, environment_id, false
		FROM project_row, environment_row
		ON CONFLICT (project_id, environment_id) DO NOTHING
	`, orgSlug.String(), projectSlug.String(), environmentSlug.String(), environmentSlug.String())
	require.NoError(t, err)
}

func pipelineDeploymentIntegrationPipeline(
	pipelineSlug slug.Slug,
	name string,
) *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: pipelineSlug,
			Name: name,
		},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: must.Slug("fetch-users"),
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: must.Slug("acme-postgres"),
				OperationType:  "sql",
				OperationRef:   must.Slug("list-users"),
			},
		}},
	}
}

func TestDeploymentRevisionRejectsConcurrentSameSpecResolutions(t *testing.T) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	ctx := t.Context()
	pool := openIntegrationPool(t)
	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	org := slug.Slug(fmt.Sprintf("resolution-cas-%d", time.Now().UnixNano()))
	seedPipelineDeploymentOrg(t, pool, org)
	seedPipelineDeploymentEnvironment(t, pool, org, defaultProjectSlug, "dev")
	pipeline := pipelineDeploymentIntegrationPipeline("sync", "Sync")
	materializePipelines(t, ctx, store, org, defaultProjectSlug, pipeline)
	spec, err := store.FindSpec(ctx, org, defaultProjectSlug, "sync")
	require.NoError(t, err)
	version, err := store.FindVersionBySpecHash(ctx, org, defaultProjectSlug, "sync", *spec.CurrentVersionSpecHash)
	require.NoError(t, err)
	authored, err := json.Marshal(pipeline)
	require.NoError(t, err)
	raw, compiled, err := contractplan.Compile(contractplan.Input{Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/sync.json"}, Authored: authored, Operations: []contractplan.Operation{{Step: "fetch-users", Role: "query", DataSource: "acme-postgres", Slug: "list-users", Revision: "v1", Content: json.RawMessage(`{"data":{"statement":"{\"sql\":\"SELECT 1\"}"},"parameters":[]}`)}}})
	require.NoError(t, err)
	_, err = compiledplans.NewRepository().Create(ctx, pool, org, defaultProjectSlug, compiledplans.CreateParams{PipelineSlug: "sync", Digest: compiled, AuthoredSpecHash: version.SpecHash, FormatVersion: contractplan.FormatVersion, Materialization: raw})
	require.NoError(t, err)
	ids := make([]string, 3)
	for i := range ids {
		plan := deploymentplan.Plan{Organization: org, Project: defaultProjectSlug, Environment: "dev", TemplateDigest: compiled, CompiledDigest: compiled, CompiledPlan: raw, Pipeline: pipeline, Targets: []deploymentplan.Target{{Step: "fetch-users", Role: "query", Reference: "acme-postgres", DataSource: "acme-postgres", ConfigDigest: "sha256:" + strings.Repeat("b", 64), BindingRevision: fmt.Sprint(i)}}}
		materialization, id, err := deploymentplan.Encode(plan)
		require.NoError(t, err)
		ids[i] = id
		_, err = deploymentresolutions.NewRepository().Create(ctx, pool, org, defaultProjectSlug, "dev", "sync", compiled, id, materialization)
		require.NoError(t, err)
	}
	repo := pipelinesrepo.New()
	zero := int64(0)
	initial, err := repo.Deploy(ctx, pool, org, defaultProjectSlug, "dev", pipelinesrepo.DeployParams{PipelineSlug: "sync", VersionID: version.ID, ExpectedRevision: &zero, ResolutionDigest: &ids[0], DeployedByActorType: "system"})
	require.NoError(t, err)
	require.EqualValues(t, 1, initial.Revision)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range ids[1:] {
		go func(id string) {
			<-start
			results <- txm.RunInTx(postgres.WithConsistentSnapshot(ctx), func(ctx context.Context, db postgres.DBTX) error {
				_, err := repo.Deploy(ctx, db, org, defaultProjectSlug, "dev", pipelinesrepo.DeployParams{PipelineSlug: "sync", VersionID: version.ID, ExpectedRevision: &initial.Revision, ResolutionDigest: &id, DeployedByActorType: "system"})
				return err
			})
		}(id)
	}
	close(start)
	won, lost := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			won++
		} else {
			require.True(t, xerror.Is(err, pipelinesrepo.ErrPipelineDeploymentNotFound) || xerror.Is(err, pipelinesrepo.ErrPipelineDeploymentConflict), err)
			lost++
		}
	}
	require.Equal(t, 1, won)
	require.Equal(t, 1, lost)
	current, err := repo.FindDeployment(ctx, pool, org, defaultProjectSlug, "dev", "sync")
	require.NoError(t, err)
	require.EqualValues(t, 2, current.Revision)
	require.Equal(t, version.SpecHash, current.CurrentVersionSpecHash)
	require.Equal(t, &ids[0], current.PreviousResolutionDigest)
	require.Contains(t, ids[1:], *current.CurrentResolutionDigest)
	_, err = repo.Deploy(ctx, pool, org, defaultProjectSlug, "dev", pipelinesrepo.DeployParams{PipelineSlug: "sync", VersionID: version.ID, ExpectedRevision: &initial.Revision, ResolutionDigest: &ids[0], DeployedByActorType: "system"})
	require.ErrorIs(t, err, pipelinesrepo.ErrPipelineDeploymentNotFound)
	_, err = repo.Deploy(ctx, pool, org, defaultProjectSlug, "dev", pipelinesrepo.DeployParams{PipelineSlug: "sync", VersionID: version.ID, ExpectedRevision: &current.Revision, DeployedByActorType: "system"})
	require.ErrorIs(t, err, pipelinesrepo.ErrPipelineDeploymentNotFound, "same-version selection cannot discard the pinned plan")
	unchanged, err := repo.FindDeployment(ctx, pool, org, defaultProjectSlug, "dev", "sync")
	require.NoError(t, err)
	require.Equal(t, current.Revision, unchanged.Revision)
	require.Equal(t, current.CurrentResolutionDigest, unchanged.CurrentResolutionDigest)

	_, err = deploymentresolutions.NewRepository().Find(ctx, pool, "other", defaultProjectSlug, "dev", "sync", ids[0])
	require.ErrorIs(t, err, deploymentresolutions.ErrNotFound)
}
