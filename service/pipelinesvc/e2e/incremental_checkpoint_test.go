package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"

	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"

	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
	"github.com/Braidplane/braidplane-core/postgres/members"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/postgres/roles"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/memberssvc"
	"github.com/Braidplane/braidplane-core/service/orgsvc"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

const (
	incrementalCheckpointProjectSlug     = slug.Slug("checkpoint-project")
	incrementalCheckpointEnvironmentSlug = model.DefaultDevelopmentEnvironmentSlug
)

func e2eRunSource(actor model.EventActor) pipelinerun.RunSourceSummary {
	return pipelinerun.RunSourceSummary{
		Type:          model.PipelineRunSourceTypeManual,
		Summary:       "Manual run",
		Entrypoint:    model.PipelineRunSourceEntrypointUI,
		ActorType:     actor.Type,
		ActorSlug:     actor.Slug,
		ParameterKeys: []string{},
	}
}

func TestPipelineServiceExecuteCommitsIncrementalCheckpointAfterSinkSuccess(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres incremental checkpoint e2e test in short mode")
	}

	ctx := context.Background()
	pool := openE2EPool(t)
	orgSlug := must.Slug(fmt.Sprintf("incremental-service-commit-org-%d", time.Now().UnixNano()))
	memberSlug := createIncrementalCheckpointServiceOrgProject(t, pool, orgSlug)

	executor := &incrementalCheckpointServiceQueryExecutor{
		sourceObservations: []json.RawMessage{
			json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`),
			json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`),
		},
	}
	svc, store := newIncrementalCheckpointService(t, pool, executor, eventtest.NewProducer(nil), noopPipelineRunProducer{})
	pipeline := incrementalCheckpointServicePipeline(must.Slug("users-sync"))
	createAndDeployIncrementalCheckpointPipeline(t, ctx, svc, store, orgSlug, memberSlug, pipeline)
	actor := model.UserEventActor(memberSlug)

	firstRun, err := svc.Execute(ctx, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("run-first"), nil, actor, e2eRunSource(actor), nil)
	require.NoError(t, err)
	require.Equal(t, "succeeded", firstRun.Metadata.Status)
	require.Len(t, executor.checkpointInputs, 1)
	require.Nil(t, executor.checkpointInputs[0].LowerBound)

	secondRun, err := svc.Execute(ctx, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("run-second"), nil, actor, e2eRunSource(actor), nil)
	require.NoError(t, err)
	require.Equal(t, "succeeded", secondRun.Metadata.Status)
	require.Len(t, executor.checkpointInputs, 2)
	require.JSONEq(t, `{"value":"2026-05-24T10:00:00Z"}`, string(executor.checkpointInputs[1].LowerBound))

	upperBound, status := currentIncrementalCheckpoint(
		t,
		pool,
		orgSlug,
		incrementalCheckpointProjectSlug,
		incrementalCheckpointEnvironmentSlug,
		pipeline.Metadata.Slug,
	)
	require.Equal(t, "committed", status)
	require.JSONEq(t, `{"value":"2026-05-24T11:00:00Z"}`, string(upperBound))
	require.Equal(
		t,
		1,
		countIncrementalCheckpointsByStatus(
			t,
			pool,
			orgSlug,
			incrementalCheckpointProjectSlug,
			incrementalCheckpointEnvironmentSlug,
			pipeline.Metadata.Slug,
			"superseded",
		),
	)
}

func TestPipelineServiceExecuteBlocksIncrementalObservationWhenDependentSinkFails(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres incremental checkpoint e2e test in short mode")
	}

	ctx := context.Background()
	pool := openE2EPool(t)
	orgSlug := must.Slug(fmt.Sprintf("incremental-service-block-org-%d", time.Now().UnixNano()))
	memberSlug := createIncrementalCheckpointServiceOrgProject(t, pool, orgSlug)

	executor := &incrementalCheckpointServiceQueryExecutor{
		sourceObservations: []json.RawMessage{
			json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`),
			json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`),
		},
		failSinkOnCall: 2,
	}
	svc, store := newIncrementalCheckpointService(t, pool, executor, eventtest.NewProducer(nil), noopPipelineRunProducer{})
	pipeline := incrementalCheckpointServicePipeline(must.Slug("users-sync-blocked"))
	createAndDeployIncrementalCheckpointPipeline(t, ctx, svc, store, orgSlug, memberSlug, pipeline)
	actor := model.UserEventActor(memberSlug)

	_, err := svc.Execute(ctx, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("run-committed"), nil, actor, e2eRunSource(actor), nil)
	require.NoError(t, err)

	failedRun, err := svc.Execute(ctx, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("run-blocked"), nil, actor, e2eRunSource(actor), nil)
	require.Error(t, err)
	require.NotNil(t, failedRun)
	require.Equal(t, "failed", failedRun.Metadata.Status)

	upperBound, status := currentIncrementalCheckpoint(
		t,
		pool,
		orgSlug,
		incrementalCheckpointProjectSlug,
		incrementalCheckpointEnvironmentSlug,
		pipeline.Metadata.Slug,
	)
	require.Equal(t, "committed", status)
	require.JSONEq(t, `{"value":"2026-05-24T10:00:00Z"}`, string(upperBound))

	require.Equal(
		t,
		1,
		countIncrementalCheckpointsByStatus(
			t,
			pool,
			orgSlug,
			incrementalCheckpointProjectSlug,
			incrementalCheckpointEnvironmentSlug,
			pipeline.Metadata.Slug,
			"blocked",
		),
	)
	blockedUpperBound, blockedReason := blockedIncrementalCheckpoint(
		t,
		pool,
		orgSlug,
		incrementalCheckpointProjectSlug,
		incrementalCheckpointEnvironmentSlug,
		pipeline.Metadata.Slug,
		"run-blocked",
	)
	require.JSONEq(t, `{"value":"2026-05-24T11:00:00Z"}`, string(blockedUpperBound))
	require.Contains(t, blockedReason, "sink write failed")
}

func TestPipelineServiceExecuteConcurrentIncrementalCommitsDoNotRollbackRun(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres incremental checkpoint e2e test in short mode")
	}

	ctx := context.Background()
	pool := openE2EPool(t)
	orgSlug := must.Slug(fmt.Sprintf("incremental-service-concurrent-org-%d", time.Now().UnixNano()))
	memberSlug := createIncrementalCheckpointServiceOrgProject(t, pool, orgSlug)

	executor := &incrementalCheckpointServiceQueryExecutor{
		sourceObservations: []json.RawMessage{
			json.RawMessage(`{"value":"seed"}`),
			json.RawMessage(`{"value":"concurrent-a"}`),
			json.RawMessage(`{"value":"concurrent-b"}`),
		},
	}
	svc, store := newIncrementalCheckpointService(t, pool, executor, eventtest.NewProducer(nil), noopPipelineRunProducer{})
	pipeline := incrementalCheckpointServicePipeline(must.Slug("users-sync-concurrent"))
	createAndDeployIncrementalCheckpointPipeline(t, ctx, svc, store, orgSlug, memberSlug, pipeline)
	actor := model.UserEventActor(memberSlug)

	_, err := svc.Execute(ctx, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("run-seed"), nil, actor, e2eRunSource(actor), nil)
	require.NoError(t, err)
	executor.sinkBarrier = newIncrementalCheckpointTwoPartyBarrier()

	runErrors := make(chan error, 2)
	for _, runSlug := range []string{"run-concurrent-a", "run-concurrent-b"} {
		go func(runSlug string) {
			run, runErr := svc.Execute(ctx, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug(runSlug), nil, actor, e2eRunSource(actor), nil)
			if runErr == nil && run.Metadata.Status != "succeeded" {
				runErr = xerror.Unexpected().Detailf("run %s finished with status %s", runSlug, run.Metadata.Status)
			}
			runErrors <- runErr
		}(runSlug)
	}

	for range 2 {
		require.NoError(t, <-runErrors)
	}

	upperBound, status := currentIncrementalCheckpoint(
		t,
		pool,
		orgSlug,
		incrementalCheckpointProjectSlug,
		incrementalCheckpointEnvironmentSlug,
		pipeline.Metadata.Slug,
	)
	require.Equal(t, "committed", status)
	var currentUpperBound map[string]string
	require.NoError(t, json.Unmarshal(upperBound, &currentUpperBound))
	require.Contains(t, []string{"concurrent-a", "concurrent-b"}, currentUpperBound["value"])
	require.Equal(
		t,
		2,
		countIncrementalCheckpointsByStatus(
			t,
			pool,
			orgSlug,
			incrementalCheckpointProjectSlug,
			incrementalCheckpointEnvironmentSlug,
			pipeline.Metadata.Slug,
			"superseded",
		),
	)
}

func TestPipelineServiceExecuteScopesIncrementalCheckpointByProject(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres incremental checkpoint e2e test in short mode")
	}

	ctx := context.Background()
	pool := openE2EPool(t)
	orgASlug := must.Slug(fmt.Sprintf("incremental-service-scope-a-org-%d", time.Now().UnixNano()))
	orgBSlug := must.Slug(fmt.Sprintf("incremental-service-scope-b-org-%d", time.Now().UnixNano()))
	orgAMemberSlug := createIncrementalCheckpointServiceOrgProject(t, pool, orgASlug)
	orgBMemberSlug := createIncrementalCheckpointServiceOrgProject(t, pool, orgBSlug)

	executor := &incrementalCheckpointServiceQueryExecutor{
		sourceObservations: []json.RawMessage{
			json.RawMessage(`{"value":"org-a"}`),
			json.RawMessage(`{"value":"org-b"}`),
		},
	}
	svc, store := newIncrementalCheckpointService(t, pool, executor, eventtest.NewProducer(nil), noopPipelineRunProducer{})
	pipeline := incrementalCheckpointServicePipeline(must.Slug("users-sync-scoped"))
	createAndDeployIncrementalCheckpointPipeline(t, ctx, svc, store, orgASlug, orgAMemberSlug, pipeline)
	createAndDeployIncrementalCheckpointPipeline(t, ctx, svc, store, orgBSlug, orgBMemberSlug, pipeline)
	orgAActor := model.UserEventActor(orgAMemberSlug)
	orgBActor := model.UserEventActor(orgBMemberSlug)

	_, err := svc.Execute(ctx, orgASlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("run-org-a"), nil, orgAActor, e2eRunSource(orgAActor), nil)
	require.NoError(t, err)
	_, err = svc.Execute(ctx, orgBSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("run-org-b"), nil, orgBActor, e2eRunSource(orgBActor), nil)
	require.NoError(t, err)

	require.Len(t, executor.checkpointInputs, 2)
	require.Nil(t, executor.checkpointInputs[0].LowerBound)
	require.Nil(t, executor.checkpointInputs[1].LowerBound)

	orgAUpperBound, orgAStatus := currentIncrementalCheckpoint(
		t,
		pool,
		orgASlug,
		incrementalCheckpointProjectSlug,
		incrementalCheckpointEnvironmentSlug,
		pipeline.Metadata.Slug,
	)
	orgBUpperBound, orgBStatus := currentIncrementalCheckpoint(
		t,
		pool,
		orgBSlug,
		incrementalCheckpointProjectSlug,
		incrementalCheckpointEnvironmentSlug,
		pipeline.Metadata.Slug,
	)
	require.Equal(t, "committed", orgAStatus)
	require.Equal(t, "committed", orgBStatus)
	require.JSONEq(t, `{"value":"org-a"}`, string(orgAUpperBound))
	require.JSONEq(t, `{"value":"org-b"}`, string(orgBUpperBound))
}

func TestPipelineServiceExecuteScopesIncrementalCheckpointByEnvironment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres incremental checkpoint e2e test in short mode")
	}

	ctx := context.Background()
	pool := openE2EPool(t)
	orgSlug := must.Slug(fmt.Sprintf("incremental-service-env-scope-org-%d", time.Now().UnixNano()))
	memberSlug := createIncrementalCheckpointServiceOrgProject(t, pool, orgSlug)
	prodEnvironmentSlug := model.DefaultProductionEnvironmentSlug
	enableIncrementalCheckpointProjectEnvironment(t, pool, orgSlug, memberSlug, prodEnvironmentSlug)

	executor := &incrementalCheckpointServiceQueryExecutor{
		sourceObservations: []json.RawMessage{
			json.RawMessage(`{"value":"dev-first"}`),
			json.RawMessage(`{"value":"prod-first"}`),
			json.RawMessage(`{"value":"dev-second"}`),
		},
	}
	svc, store := newIncrementalCheckpointService(t, pool, executor, eventtest.NewProducer(nil), noopPipelineRunProducer{})
	pipeline := incrementalCheckpointServicePipeline(must.Slug("users-sync-env-scoped"))
	specHash := createAndDeployIncrementalCheckpointPipeline(t, ctx, svc, store, orgSlug, memberSlug, pipeline)
	deployIncrementalCheckpointPipeline(
		t,
		ctx,
		svc,
		orgSlug,
		prodEnvironmentSlug,
		pipeline.Metadata.Slug,
		specHash,
	)
	actor := model.UserEventActor(memberSlug)

	_, err := svc.Execute(ctx, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("run-dev-first"), nil, actor, e2eRunSource(actor), nil)
	require.NoError(t, err)
	_, err = svc.Execute(ctx, orgSlug, incrementalCheckpointProjectSlug, prodEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("run-prod-first"), &prodEnvironmentSlug, actor, e2eRunSource(actor), nil)
	require.NoError(t, err)
	_, err = svc.Execute(ctx, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("run-dev-second"), nil, actor, e2eRunSource(actor), nil)
	require.NoError(t, err)

	require.Len(t, executor.checkpointInputs, 3)
	require.Nil(t, executor.checkpointInputs[0].LowerBound)
	require.Nil(t, executor.checkpointInputs[1].LowerBound)
	require.JSONEq(t, `{"value":"dev-first"}`, string(executor.checkpointInputs[2].LowerBound))

	devUpperBound, devStatus := currentIncrementalCheckpoint(
		t,
		pool,
		orgSlug,
		incrementalCheckpointProjectSlug,
		incrementalCheckpointEnvironmentSlug,
		pipeline.Metadata.Slug,
	)
	prodUpperBound, prodStatus := currentIncrementalCheckpoint(
		t,
		pool,
		orgSlug,
		incrementalCheckpointProjectSlug,
		prodEnvironmentSlug,
		pipeline.Metadata.Slug,
	)
	require.Equal(t, "committed", devStatus)
	require.Equal(t, "committed", prodStatus)
	require.JSONEq(t, `{"value":"dev-second"}`, string(devUpperBound))
	require.JSONEq(t, `{"value":"prod-first"}`, string(prodUpperBound))
	require.Equal(
		t,
		1,
		countIncrementalCheckpointsByStatus(
			t,
			pool,
			orgSlug,
			incrementalCheckpointProjectSlug,
			incrementalCheckpointEnvironmentSlug,
			pipeline.Metadata.Slug,
			"superseded",
		),
	)
	require.Equal(
		t,
		0,
		countIncrementalCheckpointsByStatus(
			t,
			pool,
			orgSlug,
			incrementalCheckpointProjectSlug,
			prodEnvironmentSlug,
			pipeline.Metadata.Slug,
			"superseded",
		),
	)
}

func TestPipelineGitSourceRemovalAndRematerializationPreserveIncrementalCheckpoints(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping real-Postgres incremental checkpoint e2e test in short mode")
	}

	ctx := context.Background()
	pool := openE2EPool(t)
	orgSlug := must.Slug(fmt.Sprintf("incremental-service-lifecycle-org-%d", time.Now().UnixNano()))
	memberSlug := createIncrementalCheckpointServiceOrgProject(t, pool, orgSlug)

	executor := &incrementalCheckpointServiceQueryExecutor{
		sourceObservations: []json.RawMessage{json.RawMessage(`{"cursor":"cursor-1"}`)},
	}
	svc, store := newIncrementalCheckpointService(t, pool, executor, eventtest.NewProducer(nil), noopPipelineRunProducer{})
	pipeline := incrementalCheckpointServicePipeline(must.Slug("users-sync-lifecycle"))
	createAndDeployIncrementalCheckpointPipeline(t, ctx, svc, store, orgSlug, memberSlug, pipeline)
	actor := model.UserEventActor(memberSlug)
	_, err := svc.Execute(ctx, orgSlug, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("run-lifecycle"), nil, actor, e2eRunSource(actor), nil)
	require.NoError(t, err)

	require.Equal(
		t,
		1,
		countActiveCurrentIncrementalCheckpoints(
			t,
			pool,
			orgSlug,
			incrementalCheckpointProjectSlug,
			incrementalCheckpointEnvironmentSlug,
			pipeline.Metadata.Slug,
		),
	)
	materializePipelines(t, ctx, store, orgSlug, incrementalCheckpointProjectSlug)
	require.Equal(
		t,
		0,
		countActiveCurrentIncrementalCheckpoints(
			t,
			pool,
			orgSlug,
			incrementalCheckpointProjectSlug,
			incrementalCheckpointEnvironmentSlug,
			pipeline.Metadata.Slug,
		),
	)
	require.Equal(
		t,
		1,
		countIncrementalCheckpointsByStatus(
			t,
			pool,
			orgSlug,
			incrementalCheckpointProjectSlug,
			incrementalCheckpointEnvironmentSlug,
			pipeline.Metadata.Slug,
			"committed",
		),
	)

	materializePipelines(t, ctx, store, orgSlug, incrementalCheckpointProjectSlug, pipeline)
	require.Equal(
		t,
		1,
		countActiveCurrentIncrementalCheckpoints(
			t,
			pool,
			orgSlug,
			incrementalCheckpointProjectSlug,
			incrementalCheckpointEnvironmentSlug,
			pipeline.Metadata.Slug,
		),
	)
}

func createAndDeployIncrementalCheckpointPipeline(
	t *testing.T,
	ctx context.Context,
	svc pipelinesvc.Service,
	store pipelinesvc.PipelineStore,
	orgSlug, memberSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
) string {
	t.Helper()

	materializePipelines(t, ctx, store, orgSlug, incrementalCheckpointProjectSlug, pipeline)
	created, err := svc.Get(ctx, orgSlug, incrementalCheckpointProjectSlug, pipeline.Metadata.Slug, memberSlug)
	require.NoError(t, err)
	require.NotNil(t, created.Summary)
	require.NotNil(t, created.Summary.CurrentVersionSpecHash)
	specHash := *created.Summary.CurrentVersionSpecHash

	deployIncrementalCheckpointPipeline(
		t,
		ctx,
		svc,
		orgSlug,
		incrementalCheckpointEnvironmentSlug,
		pipeline.Metadata.Slug,
		specHash,
	)

	return specHash
}

func deployIncrementalCheckpointPipeline(
	t *testing.T,
	ctx context.Context,
	svc pipelinesvc.Service,
	orgSlug, environmentSlug, pipelineSlug slug.Slug,
	specHash string,
) {
	t.Helper()

	_, err := svc.Deploy(
		ctx,
		orgSlug,
		incrementalCheckpointProjectSlug,
		environmentSlug,
		pipelineSlug,
		model.SystemEventActor(),
		pipelinesvc.DeployPipelineRequest{
			SpecHash:                         specHash,
			ProtectedEnvironmentConfirmation: &environmentSlug,
		},
	)
	require.NoError(t, err)
}

type incrementalCheckpointServiceQueryExecutor struct {
	httpSink           *httpconnector.Connector
	httpStatement      json.RawMessage
	mu                 sync.Mutex
	sourceObservations []json.RawMessage
	checkpointInputs   []*pipelinerun.IncrementalCheckpointInput
	sourceCalls        int
	sinkCalls          int
	failSinkOnCall     int
	sinkBarrier        func()
}

func (*incrementalCheckpointServiceQueryExecutor) FindBySlug(context.Context, slug.Slug, slug.Slug, slug.Slug) (*queriessvc.Query, error) {
	return nil, xerror.Unexpected().Detail("saved Workspace operations are not used by incremental checkpoint tests")
}

func (e *incrementalCheckpointServiceQueryExecutor) Execute(
	ctx context.Context,
	_, _ slug.Slug,
	querySlug slug.Slug,
	_ model.EventActor,
	_ map[string]any,
	_ ...queriessvc.ExecuteOption,
) (*result.ExecutionResult, error) {
	if querySlug == must.Slug("upsert-users") {
		if e.httpSink != nil {
			return e.httpSink.Execute(ctx, e.httpStatement, nil)
		}
		e.mu.Lock()
		e.sinkCalls++
		shouldFail := e.failSinkOnCall > 0 && e.sinkCalls == e.failSinkOnCall
		sinkBarrier := e.sinkBarrier
		e.mu.Unlock()

		if sinkBarrier != nil {
			sinkBarrier()
		}
		if shouldFail {
			return nil, xerror.Unexpected().Detail("sink write failed")
		}
		return successfulIncrementalServiceSQLResult(0), nil
	}

	return successfulIncrementalServiceSQLResult(3), nil
}

func (e *incrementalCheckpointServiceQueryExecutor) ExecuteIncremental(
	_ context.Context,
	_, _ slug.Slug,
	_ slug.Slug,
	_ map[string]any,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	e.mu.Lock()
	e.checkpointInputs = append(e.checkpointInputs, checkpoint)
	upperBound := json.RawMessage(`{"value":"fallback"}`)
	if e.sourceCalls < len(e.sourceObservations) {
		upperBound = e.sourceObservations[e.sourceCalls]
	}
	e.sourceCalls++
	observedAt := time.Date(2026, time.May, 24, 10+e.sourceCalls, 0, 0, 0, time.UTC)
	e.mu.Unlock()

	return successfulIncrementalServiceSQLResult(3), &pipelinerun.IncrementalCheckpointObservation{
		SourceStepSlug:      must.Slug("fetch-users"),
		CheckpointNamespace: "normal",
		CheckpointKind:      "watermark",
		ObservedLowerBound:  checkpoint.LowerBound,
		ObservedUpperBound:  upperBound,
		DisplayMetadata:     json.RawMessage(`{"summary":"source checkpoint","record_count":3}`),
		ObservedAt:          observedAt,
	}, nil
}

func (e *incrementalCheckpointServiceQueryExecutor) ExecuteInlineIncremental(ctx context.Context, org, ds slug.Slug, _, params map[string]any, checkpoint *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	return e.ExecuteIncremental(ctx, org, ds, "", params, checkpoint)
}

func (e *incrementalCheckpointServiceQueryExecutor) GetExpectedInputContract(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (*payload.Contract, error) {
	return incrementalCheckpointServiceRowsContract(), nil
}

func (e *incrementalCheckpointServiceQueryExecutor) ExecuteInline(
	context.Context,
	slug.Slug,
	slug.Slug,
	map[string]any,
	map[string]any,
) (*result.ExecutionResult, error) {
	return nil, xerror.Unexpected().Detail("inline execution is not used by incremental checkpoint service tests")
}

func newIncrementalCheckpointService(
	t *testing.T,
	pool *pgxpool.Pool,
	executor *incrementalCheckpointServiceQueryExecutor,
	events workerproducer.EventProducer,
	runProducer workerproducer.PipelineRunProducer,
) (pipelinesvc.Service, pipelinesvc.PipelineStore) {
	t.Helper()

	txm := postgres.NewTransactionManager(pool)
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelinesrepo.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	svc, err := pipelinesvc.New(compiledplans.NewRepository(), deploymentresolutions.NewRepository(),
		store,
		pool,
		txm,
		pipelineruns.New(), incrementalcheckpoints.New(),
		pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()),
		driftimpactrepo.New(),
		executor,
		allowAuthorizationService{},
		&fakes.AdmissionService{},
		&fakes.DataSourceService{
			FindBySlugFn: func(_ context.Context, _, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
				if executor.httpSink != nil && dsSlug == "warehouse-postgres" {
					return &datasourcessvc.DataSource{Slug: dsSlug, Type: model.DataSourceTypeHTTP, IsActive: true}, nil
				}
				if dsSlug == "source-http" {
					return &datasourcessvc.DataSource{Slug: dsSlug, Type: model.DataSourceTypeHTTP, IsActive: true, Schema: datasourcessvc.Schema[datasourcessvc.HTTPSchemaPayload]{SchemaTypeValue: datasourcessvc.SchemaTypeHTTP, Type: "http"}}, nil
				}
				return incrementalCheckpointServiceDataSource(dsSlug), nil
			},
		},
		&fakes.ProjectsService{
			GetProjectFn: func(_ context.Context, _, projectSlug slug.Slug) (*model.Project, error) {
				return &model.Project{Slug: projectSlug, Name: "Checkpoint Project"}, nil
			},
		},
		&ownershipGitSourceResolver{},
		clock.New(),
		runProducer,
		noopPipelineGitProducer{},
		events,
		noopPipelineRuntimeResolver{},
		testSinkEffectLedger(t, txm),
		newTestProofLinks(),
		testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)
	return svc, store
}

func incrementalCheckpointServiceDataSource(dsSlug slug.Slug) *datasourcessvc.DataSource {
	return &datasourcessvc.DataSource{
		Slug:     dsSlug,
		Type:     model.DataSourceTypePostgres,
		IsActive: true,
		Schema: datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
			Type:            "postgres",
			Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
				{
					Name: "users",
					Columns: []datasourcessvc.SchemaColumn{
						{Name: "id", Type: "integer"},
						{Name: "updated_at", Type: "timestamp"},
					},
				},
			}},
		},
	}
}

func createIncrementalCheckpointServiceOrgProject(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug slug.Slug,
) slug.Slug {
	t.Helper()

	ctx := context.Background()
	txm := postgres.NewTransactionManager(pool)
	eventProducer := eventtest.NewProducer(nil)
	rolesService := rolessvc.New(txm, roles.NewRepository(), permissions.NewRepository(), eventProducer)
	usersService := userssvc.New(
		txm,
		crypto.NewArgon2(),
		users.NewRepository(),
		&testutil.FakeEmailProducer{},
		eventProducer,
		"http://localhost:3000",
	)
	membersService := memberssvc.New(
		txm,
		members.NewRepository(),
		memberinvitations.NewRepository(),
		organizations.NewRepository(),
		rolesService,
		usersService,
		&testutil.FakeEmailProducer{},
		eventProducer,
		"http://localhost:3000",
		clock.New(),
	)
	environmentsRepo := environments.NewRepository()
	environmentsService := environmentssvc.New(txm, environmentsRepo, eventProducer)
	orgService := orgsvc.New(
		txm,
		organizations.NewRepository(),
		environmentsService,
		membersService,
		rolesService,
		eventProducer,
		admissionsvc.New(txm, organizations.NewRepository()),
	)
	projectsService := projectssvc.New(
		txm,
		projects.NewRepository(),
		organizations.NewRepository(),
		environmentsRepo,
		&fakes.DataSourceService{},
		nil,
		nil,
		nil,
		eventProducer,
	)

	unique := time.Now().UnixNano()
	user, err := usersService.CreateUser(ctx, model.NewUser{
		FullName: fmt.Sprintf("Incremental Checkpoint User %d", unique),
		Email:    orgSlug.String() + "@example.com",
		Password: "password123",
		Role:     model.RoleUser,
		Locale:   "en",
	})
	require.NoError(t, err)

	actor := model.UserEventActor(user.Slug)
	_, err = orgService.CreateOrganization(ctx, user.Slug, actor, model.NewOrganization{
		Name: "Incremental Checkpoint Org",
		Slug: orgSlug,
	})
	require.NoError(t, err)

	_, err = projectsService.CreateProject(ctx, orgSlug, model.NewProject{
		Name: "Checkpoint Project",
		Slug: incrementalCheckpointProjectSlug,
	}, actor)
	require.NoError(t, err)

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, cleanupErr := pool.Exec(cleanupCtx, `DELETE FROM project_environments WHERE organization_id = (SELECT id FROM organizations WHERE slug = $1)`, orgSlug.String())
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE slug = $1`, orgSlug.String())
		require.NoError(t, cleanupErr)
		_, cleanupErr = pool.Exec(cleanupCtx, `DELETE FROM users WHERE slug = $1`, user.Slug.String())
		require.NoError(t, cleanupErr)
	})

	return user.Slug
}

func enableIncrementalCheckpointProjectEnvironment(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, memberSlug, environmentSlug slug.Slug,
) {
	t.Helper()

	ctx := context.Background()
	txm := postgres.NewTransactionManager(pool)
	environmentsService := environmentssvc.New(txm, environments.NewRepository(), eventtest.NewProducer(nil))
	_, err := environmentsService.EnableProjectEnvironment(
		ctx,
		orgSlug,
		incrementalCheckpointProjectSlug,
		environmentSlug,
		model.EnableProjectEnvironment{ProtectedEnvironmentConfirmation: &environmentSlug},
		model.UserEventActor(memberSlug),
	)
	require.NoError(t, err)
}

func incrementalCheckpointServicePipeline(pipelineSlug slug.Slug) *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: pipelineSlug, Name: "Incremental Users Sync"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("source-postgres"),
					OperationType:  "sql",
					OperationRef:   must.Slug("list-users"),
					Incremental: &pipelinespec.IncrementalSourceContract{
						CheckpointKind: incrementalCheckpointServiceKind(model.IncrementalCheckpointKindWatermark),
						Field:          "updated_at",
						LookbackWindow: "5m",
						Ordering:       []string{"updated_at", "id"},
						DedupeKey:      "id",
					},
					OutputContract: incrementalCheckpointServiceRowsContract(),
				},
			},
			{
				Kind: "sink",
				Slug: must.Slug("persist-users"),
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
					InputContract:    incrementalCheckpointServiceRowsContract(),
					DataSourceSlug:   must.Slug("warehouse-postgres"),
					OperationType:    "sql",
					OperationRef:     must.Slug("upsert-users"),
				},
			},
		},
	}
}

func incrementalCheckpointServiceRowsContract() *payload.Contract {
	return &payload.Contract{
		Name:           "rows",
		Representation: model.ContractRepresentationRows,
		Fields: []payload.ContractField{
			{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
			{Name: "updated_at", Type: model.ContractFieldTypeValueDatetime, Required: true},
		},
	}
}

func successfulIncrementalServiceSQLResult(rowCount int) *result.ExecutionResult {
	rows := make([]map[string]any, rowCount)
	for i := range rows {
		rows[i] = map[string]any{
			"id":         i + 1,
			"updated_at": fmt.Sprintf("2026-05-24T10:%02d:00Z", i),
		}
	}
	return result.NewSQLResult("postgres", &result.SQLResult{
		Rows:        rows,
		ColumnNames: []string{"id", "updated_at"},
		RowCount:    rowCount,
	}, 25, nil)
}

func currentIncrementalCheckpoint(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
) (json.RawMessage, string) {
	t.Helper()

	var upperBound []byte
	var status string
	err := pool.QueryRow(context.Background(), `
		SELECT pic.observed_upper_bound, pic.commit_status
		FROM pipeline_incremental_checkpoints pic
		JOIN projects p ON p.id = pic.project_id
		JOIN organizations o ON o.id = p.organization_id
		JOIN project_environments pe ON pe.id = pic.project_environment_id
			AND pe.project_id = p.id
		JOIN environments e ON e.id = pe.environment_id
		WHERE o.slug = $1
			AND p.slug = $2
			AND e.slug = $3
			AND pic.pipeline_slug = $4
			AND pic.commit_status = 'committed'
			AND pic.superseded_at IS NULL
	`, orgSlug.String(), projectSlug.String(), environmentSlug.String(), pipelineSlug.String()).Scan(&upperBound, &status)
	require.NoError(t, err)
	return json.RawMessage(upperBound), status
}

func blockedIncrementalCheckpoint(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	runSlug string,
) (json.RawMessage, string) {
	t.Helper()

	var upperBound []byte
	var blockedReason string
	err := pool.QueryRow(context.Background(), `
		SELECT pic.observed_upper_bound, pic.blocked_reason
		FROM pipeline_incremental_checkpoints pic
		JOIN projects p ON p.id = pic.project_id
		JOIN organizations o ON o.id = p.organization_id
		JOIN project_environments pe ON pe.id = pic.project_environment_id
			AND pe.project_id = p.id
		JOIN environments e ON e.id = pe.environment_id
		WHERE o.slug = $1
			AND p.slug = $2
			AND e.slug = $3
			AND pic.pipeline_slug = $4
			AND pic.observed_run_slug = $5
			AND pic.commit_status = 'blocked'
	`, orgSlug.String(), projectSlug.String(), environmentSlug.String(), pipelineSlug.String(), runSlug).Scan(&upperBound, &blockedReason)
	require.NoError(t, err)
	return json.RawMessage(upperBound), blockedReason
}

func countActiveCurrentIncrementalCheckpoints(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
) int {
	t.Helper()

	var count int
	err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*)
		FROM pipeline_incremental_checkpoints pic
		JOIN projects p ON p.id = pic.project_id
		JOIN organizations o ON o.id = p.organization_id
		JOIN project_environments pe ON pe.id = pic.project_environment_id
			AND pe.project_id = p.id
		JOIN environments e ON e.id = pe.environment_id
		JOIN pipeline_specs ps ON ps.project_id = pic.project_id
			AND ps.slug = pic.pipeline_slug
			AND ps.deleted_at IS NULL
		WHERE o.slug = $1
			AND p.slug = $2
			AND e.slug = $3
			AND pic.pipeline_slug = $4
			AND pic.commit_status = 'committed'
			AND pic.superseded_at IS NULL
	`, orgSlug.String(), projectSlug.String(), environmentSlug.String(), pipelineSlug.String()).Scan(&count)
	require.NoError(t, err)
	return count
}

func countIncrementalCheckpointsByStatus(
	t *testing.T,
	pool *pgxpool.Pool,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	status string,
) int {
	t.Helper()

	var count int
	err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*)
		FROM pipeline_incremental_checkpoints pic
		JOIN projects p ON p.id = pic.project_id
		JOIN organizations o ON o.id = p.organization_id
		JOIN project_environments pe ON pe.id = pic.project_environment_id
			AND pe.project_id = p.id
		JOIN environments e ON e.id = pe.environment_id
		WHERE o.slug = $1
			AND p.slug = $2
			AND e.slug = $3
			AND pic.pipeline_slug = $4
			AND pic.commit_status = $5
	`, orgSlug.String(), projectSlug.String(), environmentSlug.String(), pipelineSlug.String(), status).Scan(&count)
	require.NoError(t, err)
	return count
}

func ptrSlug(value string) *slug.Slug {
	s := must.Slug(value)
	return &s
}

func incrementalCheckpointServiceKind(value model.IncrementalCheckpointKind) *model.IncrementalCheckpointKind {
	return &value
}

func newIncrementalCheckpointTwoPartyBarrier() func() {
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	count := 0

	return func() {
		mu.Lock()
		defer mu.Unlock()

		count++
		if count >= 2 {
			cond.Broadcast()
			return
		}
		for count < 2 {
			cond.Wait()
		}
	}
}

type bootstrapEventRecorder struct {
	calls []eventssvc.CreateEventParams
	err   error
}

func (r *bootstrapEventRecorder) CreateEvent(_ context.Context, params eventssvc.CreateEventParams) (*model.Event, error) {
	r.calls = append(r.calls, params)
	return &model.Event{Slug: params.Slug}, r.err
}

func TestPipelineServiceBootstrapInitialCheckpoints(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real PostgreSQL")
	}
	ctx := context.Background()
	pool := openE2EPool(t)
	org := must.Slug(fmt.Sprintf("bootstrap-org-%d", time.Now().UnixNano()))
	member := createIncrementalCheckpointServiceOrgProject(t, pool, org)
	executor := &incrementalCheckpointServiceQueryExecutor{sourceObservations: []json.RawMessage{json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`)}}
	events := &bootstrapEventRecorder{}
	svc, store := newIncrementalCheckpointService(t, pool, executor, eventtest.NewProducer(events), noopPipelineRunProducer{})
	pipeline := incrementalCheckpointServicePipeline("bootstrap-users")
	specHash := createAndDeployIncrementalCheckpointPipeline(t, ctx, svc, store, org, member, pipeline)
	actor := model.UserEventActor(member)
	seeds := map[slug.Slug]json.RawMessage{"fetch-users": json.RawMessage(`{"value":"2026-05-24T10:00:00Z"}`)}
	events.err = xerror.Unexpected().Detail("audit unavailable")
	require.Error(t, svc.BootstrapCheckpoints(ctx, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, "bootstrap-001", nil, actor, nil, seeds))
	require.Zero(t, countIncrementalCheckpointsByStatus(t, pool, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, "committed"))
	events.err = nil
	require.NoError(t, svc.BootstrapCheckpoints(ctx, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, "bootstrap-001", nil, actor, nil, seeds))
	require.Empty(t, executor.checkpointInputs)
	last := events.calls[len(events.calls)-1]
	require.Equal(t, model.EventClassSecurityAudit, last.EventClass)
	require.Equal(t, "checkpoint_bootstrap", last.Metadata["operation"])
	require.NotContains(t, fmt.Sprint(last.Metadata), "2026-05-24T10:00:00Z")
	require.Error(t, svc.BootstrapCheckpoints(ctx, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, "bootstrap-002", nil, actor, nil, seeds))
	_, err := svc.Execute(ctx, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, ptrSlug("after-bootstrap"), nil, actor, e2eRunSource(actor), nil)
	require.NoError(t, err)
	require.Len(t, executor.checkpointInputs, 1)
	require.JSONEq(t, string(seeds["fetch-users"]), string(executor.checkpointInputs[0].LowerBound))
	otherOrg := must.Slug(fmt.Sprintf("bootstrap-other-%d", time.Now().UnixNano()))
	otherMember := createIncrementalCheckpointServiceOrgProject(t, pool, otherOrg)
	createAndDeployIncrementalCheckpointPipeline(t, ctx, svc, store, otherOrg, otherMember, pipeline)
	require.Zero(t, countIncrementalCheckpointsByStatus(t, pool, otherOrg, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, "committed"))
	require.NoError(t, svc.BootstrapCheckpoints(ctx, otherOrg, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, pipeline.Metadata.Slug, "bootstrap-001", nil, model.UserEventActor(otherMember), nil, seeds))
	prod := model.DefaultProductionEnvironmentSlug
	enableIncrementalCheckpointProjectEnvironment(t, pool, org, member, prod)
	deployIncrementalCheckpointPipeline(t, ctx, svc, org, prod, pipeline.Metadata.Slug, specHash)
	var identity string
	require.NoError(t, pool.QueryRow(ctx, `SELECT l.operation_identity FROM pipeline_incremental_checkpoint_locks l JOIN projects p ON p.id=l.project_id JOIN organizations o ON o.id=p.organization_id WHERE o.slug=$1 AND l.pipeline_slug=$2 LIMIT 1`, org, pipeline.Metadata.Slug).Scan(&identity))
	lookup := pipelinerun.IncrementalCheckpointLookup{PipelineSlug: pipeline.Metadata.Slug, SourceStepSlug: "fetch-users", CheckpointNamespace: "normal", OperationIdentity: identity}
	repo := incrementalcheckpoints.New()
	require.NoError(t, repo.EnsureCommitLock(ctx, pool, org, incrementalCheckpointProjectSlug, prod, lookup))
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, repo.LockCommit(ctx, tx, org, incrementalCheckpointProjectSlug, prod, lookup))
	bootstrapCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	completed := make(chan error, 1)
	go func() {
		completed <- svc.BootstrapCheckpoints(bootstrapCtx, org, incrementalCheckpointProjectSlug, prod, pipeline.Metadata.Slug, "bootstrap-lock", &prod, actor, nil, seeds)
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE state='active' AND wait_event_type='Lock' AND query LIKE '%LockIncrementalCheckpointCommit%')`).Scan(&waiting)
		return err == nil && waiting
	}, 3*time.Second, 10*time.Millisecond, "bootstrap must wait for the normal checkpoint commit lock")
	require.NoError(t, tx.Commit(ctx))
	require.NoError(t, <-completed)
	require.Equal(t, 1, countIncrementalCheckpointsByStatus(t, pool, org, incrementalCheckpointProjectSlug, prod, pipeline.Metadata.Slug, "committed"))
	concurrentPipeline := incrementalCheckpointServicePipeline("concurrent-bootstrap")
	createAndDeployIncrementalCheckpointPipeline(t, ctx, svc, store, org, member, concurrentPipeline)
	outcomes := make(chan error, 2)
	for range 2 {
		go func() {
			outcomes <- svc.BootstrapCheckpoints(ctx, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, concurrentPipeline.Metadata.Slug, "bootstrap-concurrent", nil, actor, nil, seeds)
		}()
	}
	first, second := <-outcomes, <-outcomes
	require.NotEqual(t, first == nil, second == nil, "exactly one concurrent bootstrap must succeed")
	require.Equal(t, 1, countIncrementalCheckpointsByStatus(t, pool, org, incrementalCheckpointProjectSlug, incrementalCheckpointEnvironmentSlug, concurrentPipeline.Metadata.Slug, "committed"))
}
