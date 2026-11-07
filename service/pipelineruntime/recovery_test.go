package pipelineruntime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	executionruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/stretchr/testify/require"
)

func TestRecoveryPreservesMissingCheckpointCause(t *testing.T) {
	t.Parallel()
	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{}, &testutil.FakeClock{})

	outcome, err := runner.Recover(t.Context(), ReplayRequest{
		OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod",
		SourceRunSlug: "missing-run", InputMode: ReplayInputModePinned,
	})
	require.Nil(t, outcome)
	require.ErrorIs(t, err, ErrReplayRunNotFound)
	require.ErrorIs(t, err, pipelinerun.ErrReplayCheckpointNotFound)
	require.ErrorContains(t, err, pipelinerun.ErrReplayCheckpointNotFound.Error())
	require.Equal(t, "Retained repair inputs are unavailable or expired. Reconcile provider effects before starting a new evaluation, which reads sources again.", xerror.PublicDetail(err))
}

func TestRecoveryRequiresRetainedFileBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		content []byte
	}{{name: "metadata-only", content: []byte("receipt")}, {name: "empty-file"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			content := tc.content
			metadata := filepayload.NewFilePayloadMetadata("receipt.bin", "application/octet-stream", content)
			read := pipelinerun.NewStepResult(result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200, File: &metadata, RawBody: content,
			}, 0, nil))
			encoded, err := json.Marshal(read)
			require.NoError(t, err)
			var retained pipelinerun.StepResult
			require.NoError(t, json.Unmarshal(encoded, &retained))
			storedPayload, ok := retained.Payload()
			require.True(t, ok)
			require.Empty(t, storedPayload.FileBytes)
			store := &fakeRunStore{checkpoints: map[string]pipelinerun.ReplayCheckpoint{
				"original": {Pipeline: &pipelinespec.Pipeline{
					Metadata: pipelinespec.Metadata{Slug: "archive", Name: "Archive"},
					Steps: []pipelinespec.Step{
						{Kind: "query", Slug: "read", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OperationType: "http", OperationRef: "read-file", OutputContract: &payload.Contract{Name: "file", Representation: model.ContractRepresentationFile}}},
						{Kind: "sink", Slug: "write", Sink: &pipelinespec.SinkSpec{
							Input: &pipelinespec.StepInput{FromStep: "read"}, DataSourceSlug: "object-store",
							InputContract: &payload.Contract{Name: "file", Representation: model.ContractRepresentationFile},
							OperationType: "s3", OperationRef: "save", IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
							Config: map[string]any{"key": "receipt.bin", "format": "file", "overwrite": true},
						}},
					},
				}, StepSlug: "read", Results: map[slug.Slug]*pipelinerun.StepResult{"read": &retained}},
			}, timelines: map[string]pipelinerun.RunTimeline{
				"original": {ExecutionContext: &executionruntime.ExecutionContext{At: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}},
			}}
			ledger := &recoveryLedger{effects: map[string]pipelinerun.SinkEffectState{}}
			sinkCalls := 0
			sink := NewSinkRuntime(&fakeSinkQueryExecutor{executeFn: func(_ context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any, _ ...queriessvc.ExecuteOption) (*result.ExecutionResult, error) {
				sinkCalls++
				return result.NewS3Result(&result.FileResult{Operation: "write"}, 0, nil), nil
			}}, ledger)
			runner, err := NewRunner(adaptTestQueryExecutor(&testutil.FakeQueryExecutor{}), &testutil.FakeClock{}, store, recoveryDataSources{}, sink)
			require.NoError(t, err)
			outcome, err := runner.Recover(t.Context(), ReplayRequest{
				OrgSlug: "acme", ProjectSlug: "ops", EnvironmentSlug: "dev", SourceRunSlug: "original", ReplayRunSlug: "repair", InputMode: ReplayInputModePinned,
			})
			if len(content) == 0 {
				require.NoError(t, err)
				require.Equal(t, "succeeded", outcome.Metadata.Status)
				require.Equal(t, 1, sinkCalls)
				return
			}
			require.ErrorIs(t, err, ErrReplayUnsupportedMode)
			require.Contains(t, xerror.PublicDetail(err), "file bytes")
			require.Nil(t, outcome)
			require.Zero(t, sinkCalls)
			require.Empty(t, ledger.attempts)
			require.NotContains(t, store.timelines, "repair")
		})
	}
}

type recoveryLedger struct {
	effects  map[string]pipelinerun.SinkEffectState
	attempts []pipelinerun.SinkEffectAttempt
	loseAck  bool
}

func (l *recoveryLedger) BeginOrGet(_ context.Context, org, project, environment slug.Slug, attempt pipelinerun.SinkEffectAttempt) (*pipelinerun.SinkEffect, error) {
	key := org.String() + "/" + project.String() + "/" + environment.String() + "/" + attempt.IdempotencyKey
	l.attempts = append(l.attempts, attempt)
	if state, ok := l.effects[key]; ok {
		return &pipelinerun.SinkEffect{State: state}, nil
	}
	l.effects[key] = pipelinerun.SinkEffectStateInProgress
	return &pipelinerun.SinkEffect{Claimed: true, State: pipelinerun.SinkEffectStateInProgress}, nil
}

func (l *recoveryLedger) MarkSucceeded(_ context.Context, org, project, environment slug.Slug, attempt pipelinerun.SinkEffectAttempt) error {
	if l.loseAck && attempt.SinkStepSlug == "write-b" {
		l.loseAck = false
		return xerror.Unexpected().Detail("lost acknowledgement after write")
	}
	l.effects[org.String()+"/"+project.String()+"/"+environment.String()+"/"+attempt.IdempotencyKey] = pipelinerun.SinkEffectStateSucceeded
	return nil
}

type recoveryDataSources struct{ datasourcessvc.Service }

func (recoveryDataSources) FindBySlug(_ context.Context, _, ds slug.Slug) (*datasourcessvc.DataSource, error) {
	if ds == "object-store" {
		return &datasourcessvc.DataSource{Type: model.DataSourceTypeS3}, nil
	}
	return &datasourcessvc.DataSource{Type: model.DataSourceTypePostgres}, nil
}

func TestRunner_RecoverySkipsCompletedSiblingAndReconcilesLostAcknowledgement(t *testing.T) {
	t.Parallel()
	testRunnerRecoveryPreservesEffect(t, pipelinerun.RunSourceSummary{}, nil, "original", false)
}

func TestWorkerLossRecoverySkipsConfirmedSiblingWithoutRereadingSource(t *testing.T) {
	t.Parallel()
	testRunnerRecoveryPreservesEffect(t, pipelinerun.RunSourceSummary{}, nil, "original", true)
}

func TestRunnerRecoveryOfThirdAttemptPreservesRootEffect(t *testing.T) {
	t.Parallel()
	previous := must.Slug("second-attempt")
	previousRun := previous.String()
	testRunnerRecoveryPreservesEffect(t, pipelinerun.RunSourceSummary{Type: model.PipelineRunSourceTypeSchedule, AttemptNumber: 3, RootRunSlug: "logical-root", RetryOfRunSlug: &previous}, &previousRun, "logical-root", false)
}

func testRunnerRecoveryPreservesEffect(t *testing.T, source pipelinerun.RunSourceSummary, previous *string, effectIdentity string, workerLoss bool) {
	t.Helper()
	ledger := &recoveryLedger{effects: map[string]pipelinerun.SinkEffectState{}, loseAck: true}
	sourceCalls := 0
	sourceValue := 1
	providerCalls := map[string]int{}
	providerObjects := map[string]any{}
	sink := NewSinkRuntime(&fakeSinkQueryExecutor{executeFn: func(_ context.Context, _, _, operation slug.Slug, _ model.EventActor, params map[string]any, _ ...queriessvc.ExecuteOption) (*result.ExecutionResult, error) {
		providerCalls[operation.String()]++
		providerObjects[operation.String()] = params["input"]
		return result.NewS3Result(&result.FileResult{Operation: "write"}, 0, nil), nil
	}}, ledger)
	store := &fakeRunStore{}
	if previous != nil {
		store.timelines = map[string]pipelinerun.RunTimeline{*previous: {ExecutionContext: &executionruntime.ExecutionContext{At: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}}}
	}
	runner, err := NewRunner(adaptTestQueryExecutor(&testutil.FakeQueryExecutor{ExecuteFn: func(_ context.Context, _, _, _ slug.Slug, parameters map[string]any) (*result.ExecutionResult, error) {
		sourceCalls++
		require.Equal(t, "2026-10-10", parameters["as_of"])
		return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": sourceValue}}, ColumnNames: []string{"id"}, RowCount: 1}, 0, nil), nil
	}}), &testutil.FakeClock{NowFn: func() time.Time { return time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) }}, store, recoveryDataSources{}, sink)
	require.NoError(t, err)
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "recovery", Name: "Recovery"}, Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "read", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OperationType: "sql", OperationRef: "read-rows", Parameters: map[string]any{"as_of": "{{runtime.input.as_of}}"}}},
	}}
	for _, name := range []string{"a", "b"} {
		pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "sink", Slug: must.Slug("write-" + name), Sink: &pipelinespec.SinkSpec{
			Input: &pipelinespec.StepInput{FromStep: "read"}, InputContract: &payload.Contract{Name: "rows", Representation: model.ContractRepresentationRows, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}, DataSourceSlug: "object-store", OperationType: "s3", OperationRef: must.Slug("save-" + name), IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
			Config: map[string]any{"key": "recovery/{{runtime.sink.replay_identity}}/" + name + ".json", "format": "json", "overwrite": true},
		}})
	}
	first, err := runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "ops", EnvironmentSlug: "dev", RunSlug: "original", Source: source, ReplayOfRunSlug: previous, Pipeline: pipeline, PipelineVersionID: 42, RuntimeInput: map[string]any{"as_of": "2026-10-10"}})
	require.ErrorContains(t, err, "lost acknowledgement")
	require.Equal(t, "confirmed", first.Timeline.Snapshots[1].Input.SinkDeliveryOutcome)
	require.Equal(t, "unknown", first.Timeline.Snapshots[2].Input.SinkDeliveryOutcome)
	require.NotNil(t, providerObjects["save-a"])
	require.NotNil(t, providerObjects["save-b"])
	require.Equal(t, "{{runtime.input.as_of}}", runner.store.(*fakeRunStore).checkpoints["original"].Pipeline.Steps[0].Query.Parameters["as_of"], "request-local source input values must not be persisted")
	originalObjects := map[string]any{"save-a": providerObjects["save-a"], "save-b": providerObjects["save-b"]}
	sourceValue = 999
	if workerLoss {
		timeline := store.timelines["original"]
		timeline.Failure = &pipelinerun.FailureContext{Kind: failureKindPlatform, ReasonCode: pipelinerun.FailureReasonCodeWorkerOwnershipLost, OutcomeUncertain: true}
		store.timelines["original"] = timeline
	}
	for _, run := range []string{"recovered", "recovered-again"} {
		outcome, err := runner.Recover(t.Context(), ReplayRequest{OrgSlug: "acme", ProjectSlug: "ops", EnvironmentSlug: "dev", SourceRunSlug: "original", ReplayRunSlug: run, InputMode: ReplayInputModePinned, PipelineVersionID: 42})
		require.NoError(t, err)
		require.Equal(t, "succeeded", outcome.Metadata.Status)
		require.Equal(t, int64(42), outcome.Metadata.PipelineVersionID)
		require.Equal(t, effectIdentity, *outcome.Metadata.ReplayOfRunSlug)
		require.Equal(t, "skip_ledger_succeeded", outcome.Timeline.Snapshots[1].Input.SinkReplayDecision)
	}
	require.Equal(t, 1, sourceCalls, "recovery must not reread current source data")
	require.Equal(t, map[string]int{"save-a": 1, "save-b": 2}, providerCalls)
	require.Equal(t, originalObjects, providerObjects, "unknown effect recovery converges on the same payload")
	require.Equal(t, ledger.attempts[0].IdempotencyKey, ledger.attempts[2].IdempotencyKey)
	require.Equal(t, ledger.attempts[1].IdempotencyKey, ledger.attempts[3].IdempotencyKey)
	require.Equal(t, effectIdentity, ledger.attempts[0].ReplayIdentity)
}

func TestRunnerRecoveryStillRefusesMissingSinkRuntimeInput(t *testing.T) {
	t.Parallel()
	pipeline := &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "recovery", Name: "Recovery"}, Steps: []pipelinespec.Step{
		{Kind: "query", Slug: "read", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OperationType: "sql", OperationRef: "read-rows", Parameters: map[string]any{"as_of": "{{runtime.input.as_of}}"}}},
		{Kind: "sink", Slug: "write", Sink: &pipelinespec.SinkSpec{
			Input: &pipelinespec.StepInput{FromStep: "read"}, DataSourceSlug: "object-store", OperationType: "s3", OperationRef: "save",
			IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
			Config:           map[string]any{"key": "{{runtime.input.destination}}", "format": "json", "overwrite": true},
		}},
	}}
	store := &fakeRunStore{
		timelines: map[string]pipelinerun.RunTimeline{"original": {ExecutionContext: testExecutionContext(), Failure: &pipelinerun.FailureContext{Step: "write", Side: "sink"}}},
		checkpoints: map[string]pipelinerun.ReplayCheckpoint{"original": {
			Pipeline: pipeline, StepSlug: "read", Results: map[slug.Slug]*pipelinerun.StepResult{"read": pipelinerun.NewStepResult(successfulSQLResult(1))},
		}},
	}
	runner, err := NewRunner(adaptTestQueryExecutor(&testutil.FakeQueryExecutor{ExecuteFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, map[string]any) (*result.ExecutionResult, error) {
		t.Fatal("recovery must not reread the source")
		return nil, xerror.Unexpected().Detail("recovery reread the source")
	}}), &testutil.FakeClock{}, store, recoveryDataSources{}, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
		t.Fatal("a missing sink destination must prevent provider dispatch")
		return nil
	}})
	require.NoError(t, err)
	outcome, err := runner.Recover(t.Context(), ReplayRequest{OrgSlug: "acme", ProjectSlug: "ops", EnvironmentSlug: "dev", SourceRunSlug: "original", ReplayRunSlug: "recovered", PipelineVersionID: 42})
	require.ErrorContains(t, err, "runtime input destination is not defined for this run")
	require.Nil(t, outcome)
}

func TestSinkRuntime_ExplicitEmptyInputPolicy(t *testing.T) {
	t.Parallel()
	for _, policy := range []string{"skip", "reject"} {
		t.Run(policy, func(t *testing.T) {
			input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassNaturallyIdempotent)
			input.SinkBackend = model.SinkBackendS3
			input.SinkConfig = map[string]any{"key": "out.json"}
			input.EmptyInputPolicy = policy
			input.SourceResult = pipelinerun.NewStepResult(result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{}, RowCount: 0}, 0, nil))
			ledger := &fakeSinkEffectLedger{}
			err := NewSinkRuntime(&fakeSinkQueryExecutor{executeFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.EventActor, map[string]any, ...queriessvc.ExecuteOption) (*result.ExecutionResult, error) {
				t.Fatal("empty input must not reach the connector")
				return nil, xerror.Unexpected().Detail("empty input reached connector")
			}}, ledger).Execute(t.Context(), input)
			if policy == "skip" {
				require.NoError(t, err)
				require.Equal(t, "no_op", input.DeliveryOutcome)
			} else {
				require.ErrorContains(t, err, "zero rows")
				require.Equal(t, "failed", input.DeliveryOutcome)
			}
			require.Zero(t, ledger.beginCalls)
		})
	}
}

func TestReplaySafetyIgnoresNoOpSiblingWithoutAnExternalEffect(t *testing.T) {
	t.Parallel()
	timeline := pipelinerun.RunTimeline{
		ExecutionContext: testExecutionContext(),
		Events:           []pipelinerun.TimelineEvent{{Step: "empty", Type: "step_succeeded"}, {Step: "completed", Type: "step_succeeded"}},
		Snapshots:        []pipelinerun.StepSnapshot{{Step: "empty", Input: pipelinerun.StepInputSnapshot{SinkDeliveryOutcome: "no_op", SinkReplayDecision: "skip_empty_input"}}},
	}
	require.Equal(t, []slug.Slug{"completed"}, successfulSinkStepsBeforeFailure(timeline))
}

func TestRunnerRecoveryPreservesSourceWindowWithoutCheckpointPromotion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		cause error
	}{
		{"unavailable", xerror.Unexpected().Detail("provider unavailable")},
		{"constraint", xerror.DestinationConstraintViolation()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lower := json.RawMessage(`{"value":"2026-10-10T10:00:00Z"}`)
			upper := json.RawMessage(`{"value":"2026-10-10T11:00:00Z"}`)
			store := newIncrementalCheckpointTestRunStore(&pipelinerun.IncrementalCheckpoint{ObservedUpperBound: lower, CommitStatus: pipelinerun.IncrementalCheckpointStatusCommitted})
			executor := &incrementalCheckpointQueryExecutor{executeWithCheckpointFn: func(_ context.Context, _, _, _ slug.Slug, _ map[string]any, input *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
				return successfulSQLResult(1), &pipelinerun.IncrementalCheckpointObservation{SourceStepSlug: "fetch-users", CheckpointNamespace: "normal", CheckpointKind: "watermark", ObservedLowerBound: input.LowerBound, ObservedUpperBound: upper}, nil
			}}
			var keys []string
			runner := mustNewIncrementalCheckpointRunner(t, executor, store, &fakeSinkRuntime{executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
				keys = append(keys, input.IdempotencyKey)
				if len(keys) == 1 {
					return tc.cause
				}
				return nil
			}})
			_, err := runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", RunSlug: "original-window", PipelineVersionID: 42, Pipeline: incrementalCheckpointPipeline()})
			require.ErrorIs(t, err, tc.cause)
			recovered, err := runner.Recover(t.Context(), ReplayRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", SourceRunSlug: "original-window", ReplayRunSlug: "recovery-window", PipelineVersionID: 42, InputMode: ReplayInputModePinned})
			require.NoError(t, err)
			require.Equal(t, "succeeded", recovered.Metadata.Status)
			require.Len(t, executor.checkpointInputs, 1)
			require.Equal(t, keys[0], keys[1])
			require.Equal(t, store.checkpoints["original-window"].IncrementalSourceWindows, store.checkpoints["recovery-window"].IncrementalSourceWindows)
			require.Len(t, store.persistedRuns[1].IncrementalCheckpointTransitions, 1)
			transition := store.persistedRuns[1].IncrementalCheckpointTransitions[0]
			require.Equal(t, pipelinerun.IncrementalCheckpointTransitionBlocked, transition.Status)
			require.Contains(t, transition.BlockedReason, "promotion skipped")
			require.Empty(t, transition.CommittingRunSlug)
			require.Equal(t, store.persistedRuns[0].IncrementalCheckpointTransitions[0].Lookup, transition.Lookup)
			require.Equal(t, "watermark", transition.CheckpointKind)
			require.Equal(t, lower, store.current.ObservedUpperBound)
		})
	}
}

func TestRecoveryRequiresTimelineToPreserveOriginalEffectIdentity(t *testing.T) {
	t.Parallel()
	store := newIncrementalCheckpointTestRunStore(nil)
	store.checkpoints["original"] = pipelinerun.ReplayCheckpoint{Pipeline: incrementalCheckpointPipeline(), StepSlug: "fetch-users", Results: map[slug.Slug]*pipelinerun.StepResult{"fetch-users": pipelinerun.NewStepResult(successfulSQLResult(1))}}
	runner := mustNewIncrementalCheckpointRunner(t, &incrementalCheckpointQueryExecutor{}, store, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
		t.Fatal("missing timeline must prevent provider dispatch")
		return ErrReplayRunNotFound
	}})
	outcome, err := runner.Recover(t.Context(), ReplayRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", SourceRunSlug: "original", ReplayRunSlug: "recovery", InputMode: ReplayInputModePinned, PipelineVersionID: 42})
	require.ErrorIs(t, err, pipelinerun.ErrTimelineNotFound)
	require.Nil(t, outcome)
	require.Empty(t, store.persistedRuns)
}

func TestRecoveryDoesNotReturnAnUnpersistedFailedRun(t *testing.T) {
	t.Parallel()
	store := &fakeRunStore{
		timelines:   map[string]pipelinerun.RunTimeline{"original": {ExecutionContext: testExecutionContext(), Failure: &pipelinerun.FailureContext{Step: "persist-users", Side: "sink"}}},
		checkpoints: map[string]pipelinerun.ReplayCheckpoint{"original": {Pipeline: incrementalCheckpointPipeline(), StepSlug: "fetch-users", Results: map[slug.Slug]*pipelinerun.StepResult{"fetch-users": pipelinerun.NewStepResult(successfulSQLResult(1))}}},
		persistRunFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, *pipelinespec.Pipeline, *pipelinerun.RunResult) error {
			return xerror.Unexpected().Detail("run storage unavailable")
		},
	}
	called := false
	runner, err := NewRunner(&incrementalCheckpointQueryExecutor{}, nil, store, incrementalCheckpointDataSourceService{}, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
		called = true
		return xerror.Unexpected().Detail("provider unavailable")
	}})
	require.NoError(t, err)
	outcome, err := runner.Recover(t.Context(), ReplayRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", SourceRunSlug: "original", ReplayRunSlug: "recovery", InputMode: ReplayInputModePinned, PipelineVersionID: 42})
	require.False(t, called, "context persistence must precede provider dispatch")
	require.ErrorContains(t, err, "run storage unavailable")
	require.Nil(t, outcome, "an unpersisted failure must not be returned as a recorded recovery")
	require.NotContains(t, store.timelines, "recovery")
}

func TestRecoveryRejectsMissingOrMismatchedDeploymentResolution(t *testing.T) {
	t.Parallel()
	for name, digest := range map[string]string{"missing": "", "mismatched": "sha256:another-resolution"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &fakeRunStore{
				timelines: map[string]pipelinerun.RunTimeline{"original": {ExecutionContext: testExecutionContext(), DeploymentResolutionDigest: "sha256:original-resolution"}},
				checkpoints: map[string]pipelinerun.ReplayCheckpoint{"original": {
					Pipeline: incrementalCheckpointPipeline(), StepSlug: "fetch-users",
					DeploymentResolutionDigest: digest,
					Results:                    map[slug.Slug]*pipelinerun.StepResult{"fetch-users": pipelinerun.NewStepResult(successfulSQLResult(1))},
				}},
			}
			runner, err := NewRunner(&incrementalCheckpointQueryExecutor{}, nil, store, incrementalCheckpointDataSourceService{}, &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
				t.Fatal("missing or mismatched deployment identity must prevent provider dispatch")
				return nil
			}})
			require.NoError(t, err)
			outcome, err := runner.Recover(t.Context(), ReplayRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", SourceRunSlug: "original", ReplayRunSlug: "recovery", InputMode: ReplayInputModePinned, PipelineVersionID: 42})
			require.ErrorIs(t, err, deploymentplan.ErrInvalid)
			require.Nil(t, outcome)
			require.NotContains(t, store.timelines, "recovery")
		})
	}
}
