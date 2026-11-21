package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/runtriagesvc"
	"github.com/stretchr/testify/require"
)

// TestRunTriage_ReadsPersistedRunsThroughPipelineService runs the triage service
// against the real pipeline service and PostgreSQL repositories.
func TestRunTriage_ReadsPersistedRunsThroughPipelineService(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres run triage integration test in short mode")
	}

	ctx := context.Background()
	pool := openIntegrationPool(t)
	unique := time.Now().UnixNano()
	orgSlug := must.Slug(fmt.Sprintf("run-triage-org-%d", unique))
	otherOrgSlug := must.Slug(fmt.Sprintf("run-triage-other-org-%d", unique))
	prod := must.Slug("prod")
	seedOrganization(t, pool, orgSlug, "Run Triage Org")
	seedOrganization(t, pool, otherOrgSlug, "Run Triage Other Org")
	seedPipelineDeploymentEnvironment(t, pool, orgSlug, defaultProjectSlug, prod)
	seedPipelineDeploymentEnvironment(t, pool, otherOrgSlug, defaultProjectSlug, prod)
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, `DELETE FROM project_environments WHERE organization_id IN (SELECT id FROM organizations WHERE slug = $1 OR slug = $2)`, orgSlug, otherOrgSlug)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, `DELETE FROM organizations WHERE slug = $1 OR slug = $2`, orgSlug, otherOrgSlug)
		require.NoError(t, err)
	})

	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	failingPipeline := softDeleteIntegrationPipeline(must.Slug("customer-enrichment"))
	healthyPipeline := softDeleteIntegrationPipeline(must.Slug("orders-daily-sync"))
	materializePipelines(t, ctx, store, orgSlug, defaultProjectSlug, failingPipeline, healthyPipeline)
	runStore := pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New())
	svc, err := pipelinesvc.New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		store, pool, txm, pipelineruns.New(), incrementalcheckpoints.New(), runStore, driftimpactrepo.New(),
		noopQueryExecutor{}, allowAuthorizationService{}, &fakes.AdmissionService{}, &fakes.DataSourceService{},
		&fakes.ProjectsService{}, &ownershipGitSourceResolver{}, clock.New(), noopPipelineRunProducer{},
		noopPipelineGitProducer{}, eventtest.NewProducer(nil), noopPipelineRuntimeResolver{},
		testSinkEffectLedger(t, txm), newTestProofLinks(), testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)

	base := time.Date(2026, time.November, 18, 8, 0, 0, 0, time.UTC)
	persist := func(pipeline *pipelinespec.Pipeline, run, status string, at time.Time, failure *pipelinerun.FailureContext) {
		t.Helper()
		spec, err := store.FindSpec(ctx, orgSlug, defaultProjectSlug, pipeline.Metadata.Slug)
		require.NoError(t, err)
		require.NotNil(t, spec.CurrentVersionID)
		require.NoError(t, runStore.PersistRun(ctx, orgSlug, defaultProjectSlug, prod, pipeline, &pipelinerun.RunResult{
			Metadata: pipelinerun.RunMetadata{
				EnvironmentSlug: prod, RunSlug: run, Status: status, DurationMs: 1800,
				StepCount: len(pipeline.Steps), PipelineVersionID: *spec.CurrentVersionID,
			},
			Timeline: pipelinerun.RunTimeline{
				RunSlug: run, PipelineSlug: pipeline.Metadata.Slug, EnvironmentSlug: prod, Status: status, Failure: failure,
				Events: []pipelinerun.TimelineEvent{
					{At: at, Type: "run_started", Status: "running"},
					{At: at.Add(2 * time.Second), Type: "run_completed", Status: status},
				},
			},
		}))
	}
	failure := func(msg string) *pipelinerun.FailureContext {
		return &pipelinerun.FailureContext{
			Step: must.Slug("fetch-users"), Error: msg, At: base, Kind: "data_source", Side: "source",
			ReasonCode: pipelinerun.FailureReasonCodeSourceQueryFailed, ActionHint: "Check that the source table exists.",
		}
	}
	persist(failingPipeline, "ce-r1", "succeeded", base, nil)
	persist(failingPipeline, "ce-r2", "failed", base.Add(24*time.Hour), failure("relation crm.customers does not exist (older)"))
	persist(failingPipeline, "ce-r3", "failed", base.Add(48*time.Hour), failure("relation crm.customers does not exist"))
	persist(healthyPipeline, "od-r1", "failed", base, failure("timeout"))
	persist(healthyPipeline, "od-r2", "succeeded", base.Add(48*time.Hour), nil)

	triage := runtriagesvc.New(svc)
	report, err := triage.Triage(ctx, runtriagesvc.Params{Organization: orgSlug, Project: defaultProjectSlug, Environment: prod})
	require.NoError(t, err)

	require.Equal(t, 5, report.RunsInspected)
	require.Equal(t, 2, report.PipelinesInspected)
	require.Len(t, report.Failing, 1)
	f := report.Failing[0]
	require.Equal(t, "customer-enrichment", f.Pipeline)
	require.Equal(t, "ce-r3", f.LatestRun)
	require.Equal(t, 2, f.FailedStreak)
	require.Equal(t, 2, f.FailedInWindow)
	require.NotNil(t, f.LastSuccess)
	require.Equal(t, "ce-r1", f.LastSuccess.RunSlug)
	require.NotNil(t, f.Failure)
	require.Equal(t, "fetch-users", f.Failure.Step)
	require.Equal(t, "relation crm.customers does not exist", f.Failure.Error)
	require.Equal(t, "source_query_failed", f.Failure.ReasonCode)
	require.Equal(t, "Check that the source table exists.", f.Failure.ActionHint)
	require.False(t, f.DetailsUnavailable)
	require.Len(t, report.Recovered, 1)
	require.Equal(t, "orders-daily-sync", report.Recovered[0].Pipeline)
	require.Equal(t, "od-r2", report.Recovered[0].LatestRun)

	onlyHealthy := healthyPipeline.Metadata.Slug
	filtered, err := triage.Triage(ctx, runtriagesvc.Params{Organization: orgSlug, Project: defaultProjectSlug, Environment: prod, Pipeline: &onlyHealthy})
	require.NoError(t, err)
	require.Empty(t, filtered.Failing)
	require.Equal(t, 2, filtered.RunsInspected)

	otherOrg, err := triage.Triage(ctx, runtriagesvc.Params{Organization: otherOrgSlug, Project: defaultProjectSlug, Environment: prod})
	require.NoError(t, err)
	require.Zero(t, otherOrg.RunsInspected, "triage never crosses organizations")
	require.Empty(t, otherOrg.Failing)
}
