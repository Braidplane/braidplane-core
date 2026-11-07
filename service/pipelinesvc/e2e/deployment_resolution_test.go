package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/pkg/testutil/richcontractfixture"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	"github.com/Braidplane/braidplane-core/postgres/datasourcerefs"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	"github.com/Braidplane/braidplane-core/postgres/deploymentresolutions"
	"github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/incrementalcheckpoints"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/postgres/queries"
	"github.com/Braidplane/braidplane-core/service/contractplansvc"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/odcscontractsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/pipelinetriggersvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
	"github.com/stretchr/testify/require"
)

type resolutionQueryExecutor struct {
	noopQueryExecutor
	execute func(context.Context, slug.Slug, slug.Slug, contractplan.OperationContent) (*result.ExecutionResult, error)
}

func (e resolutionQueryExecutor) ExecuteSnapshot(ctx context.Context, org, source slug.Slug, content contractplan.OperationContent, _ map[string]any, _ *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	output, err := e.execute(ctx, org, source, content)
	return output, nil, err
}

type resolutionRuntimeResolver struct{ datasourcerefssvc.Service }

func (resolutionRuntimeResolver) ResolveRuntimeSecrets(context.Context, slug.Slug, slug.Slug) ([]model.RuntimeSecretValue, error) {
	return nil, nil
}

type resolutionRunProducer struct {
	args []jobs.PipelineRunExecutionArgs
}

func (p *resolutionRunProducer) EnqueuePipelineRunExecution(_ context.Context, _ postgres.DBTX, args jobs.PipelineRunExecutionArgs) (queue.QueuedJob, error) {
	p.args = append(p.args, args)
	return queue.QueuedJob{}, nil
}

func TestDeploymentResolutionPinsAttemptAndRevalidatesRollback(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		t.Run(fmt.Sprintf("scoped=%t", scoped), func(t *testing.T) { testDeploymentResolutionPinsAttempt(t, scoped, false, false, false) })
	}
	t.Run("rich", func(t *testing.T) { testDeploymentResolutionPinsAttempt(t, false, true, false, false) })
	t.Run("completion-required-input", func(t *testing.T) { testDeploymentResolutionPinsAttempt(t, false, false, false, true) })
	t.Run("rich-event", func(t *testing.T) { testDeploymentResolutionPinsAttempt(t, false, true, true, false) })
}

func testDeploymentResolutionPinsAttempt(t *testing.T, scoped, rich, event, requiresInput bool) {
	if testing.Short() {
		t.Skip("requires PostgreSQL")
	}
	ctx := t.Context()
	pool := openIntegrationPool(t)
	txm := postgres.NewTransactionManager(pool)
	org := slug.Slug(fmt.Sprintf("pinned-resolution-%d", time.Now().UnixNano()))
	project := defaultProjectSlug
	seedPipelineDeploymentOrg(t, pool, org)
	seedPipelineDeploymentEnvironment(t, pool, org, project, "dev")
	_, err := pool.Exec(ctx, `WITH credential AS (
        INSERT INTO credentials(organization_id,slug,kind,credential_type,status,encrypted_payload)
        SELECT id,'secret','static_secret','postgres','active',''::bytea FROM organizations WHERE slug=$1 RETURNING id,organization_id
    ) INSERT INTO data_sources(organization_id,credential_id,slug,name,type,config,is_active)
      SELECT organization_id,id,target,target,'postgres','{}',true FROM credential CROSS JOIN unnest(ARRAY['physical','replacement','acme-postgres']) target`, org)
	require.NoError(t, err)
	if scoped {
		_, err = pool.Exec(ctx, `UPDATE data_sources SET type='s3', config='{"endpoint":"https://storage.example","region":"test","bucket":"fixed-bucket","prefix":"imports"}' WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
		require.NoError(t, err)
	}
	sources := &fakes.DataSourceService{FindBySlugFn: func(ctx context.Context, org, source slug.Slug) (*datasourcessvc.DataSource, error) {
		stored, err := datasources.New().FindBySlug(ctx, pool, org, source)
		if err != nil {
			return nil, err
		}
		return &datasourcessvc.DataSource{Slug: stored.Slug, Type: stored.Type, Config: stored.Config, IsActive: stored.IsActive, ReadOnly: stored.ReadOnly, Credential: &credentialssvc.CredentialMetadata{Slug: stored.Credential.Slug, Status: credentialssvc.CredentialStatusActive, UpdatedAt: stored.Credential.UpdatedAt}}, nil
	}}
	events := eventtest.NewProducer(nil)
	refs := datasourcerefssvc.New(txm, datasourcerefs.NewRepository(), sources, events)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), `DELETE FROM data_source_bindings WHERE organization_id=(SELECT id FROM organizations WHERE slug=$1)`, org)
		require.NoError(t, err)
	})

	actor := model.SystemEventActor()
	pipeline := pipelineDeploymentIntegrationPipeline("sync", "Sync")
	second := pipeline.Steps[0]
	second.Slug = "second"
	query := *second.Query
	query.Parameters = map[string]any{"parents": "$steps.fetch-users.rows"}
	second.Query = &query
	pipeline.Steps = append(pipeline.Steps, second)
	if requiresInput {
		pipeline.Steps[0].Query.Parameters = map[string]any{"customer_id": "{{runtime.input.customer_id}}"}
	}
	if scoped {
		for i := range pipeline.Steps {
			pipeline.Steps[i].Query.OperationType = "s3"
		}
	}
	store, err := pipelinesvc.NewPipelineStore(pool, txm, pipelines.New(), odcscontracts.NewRepository())
	require.NoError(t, err)
	plans := compiledplans.NewRepository()
	operations := queries.New()
	operationData := json.RawMessage(`{"statement":"{\"sql\":\"SELECT captured\"}"}`)
	var operationContract json.RawMessage
	var scopes []contractplansvc.OperationScope

	var projection *odcs.ProjectionRequest
	if rich {
		catalog := odcscontractsvc.New(txm, odcscontracts.NewRepository(), projects.NewRepository(), plans)
		_, err = catalog.CreateContract(ctx, org, project, model.NewODCSContract{Slug: "orders", Name: "Orders", ODCSID: "orders"})
		require.NoError(t, err)
		_, err = catalog.CreateRevision(ctx, org, project, "orders", "v1", json.RawMessage(richcontractfixture.Document))
		require.NoError(t, err)
		_, projection, err = richcontractfixture.Selection(model.ContractRepresentationRows, model.ContractRootShapeUnknown)
		require.NoError(t, err)
		operationContract, err = json.Marshal(richcontractfixture.Reference(model.ContractRepresentationRows, model.ContractRootShapeUnknown))
		require.NoError(t, err)
		for i := range pipeline.Steps {
			pipeline.Steps[i].Query.OutputContract = nil
			pipeline.Steps[i].Query.OutputContractRef = richcontractfixture.Reference(model.ContractRepresentationRows, model.ContractRootShapeUnknown)
		}
		if event {
			pipeline.Steps = append([]pipelinespec.Step{{Kind: "input", Slug: "event", Input: &pipelinespec.InputSpec{OutputContractRef: richcontractfixture.Reference(model.ContractRepresentationJSON, model.ContractRootShapeObject)}}}, pipeline.Steps...)
			_, eventProjection, err := richcontractfixture.Selection(model.ContractRepresentationJSON, model.ContractRootShapeObject)
			require.NoError(t, err)
			projection.Inputs = append(projection.Inputs, eventProjection.Inputs[0])
			pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "transform", Slug: "joined", Transform: &pipelinespec.TransformSpec{SQL: "SELECT id FROM query_rows", OutputKind: model.TransformOutputKindRows, Inputs: map[string]pipelinespec.StepInput{"event_rows": {FromStep: "event"}, "query_rows": {FromStep: "second"}}}})
		}
	}
	if projection != nil {
		for i := range projection.Inputs {
			projection.Inputs[i].Mapping.Physical = map[string]odcs.PhysicalTypeMapping{}
		}
	}

	authored, err := json.Marshal(pipeline)
	require.NoError(t, err)
	commit, ref := strings.Repeat("a", 40), "refs/heads/main"
	gitSource := &ownershipGitSourceResolver{source: &pipelinegit.Source{RepoStore: pipelinegit.NewSnapshotRepoStore(map[string][]byte{"pipelines/sync.pipeline.json": authored}), SourceRef: &ref, LastAppliedCommit: &commit}}

	calls := 0
	failNextCompletion := false
	rebindAfterDispatch := false
	producer := &resolutionRunProducer{}
	executor := resolutionQueryExecutor{execute: func(ctx context.Context, actualOrg, source slug.Slug, content contractplan.OperationContent) (*result.ExecutionResult, error) {
		calls++
		if failNextCompletion {
			failNextCompletion = false
			return nil, xerror.ConnectionFailed(xerror.DataSource)
		}
		require.Equal(t, org, actualOrg)
		require.Equal(t, slug.Slug("physical"), source)
		require.Contains(t, string(content.Data), "captured")
		if scoped {
			expectedScope := scopes[0].Scope
			expectedScope.DataSource = "physical"
			require.Equal(t, &expectedScope, content.ResourceScope)
		} else {
			require.Nil(t, content.ResourceScope)
		}
		_, err := deploymentplan.DispatchConfig(ctx, org, source)
		require.NoError(t, err)
		if rebindAfterDispatch && calls == 1 {
			_, err = refs.UpsertEnvironmentBinding(ctx, org, project, "sync", "dev", "acme-postgres", model.UpsertDataSourceBinding{DataSourceSlug: "replacement"}, actor)
			require.NoError(t, err)
		}
		row := map[string]any{"id": 1}
		if rich {
			require.NoError(t, jsonvalue.Unmarshal([]byte(richcontractfixture.ValidJSON), &row))
		}
		return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{row}, ColumnNames: []string{"id", "region", "created", "embedding", "context"}, RowCount: 1}, 0, nil), nil
	}}
	svc, err := pipelinesvc.New(plans, deploymentresolutions.NewRepository(), store, pool, txm, pipelineruns.New(), incrementalcheckpoints.New(), pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()), driftimpact.New(), executor, allowAuthorizationService{}, &fakes.AdmissionService{}, sources, &fakes.ProjectsService{}, gitSource, clock.New(), producer, noopPipelineGitProducer{}, events, resolutionRuntimeResolver{refs}, testSinkEffectLedger(t, txm), newTestProofLinks(), testutil.FakeRunSucceededNotifier{})
	require.NoError(t, err)
	compiler, err := contractplansvc.New(pipelines.New(), txm, plans, operations, odcscontracts.NewRepository(), store, refs)
	require.NoError(t, err)
	app := resolutionAPI(svc, compiler)
	projectPath := fmt.Sprintf("/organizations/%s/projects/%s", org, project)
	deploymentPath := projectPath + "/environments/dev/pipelines/sync:deploy"
	var reconciled api.PipelineReconcileResponse
	resolutionPost(t, app, projectPath+"/pipelines:reconcile", map[string]any{}, 200, &reconciled)
	require.Empty(t, reconciled.Failures)
	require.Equal(t, 1, reconciled.Summary.Synced)

	_, err = refs.UpsertEnvironmentBinding(ctx, org, project, "sync", "dev", "acme-postgres", model.UpsertDataSourceBinding{DataSourceSlug: "physical"}, actor)
	require.NoError(t, err)
	versionMeta, err := store.FindSpec(ctx, org, project, "sync")
	require.NoError(t, err)
	version, err := store.FindVersionBySpecHash(ctx, org, project, "sync", *versionMeta.CurrentVersionSpecHash)
	require.NoError(t, err)
	if scoped {
		catalog := odcscontractsvc.New(txm, odcscontracts.NewRepository(), projects.NewRepository(), plans)
		_, err = catalog.CreateContract(ctx, org, project, model.NewODCSContract{Slug: "users", Name: "Users", ODCSID: "users"})
		require.NoError(t, err)
		_, err = catalog.CreateRevision(ctx, org, project, "users", "v1", json.RawMessage(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"users","version":"1","schema":[{"id":"row","name":"Users","properties":[{"id":"id","name":"id","logicalType":"integer","required":true}]}]}`))
		require.NoError(t, err)
		operationContract = json.RawMessage(`{"contract_slug":"users","revision_slug":"v1","schema_object_id":"row","representation":"rows"}`)
		operationData = json.RawMessage(`{"statement":"{\"type\":\"s3\",\"operation\":\"read\",\"key\":\"captured.json\",\"format\":\"json\"}"}`)
		for _, step := range pipeline.Steps {
			scopes = append(scopes, contractplansvc.OperationScope{Step: step.Slug, Role: "query", Scope: resourcescope.Scope{PolicyID: resourcescope.S3PolicyID, DataSource: "acme-postgres", Operation: "list-users", Contract: "users", Revision: "v1", ObjectID: "row", Representation: model.ContractRepresentationRows, S3: &resourcescope.S3{Endpoint: "https://storage.example", Region: "test", Bucket: "fixed-bucket", Prefix: "imports", DynamicKey: true}}})
		}
	}
	_, _, err = operations.Create(ctx, pool, org, "physical", &queries.CreateParams{Slug: "list-users", Name: "Captured", Data: operationData, OutputContractRef: operationContract})
	require.NoError(t, err)
	_, err = compiler.CompileVersion(ctx, org, project, "sync", version.SpecHash, "", nil, nil)
	require.ErrorIs(t, err, contractplansvc.ErrOperationSource)
	if rich {
		_, err = compiler.CompileVersion(ctx, org, project, "sync", version.SpecHash, "dev", nil, scopes)
		require.Error(t, err, "rich compilation requires an exact projection")
	}

	storedPlan, err := compiler.CompileVersion(ctx, org, project, "sync", version.SpecHash, "dev", projection, scopes)
	require.NoError(t, err)
	if rich {
		require.JSONEq(t, `[]`, string(version.ResolvedContracts), "rich authoring has no flat execution pins")
		origins := map[string]int{}
		for _, pin := range storedPlan.Pins {
			origins[pin.Origin]++
		}
		require.Equal(t, 2, origins["operation"], "saved-operation obligations remain independent")
		if event {
			require.Equal(t, 3, origins["step"])
		} else {
			require.Equal(t, 2, origins["step"])
		}
	}

	if rich {
		var compiledAPI api.PipelineCompiledPlan
		resolutionPost(t, app, projectPath+"/pipelines/sync/versions/"+version.SpecHash+"/compiled-plans?environment_slug=dev", resolutionProjectionRequest(projection), 200, &compiledAPI)
		require.Equal(t, storedPlan.Digest, compiledAPI.Digest)
	}

	compiled := storedPlan.Digest
	retained, err := compiler.Reload(ctx, org, project, "sync", compiled)
	require.NoError(t, err)
	require.Equal(t, *version.GitCommitSHA, retained.Source.Commit)
	require.Equal(t, "pipelines/sync.pipeline.json", retained.Source.Path)
	_, err = compiler.CompileVersion(ctx, org, "other-project", "sync", version.SpecHash, "dev", projection, scopes)
	require.ErrorIs(t, err, pipelines.ErrPipelineVersionNotFound)
	_, err = compiler.CompileVersion(ctx, "other-tenant", project, "sync", version.SpecHash, "dev", projection, scopes)
	require.ErrorIs(t, err, pipelines.ErrPipelineVersionNotFound)
	duplicate, err := compiler.CompileVersion(ctx, org, project, "sync", version.SpecHash, "dev", projection, scopes)
	require.NoError(t, err)
	require.Equal(t, storedPlan, duplicate)
	pipeline.Metadata.Name = "New current version"
	materializePipelines(t, ctx, store, org, project, pipeline)
	oldVersionPlan, err := compiler.CompileVersion(ctx, org, project, "sync", version.SpecHash, "dev", projection, scopes)
	require.NoError(t, err)
	require.Equal(t, storedPlan, oldVersionPlan, "compilation never borrows the current authored bytes or source commit")
	changedData := json.RawMessage(strings.ReplaceAll(string(operationData), "captured", "changed"))
	_, _, err = operations.Update(ctx, pool, org, "physical", "list-users", &queries.UpdateParams{Name: "Changed", Data: changedData, OutputContractRef: operationContract})
	require.NoError(t, err)
	recompiled, err := compiler.CompileVersion(ctx, org, project, "sync", version.SpecHash, "dev", projection, scopes)
	require.NoError(t, err)
	require.NotEqual(t, compiled, recompiled.Digest)
	unchanged, err := compiler.Reload(ctx, org, project, "sync", compiled)
	require.NoError(t, err)
	require.Equal(t, retained, unchanged)
	historicalPlan, err := compiler.Get(ctx, org, project, "sync", compiled)
	require.NoError(t, err)
	require.Equal(t, storedPlan, historicalPlan)
	zero := int64(0)
	if rich {
		resolutionPost(t, app, deploymentPath, map[string]any{"spec_hash": version.SpecHash, "expected_revision": zero}, 400, nil)
	}

	current, err := store.FindSpec(ctx, org, project, "sync")
	require.NoError(t, err)
	_, err = svc.Deploy(ctx, org, project, "dev", "sync", actor, pipelinesvc.DeployPipelineRequest{SpecHash: *current.CurrentVersionSpecHash, CompiledPlanDigest: &compiled, ExpectedRevision: &zero})
	require.ErrorIs(t, err, deploymentplan.ErrInvalid, "selection must match the exact authored version")
	_, err = pool.Exec(ctx, `UPDATE pipeline_compiled_plans SET format_version=999 WHERE digest=$1`, compiled)
	require.NoError(t, err)
	_, err = svc.Deploy(ctx, org, project, "dev", "sync", actor, pipelinesvc.DeployPipelineRequest{SpecHash: version.SpecHash, CompiledPlanDigest: &compiled, ExpectedRevision: &zero})
	require.ErrorIs(t, err, deploymentplan.ErrInvalid, "unsupported plan formats fail closed")
	_, err = pool.Exec(ctx, `UPDATE pipeline_compiled_plans SET format_version=$2 WHERE digest=$1`, compiled, contractplan.FormatVersion)
	require.NoError(t, err)

	var deployedAPI api.PipelineDeployment
	resolutionPost(t, app, deploymentPath, map[string]any{"spec_hash": version.SpecHash, "compiled_plan_digest": compiled, "expected_revision": zero}, 200, &deployedAPI)
	deployed, err := svc.GetDeployment(ctx, org, project, "dev", "sync")
	require.NoError(t, err)
	require.Equal(t, deployed.CurrentResolutionDigest, deployedAPI.CurrentResolutionDigest)
	require.NotNil(t, deployed.CurrentResolutionDigest)
	original := *deployed.CurrentResolutionDigest
	completionBytes, err := deploymentresolutions.NewRepository().Find(ctx, pool, org, project, "dev", "sync", original)
	require.NoError(t, err)
	completionSnapshot := &model.PipelineCompletionSnapshot{PipelineSlug: "sync", VersionHash: version.SpecHash, ResolutionDigest: original, Materialization: completionBytes}
	completionErr := svc.ValidateCompletionExecutionContext(ctx, org, project, "dev", completionSnapshot)
	switch {
	case requiresInput:
		require.ErrorContains(t, completionErr, "runtime input customer_id")
	case event:
		require.ErrorContains(t, completionErr, "do not supply event input")
	default:
		require.NoError(t, completionErr)
	}
	completionSnapshot.State, completionSnapshot.CausalDepth = "pending", 1
	completionSnapshot.ClosureAuthority = model.CompletionAuthoritySnapshot{Valid: true, Permissions: []string{"*:*"}}
	completionSnapshot.CurrentAuthority = completionSnapshot.ClosureAuthority
	completionService := pipelinetriggersvc.New(true, txm, completionValidationRepository{Repository: pipelinetriggers.NewRepository(), snapshot: completionSnapshot}, svc, events, nil)
	_, err = completionService.CheckCompletionSnapshot(ctx, org, project, "dev", 1)
	if requiresInput || event {
		require.ErrorContains(t, err, "execution_context_unavailable")
		if requiresInput {
			return
		}
	} else {
		require.NoError(t, err, "completion admission must support nested exact-context revalidation in one consistent transaction")
	}
	if !scoped && !rich && !event && !requiresInput {
		t.Run("completion runtime", func(t *testing.T) {
			materializePipelines(t, ctx, store, org, project, pipeline, pipelineDeploymentIntegrationPipeline("upstream", "Completion upstream"))
			client, err := queue.NewEnqueueOnly(pool)
			require.NoError(t, err)
			runProducer, err := workerproducer.NewPipelineRunProducerForQueue(queue.NewEnqueuer(client), pipelineruns.New(), "completion-"+org.String())
			require.NoError(t, err)
			live, err := pipelinesvc.New(plans, deploymentresolutions.NewRepository(), store, pool, txm, pipelineruns.New(), incrementalcheckpoints.New(), pipelineruntime.NewRunStore(txm, pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()), driftimpact.New(), executor, allowAuthorizationService{}, &fakes.AdmissionService{}, sources, &fakes.ProjectsService{}, gitSource, clock.New(), runProducer, noopPipelineGitProducer{}, events, resolutionRuntimeResolver{refs}, testSinkEffectLedger(t, txm), newTestProofLinks(), testutil.FakeRunSucceededNotifier{})
			require.NoError(t, err)
			testSuccessfulCompletionRuntime(ctx, t, pool, live, runProducer, org, project, "dev", original, func() int { return calls }, func() { failNextCompletion = true })
		})
		calls = 0
	}
	corruptCompletion := *completionSnapshot
	corruptCompletion.Materialization = []byte("replacement")
	require.ErrorIs(t, svc.ValidateCompletionExecutionContext(ctx, org, project, "dev", &corruptCompletion), deploymentplan.ErrInvalid)

	_, err = svc.Deploy(ctx, org, project, "dev", "sync", actor, pipelinesvc.DeployPipelineRequest{SpecHash: version.SpecHash, CompiledPlanDigest: &compiled, ExpectedRevision: &zero})
	require.ErrorIs(t, err, pipelines.ErrPipelineDeploymentConflict)

	source := pipelinerun.RunSourceSummary{Type: model.PipelineRunSourceTypeAPI, Summary: "Synthetic resolution test", Entrypoint: model.PipelineRunSourceEntrypointAPI, ActorType: model.EventActorTypeSystem}
	if rich && !event {
		queuedSlug := slug.Slug("queued-rich")
		resolutionPost(t, app, projectPath+"/environments/dev/pipelines/sync:execute", map[string]any{"run_slug": queuedSlug}, 202, nil)
		require.Len(t, producer.args, 1)
		require.Equal(t, version.SpecHash, producer.args[0].PipelineVersionHash)
		require.Equal(t, &original, producer.args[0].DeploymentResolutionDigest)
		require.NoError(t, svc.ExecuteQueuedPipelineRun(ctx, producer.args[0]))
		completed, err := svc.GetRun(ctx, org, project, "dev", queuedSlug.String())
		require.NoError(t, err)
		require.Equal(t, "succeeded", completed.Metadata.Status)
		require.Equal(t, compiled, completed.Composition.TemplateDigest)
		require.Equal(t, original, completed.Composition.ResolutionDigest)
		require.Equal(t, 2, calls)
		calls = 0
	}
	rebindAfterDispatch = true

	runSlug := slug.Slug("first")
	var run *pipelinesvc.PipelineRunDetail
	if event {
		invalidSlug := slug.Slug("invalid-event")
		_, err = svc.ExecuteVerifiedEvent(ctx, org, project, "dev", "sync", &invalidSlug, actor, source, nil, json.RawMessage(`{"id":"wrong-type"}`))
		require.Error(t, err)
		require.Zero(t, calls, "event admission rejects invalid rich payloads before operation dispatch")
		run, err = svc.ExecuteVerifiedEventVersion(ctx, org, project, "dev", "sync", version.SpecHash, &runSlug, actor, source, nil, json.RawMessage(richcontractfixture.ValidJSON))
	} else {
		run, err = svc.Execute(ctx, org, project, "dev", "sync", &runSlug, nil, actor, source, nil)
	}
	require.NoError(t, err)
	require.Equal(t, 2, calls, "the second dispatch keeps the original target after a concurrent rebind")
	require.Equal(t, version.ID, run.Metadata.PipelineVersionID)
	require.Equal(t, version.SpecHash, run.Metadata.PipelineVersionSpecHash)
	require.Equal(t, original, run.Timeline.DeploymentResolutionDigest)
	storedRun, err := pipelineruns.New().FindRunBySlug(ctx, pool, org, project, "dev", "first")
	require.NoError(t, err)
	require.Equal(t, &original, storedRun.DeploymentResolutionDigest)
	inspected, err := svc.GetRun(ctx, org, project, "dev", "first")
	require.NoError(t, err)
	require.Equal(t, "available", inspected.Composition.Status)
	if scoped {
		require.Equal(t, scopes[0].Scope.Identity(), inspected.Composition.Operations[0].ResourceScope)
	}
	require.Equal(t, original, inspected.Composition.ResolutionDigest)
	require.Equal(t, compiled, inspected.Composition.TemplateDigest)
	require.Equal(t, slug.Slug("physical"), inspected.Composition.Targets[0].DataSource, "history retains the original target after rebinding")
	historical, err := svc.GetRun(ctx, org, project, "dev", "first")
	require.NoError(t, err)
	require.True(t, historical.Composition.AdmissionEnabled)
	require.Equal(t, inspected.Composition, historical.Composition, "historical reads preserve recorded selection after rebinding")
	_, err = svc.Execute(ctx, org, project, "dev", "sync", nil, nil, actor, source, nil)
	require.ErrorIs(t, err, deploymentplan.ErrInvalid)
	require.Equal(t, 2, calls)
	require.ErrorIs(t, svc.ValidateCompletionExecutionContext(ctx, org, project, "dev", completionSnapshot), deploymentplan.ErrInvalid, "completion admission cannot repair changed bindings")
	err = svc.BootstrapCheckpoints(ctx, org, project, "dev", "sync", "bootstrap", nil, actor, nil, map[slug.Slug]json.RawMessage{"fetch-users": json.RawMessage(`1`)})
	require.ErrorIs(t, err, deploymentplan.ErrInvalid, "bootstrap must reject the same binding drift as execution")
	_, err = svc.Deploy(ctx, org, project, "dev", "sync", actor, pipelinesvc.DeployPipelineRequest{SpecHash: version.SpecHash, ResolutionDigest: &original, ExpectedRevision: &deployed.Revision})
	require.ErrorIs(t, err, deploymentplan.ErrInvalid, "rollback cannot substitute the replacement binding")
	_, err = refs.UpsertEnvironmentBinding(ctx, org, project, "sync", "dev", "acme-postgres", model.UpsertDataSourceBinding{DataSourceSlug: "physical"}, actor)
	require.NoError(t, err)
	restored, err := svc.Deploy(ctx, org, project, "dev", "sync", actor, pipelinesvc.DeployPipelineRequest{SpecHash: version.SpecHash, ResolutionDigest: &original, ExpectedRevision: &deployed.Revision})
	require.NoError(t, err)
	require.EqualValues(t, 2, restored.Revision)
	require.NotEqual(t, original, *restored.CurrentResolutionDigest)
	exact, err := deploymentresolutions.NewRepository().Find(ctx, pool, org, project, "dev", "sync", *restored.CurrentResolutionDigest)
	require.NoError(t, err)
	resolution, err := deploymentplan.Read(exact, *restored.CurrentResolutionDigest)
	require.NoError(t, err)
	require.Equal(t, original, resolution.RevalidatedFrom)
	if rich {
		pinnedSlug := slug.Slug("pinned-version")
		var pinned *pipelinesvc.PipelineRunDetail
		if event {
			pinned, err = svc.ExecuteVerifiedEvent(ctx, org, project, "dev", "sync", &pinnedSlug, actor, source, nil, json.RawMessage(richcontractfixture.ValidJSON))
		} else {
			pinned, err = svc.ExecuteVersion(ctx, org, project, "dev", "sync", version.SpecHash, &pinnedSlug, nil, actor, source, nil)
		}
		require.NoError(t, err)
		require.Equal(t, version.ID, pinned.Metadata.PipelineVersionID)
		require.Equal(t, *restored.CurrentResolutionDigest, pinned.Timeline.DeploymentResolutionDigest)
		require.Equal(t, 4, calls)
	}
	dispatches := calls

	_, err = pool.Exec(ctx, `UPDATE pipeline_compiled_plans SET materialization='corrupt' WHERE digest=$1`, compiled)
	require.NoError(t, err)
	_, err = svc.Execute(ctx, org, project, "dev", "sync", nil, nil, actor, source, nil)
	require.ErrorIs(t, err, deploymentplan.ErrInvalid)
	require.Equal(t, dispatches, calls)
}

// This adapter isolates transaction composition from closure snapshot persistence,
// whose real query/locking behavior is covered by the repository E2E suite.
type completionValidationRepository struct {
	pipelinetriggers.Repository
	snapshot *model.PipelineCompletionSnapshot
}

func (r completionValidationRepository) LoadCompletionSnapshot(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, int) (*model.PipelineCompletionSnapshot, error) {
	return r.snapshot, nil
}
