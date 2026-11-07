package pipelineruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/writegate"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/stretchr/testify/require"
)

type capturedTestExecutor struct {
	queryExecutor
	execute func(contractplan.OperationContent) (*result.ExecutionResult, error)
}

func (e capturedTestExecutor) ExecuteSnapshot(_ context.Context, _, _ slug.Slug, content contractplan.OperationContent, _ map[string]any, _ *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	output, err := e.execute(content)
	return output, nil, err
}

func operationTestContract(t *testing.T, raw string) *payload.Contract {
	t.Helper()
	var c payload.Contract
	require.NoError(t, json.Unmarshal([]byte(raw), &c))
	return &c
}

func compiledOperationRequest(t *testing.T, p *pipelinespec.Pipeline, contents map[slug.Slug]contractplan.OperationContent) RunRequest {
	t.Helper()
	authored, err := json.Marshal(p)
	require.NoError(t, err)
	input := contractplan.Input{Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/orders.json"}, Authored: authored}
	for _, step := range p.Steps {
		role := "query"
		ref := slug.Slug("orders")
		if step.Sink != nil {
			role = "sink"
			ref = "save"
		}
		content, ok := contents[step.Slug]
		if !ok {
			continue
		}
		raw, err := json.Marshal(content)
		require.NoError(t, err)
		input.Operations = append(input.Operations, contractplan.Operation{Step: step.Slug, Role: role, Slug: ref, DataSource: "db", Revision: "v1", Content: raw})
	}
	raw, id, err := contractplan.Compile(input)
	require.NoError(t, err)
	return RunRequest{OrgSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "staging", RunSlug: "saved-operation-test", CompiledPlan: raw, CompiledPlanDigest: id}
}

func TestCompiledQueryChecksBothOriginsBeforeDownstreamUse(t *testing.T) {
	saved := json.RawMessage(`{"name":"operation","representation":"rows","fields":[{"name":"id","type":"number","required":true,"validations":{"min":0}}]}`)
	step := operationTestContract(t, `{"name":"step","representation":"rows","fields":[{"name":"id","type":"integer","required":true},{"name":"amount","type":"number","required":true}]}`)
	p := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}, Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationRef: "orders", OutputContract: step}},
		{Kind: "sink", Slug: "deliver", Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: "fetch"}, DataSourceSlug: "db", OperationType: "sql", OperationRef: "save", IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent}},
	}}
	content := contractplan.OperationContent{Data: json.RawMessage(`{"statement":"{\"sql\":\"SELECT captured\"}","row_limit":7}`), OutputContract: saved}
	for _, tc := range []struct {
		name   string
		row    map[string]any
		origin string
	}{
		{"both hold", map[string]any{"id": 1, "amount": 2}, ""},
		{"operation rejected", map[string]any{"id": -1, "amount": 2}, "operation output"},
		{"stronger step rejected", map[string]any{"id": 1}, "step output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := compiledOperationRequest(t, p, map[slug.Slug]contractplan.OperationContent{"fetch": content, "deliver": {Data: content.Data, ExpectedInputContract: saved}})
			calls := 0
			executor := capturedTestExecutor{queryExecutor: adaptTestQueryExecutor(&testutil.FakeQueryExecutor{ExecuteFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, map[string]any) (*result.ExecutionResult, error) {
				t.Fatal("mutable lookup dispatched")
				return nil, context.Canceled
			}}), execute: func(snapshot contractplan.OperationContent) (*result.ExecutionResult, error) {
				calls++
				require.JSONEq(t, string(content.Data), string(snapshot.Data))
				return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{tc.row}, ColumnNames: []string{"id", "amount"}, RowCount: 1}, 0, nil), nil
			}}
			sinkCalls := 0
			sink := &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error { sinkCalls++; return nil }}
			runner, err := NewRunner(executor, nil, NewNoopRunStore(), compositionDataSourceService{}, sink)
			require.NoError(t, err)
			output, err := runner.Run(t.Context(), req)
			require.Equal(t, 1, calls)
			if tc.name == "both hold" {
				require.NoError(t, err)
				require.Equal(t, "succeeded", output.Timeline.Status)
				require.Equal(t, 1, sinkCalls)
			} else {
				require.Error(t, err)
				require.Zero(t, sinkCalls)
				if tc.origin != "" {
					require.Contains(t, err.Error(), tc.origin)
				}
			}
		})
	}
}

func TestCompiledSinkChecksOperationAndStepBeforeDispatch(t *testing.T) {
	for _, operationRequiresAmount := range []bool{true, false} {
		name := "operation input"
		operationField, stepField := "amount", "id"
		if !operationRequiresAmount {
			name = "step input"
			operationField, stepField = "id", "amount"
		}
		t.Run(name, func(t *testing.T) {
			makeContract := func(field string) *payload.Contract {
				return operationTestContract(t, `{"name":"requirement","representation":"rows","fields":[{"name":"`+field+`","type":"integer","required":true}]}`)
			}
			p := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}, Steps: []pipelinespec.Step{
				{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationRef: "orders"}},
				{Kind: "sink", Slug: "deliver", Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: "fetch"}, DataSourceSlug: "db", OperationType: "sql", OperationRef: "save", InputContract: makeContract(stepField), IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent}},
			}}
			input, err := json.Marshal(makeContract(operationField))
			require.NoError(t, err)
			data := json.RawMessage(`{"statement":"{\"sql\":\"SELECT captured\"}"}`)
			req := compiledOperationRequest(t, p, map[slug.Slug]contractplan.OperationContent{"fetch": {Data: data}, "deliver": {Data: data, ExpectedInputContract: input}})
			executor := capturedTestExecutor{queryExecutor: adaptTestQueryExecutor(&testutil.FakeQueryExecutor{}), execute: func(contractplan.OperationContent) (*result.ExecutionResult, error) {
				return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": 1}}, ColumnNames: []string{"id"}, RowCount: 1}, 0, nil), nil
			}}
			dispatched := false
			sink := &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error { dispatched = true; return nil }}
			runner, err := NewRunner(executor, nil, NewNoopRunStore(), compositionDataSourceService{}, sink)
			require.NoError(t, err)
			_, err = runner.Run(t.Context(), req)
			require.Error(t, err)
			require.False(t, dispatched)
		})
	}
}

type capturedSinkTestExecutor struct {
	sinkQueryExecutor
	content *contractplan.OperationContent
}

func (e *capturedSinkTestExecutor) ExecuteSnapshot(ctx context.Context, _, _ slug.Slug, content contractplan.OperationContent, _ map[string]any, _ *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	if err := writegate.BeforeWrite(ctx); err != nil {
		return nil, nil, err
	}
	e.content = &content
	return result.NewSQLResult("postgres", &result.SQLResult{RowCount: 1}, 0, nil), nil, nil
}

func TestCompiledSinkDispatchUsesCapturedDefinition(t *testing.T) {
	executor := &capturedSinkTestExecutor{}
	input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassNaturallyIdempotent)
	input.OperationSnapshot = &contractplan.OperationContent{Data: json.RawMessage(`{"statement":"{\"sql\":\"INSERT captured\"}"}`)}
	require.NoError(t, NewSinkRuntime(executor).Execute(t.Context(), input))
	require.NotNil(t, executor.content)
	require.JSONEq(t, string(input.OperationSnapshot.Data), string(executor.content.Data))
}

func TestCompiledRecoveryRetainsOperationSnapshot(t *testing.T) {
	p := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}, Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationRef: "orders"}},
		{Kind: "sink", Slug: "deliver", Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: "fetch"}, DataSourceSlug: "db", OperationType: "sql", OperationRef: "save", IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent}},
	}}
	data := json.RawMessage(`{"statement":"{\"sql\":\"SELECT captured\"}"}`)
	req := compiledOperationRequest(t, p, map[slug.Slug]contractplan.OperationContent{"fetch": {Data: data}, "deliver": {Data: data, ExpectedInputContract: json.RawMessage(`{"name":"input","representation":"rows","fields":[{"name":"id","type":"integer","required":true}]}`)}})
	sourceCalls, sinkCalls := 0, 0
	executor := capturedTestExecutor{queryExecutor: adaptTestQueryExecutor(&testutil.FakeQueryExecutor{}), execute: func(contractplan.OperationContent) (*result.ExecutionResult, error) {
		sourceCalls++
		return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": 1}}, ColumnNames: []string{"id"}, RowCount: 1}, 0, nil), nil
	}}
	store := NewNoopRunStore()
	sink := &fakeSinkRuntime{executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
		sinkCalls++
		require.NotNil(t, input.OperationSnapshot)
		require.JSONEq(t, string(data), string(input.OperationSnapshot.Data))
		if sinkCalls == 1 {
			return context.DeadlineExceeded
		}
		return nil
	}}
	runner, err := NewRunner(executor, nil, store, compositionDataSourceService{}, sink)
	require.NoError(t, err)
	_, err = runner.Run(t.Context(), req)
	require.Error(t, err)
	checkpoint, err := store.GetCheckpoint(t.Context(), "acme", "etl", "staging", req.RunSlug)
	require.NoError(t, err)
	require.Equal(t, req.CompiledPlan, checkpoint.CompiledPlan)
	_, err = runner.Replay(t.Context(), ReplayRequest{OrgSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "staging", SourceRunSlug: req.RunSlug, ReplayRunSlug: "saved-operation-recovery", InputMode: ReplayInputModePinned})
	require.NoError(t, err)
	require.Equal(t, 1, sourceCalls)
	require.Equal(t, 2, sinkCalls)
}

func TestDeploymentRecoveryKeepsPinnedTargetAndPartialEffectIdentity(t *testing.T) {
	p := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}, Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationRef: "orders"}},
		{Kind: "sink", Slug: "deliver", Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: "fetch"}, DataSourceSlug: "db", OperationType: "sql", OperationRef: "save", IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent}},
	}}
	data := json.RawMessage(`{"statement":"{\"sql\":\"SELECT captured\"}"}`)
	req := compiledOperationRequest(t, p, map[slug.Slug]contractplan.OperationContent{"fetch": {Data: data}, "deliver": {Data: data, ExpectedInputContract: json.RawMessage(`{"name":"input","representation":"rows","fields":[{"name":"id","type":"integer","required":true}]}`)}})
	compiled, err := contractplan.Read(req.CompiledPlan, req.CompiledPlanDigest)
	require.NoError(t, err)
	executable, err := compiled.RuntimePipeline()
	require.NoError(t, err)
	executable.Steps[0].Query.DataSourceSlug = "pinned"
	executable.Steps[1].Sink.DataSourceSlug = "pinned"
	targets := []deploymentplan.Target{
		{Step: "fetch", Role: "query", Reference: "db", DataSource: "pinned", BindingRevision: "binding-one", ConfigDigest: "sha256:" + strings.Repeat("a", 64)},
		{Step: "deliver", Role: "sink", Reference: "db", DataSource: "pinned", BindingRevision: "binding-one", ConfigDigest: "sha256:" + strings.Repeat("a", 64)},
	}
	req.DeploymentPlan, req.DeploymentResolutionDigest, err = deploymentplan.Encode(deploymentplan.Plan{Organization: "acme", Project: "etl", Environment: "staging", CompiledPlan: req.CompiledPlan, TemplateDigest: req.CompiledPlanDigest, CompiledDigest: req.CompiledPlanDigest, Pipeline: executable, Targets: targets})
	require.NoError(t, err)
	store := NewNoopRunStore()
	sources, sinks := 0, 0
	var replayIdentity, idempotencyKey string
	executor := capturedTestExecutor{queryExecutor: adaptTestQueryExecutor(&testutil.FakeQueryExecutor{}), execute: func(contractplan.OperationContent) (*result.ExecutionResult, error) {
		sources++
		return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": 1}}, ColumnNames: []string{"id"}, RowCount: 1}, 0, nil), nil
	}}
	sink := &fakeSinkRuntime{executeFn: func(ctx context.Context, input *SinkRuntimeInput) error {
		sinks++
		require.Equal(t, slug.Slug("pinned"), input.SinkDataSourceSlug)
		got, err := deploymentplan.DispatchConfig(ctx, "acme", "pinned")
		require.NoError(t, err)
		require.Equal(t, targets[0].ConfigDigest, got)
		if sinks == 1 {
			replayIdentity, idempotencyKey = input.ReplayIdentity, input.IdempotencyKey
			return context.DeadlineExceeded
		}
		require.Equal(t, replayIdentity, input.ReplayIdentity)
		require.Equal(t, idempotencyKey, input.IdempotencyKey)
		return nil
	}}
	runner, err := NewRunner(executor, nil, store, compositionDataSourceService{}, sink)
	require.NoError(t, err)
	first, err := runner.Run(t.Context(), req)
	require.Error(t, err)
	require.Equal(t, req.DeploymentResolutionDigest, first.Timeline.DeploymentResolutionDigest)
	checkpoint, err := store.GetCheckpoint(t.Context(), "acme", "etl", "staging", req.RunSlug)
	require.NoError(t, err)
	require.Equal(t, req.DeploymentPlan, checkpoint.DeploymentPlan)
	require.Equal(t, req.DeploymentResolutionDigest, checkpoint.DeploymentResolutionDigest)
	_, err = runner.Replay(t.Context(), ReplayRequest{OrgSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "staging", SourceRunSlug: req.RunSlug, ReplayRunSlug: "recovery", InputMode: ReplayInputModePinned})
	require.NoError(t, err)
	require.Equal(t, 1, sources)
	require.Equal(t, 2, sinks)
}

func TestCompiledInlineS3SinkUsesCapturedDefinition(t *testing.T) {
	executor := &capturedSinkTestExecutor{}
	input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassDeduplicatedByLedger)
	input.SinkBackend = model.SinkBackendS3
	input.SinkOperationRef = ""
	input.SinkOperationInline = map[string]any{"operation": "write", "key": "mutable.json"}
	input.SinkConfig = map[string]any{"key": "captured.json"}
	input.OperationSnapshot = &contractplan.OperationContent{Data: json.RawMessage(`{"statement":"{\"operation\":\"write\",\"key\":\"captured.json\"}"}`)}
	ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{Claimed: true}}
	require.NoError(t, NewSinkRuntime(executor, ledger).Execute(t.Context(), input))
	require.Equal(t, input.OperationSnapshot, executor.content)
	require.Equal(t, 1, ledger.beginCalls)
	require.Equal(t, 1, ledger.markCalls)
}
