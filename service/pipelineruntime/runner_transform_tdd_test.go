package pipelineruntime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

// runtimeRowsInputContract builds rows contracts for runtime fixtures.
func runtimeRowsInputContract(fields ...payload.ContractField) *payload.Contract {
	return &payload.Contract{
		Representation: model.ContractRepresentationRows,
		Fields:         fields,
	}
}

// runtimeField builds compact contract fields for runtime fixtures.
func runtimeField(
	name string,
	fieldType model.ContractFieldType,
) payload.ContractField {
	return payload.ContractField{
		Name: name,
		Type: fieldType,
	}
}

// userRuntimeInputContract declares a broad rows contract for user transform fixtures.
func userRuntimeInputContract() *payload.Contract {
	return runtimeRowsInputContract(
		runtimeField("id", model.ContractFieldTypeValueInteger),
		runtimeField("email", model.ContractFieldTypeValueString),
		runtimeField("payload", model.ContractFieldTypeValueString),
		runtimeField("csv_row", model.ContractFieldTypeValueArray),
		runtimeField("missing", model.ContractFieldTypeValueInteger),
	)
}

// orderRuntimeInputContract declares a broad rows contract for order transform fixtures.
func orderRuntimeInputContract() *payload.Contract {
	return runtimeRowsInputContract(
		runtimeField("amount", model.ContractFieldTypeValueNumber),
		runtimeField("category", model.ContractFieldTypeValueString),
	)
}

func nullableOrderRuntimeInputContract() *payload.Contract {
	contract := orderRuntimeInputContract()
	contract.Fields[0].Nullable = true
	return contract
}

func nullableOrderRuntimeQueryContract() *payload.Contract {
	return runtimeRowsInputContract(payload.ContractField{
		Name:     "amount",
		Type:     model.ContractFieldTypeValueInteger,
		Nullable: true,
	})
}

func TestRunner_RunTransform_ExecutesTransformStep(t *testing.T) {
	// Shows the baseline transform runtime path: query output rows are projected by
	// a transform step and materialized as a SQL-shaped step result.
	t.Parallel()

	callCount := 0
	schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{
			Name: "users",
			Columns: []datasourcessvc.SchemaColumn{
				{Name: "id", Type: "integer"},
				{Name: "email", Type: "varchar"},
			},
		}}},
	}

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			callCount++
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows: []map[string]any{
					{"id": 1, "email": "a@example.com"},
					{"id": 2, "email": "b@example.com"},
				},
				ColumnNames: []string{"id", "email"},
				RowCount:    2,
			}, 7, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 17, 10, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("acme-postgres"): schema},
		},
	)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "transform-runtime-test",
			Name: "Transform Runtime Test",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-active-users",
				},
			},
			{
				Kind: "transform",
				Slug: "project-user-ids",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS id FROM input",
					InputContract: userRuntimeInputContract(),
					OutputKind:    model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						},
					}},
				},
			},
		},
	}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-transform-001",
		Pipeline:        pipeline,
	})

	require.NoError(t, err)
	require.NotNil(t, runResult)
	require.Equal(t, 1, callCount, "query executor should be called only for query steps")
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Equal(t, 2, runResult.Metadata.StepCount)

	transformResult, ok := runResult.StepResults[must.Slug("project-user-ids")]
	require.True(t, ok, "transform step result should be present")
	require.NotNil(t, transformResult)

	transformOutput, ok := transformResult.Transform()
	require.True(t, ok, "transform should emit SQL-shaped rows result")
	require.Equal(t, 2, transformOutput.RowCount)
	require.Equal(t, []string{"id"}, transformOutput.ColumnNames)
	require.Equal(
		t,
		[]map[string]any{{"id": 1}, {"id": 2}},
		transformOutput.Rows,
	)
}

func TestRunner_RunTransform_UsesStepInputForUpstreamResolution(t *testing.T) {
	t.Parallel()

	callCount := 0
	schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{
			Name: "users",
			Columns: []datasourcessvc.SchemaColumn{
				{Name: "id", Type: "integer"},
			},
		}}},
	}

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			callCount++
			switch querySlug {
			case must.Slug("list-users"):
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 1}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 7, nil), nil
			case must.Slug("list-orders"):
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 999}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 7, nil), nil
			default:
				return result.NewSQLResult("postgres", &result.SQLResult{}, 7, nil), nil
			}
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 17, 10, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("acme-postgres"): schema},
		},
	)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "transform-binding-runtime-test",
			Name: "Transform Binding Runtime Test",
		},
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
				Kind: "query",
				Slug: "fetch-orders",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-orders",
					Parameters: map[string]any{
						"seed": "$steps.fetch-users.rows",
					},
				},
			},
			{
				Kind: "transform",
				Slug: "project-user-ids",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS id FROM input",
					InputContract: userRuntimeInputContract(),
					OutputKind:    model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: true,
					}}},
				},
			},
			{
				Kind: "sink",
				Slug: "transform-binding-runtime-test",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: must.Slug("project-user-ids")},
					DataSourceSlug:   "acme-postgres",
					OperationType:    "sql",
					OperationRef:     "insert-users",
				},
			},
		},
	}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-transform-binding-001",
		Pipeline:        pipeline,
	})

	require.NoError(t, err)
	require.NotNil(t, runResult)
	require.Equal(t, 2, callCount)

	transformResult, ok := runResult.StepResults[must.Slug("project-user-ids")]
	require.True(t, ok)
	transformOutput, ok := transformResult.Transform()
	require.True(t, ok)
	require.Equal(t, []map[string]any{{"id": 1}}, transformOutput.Rows)
}

func TestRunner_RunTransform_PreservesStepReferenceForDownstreamQuery(t *testing.T) {
	// Shows that transform results remain addressable via $steps.<slug>.rows and
	// are resolved into downstream query parameters.
	t.Parallel()

	callCount := 0
	var step3Params map[string]any
	schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{
			Name: "users",
			Columns: []datasourcessvc.SchemaColumn{
				{Name: "id", Type: "integer"},
				{Name: "email", Type: "varchar"},
			},
		}}},
	}

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			callCount++
			if callCount == 1 {
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows: []map[string]any{
						{"id": 1, "email": "a@example.com"},
						{"id": 2, "email": "b@example.com"},
					},
					ColumnNames: []string{"id", "email"},
					RowCount:    2,
				}, 7, nil), nil
			}
			step3Params = paramValues
			return result.NewSQLResult(
				"postgres",
				&result.SQLResult{Rows: []map[string]any{{"ok": true}}, RowCount: 1},
				2,
				nil,
			), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 17, 10, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("acme-postgres"): schema},
		},
	)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "transform-ref-test", Name: "Transform Ref Test"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres", OperationType: "sql",
					OperationRef: "list-active-users",
				},
			},
			{
				Kind: "transform",
				Slug: "project-user-ids",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS id FROM input",
					InputContract: userRuntimeInputContract(),
					OutputKind:    model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						},
					}},
				},
			},
			{
				Kind: "query",
				Slug: "consume-transform",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "bulk-upsert-users",
					Parameters: map[string]any{
						"user_ids": "$steps.project-user-ids.rows",
					},
				},
			},
		},
	}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-transform-002",
		Pipeline:        pipeline,
	})

	require.NoError(t, err)
	require.NotNil(t, runResult)
	require.Equal(t, 2, callCount, "only query steps should call query executor")

	resolvedRows, ok := step3Params["user_ids"].([]map[string]any)
	require.True(t, ok, "downstream query should receive resolved transform rows")
	require.Equal(t, []map[string]any{{"id": 1}, {"id": 2}}, resolvedRows)
}

func TestRunner_RunTransform_ExecutesCastProjection(t *testing.T) {
	// Shows CAST expression evaluation at runtime by converting integer ids into
	// string output values via SELECT CAST(... AS CHAR) projection.
	t.Parallel()

	schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{
			Name: "users",
			Columns: []datasourcessvc.SchemaColumn{
				{Name: "id", Type: "integer"},
			},
		}}},
	}

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": 1}, {"id": 2}},
				ColumnNames: []string{"id"},
				RowCount:    2,
			}, 7, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 17, 10, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("acme-postgres"): schema},
		},
	)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "transform-cast-test", Name: "Transform Cast Test"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres", OperationType: "sql",
					OperationRef: "list-active-users",
				},
			},
			{
				Kind: "transform",
				Slug: "cast-user-ids",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT CAST(id AS CHAR) AS id_text FROM input",
					InputContract: userRuntimeInputContract(),
					OutputKind:    model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{
							Name:     "id_text",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						},
					}},
				},
			},
		},
	}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-transform-cast",
		Pipeline:        pipeline,
	})

	require.NoError(t, err)
	transformResult, ok := runResult.StepResults[must.Slug("cast-user-ids")]
	require.True(t, ok)
	transformOutput, ok := transformResult.Transform()
	require.True(t, ok)
	require.Equal(t, []string{"id_text"}, transformOutput.ColumnNames)
	require.Equal(t, []map[string]any{{"id_text": "1"}, {"id_text": "2"}}, transformOutput.Rows)
}

func TestRunner_RunTransform_ProjectionRuntime(t *testing.T) {
	// Shows projection runtime behavior in one matrix: direct columns and CAST
	// succeed, missing columns and unsupported derived expressions fail.
	t.Parallel()

	newRunner := func(t *testing.T, rows []map[string]any) *Runner {
		t.Helper()

		schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
			Type:            "postgres",
			Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{
				Name: "users",
				Columns: []datasourcessvc.SchemaColumn{
					{Name: "id", Type: "integer"},
				},
			}}},
		}

		return mustNewRunnerWithStore(
			&testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        rows,
					ColumnNames: []string{"id"},
					RowCount:    len(rows),
				}, 7, nil), nil
			}},
			&testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.March, 17, 10, 0, 0, 0, time.UTC)
			}},
			NewNoopRunStore(),
			preValidationMockService{
				schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("acme-postgres"): schema},
			},
		)
	}

	newProjectionPipeline := func(metadataSlug slug.Slug, metadataName, sqlText string, outputContract *payload.Contract) *pipelinespec.Pipeline {
		return &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: metadataSlug, Name: metadataName},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "list-active-users",
					},
				},
				{Kind: "transform", Slug: "project", Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: sqlText,
					InputContract:  userRuntimeInputContract(),
					OutputKind:     model.TransformOutputKindRows,
					OutputContract: outputContract,
				}},
			},
		}
	}

	runProjectionRows := func(t *testing.T, runner *Runner, runSlug string, pipeline *pipelinespec.Pipeline) []map[string]any {
		t.Helper()

		runResult, err := runner.Run(
			context.Background(),
			RunRequest{OrgSlug: must.Slug("acme"), EnvironmentSlug: must.Slug("prod"), RunSlug: runSlug, Pipeline: pipeline},
		)
		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("project")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		return transformOutput.Rows
	}

	runProjectionError := func(t *testing.T, runner *Runner, runSlug string, pipeline *pipelinespec.Pipeline) error {
		t.Helper()

		_, err := runner.Run(
			context.Background(),
			RunRequest{OrgSlug: must.Slug("acme"), EnvironmentSlug: must.Slug("prod"), RunSlug: runSlug, Pipeline: pipeline},
		)
		require.Error(t, err)
		return err
	}
	t.Run("ExecutesColumnProjection", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"id": 1}, {"id": 2}})
		pipeline := newProjectionPipeline(
			s("projection-col"),
			"Projection Column",
			"SELECT id AS id FROM input",
			&payload.Contract{Fields: []payload.ContractField{{
				Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true,
			}}},
		)

		rows := runProjectionRows(t, runner, "run-projection-col", pipeline)
		require.Equal(t, []map[string]any{{"id": 1}, {"id": 2}}, rows)
	})

	t.Run("ExecutesCastProjection", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"id": 1}, {"id": 2}})
		pipeline := newProjectionPipeline(
			s("projection-cast"),
			"Projection Cast",
			"SELECT CAST(id AS CHAR) AS id_text FROM input",
			&payload.Contract{Fields: []payload.ContractField{{
				Name: "id_text", Type: model.ContractFieldTypeValueString, Required: true,
			}}},
		)

		rows := runProjectionRows(t, runner, "run-projection-cast", pipeline)
		require.Equal(t, []map[string]any{{"id_text": "1"}, {"id_text": "2"}}, rows)
	})

	t.Run("ExecutesJSONObjectProjection", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"id": 1}, {"id": 2}})
		pipeline := newProjectionPipeline(
			s("projection-json-object"),
			"Projection JSON Object",
			"SELECT JSON_OBJECT('id', id) AS payload FROM input",
			&payload.Contract{Fields: []payload.ContractField{{
				Name: "payload", Type: model.ContractFieldTypeValueString, Required: true,
			}}},
		)

		rows := runProjectionRows(t, runner, "run-projection-json-object", pipeline)
		require.Equal(
			t,
			[]map[string]any{{"payload": `{"id":1}`}, {"payload": `{"id":2}`}},
			rows,
		)
	})

	t.Run("ExecutesJSONArrayProjection", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"id": 1}, {"id": 2}})
		pipeline := newProjectionPipeline(
			s("projection-json-array"),
			"Projection JSON Array",
			"SELECT JSON_ARRAY(id, id + 1) AS payload FROM input",
			&payload.Contract{Fields: []payload.ContractField{{
				Name: "payload", Type: model.ContractFieldTypeValueString, Required: true,
			}}},
		)

		rows := runProjectionRows(t, runner, "run-projection-json-array", pipeline)
		require.Equal(
			t,
			[]map[string]any{{"payload": `[1,2]`}, {"payload": `[2,3]`}},
			rows,
		)
	})

	t.Run("ExecutesJSONPathProjection", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{
			{"id": 1, "payload": `{"user":{"name":"alice"}}`},
			{"id": 2, "payload": `{"user":{"name":"bob"}}`},
		})
		pipeline := newProjectionPipeline(
			s("projection-json-path"),
			"Projection JSON Path",
			"SELECT payload -> '$.user.name' AS user_name FROM input",
			nil,
		)

		rows := runProjectionRows(t, runner, "run-projection-json-path", pipeline)
		require.Equal(
			t,
			[]map[string]any{{"user_name": "alice"}, {"user_name": "bob"}},
			rows,
		)
	})

	t.Run("FailsOnMissingProjectionField", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"id": 1}})
		pipeline := newProjectionPipeline(
			s("projection-missing"),
			"Projection Missing",
			"SELECT missing AS id FROM input",
			nil,
		)

		err := runProjectionError(t, runner, "run-projection-missing", pipeline)
		require.ErrorIs(t, err, transformsql.ErrRuntimeProjectionFieldNotFound)
	})

	t.Run("RejectsUnsupportedProjectionExpressionAtValidation", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"id": 1}})
		pipeline := newProjectionPipeline(
			s("projection-unsupported"),
			"Projection Unsupported",
			"SELECT ABS(id) AS id FROM input",
			nil,
		)

		err := runProjectionError(t, runner, "run-projection-unsupported", pipeline)
		require.ErrorIs(t, err, transformsql.ErrFeatureNotAllowed)
	})

	t.Run("ExecutesArithmeticProjection", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"id": 1}, {"id": 2}})
		pipeline := newProjectionPipeline(
			s("projection-arithmetic"),
			"Projection Arithmetic",
			"SELECT id + 1 AS id_inc FROM input",
			&payload.Contract{Fields: []payload.ContractField{{
				Name: "id_inc", Type: model.ContractFieldTypeValueNumber, Required: true,
			}}},
		)

		rows := runProjectionRows(t, runner, "run-projection-arithmetic", pipeline)
		require.Equal(t, []map[string]any{{"id_inc": int64(2)}, {"id_inc": int64(3)}}, rows)
	})

	t.Run("ExecutesWhereProjectionFilter", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"id": 1}, {"id": 2}, {"id": 3}})
		pipeline := newProjectionPipeline(
			s("projection-where"),
			"Projection Where",
			"SELECT id AS id FROM input WHERE id > 1",
			&payload.Contract{Fields: []payload.ContractField{{
				Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true,
			}}},
		)

		rows := runProjectionRows(t, runner, "run-projection-where", pipeline)
		require.Equal(t, []map[string]any{{"id": 2}, {"id": 3}}, rows)
	})

	t.Run("ExecutesOrderByAndLimitProjection", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"id": 1}, {"id": 3}, {"id": 2}})
		pipeline := newProjectionPipeline(
			s("projection-order-limit"),
			"Projection Order Limit",
			"SELECT id AS id FROM input ORDER BY id DESC LIMIT 2",
			&payload.Contract{Fields: []payload.ContractField{{
				Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true,
			}}},
		)

		rows := runProjectionRows(t, runner, "run-projection-order-limit", pipeline)
		require.Equal(t, []map[string]any{{"id": 3}, {"id": 2}}, rows)
	})

	t.Run("RejectsUnsupportedOrderByExpressionAtValidation", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"id": 1}, {"id": 3}, {"id": 2}})
		pipeline := newProjectionPipeline(
			s("projection-order-unsupported-expr"),
			"Projection Order Unsupported Expression",
			"SELECT id AS id FROM input ORDER BY ABS(id) DESC",
			nil,
		)

		err := runProjectionError(t, runner, "run-projection-order-unsupported-expr", pipeline)
		require.ErrorIs(t, err, transformsql.ErrFeatureNotAllowed)
	})

	t.Run("FailsOnInvalidLimitLiteralAtValidation", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"id": 1}, {"id": 3}, {"id": 2}})
		pipeline := newProjectionPipeline(
			s("projection-limit-unsupported-literal"),
			"Projection Limit Unsupported Literal",
			"SELECT id AS id FROM input LIMIT 'x'",
			nil,
		)

		err := runProjectionError(t, runner, "run-projection-limit-unsupported-literal", pipeline)
		require.ErrorContains(t, err, "parse transform sql")
	})

	t.Run("FailsOnOrderByIncomparableValuesAtRuntime", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{
			{"id": map[string]any{"v": 1}},
			{"id": map[string]any{"v": 2}},
		})
		pipeline := newProjectionPipeline(
			s("projection-order-incomparable-values"),
			"Projection Order Incomparable Values",
			"SELECT id AS id FROM input ORDER BY id DESC",
			nil,
		)

		err := runProjectionError(t, runner, "run-projection-order-incomparable-values", pipeline)
		require.ErrorContains(t, err, "apply transform ORDER BY/LIMIT")
		require.ErrorContains(t, err, "ordered WHERE comparison is unsupported")
	})
	t.Run("ExecutesCSVIndexAccessProjection", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(
			t,
			[]map[string]any{{"csv_row": []any{"alice", 31}}, {"csv_row": []any{"bob", 29}}},
		)
		pipeline := newProjectionPipeline(
			s("projection-csv-index"),
			"Projection CSV Index",
			"SELECT csv_row ->[0] AS first_col FROM input",
			nil,
		)

		rows := runProjectionRows(t, runner, "run-projection-csv-index", pipeline)
		require.Equal(t, []map[string]any{{"first_col": "alice"}, {"first_col": "bob"}}, rows)
	})

	t.Run("FailsCSVNamedAccessProjection", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(
			t,
			[]map[string]any{{"csv_row": []any{"alice", 31}}},
		)
		pipeline := newProjectionPipeline(
			s("projection-csv-name"),
			"Projection CSV Name",
			"SELECT csv_row -> '$.name' AS name FROM input",
			nil,
		)

		err := runProjectionError(t, runner, "run-projection-csv-name", pipeline)
		require.ErrorContains(t, err, "headless CSV/array value")
		require.ErrorIs(t, err, transformsql.ErrRuntimeHeadlessCSVNamedAccessUnsupported)
	})

	t.Run("ExecutesCSVIndexShorthandWhereProjection", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(
			t,
			[]map[string]any{
				{"id": 1, "csv_row": []any{"alice", 31}},
				{"id": 2, "csv_row": []any{"bob", 29}},
			},
		)
		pipeline := newProjectionPipeline(
			s("projection-csv-index-where"),
			"Projection CSV Index Where",
			"SELECT id AS id FROM input WHERE csv_row ->[0] = 'alice'",
			nil,
		)

		rows := runProjectionRows(t, runner, "run-projection-csv-index-where", pipeline)
		require.Equal(t, []map[string]any{{"id": 1}}, rows)
	})
}

func TestRunner_RunTransform_AggregateRuntime(t *testing.T) {
	// Shows aggregate runtime behavior for success and fail-closed cases using a
	// single parent test with focused subtests.
	t.Parallel()

	newRunner := func(t *testing.T, rows []map[string]any) *Runner {
		t.Helper()

		schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
			Type:            "postgres",
			Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{
				Name: "orders",
				Columns: []datasourcessvc.SchemaColumn{
					{Name: "amount", Type: "integer", Nullable: true},
					{Name: "category", Type: "varchar"},
				},
			}}},
		}

		return mustNewRunnerWithStore(
			&testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        rows,
					ColumnNames: []string{"amount", "category"},
					RowCount:    len(rows),
				}, 7, nil), nil
			}},
			&testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.March, 17, 10, 0, 0, 0, time.UTC)
			}},
			NewNoopRunStore(),
			preValidationMockService{
				schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("acme-postgres"): schema},
			},
		)
	}

	t.Run("ExecutesGroupByCountAggregate", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{
			{"category": "hardware", "amount": 10},
			{"category": "hardware", "amount": 20},
			{"category": "software", "amount": 5},
		})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "transform-group-by-count-test",
				Name: "Transform Group By Count Test",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "count-by-category",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT category AS category, COUNT(*) AS total FROM input GROUP BY category",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "category",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							},
							{
								Name:     "total",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-group-by-count",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("count-by-category")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, []string{"category", "total"}, transformOutput.ColumnNames)
		require.Equal(
			t,
			[]map[string]any{
				{"category": "hardware", "total": int64(2)},
				{"category": "software", "total": int64(1)},
			},
			transformOutput.Rows,
		)
		require.Equal(t, 2, transformOutput.RowCount)
	})

	t.Run("ExecutesWhereBeforeAggregate", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": 10}, {"amount": 20}, {"amount": 5}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "transform-where-aggregate-test",
				Name: "Transform Where Aggregate Test",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "sum-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT SUM(amount) AS total_amount FROM input WHERE amount >= 10",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "total_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
								Nullable: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-where-aggregate",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("sum-orders")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, []map[string]any{{"total_amount": int64(30)}}, transformOutput.Rows)
	})

	t.Run("ExecutesGroupByOrderByAndLimit", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{
			{"category": "hardware", "amount": 10},
			{"category": "hardware", "amount": 20},
			{"category": "software", "amount": 5},
			{"category": "services", "amount": 15},
		})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "transform-group-order-limit-test",
				Name: "Transform Group Order Limit Test",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "count-by-category",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT category AS category, SUM(amount) AS total_amount FROM input GROUP BY category ORDER BY total_amount DESC LIMIT 1",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "category",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							},
							{
								Name:     "total_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-group-order-limit",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("count-by-category")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(
			t,
			[]map[string]any{{"category": "hardware", "total_amount": int64(30)}},
			transformOutput.Rows,
		)
	})

	t.Run("ExecutesSumAggregateWithNumericCastArgument", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": 10}, {"amount": 20}, {"amount": 5}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "transform-sum-cast-test", Name: "Transform Sum Cast Test"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "sum-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT SUM(CAST(amount AS DOUBLE)) AS total_amount FROM input",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "total_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
								Nullable: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-sum-cast",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("sum-orders")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, []map[string]any{{"total_amount": float64(35)}}, transformOutput.Rows)
	})

	t.Run("ExecutesCountStarAggregate", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": 1}, {"amount": 2}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "transform-count-star-test",
				Name: "Transform Count Star Test",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "count-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT COUNT(*) AS total FROM input",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "total",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-count-star",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("count-orders")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, []string{"total"}, transformOutput.ColumnNames)
		require.Equal(t, []map[string]any{{"total": int64(2)}}, transformOutput.Rows)
		require.Equal(t, 1, transformOutput.RowCount)
	})

	t.Run("ExecutesCountOneAggregate", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": 1}, {"amount": 2}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "transform-count-one-test", Name: "Transform Count One Test"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "count-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT COUNT(1) AS total FROM input",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "total",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-count-one",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("count-orders")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, []string{"total"}, transformOutput.ColumnNames)
		require.Equal(t, []map[string]any{{"total": int64(2)}}, transformOutput.Rows)
		require.Equal(t, 1, transformOutput.RowCount)
	})

	t.Run("ExecutesCountColumnAggregateWithNulls", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": 10}, {"amount": nil}, {"amount": 3}, {}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "transform-count-column-test",
				Name: "Transform Count Column Test",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef:   "list-orders",
						OutputContract: nullableOrderRuntimeQueryContract(),
					},
				},
				{
					Kind: "transform",
					Slug: "count-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT COUNT(amount) AS non_null_amounts FROM input",
						InputContract: nullableOrderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "non_null_amounts",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-count-column",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("count-orders")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, []string{"non_null_amounts"}, transformOutput.ColumnNames)
		require.Equal(t, []map[string]any{{"non_null_amounts": int64(2)}}, transformOutput.Rows)
		require.Equal(t, 1, transformOutput.RowCount)
	})

	t.Run("ExecutesCountDistinctColumnAggregate", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(
			t,
			[]map[string]any{{"amount": 10}, {"amount": 10}, {"amount": 3}, {"amount": nil}, {}},
		)

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "transform-count-distinct-test",
				Name: "Transform Count Distinct Test",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef:   "list-orders",
						OutputContract: nullableOrderRuntimeQueryContract(),
					},
				},
				{
					Kind: "transform",
					Slug: "count-distinct-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT COUNT(DISTINCT amount) AS distinct_amounts FROM input",
						InputContract: nullableOrderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "distinct_amounts",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-count-distinct",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("count-distinct-orders")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, []string{"distinct_amounts"}, transformOutput.ColumnNames)
		require.Equal(t, []map[string]any{{"distinct_amounts": int64(2)}}, transformOutput.Rows)
		require.Equal(t, 1, transformOutput.RowCount)
	})

	t.Run("FailsOnCountDistinctNonColumnExpression", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": 10}, {"amount": 20}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "transform-count-distinct-expression-test",
				Name: "Transform Count Distinct Expression Test",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "count-distinct-expression",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT COUNT(DISTINCT CAST(amount AS CHAR)) AS distinct_amounts FROM input",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "distinct_amounts",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							},
						}},
					},
				},
			},
		}

		_, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-count-distinct-expression",
			Pipeline:        pipeline,
		})

		require.Error(t, err)
		require.ErrorIs(t, err, transformsql.ErrFeatureNotAllowed)
		require.ErrorContains(t, err, "count aggregate supports only column references at runtime")
	})

	t.Run("FailsOnCountNonColumnExpression", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": 10}, {"amount": 20}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "transform-count-expression-test",
				Name: "Transform Count Expression Test",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "count-expression",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT COUNT(CAST(amount AS CHAR)) AS count_amounts FROM input",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "count_amounts",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							},
						}},
					},
				},
			},
		}

		_, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-count-expression",
			Pipeline:        pipeline,
		})

		require.Error(t, err)
		require.ErrorIs(t, err, transformsql.ErrFeatureNotAllowed)
		require.ErrorContains(t, err, "count aggregate supports only column references at runtime")
	})

	t.Run("FailsOnNonColumnAggregateExpressions", func(t *testing.T) {
		t.Parallel()

		testCases := []struct {
			name               string
			sql                string
			outputFieldType    model.ContractFieldType
			expectedErrMessage string
		}{
			{
				name:               "FailsOnSumWithCastExpression",
				sql:                "SELECT SUM(CAST(amount AS CHAR)) AS out_value FROM input",
				outputFieldType:    model.ContractFieldTypeValueNumber,
				expectedErrMessage: "sum aggregate cast argument must target a numeric type",
			},
			{
				name:               "FailsOnAvgWithCastExpression",
				sql:                "SELECT AVG(CAST(amount AS CHAR)) AS out_value FROM input",
				outputFieldType:    model.ContractFieldTypeValueNumber,
				expectedErrMessage: "avg aggregate cast argument must target a numeric type",
			},
			{
				name:               "FailsOnMaxWithCastExpression",
				sql:                "SELECT MAX(CAST(amount AS CHAR)) AS out_value FROM input",
				outputFieldType:    model.ContractFieldTypeValueString,
				expectedErrMessage: "max aggregate cast argument must target a numeric type",
			},
			{
				name:               "FailsOnMinWithCastExpression",
				sql:                "SELECT MIN(CAST(amount AS CHAR)) AS out_value FROM input",
				outputFieldType:    model.ContractFieldTypeValueString,
				expectedErrMessage: "min aggregate cast argument must target a numeric type",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				runner := newRunner(t, []map[string]any{{"amount": 10}, {"amount": 20}})

				pipeline := &pipelinespec.Pipeline{
					Metadata: pipelinespec.Metadata{
						Slug: "transform-aggregate-expression-test",
						Name: "Transform Aggregate Expression Test",
					},
					Steps: []pipelinespec.Step{
						{
							Kind: "query",
							Slug: "fetch-orders",
							Query: &pipelinespec.QueryRef{
								DataSourceSlug: "acme-postgres",
								OperationType:  "sql",
								OperationRef:   "list-orders",
							},
						},
						{
							Kind: "transform",
							Slug: "aggregate-expression",
							Transform: &pipelinespec.TransformSpec{
								Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: tc.sql,
								InputContract: orderRuntimeInputContract(),
								OutputKind:    model.TransformOutputKindRows,
								OutputContract: &payload.Contract{Fields: []payload.ContractField{{
									Name: "out_value", Type: tc.outputFieldType, Required: true,
								}}},
							},
						},
					},
				}

				_, err := runner.Run(context.Background(), RunRequest{
					OrgSlug:         must.Slug("acme"),
					EnvironmentSlug: must.Slug("prod"),
					RunSlug:         "run-transform-aggregate-expression",
					Pipeline:        pipeline,
				})

				require.Error(t, err)
				require.ErrorIs(t, err, transformsql.ErrFeatureNotAllowed)
				require.ErrorContains(t, err, tc.expectedErrMessage)
			})
		}
	})

	t.Run("ExecutesSumAggregate", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": 10}, {"amount": 20}, {"amount": 5}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "transform-sum-test", Name: "Transform Sum Test"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "sum-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT SUM(amount) AS total_amount FROM input",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "total_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
								Nullable: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-sum",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("sum-orders")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, []string{"total_amount"}, transformOutput.ColumnNames)
		require.Equal(t, []map[string]any{{"total_amount": int64(35)}}, transformOutput.Rows)
		require.Equal(t, 1, transformOutput.RowCount)
	})

	t.Run("ExecutesSumAggregateWithNoNumericValues", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": nil}, {}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "transform-sum-empty-test", Name: "Transform Sum Empty Test"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef:   "list-orders",
						OutputContract: nullableOrderRuntimeQueryContract(),
					},
				},
				{
					Kind: "transform",
					Slug: "sum-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT SUM(amount) AS total_amount FROM input",
						InputContract: nullableOrderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "total_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
								Nullable: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-sum-empty",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("sum-orders")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, []string{"total_amount"}, transformOutput.ColumnNames)
		require.Equal(t, []map[string]any{{"total_amount": nil}}, transformOutput.Rows)
		require.Equal(t, 1, transformOutput.RowCount)
	})

	t.Run("FailsOnSumAggregateWithInvalidNumericString", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": "abc"}, {"amount": 10}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "transform-sum-invalid-numeric-test",
				Name: "Transform Sum Invalid Numeric Test",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "sum-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT SUM(amount) AS total_amount FROM input",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "total_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
							},
						}},
					},
				},
			},
		}

		_, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-sum-invalid-numeric",
			Pipeline:        pipeline,
		})

		require.Error(t, err)
		require.ErrorContains(t, err, "sum field \"amount\"")
	})

	t.Run("ExecutesMaxMinAggregates", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": 10}, {"amount": 20}, {"amount": 5}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "transform-max-min-test", Name: "Transform Max Min Test"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "max-min-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT MAX(amount) AS max_amount, MIN(amount) AS min_amount FROM input",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "max_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
							},
							{
								Name:     "min_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-max-min",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("max-min-orders")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, []string{"max_amount", "min_amount"}, transformOutput.ColumnNames)
		require.Equal(
			t,
			[]map[string]any{{"max_amount": int64(20), "min_amount": int64(5)}},
			transformOutput.Rows,
		)
		require.Equal(t, 1, transformOutput.RowCount)
	})

	t.Run("ExecutesMaxMinAggregatesWithMixedNumericTypes", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(
			t,
			[]map[string]any{{"amount": int64(10)}, {"amount": 20.5}, {"amount": "5.25"}},
		)

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "transform-max-min-mixed-test",
				Name: "Transform Max Min Mixed Test",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "max-min-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT MAX(amount) AS max_amount, MIN(amount) AS min_amount FROM input",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "max_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
							},
							{
								Name:     "min_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-max-min-mixed",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("max-min-orders")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, []string{"max_amount", "min_amount"}, transformOutput.ColumnNames)
		require.Equal(
			t,
			[]map[string]any{{"max_amount": float64(20.5), "min_amount": float64(5.25)}},
			transformOutput.Rows,
		)
		require.Equal(t, 1, transformOutput.RowCount)
	})

	t.Run("FailsOnMaxMinAggregatesWithInvalidNumericString", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": "abc"}, {"amount": 10}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "transform-max-min-invalid-numeric-test",
				Name: "Transform Max Min Invalid Numeric Test",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "max-min-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT MAX(amount) AS max_amount, MIN(amount) AS min_amount FROM input",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "max_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
							},
							{
								Name:     "min_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
							},
						}},
					},
				},
			},
		}

		_, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-max-min-invalid-numeric",
			Pipeline:        pipeline,
		})

		require.Error(t, err)
		require.ErrorContains(t, err, "max field \"amount\"")
	})

	t.Run("ExecutesAvgAggregate", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": 10}, {"amount": 20}, {"amount": 5}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "transform-avg-test", Name: "Transform Avg Test"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "avg-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT AVG(amount) AS avg_amount FROM input",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "avg_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
								Nullable: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-avg",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("avg-orders")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, []string{"avg_amount"}, transformOutput.ColumnNames)
		require.InDelta(t, 35.0/3.0, transformOutput.Rows[0]["avg_amount"], 0.0000001)
		require.Equal(t, 1, transformOutput.RowCount)
	})

	t.Run("ExecutesAvgAggregateWithNoNumericValues", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": nil}, {}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "transform-avg-empty-test", Name: "Transform Avg Empty Test"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef:   "list-orders",
						OutputContract: nullableOrderRuntimeQueryContract(),
					},
				},
				{
					Kind: "transform",
					Slug: "avg-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT AVG(amount) AS avg_amount FROM input",
						InputContract: nullableOrderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "avg_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
								Nullable: true,
							},
						}},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-avg-empty",
			Pipeline:        pipeline,
		})

		require.NoError(t, err)
		transformResult, ok := runResult.StepResults[must.Slug("avg-orders")]
		require.True(t, ok)
		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, []string{"avg_amount"}, transformOutput.ColumnNames)
		require.Equal(t, []map[string]any{{"avg_amount": nil}}, transformOutput.Rows)
		require.Equal(t, 1, transformOutput.RowCount)
	})

	t.Run("FailsOnAvgAggregateWithInvalidNumericString", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"amount": "abc"}, {"amount": 10}})

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "transform-avg-invalid-numeric-test",
				Name: "Transform Avg Invalid Numeric Test",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql",
						OperationRef: "list-orders",
					},
				},
				{
					Kind: "transform",
					Slug: "avg-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-orders")}}, SQL: "SELECT AVG(amount) AS avg_amount FROM input",
						InputContract: orderRuntimeInputContract(),
						OutputKind:    model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "avg_amount",
								Type:     model.ContractFieldTypeValueNumber,
								Required: true,
							},
						}},
					},
				},
			},
		}

		_, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-avg-invalid-numeric",
			Pipeline:        pipeline,
		})

		require.Error(t, err)
		require.ErrorContains(t, err, "avg field \"amount\"")
	})
}

func TestRunner_RunTransform_OutputKindSerialization(t *testing.T) {
	t.Parallel()

	newRunner := func(t *testing.T, rows []map[string]any) *Runner {
		t.Helper()

		schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
			Type:            "postgres",
			Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{
				Name: "users",
				Columns: []datasourcessvc.SchemaColumn{
					{Name: "id", Type: "integer"},
					{Name: "email", Type: "varchar"},
				},
			}}},
		}

		return mustNewRunnerWithStore(
			&testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        rows,
					ColumnNames: []string{"id", "email"},
					RowCount:    len(rows),
				}, 7, nil), nil
			}},
			&testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.March, 17, 10, 0, 0, 0, time.UTC)
			}},
			NewNoopRunStore(),
			preValidationMockService{
				schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("acme-postgres"): schema},
			},
		)
	}

	newPipeline := func(outputKind model.TransformOutputKind) *pipelinespec.Pipeline {
		return &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "transform-output-kind-test",
				Name: "Transform Output Kind Test",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "list-active-users",
					},
				},
				{
					Kind: "transform",
					Slug: "shape-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS id, email AS email FROM input",
						InputContract: userRuntimeInputContract(),
						OutputKind:    outputKind,
					},
				},
			},
		}
	}

	t.Run("SerializesDocumentOutputKind", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"id": 1, "email": "a@example.com"}})
		runResult, err := runner.Run(
			context.Background(),
			RunRequest{
				OrgSlug:         must.Slug("acme"),
				EnvironmentSlug: must.Slug("prod"),
				RunSlug:         "run-transform-document-output",
				Pipeline:        newPipeline(model.TransformOutputKindJSON),
			},
		)
		require.NoError(t, err)

		transformResult := runResult.StepResults[must.Slug("shape-users")]
		require.NotNil(t, transformResult)

		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.JSONEq(t, `[{"id":1,"email":"a@example.com"}]`, string(transformOutput.Document))
	})

	t.Run("SerializesCSVOutputKind", func(t *testing.T) {
		t.Parallel()

		runner := newRunner(t, []map[string]any{{"id": 1, "email": "a@example.com"}})
		runResult, err := runner.Run(
			context.Background(),
			RunRequest{
				OrgSlug:         must.Slug("acme"),
				EnvironmentSlug: must.Slug("prod"),
				RunSlug:         "run-transform-csv-output",
				Pipeline:        newPipeline(model.TransformOutputKindCSV),
			},
		)
		require.NoError(t, err)

		transformResult := runResult.StepResults[must.Slug("shape-users")]
		require.NotNil(t, transformResult)

		transformOutput, ok := transformResult.Transform()
		require.True(t, ok)
		require.Equal(t, "id,email\n1,a@example.com\n", transformOutput.CSV)
	})
}

func TestRunner_RunTransform_JSONConstructionValidatesNestedOutputContract(t *testing.T) {
	t.Parallel()

	schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{
			Name: "users",
			Columns: []datasourcessvc.SchemaColumn{
				{Name: "id", Type: "integer"},
				{Name: "email", Type: "varchar"},
			},
		}}},
	}

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": 1, "email": "alice@example.com"}},
				ColumnNames: []string{"id", "email"},
				RowCount:    1,
			}, 7, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 17, 10, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("acme-postgres"): schema},
		},
	)

	runResult, err := runner.Run(
		context.Background(),
		RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-nested-json-output-contract",
			Pipeline: &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{
					Slug: "nested-json-transform",
					Name: "Nested JSON Transform",
				},
				Steps: []pipelinespec.Step{
					{
						Kind: "query",
						Slug: "fetch-users",
						Query: &pipelinespec.QueryRef{
							DataSourceSlug: "acme-postgres",
							OperationType:  "sql",
							OperationRef:   "list-active-users",
							OutputContract: &payload.Contract{Fields: []payload.ContractField{
								{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
								{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
							}},
						},
					},
					{
						Kind: "transform",
						Slug: "shape-users",
						Transform: &pipelinespec.TransformSpec{
							Inputs: map[string]pipelinespec.StepInput{
								"input": {FromStep: must.Slug("fetch-users")},
							},
							SQL:        "SELECT JSON_OBJECT('contact', JSON_OBJECT('email', email), 'id', id) AS profile, JSON_ARRAY(JSON_OBJECT('sku', email, 'quantity', id)) AS items FROM input",
							OutputKind: model.TransformOutputKindJSON,
							OutputContract: &payload.Contract{
								Representation: model.ContractRepresentationJSON,
								RootShape:      model.ContractRootShapeArray,
								Fields: []payload.ContractField{
									{Name: "profile", Type: model.ContractFieldTypeValueObject, Required: true},
									{Name: "profile.contact.email", Type: model.ContractFieldTypeValueString, Required: true},
									{Name: "items", Type: model.ContractFieldTypeValueArray, Required: true},
									{Name: "items[].sku", Type: model.ContractFieldTypeValueString, Required: true},
									{Name: "items[].quantity", Type: model.ContractFieldTypeValueInteger, Required: true},
								},
							},
						},
					},
				},
			},
		},
	)
	require.NoError(t, err)

	transformResult := runResult.StepResults[must.Slug("shape-users")]
	require.NotNil(t, transformResult)

	transformOutput, ok := transformResult.Transform()
	require.True(t, ok)
	require.JSONEq(
		t,
		`[{"profile":{"contact":{"email":"alice@example.com"},"id":1},"items":[{"sku":"alice@example.com","quantity":1}]}]`,
		string(transformOutput.Document),
	)
}

func TestRunner_RunTransform_CapturesTransformDuration(t *testing.T) {
	t.Parallel()

	schema := datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{{
			Name: "users",
			Columns: []datasourcessvc.SchemaColumn{
				{Name: "id", Type: "integer"},
			},
		}}},
	}

	nowCounter := 0
	base := time.Date(2026, time.March, 17, 10, 0, 0, 0, time.UTC)

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": 1}},
				ColumnNames: []string{"id"},
				RowCount:    1,
			}, 7, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			nowCounter++
			return base.Add(time.Duration(nowCounter) * time.Millisecond)
		}},
		NewNoopRunStore(),
		preValidationMockService{
			schemas: map[slug.Slug]datasourcessvc.SchemaResult{s("acme-postgres"): schema},
		},
	)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "transform-duration-test", Name: "Transform Duration Test"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres", OperationType: "sql",
					OperationRef: "list-users",
				},
			},
			{
				Kind: "transform",
				Slug: "shape-users",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS id FROM input",
					InputContract: userRuntimeInputContract(),
					OutputKind:    model.TransformOutputKindRows,
				},
			},
		},
	}

	runResult, err := runner.Run(
		context.Background(),
		RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-transform-duration",
			Pipeline:        pipeline,
		},
	)
	require.NoError(t, err)

	transformResult := runResult.StepResults[must.Slug("shape-users")]
	require.NotNil(t, transformResult)
	require.Positive(t, transformResult.DurationMs())
}
