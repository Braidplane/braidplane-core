package pipelineruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/writegate"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	formpayload "github.com/Braidplane/braidplane-core/pkg/payload/form"
	pipepayload "github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

type fakeSinkQueryExecutor struct {
	executeFn func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		actor model.EventActor,
		paramValues map[string]any,
		opts ...queriessvc.ExecuteOption,
	) (*result.ExecutionResult, error)
}

func TestSinkMetadataUnavailableBeforeConnectorDispatch(t *testing.T) {
	for _, name := range []string{"runtime.run.slug", "runtime.sink.replay_identity", "runtime.sink.idempotency_key", "runtime.sink.idempotency_class"} {
		executor := &fakeSinkQueryExecutor{executeFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.EventActor, map[string]any, ...queriessvc.ExecuteOption) (*result.ExecutionResult, error) {
			t.Fatal("unavailable metadata must fail before connector dispatch")
			return successfulSQLResult(1), nil
		}}
		sink := &sinkRuntime{queryExecutor: executor}
		err := sink.Execute(t.Context(), &SinkRuntimeInput{SinkBackend: model.SinkBackendS3, SinkDataSourceSlug: "sink", SinkOperationRef: "write", SinkConfig: map[string]any{"key": "{{" + name + "}}"}, SourceResult: pipelinerun.NewStepResult(successfulSQLResult(1))})
		require.ErrorContains(t, err, name+" is unavailable during this sink execution")
	}
}

type fakeSinkEffectLedger struct {
	effect      pipelinerun.SinkEffect
	beginCalls  int
	markCalls   int
	markErr     error
	lastAttempt pipelinerun.SinkEffectAttempt
}

// BeginOrGet returns the configured durable state so tests can model worker
// redelivery after an external provider has already observed a write.
func (f *fakeSinkEffectLedger) BeginOrGet(
	_ context.Context,
	_, _, _ slug.Slug,
	attempt pipelinerun.SinkEffectAttempt,
) (*pipelinerun.SinkEffect, error) {
	f.beginCalls++
	f.lastAttempt = attempt
	effect := f.effect
	return &effect, nil
}

// MarkSucceeded records a provider-confirmed write unless a test injects the
// crash-after-effect persistence failure that leaves the attempt ambiguous.
func (f *fakeSinkEffectLedger) MarkSucceeded(
	_ context.Context,
	_, _, _ slug.Slug,
	attempt pipelinerun.SinkEffectAttempt,
) error {
	f.markCalls++
	f.lastAttempt = attempt
	if f.markErr != nil {
		return f.markErr
	}
	f.effect.State = pipelinerun.SinkEffectStateSucceeded
	return nil
}

func (f *fakeSinkQueryExecutor) Execute(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	actor model.EventActor,
	paramValues map[string]any,
	opts ...queriessvc.ExecuteOption,
) (*result.ExecutionResult, error) {
	if err := writegate.BeforeWrite(ctx); err != nil {
		return nil, err
	}
	if f.executeFn != nil {
		return f.executeFn(ctx, orgSlug, dsSlug, querySlug, actor, paramValues, opts...)
	}

	return nil, fmt.Errorf("fake sink query executor should not be used")
}

func TestSinkRuntime_ExecuteReturnsErrWhenBackendUnknown(t *testing.T) {
	t.Parallel()

	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{})

	err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		SinkBackend:        model.SinkBackendUnknown,
		SinkDataSourceSlug: must.Slug("acme-ftp"),
		SinkOperationType:  "sql",
		SinkOperationRef:   must.Slug("ftp-upload-users"),
	})

	require.ErrorIs(t, err, errSinkNotConfigured)
	require.ErrorContains(t, err, "sink backend is unknown")
}

func TestSinkRuntime_ExecuteReturnsErrWhenDataSourceReadOnly(t *testing.T) {
	t.Parallel()

	var executorCalled bool
	queryExec := &fakeSinkQueryExecutor{
		executeFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			_ model.EventActor,
			_ map[string]any,
			_ ...queriessvc.ExecuteOption,
		) (*result.ExecutionResult, error) {
			executorCalled = true
			return nil, fmt.Errorf("unexpected call")
		},
	}
	sinkRuntime := NewSinkRuntime(queryExec)

	err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
		OrgSlug:                must.Slug("acme"),
		SinkBackend:            model.SinkBackendDB,
		SinkDataSourceSlug:     must.Slug("acme-postgres"),
		SinkDataSourceReadOnly: true,
		SinkOperationType:      "sql",
		SinkOperationRef:       must.Slug("insert-users"),
	})

	require.ErrorIs(t, err, errSinkReadOnly)
	require.False(
		t,
		executorCalled,
		"query executor must not be called when data source is read-only",
	)
}

func TestSinkRuntime_UnsafeReplayRefusesDuplicateJobAfterUnknownEffect(t *testing.T) {
	t.Parallel()

	for _, state := range []pipelinerun.SinkEffectState{
		pipelinerun.SinkEffectStateInProgress,
		pipelinerun.SinkEffectStateSucceeded,
	} {
		t.Run(state.String(), func(t *testing.T) {
			t.Parallel()

			ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{
				Claimed: false,
				State:   state,
			}}
			queryExec := &fakeSinkQueryExecutor{executeFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
				slug.Slug,
				model.EventActor,
				map[string]any,
				...queriessvc.ExecuteOption,
			) (*result.ExecutionResult, error) {
				return nil, fmt.Errorf("unsafe sink must not be delivered twice")
			}}

			input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassUnsafe)
			err := NewSinkRuntime(queryExec, ledger).Execute(context.Background(), input)

			require.ErrorIs(t, err, errSinkReplayBlocked)
			require.ErrorContains(t, err, "prior effect attempt is "+state.String())
			require.Equal(t, 1, ledger.beginCalls)
			require.Zero(t, ledger.markCalls)
			require.Equal(t, "blocked_unsafe_replay", input.ReplayDecision)
			require.Equal(t, sinkReplayDecisionDiagnosticCode, ledger.lastAttempt.DiagnosticCode)
			require.Equal(t, ledger.lastAttempt.ReplayDecision, ledger.lastAttempt.DiagnosticSummary)
		})
	}
}

func TestSinkRuntime_CrashAfterEffectBlocksUnsafeRedelivery(t *testing.T) {
	t.Parallel()

	markErr := fmt.Errorf("simulated worker termination before effect state persistence")
	ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{
		Claimed: true,
		State:   pipelinerun.SinkEffectStateInProgress,
	}, markErr: markErr}
	providerCalls := 0
	queryExec := &fakeSinkQueryExecutor{executeFn: func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		model.EventActor,
		map[string]any,
		...queriessvc.ExecuteOption,
	) (*result.ExecutionResult, error) {
		providerCalls++
		return result.NewSQLResult("postgres", &result.SQLResult{RowCount: 1}, 0, nil), nil
	}}
	sinkRuntime := NewSinkRuntime(queryExec, ledger)

	err := sinkRuntime.Execute(context.Background(), sinkEffectTestInput(
		pipelinespec.SinkIdempotencyClassUnsafe,
	))
	require.ErrorIs(t, err, markErr)
	require.Equal(t, 1, providerCalls, "the external effect succeeded once before termination")
	require.Equal(t, sinkReplayDecisionDiagnosticCode, ledger.lastAttempt.DiagnosticCode)
	require.Equal(t, ledger.lastAttempt.ReplayDecision, ledger.lastAttempt.DiagnosticSummary)

	// River can deliver the same job again after a worker dies. The durable
	// in-progress claim refuses that automatic retry rather than duplicating it.
	ledger.effect.Claimed = false
	err = sinkRuntime.Execute(context.Background(), sinkEffectTestInput(
		pipelinespec.SinkIdempotencyClassUnsafe,
	))
	require.ErrorIs(t, err, errSinkReplayBlocked)
	require.Equal(t, 1, providerCalls)
}

func TestSinkRuntime_LedgerReplaySkipsSucceededEffect(t *testing.T) {
	t.Parallel()

	ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{
		Claimed: false,
		State:   pipelinerun.SinkEffectStateSucceeded,
	}}
	queryExec := &fakeSinkQueryExecutor{executeFn: func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		model.EventActor,
		map[string]any,
		...queriessvc.ExecuteOption,
	) (*result.ExecutionResult, error) {
		return nil, fmt.Errorf("succeeded ledger effect must not be delivered again")
	}}
	input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassDeduplicatedByLedger)

	err := NewSinkRuntime(queryExec, ledger).Execute(context.Background(), input)

	require.NoError(t, err)
	require.Equal(t, "skip_ledger_succeeded", input.ReplayDecision)
	require.Zero(t, ledger.markCalls)
}

func TestSinkRuntime_SafeReplayPreservesCompletedEffect(t *testing.T) {
	t.Parallel()
	for _, class := range []pipelinespec.SinkIdempotencyClass{
		pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
		pipelinespec.SinkIdempotencyClassIdempotentWithKey,
	} {
		t.Run(class.String(), func(t *testing.T) {
			t.Parallel()
			ledger := &fakeSinkEffectLedger{
				effect:  pipelinerun.SinkEffect{Claimed: false, State: pipelinerun.SinkEffectStateSucceeded},
				markErr: fmt.Errorf("completed effect cannot be updated"),
			}
			calls := 0
			executor := &fakeSinkQueryExecutor{executeFn: func(
				context.Context, slug.Slug, slug.Slug, slug.Slug, model.EventActor,
				map[string]any, ...queriessvc.ExecuteOption,
			) (*result.ExecutionResult, error) {
				calls++
				return result.NewS3Result(&result.FileResult{Operation: "write"}, 0, nil), nil
			}}
			input := sinkEffectTestInput(class)
			input.SinkBackend = model.SinkBackendS3
			input.SinkConfig = map[string]any{"key": "replay/orders.json", "format": "json", "overwrite": true}
			err := NewSinkRuntime(executor, ledger).Execute(t.Context(), input)
			require.NoError(t, err)
			require.Zero(t, calls)
			require.Equal(t, "skip_ledger_succeeded", input.ReplayDecision)
			require.Zero(t, ledger.markCalls)
			require.Equal(t, pipelinerun.SinkEffectStateSucceeded, input.EffectState)
		})
	}
}

func TestSinkReplayIdentityPinsOperatorReplayToOriginalEffect(t *testing.T) {
	t.Parallel()

	originalRunSlug := "run-original-001"
	replayIdentity := sinkReplayIdentity("run-replay-002", &originalRunSlug, pipelinerun.RunSourceSummary{})

	require.Equal(t, originalRunSlug, replayIdentity)
	require.Equal(t, "run-first-003", sinkReplayIdentity("run-first-003", nil, pipelinerun.RunSourceSummary{}))
	for _, attempt := range []int{2, 3} {
		previousAttempt := fmt.Sprintf("attempt-%d", attempt-1)
		require.Equal(t, originalRunSlug, sinkReplayIdentity(fmt.Sprintf("attempt-%d", attempt), &previousAttempt, pipelinerun.RunSourceSummary{AttemptNumber: attempt, RootRunSlug: slug.Slug(originalRunSlug)}))
	}
}

func TestSinkRuntime_S3ReplayKeepsEffectIdentityAfterPersistenceFailure(t *testing.T) {
	t.Parallel()

	originalRun := "run-original-001"
	persistenceErr := fmt.Errorf("simulated termination after S3 accepted the write")
	ledger := &fakeSinkEffectLedger{
		effect:  pipelinerun.SinkEffect{Claimed: true, State: pipelinerun.SinkEffectStateInProgress},
		markErr: persistenceErr,
	}
	var deliveries []map[string]any
	executor := &fakeSinkQueryExecutor{executeFn: func(
		_ context.Context,
		_, _, _ slug.Slug,
		_ model.EventActor,
		params map[string]any,
		_ ...queriessvc.ExecuteOption,
	) (*result.ExecutionResult, error) {
		deliveries = append(deliveries, params)
		return result.NewS3Result(&result.FileResult{Operation: "write"}, 0, nil), nil
	}}
	runtime := NewSinkRuntime(executor, ledger)
	input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassNaturallyIdempotent)
	input.SinkBackend = model.SinkBackendS3
	input.RunSlug = originalRun
	input.ReplayIdentity = sinkReplayIdentity(originalRun, nil, pipelinerun.RunSourceSummary{})
	input.SinkConfig = map[string]any{
		"key":       "golden-path/{{runtime.sink.replay_identity}}/joined-orders.json",
		"format":    "json",
		"overwrite": true,
	}

	require.ErrorIs(t, runtime.Execute(t.Context(), input), persistenceErr)
	require.Len(t, deliveries, 1, "provider accepted the write before persistence failed")
	require.Equal(t, pipelinerun.SinkEffectStateInProgress, ledger.effect.State)
	require.Equal(t, "execute_first_attempt", ledger.lastAttempt.ReplayDecision)

	ledger.effect.Claimed = false
	ledger.markErr = nil
	for _, runSlug := range []string{originalRun, "run-operator-replay-002"} {
		input.RunSlug = runSlug
		input.ReplayIdentity = sinkReplayIdentity(runSlug, &originalRun, pipelinerun.RunSourceSummary{})
		require.NoError(t, runtime.Execute(t.Context(), input))
		if runSlug == originalRun {
			require.Equal(t, "execute_naturally_idempotent_replay", input.ReplayDecision)
		} else {
			require.Equal(t, "skip_ledger_succeeded", input.ReplayDecision)
		}
		require.Equal(t, pipelinerun.SinkEffectStateSucceeded, input.EffectState)
		require.Equal(t, originalRun, ledger.lastAttempt.ReplayIdentity)
		require.Equal(t, "deterministic_external_write", ledger.lastAttempt.ReplayMethod)
		if runSlug == originalRun {
			require.Equal(t, "execute_naturally_idempotent_replay", ledger.lastAttempt.DiagnosticSummary)
		} else {
			// A completed effect is only read; its success diagnostic is not rewritten.
			require.Equal(t, "claim_effect_attempt", ledger.lastAttempt.DiagnosticSummary)
		}
	}
	require.Len(t, deliveries, 2)
	require.Equal(t, 2, ledger.markCalls)
	for _, delivery := range deliveries {
		require.Equal(t, originalRun, delivery["runtime.sink.replay_identity"])
		require.Equal(t, input.SinkConfig["key"], delivery["key"])
		require.Equal(t, true, delivery["overwrite"])
		require.Equal(t, deliveries[0][sinkbinding.Input], delivery[sinkbinding.Input])
	}
	require.NotEqual(t, deliveries[0]["runtime.run.slug"], input.RunSlug)
}

// sinkEffectTestInput creates a minimal SQL sink input whose provider call is
// observable without persisting payload values in ledger diagnostics.
func sinkEffectTestInput(class pipelinespec.SinkIdempotencyClass) *SinkRuntimeInput {
	return &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		ProjectSlug:        must.Slug("default"),
		EnvironmentSlug:    must.Slug("prod"),
		PipelineSlug:       must.Slug("users-sync"),
		RunSlug:            "run-001",
		ReplayIdentity:     "run-001",
		SinkStepSlug:       must.Slug("write-users"),
		SinkBackend:        model.SinkBackendDB,
		SinkDataSourceSlug: must.Slug("warehouse"),
		SinkOperationRef:   must.Slug("upsert-users"),
		IdempotencyClass:   class,
		IdempotencyKey:     "sink:v1:test-key",
		SourceResult: pipelinerun.NewStepResult(result.NewSQLResult(
			"postgres",
			&result.SQLResult{Rows: []map[string]any{{"id": 1}}, ColumnNames: []string{"id"}, RowCount: 1},
			0,
			nil,
		)),
	}
}

func TestSinkRuntime_ExecuteReturnsErrWhenSinkConfigUsesReservedBinding(t *testing.T) {
	t.Parallel()

	for _, reserved := range []string{
		sinkbinding.Input,
		sinkbinding.InternalInputRows,
		sinkbinding.InternalInputColumns,
		sinkbinding.InternalInputRepresentation,
	} {
		t.Run(reserved, func(t *testing.T) {
			t.Parallel()

			sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{})

			err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
				OrgSlug:            must.Slug("acme"),
				SinkBackend:        model.SinkBackendHTTP,
				SinkDataSourceSlug: must.Slug("acme-http"),
				SinkOperationRef:   must.Slug("post-users"),
				SinkConfig: map[string]any{
					reserved: "override",
				},
			})

			require.ErrorIs(t, err, errSinkNotConfigured)
			require.ErrorContains(t, err, "reserved for runtime payload binding")
		})
	}
}

func TestSinkRuntime_ExecuteReturnsErrWhenDBSinkMissingQuerySlug(t *testing.T) {
	t.Parallel()

	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{})

	err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		SinkBackend:        model.SinkBackendDB,
		SinkDataSourceSlug: must.Slug("acme-postgres"),
	})

	require.ErrorIs(t, err, errSinkNotConfigured)
	require.ErrorContains(t, err, "sink.operation_ref is required for db backend")
}

func TestSinkRuntime_ExecuteReturnsErrWhenMongoSinkMissingQuerySlug(t *testing.T) {
	t.Parallel()

	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{})

	err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		SinkBackend:        model.SinkBackendMongo,
		SinkDataSourceSlug: must.Slug("acme-mongo"),
	})

	require.ErrorIs(t, err, errSinkNotConfigured)
	require.ErrorContains(t, err, "sink.operation_ref is required for mongo backend")
}

func TestSinkRuntime_ExecuteReturnsErrWhenGitSinkMissingQuerySlug(t *testing.T) {
	t.Parallel()

	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{})

	err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		SinkBackend:        model.SinkBackendGit,
		SinkDataSourceSlug: must.Slug("acme-git"),
		SinkConfig: map[string]any{
			"path": "exports/users.json",
		},
	})

	require.ErrorIs(t, err, errSinkNotConfigured)
	require.ErrorContains(t, err, "sink.operation_ref is required for git backend")
}

func TestSinkRuntime_ExecuteReturnsErrWhenFTPSinkMissingPath(t *testing.T) {
	t.Parallel()

	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{})

	err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		SinkBackend:        model.SinkBackendFTP,
		SinkDataSourceSlug: must.Slug("acme-ftp"),
		SinkOperationRef:   must.Slug("ftp-upload-users"),
	})

	require.ErrorIs(t, err, errSinkNotConfigured)
	require.ErrorContains(t, err, "sink.config.path is required for ftp backend")
}

func TestSinkRuntime_ExecuteReturnsErrWhenFTPSinkConfigHasInvalidType(t *testing.T) {
	t.Parallel()

	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{})

	err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		SinkBackend:        model.SinkBackendFTP,
		SinkDataSourceSlug: must.Slug("acme-ftp"),
		SinkOperationRef:   must.Slug("ftp-upload-users"),
		SinkConfig: map[string]any{
			"path":       "exports/users.csv",
			"has_header": "true",
		},
	})

	require.ErrorIs(t, err, errSinkNotConfigured)
	require.ErrorContains(t, err, "sink.config.has_header must be a bool for ftp backend")
}

func TestSinkRuntime_ExecuteReturnsErrWhenGitSinkConfigHasInvalidType(t *testing.T) {
	t.Parallel()

	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{})

	err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		SinkBackend:        model.SinkBackendGit,
		SinkDataSourceSlug: must.Slug("acme-git"),
		SinkOperationRef:   must.Slug("git-write-users"),
		SinkConfig: map[string]any{
			"path":   "exports/users.csv",
			"branch": true,
		},
	})

	require.ErrorIs(t, err, errSinkNotConfigured)
	require.ErrorContains(t, err, "sink.config.branch must be a string for git backend")
}

func TestSinkRuntime_ExecuteDBSinkBindsRowsAndNativeInput(t *testing.T) {
	t.Parallel()

	sourceRows := []map[string]any{{"id": 1, "email": "alice@example.test"}}
	source := pipelinerun.NewStepResult(result.NewSQLResult(
		"postgres",
		&result.SQLResult{Rows: sourceRows, ColumnNames: []string{"id", "email"}, RowCount: 1},
		0,
		nil,
	))
	var captured map[string]any
	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{
		executeFn: func(
			_ context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			_ model.EventActor,
			paramValues map[string]any,
			_ ...queriessvc.ExecuteOption,
		) (*result.ExecutionResult, error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, must.Slug("acme-postgres"), dsSlug)
			require.Equal(t, must.Slug("insert-users"), querySlug)
			captured = paramValues
			return result.NewSQLResult("postgres", &result.SQLResult{RowCount: 1}, 0, nil), nil
		},
	})

	err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		SinkBackend:        model.SinkBackendDB,
		SinkDataSourceSlug: must.Slug("acme-postgres"),
		SinkOperationRef:   must.Slug("insert-users"),
		SinkConfig:         map[string]any{"batch_size": 100},
		SourceResult:       source,
	})

	require.NoError(t, err)
	require.Equal(t, 100, captured["batch_size"])
	require.JSONEq(t, `[{"email":"alice@example.test","id":1}]`, captured[sinkbinding.Input].(string))
	require.Equal(t, []string{"id", "email"}, captured[sinkbinding.InternalInputColumns])
	require.Equal(t, []map[string]any{{"id": 1, "email": "alice@example.test"}}, captured[sinkbinding.InternalInputRows])
	captured[sinkbinding.InternalInputRows].([]map[string]any)[0]["id"] = 2
	require.Equal(t, 1, sourceRows[0]["id"])
}

func TestSinkRuntime_ExecuteBigQueryDBSinkBindsNativeRowsAndRunSlug(t *testing.T) {
	t.Parallel()

	sourceRows := []map[string]any{
		{"order_id": "o-100", "total_amount": "123.45"},
		{"order_id": "o-102", "total_amount": "98.76"},
	}
	source := pipelinerun.NewStepResult(result.NewSQLResult(
		"bigquery",
		&result.SQLResult{
			Rows:        sourceRows,
			ColumnNames: []string{"order_id", "total_amount"},
			RowCount:    2,
		},
		0,
		nil,
	))
	var captured map[string]any
	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{
		executeFn: func(
			_ context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			_ model.EventActor,
			paramValues map[string]any,
			_ ...queriessvc.ExecuteOption,
		) (*result.ExecutionResult, error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, must.Slug("acme-bigquery"), dsSlug)
			require.Equal(t, must.Slug("insert-orders"), querySlug)
			captured = paramValues
			return result.NewSQLResult("bigquery", &result.SQLResult{Rows: sourceRows, ColumnNames: []string{"order_id", "total_amount"}, RowCount: 2}, 17, nil), nil
		},
	})

	input := &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		RunSlug:            "run-abc",
		SinkBackend:        model.SinkBackendDB,
		SinkDataSourceSlug: must.Slug("acme-bigquery"),
		SinkOperationType:  pipelinespec.OperationTypeBigQuery,
		SinkOperationRef:   must.Slug("insert-orders"),
		SourceResult:       source,
	}
	err := sinkRuntime.Execute(context.Background(), input)

	require.NoError(t, err)
	require.NotNil(t, input.SinkResult)
	written, ok := input.SinkResult.SQL()
	require.True(t, ok)
	require.Equal(t, 2, written.RowCount)
	require.Empty(t, written.Rows)
	require.Equal(t, int64(17), input.SinkResult.DurationMs())
	output := snapshotOutput(pipelinerun.NewStepResult(input.SinkResult))
	require.Equal(t, "bigquery", output.VendorType)
	require.Equal(t, 2, *output.Details.RowCount)
	content, ok := captured[sinkbinding.Input].([]any)
	require.True(t, ok)
	require.Equal(t, []any{
		map[string]any{"order_id": "o-100", "total_amount": "123.45"},
		map[string]any{"order_id": "o-102", "total_amount": "98.76"},
	}, content)
	require.Equal(t, "run-abc", captured["runtime.run.slug"])
}

func TestDBSinkPayloadBindingBigQueryUsesNormalizedContractRows(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		source *pipelinerun.StepResult
		want   []any
	}{
		{
			name: "JSON",
			source: transformStepResultWithPayload(&pipepayload.Payload{
				Representation: model.ContractRepresentationJSON,
				Document:       json.RawMessage(`[{"id":1,"email":"alice@example.test"}]`),
			}),
			want: []any{
				map[string]any{"id": int64(1), "email": "alice@example.test"},
			},
		},
		{
			name: "CSV",
			source: transformStepResultWithPayload(&pipepayload.Payload{
				Representation: model.ContractRepresentationCSV,
				CSV:            "id,email\n1,alice@example.test\n",
			}),
			want: []any{
				map[string]any{"id": "1", "email": "alice@example.test"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			binding, err := dbSinkPayloadBinding(&SinkRuntimeInput{
				SinkOperationType: pipelinespec.OperationTypeBigQuery,
				SourceResult:      tc.source,
			})

			require.NoError(t, err)
			require.Equal(t, sinkbinding.Input, binding.name)
			require.Equal(t, model.ContractRepresentationRows, binding.representation)
			require.Equal(t, tc.want, binding.value)
		})
	}
}

func TestSinkRuntime_ExecuteHTTPSinkBindsJSONAndRequiresSuccessResponse(t *testing.T) {
	t.Parallel()

	source := pipelinerun.NewTransformStepResult(&pipepayload.Output{
		OutputKind: model.TransformOutputKindJSON,
		Document:   json.RawMessage(`{"ok":true}`),
	}, 0, nil)
	var captured map[string]any
	var optionCount int
	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{
		executeFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			_ model.EventActor,
			paramValues map[string]any,
			opts ...queriessvc.ExecuteOption,
		) (*result.ExecutionResult, error) {
			captured = paramValues
			optionCount = len(opts)
			return result.NewHTTPResult(&result.HTTPResult{StatusCode: 204}, 0, nil), nil
		},
	})

	err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		SinkBackend:        model.SinkBackendHTTP,
		SinkDataSourceSlug: must.Slug("acme-http"),
		SinkOperationRef:   must.Slug("post-users"),
		SinkConfig:         map[string]any{"method": "POST"},
		SourceResult:       source,
	})

	require.NoError(t, err)
	require.Equal(t, "POST", captured["method"])
	require.Equal(t, map[string]any{"ok": true}, captured[sinkbinding.Input])
	require.Equal(t, model.ContractRepresentationJSON.String(), captured[sinkbinding.InternalInputRepresentation])
	require.Positive(t, optionCount)
}

func TestSinkRuntime_ExecuteHubSpotSinkBindsRowsAndAcceptsUpsertResult(t *testing.T) {
	t.Parallel()

	source := pipelinerun.NewTransformStepResult(&pipepayload.Output{
		OutputKind:  model.TransformOutputKindRows,
		ColumnNames: []string{"customer_id", "email"},
		Rows: []map[string]any{{
			"customer_id": "customer-1",
			"email":       "alice@example.test",
		}},
		RowCount: 1,
	}, 0, nil)
	var captured map[string]any
	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{
		executeFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			_ model.EventActor,
			paramValues map[string]any,
			_ ...queriessvc.ExecuteOption,
		) (*result.ExecutionResult, error) {
			captured = paramValues
			return result.NewObjectRowsResult("hubspot", &result.ObjectRowsResult{
				ObjectType: "contacts",
				Operation:  "upsert",
				Rows:       []map[string]any{{"hs_object_id": "101", "created": true}},
				RowCount:   1,
			}, 0, nil), nil
		},
	})

	err := sinkRuntime.Execute(t.Context(), &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		SinkBackend:        model.SinkBackendHTTP,
		SinkOperationType:  pipelinespec.OperationTypeHubSpot,
		SinkDataSourceSlug: must.Slug("acme-hubspot"),
		SinkOperationRef:   must.Slug("upsert-contacts"),
		SourceResult:       source,
	})
	require.NoError(t, err)
	require.Equal(t, []any{map[string]any{
		"customer_id": "customer-1",
		"email":       "alice@example.test",
	}}, captured[sinkbinding.Input])
	require.Equal(t, model.ContractRepresentationRows.String(), captured[sinkbinding.InternalInputRepresentation])
}

func TestSinkRuntime_ExecuteHTTPSinkRejectsInvalidResponses(t *testing.T) {
	t.Parallel()

	source := sinkCSVSourceResult()
	for _, tc := range []struct {
		name    string
		result  *result.ExecutionResult
		wantErr string
	}{
		{
			name:    "nil result",
			wantErr: "empty execution result",
		},
		{
			name:    "connector error",
			result:  result.NewHTTPResult(&result.HTTPResult{StatusCode: 200}, 0, fmt.Errorf("upstream failed")),
			wantErr: "upstream failed",
		},
		{
			name:    "wrong result type",
			result:  result.NewSQLResult("postgres", &result.SQLResult{}, 0, nil),
			wantErr: "connector did not return HTTP result",
		},
		{
			name:    "non success status",
			result:  result.NewHTTPResult(&result.HTTPResult{StatusCode: 500}, 0, nil),
			wantErr: "non-2xx status 500",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{
				executeFn: func(
					context.Context,
					slug.Slug,
					slug.Slug,
					slug.Slug,
					model.EventActor,
					map[string]any,
					...queriessvc.ExecuteOption,
				) (*result.ExecutionResult, error) {
					return tc.result, nil
				},
			})

			err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
				OrgSlug:            must.Slug("acme"),
				SinkBackend:        model.SinkBackendHTTP,
				SinkDataSourceSlug: must.Slug("acme-http"),
				SinkOperationRef:   must.Slug("post-users"),
				SourceResult:       source,
			})

			require.Error(t, err)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestSinkRuntime_ExecuteFileSinksBindConfigAndPayload(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		backend     model.SinkBackend
		dataSource  slug.Slug
		operation   slug.Slug
		execResult  *result.ExecutionResult
		extraConfig map[string]any
	}{
		{
			name:       "ftp",
			backend:    model.SinkBackendFTP,
			dataSource: must.Slug("acme-ftp"),
			operation:  must.Slug("ftp-write-users"),
			execResult: result.NewFTPResult(&result.FileResult{Operation: "write"}, 0, nil),
		},
		{
			name:       "git",
			backend:    model.SinkBackendGit,
			dataSource: must.Slug("acme-git"),
			operation:  must.Slug("git-write-users"),
			execResult: result.NewGitResult(&result.FileResult{Operation: "write"}, 0, nil),
			extraConfig: map[string]any{
				"branch":  "main",
				"message": "seed users",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config := map[string]any{
				"path":       "exports/users.csv",
				"format":     "csv",
				"delimiter":  ",",
				"has_header": true,
				"overwrite":  true,
			}
			for key, value := range tc.extraConfig {
				config[key] = value
			}
			var captured map[string]any
			sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{
				executeFn: func(
					_ context.Context,
					_, dsSlug, querySlug slug.Slug,
					_ model.EventActor,
					paramValues map[string]any,
					_ ...queriessvc.ExecuteOption,
				) (*result.ExecutionResult, error) {
					require.Equal(t, tc.dataSource, dsSlug)
					require.Equal(t, tc.operation, querySlug)
					captured = paramValues
					return tc.execResult, nil
				},
			})

			err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
				OrgSlug:            must.Slug("acme"),
				SinkBackend:        tc.backend,
				SinkDataSourceSlug: tc.dataSource,
				SinkOperationRef:   tc.operation,
				SinkConfig:         config,
				SourceResult:       sinkCSVSourceResult(),
			})

			require.NoError(t, err)
			require.Equal(t, "id,email\n1,alice@example.test\n", captured[sinkbinding.Input])
			require.Equal(t, "exports/users.csv", captured["path"])
			require.Equal(t, "csv", captured["format"])
			require.Equal(t, ",", captured["delimiter"])
			require.Equal(t, true, captured["has_header"])
			require.Equal(t, true, captured["overwrite"])
			for key, value := range tc.extraConfig {
				require.Equal(t, value, captured[key])
			}
		})
	}
}

func TestSinkRuntime_ExecuteS3SinkBindsConfigAndPayload(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{
		executeFn: func(
			_ context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			_ model.EventActor,
			paramValues map[string]any,
			_ ...queriessvc.ExecuteOption,
		) (*result.ExecutionResult, error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, must.Slug("acme-s3"), dsSlug)
			require.Equal(t, must.Slug("s3-write-users"), querySlug)
			captured = paramValues
			return result.NewS3Result(&result.FileResult{Operation: "write"}, 0, nil), nil
		},
	})

	err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		RunSlug:            "run-001",
		SinkBackend:        model.SinkBackendS3,
		SinkDataSourceSlug: must.Slug("acme-s3"),
		SinkOperationRef:   must.Slug("s3-write-users"),
		SinkConfig: map[string]any{
			"key":          "exports/{{runtime.run.slug}}/users.csv",
			"bucket":       "archive-bucket",
			"format":       "csv",
			"delimiter":    ",",
			"has_header":   true,
			"overwrite":    false,
			"sheet":        "Users",
			"range":        "A1:C5",
			"content_type": "text/csv",
			"metadata": map[string]any{
				"source": "pipeline",
				"run":    "{{runtime.run.slug}}",
			},
		},
		SourceResult: sinkCSVSourceResult(),
	})

	require.NoError(t, err)
	require.Equal(t, "id,email\n1,alice@example.test\n", captured[sinkbinding.Input])
	require.Equal(t, "exports/run-001/users.csv", captured["key"])
	require.Equal(t, "archive-bucket", captured["bucket"])
	require.Equal(t, "csv", captured["format"])
	require.Equal(t, ",", captured["delimiter"])
	require.Equal(t, true, captured["has_header"])
	require.Equal(t, false, captured["overwrite"])
	require.Equal(t, "Users", captured["sheet"])
	require.Equal(t, "A1:C5", captured["range"])
	require.Equal(t, "text/csv", captured["content_type"])
	require.Equal(t, map[string]string{
		"source": "pipeline",
		"run":    "run-001",
	}, captured["metadata"])
	require.Equal(t, "run-001", captured["runtime.run.slug"])
}

func TestSinkRuntime_ExecuteFileObjectSinkCapturesMetadataOnlyResult(t *testing.T) {
	t.Parallel()

	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{
		executeFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			_ model.EventActor,
			_ map[string]any,
			_ ...queriessvc.ExecuteOption,
		) (*result.ExecutionResult, error) {
			writtenRows := 2
			writtenBytes := 128
			return result.NewS3Result(&result.FileResult{
				Rows:         []map[string]any{{"token": "raw object body"}},
				ColumnNames:  []string{"token"},
				RowCount:     1,
				Operation:    "write",
				Content:      []byte("raw object body"),
				TargetPath:   " exports/users/run-001.json ",
				WrittenRows:  &writtenRows,
				WrittenBytes: &writtenBytes,
				File: &filepayload.FilePayloadMetadata{
					Filename:        "users.json",
					MIMEType:        "application/json",
					SizeBytes:       128,
					DigestAlgorithm: "sha256",
					Digest:          "abc123",
				},
			}, 11, nil), nil
		},
	})

	input := &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		SinkBackend:        model.SinkBackendS3,
		SinkDataSourceSlug: must.Slug("acme-s3"),
		SinkOperationRef:   must.Slug("s3-write-users"),
		SinkConfig:         map[string]any{"key": "exports/users/run-001.json"},
		SourceResult:       sinkCSVSourceResult(),
	}

	err := sinkRuntime.Execute(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, input.SinkResult)
	require.Equal(t, result.ExecutionResultTypeS3, input.SinkResult.Type())
	require.Equal(t, int64(11), input.SinkResult.DurationMs())

	fileResult, ok := input.SinkResult.S3()
	require.True(t, ok)
	require.Equal(t, "exports/users/run-001.json", fileResult.TargetPath)
	require.Equal(t, []string{"token"}, fileResult.ColumnNames)
	require.Equal(t, 1, fileResult.RowCount)
	require.Equal(t, "write", fileResult.Operation)
	require.NotNil(t, fileResult.WrittenRows)
	require.Equal(t, 2, *fileResult.WrittenRows)
	require.NotNil(t, fileResult.WrittenBytes)
	require.Equal(t, 128, *fileResult.WrittenBytes)
	require.Empty(t, fileResult.Rows)
	require.Empty(t, fileResult.Content)
	require.NotNil(t, fileResult.File)
	require.Equal(t, "application/json", fileResult.File.MIMEType)
}

func TestSinkRuntime_ExecuteFileObjectSinkCapturesPartialMetadataOnExecutorError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		backend    model.SinkBackend
		config     map[string]any
		resultType result.ExecutionResultType
		execResult *result.ExecutionResult
		wantErr    string
	}{
		{
			name:       "FTP",
			backend:    model.SinkBackendFTP,
			config:     map[string]any{"path": "exports/users.csv"},
			resultType: result.ExecutionResultTypeFTP,
			execResult: result.NewFTPResult(&result.FileResult{
				Rows:       []map[string]any{{"secret": "raw object body"}},
				Content:    []byte("raw object body"),
				TargetPath: "exports/users.csv",
			}, 13, nil),
			wantErr: "execute ftp sink query",
		},
		{
			name:       "Git",
			backend:    model.SinkBackendGit,
			config:     map[string]any{"path": "exports/users.json"},
			resultType: result.ExecutionResultTypeGit,
			execResult: result.NewGitResult(&result.FileResult{
				Rows:       []map[string]any{{"secret": "raw object body"}},
				Content:    []byte("raw object body"),
				TargetPath: "exports/users.json",
			}, 17, nil),
			wantErr: "execute git sink query",
		},
		{
			name:       "S3",
			backend:    model.SinkBackendS3,
			config:     map[string]any{"key": "exports/users.json"},
			resultType: result.ExecutionResultTypeS3,
			execResult: result.NewS3Result(&result.FileResult{
				Rows:       []map[string]any{{"secret": "raw object body"}},
				Content:    []byte("raw object body"),
				TargetPath: "exports/users.json",
			}, 19, nil),
			wantErr: "execute s3 sink query",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{
				executeFn: func(
					_ context.Context,
					_, _, _ slug.Slug,
					_ model.EventActor,
					_ map[string]any,
					_ ...queriessvc.ExecuteOption,
				) (*result.ExecutionResult, error) {
					return tc.execResult, fmt.Errorf("connector returned partial result")
				},
			})

			input := &SinkRuntimeInput{
				OrgSlug:            must.Slug("acme"),
				SinkBackend:        tc.backend,
				SinkDataSourceSlug: must.Slug("archive-store"),
				SinkOperationRef:   must.Slug("write-users"),
				SinkConfig:         tc.config,
				SourceResult:       sinkCSVSourceResult(),
			}

			err := sinkRuntime.Execute(context.Background(), input)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.wantErr)
			require.NotNil(t, input.SinkResult)
			require.Equal(t, tc.resultType, input.SinkResult.Type())

			fileResult, ok := input.SinkResult.File()
			require.True(t, ok)
			require.NotEmpty(t, fileResult.TargetPath)
			require.Empty(t, fileResult.Rows)
			require.Empty(t, fileResult.Content)
		})
	}
}

func TestSinkRuntime_ExecuteMongoSinkBindsPayloadAndConfig(t *testing.T) {
	t.Parallel()

	source := pipelinerun.NewTransformStepResult(&pipepayload.Output{
		OutputKind: model.TransformOutputKindRows,
		Rows:       []map[string]any{{"id": 1}},
		ColumnNames: []string{
			"id",
		},
		RowCount: 1,
	}, 0, nil)
	var captured map[string]any
	sinkRuntime := NewSinkRuntime(&fakeSinkQueryExecutor{
		executeFn: func(
			_ context.Context,
			_, dsSlug, querySlug slug.Slug,
			_ model.EventActor,
			paramValues map[string]any,
			_ ...queriessvc.ExecuteOption,
		) (*result.ExecutionResult, error) {
			require.Equal(t, must.Slug("acme-mongo"), dsSlug)
			require.Equal(t, must.Slug("insert-users"), querySlug)
			captured = paramValues
			return result.NewMongoDBResult(&result.MongoDBResult{OperationType: "insert_many"}, 0, nil), nil
		},
	})

	err := sinkRuntime.Execute(context.Background(), &SinkRuntimeInput{
		OrgSlug:            must.Slug("acme"),
		SinkBackend:        model.SinkBackendMongo,
		SinkDataSourceSlug: must.Slug("acme-mongo"),
		SinkOperationRef:   must.Slug("insert-users"),
		SinkConfig:         map[string]any{"collection": "users"},
		SourceResult:       source,
	})

	require.NoError(t, err)
	require.Equal(t, "users", captured["collection"])
	require.Equal(t, []any{map[string]any{"id": 1}}, captured[sinkbinding.Input])
}

func TestSinkRuntime_BuildPayloadBindingRejectsMissingAndInvalidPayloads(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		input   *SinkRuntimeInput
		wantErr string
	}{
		{name: "nil input", wantErr: "sink source result is required"},
		{name: "nil source", input: &SinkRuntimeInput{}, wantErr: "sink source result is required"},
		{
			name: "empty json",
			input: &SinkRuntimeInput{
				SourceResult: pipelinerun.NewTransformStepResult(&pipepayload.Output{
					OutputKind: model.TransformOutputKindJSON,
				}, 0, nil),
			},
			wantErr: "json payload is empty",
		},
		{
			name: "invalid json",
			input: &SinkRuntimeInput{
				SourceResult: pipelinerun.NewTransformStepResult(&pipepayload.Output{
					OutputKind: model.TransformOutputKindJSON,
					Document:   json.RawMessage(`{"broken"`),
				}, 0, nil),
			},
			wantErr: "unexpected EOF",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := buildSinkPayloadBinding(tc.input)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestBuildSinkPayloadBindingUsesProducerPayloadRepresentation(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name           string
		payload        *pipepayload.Payload
		wantValue      any
		wantEncoded    any
		wantEncodedRaw string
	}{
		{
			name: "Rows",
			payload: &pipepayload.Payload{
				Representation: model.ContractRepresentationRows,
				Rows: []map[string]any{
					{"id": float64(1), "email": "one@example.test"},
					{"id": float64(2), "email": "two@example.test"},
				},
			},
			wantValue: []any{
				map[string]any{"id": float64(1), "email": "one@example.test"},
				map[string]any{"id": float64(2), "email": "two@example.test"},
			},
			wantEncoded: []any{
				map[string]any{"id": float64(1), "email": "one@example.test"},
				map[string]any{"id": float64(2), "email": "two@example.test"},
			},
		},
		{
			name: "JSONDocument",
			payload: &pipepayload.Payload{
				Representation: model.ContractRepresentationJSON,
				Document:       json.RawMessage(`{"profiles":[{"id":1,"tier":"gold"}]}`),
			},
			wantValue: map[string]any{
				"profiles": []any{
					map[string]any{"id": json.Number("1"), "tier": "gold"},
				},
			},
			wantEncoded: map[string]any{
				"profiles": []any{
					map[string]any{"id": float64(1), "tier": "gold"},
				},
			},
		},
		{
			name: "CSV",
			payload: &pipepayload.Payload{
				Representation: model.ContractRepresentationCSV,
				CSV:            "id,tier\n1,gold\n",
			},
			wantValue:      "id,tier\n1,gold\n",
			wantEncodedRaw: "id,tier\n1,gold\n",
		},
		{
			name: "MultipartForm",
			payload: &pipepayload.Payload{
				Representation: model.ContractRepresentationMultipartForm,
				Form: &formpayload.FormData{
					Encoding: formpayload.FormEncodingMultipart,
					Fields: []formpayload.FormField{{
						Name:   "name",
						Values: []string{"Alice"},
					}},
				},
			},
			wantValue: &formpayload.FormData{
				Encoding: formpayload.FormEncodingMultipart,
				Fields: []formpayload.FormField{{
					Name:   "name",
					Values: []string{"Alice"},
				}},
				Files: []formpayload.FormFile{},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			input := &SinkRuntimeInput{
				SourceResult: transformStepResultWithPayload(tt.payload),
			}

			binding, err := buildSinkPayloadBinding(input)
			require.NoError(t, err)
			require.Equal(t, sinkbinding.Input, binding.name)
			require.Equal(t, tt.payload.Representation, binding.representation)
			require.Equal(t, tt.wantValue, binding.value)

			encoded, err := sinkEncodedPayloadBinding(input)
			if tt.payload.Representation == model.ContractRepresentationMultipartForm {
				require.ErrorContains(t, err, "multipart_form sink payloads are only supported by http backend")
				return
			}
			require.NoError(t, err)
			require.Equal(t, sinkbinding.Input, encoded.name)
			require.Equal(t, tt.payload.Representation, encoded.representation)
			if tt.payload.Representation == model.ContractRepresentationCSV {
				require.Equal(t, tt.wantEncodedRaw, encoded.value)
				return
			}

			var decoded any
			require.NoError(t, json.Unmarshal([]byte(encoded.value.(string)), &decoded))
			require.Equal(t, tt.wantEncoded, decoded)
		})
	}
}

func TestBuildSinkPayloadBindingUsesNativeFileBytesAndMetadata(t *testing.T) {
	t.Parallel()

	content := []byte{0, 1, 2}
	metadata := filepayload.NewFilePayloadMetadata("users.bin", "application/octet-stream", content)
	sourceResult := pipelinerun.NewStepResult(result.NewFTPResult(&result.FileResult{
		Format:  "file",
		File:    &metadata,
		Content: content,
	}, 0, nil))

	input := &SinkRuntimeInput{SourceResult: sourceResult}

	binding, err := buildSinkPayloadBinding(input)
	require.NoError(t, err)
	require.Equal(t, sinkbinding.Input, binding.name)
	require.Equal(t, model.ContractRepresentationFile, binding.representation)
	require.Equal(t, content, binding.value)
	require.Equal(t, &metadata, binding.file)

	encoded, err := sinkEncodedPayloadBinding(input)
	require.NoError(t, err)
	require.Equal(t, content, encoded.value)

	params := map[string]any{}
	addSinkPayloadBindingMetadataParams(params, binding)
	require.Equal(t, "application/octet-stream", params[sinkbinding.InternalInputFileMIMEType])
	require.Equal(t, "users.bin", params[sinkbinding.InternalInputFileFilename])
	require.EqualValues(t, len(content), params[sinkbinding.InternalInputFileSizeBytes])
}

func TestValidateSinkBackendPayloadSupportRejectsMultipartFormForNonHTTP(t *testing.T) {
	t.Parallel()

	source := stepResultWithPayload(&pipepayload.Payload{
		Representation: model.ContractRepresentationMultipartForm,
		Form: &formpayload.FormData{
			Fields: []formpayload.FormField{{Name: "name", Values: []string{"Alice"}}},
		},
	})

	err := validateSinkBackendPayloadSupport(model.SinkBackendDB, &SinkRuntimeInput{
		SourceResult: source,
	})
	require.ErrorContains(t, err, "multipart_form sink payloads are only supported by http backend")
}

func TestCloneSinkInputRowsReturnsIndependentRows(t *testing.T) {
	t.Parallel()

	rows := []map[string]any{
		{"id": 1, "email": "one@example.test"},
		{"id": json.Number("9007199254740993"), "score": json.Number("4.25"), "email": "two@example.test"},
	}

	cloned := cloneSinkInputRows(rows)
	require.Equal(t, 1, cloned[0]["id"])
	require.Equal(t, int64(9007199254740993), cloned[1]["id"])
	require.Equal(t, json.Number("4.25"), cloned[1]["score"])

	cloned[0]["email"] = "changed@example.test"
	require.Equal(t, "one@example.test", rows[0]["email"])
}

func TestDecodeFTPSinkConfig(t *testing.T) {
	t.Parallel()

	config, err := decodeFTPSinkConfig(map[string]any{
		"path":       "exports/users.csv",
		"format":     "csv",
		"delimiter":  ";",
		"has_header": true,
		"overwrite":  true,
		"sheet":      "Orders",
		"range":      "B2:D20",
	})
	require.NoError(t, err)
	require.NotNil(t, config.Path)
	require.Equal(t, "exports/users.csv", *config.Path)
	require.NotNil(t, config.Format)
	require.Equal(t, "csv", *config.Format)
	require.NotNil(t, config.Delimiter)
	require.Equal(t, ";", *config.Delimiter)
	require.NotNil(t, config.HasHeader)
	require.True(t, *config.HasHeader)
	require.NotNil(t, config.Overwrite)
	require.True(t, *config.Overwrite)
	require.NotNil(t, config.Sheet)
	require.Equal(t, "Orders", *config.Sheet)
	require.NotNil(t, config.Range)
	require.Equal(t, "B2:D20", *config.Range)
}

func TestDecodeGitSinkConfigIncludesSpreadsheetOptions(t *testing.T) {
	t.Parallel()

	config, err := decodeGitSinkConfig(map[string]any{
		"path":  "exports/users.xlsx",
		"sheet": "Orders",
		"range": "B2",
	})
	require.NoError(t, err)
	require.NotNil(t, config.Sheet)
	require.Equal(t, "Orders", *config.Sheet)
	require.NotNil(t, config.Range)
	require.Equal(t, "B2", *config.Range)
}

func TestDecodeGitSinkConfigReturnsErrWhenBranchHasInvalidType(t *testing.T) {
	t.Parallel()

	_, err := decodeGitSinkConfig(map[string]any{
		"path":   "exports/users.csv",
		"branch": true,
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "sink.config.branch must be a string for git backend")
}

func sinkCSVSourceResult() *pipelinerun.StepResult {
	return pipelinerun.NewTransformStepResult(&pipepayload.Output{
		OutputKind: model.TransformOutputKindCSV,
		CSV:        "id,email\n1,alice@example.test\n",
	}, 0, nil)
}

func transformStepResultWithPayload(payload *pipepayload.Payload) *pipelinerun.StepResult {
	if payload != nil && payload.Representation == model.ContractRepresentationMultipartForm {
		return stepResultWithPayload(payload)
	}

	output := &pipepayload.Output{}
	if payload != nil {
		switch payload.Representation {
		case model.ContractRepresentationUnknown:
			output.OutputKind = model.TransformOutputKindUnknown
		case model.ContractRepresentationRows:
			output.OutputKind = model.TransformOutputKindRows
		case model.ContractRepresentationJSON:
			output.OutputKind = model.TransformOutputKindJSON
		case model.ContractRepresentationCSV:
			output.OutputKind = model.TransformOutputKindCSV
		default:
			output.OutputKind = model.TransformOutputKindUnknown
		}
		output.RowCount = payload.RowCount
		output.ColumnNames = append([]string(nil), payload.ColumnNames...)
		output.Rows = payload.Rows
		output.Document = payload.Document
		output.CSV = payload.CSV
	}

	return pipelinerun.NewTransformStepResult(output, 0, nil)
}

func stepResultWithPayload(payload *pipepayload.Payload) *pipelinerun.StepResult {
	stepResult := pipelinerun.NewStepResult(result.NewHTTPResult(&result.HTTPResult{
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    []byte(`{}`),
	}, 0, nil))
	runtimePayload, ok := stepResult.Payload()
	if ok && payload != nil {
		*runtimePayload = *payload.Clone()
	}
	return stepResult
}

func TestCloneSinkInputRowsPreservesUnsupportedIntegerRanges(t *testing.T) {
	t.Parallel()
	rows := cloneSinkInputRows([]map[string]any{{
		"unsigned": json.Number("18446744073709551615"),
		"overflow": json.Number("18446744073709551616"),
		"exponent": json.Number("9007199254740993e0"),
	}})
	require.Equal(t, uint64(18446744073709551615), rows[0]["unsigned"])
	require.Equal(t, json.Number("18446744073709551616"), rows[0]["overflow"])
	require.Equal(t, json.Number("9007199254740993e0"), rows[0]["exponent"])
}
