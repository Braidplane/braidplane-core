package pipelineruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func checkTestRequest(t *testing.T) RunRequest {
	t.Helper()
	contract := operationTestContract(t, `{"name":"rows","representation":"rows","fields":[{"name":"id","type":"integer","required":true}]}`)
	p := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}, Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationInline: map[string]any{"statement": `{"sql":"SELECT id"}`}, OutputContract: contract}},
		{Kind: "transform", Slug: "project", Transform: &pipelinespec.TransformSpec{SQL: "SELECT id FROM orders", OutputKind: model.TransformOutputKindRows, Inputs: map[string]pipelinespec.StepInput{"orders": {FromStep: "fetch"}}, InputContracts: map[string]*pipelinespec.InputContractAttachment{"orders": {Contract: contract}}, OutputContract: contract}},
		{Kind: "sink", Slug: "deliver", Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: "project"}, DataSourceSlug: "db", OperationType: "sql", OperationInline: map[string]any{"statement": `{"sql":"INSERT INTO orders VALUES (1)"}`}, InputContract: contract, IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent}},
	}}
	return RunRequest{OrgSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "staging", RunSlug: "checks-test", Pipeline: p}
}

func checkTestRunner(t *testing.T, store pipelinerun.RunStore, sink SinkRuntime) *Runner {
	t.Helper()
	executor := adaptTestQueryExecutor(&testutil.FakeQueryExecutor{ExecuteInlineFn: func(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error) {
		return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": 1}}, ColumnNames: []string{"id"}, RowCount: 1}, 0, nil), nil
	}})
	runner, err := NewRunner(executor, clock.New(), store, compositionDataSourceService{}, sink)
	require.NoError(t, err)
	return runner
}

// Capture bytes rather than pointers: a later in-memory pass must not mutate
// what the test claims was already durably committed.
func durableCheckStore(t *testing.T, fail func(*pipelinerun.RunResult) bool) (*fakeRunStore, *[][]byte) {
	t.Helper()
	saved := new([][]byte)
	store := &fakeRunStore{persistRunFn: func(_ context.Context, _, _, _ slug.Slug, _ *pipelinespec.Pipeline, run *pipelinerun.RunResult) error {
		if fail != nil && fail(run) {
			return xerror.Unexpected()
		}
		raw, err := json.Marshal(run.Timeline)
		require.NoError(t, err)
		*saved = append(*saved, raw)
		return nil
	}}
	return store, saved
}

func TestStructuralChecksDurableBeforeSinkAndSurviveItsFailure(t *testing.T) {
	store, saved := durableCheckStore(t, nil)
	runner := checkTestRunner(t, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
		var durable pipelinerun.RunTimeline
		require.NoError(t, json.Unmarshal((*saved)[len(*saved)-1], &durable))
		require.Len(t, durable.StructuralChecks, 4)
		for _, check := range durable.StructuralChecks {
			require.Equal(t, pipelinerun.CheckStatusPassed, check.Status)
		}
		return xerror.ConnectionFailed(xerror.DataSource)
	}})
	run, err := runner.Run(t.Context(), checkTestRequest(t))
	require.Error(t, err)
	require.NotNil(t, run)
	require.Equal(t, "failed", run.Timeline.Status)
	for _, check := range run.Timeline.StructuralChecks {
		require.Equal(t, pipelinerun.CheckStatusPassed, check.Status)
		require.NotEmpty(t, check.EvidenceDigest)
		require.Equal(t, 1, check.Evidence.Rows)
		require.Equal(t, "full", check.Window.Kind)
	}
	encoded, err := json.Marshal(run.Timeline.StructuralChecks)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "SELECT")
	require.NotContains(t, string(encoded), "INSERT")
}

func TestStructuralCheckPersistenceFailureBlocksEffectsAndPass(t *testing.T) {
	failed := false
	store, saved := durableCheckStore(t, func(run *pipelinerun.RunResult) bool {
		for _, check := range run.Timeline.StructuralChecks {
			if !failed && check.Status == pipelinerun.CheckStatusPassed {
				failed = true
				return true
			}
		}
		return false
	})
	sinkCalls := 0
	runner := checkTestRunner(t, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error { sinkCalls++; return nil }})
	run, err := runner.Run(t.Context(), checkTestRequest(t))
	require.ErrorIs(t, err, errCheckPersistence)
	require.Zero(t, sinkCalls)
	require.NotNil(t, run)
	require.Equal(t, pipelinerun.CheckStatusError, run.Timeline.StructuralChecks[0].Status)
	require.Equal(t, "persistence_failed", run.Timeline.StructuralChecks[0].ReasonCode)
	require.Nil(t, run.Timeline.StructuralChecks[0].Evidence)
	for _, raw := range *saved {
		var durable pipelinerun.RunTimeline
		require.NoError(t, json.Unmarshal(raw, &durable))
		require.NotEqual(t, pipelinerun.CheckStatusPassed, durable.StructuralChecks[0].Status)
	}
}

func TestStructuralCheckAdmissionAndDownstreamSkipped(t *testing.T) {
	req := checkTestRequest(t)
	req.Pipeline.Steps[0] = pipelinespec.Step{Kind: "input", Slug: "fetch", Input: &pipelinespec.InputSpec{OutputContract: &payload.Contract{Name: "event", Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}}}
	input, err := pipelinerun.NewVerifiedJSONEventInput("fetch", json.RawMessage(`{"id":"private-failed-value"}`))
	require.NoError(t, err)
	req.EventInput = input
	store, saved := durableCheckStore(t, nil)
	sinkCalls := 0
	priorOutput := "prior-good"
	runner := checkTestRunner(t, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error { sinkCalls++; priorOutput = "unexpected"; return nil }})
	_, err = runner.Run(t.Context(), req)
	require.ErrorIs(t, err, xerror.InvalidInput(xerror.Event))
	require.Zero(t, sinkCalls)
	require.Equal(t, "prior-good", priorOutput)
	var durable pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal((*saved)[len(*saved)-1], &durable))
	require.Equal(t, "failed", durable.Status)
	require.NotNil(t, durable.Failure)
	require.Equal(t, slug.Slug("fetch"), durable.Failure.Step)
	require.Equal(t, pipelinerun.FailureReasonCodeEventInputRejected, durable.Failure.ReasonCode)
	require.Equal(t, "source", durable.Failure.Side)
	require.Contains(t, durable.Failure.Error, "Event input was rejected")
	require.Contains(t, durable.Failure.ActionHint, "event admission")
	require.True(t, durable.Failure.AutomaticRetryProhibited)
	require.Equal(t, "event_admission", durable.StructuralChecks[0].Phase)
	require.Equal(t, pipelinerun.CheckStatusFailed, durable.StructuralChecks[0].Status)
	for _, check := range durable.StructuralChecks[1:] {
		require.Equal(t, pipelinerun.CheckStatusSkipped, check.Status)
	}
	require.NotContains(t, string((*saved)[len(*saved)-1]), "private-failed-value")
}

func TestStructuralCheckInterruptedAttemptAndRetryIdentities(t *testing.T) {
	req := checkTestRequest(t)
	store, saved := durableCheckStore(t, nil)
	runner := checkTestRunner(t, store, &fakeSinkRuntime{})
	run, err := runner.Run(t.Context(), req)
	require.NoError(t, err)
	originalID := run.Timeline.StructuralChecks[0].ID
	// Simulate the last durable state before a process crash during evaluation.
	interrupted := run.Timeline
	interrupted.Status = "running"
	interrupted.Failure = &pipelinerun.FailureContext{AutomaticRetryProhibited: true}
	interrupted.StructuralChecks[1].Status = pipelinerun.CheckStatusUnavailable
	interrupted.StructuralChecks[1].ReasonCode = "evaluating"
	store.timelines[req.RunSlug] = interrupted
	_, err = runner.Run(t.Context(), req)
	require.ErrorIs(t, err, errAttemptInterrupted)
	var recovered pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal((*saved)[len(*saved)-1], &recovered))
	require.Equal(t, "failed", recovered.Status)
	require.True(t, recovered.Failure.OutcomeUncertain)
	require.True(t, recovered.Failure.AutomaticRetryProhibited)
	require.Equal(t, pipelinerun.CheckStatusPassed, recovered.StructuralChecks[0].Status)
	require.Equal(t, pipelinerun.CheckStatusError, recovered.StructuralChecks[1].Status)
	require.Equal(t, "evaluation_interrupted", recovered.StructuralChecks[1].ReasonCode)
	require.Equal(t, originalID, recovered.StructuralChecks[0].ID)
	req.RunSlug = "checks-retry"
	req.Source.AttemptNumber = 2
	run, err = runner.Run(t.Context(), req)
	require.NoError(t, err)
	require.NotEqual(t, originalID, run.Timeline.StructuralChecks[0].ID)
	require.Equal(t, 2, run.Timeline.StructuralChecks[0].AttemptNumber)
}

func TestStructuralCheckCanceledEvaluationRemainsIncomplete(t *testing.T) {
	store, saved := durableCheckStore(t, nil)
	runner := checkTestRunner(t, store, &fakeSinkRuntime{})
	req := checkTestRequest(t)
	req.Source = pipelinerun.NormalizeAttemptSource(req.Source, "checks-test")
	timeline := pipelinerun.RunTimeline{RunSlug: req.RunSlug, Status: "running"}
	journal, err := newCheckJournal(runner, req, req.Pipeline, nil, &timeline, time.Now(), runtimeSecretRedactor{})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	source := pipelinerun.NewTransformStepResult(&payload.Output{OutputKind: model.TransformOutputKindRows, Rows: []map[string]any{{"id": 1}}}, 0, nil)
	err = journal.evaluate(ctx, "fetch", "step", "producer_output", "", source, func() error { cancel(); return context.Canceled })
	require.Error(t, err)
	var durable pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal((*saved)[len(*saved)-1], &durable))
	require.Equal(t, pipelinerun.CheckStatusError, durable.StructuralChecks[0].Status)
	require.Equal(t, "evaluation_interrupted", durable.StructuralChecks[0].ReasonCode)
}

func TestStructuralCheckEmptyTruncatedBackfillAndUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rows    []map[string]any
		hasMore bool
		want    string
	}{
		{"empty", nil, false, "empty_batch"}, {"truncated", []map[string]any{{"id": 1}}, true, "conformant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := checkTestRequest(t)
			req.Source = pipelinerun.NormalizeAttemptSource(req.Source, "checks-test")
			req.Incremental = pipelinerun.IncrementalRunOptions{BackfillSeriesSlug: "backfill-one", BackfillChunkIndex: 2, SourceWindows: map[slug.Slug]pipelinerun.IncrementalSourceWindow{"fetch": {LowerBound: json.RawMessage(`"private-cursor"`), UpperBound: json.RawMessage(`"private-end"`)}}}
			store, _ := durableCheckStore(t, nil)
			runner := checkTestRunner(t, store, &fakeSinkRuntime{})
			timeline := pipelinerun.RunTimeline{RunSlug: req.RunSlug, Status: "running"}
			journal, err := newCheckJournal(runner, req, req.Pipeline, nil, &timeline, time.Now(), runtimeSecretRedactor{})
			require.NoError(t, err)
			source := pipelinerun.NewStepResult(result.NewObjectRowsResult("test", &result.ObjectRowsResult{Rows: tc.rows, HasMore: tc.hasMore}, 0, nil))
			require.NoError(t, journal.evaluate(t.Context(), "fetch", "step", "producer_output", "", source, func() error { return nil }))
			check := timeline.StructuralChecks[0]
			require.Equal(t, tc.want, check.ReasonCode)
			require.Equal(t, "backfill", check.Window.Kind)
			require.Equal(t, []slug.Slug{"fetch"}, check.Window.SourceSteps)
			require.True(t, check.Window.HasLowerBound)
			require.True(t, check.Window.HasUpperBound)
			require.Equal(t, 2, check.Window.BackfillChunkIndex)
			if tc.hasMore {
				require.Equal(t, "truncated_batch", check.Evidence.BatchScope)
			}
			raw, err := json.Marshal(check)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "private-")
			require.Error(t, journal.evaluate(t.Context(), "deliver", "step", "pre_sink", "", nil, func() error { return nil }))
			require.Equal(t, pipelinerun.CheckStatusUnavailable, timeline.StructuralChecks[3].Status)
		})
	}
}

func TestProjectedStructuralBatchEnforcesNestedTypesAndUniqueness(t *testing.T) {
	integer := &payload.StructuralType{Kind: payload.StructuralInteger, Bits: 64}
	object := &payload.StructuralType{Kind: payload.StructuralObject, Additional: payload.StructuralClosed, Properties: []payload.StructuralProperty{{Name: "id", Required: true, Type: integer}}}
	structure := &payload.Structure{Contract: "orders", Revision: "v1", Object: "row", Representation: model.ContractRepresentationRows, Root: object}
	require.NoError(t, validateProjectedBatch(structure, []map[string]any{{"id": int64(1)}}))
	require.Error(t, validateProjectedBatch(structure, []map[string]any{{"id": "private-wrong-type"}}))
	require.Error(t, validateProjectedBatch(structure, []map[string]any{{"id": 1, "extra": true}}))
	structure.Root = &payload.StructuralType{Kind: payload.StructuralArray, Items: object, Unique: true}
	require.Error(t, validateProjectedBatch(structure, []map[string]any{{"id": int64(1)}, {"id": int64(1)}}))
}

func TestStructuralCheckFinalizationRetainsBackfillReplayAndFailureMetadata(t *testing.T) {
	req := checkTestRequest(t)
	source, from := "original-run", "project"
	req.ReplayOfRunSlug, req.ReplayFromStep = &source, &from
	req.Incremental = pipelinerun.IncrementalRunOptions{BackfillSeriesSlug: "backfill-series", BackfillChunkIndex: 2, BackfillChunkCount: 3}
	req.Source = pipelinerun.NormalizeAttemptSource(req.Source, "checks-test")
	var persisted *pipelinerun.RunResult
	store, _ := durableCheckStore(t, func(run *pipelinerun.RunResult) bool { persisted = run; return false })
	runner := checkTestRunner(t, store, &fakeSinkRuntime{})
	timeline := pipelinerun.RunTimeline{Admissions: []deploymentplan.Admission{{Step: "fetch", DataSource: "db", CredentialReference: "revision-one"}}, RunSlug: req.RunSlug, Status: "failed", Failure: &pipelinerun.FailureContext{Kind: failureKindPlatform, Error: errAttemptInterrupted.Error()}}
	journal, err := newCheckJournal(runner, req, req.Pipeline, nil, &timeline, time.Now(), runtimeSecretRedactor{})
	require.NoError(t, err)
	require.NoError(t, journal.persist(t.Context()))
	require.Equal(t, []deploymentplan.Admission{{Step: "fetch", DataSource: "db", CredentialReference: "revision-one"}}, persisted.Timeline.Admissions)
	require.Equal(t, "backfill", persisted.Metadata.Incremental.Mode)
	require.Equal(t, "backfill-series", persisted.Metadata.Incremental.BackfillSeriesSlug)
	require.Equal(t, 2, persisted.Metadata.Incremental.BackfillChunkIndex)
	require.Equal(t, 3, persisted.Metadata.Incremental.BackfillChunkCount)
	require.Equal(t, &source, persisted.Metadata.ReplayOfRunSlug)
	require.Equal(t, &from, persisted.Metadata.ReplayFromStep)
	require.Equal(t, failureKindPlatform, persisted.Timeline.Failure.Kind)
	require.NotEmpty(t, persisted.Timeline.Failure.Error)
}

func TestStructuralCheckMissingEventKeepsRequiredInputError(t *testing.T) {
	req := checkTestRequest(t)
	req.Pipeline.Steps[0] = pipelinespec.Step{Kind: "input", Slug: "fetch", Input: &pipelinespec.InputSpec{OutputContract: &payload.Contract{Name: "event", Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}}}
	store, saved := durableCheckStore(t, nil)
	runner := checkTestRunner(t, store, &fakeSinkRuntime{})
	_, err := runner.Run(t.Context(), req)
	require.ErrorIs(t, err, xerror.InvalidRequired(xerror.Event))
	require.ErrorContains(t, err, `pipeline input "fetch" is required`)
	var durable pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal((*saved)[len(*saved)-1], &durable))
	require.Equal(t, pipelinerun.CheckStatusUnavailable, durable.StructuralChecks[0].Status)
}

func TestStructuralCheckTerminalPersistenceFailureHasOneCompletionEvent(t *testing.T) {
	failed := false
	store, saved := durableCheckStore(t, func(run *pipelinerun.RunResult) bool {
		if run.Metadata.Status == "succeeded" && !failed {
			failed = true
			return true
		}
		return false
	})
	runner := checkTestRunner(t, store, &fakeSinkRuntime{})
	run, err := runner.Run(t.Context(), checkTestRequest(t))
	require.Error(t, err)
	require.Nil(t, run)
	// Recover from the last durable timeline, without inventing terminal or
	// checkpoint commit events from the failed transaction.
	_, err = runner.Run(t.Context(), checkTestRequest(t))
	require.ErrorIs(t, err, errAttemptInterrupted)

	var durable pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal((*saved)[len(*saved)-1], &durable))
	require.Equal(t, "failed", durable.Status)
	completions := 0
	for _, event := range durable.Events {
		if event.Type == "run_completed" {
			completions++
			require.Equal(t, "failed", event.Status)
		}
	}
	require.Equal(t, 1, completions)
	require.NotNil(t, durable.Failure)
	for _, check := range durable.StructuralChecks {
		require.Equal(t, pipelinerun.CheckStatusPassed, check.Status)
	}
}

func TestStructuralCheckProjectionRejectsAmbiguousExecutableShapes(t *testing.T) {
	shape := &payload.Structure{Contract: "orders", Revision: "v1", Object: "row", Representation: model.ContractRepresentationRows, Root: &payload.StructuralType{Kind: payload.StructuralObject, Additional: payload.StructuralClosed}}
	selected := odcs.ProjectedInput{Contract: "orders", Revision: "v1", ObjectID: "row", Structure: shape}
	plan := &odcs.ProjectionPlan{Inputs: []odcs.ProjectedInput{selected, selected}}
	ref := &pipelinespec.ODCSContractRef{ContractSlug: "orders", RevisionSlug: "v1", SchemaObjectID: "row"}
	contract := &payload.Contract{Representation: model.ContractRepresentationRows}
	got, digest, err := structuralCheckProjection(plan, ref, contract)
	require.NoError(t, err)
	require.Same(t, shape, got)
	require.NotEmpty(t, digest)
	different := *shape
	different.Root = &payload.StructuralType{Kind: payload.StructuralObject, Additional: payload.StructuralOpen}
	plan.Inputs[1].Structure = &different
	_, _, err = structuralCheckProjection(plan, ref, contract)
	require.ErrorIs(t, err, contractplan.ErrInvalid)
	contract.Representation = model.ContractRepresentationCSV
	_, _, err = structuralCheckProjection(plan, ref, contract)
	require.ErrorIs(t, err, contractplan.ErrInvalid)
}

func TestCompiledStepCheckRetainsPinAndExecutesRichProjection(t *testing.T) {
	document := json.RawMessage(`{"apiVersion":"v3.2.0","kind":"DataContract","id":"dataset","version":"1","schema":[{"id":"row","name":"Row","logicalType":"object","properties":[{"id":"id","name":"id","logicalType":"integer","required":true}]}]}`)
	record, err := odcs.Record(document, "dataset")
	require.NoError(t, err)
	converted, err := odcs.Convert(document, "row")
	require.NoError(t, err)
	require.NotNil(t, converted.Shape)
	converted.Shape.Representation = model.ContractRepresentationRows
	converted.Shape.RootShape = model.ContractRootShapeUnknown
	ref := &pipelinespec.ODCSContractRef{ContractSlug: "dataset-contract", RevisionSlug: "revision-one", SchemaObjectID: "row", Representation: model.ContractRepresentationRows}
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "compiled-checks", Name: "Compiled checks"}, Steps: []pipelinespec.Step{{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationInline: map[string]any{"statement": `{"sql":"SELECT id"}`}, OutputContractRef: ref}}}}
	authored, err := json.Marshal(pipeline)
	require.NoError(t, err)
	raw, digest, err := contractplan.Compile(contractplan.Input{
		Source: contractplan.Source{Commit: strings.Repeat("a", 40), Path: "pipelines/checks.json"}, Authored: authored,
		Pins:       []model.PinnedODCSPort{{StepSlug: "fetch", Port: "output", ContractSlug: ref.ContractSlug, RevisionSlug: ref.RevisionSlug, SchemaObjectID: ref.SchemaObjectID, Digest: record.Digest, CompilerID: odcs.CompilerID, Shape: converted.Shape}},
		Projection: &odcs.ProjectionRequest{Inputs: []odcs.ProjectionInput{{Contract: ref.ContractSlug.String(), Revision: ref.RevisionSlug.String(), ObjectID: ref.SchemaObjectID, Digest: record.Digest, Document: document, Mapping: odcs.ProjectionMapping{ID: "synthetic/rows/v1", Representation: model.ContractRepresentationRows, Types: map[payload.StructuralKind]bool{payload.StructuralObject: true, payload.StructuralInteger: true}}}}},
	})
	require.NoError(t, err)
	plan, err := contractplan.Read(raw, digest)
	require.NoError(t, err)
	runtime, err := plan.RuntimePipeline()
	require.NoError(t, err)
	require.Nil(t, runtime.Steps[0].Query.OutputContractRef, "runtime readers intentionally clear authoring references")
	expectedProjection, err := checkDigest(plan.Projection.Inputs[0].Structure)
	require.NoError(t, err)
	for _, test := range []struct {
		name  string
		value int64
		valid bool
	}{{"valid", 1, true}, {"width-violation", 1 << 40, false}} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := durableCheckStore(t, nil)
			runner := checkTestRunner(t, store, &fakeSinkRuntime{})
			runner.executor = adaptTestQueryExecutor(&testutil.FakeQueryExecutor{ExecuteInlineFn: func(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error) {
				return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": test.value}}, ColumnNames: []string{"id"}, RowCount: 1}, 0, nil), nil
			}})
			run, err := runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "staging", RunSlug: "compiled-" + test.name, CompiledPlan: raw, CompiledPlanDigest: digest})
			if test.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrQueryOutputContractSchemaMismatch)
			}
			require.NotNil(t, run)
			if !test.valid {
				require.Equal(t, failureKindPlatform, run.Timeline.Failure.Kind)
				require.Equal(t, pipelinerun.FailureReasonCodeSourceResultShapeMismatch, run.Timeline.Failure.ReasonCode)
			}
			require.Len(t, run.Timeline.StructuralChecks, 1)
			check := run.Timeline.StructuralChecks[0]
			require.Contains(t, check.Declaration, "dataset-contract/revision-one/row")
			require.Equal(t, expectedProjection, check.ProjectionDigest)
			if test.valid {
				require.Equal(t, pipelinerun.CheckStatusPassed, check.Status)
			} else {
				require.Equal(t, pipelinerun.CheckStatusFailed, check.Status)
			}
		})
	}
}

func TestCompiledOperationDefaultsDoNotInventStepDeclarations(t *testing.T) {
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "operation-only", Name: "Operation only"}, Steps: []pipelinespec.Step{{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "db", OperationType: "sql", OperationRef: "orders"}}}}
	req := compiledOperationRequest(t, pipeline, map[slug.Slug]contractplan.OperationContent{"fetch": {Data: json.RawMessage(`{"statement":"{\"sql\":\"SELECT id\"}"}`), OutputContract: json.RawMessage(`{"name":"operation","representation":"rows","fields":[{"name":"id","type":"integer","required":true}]}`)}})
	store, _ := durableCheckStore(t, nil)
	runner := checkTestRunner(t, store, &fakeSinkRuntime{})
	runner.executor = capturedTestExecutor{queryExecutor: runner.executor, execute: func(contractplan.OperationContent) (*result.ExecutionResult, error) {
		return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": 1}}, ColumnNames: []string{"id"}, RowCount: 1}, 0, nil), nil
	}}
	run, err := runner.Run(t.Context(), req)
	require.NoError(t, err)
	require.Len(t, run.Timeline.StructuralChecks, 1)
	require.Equal(t, "operation", run.Timeline.StructuralChecks[0].Origin)
}

func TestAdmissionPersistenceFailureDoesNotBecomeInputRejection(t *testing.T) {
	req := checkTestRequest(t)
	req.Pipeline.Steps[0] = pipelinespec.Step{Kind: "input", Slug: "fetch", Input: &pipelinespec.InputSpec{OutputContract: &payload.Contract{Name: "event", Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}}}
	input, err := pipelinerun.NewVerifiedJSONEventInput("fetch", json.RawMessage(`{"id":"private-failed-value"}`))
	require.NoError(t, err)
	req.EventInput = input
	failed := false
	store, saved := durableCheckStore(t, func(run *pipelinerun.RunResult) bool {
		if !failed && run.Timeline.StructuralChecks[0].Status == pipelinerun.CheckStatusFailed {
			failed = true
			return true
		}
		return false
	})
	sinkCalls := 0
	runner := checkTestRunner(t, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error { sinkCalls++; return nil }})
	_, err = runner.Run(t.Context(), req)
	require.ErrorIs(t, err, xerror.Unexpected())
	require.True(t, failed)
	require.Zero(t, sinkCalls)
	var durable pipelinerun.RunTimeline
	require.NoError(t, json.Unmarshal((*saved)[len(*saved)-1], &durable))
	require.Equal(t, pipelinerun.CheckStatusError, durable.StructuralChecks[0].Status)
	require.Equal(t, "persistence_failed", durable.StructuralChecks[0].ReasonCode)
	require.NotEqual(t, pipelinerun.FailureReasonCodeEventInputRejected, durable.Failure.ReasonCode)
	require.NotContains(t, string((*saved)[len(*saved)-1]), "private-failed-value")
}
