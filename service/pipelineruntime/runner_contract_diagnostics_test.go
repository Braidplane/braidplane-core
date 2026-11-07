package pipelineruntime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
)

// TestRunner_SinkDispatchByOperationType verifies when a sink step has no Backend
// field set (zero value = SinkBackendUnknown), the runner derives the backend from
// the OperationType string.
func TestRunner_SinkDispatchByOperationType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		operationType   string
		expectedBackend model.SinkBackend
	}{
		{
			name:            "sql maps to SinkBackendDB",
			operationType:   "sql",
			expectedBackend: model.SinkBackendDB,
		},
		{
			name:            "mongodb maps to SinkBackendMongo",
			operationType:   "mongodb",
			expectedBackend: model.SinkBackendMongo,
		},
		{
			name:            "http maps to SinkBackendHTTP",
			operationType:   "http",
			expectedBackend: model.SinkBackendHTTP,
		},
		{
			name:            "ftp maps to SinkBackendFTP",
			operationType:   "ftp",
			expectedBackend: model.SinkBackendFTP,
		},
		{
			name:            "git maps to SinkBackendGit",
			operationType:   "git",
			expectedBackend: model.SinkBackendGit,
		},
		{
			name:            "s3 maps to SinkBackendS3",
			operationType:   "s3",
			expectedBackend: model.SinkBackendS3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			clk := &testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.May, 1, 10, 0, 0, 0, time.UTC)
			}}

			runner := mustNewRunnerWithStore(
				&testutil.FakeQueryExecutor{ExecuteFn: func(
					_ context.Context,
					_, _, _ slug.Slug,
					_ map[string]any,
				) (*result.ExecutionResult, error) {
					return result.NewSQLResult("postgres", &result.SQLResult{
						Rows:        []map[string]any{{"id": 1}},
						ColumnNames: []string{"id"},
						RowCount:    1,
					}, 5, nil), nil
				}},
				clk,
				NewNoopRunStore(),
				runnerTestDataSourceLookup{},
			)

			var capturedBackend model.SinkBackend
			runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(
				_ context.Context,
				input *SinkRuntimeInput,
			) error {
				capturedBackend = input.SinkBackend
				return nil
			}}

			_, runErr := runner.Run(context.Background(), RunRequest{
				OrgSlug:         must.Slug("acme"),
				EnvironmentSlug: must.Slug("prod"),
				RunSlug:         "run-dispatch-by-op-type",
				Pipeline: &pipelinespec.Pipeline{
					Metadata: pipelinespec.Metadata{Slug: "my-pipeline", Name: "My Pipeline"},
					Steps: []pipelinespec.Step{
						{
							Kind: "query",
							Slug: "fetch-data",
							Query: &pipelinespec.QueryRef{
								DataSourceSlug: "acme-postgres",
								OperationType:  "sql",
								OperationRef:   "list-users",
							},
						},
						{
							Kind: "sink",
							Slug: "write-data",
							Sink: &pipelinespec.SinkSpec{
								IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
								// Backend intentionally omitted (zero value = SinkBackendUnknown)
								Input: &pipelinespec.StepInput{FromStep: must.Slug("fetch-data")},
								InputContract: &payload.Contract{Fields: []payload.ContractField{{
									Name:     "id",
									Type:     model.ContractFieldTypeValueInteger,
									Required: true,
								}}},
								DataSourceSlug: "acme-dest",
								OperationType:  tc.operationType,
								Config:         map[string]any{"key": "out.json"},
								OperationRef:   "some-op",
							},
						},
					},
				},
			})

			require.NoError(t, runErr)
			require.Equal(t, tc.expectedBackend, capturedBackend)
		})
	}
}

func TestSinkBackendForStep_UsesOperationTypeForOperationSinks(t *testing.T) {
	t.Parallel()

	backend := sinkBackendForStep(pipelinespec.Step{
		Kind: "sink",
		Slug: "write-users",
		Sink: &pipelinespec.SinkSpec{
			IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
			OperationType:    "sql",
			OperationRef:     "insert-users",
		},
	})

	require.Equal(t, model.SinkBackendDB, backend)
}

func TestSinkBackendForStep_DefaultsToUnknownWithoutOperationMetadata(t *testing.T) {
	t.Parallel()

	backend := sinkBackendForStep(pipelinespec.Step{
		Kind: "sink",
		Slug: "write-users",
		Sink: &pipelinespec.SinkSpec{},
	})

	require.Equal(t, model.SinkBackendUnknown, backend)
}

// TestValidateSinkInputContract exercises the function returns pipelinerun.ErrSinkInputContractViolation
// when a required contract field is absent from the actual step result at runtime.
// It is tested directly here because the pre-execution edge check already catches static
// declaration mismatches; the runtime check provides defense-in-depth via a distinct error code.
func TestValidateSinkInputContract(t *testing.T) {
	t.Parallel()

	makeSQLStepResult := func(columnNames []string) *pipelinerun.StepResult {
		rows := make([]map[string]any, 0, len(columnNames))
		if len(columnNames) > 0 {
			row := make(map[string]any, len(columnNames))
			for _, col := range columnNames {
				row[col] = "value"
			}
			rows = append(rows, row)
		}
		return pipelinerun.NewStepResult(result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        rows,
			ColumnNames: columnNames,
			RowCount:    len(rows),
		}, 1, nil))
	}

	tests := []struct {
		name      string
		columns   []string
		contract  *payload.Contract
		assertErr func(t *testing.T, err error)
	}{
		{
			name:    "RequiredFieldMissing",
			columns: []string{"id"},
			contract: &payload.Contract{Fields: []payload.ContractField{
				{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
				{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
			}},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.ErrorIs(t, err, pipelinerun.ErrSinkInputContractViolation)
				require.ErrorContains(t, err, `"email"`)
			},
		},
		{
			name:    "OptionalFieldMissingIsAllowed",
			columns: []string{"id"},
			contract: &payload.Contract{Fields: []payload.ContractField{
				{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
				{Name: "email", Type: model.ContractFieldTypeValueString, Required: false},
			}},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name:    "AllRequiredFieldsPresent",
			columns: []string{"id", "email"},
			contract: &payload.Contract{Fields: []payload.ContractField{
				{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
				{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
			}},
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
		{
			name:     "NilContractAlwaysPasses",
			columns:  []string{"id"},
			contract: nil,
			assertErr: func(t *testing.T, err error) {
				t.Helper()
				require.NoError(t, err)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source := makeSQLStepResult(tc.columns)
			tc.assertErr(t, pipelinerun.ValidateSinkInputContract(source, tc.contract))
		})
	}
}

// TestRunner_SinkInputContractPasses verifies when the upstream result
// at runtime contains all fields required by the sink InputContract, the run succeeds.
func TestRunner_SinkInputContractPasses(t *testing.T) {
	t.Parallel()

	clk := &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.May, 1, 12, 0, 0, 0, time.UTC)
	}}

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			_ map[string]any,
		) (*result.ExecutionResult, error) {
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": 1, "email": "user@example.com"}},
				ColumnNames: []string{"id", "email"},
				RowCount:    1,
			}, 5, nil), nil
		}},
		clk,
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	sinkCalls := 0
	runner.sinkRuntime = &fakeSinkRuntime{
		executeFn: func(_ context.Context, _ *SinkRuntimeInput) error {
			sinkCalls++
			return nil
		},
	}

	runResult, runErr := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-sink-contract-ok",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "sync-pipeline", Name: "Sync Pipeline"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "list-users",
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
							{
								Name:     "email",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							},
						}},
					},
				},
				{
					Kind: "sink",
					Slug: "write-users",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
						DataSourceSlug:   "acme-postgres",
						OperationType:    "sql",
						OperationRef:     "insert-users",
						InputContract: &payload.Contract{Fields: []payload.ContractField{
							{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
							{
								Name:     "email",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							},
						}},
					},
				},
			},
		},
	})

	require.NoError(t, runErr)
	require.Equal(t, 1, sinkCalls, "sink runtime must be called exactly once when contract passes")
	require.Equal(t, "succeeded", runResult.Metadata.Status)
}

// TestRunner_QueryOutputContractRuntimeViolation verifies when a query step has an
// OutputContract declaring a required field absent from the actual execution result,
// Run returns an error wrapping ErrQueryOutputContractSchemaMismatch.
func TestRunner_QueryOutputContractRuntimeViolation(t *testing.T) {
	t.Parallel()

	clk := &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.May, 1, 13, 0, 0, 0, time.UTC)
	}}

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			_ map[string]any,
		) (*result.ExecutionResult, error) {
			// Real result only has {id} — does not include "email" which the contract requires.
			// The schema (id, email, payload) and the contract (id, email) match at pre-validation
			// time, but the executor returns fewer columns at runtime, triggering runtime contract validation.
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": 1}},
				ColumnNames: []string{"id"},
				RowCount:    1,
			}, 5, nil), nil
		}},
		clk,
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	runResult, runErr := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-query-contract-violation",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "report-pipeline", Name: "Report Pipeline"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-report",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "get-report",
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
							// "email" is in the acme-postgres schema (pre-validation passes),
							// but the executor returns only {id} at runtime.
							{
								Name:     "email",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							},
						}},
					},
				},
			},
		},
	})

	require.Error(t, runErr)
	require.ErrorIs(t, runErr, ErrQueryOutputContractSchemaMismatch)
	require.ErrorContains(t, runErr, `"email"`)
	require.NotNil(t, runResult)
	require.Equal(t, "failed", runResult.Metadata.Status)
	require.NotNil(t, runResult.Metadata.FailedStep)
	require.Equal(t, must.Slug("fetch-report"), *runResult.Metadata.FailedStep)
}

func TestRunner_QueryOutputContractRuntimeViolationUpdatesEdgeDiagnostics(t *testing.T) {
	t.Parallel()

	clk := &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.May, 1, 13, 30, 0, 0, time.UTC)
	}}

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			_ map[string]any,
		) (*result.ExecutionResult, error) {
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": 1}},
				ColumnNames: []string{"id"},
				RowCount:    1,
			}, 5, nil), nil
		}},
		clk,
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	sinkCalls := 0
	runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(
		_ context.Context,
		_ *SinkRuntimeInput,
	) error {
		sinkCalls++
		return nil
	}}

	runResult, runErr := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-query-contract-edge-diagnostics",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "report-pipeline", Name: "Report Pipeline"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-report",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "get-report",
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
							{
								Name:     "email",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							},
						}},
					},
				},
				{
					Kind: "sink",
					Slug: "write-report",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-report")},
						DataSourceSlug:   "acme-postgres",
						OperationType:    "sql",
						OperationRef:     "insert-report",
						InputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}}},
					},
				},
			},
		},
	})

	require.Error(t, runErr)
	require.ErrorIs(t, runErr, ErrQueryOutputContractSchemaMismatch)
	require.NotNil(t, runResult)
	require.Equal(t, 0, sinkCalls)
	require.Len(t, runResult.EdgeDiagnostics, 1)
	require.False(t, runResult.EdgeDiagnostics[0].Compatible)
	require.Equal(t, pipelinevalidate.ContractReasonMissingRequiredField, runResult.EdgeDiagnostics[0].ReasonCode)
	require.Equal(t, "email", runResult.EdgeDiagnostics[0].Expected["field"])
	require.Equal(t, "missing", runResult.EdgeDiagnostics[0].Actual["field"])
}

func TestRunner_TransformOutputContractRuntimeViolationUpdatesEdgeDiagnostics(t *testing.T) {
	t.Parallel()

	clk := &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.May, 1, 13, 45, 0, 0, time.UTC)
	}}

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			_ map[string]any,
		) (*result.ExecutionResult, error) {
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": 1, "email": "alice@example.com"}},
				ColumnNames: []string{"id", "email"},
				RowCount:    1,
			}, 5, nil), nil
		}},
		clk,
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)
	runner.transformRuntime = &fakeTransformRuntime{executeFn: func(
		_ *pipelinespec.TransformSpec,
		_ *payload.Payload,
	) (*payload.Output, error) {
		return &payload.Output{
			OutputKind: model.TransformOutputKindJSON,
			RowCount:   1,
			Document:   []byte(`[{"name":"Alice"}]`),
		}, nil
	}}

	sinkCalls := 0
	runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(
		_ context.Context,
		_ *SinkRuntimeInput,
	) error {
		sinkCalls++
		return nil
	}}

	runResult, runErr := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-transform-contract-edge-diagnostics",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "report-pipeline", Name: "Report Pipeline"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-report",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "get-report",
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
							{
								Name:     "email",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							},
						}},
					},
				},
				{
					Kind: "transform",
					Slug: "summarize-report",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-report")}}, SQL: "SELECT email FROM input",
						OutputKind: model.TransformOutputKindJSON,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}}},
					},
				},
				{
					Kind: "sink",
					Slug: "write-report",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("summarize-report")},
						DataSourceSlug:   "acme-postgres",
						OperationType:    "sql",
						OperationRef:     "insert-report",
						InputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}}, Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeArray},
					},
				},
			},
		},
	})

	require.Error(t, runErr)
	require.ErrorIs(t, runErr, transformsql.ErrOutputContractViolation)
	require.NotNil(t, runResult)
	require.Equal(t, 0, sinkCalls)
	require.Len(t, runResult.EdgeDiagnostics, 2)
	require.True(t, runResult.EdgeDiagnostics[0].Compatible)
	require.False(t, runResult.EdgeDiagnostics[1].Compatible)
	require.Equal(t, pipelinevalidate.ContractReasonMissingRequiredField, runResult.EdgeDiagnostics[1].ReasonCode)
	require.Equal(t, "email", runResult.EdgeDiagnostics[1].Expected["field"])
	require.Equal(t, "missing", runResult.EdgeDiagnostics[1].Actual["field"])
}

func TestRunner_JSONTransformRowsReportArrayRootShapeMismatch(t *testing.T) {
	t.Parallel()

	queryCalls := 0
	sinkCalls := 0
	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			_ map[string]any,
		) (*result.ExecutionResult, error) {
			queryCalls++
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": 1, "email": "alice@example.com"}},
				ColumnNames: []string{"id", "email"},
				RowCount:    1,
			}, 5, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.June, 27, 9, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)
	runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(
		_ context.Context,
		_ *SinkRuntimeInput,
	) error {
		sinkCalls++
		return nil
	}}

	runResult, runErr := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-json-transform-object-sink-mismatch",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "report-pipeline", Name: "Report Pipeline"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "list-users",
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
							{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
						}},
					},
				},
				{
					Kind: "transform",
					Slug: "serialize-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs:     map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}},
						SQL:        "SELECT id, email FROM input",
						OutputKind: model.TransformOutputKindJSON,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
							{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
						}},
					},
				},
				{
					Kind: "sink",
					Slug: "load-users",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("serialize-users")},
						DataSourceSlug:   "acme-http",
						OperationType:    "http",
						OperationRef:     "post-user",
						InputContract: &payload.Contract{
							Representation: model.ContractRepresentationJSON,
							RootShape:      model.ContractRootShapeObject,
							Fields: []payload.ContractField{
								{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
								{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
							},
						},
					},
				},
			},
		},
	})

	require.Error(t, runErr)
	require.ErrorIs(t, runErr, pipelinerun.ErrContractMismatch)
	require.NotNil(t, runResult)
	require.Equal(t, 0, queryCalls)
	require.Equal(t, 0, sinkCalls)
	require.Len(t, runResult.EdgeDiagnostics, 2)
	require.True(t, runResult.EdgeDiagnostics[0].Compatible)
	require.False(t, runResult.EdgeDiagnostics[1].Compatible)
	require.Equal(t, pipelinevalidate.ContractReasonRootShapeMismatch, runResult.EdgeDiagnostics[1].ReasonCode)
	require.Equal(t, "object", runResult.EdgeDiagnostics[1].Expected["root_shape"])
	require.Equal(t, "array", runResult.EdgeDiagnostics[1].Actual["root_shape"])
}

// TestRunner_QueryOutputContractRuntimePasses verifies when the actual
// execution result contains all required fields in the output contract, the run succeeds.
func TestRunner_QueryOutputContractRuntimePasses(t *testing.T) {
	t.Parallel()

	clk := &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.May, 1, 14, 0, 0, 0, time.UTC)
	}}

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			_ context.Context,
			_, _, _ slug.Slug,
			_ map[string]any,
		) (*result.ExecutionResult, error) {
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": 1, "email": "user@example.com"}},
				ColumnNames: []string{"id", "email"},
				RowCount:    1,
			}, 5, nil), nil
		}},
		clk,
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	runResult, runErr := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-query-contract-ok",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "report-pipeline", Name: "Report Pipeline"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-report",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "get-report",
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
							{
								Name:     "email",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							},
						}},
					},
				},
			},
		},
	})

	require.NoError(t, runErr)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
}

func TestRunner_FanOutSavedSinkContractFailureMarksOnlyFailingEdge(t *testing.T) {
	t.Parallel()

	contractCalls := make([]slug.Slug, 0, 2)
	sinkCalls := make([]slug.Slug, 0, 2)
	runner, err := NewRunner(
		newTestQueryExecutorAdapter(
			&testutil.FakeQueryExecutor{ExecuteFn: func(
				_ context.Context,
				_, _, _ slug.Slug,
				_ map[string]any,
			) (*result.ExecutionResult, error) {
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 1}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 5, nil), nil
			}},
			func(
				_ context.Context,
				_, _,
				querySlug slug.Slug,
			) (*payload.Contract, error) {
				contractCalls = append(contractCalls, querySlug)
				switch querySlug {
				case must.Slug("insert-warehouse"):
					return &payload.Contract{Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: true,
					}}}, nil
				case must.Slug("post-webhook"):
					return &payload.Contract{Fields: []payload.ContractField{{
						Name:     "user_id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: true,
					}}}, nil
				default:
					t.Fatalf("unexpected saved sink operation %q", querySlug)
					return nil, assert.AnError
				}
			},
		),
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.May, 14, 16, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
		runnerNoopSinkRuntime{},
	)
	require.NoError(t, err)
	runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(
		_ context.Context,
		input *SinkRuntimeInput,
	) error {
		sinkCalls = append(sinkCalls, input.SinkOperationRef)
		return nil
	}}

	runResult, runErr := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-fan-out-saved-contract-failure",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "users-fan-out", Name: "Users Fan Out"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "list-users",
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}}},
					},
				},
				{
					Kind: "sink",
					Slug: "load-warehouse",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
						DataSourceSlug:   "acme-ftp",
						OperationType:    "ftp",
						OperationRef:     "insert-warehouse",
						Config:           map[string]any{"path": "users.csv"},
					},
				},
				{
					Kind: "sink",
					Slug: "call-webhook",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
						DataSourceSlug:   "acme-ftp",
						OperationType:    "ftp",
						OperationRef:     "post-webhook",
						Config:           map[string]any{"path": "webhook.json"},
					},
				},
			},
		},
	})

	require.Error(t, runErr)
	require.ErrorIs(t, runErr, pipelinerun.ErrSinkInputContractViolation)
	require.NotNil(t, runResult)
	require.Equal(t, "failed", runResult.Metadata.Status)
	require.NotNil(t, runResult.Metadata.FailedStep)
	require.Equal(t, must.Slug("call-webhook"), *runResult.Metadata.FailedStep)
	require.Equal(t, []slug.Slug{must.Slug("insert-warehouse"), must.Slug("post-webhook")}, contractCalls)
	require.Equal(t, []slug.Slug{must.Slug("insert-warehouse")}, sinkCalls)
	require.Len(t, runResult.EdgeDiagnostics, 2)
	require.True(t, runResult.EdgeDiagnostics[0].Compatible)
	require.Equal(t, "fetch-users->load-warehouse", runResult.EdgeDiagnostics[0].EdgeSlug)
	require.False(t, runResult.EdgeDiagnostics[1].Compatible)
	require.Equal(t, "fetch-users->call-webhook", runResult.EdgeDiagnostics[1].EdgeSlug)
	require.Equal(t, pipelinevalidate.ContractReasonMissingRequiredField, runResult.EdgeDiagnostics[1].ReasonCode)
	require.Equal(t, "user_id", runResult.EdgeDiagnostics[1].Expected["field"])
	require.Equal(t, "missing", runResult.EdgeDiagnostics[1].Actual["field"])
}
