package pipelineruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	executionruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/metrics"
	"github.com/Braidplane/braidplane-core/pkg/must"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	transformsql "github.com/Braidplane/braidplane-core/pkg/pipeline/transform/sql"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	connectorhttp "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	connectortypes "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

type fakeRunStore struct {
	saveCheckpointFn  func(context.Context, slug.Slug, slug.Slug, slug.Slug, string, pipelinerun.ReplayCheckpoint) error
	observeWorkerFn   func(context.Context, slug.Slug, slug.Slug, slug.Slug, string, time.Time) error
	previousSuccessFn func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, int64, time.Time) (*executionruntime.SuccessfulRun, error)
	persistRunFn      func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		pipeline *pipelinespec.Pipeline,
		runResult *pipelinerun.RunResult,
	) error
	timelines   map[string]pipelinerun.RunTimeline
	checkpoints map[string]pipelinerun.ReplayCheckpoint
}

type fakeSinkRuntime struct {
	executeFn func(ctx context.Context, input *SinkRuntimeInput) error
}

type fakeEnvironmentSecretResolver struct {
	resolveFn func(ctx context.Context, orgSlug, environmentSlug slug.Slug) ([]model.RuntimeSecretValue, error)
}

func (f fakeEnvironmentSecretResolver) ResolveRuntimeSecrets(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
) ([]model.RuntimeSecretValue, error) {
	if f.resolveFn == nil {
		return nil, fmt.Errorf("unexpected environment secret resolver call")
	}
	return f.resolveFn(ctx, orgSlug, environmentSlug)
}

func mustNewRunner(t *testing.T, executor *testutil.FakeQueryExecutor, clk clock.Clock) *Runner {
	t.Helper()
	runner, err := NewRunner(
		adaptTestQueryExecutor(executor),
		clk,
		NewNoopRunStore(),
		runnerTestDataSourceLookup{},
		runnerNoopSinkRuntime{},
	)
	require.NoError(t, err)
	return runner
}

func newRunnerPreValidationMockService() preValidationMockService {
	return preValidationMockService{schemas: map[slug.Slug]datasourcessvc.SchemaResult{
		must.Slug("acme-postgres"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
			Type:            "postgres",
			Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
				{
					Name: "users",
					Columns: []datasourcessvc.SchemaColumn{
						{Name: "id", Type: "integer"},
						{Name: "email", Type: "text"},
						{Name: "payload", Type: "jsonb"},
					},
				},
			}},
		},
		must.Slug("acme-ftp"): datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeFile,
			Type:            "ftp",
			Payload: datasourcessvc.FileSchemaPayload{Files: []datasourcessvc.SchemaFile{
				{
					Name: "users.csv",
					Fields: []datasourcessvc.SchemaField{
						{Name: "id", Type: "string"},
						{Name: "email", Type: "string"},
						{Name: "csv_row", Type: "array"},
					},
				},
			}},
		},
		must.Slug("acme-http"): datasourcessvc.Schema[datasourcessvc.HTTPSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeHTTP,
			Type:            "http",
			Payload:         datasourcessvc.HTTPSchemaPayload{Operations: []datasourcessvc.SchemaOperation{}},
		},
		must.Slug("archive-s3"): datasourcessvc.Schema[datasourcessvc.FileSchemaPayload]{
			SchemaTypeValue: datasourcessvc.SchemaTypeFile,
			Type:            "s3",
			Payload:         datasourcessvc.FileSchemaPayload{},
		},
	}}
}

func (f *fakeSinkRuntime) Execute(ctx context.Context, input *SinkRuntimeInput) error {
	if f.executeFn != nil {
		return f.executeFn(ctx, input)
	}

	return nil
}

func (f *fakeRunStore) PersistRun(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	runResult *pipelinerun.RunResult,
) error {
	if f.persistRunFn != nil {
		if err := f.persistRunFn(ctx, orgSlug, projectSlug, environmentSlug, pipeline, runResult); err != nil {
			return err
		}
	}
	if runResult != nil {
		if f.timelines == nil {
			f.timelines = make(map[string]pipelinerun.RunTimeline)
		}
		f.timelines[runResult.Metadata.RunSlug] = runResult.Timeline
	}

	return nil
}

func (f *fakeRunStore) Get(_ context.Context, _, _, _ slug.Slug, runSlug string) (*pipelinerun.RunTimeline, error) {
	if f.timelines == nil {
		return nil, pipelinerun.ErrTimelineNotFound
	}

	timeline, ok := f.timelines[runSlug]
	if !ok {
		return nil, pipelinerun.ErrTimelineNotFound
	}

	return &timeline, nil
}

func (f *fakeRunStore) SaveCheckpoint(
	ctx context.Context,
	org slug.Slug,
	project slug.Slug,
	environment slug.Slug,
	runSlug string,
	checkpoint pipelinerun.ReplayCheckpoint,
) error {
	if f.saveCheckpointFn != nil {
		if err := f.saveCheckpointFn(ctx, org, project, environment, runSlug, checkpoint); err != nil {
			return err
		}
	}
	if f.checkpoints == nil {
		f.checkpoints = make(map[string]pipelinerun.ReplayCheckpoint)
	}
	f.checkpoints[runSlug] = checkpoint
	return nil
}

func (f *fakeRunStore) GetCheckpoint(
	_ context.Context,
	_ slug.Slug,
	_ slug.Slug,
	_ slug.Slug,
	runSlug string,
) (*pipelinerun.ReplayCheckpoint, error) {
	if f.checkpoints == nil {
		return nil, pipelinerun.ErrReplayCheckpointNotFound
	}

	checkpoint, ok := f.checkpoints[runSlug]
	if !ok {
		return nil, pipelinerun.ErrReplayCheckpointNotFound
	}

	return &checkpoint, nil
}

func (f *fakeRunStore) GetCurrentIncrementalCheckpoint(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	pipelinerun.IncrementalCheckpointLookup,
) (*pipelinerun.IncrementalCheckpoint, error) {
	return nil, pipelinerun.ErrIncrementalCheckpointNotFound
}

func TestRunner_Run(t *testing.T) {
	t.Parallel()

	clockTimes := []time.Time{
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
	}
	clockIndex := 0
	environmentSlug := must.Slug("prod")
	projectSlug := must.Slug("default")

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		require.Equal(t, must.Slug("acme"), orgSlug)
		require.Equal(t, must.Slug("acme-postgres"), dsSlug)
		require.Equal(t, must.Slug("list-active-users"), querySlug)
		require.Equal(t, map[string]any{"limit": 100}, paramValues)

		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"email": "user@example.com"}},
			ColumnNames: []string{"email"},
			RowCount:    1,
		}, 12, nil), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		current := clockTimes[clockIndex]
		clockIndex++
		return current
	}})

	runResult, err := runner.Run(context.Background(), RunRequest{
		EnvironmentSlug: environmentSlug,
		OrgSlug:         must.Slug("acme"),
		ProjectSlug:     projectSlug,
		RunSlug:         "run-001",
		Source: pipelinerun.RunSourceSummary{
			Type:          model.PipelineRunSourceTypeManual,
			Summary:       "Manual run",
			Entrypoint:    model.PipelineRunSourceEntrypointUI,
			ActorType:     model.EventActorTypeUser,
			ParameterKeys: []string{"run_slug"},
			AttemptNumber: 1,
			RootRunSlug:   must.Slug("run-001"),
		},
		PipelineVersionID: 42,
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "acme-users-sync",
				Name: "ACME Users Sync",
			},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-active-users",
					Parameters:     map[string]any{"limit": 100},
				},
			}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, runResult)
	require.Equal(t, pipelinerun.RunMetadata{
		ProjectSlug:     projectSlug,
		EnvironmentSlug: environmentSlug,
		RunSlug:         "run-001",
		Source: pipelinerun.RunSourceSummary{
			Type:          model.PipelineRunSourceTypeManual,
			Summary:       "Manual run",
			Entrypoint:    model.PipelineRunSourceEntrypointUI,
			ActorType:     model.EventActorTypeUser,
			ParameterKeys: []string{"run_slug"},
			AttemptNumber: 1,
			RootRunSlug:   must.Slug("run-001"),
		},
		Status:            "succeeded",
		DurationMs:        125,
		StepCount:         1,
		PipelineVersionID: 42,
		Incremental:       fullIncrementalRunSummary(),
	}, runResult.Metadata)
	require.Equal(t, "run-001", runResult.Timeline.RunSlug)
	require.Equal(t, "acme-users-sync", string(runResult.Timeline.PipelineSlug))
	require.Equal(t, projectSlug, runResult.Timeline.ProjectSlug)
	require.Equal(t, environmentSlug, runResult.Timeline.EnvironmentSlug)
	require.Equal(t, "succeeded", runResult.Timeline.Status)
	require.Len(t, runResult.Timeline.Events, 4)
	require.Equal(t, "run_started", runResult.Timeline.Events[0].Type)
	require.Equal(t, "step_started", runResult.Timeline.Events[1].Type)
	require.Equal(t, "step_succeeded", runResult.Timeline.Events[2].Type)
	require.Equal(t, "run_completed", runResult.Timeline.Events[3].Type)
	require.Len(t, runResult.Timeline.Snapshots, 1)
	rowCount := 1
	require.Equal(t, pipelinerun.StepSnapshot{
		Step: must.Slug("fetch-users"),
		Kind: "query",
		Input: pipelinerun.StepInputSnapshot{
			OrganizationSlug: must.Slug("acme"),
			ProjectSlug:      projectSlug,
			EnvironmentSlug:  environmentSlug,
			DataSourceSlug:   must.Slug("acme-postgres"),
			OperationType:    "sql",
			OperationRef:     must.Slug("list-active-users"),
			ParameterKeys:    []string{"limit"},
		},
		Output: &pipelinerun.StepOutputSnapshot{
			ResultType: "SQL",
			VendorType: "postgres",
			DurationMs: 12,
			Details: &pipelinerun.StepOutputDetails{
				Kind:        pipelinerun.StepOutputDetailKindRows,
				RowCount:    &rowCount,
				ColumnNames: []string{"email"},
			},
		},
	}, runResult.Timeline.Snapshots[0])
	require.Nil(t, runResult.Timeline.Failure)
	require.Empty(t, runResult.EdgeDiagnostics)

	stepResult, ok := runResult.StepResults[must.Slug("fetch-users")]
	require.True(t, ok)
	sqlResult, ok := stepResult.SQL()
	require.True(t, ok)
	require.Equal(t, 1, sqlResult.RowCount)

	storedTimeline, err := runner.TimelineByRunSlug(
		context.Background(),
		must.Slug("acme"),
		must.Slug("default"),
		environmentSlug,
		"run-001",
	)
	require.NoError(t, err)
	require.NotNil(t, storedTimeline)
	require.Equal(t, runResult.Timeline, *storedTimeline)

	metadataJSON, err := json.Marshal(runResult.Metadata)
	require.NoError(t, err)
	require.JSONEq(
		t,
		`{"project_slug":"default","environment_slug":"prod","run_slug":"run-001","source":{"type":"manual","summary":"Manual run","entrypoint":"ui","actor_type":"user","parameter_keys":["run_slug"],"attempt_number":1,"root_run_slug":"run-001"},"status":"succeeded","duration_ms":125,"step_count":1,"pipeline_version_id":42,"incremental":{"mode":"full","checkpoint_status":"not_applicable","replay_safety_status":"safe"}}`,
		string(metadataJSON),
	)
}

// requireStepOutputDetails returns typed output details after checking the expected detail kind.
func requireStepOutputDetails(
	t *testing.T,
	output *pipelinerun.StepOutputSnapshot,
	kind pipelinerun.StepOutputDetailKind,
) *pipelinerun.StepOutputDetails {
	t.Helper()

	require.NotNil(t, output)
	require.NotNil(t, output.Details)
	require.Equal(t, kind, output.Details.Kind)
	return output.Details
}

func TestRunner_RunPreservesSourceOnFailure(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC)
	source := pipelinerun.RunSourceSummary{
		Type:          model.PipelineRunSourceTypeManual,
		Summary:       "Manual run",
		Entrypoint:    model.PipelineRunSourceEntrypointUI,
		ActorType:     model.EventActorTypeUser,
		ParameterKeys: []string{"run_slug"},
	}
	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		map[string]any,
	) (*result.ExecutionResult, error) {
		return nil, fmt.Errorf("query failed")
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		return startedAt
	}})

	runResult, err := runner.Run(context.Background(), RunRequest{
		EnvironmentSlug: must.Slug("prod"),
		OrgSlug:         must.Slug("acme"),
		ProjectSlug:     must.Slug("default"),
		RunSlug:         "run-source-failure",
		Source:          source,
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "acme-users-sync",
				Name: "ACME Users Sync",
			},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-active-users",
				},
			}},
		},
	})

	require.Error(t, err)
	require.NotNil(t, runResult)
	require.Equal(t,
		pipelinerun.NormalizeAttemptSource(source, must.Slug("run-source-failure")),
		runResult.Metadata.Source,
	)
	require.Equal(t, "failed", runResult.Metadata.Status)
	require.NotNil(t, runResult.Timeline.Failure)
}

func TestRunner_RunFailureReasonCodeForBadSourceCredentials(t *testing.T) {
	t.Parallel()

	const secret = "synthetic-secret-password"
	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		map[string]any,
	) (*result.ExecutionResult, error) {
		return nil, xerror.InvalidCredentials().Detail("provider rejected password " + secret)
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.July, 2, 12, 0, 0, 0, time.UTC)
	}})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-bad-source-credentials",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "launch-etl", Name: "Launch ETL"},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: "fetch-orders",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "select-orders",
				},
			}},
		},
	})

	require.Error(t, err)
	require.NotNil(t, runResult)
	require.NotNil(t, runResult.Timeline.Failure)
	require.Equal(t, failureKindDataSource, runResult.Timeline.Failure.Kind)
	require.Equal(t, failureSideSource, runResult.Timeline.Failure.Side)
	require.Equal(t, "acme-postgres", runResult.Timeline.Failure.DataSourceSlug)
	require.Equal(
		t,
		pipelinerun.FailureReasonCodeDataSourceCredentialsFailed,
		runResult.Timeline.Failure.ReasonCode,
	)
	require.Equal(t, "invalid credentials", runResult.Timeline.Failure.Error)
	require.Contains(t, runResult.Timeline.Failure.ActionHint, "Verify the credentials")
	requireTimelineDoesNotContain(t, runResult.Timeline, secret)
}

func TestRunner_RunFailureReasonCodeForSourceQueryError(t *testing.T) {
	t.Parallel()

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		map[string]any,
	) (*result.ExecutionResult, error) {
		return nil, xerror.Unavailable(xerror.DataSource).Detail("relation launch_orders does not exist")
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.July, 2, 12, 5, 0, 0, time.UTC)
	}})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-source-query-error",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "launch-etl", Name: "Launch ETL"},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: "fetch-orders",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "select-orders",
				},
			}},
		},
	})

	require.Error(t, err)
	require.NotNil(t, runResult)
	require.NotNil(t, runResult.Timeline.Failure)
	require.Equal(t, failureKindDataSource, runResult.Timeline.Failure.Kind)
	require.Equal(t, failureSideSource, runResult.Timeline.Failure.Side)
	require.Equal(
		t,
		pipelinerun.FailureReasonCodeSourceQueryFailed,
		runResult.Timeline.Failure.ReasonCode,
	)
	require.Equal(t, "data source unavailable", runResult.Timeline.Failure.Error)
}

func TestRunner_RunFailureReasonCodeForSourceResultShapeMismatch(t *testing.T) {
	t.Parallel()

	runner := mustNewRunnerWithStore(&testutil.FakeQueryExecutor{ExecuteFn: func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		map[string]any,
	) (*result.ExecutionResult, error) {
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"email": "ops@example.test"}},
			ColumnNames: []string{"email"},
			RowCount:    1,
		}, 12, nil), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.July, 2, 12, 10, 0, 0, time.UTC)
	}}, NewNoopRunStore(), newRunnerPreValidationMockService())

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-source-shape-mismatch",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "launch-etl", Name: "Launch ETL"},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: "fetch-orders",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "select-orders",
					OutputContract: &payload.Contract{
						Representation: model.ContractRepresentationRows,
						Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}},
					},
				},
			}},
		},
	})

	require.ErrorIs(t, err, pipelinerun.ErrQueryOutputContractSchemaMismatch)
	require.NotNil(t, runResult)
	require.NotNil(t, runResult.Timeline.Failure)
	require.Equal(t, failureKindPlatform, runResult.Timeline.Failure.Kind)
	require.Equal(t, failureSideSource, runResult.Timeline.Failure.Side)
	require.Equal(
		t,
		pipelinerun.FailureReasonCodeSourceResultShapeMismatch,
		runResult.Timeline.Failure.ReasonCode,
	)
	require.Contains(t, runResult.Timeline.Failure.Error, "id")
}

func TestRunner_RunFailureReasonCodeForSinkInputContractMismatch(t *testing.T) {
	t.Parallel()

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		map[string]any,
	) (*result.ExecutionResult, error) {
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"email": "ops@example.test"}},
			ColumnNames: []string{"email"},
			RowCount:    1,
		}, 12, nil), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.July, 2, 12, 15, 0, 0, time.UTC)
	}})
	runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
		return fmt.Errorf("sink runtime should not be called")
	}}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-sink-contract-mismatch",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "launch-etl", Name: "Launch ETL"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "select-orders",
					},
				},
				{
					Kind: "sink",
					Slug: "write-orders",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-orders")},
						InputContract: &payload.Contract{
							Representation: model.ContractRepresentationRows,
							Fields: []payload.ContractField{{
								Name:     "order_id",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							}},
						},
						DataSourceSlug: "archive-s3",
						OperationType:  "s3",
						Config:         map[string]any{"key": "orders.json"},
						OperationRef:   "write-orders-json",
					},
				},
			},
		},
	})

	require.ErrorIs(t, err, pipelinerun.ErrSinkInputContractViolation)
	require.NotNil(t, runResult)
	require.NotNil(t, runResult.Timeline.Failure)
	require.Equal(t, failureKindPlatform, runResult.Timeline.Failure.Kind)
	require.Equal(t, failureSideSink, runResult.Timeline.Failure.Side)
	require.Equal(
		t,
		pipelinerun.FailureReasonCodeSinkContractMismatch,
		runResult.Timeline.Failure.ReasonCode,
	)
	require.Contains(t, runResult.Timeline.Failure.Error, "order_id")
}

func TestRunner_RunFailureReasonCodeForS3ObjectWriteFailure(t *testing.T) {
	t.Parallel()
	for _, configurationFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("configuration_failure=%t", configurationFailure), func(t *testing.T) {
			const secret = "synthetic-secret-object-body"
			runner := mustNewRunnerWithStore(&testutil.FakeQueryExecutor{ExecuteFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
				slug.Slug,
				map[string]any,
			) (*result.ExecutionResult, error) {
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 101}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 12, nil), nil
			}}, &testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.July, 2, 12, 20, 0, 0, time.UTC)
			}}, NewNoopRunStore(), newRunnerPreValidationMockService())
			runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
				if configurationFailure {
					return errSinkConfigurationInvalid.WithPublicDetail("sink.config.key is required and must be a non-empty string for s3 backend").Detail(secret)
				}
				return xerror.Unavailable(xerror.DataSource).Detail("put object failed with payload " + secret)
			}}

			runResult, err := runner.Run(context.Background(), RunRequest{
				OrgSlug:         must.Slug("acme"),
				EnvironmentSlug: must.Slug("prod"),
				RunSlug:         "run-s3-write-failure",
				Pipeline: &pipelinespec.Pipeline{
					Metadata: pipelinespec.Metadata{Slug: "launch-etl", Name: "Launch ETL"},
					Steps: []pipelinespec.Step{
						{
							Kind: "query",
							Slug: "fetch-orders",
							Query: &pipelinespec.QueryRef{
								DataSourceSlug: "acme-postgres",
								OperationType:  "sql",
								OperationRef:   "select-orders",
								OutputContract: &payload.Contract{
									Representation: model.ContractRepresentationRows,
									Fields: []payload.ContractField{{
										Name:     "id",
										Type:     model.ContractFieldTypeValueInteger,
										Required: true,
									}},
								},
							},
						},
						{
							Kind: "sink",
							Slug: "write-orders",
							Sink: &pipelinespec.SinkSpec{
								IdempotencyClass: pipelinespec.SinkIdempotencyClassNaturallyIdempotent,
								Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-orders")},
								InputContract: &payload.Contract{
									Representation: model.ContractRepresentationRows,
									Fields: []payload.ContractField{{
										Name:     "id",
										Type:     model.ContractFieldTypeValueInteger,
										Required: true,
									}},
								},
								DataSourceSlug: "archive-s3",
								OperationType:  "s3",
								Config:         map[string]any{"key": "orders.json"},
								OperationRef:   "write-orders-json",
							},
						},
					},
				},
			})

			require.Error(t, err)
			require.NotNil(t, runResult)
			require.NotNil(t, runResult.Timeline.Failure)
			if configurationFailure {
				require.Equal(t, failureKindPlatform, runResult.Timeline.Failure.Kind)
				require.Equal(t, failureSideSink, runResult.Timeline.Failure.Side)
				require.Equal(t, pipelinerun.FailureReasonCodeSinkContractMismatch, runResult.Timeline.Failure.ReasonCode)
				require.Contains(t, runResult.Timeline.Failure.Error, "sink.config.key")
				require.Contains(t, runResult.Timeline.Failure.ActionHint, "before dispatch")
				requireTimelineDoesNotContain(t, runResult.Timeline, secret)
				return
			}
			require.Equal(t, failureKindDataSource, runResult.Timeline.Failure.Kind)
			require.Equal(t, failureSideSink, runResult.Timeline.Failure.Side)
			require.Equal(t, "archive-s3", runResult.Timeline.Failure.DataSourceSlug)
			require.Equal(
				t,
				pipelinerun.FailureReasonCodeS3ObjectWriteFailed,
				runResult.Timeline.Failure.ReasonCode,
			)
			require.Equal(t, "s3 object sink write failed", runResult.Timeline.Failure.Error)
			requireTimelineDoesNotContain(t, runResult.Timeline, secret)
		})
	}
}

func TestRunner_RunRejectsUnsupportedSinkIdempotencyClassBeforeDispatch(t *testing.T) {
	t.Parallel()

	runner := mustNewRunnerWithStore(&testutil.FakeQueryExecutor{ExecuteFn: func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		map[string]any,
	) (*result.ExecutionResult, error) {
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows: []map[string]any{{"id": 101}}, ColumnNames: []string{"id"}, RowCount: 1,
		}, 12, nil), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.July, 2, 12, 22, 0, 0, time.UTC)
	}}, NewNoopRunStore(), newRunnerPreValidationMockService())
	sinkCalls := 0
	runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
		sinkCalls++
		return nil
	}}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug: must.Slug("acme"), EnvironmentSlug: must.Slug("prod"), RunSlug: "run-s3-unsupported-key",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "launch-etl", Name: "Launch ETL"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query", Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres", OperationType: "sql", OperationRef: "select-orders",
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true,
						}}},
					},
				},
				{
					Kind: "sink", Slug: "write-orders",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-orders")},
						InputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true,
						}}},
						DataSourceSlug: "archive-s3",
						OperationType:  "s3",
						Config:         map[string]any{"key": "orders.json"},
						OperationRef:   "write-orders-json",
					},
				},
			},
		},
	})

	require.Error(t, err)
	require.Zero(t, sinkCalls)
	require.NotNil(t, runResult)
	require.NotNil(t, runResult.Timeline.Failure)
	require.Equal(t, failureKindPlatform, runResult.Timeline.Failure.Kind)
	require.Equal(t, failureSideSink, runResult.Timeline.Failure.Side)
	require.Contains(t, runResult.Timeline.Failure.Error, "does not support idempotency_class")
}

func TestRunner_RunFailureReasonCodeForTransformRuntimeFailure(t *testing.T) {
	t.Parallel()

	runner := mustNewRunnerWithStore(&testutil.FakeQueryExecutor{ExecuteFn: func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		map[string]any,
	) (*result.ExecutionResult, error) {
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"id": 101}},
			ColumnNames: []string{"id"},
			RowCount:    1,
		}, 12, nil), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.July, 2, 12, 25, 0, 0, time.UTC)
	}}, NewNoopRunStore(), newRunnerPreValidationMockService())
	runner.transformRuntime = &fakeTransformRuntime{
		executeFn: func(*pipelinespec.TransformSpec, *payload.Payload) (*payload.Output, error) {
			return nil, xerror.ValidationFailed().Detail("sql_v1 transform failed")
		},
	}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-transform-runtime-failure",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "launch-etl", Name: "Launch ETL"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-orders",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "select-orders",
						OutputContract: &payload.Contract{
							Representation: model.ContractRepresentationRows,
							Fields: []payload.ContractField{{
								Name:     "id",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							}},
						},
					},
				},
				{
					Kind: "transform",
					Slug: "shape-orders",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{
							"orders": {FromStep: must.Slug("fetch-orders")},
						},
						SQL:        "SELECT id FROM orders",
						OutputKind: model.TransformOutputKindRows,
						InputContract: &payload.Contract{
							Representation: model.ContractRepresentationRows,
							Fields: []payload.ContractField{{
								Name:     "id",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							}},
						},
					},
				},
			},
		},
	})

	require.Error(t, err)
	require.NotNil(t, runResult)
	require.NotNil(t, runResult.Timeline.Failure)
	require.Equal(t, failureKindPlatform, runResult.Timeline.Failure.Kind)
	require.Equal(t, failureSideTransform, runResult.Timeline.Failure.Side)
	require.Equal(
		t,
		pipelinerun.FailureReasonCodeTransformRuntimeFailed,
		runResult.Timeline.Failure.ReasonCode,
	)
}

// requireTimelineDoesNotContain verifies persisted run diagnostics did not copy
// a sentinel secret into the JSON evidence exposed by run detail surfaces.
func requireTimelineDoesNotContain(
	t *testing.T,
	timeline pipelinerun.RunTimeline,
	forbidden string,
) {
	t.Helper()

	timelineJSON, err := json.Marshal(timeline)
	require.NoError(t, err)
	require.NotContains(t, string(timelineJSON), forbidden)
}

func TestRunner_RunDoesNotRecordMetricForPreExecutionValidationFailure(t *testing.T) { // This test mutates the global metrics meter.
	meter, err := metrics.New(metrics.Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)
	restore := metrics.SetProcessMeter(meter)
	t.Cleanup(restore)

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{}, &testutil.FakeClock{})
	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
	})
	require.ErrorIs(t, err, ErrNilPipeline)
	require.Nil(t, runResult)

	body := scrapeRunnerMetrics(t, meter)
	require.NotContains(t, body, `error_type="pipeline_required"`)
}

func TestRunner_ReplayDoesNotRecordMetricForPreExecutionValidationFailure(t *testing.T) { // This test mutates the global metrics meter.
	meter, err := metrics.New(metrics.Config{
		Enabled:     true,
		Path:        "/metrics",
		ServiceName: "backend",
		Environment: "test",
	})
	require.NoError(t, err)
	restore := metrics.SetProcessMeter(meter)
	t.Cleanup(restore)

	runner, err := NewRunner(
		adaptTestQueryExecutor(&testutil.FakeQueryExecutor{}),
		&testutil.FakeClock{},
		&fakeRunStore{},
		runnerTestDataSourceLookup{},
		runnerNoopSinkRuntime{},
	)
	require.NoError(t, err)

	runResult, err := runner.Replay(context.Background(), ReplayRequest{
		OrgSlug:       must.Slug("acme"),
		ProjectSlug:   must.Slug("default"),
		SourceRunSlug: "missing-run",
		InputMode:     ReplayInputModePinned,
	})
	require.ErrorIs(t, err, ErrReplayRunNotFound)
	require.Nil(t, runResult)

	body := scrapeRunnerMetrics(t, meter)
	require.NotContains(t, body, `error_type="pipeline_replay_run_not_found"`)
}

func TestPipelineOutcomeTreatsReturnedErrorAsError(t *testing.T) {
	t.Parallel()

	runResult := &pipelinerun.RunResult{
		Metadata: pipelinerun.RunMetadata{
			Status: runStatusSucceeded,
		},
	}

	require.Equal(t, metricOutcomeError, pipelineOutcome(runResult, ErrNilRunStore))
}

func scrapeRunnerMetrics(t *testing.T, meter *metrics.Meter) string {
	t.Helper()

	app := fiber.New()
	meter.RegisterRoutes(app)

	resp, err := app.Test(httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, resp.Body.Close())
	})

	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(bodyBytes)
}

func TestRunner_RunGeneratesUniqueDefaultRunSlug(t *testing.T) {
	t.Parallel()

	clockTimes := []time.Time{
		time.Date(2026, time.March, 13, 10, 0, 1, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 1, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 1, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 1, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 1, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 2, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 2, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 2, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 2, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 2, 125_000_000, time.UTC),
	}
	clockIndex := 0

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"email": "user@example.com"}},
			ColumnNames: []string{"email"},
			RowCount:    1,
		}, 12, nil), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		current := clockTimes[clockIndex]
		clockIndex++
		return current
	}})

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "acme-users-sync",
			Name: "ACME Users Sync",
		},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: "fetch-users",
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: "acme-postgres",
				OperationType:  "sql",
				OperationRef:   "list-active-users",
			},
		}},
	}

	first, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		Pipeline:        pipeline,
	})
	require.NoError(t, err)
	require.Regexp(t, `^acme-users-sync-20260313100001-[a-z0-9]{4}$`, first.Metadata.RunSlug)

	second, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		Pipeline:        pipeline,
	})
	require.NoError(t, err)
	require.Regexp(t, `^acme-users-sync-20260313100002-[a-z0-9]{4}$`, second.Metadata.RunSlug)
	require.NotEqual(t, first.Metadata.RunSlug, second.Metadata.RunSlug)
	require.Less(t, first.Metadata.RunSlug, second.Metadata.RunSlug)
}

func TestRunner_RunPersistsRun(t *testing.T) {
	t.Parallel()

	clockTimes := []time.Time{
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
	}
	clockIndex := 0

	var persistedPipeline *pipelinespec.Pipeline
	var persistedResult *pipelinerun.RunResult
	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			return result.NewSQLResult(
				"postgres",
				&result.SQLResult{
					Rows:     []map[string]any{{"email": "user@example.com"}},
					RowCount: 1,
				},
				12,
				nil,
			), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			current := clockTimes[clockIndex]
			clockIndex++
			return current
		}},
		&fakeRunStore{persistRunFn: func(
			_ context.Context,
			_ slug.Slug,
			_ slug.Slug,
			_ slug.Slug,
			pipeline *pipelinespec.Pipeline,
			runResult *pipelinerun.RunResult,
		) error {
			persistedPipeline = pipeline
			persistedResult = runResult
			return nil
		}},
		runnerTestDataSourceLookup{},
	)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "acme-users-sync",
			Name: "ACME Users Sync",
		},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: "fetch-users",
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: "acme-postgres",
				OperationType:  "sql",
				OperationRef:   "list-active-users",
				Parameters:     map[string]any{"limit": 100},
			},
		}},
	}

	runResult, err := runner.Run(
		context.Background(),
		RunRequest{OrgSlug: must.Slug("acme"), EnvironmentSlug: must.Slug("prod"), RunSlug: "run-001", Pipeline: pipeline},
	)
	require.NoError(t, err)
	require.NotNil(t, persistedPipeline)
	require.NotNil(t, persistedResult)
	require.Equal(t, pipeline, persistedPipeline)
	require.Equal(t, runResult.Metadata, persistedResult.Metadata)
	require.Nil(t, persistedResult.Metadata.ReplayOfRunSlug)
	require.Nil(t, persistedResult.Metadata.ReplayFromStep)
}

func TestRunner_RunResolvesRuntimeInputWithoutPersistingValuesInCheckpoint(t *testing.T) {
	t.Parallel()

	store := &fakeRunStore{}
	executor := &testutil.FakeQueryExecutor{ExecuteFn: func(
		_ context.Context,
		_ slug.Slug,
		_ slug.Slug,
		_ slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		require.Equal(t, map[string]any{
			"archive_path": "$steps.archive/users",
			"customer_id":  "cus_123",
		}, paramValues)

		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"email": "user@example.com"}},
			ColumnNames: []string{"email"},
			RowCount:    1,
		}, 12, nil), nil
	}}
	runner, err := NewRunner(
		adaptTestQueryExecutor(executor),
		clock.New(),
		store,
		runnerTestDataSourceLookup{},
		runnerNoopSinkRuntime{},
	)
	require.NoError(t, err)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "acme-users-sync",
			Name: "ACME Users Sync",
		},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: "fetch-users",
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: "acme-postgres",
				OperationType:  "sql",
				OperationRef:   "list-active-users",
				Parameters: map[string]any{
					"archive_path": "{{runtime.input.archive_path}}",
					"customer_id":  "{{runtime.input.customer_id}}",
				},
			},
		}},
	}
	runResult, err := runner.Run(context.Background(), RunRequest{
		EnvironmentSlug: must.Slug("prod"),
		OrgSlug:         must.Slug("acme"),
		ProjectSlug:     must.Slug("default"),
		RunSlug:         "run-001",
		Source: pipelinerun.RunSourceSummary{
			Type:          model.PipelineRunSourceTypeManual,
			Summary:       "Manual run",
			Entrypoint:    model.PipelineRunSourceEntrypointUI,
			ActorType:     model.EventActorTypeUser,
			ParameterKeys: []string{"input.archive_path", "input.customer_id"},
		},
		PipelineVersionID: 42,
		RuntimeInput: map[string]any{
			"archive_path": "$steps.archive/users",
			"customer_id":  "cus_123",
		},
		Pipeline: pipeline,
	})
	require.NoError(t, err)
	require.NotNil(t, runResult)
	require.Equal(t, []string{"archive_path", "customer_id"}, runResult.Timeline.Snapshots[0].Input.ParameterKeys)
	require.Equal(t, "{{runtime.input.archive_path}}", pipeline.Steps[0].Query.Parameters["archive_path"])
	require.Equal(t, "{{runtime.input.customer_id}}", pipeline.Steps[0].Query.Parameters["customer_id"])

	checkpoint, ok := store.checkpoints["run-001"]
	require.True(t, ok)
	require.Equal(t, "{{runtime.input.archive_path}}", checkpoint.Pipeline.Steps[0].Query.Parameters["archive_path"])
	require.Equal(t, "{{runtime.input.customer_id}}", checkpoint.Pipeline.Steps[0].Query.Parameters["customer_id"])
	checkpointPipelineJSON, err := json.Marshal(checkpoint.Pipeline)
	require.NoError(t, err)
	require.NotContains(t, string(checkpointPipelineJSON), "$steps.archive/users")
	require.NotContains(t, string(checkpointPipelineJSON), "cus_123")
}

func TestRunner_RunResolvesEnvironmentSecretsWithoutPersistingValues(t *testing.T) {
	t.Parallel()

	const secretValue = "ghp_super_secret"

	store := &fakeRunStore{}
	var persistedPipeline *pipelinespec.Pipeline
	var persistedResult *pipelinerun.RunResult
	store.persistRunFn = func(
		_ context.Context,
		_ slug.Slug,
		_ slug.Slug,
		_ slug.Slug,
		pipeline *pipelinespec.Pipeline,
		runResult *pipelinerun.RunResult,
	) error {
		persistedPipeline = pipeline
		persistedResult = runResult
		if runResult != nil {
			store.timelines = map[string]pipelinerun.RunTimeline{runResult.Metadata.RunSlug: runResult.Timeline}
		}
		return nil
	}
	executor := &testutil.FakeQueryExecutor{ExecuteFn: func(
		_ context.Context,
		_ slug.Slug,
		_ slug.Slug,
		_ slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		require.Equal(t, map[string]any{"token": secretValue}, paramValues)
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"email": "user@example.com", "token_echo": secretValue}},
			ColumnNames: []string{"email", "token_echo"},
			RowCount:    1,
		}, 12, nil), nil
	}}
	runner, err := NewRunner(
		adaptTestQueryExecutor(executor),
		clock.New(),
		store,
		runnerTestDataSourceLookup{},
		runnerNoopSinkRuntime{},
		WithEnvironmentSecretResolver(fakeEnvironmentSecretResolver{
			resolveFn: func(_ context.Context, orgSlug, environmentSlug slug.Slug) ([]model.RuntimeSecretValue, error) {
				require.Equal(t, must.Slug("acme"), orgSlug)
				require.Equal(t, must.Slug("prod"), environmentSlug)
				return []model.RuntimeSecretValue{{Key: "API_TOKEN", Value: secretValue}}, nil
			},
		}),
	)
	require.NoError(t, err)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "acme-users-sync",
			Name: "ACME Users Sync",
		},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: "fetch-users",
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: "acme-postgres",
				OperationType:  "sql",
				OperationRef:   "list-active-users",
				Parameters: map[string]any{
					"token": "{{runtime.secrets.API_TOKEN}}",
				},
			},
		}},
	}
	runResult, err := runner.Run(context.Background(), RunRequest{
		EnvironmentSlug: must.Slug("prod"),
		OrgSlug:         must.Slug("acme"),
		ProjectSlug:     must.Slug("default"),
		RunSlug:         "run-001",
		Pipeline:        pipeline,
	})

	require.NoError(t, err)
	require.NotNil(t, runResult)
	require.NotNil(t, persistedPipeline)
	require.NotNil(t, persistedResult)
	require.Equal(t, "{{runtime.secrets.API_TOKEN}}", pipeline.Steps[0].Query.Parameters["token"])
	require.Equal(t, "{{runtime.secrets.API_TOKEN}}", persistedPipeline.Steps[0].Query.Parameters["token"])
	stepResult, ok := runResult.StepResults[must.Slug("fetch-users")]
	require.True(t, ok)
	sqlResult, ok := stepResult.SQL()
	require.True(t, ok)
	require.Equal(t, "[REDACTED]", sqlResult.Rows[0]["token_echo"])

	checkpoint, ok := store.checkpoints["run-001"]
	require.True(t, ok)
	require.Equal(t, "{{runtime.secrets.API_TOKEN}}", checkpoint.Pipeline.Steps[0].Query.Parameters["token"])
	persistedPipelineJSON, err := json.Marshal(persistedPipeline)
	require.NoError(t, err)
	checkpointPipelineJSON, err := json.Marshal(checkpoint.Pipeline)
	require.NoError(t, err)
	runResultJSON, err := json.Marshal(runResult)
	require.NoError(t, err)
	persistedResultJSON, err := json.Marshal(persistedResult)
	require.NoError(t, err)
	checkpointResultsJSON, err := json.Marshal(checkpoint.Results)
	require.NoError(t, err)
	require.NotContains(t, string(persistedPipelineJSON), secretValue)
	require.NotContains(t, string(checkpointPipelineJSON), secretValue)
	require.NotContains(t, string(runResultJSON), secretValue)
	require.NotContains(t, string(persistedResultJSON), secretValue)
	require.NotContains(t, string(checkpointResultsJSON), secretValue)
	require.Contains(t, string(checkpointResultsJSON), "[REDACTED]")
}

func TestRunner_RunRedactsEnvironmentSecretValuesFromFailures(t *testing.T) {
	t.Parallel()

	const secretValue = "ghp_super_secret"

	var persistedResult *pipelinerun.RunResult
	store := &fakeRunStore{persistRunFn: func(
		_ context.Context,
		_ slug.Slug,
		_ slug.Slug,
		_ slug.Slug,
		_ *pipelinespec.Pipeline,
		runResult *pipelinerun.RunResult,
	) error {
		persistedResult = runResult
		return nil
	}}
	executor := &testutil.FakeQueryExecutor{ExecuteFn: func(
		_ context.Context,
		_ slug.Slug,
		_ slug.Slug,
		_ slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		require.Equal(t, secretValue, paramValues["token"])
		return nil, xerror.Unavailable(xerror.DataSource).
			Detailf("provider rejected bearer %s", secretValue).
			WithPublicDetail("provider rejected bearer " + secretValue)
	}}
	runner, err := NewRunner(
		adaptTestQueryExecutor(executor),
		clock.New(),
		store,
		runnerTestDataSourceLookup{},
		runnerNoopSinkRuntime{},
		WithEnvironmentSecretResolver(fakeEnvironmentSecretResolver{
			resolveFn: func(context.Context, slug.Slug, slug.Slug) ([]model.RuntimeSecretValue, error) {
				return []model.RuntimeSecretValue{{Key: "API_TOKEN", Value: secretValue}}, nil
			},
		}),
	)
	require.NoError(t, err)

	runResult, err := runner.Run(context.Background(), RunRequest{
		EnvironmentSlug: must.Slug("prod"),
		OrgSlug:         must.Slug("acme"),
		ProjectSlug:     must.Slug("default"),
		RunSlug:         "run-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "acme-users-sync",
				Name: "ACME Users Sync",
			},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-active-users",
					Parameters: map[string]any{
						"token": "{{runtime.secrets.API_TOKEN}}",
					},
				},
			}},
		},
	})

	require.Error(t, err)
	require.NotContains(t, err.Error(), secretValue)
	require.Contains(t, err.Error(), "[REDACTED]")
	require.NotNil(t, runResult)
	require.NotNil(t, persistedResult)
	require.NotNil(t, runResult.Timeline.Failure)
	require.NotContains(t, runResult.Timeline.Failure.Error, secretValue)
	require.Contains(t, runResult.Timeline.Failure.Error, "[REDACTED]")
	persistedResultJSON, err := json.Marshal(persistedResult)
	require.NoError(t, err)
	require.NotContains(t, string(persistedResultJSON), secretValue)
	require.Contains(t, string(persistedResultJSON), "[REDACTED]")
}

func TestRunner_RunFailsClosedWhenEnvironmentSecretResolverMissing(t *testing.T) {
	t.Parallel()

	executor := &testutil.FakeQueryExecutor{ExecuteFn: func(
		context.Context,
		slug.Slug,
		slug.Slug,
		slug.Slug,
		map[string]any,
	) (*result.ExecutionResult, error) {
		return nil, fmt.Errorf("unexpected query execution")
	}}
	runner, err := NewRunner(
		adaptTestQueryExecutor(executor),
		clock.New(),
		NewNoopRunStore(),
		runnerTestDataSourceLookup{},
		runnerNoopSinkRuntime{},
	)
	require.NoError(t, err)

	runResult, err := runner.Run(context.Background(), RunRequest{
		EnvironmentSlug: must.Slug("prod"),
		OrgSlug:         must.Slug("acme"),
		ProjectSlug:     must.Slug("default"),
		RunSlug:         "run-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "acme-users-sync",
				Name: "ACME Users Sync",
			},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-active-users",
					Parameters: map[string]any{
						"token": "{{runtime.secrets.API_TOKEN}}",
					},
				},
			}},
		},
	})

	require.Nil(t, runResult)
	require.ErrorContains(t, err, "resolve environment secrets")
	require.ErrorContains(t, err, "environment secret resolver is required")
}

func TestRunner_RunCheckpointUsesLastCompletedProducerAfterSinkStep(t *testing.T) {
	t.Parallel()

	store := &fakeRunStore{}
	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			_ context.Context,
			_, _, querySlug slug.Slug,
			_ map[string]any,
		) (*result.ExecutionResult, error) {
			switch querySlug {
			case must.Slug("list-users"):
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 1}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 12, nil), nil
			case must.Slug("list-profiles"):
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"profile_id": 10}},
					ColumnNames: []string{"profile_id"},
					RowCount:    1,
				}, 12, nil), nil
			default:
				t.Fatalf("unexpected query slug %q", querySlug)
				return nil, assert.AnError
			}
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 24, 10, 0, 0, 0, time.UTC)
		}},
		store,
		newRunnerPreValidationMockService(),
	)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "users-sync", Name: "Users Sync"},
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
				Slug: "archive-users",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
					DataSourceSlug:   "acme-ftp",
					OperationType:    "ftp",
					OperationRef:     "upload-users",
					InputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: true,
					}}},
				},
			},
			{
				Kind: "query",
				Slug: "fetch-profiles",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-profiles",
					Parameters:     map[string]any{"user_rows": "$steps.fetch-users.rows"},
				},
			},
		},
	}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-with-middle-sink",
		Pipeline:        pipeline,
	})

	require.NoError(t, err)
	require.Contains(t, runResult.StepResults, must.Slug("fetch-users"))
	require.Contains(t, runResult.StepResults, must.Slug("fetch-profiles"))
	checkpoint, ok := store.checkpoints["run-with-middle-sink"]
	require.True(t, ok)
	require.Equal(t, must.Slug("fetch-profiles"), checkpoint.StepSlug)
}

func TestRunner_RunExecutesSinkFromTransformOutput(t *testing.T) {
	t.Parallel()

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
			}, 12, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 19, 10, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	sinkCalls := 0
	runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(
		_ context.Context,
		input *SinkRuntimeInput,
	) error {
		sinkCalls++
		require.Equal(t, must.Slug("acme"), input.OrgSlug)
		require.Equal(t, must.Slug("users-sync"), input.PipelineSlug)
		require.Equal(t, model.TransformOutputKindRows, input.InputKind)
		require.Equal(t, must.Slug("project-users"), input.SourceStep)
		require.NotNil(t, input.SourceResult)
		require.Equal(t, model.SinkBackendFTP, input.SinkBackend)
		require.Equal(t, must.Slug("acme-ftp"), input.SinkDataSourceSlug)
		require.Equal(t, "ftp", input.SinkOperationType)
		require.Equal(t, must.Slug("upload-users"), input.SinkOperationRef)
		require.Equal(
			t,
			map[string]any{"path": "users-out.csv", "overwrite": true},
			input.SinkConfig,
		)

		transformOutput, ok := input.SourceResult.Transform()
		require.True(t, ok)
		require.Equal(t, []map[string]any{{"user_id": 1}}, transformOutput.Rows)
		input.SinkResult = result.NewFTPResult(&result.FileResult{
			Format:     "csv",
			Operation:  "write",
			TargetPath: "exports/users-out.csv",
		}, 14, nil)
		return nil
	}}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-with-sink-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "users-sync",
				Name: "Users Sync",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "list-users",
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
					Kind: "transform",
					Slug: "project-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS user_id FROM input",
						OutputKind: model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "user_id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}}},
					},
				},
				{
					Kind: "sink",
					Slug: "users-sync",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("project-users")},
						DataSourceSlug:   "acme-ftp",
						OperationType:    "ftp",
						OperationRef:     "upload-users",
						Config: map[string]any{
							"path":      "users-out.csv",
							"overwrite": true,
						},
					},
				},
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, sinkCalls)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Len(t, runResult.Timeline.Snapshots, 3)
	sinkSnapshot := runResult.Timeline.Snapshots[2]
	require.Equal(t, must.Slug("users-sync"), sinkSnapshot.Step)
	require.Equal(t, "sink", sinkSnapshot.Kind)
	require.Equal(t, must.Slug("acme-ftp"), sinkSnapshot.Input.DataSourceSlug)
	require.Equal(t, "ftp", sinkSnapshot.Input.OperationType)
	require.Equal(t, must.Slug("upload-users"), sinkSnapshot.Input.OperationRef)
	require.Equal(t, []string{"overwrite", "path"}, sinkSnapshot.Input.ParameterKeys)
	require.NotNil(t, sinkSnapshot.Output)
	require.Equal(t, "FTP", sinkSnapshot.Output.ResultType)
	require.Equal(t, "ftp", sinkSnapshot.Output.VendorType)
	sinkDetails := requireStepOutputDetails(t, sinkSnapshot.Output, pipelinerun.StepOutputDetailKindFileWrite)
	require.NotNil(t, sinkDetails.TargetPath)
	require.Equal(t, "exports/users-out.csv", *sinkDetails.TargetPath)
	require.Len(t, runResult.EdgeDiagnostics, 2)
	require.True(t, runResult.EdgeDiagnostics[0].Compatible)
	require.True(t, runResult.EdgeDiagnostics[1].Compatible)
}

func TestRunner_RunExecutesSinkFromNestedJSONTransformOutput(t *testing.T) {
	t.Parallel()

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			return result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 200,
				Headers:    map[string]string{"Content-Type": "application/json"},
				Body:       json.RawMessage(`[{"id":9007199254740993,"email":"alice@example.com"}]`),
			}, 12, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 19, 10, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	bodies := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		bodies <- string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	connector, err := connectorhttp.New(transportsecurity.WithPlaintext(t.Context()), connectorhttp.Config{BaseURL: server.URL, AllowPrivateHosts: true}, connectorhttp.Credentials{})
	require.NoError(t, err)
	sinkCalls := 0
	runner.sinkRuntime = NewSinkRuntime(&fakeSinkQueryExecutor{executeFn: func(
		ctx context.Context, _, _, _ slug.Slug, _ model.EventActor,
		params map[string]any, _ ...queriessvc.ExecuteOption,
	) (*result.ExecutionResult, error) {
		sinkCalls++
		return connector.Execute(ctx, []byte(`{"method":"POST","path":"/sink","body":"{{payload}}"}`), map[string]connectortypes.TypedParam{
			"payload": {Value: params[sinkbinding.Input], DataType: "json"},
		})
	}})

	jsonContract := &payload.Contract{
		Representation: model.ContractRepresentationJSON,
		RootShape:      model.ContractRootShapeArray,
		Fields: []payload.ContractField{
			{Name: "profile", Type: model.ContractFieldTypeValueObject, Required: true},
			{Name: "profile.contact.email", Type: model.ContractFieldTypeValueString, Required: true},
			{Name: "items", Type: model.ContractFieldTypeValueArray, Required: true},
			{Name: "items[].sku", Type: model.ContractFieldTypeValueString, Required: true},
			{Name: "items[].quantity", Type: model.ContractFieldTypeValueInteger, Required: true},
		},
	}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-with-nested-json-sink-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "users-sync",
				Name: "Users Sync",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-http",
						OperationType:  "http",
						OperationRef:   "list-users",
						OutputContract: &payload.Contract{Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeArray, Fields: []payload.ContractField{
							{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
							{Name: "email", Type: model.ContractFieldTypeValueString, Required: true},
						}},
					},
				},
				{
					Kind: "transform",
					Slug: "shape-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs:         map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}},
						SQL:            "SELECT JSON_OBJECT('contact', JSON_OBJECT('email', email), 'id', id) AS profile, JSON_ARRAY(JSON_OBJECT('sku', email, 'quantity', id)) AS items FROM input",
						OutputKind:     model.TransformOutputKindJSON,
						OutputContract: jsonContract,
					},
				},
				{
					Kind: "sink",
					Slug: "users-sync",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("shape-users")},
						DataSourceSlug:   "acme-http",
						OperationType:    "http",
						OperationRef:     "post-users",
						InputContract:    payload.CloneContract(jsonContract),
					},
				},
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, sinkCalls)
	delivered, _, err := payload.ParseJSONBodyToRowsPreserveNumbers([]byte(<-bodies))
	require.NoError(t, err)
	require.Equal(t, []map[string]any{{
		"profile": map[string]any{"contact": map[string]any{"email": "alice@example.com"}, "id": json.Number("9007199254740993")},
		"items":   []any{map[string]any{"sku": "alice@example.com", "quantity": json.Number("9007199254740993")}},
	}}, delivered)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Len(t, runResult.EdgeDiagnostics, 2)
	require.True(t, runResult.EdgeDiagnostics[0].Compatible)
	require.True(t, runResult.EdgeDiagnostics[1].Compatible)
}

func TestRunner_RunDerivesSinkInputKindFromSinkContractRepresentation(t *testing.T) {
	t.Parallel()

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"payload": map[string]any{"id": 1}}},
				ColumnNames: []string{"payload"},
				RowCount:    1,
			}, 12, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 22, 10, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	sinkCalls := 0
	runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(
		_ context.Context,
		input *SinkRuntimeInput,
	) error {
		sinkCalls++
		require.Equal(t, model.TransformOutputKindJSON, input.InputKind)
		return nil
	}}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-derive-sink-kind-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "users-sync",
				Name: "Users Sync",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-http",
						OperationType:  "http",
						OperationRef:   "list-users",
						OutputContract: &payload.Contract{
							Representation: model.ContractRepresentationJSON,
							RootShape:      model.ContractRootShapeObject,
							Fields: []payload.ContractField{{
								Name:     "payload",
								Type:     model.ContractFieldTypeValueObject,
								Required: true,
							}},
						},
					},
				},
				{
					Kind: "sink",
					Slug: "users-sync",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
						DataSourceSlug:   "acme-postgres",
						OperationType:    "sql",
						OperationRef:     "insert-users",
						InputContract: &payload.Contract{
							Representation: model.ContractRepresentationJSON,
							RootShape:      model.ContractRootShapeObject,
							Fields: []payload.ContractField{{
								Name:     "payload",
								Type:     model.ContractFieldTypeValueObject,
								Required: true,
							}},
						},
					},
				},
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, sinkCalls)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
}

func TestRunner_RunFailsWhenSinkInputMissing(t *testing.T) {
	t.Parallel()

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
			}, 5, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 22, 11, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	sinkCalls := 0
	runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(
		_ context.Context,
		input *SinkRuntimeInput,
	) error {
		sinkCalls++
		return nil
	}}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-sink-from-query-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "users-sync",
				Name: "Users Sync",
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
					Kind: "sink",
					Slug: "users-sync",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
						DataSourceSlug:   "acme-postgres",
						OperationType:    "sql",
						OperationRef:     "insert-users",
					},
				},
			},
		},
	})

	require.Error(t, err)
	require.ErrorContains(
		t,
		err,
		"sink step \"users-sync\" must reference at least one upstream step output",
	)
	require.Equal(t, 0, sinkCalls)
	require.Nil(t, runResult)
}

func TestResolveSinkUpstreamStepSlug_UsesSinkSpecificError(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Steps: []pipelinespec.Step{
			{Kind: "query", Slug: must.Slug("fetch-users"), Query: &pipelinespec.QueryRef{}},
			{
				Kind: "sink",
				Slug: must.Slug("users-sync"),
				Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: must.Slug("missing")}},
			},
		},
	}

	_, err := resolveSinkUpstreamStepSlug(pipeline, 1)
	require.ErrorIs(t, err, ErrSinkUpstreamRequired)
}

func TestResolveTransformUpstreamStepSlug_UsesExplicitInputFromStep(t *testing.T) {
	t.Parallel()

	pipeline := &pipelinespec.Pipeline{
		Steps: []pipelinespec.Step{
			{Kind: "query", Slug: must.Slug("fetch-users"), Query: &pipelinespec.QueryRef{}},
			{
				Kind:      "transform",
				Slug:      must.Slug("normalize-users"),
				Transform: &pipelinespec.TransformSpec{Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id FROM input"},
			},
		},
	}

	upstream, err := resolveTransformUpstreamStepSlug(pipeline, 1)
	require.NoError(t, err)
	require.Equal(t, must.Slug("fetch-users"), upstream)
}

func TestRunner_RunExecutesTransformFromFTPQuerySource(t *testing.T) {
	t.Parallel()

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, must.Slug("acme-ftp"), dsSlug)
			require.Equal(t, must.Slug("read-users-csv"), querySlug)
			require.Empty(t, paramValues)

			return result.NewFTPResult(&result.FileResult{
				Rows: []map[string]any{
					{"id": "1", "email": "alice@example.com"},
					{"id": "2", "email": "bob@example.com"},
				},
				ColumnNames: []string{"id", "email"},
				RowCount:    2,
				Format:      "csv",
			}, 8, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 22, 12, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-ftp-source-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "users-sync",
				Name: "Users Sync",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users-csv",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-ftp",
						OperationType:  "sql",
						OperationRef:   "read-users-csv",
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "id",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							},
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
					Slug: "project-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users-csv")}}, SQL: "SELECT email AS email FROM input",
						OutputKind: model.TransformOutputKindRows,
					},
				},
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	queryStepResult, ok := runResult.StepResults[must.Slug("fetch-users-csv")]
	require.True(t, ok)
	ftpResult, ok := queryStepResult.FTP()
	require.True(t, ok)
	require.Equal(t, 2, ftpResult.RowCount)

	transformStepResult, ok := runResult.StepResults[must.Slug("project-users")]
	require.True(t, ok)
	transformOutput, ok := transformStepResult.Transform()
	require.True(t, ok)
	require.Equal(t, model.TransformOutputKindRows, transformOutput.OutputKind)
	require.Equal(t, []map[string]any{
		{"email": "alice@example.com"},
		{"email": "bob@example.com"},
	}, transformOutput.Rows)
}

func TestRunner_RunAnnotatesQueryPayloadWithVerifiedContract(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{Fields: []payload.ContractField{
		{
			Name:     "id",
			Type:     model.ContractFieldTypeValueInteger,
			Required: true,
		},
		{
			Name:     "email",
			Type:     model.ContractFieldTypeValueString,
			Required: true,
		},
	}}

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
			}, 5, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 23, 9, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-query-payload-contract-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "users-sync", Name: "Users Sync"},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-users",
					OutputContract: contract,
				},
			}},
		},
	})

	require.NoError(t, err)
	stepResult, ok := runResult.StepResults[must.Slug("fetch-users")]
	require.True(t, ok)
	runtimePayload, ok := stepResult.Payload()
	require.True(t, ok)
	require.NotNil(t, runtimePayload.Contract)
	require.Equal(t, payload.VerificationStatusVerified, runtimePayload.VerificationStatus)
	require.Equal(t, "fetch-users-output", runtimePayload.Contract.Name)
	require.Equal(t, model.ContractRepresentationRows, runtimePayload.Contract.Representation)
	require.Equal(t, model.ContractRootShapeUnknown, runtimePayload.Contract.RootShape)
	require.Equal(t, contract.Fields, runtimePayload.Contract.Fields)
}

func TestRunner_RunAnnotatesQueryPayloadWithStaleIntrospectionContract(t *testing.T) {
	t.Parallel()

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
			}, 5, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 23, 9, 15, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-query-payload-contract-002",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "users-sync", Name: "Users Sync"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "list-users",
					},
				},
				{
					Kind: "transform",
					Slug: "project-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id FROM input",
						InputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}}},
						OutputKind: model.TransformOutputKindRows,
					},
				},
			},
		},
	})

	require.NoError(t, err)
	stepResult, ok := runResult.StepResults[must.Slug("fetch-users")]
	require.True(t, ok)
	runtimePayload, ok := stepResult.Payload()
	require.True(t, ok)
	require.NotNil(t, runtimePayload.Contract)
	require.Equal(t, payload.VerificationStatusStaleIntrospection, runtimePayload.VerificationStatus)
	require.Equal(t, "fetch-users-output", runtimePayload.Contract.Name)
	require.NotEmpty(t, runtimePayload.Contract.Fields)
	_, hasID := stepResult.SQL()
	require.True(t, hasID)
}

func TestRunner_RunAnnotatesTransformPayloadWithVerifiedContract(t *testing.T) {
	t.Parallel()

	contract := &payload.Contract{Fields: []payload.ContractField{{
		Name:     "user_id",
		Type:     model.ContractFieldTypeValueInteger,
		Required: true,
	}}}

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
			}, 5, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 23, 9, 30, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-transform-payload-contract-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "users-sync", Name: "Users Sync"},
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
					Kind: "transform",
					Slug: "project-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS user_id FROM input",
						OutputKind:     model.TransformOutputKindRows,
						OutputContract: contract,
					},
				},
			},
		},
	})

	require.NoError(t, err)
	stepResult, ok := runResult.StepResults[must.Slug("project-users")]
	require.True(t, ok)
	runtimePayload, ok := stepResult.Payload()
	require.True(t, ok)
	require.NotNil(t, runtimePayload.Contract)
	require.Equal(t, payload.VerificationStatusVerified, runtimePayload.VerificationStatus)
	require.Equal(t, "project-users-output", runtimePayload.Contract.Name)
	require.Equal(t, model.ContractRepresentationRows, runtimePayload.Contract.Representation)
	require.Equal(t, model.ContractRootShapeUnknown, runtimePayload.Contract.RootShape)
	require.Equal(t, contract.Fields, runtimePayload.Contract.Fields)
}

func TestRunner_RunAnnotatesTransformPayloadWithTransitivelyVerifiedContract(t *testing.T) {
	t.Parallel()

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
			}, 5, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 23, 9, 45, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-transform-payload-contract-002",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "users-sync", Name: "Users Sync"},
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
					Kind: "transform",
					Slug: "project-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS user_id FROM input",
						OutputKind: model.TransformOutputKindRows,
					},
				},
			},
		},
	})

	require.NoError(t, err)
	stepResult, ok := runResult.StepResults[must.Slug("project-users")]
	require.True(t, ok)
	runtimePayload, ok := stepResult.Payload()
	require.True(t, ok)
	require.NotNil(t, runtimePayload.Contract)
	require.Equal(
		t,
		payload.VerificationStatusTransitivelyVerified,
		runtimePayload.VerificationStatus,
	)
	require.Equal(t, "project-users-output", runtimePayload.Contract.Name)
	require.Equal(t, []payload.ContractField{{
		Name: "user_id",
		Type: model.ContractFieldTypeValueInteger,
	}}, runtimePayload.Contract.Fields)
}

func TestRunner_RunExecutesTransformFromFTPHeadlessCSVIndexPath(t *testing.T) {
	t.Parallel()

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, must.Slug("acme-ftp"), dsSlug)
			require.Equal(t, must.Slug("read-users-csv"), querySlug)
			require.Empty(t, paramValues)

			return result.NewFTPResult(&result.FileResult{
				Rows: []map[string]any{
					{"csv_row": []any{"alice", 31}},
					{"csv_row": []any{"bob", 29}},
				},
				ColumnNames:   []string{"csv_row"},
				RowCount:      2,
				Format:        "csv",
				IsHeadlessCSV: true,
			}, 8, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 22, 12, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-ftp-headless-csv-index-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "users-sync",
				Name: "Users Sync",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users-csv",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-ftp",
						OperationType:  "sql",
						OperationRef:   "read-users-csv",
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "csv_row",
							Type:     model.ContractFieldTypeValueArray,
							Required: true,
						}}},
					},
				},
				{
					Kind: "transform",
					Slug: "project-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users-csv")}}, SQL: "SELECT json_extract(csv_row, '$[0]') AS first_col FROM input",
						OutputKind: model.TransformOutputKindRows,
					},
				},
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	transformStepResult, ok := runResult.StepResults[must.Slug("project-users")]
	require.True(t, ok)
	transformOutput, ok := transformStepResult.Transform()
	require.True(t, ok)
	require.Equal(t, []map[string]any{
		{"first_col": "alice"},
		{"first_col": "bob"},
	}, transformOutput.Rows)
}

func TestRunner_RunFailsTransformFromFTPHeadlessCSVNamedPath(t *testing.T) {
	t.Parallel()

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, must.Slug("acme-ftp"), dsSlug)
			require.Equal(t, must.Slug("read-users-csv"), querySlug)
			require.Empty(t, paramValues)

			return result.NewFTPResult(&result.FileResult{
				Rows: []map[string]any{
					{"csv_row": []any{"alice", 31}},
				},
				ColumnNames:   []string{"csv_row"},
				RowCount:      1,
				Format:        "csv",
				IsHeadlessCSV: true,
			}, 8, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 22, 12, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-ftp-headless-csv-named-path-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "users-sync",
				Name: "Users Sync",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users-csv",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-ftp",
						OperationType:  "sql",
						OperationRef:   "read-users-csv",
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "csv_row",
							Type:     model.ContractFieldTypeValueArray,
							Required: true,
						}}},
					},
				},
				{
					Kind: "transform",
					Slug: "project-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users-csv")}}, SQL: "SELECT json_extract(csv_row, '$.name') AS name FROM input",
						OutputKind: model.TransformOutputKindRows,
					},
				},
			},
		},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "headless CSV/array value")
	require.ErrorIs(t, err, transformsql.ErrRuntimeHeadlessCSVNamedAccessUnsupported)
	require.NotNil(t, runResult)
	require.Equal(t, "failed", runResult.Metadata.Status)
	require.NotNil(t, runResult.Metadata.FailedStep)
	require.Equal(t, must.Slug("project-users"), *runResult.Metadata.FailedStep)
}

func TestRunner_RunPreflightEdgeCompatibility(t *testing.T) {
	t.Parallel()

	t.Run("WatermarkEffectiveContractFailsBeforeAnyStepStarts", func(t *testing.T) {
		t.Parallel()

		queryCalls := 0
		sinkCalls := 0
		runner := mustNewRunnerWithStore(
			&testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				queryCalls++
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 1}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 12, nil), nil
			}},
			&testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.March, 21, 9, 0, 0, 0, time.UTC)
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

		checkpointKind := model.IncrementalCheckpointKindWatermark
		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			ProjectSlug:     must.Slug("default"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-preflight-watermark-001",
			Pipeline: &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{Slug: "users-sync", Name: "Users Sync"},
				Steps: []pipelinespec.Step{
					{
						Kind: "query",
						Slug: "fetch-users",
						Query: &pipelinespec.QueryRef{
							DataSourceSlug: "acme-postgres",
							OperationType:  "sql",
							OperationRef:   "list-users",
							Incremental: &pipelinespec.IncrementalSourceContract{
								CheckpointKind: &checkpointKind,
								Field:          "updated_at",
							},
						},
					},
					{
						Kind: "sink",
						Slug: "users-sync",
						Sink: &pipelinespec.SinkSpec{
							IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
							Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
							DataSourceSlug:   "acme-postgres",
							OperationType:    "sql",
							OperationRef:     "insert-users",
							InputContract: &payload.Contract{
								Fields: []payload.ContractField{{
									Name:     "id",
									Type:     model.ContractFieldTypeValueInteger,
									Required: true,
								}},
							},
						},
					},
				},
			},
		})

		require.Error(t, err)
		require.ErrorContains(t, err, `watermark incremental field "updated_at"`)
		require.NotNil(t, runResult)
		require.Equal(t, 0, queryCalls)
		require.Equal(t, 0, sinkCalls)
		require.Equal(t, "failed", runResult.Metadata.Status)
		require.Len(t, runResult.EdgeDiagnostics, 2)
		require.True(t, runResult.EdgeDiagnostics[0].Compatible)
		require.False(t, runResult.EdgeDiagnostics[1].Compatible)
		require.Equal(t, pipelinevalidate.ContractReasonMissingRequiredField, runResult.EdgeDiagnostics[1].ReasonCode)
		require.Equal(t, must.Slug("fetch-users"), runResult.EdgeDiagnostics[1].FromStepSlug)
		require.Equal(t, must.Slug("users-sync"), runResult.EdgeDiagnostics[1].ToStepSlug)
	})

	t.Run("MissingExplicitEdgeContractFailsBeforeSchemaPrevalidation", func(t *testing.T) {
		t.Parallel()

		queryCalls := 0
		sinkCalls := 0
		runner := mustNewRunnerWithStore(
			&testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				queryCalls++
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 1}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 12, nil), nil
			}},
			&testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.March, 21, 9, 30, 0, 0, time.UTC)
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

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "users-sync", Name: "Users Sync"},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "list-users",
					},
				},
				{
					Kind: "transform",
					Slug: "project-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs:     map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}},
						SQL:        "SELECT id FROM input",
						OutputKind: model.TransformOutputKindRows,
						OutputContract: &payload.Contract{
							Representation: model.ContractRepresentationRows,
							Fields: []payload.ContractField{{
								Name:     "id",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							}},
						},
					},
				},
				{
					Kind: "sink",
					Slug: "users-sync",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("project-users")},
						DataSourceSlug:   "acme-postgres",
						OperationType:    "sql",
						OperationRef:     "insert-users",
						InputContract: &payload.Contract{
							Representation: model.ContractRepresentationRows,
							Fields: []payload.ContractField{{
								Name:     "id",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							}},
						},
					},
				},
			},
		}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			ProjectSlug:     must.Slug("default"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-preflight-missing-contract-001",
			Pipeline:        pipeline,
		})

		require.ErrorIs(t, err, pipelinerun.ErrContractMismatch)
		require.ErrorContains(t, err, "fetch-users->project-users")
		require.ErrorContains(t, err, "missing_contract")
		require.NotNil(t, runResult)
		require.Nil(t, pipeline.Steps[0].Query.OutputContract)
		require.Equal(t, 0, queryCalls)
		require.Equal(t, 0, sinkCalls)
		require.Equal(t, "failed", runResult.Metadata.Status)
		require.Empty(t, runResult.StepResults)
		require.Len(t, runResult.EdgeDiagnostics, 1)
		require.False(t, runResult.EdgeDiagnostics[0].Compatible)
		require.Equal(t, pipelinevalidate.ContractReasonMissingContract, runResult.EdgeDiagnostics[0].ReasonCode)
	})

	t.Run("StrictModeFailsBeforeAnyStepStarts", func(t *testing.T) {
		t.Parallel()

		queryCalls := 0
		sinkCalls := 0
		runner := mustNewRunnerWithStore(
			&testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				queryCalls++
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 1}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 12, nil), nil
			}},
			&testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.March, 21, 10, 0, 0, 0, time.UTC)
			}},
			NewNoopRunStore(),
			preValidationMockService{schemas: map[slug.Slug]datasourcessvc.SchemaResult{
				must.Slug("acme-postgres"): datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
					SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
					Type:            "postgres",
					Payload: datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{
						{
							Name:    "users",
							Columns: []datasourcessvc.SchemaColumn{{Name: "id", Type: "integer"}},
						},
						{
							Name: "users",
							Columns: []datasourcessvc.SchemaColumn{
								{Name: "id", Type: "integer"},
								{Name: "email", Type: "varchar"},
							},
						},
					}},
				},
			}},
		)

		runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(
			_ context.Context,
			_ *SinkRuntimeInput,
		) error {
			sinkCalls++
			return nil
		}}

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-preflight-strict-001",
			Pipeline: &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{
					Slug: "users-sync",
					Name: "Users Sync",
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
						Kind: "transform",
						Slug: "project-users",
						Transform: &pipelinespec.TransformSpec{
							Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS user_id FROM input",
							OutputKind: model.TransformOutputKindRows,
							OutputContract: &payload.Contract{Fields: []payload.ContractField{{
								Name:     "user_id",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							}}},
						},
					},
					{
						Kind: "sink",
						Slug: "users-sync",
						Sink: &pipelinespec.SinkSpec{
							IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
							Input:            &pipelinespec.StepInput{FromStep: must.Slug("project-users")},
							DataSourceSlug:   "acme-postgres",
							OperationType:    "sql",
							OperationRef:     "insert-users",
							InputContract: &payload.Contract{
								Representation: model.ContractRepresentationRows,
								Fields: []payload.ContractField{{
									Name:     "user_id",
									Type:     model.ContractFieldTypeValueString,
									Required: true,
								}},
							},
						},
					},
				},
			},
		})

		require.ErrorIs(t, err, pipelinerun.ErrContractMismatch)
		require.NotNil(t, runResult)
		require.Equal(t, 0, queryCalls)
		require.Equal(t, 0, sinkCalls)
		require.Equal(t, "failed", runResult.Metadata.Status)
		require.Nil(t, runResult.Metadata.FailedStep)
		require.Len(t, runResult.Timeline.Events, 2)
		require.Equal(t, "run_started", runResult.Timeline.Events[0].Type)
		require.Equal(t, "run_completed", runResult.Timeline.Events[1].Type)
		require.Empty(t, runResult.StepResults)
		require.Len(t, runResult.EdgeDiagnostics, 2)
		require.True(t, runResult.EdgeDiagnostics[0].Compatible)
		require.False(t, runResult.EdgeDiagnostics[1].Compatible)
		require.Equal(t, pipelinevalidate.ContractReasonTypeMismatch, runResult.EdgeDiagnostics[1].ReasonCode)
		require.NotNil(t, runResult.Timeline.Failure)
		require.Equal(t, must.Slug("users-sync"), runResult.Timeline.Failure.Step)
		require.Equal(t, failureKindPlatform, runResult.Timeline.Failure.Kind)
		require.Equal(t, failureSideSink, runResult.Timeline.Failure.Side)
	})

	t.Run("ContractMismatchBlocksRunWithoutRunModeOverride", func(t *testing.T) {
		t.Parallel()

		queryCalls := 0
		sinkCalls := 0
		runner := mustNewRunnerWithStore(
			&testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				queryCalls++
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 1}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 12, nil), nil
			}},
			&testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.March, 21, 11, 0, 0, 0, time.UTC)
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

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-preflight-001",
			Pipeline: &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{
					Slug: "users-sync",
					Name: "Users Sync",
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
						Kind: "transform",
						Slug: "project-users",
						Transform: &pipelinespec.TransformSpec{
							Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS user_id FROM input",
							OutputKind: model.TransformOutputKindRows,
							OutputContract: &payload.Contract{Fields: []payload.ContractField{{
								Name:     "user_id",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							}}},
						},
					},
					{
						Kind: "sink",
						Slug: "users-sync",
						Sink: &pipelinespec.SinkSpec{
							IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
							Input:            &pipelinespec.StepInput{FromStep: must.Slug("project-users")},
							DataSourceSlug:   "acme-postgres",
							OperationType:    "sql",
							OperationRef:     "insert-users",
							InputContract: &payload.Contract{Fields: []payload.ContractField{{
								Name:     "user_id",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							}}},
						},
					},
				},
			},
		})

		require.ErrorIs(t, err, pipelinerun.ErrContractMismatch)
		require.NotNil(t, runResult)
		require.Equal(t, 0, queryCalls)
		require.Equal(t, 0, sinkCalls)
		require.Equal(t, "failed", runResult.Metadata.Status)
		require.Len(t, runResult.EdgeDiagnostics, 2)
		require.True(t, runResult.EdgeDiagnostics[0].Compatible)
		require.False(t, runResult.EdgeDiagnostics[1].Compatible)
		require.Equal(t, pipelinevalidate.ContractReasonTypeMismatch, runResult.EdgeDiagnostics[1].ReasonCode)
	})

	t.Run("PreflightContractMismatchFailsRun", func(t *testing.T) {
		t.Parallel()

		queryCalls := 0
		sinkCalls := 0
		runner := mustNewRunnerWithStore(
			&testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				queryCalls++
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 1}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 12, nil), nil
			}},
			&testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.March, 21, 12, 0, 0, 0, time.UTC)
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

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-preflight-002",
			Pipeline: &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{
					Slug: "users-sync",
					Name: "Users Sync",
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
						Kind: "transform",
						Slug: "project-users",
						Transform: &pipelinespec.TransformSpec{
							Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS user_id FROM input",
							OutputKind: model.TransformOutputKindRows,
							OutputContract: &payload.Contract{Fields: []payload.ContractField{{
								Name:     "user_id",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							}}},
						},
					},
					{
						Kind: "sink",
						Slug: "users-sync",
						Sink: &pipelinespec.SinkSpec{
							IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
							Input:            &pipelinespec.StepInput{FromStep: must.Slug("project-users")},
							DataSourceSlug:   "acme-postgres",
							OperationType:    "sql",
							OperationRef:     "insert-users",
							InputContract: &payload.Contract{Fields: []payload.ContractField{{
								Name:     "user_id",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							}}},
						},
					},
				},
			},
		})

		require.Error(t, err)
		require.NotNil(t, runResult)
		require.Equal(t, 0, queryCalls)
		require.Equal(t, 0, sinkCalls)
		require.Equal(t, "failed", runResult.Metadata.Status)
		require.Len(t, runResult.EdgeDiagnostics, 2)
		require.True(t, runResult.EdgeDiagnostics[0].Compatible)
		require.False(t, runResult.EdgeDiagnostics[1].Compatible)
		require.Equal(t, pipelinevalidate.ContractReasonTypeMismatch, runResult.EdgeDiagnostics[1].ReasonCode)
	})

	t.Run("CompatibleContractsRunSucceeds", func(t *testing.T) {
		t.Parallel()

		queryCalls := 0
		sinkCalls := 0
		runner := mustNewRunnerWithStore(
			&testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				queryCalls++
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 1}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 12, nil), nil
			}},
			&testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.March, 21, 13, 0, 0, 0, time.UTC)
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

		runResult, err := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "run-preflight-003",
			Pipeline: &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{
					Slug: "users-sync",
					Name: "Users Sync",
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
						Kind: "transform",
						Slug: "project-users",
						Transform: &pipelinespec.TransformSpec{
							Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS user_id FROM input",
							OutputKind: model.TransformOutputKindRows,
							OutputContract: &payload.Contract{Fields: []payload.ContractField{{
								Name:     "user_id",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							}}},
						},
					},
					{
						Kind: "sink",
						Slug: "users-sync",
						Sink: &pipelinespec.SinkSpec{
							IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
							Input:            &pipelinespec.StepInput{FromStep: must.Slug("project-users")},
							InputContract: &payload.Contract{Fields: []payload.ContractField{{
								Name:     "user_id",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							}}},
							DataSourceSlug: "acme-ftp",
							OperationType:  "ftp",
							OperationRef:   "upload-users",
							Config:         map[string]any{"path": "users-out.csv"},
						},
					},
				},
			},
		})

		require.NoError(t, err)
		require.NotNil(t, runResult)
		require.Equal(t, 1, queryCalls)
		require.Equal(t, 1, sinkCalls)
		require.Equal(t, "succeeded", runResult.Metadata.Status)
		require.Len(t, runResult.EdgeDiagnostics, 2)
		require.True(t, runResult.EdgeDiagnostics[0].Compatible)
		require.True(t, runResult.EdgeDiagnostics[1].Compatible)
		require.Empty(t, runResult.EdgeDiagnostics[1].ReasonCode)
	})
}

func TestRunner_RunFailsWhenSinkExecutionFails(t *testing.T) {
	t.Parallel()

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
			}, 12, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 19, 11, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	runner.sinkRuntime = &fakeSinkRuntime{
		executeFn: func(_ context.Context, input *SinkRuntimeInput) error {
			input.SinkResult = result.NewFTPResult(&result.FileResult{
				Format:     "csv",
				Operation:  "write",
				TargetPath: "exports/users-partial.csv",
			}, 17, nil)
			return xerror.ExecutionResponseSizeExceeded().Detail("response size safety limit exceeded")
		},
	}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-with-sink-failure-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "users-sync",
				Name: "Users Sync",
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
					Kind: "sink",
					Slug: "users-sync",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
						DataSourceSlug:   must.Slug("acme-ftp"),
						OperationType:    "ftp",
						OperationRef:     must.Slug("upload-users"),
					},
				},
			},
		},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "execute step \"users-sync\"")
	require.NotNil(t, runResult)
	require.Equal(t, "failed", runResult.Metadata.Status)
	require.NotNil(t, runResult.Metadata.FailedStep)
	require.Equal(t, must.Slug("users-sync"), *runResult.Metadata.FailedStep)
	require.NotNil(t, runResult.Timeline.Failure)
	require.Equal(t, failureKindDataSource, runResult.Timeline.Failure.Kind)
	require.Equal(t, failureSideSink, runResult.Timeline.Failure.Side)
	require.Equal(t, "acme-ftp", runResult.Timeline.Failure.DataSourceSlug)
	require.Contains(t, runResult.Timeline.Failure.Error, "response size")
	require.True(t, runResult.Timeline.Failure.AutomaticRetryProhibited)
	require.Contains(t, runResult.Timeline.Failure.ActionHint, "response size limit")
	require.Len(t, runResult.Timeline.Snapshots, 2)
	sinkSnapshot := runResult.Timeline.Snapshots[1]
	require.Equal(t, must.Slug("users-sync"), sinkSnapshot.Step)
	require.NotNil(t, sinkSnapshot.Output)
	require.Equal(t, "FTP", sinkSnapshot.Output.ResultType)
	sinkDetails := requireStepOutputDetails(t, sinkSnapshot.Output, pipelinerun.StepOutputDetailKindFileWrite)
	require.NotNil(t, sinkDetails.TargetPath)
	require.Equal(t, "exports/users-partial.csv", *sinkDetails.TargetPath)
}

func TestRunner_RunUsesSavedSinkQueryExpectedInputContract(t *testing.T) {
	t.Parallel()

	contractCalls := 0
	sinkCalls := 0
	runner, err := NewRunner(
		newTestQueryExecutorAdapter(
			&testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 1}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 12, nil), nil
			}},
			func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug) (*payload.Contract, error) {
				contractCalls++
				return &payload.Contract{Fields: []payload.ContractField{{
					Name:     "user_id",
					Type:     model.ContractFieldTypeValueInteger,
					Required: true,
				}}}, nil
			},
		),
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 22, 10, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
		runnerNoopSinkRuntime{},
	)
	require.NoError(t, err)

	runner.sinkRuntime = &fakeSinkRuntime{
		executeFn: func(_ context.Context, _ *SinkRuntimeInput) error {
			sinkCalls++
			return nil
		},
	}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-saved-sink-contract-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "users-sync", Name: "Users Sync"},
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
					Slug: "users-sync",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
						DataSourceSlug:   "acme-ftp",
						OperationType:    "ftp",
						OperationRef:     "upload-users",
						Config:           map[string]any{"path": "users.csv"},
					},
				},
			},
		},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, `required field "user_id" is missing`)
	require.NotNil(t, runResult)
	assert.Equal(t, 1, contractCalls)
	assert.Equal(t, 0, sinkCalls)
	assert.Equal(t, "failed", runResult.Metadata.Status)
}

func TestRunner_RunPrefersExplicitSinkInputContractOverSavedQueryContract(t *testing.T) {
	t.Parallel()

	contractCalls := 0
	sinkCalls := 0
	runner, err := NewRunner(
		newTestQueryExecutorAdapter(
			&testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 1}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 12, nil), nil
			}},
			func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug) (*payload.Contract, error) {
				contractCalls++
				return &payload.Contract{Fields: []payload.ContractField{{
					Name:     "user_id",
					Type:     model.ContractFieldTypeValueInteger,
					Required: true,
				}}}, nil
			},
		),
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 22, 11, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
		runnerNoopSinkRuntime{},
	)
	require.NoError(t, err)

	runner.sinkRuntime = &fakeSinkRuntime{
		executeFn: func(_ context.Context, _ *SinkRuntimeInput) error {
			sinkCalls++
			return nil
		},
	}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-saved-sink-contract-override-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "users-sync", Name: "Users Sync"},
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
					Slug: "users-sync",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
						InputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueInteger,
							Required: true,
						}}},
						DataSourceSlug: "acme-ftp",
						OperationType:  "ftp",
						OperationRef:   "upload-users",
						Config:         map[string]any{"path": "users.csv"},
					},
				},
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, runResult)
	assert.Equal(t, 0, contractCalls)
	assert.Equal(t, 1, sinkCalls)
	assert.Equal(t, "succeeded", runResult.Metadata.Status)
}

func TestNewRunner_RequiresDependencies(t *testing.T) {
	t.Parallel()

	t.Run("ReturnsErrNilRunStoreWhenRunStoreIsNil", func(t *testing.T) {
		t.Parallel()

		_, err := NewRunner(
			adaptTestQueryExecutor(&testutil.FakeQueryExecutor{}),
			&testutil.FakeClock{},
			nil,
			runnerTestDataSourceLookup{},
			runnerNoopSinkRuntime{},
		)
		require.ErrorIs(t, err, ErrNilRunStore)
	})

	t.Run("ReturnsErrNilDataSourceServiceWhenDataSourceServiceIsNil", func(t *testing.T) {
		t.Parallel()

		_, err := NewRunner(
			adaptTestQueryExecutor(&testutil.FakeQueryExecutor{}),
			&testutil.FakeClock{},
			NewNoopRunStore(),
			nil,
			runnerNoopSinkRuntime{},
		)
		require.ErrorIs(t, err, ErrNilDataSourceService)
	})

	t.Run("ReturnsErrNilSinkRuntimeWhenSinkRuntimeIsNil", func(t *testing.T) {
		t.Parallel()

		_, err := NewRunner(
			adaptTestQueryExecutor(&testutil.FakeQueryExecutor{}),
			&testutil.FakeClock{},
			NewNoopRunStore(),
			runnerTestDataSourceLookup{},
			nil,
		)
		require.ErrorIs(t, err, ErrNilSinkRuntime)
	})
}

func TestRunner_RunErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		runner          *Runner
		request         RunRequest
		wantErr         error
		wantErrContains string
		assertFn        func(t *testing.T, runResult *pipelinerun.RunResult)
	}{
		{
			name:    "RejectsNilPipeline",
			runner:  mustNewRunner(t, &testutil.FakeQueryExecutor{}, &testutil.FakeClock{}),
			request: RunRequest{OrgSlug: must.Slug("acme")},
			wantErr: ErrNilPipeline,
		},
		{
			name:   "RejectsMissingExecutor",
			runner: mustNewRunner(t, nil, &testutil.FakeClock{}),
			request: RunRequest{OrgSlug: must.Slug("acme"), EnvironmentSlug: must.Slug("prod"), Pipeline: &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{
					Slug: "acme-users-sync",
					Name: "ACME Users Sync",
				},
				Steps: []pipelinespec.Step{{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "list-active-users",
					},
				}},
			}},
			wantErr: ErrQueryExecutorNotProvided,
		},
		{
			name:   "RejectsMissingEnvironmentSlug",
			runner: mustNewRunner(t, &testutil.FakeQueryExecutor{}, &testutil.FakeClock{}),
			request: RunRequest{OrgSlug: must.Slug("acme"), Pipeline: &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{
					Slug: "acme-users-sync",
					Name: "ACME Users Sync",
				},
				Steps: []pipelinespec.Step{{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "list-active-users",
					},
				}},
			}},
			wantErr: ErrMissingEnvironmentSlug,
		},
		{
			name: "ReturnsFailedMetadataOnExecutionError",
			runner: mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				return nil, fmt.Errorf("boom")
			}}, &testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC)
			}}),
			request: RunRequest{OrgSlug: must.Slug("acme"), EnvironmentSlug: must.Slug("prod"), Pipeline: &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{
					Slug: "acme-users-sync",
					Name: "ACME Users Sync",
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
				},
			}},
			wantErrContains: "execute step \"fetch-users\": boom",
			assertFn: func(t *testing.T, runResult *pipelinerun.RunResult) {
				require.NotNil(t, runResult)
				require.Equal(t, "failed", runResult.Metadata.Status)
				require.Regexp(
					t,
					`^acme-users-sync-20260313100000-[a-z0-9]{4}$`,
					runResult.Metadata.RunSlug,
				)
				require.Equal(t, int64(0), runResult.Metadata.DurationMs)
				require.Equal(t, 1, runResult.Metadata.StepCount)
				require.Equal(t, must.Slug("fetch-users"), *runResult.Metadata.FailedStep)
				require.Equal(t, "failed", runResult.Timeline.Status)
				require.Len(t, runResult.Timeline.Events, 4)
				require.Equal(t, "step_failed", runResult.Timeline.Events[2].Type)
				require.Len(t, runResult.Timeline.Snapshots, 1)
				require.NotNil(t, runResult.Timeline.Failure)
				require.Equal(t, must.Slug("fetch-users"), runResult.Timeline.Failure.Step)
				require.Equal(t, "data source execution failed", runResult.Timeline.Failure.Error)
			},
		},
		{
			name: "SourceSafetyLimitFailureKeepsDataSourceContext",
			runner: mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				return nil, xerror.ExecutionResponseSizeExceeded().
					Detail("response body exceeds configured response size limit")
			}}, &testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC)
			}}),
			request: RunRequest{OrgSlug: must.Slug("acme"), EnvironmentSlug: must.Slug("prod"), Pipeline: &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{
					Slug: "acme-users-sync",
					Name: "ACME Users Sync",
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
				},
			}},
			wantErrContains: "execute step \"fetch-users\": execution response size exceeded: response body exceeds configured response size limit",
			assertFn: func(t *testing.T, runResult *pipelinerun.RunResult) {
				require.NotNil(t, runResult)
				require.Equal(t, "failed", runResult.Metadata.Status)
				require.NotNil(t, runResult.Timeline.Failure)
				require.Equal(t, must.Slug("fetch-users"), runResult.Timeline.Failure.Step)
				require.Equal(t, failureKindDataSource, runResult.Timeline.Failure.Kind)
				require.Equal(t, failureSideSource, runResult.Timeline.Failure.Side)
				require.Equal(t, "acme-postgres", runResult.Timeline.Failure.DataSourceSlug)
				require.Contains(t, runResult.Timeline.Failure.Error, "response size")
				require.Contains(t, runResult.Timeline.Failure.ActionHint, "response size limit")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runResult, err := tc.runner.Run(context.Background(), tc.request)
			switch {
			case tc.wantErr != nil:
				require.ErrorIs(t, err, tc.wantErr)
			case tc.wantErrContains != "":
				require.Error(t, err)
				require.Equal(t, tc.wantErrContains, err.Error())
			default:
				require.NoError(t, err)
			}

			if tc.assertFn != nil {
				tc.assertFn(t, runResult)
			}
		})
	}
}

func TestRunner_SnapshotSanitizesParameterValues(t *testing.T) {
	t.Parallel()

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{},
			ColumnNames: []string{"id"},
			RowCount:    0,
		}, 3, nil), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC)
	}})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-secret-test",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "acme-users-sync",
				Name: "ACME Users Sync",
			},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-active-users",
					Parameters: map[string]any{
						"limit":        100,
						"api_key":      "super-secret",
						"access_token": "token-value",
					},
				},
			}},
		},
	})
	require.NoError(t, err)
	require.Len(t, runResult.Timeline.Snapshots, 1)
	require.Equal(
		t,
		[]string{"access_token", "api_key", "limit"},
		runResult.Timeline.Snapshots[0].Input.ParameterKeys,
	)

	encoded, err := json.Marshal(runResult.Timeline)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "super-secret")
	require.NotContains(t, string(encoded), "token-value")
}

func TestRunner_RunExecutesQueryOperationRefForEachSupportedType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		dataSourceSlug   slug.Slug
		operationType    string
		operationRef     slug.Slug
		buildResult      func() *result.ExecutionResult
		assertOutputSnap func(t *testing.T, output *pipelinerun.StepOutputSnapshot)
	}{
		{
			name:           "SQL",
			dataSourceSlug: must.Slug("acme-postgres"),
			operationType:  "sql",
			operationRef:   must.Slug("list-users"),
			buildResult: func() *result.ExecutionResult {
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 1}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 7, nil)
			},
			assertOutputSnap: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.NotNil(t, output)
				require.Equal(t, "SQL", output.ResultType)
				require.Equal(t, "postgres", output.VendorType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindRows)
				require.Equal(t, []string{"id"}, details.ColumnNames)
				require.NotNil(t, details.RowCount)
				require.Equal(t, 1, *details.RowCount)
			},
		},
		{
			name:           "HTTP",
			dataSourceSlug: must.Slug("crm-api"),
			operationType:  "http",
			operationRef:   must.Slug("fetch-users-http"),
			buildResult: func() *result.ExecutionResult {
				return result.NewHTTPResult(&result.HTTPResult{
					StatusCode: 200,
					Body:       json.RawMessage(`{"users":[{"id":1}]}`),
				}, 9, nil)
			},
			assertOutputSnap: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.NotNil(t, output)
				require.Equal(t, "HTTP", output.ResultType)
				require.Equal(t, "http", output.VendorType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindHTTPResponse)
				require.NotNil(t, details.StatusCode)
				require.Equal(t, 200, *details.StatusCode)
			},
		},
		{
			name:           "FTP",
			dataSourceSlug: must.Slug("acme-ftp"),
			operationType:  "ftp",
			operationRef:   must.Slug("read-users-csv"),
			buildResult: func() *result.ExecutionResult {
				return result.NewFTPResult(&result.FileResult{
					Rows:        []map[string]any{{"id": "1"}},
					ColumnNames: []string{"id"},
					RowCount:    1,
					Format:      "csv",
				}, 11, nil)
			},
			assertOutputSnap: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.NotNil(t, output)
				require.Equal(t, "FTP", output.ResultType)
				require.Equal(t, "ftp", output.VendorType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindFileRead)
				require.Equal(t, []string{"id"}, details.ColumnNames)
				require.NotNil(t, details.RowCount)
				require.Equal(t, 1, *details.RowCount)
			},
		},
		{
			name:           "MongoDB",
			dataSourceSlug: must.Slug("acme-mongodb"),
			operationType:  "mongodb",
			operationRef:   must.Slug("find-users"),
			buildResult: func() *result.ExecutionResult {
				documentCount := 2
				return result.NewMongoDBResult(&result.MongoDBResult{
					OperationType: "find",
					DocumentCount: &documentCount,
					Documents: []map[string]any{
						{"id": 1},
						{"id": 2},
					},
				}, 13, nil)
			},
			assertOutputSnap: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.NotNil(t, output)
				require.Equal(t, "MongoDB", output.ResultType)
				require.Equal(t, "mongodb", output.VendorType)
				require.Equal(t, "find", output.OperationType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindDocuments)
				require.NotNil(t, details.DocumentCount)
				require.Equal(t, 2, *details.DocumentCount)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executorCalls := 0
			runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
				ctx context.Context,
				orgSlug, dsSlug, querySlug slug.Slug,
				paramValues map[string]any,
			) (*result.ExecutionResult, error) {
				executorCalls++
				require.Equal(t, must.Slug("acme"), orgSlug)
				require.Equal(t, tc.dataSourceSlug, dsSlug)
				require.Equal(t, tc.operationRef, querySlug)
				require.Empty(t, paramValues)
				return tc.buildResult(), nil
			}}, &testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.April, 14, 10, 0, 0, 0, time.UTC)
			}})

			runResult, err := runner.Run(context.Background(), RunRequest{
				OrgSlug:         must.Slug("acme"),
				EnvironmentSlug: must.Slug("prod"),
				RunSlug:         "query-type-runtime-001",
				Pipeline: &pipelinespec.Pipeline{
					Metadata: pipelinespec.Metadata{Slug: "users-sync", Name: "Users Sync"},
					Steps: []pipelinespec.Step{{
						Kind: "query",
						Slug: "fetch-users",
						Query: &pipelinespec.QueryRef{
							DataSourceSlug: tc.dataSourceSlug,
							OperationType:  tc.operationType,
							OperationRef:   tc.operationRef,
						},
					}},
				},
			})

			require.NoError(t, err)
			require.Equal(t, 1, executorCalls)
			require.NotNil(t, runResult)
			require.Equal(t, "succeeded", runResult.Metadata.Status)
			require.Len(t, runResult.Timeline.Snapshots, 1)

			snapshot := runResult.Timeline.Snapshots[0]
			require.Equal(t, must.Slug("fetch-users"), snapshot.Step)
			require.Equal(t, tc.dataSourceSlug, snapshot.Input.DataSourceSlug)
			require.Equal(t, tc.operationType, snapshot.Input.OperationType)
			require.Equal(t, tc.operationRef, snapshot.Input.OperationRef)
			tc.assertOutputSnap(t, snapshot.Output)
		})
	}
}

func TestRunner_RunExecutesInlineQueryOperationForEachSupportedType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		dataSourceSlug   slug.Slug
		operationType    string
		operationInline  map[string]any
		buildResult      func() *result.ExecutionResult
		assertOutputSnap func(t *testing.T, output *pipelinerun.StepOutputSnapshot)
	}{
		{
			name:            "SQL",
			dataSourceSlug:  must.Slug("acme-postgres"),
			operationType:   "sql",
			operationInline: map[string]any{"sql": "SELECT id FROM users"},
			buildResult: func() *result.ExecutionResult {
				return result.NewSQLResult("postgres", &result.SQLResult{
					Rows:        []map[string]any{{"id": 1}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				}, 7, nil)
			},
			assertOutputSnap: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.NotNil(t, output)
				require.Equal(t, "SQL", output.ResultType)
				require.Equal(t, "postgres", output.VendorType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindRows)
				require.Equal(t, []string{"id"}, details.ColumnNames)
				require.NotNil(t, details.RowCount)
				require.Equal(t, 1, *details.RowCount)
			},
		},
		{
			name:            "HTTP",
			dataSourceSlug:  must.Slug("crm-api"),
			operationType:   "http",
			operationInline: map[string]any{"method": "GET", "path": "/v1/users"},
			buildResult: func() *result.ExecutionResult {
				return result.NewHTTPResult(&result.HTTPResult{
					StatusCode: 200,
					Body:       json.RawMessage(`{"users":[{"id":1}]}`),
				}, 9, nil)
			},
			assertOutputSnap: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.NotNil(t, output)
				require.Equal(t, "HTTP", output.ResultType)
				require.Equal(t, "http", output.VendorType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindHTTPResponse)
				require.NotNil(t, details.StatusCode)
				require.Equal(t, 200, *details.StatusCode)
			},
		},
		{
			name:           "FTP",
			dataSourceSlug: must.Slug("acme-ftp"),
			operationType:  "ftp",
			operationInline: map[string]any{
				"path":   "/exports/users.csv",
				"format": "csv",
			},
			buildResult: func() *result.ExecutionResult {
				return result.NewFTPResult(&result.FileResult{
					Rows:        []map[string]any{{"id": "1"}},
					ColumnNames: []string{"id"},
					RowCount:    1,
					Format:      "csv",
				}, 11, nil)
			},
			assertOutputSnap: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.NotNil(t, output)
				require.Equal(t, "FTP", output.ResultType)
				require.Equal(t, "ftp", output.VendorType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindFileRead)
				require.Equal(t, []string{"id"}, details.ColumnNames)
				require.NotNil(t, details.RowCount)
				require.Equal(t, 1, *details.RowCount)
			},
		},
		{
			name:           "MongoDB",
			dataSourceSlug: must.Slug("acme-mongodb"),
			operationType:  "mongodb",
			operationInline: map[string]any{
				"operation":  "find",
				"collection": "users",
			},
			buildResult: func() *result.ExecutionResult {
				documentCount := 2
				return result.NewMongoDBResult(&result.MongoDBResult{
					OperationType: "find",
					DocumentCount: &documentCount,
					Documents: []map[string]any{
						{"id": 1},
						{"id": 2},
					},
				}, 13, nil)
			},
			assertOutputSnap: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.NotNil(t, output)
				require.Equal(t, "MongoDB", output.ResultType)
				require.Equal(t, "mongodb", output.VendorType)
				require.Equal(t, "find", output.OperationType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindDocuments)
				require.NotNil(t, details.DocumentCount)
				require.Equal(t, 2, *details.DocumentCount)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			executeCalls := 0
			executeInlineCalls := 0
			runner := mustNewRunner(t, &testutil.FakeQueryExecutor{
				ExecuteFn: func(
					ctx context.Context,
					orgSlug, dsSlug, querySlug slug.Slug,
					paramValues map[string]any,
				) (*result.ExecutionResult, error) {
					executeCalls++
					return nil, fmt.Errorf("unexpected saved-query execution")
				},
				ExecuteInlineFn: func(
					ctx context.Context,
					orgSlug, dsSlug slug.Slug,
					operationInline map[string]any,
					paramValues map[string]any,
				) (*result.ExecutionResult, error) {
					executeInlineCalls++
					require.Equal(t, must.Slug("acme"), orgSlug)
					require.Equal(t, tc.dataSourceSlug, dsSlug)
					require.Equal(t, tc.operationInline, operationInline)
					require.Empty(t, paramValues)
					return tc.buildResult(), nil
				},
			}, &testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.April, 14, 10, 0, 0, 0, time.UTC)
			}})

			runResult, err := runner.Run(context.Background(), RunRequest{
				OrgSlug:         must.Slug("acme"),
				EnvironmentSlug: must.Slug("prod"),
				RunSlug:         "query-inline-runtime-001",
				Pipeline: &pipelinespec.Pipeline{
					Metadata: pipelinespec.Metadata{Slug: "users-sync", Name: "Users Sync"},
					Steps: []pipelinespec.Step{{
						Kind: "query",
						Slug: "fetch-users",
						Query: &pipelinespec.QueryRef{
							DataSourceSlug:  tc.dataSourceSlug,
							OperationType:   tc.operationType,
							OperationInline: tc.operationInline,
						},
					}},
				},
			})

			require.NoError(t, err)
			require.Equal(t, 0, executeCalls)
			require.Equal(t, 1, executeInlineCalls)
			require.NotNil(t, runResult)
			require.Equal(t, "succeeded", runResult.Metadata.Status)
			require.Len(t, runResult.Timeline.Snapshots, 1)

			snapshot := runResult.Timeline.Snapshots[0]
			require.Equal(t, must.Slug("fetch-users"), snapshot.Step)
			require.Equal(t, tc.dataSourceSlug, snapshot.Input.DataSourceSlug)
			require.Equal(t, tc.operationType, snapshot.Input.OperationType)
			require.Empty(t, snapshot.Input.OperationRef)
			require.True(t, snapshot.Input.UsesInline)
			tc.assertOutputSnap(t, snapshot.Output)
		})
	}
}

func TestRunner_RunResolvesStepOutputReferences(t *testing.T) {
	t.Parallel()

	// step-1 returns rows; step-2 receives them via $steps.step-1.rows.
	var step2Params map[string]any
	callCount := 0

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		callCount++
		if callCount == 1 {
			// step-1: return some rows.
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": 1}, {"id": 2}},
				ColumnNames: []string{"id"},
				RowCount:    2,
			}, 10, nil), nil
		}
		// step-2: capture resolved params.
		step2Params = paramValues
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:     []map[string]any{{"ok": true}},
			RowCount: 1,
		}, 5, nil), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC)
	}})

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "two-step-pipe", Name: "Two Step Pipe"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "step-1",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-active-users",
				},
			},
			{
				Kind: "query",
				Slug: "step-2",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "process-users",
					Parameters:     map[string]any{"users": "$steps.step-1.rows"},
				},
			},
		},
	}

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-two",
		Pipeline:        pipeline,
	})
	require.NoError(t, err)
	require.Equal(t, 2, callCount)
	require.Equal(t, "succeeded", runResult.Metadata.Status)
	require.Equal(t, 2, runResult.Metadata.StepCount)
	require.Nil(t, runResult.Metadata.FailedStep)

	// step-2 should have received resolved rows, not the raw reference string.
	resolvedRows, ok := step2Params["users"].([]map[string]any)
	require.True(t, ok, "expected step2 users param to be resolved to []map[string]any")
	require.Equal(t, []map[string]any{{"id": 1}, {"id": 2}}, resolvedRows)

	// Both steps have results.
	require.Len(t, runResult.StepResults, 2)
	_, hasStep1 := runResult.StepResults[must.Slug("step-1")]
	_, hasStep2 := runResult.StepResults[must.Slug("step-2")]
	require.True(t, hasStep1)
	require.True(t, hasStep2)
}

func TestRunner_TimelineByRunSlugReturnsErrorForMissingRun(t *testing.T) {
	t.Parallel()

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{}, &testutil.FakeClock{})
	timeline, err := runner.TimelineByRunSlug(
		context.Background(),
		must.Slug("acme"),
		must.Slug("default"),
		must.Slug("prod"),
		"missing-run",
	)
	require.ErrorIs(t, err, pipelinerun.ErrTimelineNotFound)
	require.Nil(t, timeline)
}

func TestSnapshotOutput(t *testing.T) {
	t.Parallel()

	docCount := 2

	tests := []struct {
		name   string
		input  *pipelinerun.StepResult
		assert func(t *testing.T, output *pipelinerun.StepOutputSnapshot)
	}{
		{
			name: "SQL",
			input: pipelinerun.NewStepResult(result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": 1}},
				ColumnNames: []string{"id"},
				RowCount:    1,
			}, 12, nil)),
			assert: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.Equal(t, "SQL", output.ResultType)
				require.Equal(t, "postgres", output.VendorType)
				require.Equal(t, int64(12), output.DurationMs)
				require.Empty(t, output.OperationType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindRows)
				require.NotNil(t, details.RowCount)
				require.Equal(t, 1, *details.RowCount)
				require.Equal(t, []string{"id"}, details.ColumnNames)
				require.Nil(t, details.TargetPath)
				require.Nil(t, details.DocumentCount)
				require.Nil(t, details.StatusCode)
			},
		},
		{
			name: "MongoDB",
			input: pipelinerun.NewStepResult(result.NewMongoDBResult(&result.MongoDBResult{
				OperationType: "find",
				DocumentCount: &docCount,
				Documents: []map[string]any{
					{"id": 1},
					{"id": 2},
				},
			}, 18, nil)),
			assert: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.Equal(t, "MongoDB", output.ResultType)
				require.Equal(t, "mongodb", output.VendorType)
				require.Equal(t, int64(18), output.DurationMs)
				require.Equal(t, "find", output.OperationType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindDocuments)
				require.NotNil(t, details.DocumentCount)
				require.Equal(t, 2, *details.DocumentCount)
				require.Nil(t, details.RowCount)
				require.Empty(t, details.ColumnNames)
				require.Nil(t, details.StatusCode)
			},
		},
		{
			name: "MongoDBZeroDocuments",
			input: pipelinerun.NewStepResult(result.NewMongoDBResult(&result.MongoDBResult{
				OperationType: "find",
				Documents:     []map[string]any{},
			}, 19, nil)),
			assert: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.Equal(t, "MongoDB", output.ResultType)
				require.Equal(t, "mongodb", output.VendorType)
				require.Equal(t, int64(19), output.DurationMs)
				require.Equal(t, "find", output.OperationType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindDocuments)
				require.NotNil(t, details.DocumentCount)
				require.Equal(t, 0, *details.DocumentCount)
				require.Nil(t, details.RowCount)
				require.Empty(t, details.ColumnNames)
				require.Nil(t, details.StatusCode)
			},
		},
		{
			name: "HTTP",
			input: pipelinerun.NewStepResult(result.NewHTTPResult(&result.HTTPResult{
				StatusCode: 201,
				Headers:    map[string]string{"content-type": "application/json"},
				Body:       []byte(`{"ok":true,"token":"secret"}`),
			}, 25, nil)),
			assert: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.Equal(t, "HTTP", output.ResultType)
				require.Equal(t, "http", output.VendorType)
				require.Equal(t, int64(25), output.DurationMs)
				require.Empty(t, output.OperationType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindHTTPResponse)
				require.NotNil(t, details.StatusCode)
				require.Equal(t, 201, *details.StatusCode)
				require.Nil(t, details.RowCount)
				require.Nil(t, details.DocumentCount)
				require.Empty(t, details.ColumnNames)
			},
		},
		{
			name: "Transform",
			input: pipelinerun.NewTransformStepResult(&payload.Output{
				OutputKind:    model.TransformOutputKindRows,
				Rows:          []map[string]any{{"col_0": "alice"}},
				ColumnNames:   []string{"col_0"},
				RowCount:      1,
				IsHeadlessCSV: true,
			}, 9, nil),
			assert: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.Equal(t, "Transform", output.ResultType)
				require.Equal(t, "transform", output.VendorType)
				require.Equal(t, int64(9), output.DurationMs)
				require.Empty(t, output.OperationType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindRows)
				require.NotNil(t, details.RowCount)
				require.Equal(t, 1, *details.RowCount)
				require.Equal(t, []string{"col_0"}, details.ColumnNames)
				require.True(t, details.IsHeadlessCSV)
				require.Nil(t, details.DocumentCount)
				require.Nil(t, details.StatusCode)
			},
		},
		{
			name: "ObjectRows",
			input: pipelinerun.NewStepResult(result.NewObjectRowsResult("hubspot", &result.ObjectRowsResult{
				ObjectType:  "contacts",
				Operation:   " search ",
				Rows:        []map[string]any{{"id": "1", "email": "one@example.test"}},
				ColumnNames: []string{"id", "email"},
				RowCount:    1,
			}, 11, nil)),
			assert: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.Equal(t, "ObjectRows", output.ResultType)
				require.Equal(t, "hubspot", output.VendorType)
				require.Equal(t, int64(11), output.DurationMs)
				require.Equal(t, "search", output.OperationType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindRows)
				require.NotNil(t, details.RowCount)
				require.Equal(t, 1, *details.RowCount)
				require.Equal(t, []string{"id", "email"}, details.ColumnNames)
				require.Nil(t, details.TargetPath)
				require.Nil(t, details.DocumentCount)
				require.Nil(t, details.StatusCode)
			},
		},
		{
			name: "OpaqueFileRead",
			input: func() *pipelinerun.StepResult {
				content := []byte{0, 1, 2}
				metadata := filepayload.NewFilePayloadMetadata(
					"users.bin",
					"application/octet-stream",
					content,
				)
				return pipelinerun.NewStepResult(result.NewFTPResult(&result.FileResult{
					Format:     "file",
					Operation:  "read",
					TargetPath: "/exports/users.bin",
					File:       &metadata,
					Content:    content,
				}, 13, nil))
			}(),
			assert: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.Equal(t, "FTP", output.ResultType)
				require.Equal(t, "ftp", output.VendorType)
				require.Equal(t, int64(13), output.DurationMs)
				require.Equal(t, "read", output.OperationType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindFileRead)
				require.Nil(t, details.RowCount)
				require.Empty(t, details.ColumnNames)
				require.NotNil(t, details.TargetPath)
				require.Equal(t, "/exports/users.bin", *details.TargetPath)
				require.NotNil(t, details.File)
				require.Equal(t, "users.bin", details.File.Filename)
				require.Equal(t, "application/octet-stream", details.File.MIMEType)
				require.EqualValues(t, 3, details.File.SizeBytes)
				require.Nil(t, details.DocumentCount)
				require.Nil(t, details.StatusCode)
			},
		},
		{
			name: "Git",
			input: pipelinerun.NewStepResult(result.NewGitResult(&result.FileResult{
				Rows:        []map[string]any{{"id": 1}},
				ColumnNames: []string{"id"},
				RowCount:    1,
				Format:      "json",
				Operation:   "write",
				TargetPath:  "exports/users.json",
			}, 14, nil)),
			assert: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.Equal(t, "Git", output.ResultType)
				require.Equal(t, "git", output.VendorType)
				require.Equal(t, int64(14), output.DurationMs)
				require.Equal(t, "write", output.OperationType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindFileWrite)
				require.Nil(t, details.RowCount)
				require.Empty(t, details.ColumnNames)
				require.NotNil(t, details.TargetPath)
				require.Equal(t, "exports/users.json", *details.TargetPath)
				require.Nil(t, details.DocumentCount)
				require.Nil(t, details.StatusCode)
			},
		},
		{
			name: "S3Write",
			input: func() *pipelinerun.StepResult {
				writtenRows := 2
				writtenBytes := 96
				return pipelinerun.NewStepResult(result.NewS3Result(&result.FileResult{
					Format:       "json",
					Operation:    "write",
					TargetPath:   "golden-path/runs/run-001/joined-orders.json",
					WrittenRows:  &writtenRows,
					WrittenBytes: &writtenBytes,
				}, 21, nil))
			}(),
			assert: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.Equal(t, "S3", output.ResultType)
				require.Equal(t, "s3", output.VendorType)
				require.Equal(t, int64(21), output.DurationMs)
				require.Equal(t, "write", output.OperationType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindFileWrite)
				require.Nil(t, details.RowCount)
				require.NotNil(t, details.TargetPath)
				require.Equal(t, "golden-path/runs/run-001/joined-orders.json", *details.TargetPath)
				require.NotNil(t, details.WrittenRows)
				require.Equal(t, 2, *details.WrittenRows)
				require.NotNil(t, details.WrittenBytes)
				require.Equal(t, 96, *details.WrittenBytes)
				require.Empty(t, details.ColumnNames)
				require.Nil(t, details.DocumentCount)
				require.Nil(t, details.StatusCode)
			},
		},
		{
			name: "S3WriteEmptyTargetPathKeepsRequiredField",
			input: pipelinerun.NewStepResult(result.NewS3Result(&result.FileResult{
				Format:    "json",
				Operation: "write",
			}, 22, nil)),
			assert: func(t *testing.T, output *pipelinerun.StepOutputSnapshot) {
				require.Equal(t, "S3", output.ResultType)
				require.Equal(t, "s3", output.VendorType)
				require.Equal(t, int64(22), output.DurationMs)
				require.Equal(t, "write", output.OperationType)
				details := requireStepOutputDetails(t, output, pipelinerun.StepOutputDetailKindFileWrite)
				require.NotNil(t, details.TargetPath)
				require.Empty(t, *details.TargetPath)

				body, err := json.Marshal(output)
				require.NoError(t, err)
				require.Contains(t, string(body), `"target_path":""`)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			output := snapshotOutput(tc.input)
			require.NotNil(t, output)
			tc.assert(t, output)
		})
	}
}

func TestRunner_RunIsDeterministicForIdenticalInputs(t *testing.T) {
	t.Parallel()

	newRunner := func() *Runner {
		clockTimes := []time.Time{
			time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
			time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
			time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
			time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
			time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
		}
		clockIndex := 0

		return mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"email": "user@example.com"}},
				ColumnNames: []string{"email"},
				RowCount:    1,
			}, 12, nil), nil
		}}, &testutil.FakeClock{NowFn: func() time.Time {
			current := clockTimes[clockIndex]
			clockIndex++
			return current
		}})
	}

	request := RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "acme-users-sync",
				Name: "ACME Users Sync",
			},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-active-users",
					Parameters:     map[string]any{"limit": 100},
				},
			}},
		},
	}

	first, err := newRunner().Run(context.Background(), request)
	require.NoError(t, err)

	second, err := newRunner().Run(context.Background(), request)
	require.NoError(t, err)

	firstJSON, err := json.Marshal(first)
	require.NoError(t, err)

	secondJSON, err := json.Marshal(second)
	require.NoError(t, err)

	require.JSONEq(t, string(firstJSON), string(secondJSON))
}

func TestCollectEdgeDiagnostics(t *testing.T) {
	t.Parallel()

	t.Run("EDGE-RUNTIME-001_CollectsCompatibleTransformToSinkDiagnostic", func(t *testing.T) {
		t.Parallel()

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "acme-users-sync",
				Name: "ACME Users Sync",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: must.Slug("fetch-users"),
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: must.Slug("acme-postgres"),
						OperationType:  "sql",
						OperationRef:   must.Slug("list-users"),
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}}},
					},
				},
				{
					Kind: "transform",
					Slug: "normalize-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, OutputKind: model.TransformOutputKindRows,
						OutputContract: &payload.Contract{
							Fields: []payload.ContractField{{
								Name:     "email",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							}},
						},
					},
				},
				{
					Kind: "sink",
					Slug: "acme-users-sync",
					Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: must.Slug("normalize-users")}},
				},
			},
		}

		diagnostics := collectEdgeDiagnostics(pipeline)
		require.Len(t, diagnostics, 2)
		require.True(t, diagnostics[1].Compatible)
		require.Empty(t, diagnostics[1].ReasonCode)
	})

	t.Run("EDGE-RUNTIME-002_CollectsRepresentationMismatchDiagnostic", func(t *testing.T) {
		t.Parallel()

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "acme-users-sync",
				Name: "ACME Users Sync",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: must.Slug("fetch-users"),
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: must.Slug("acme-postgres"),
						OperationType:  "sql",
						OperationRef:   must.Slug("list-users"),
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}}},
					},
				},
				{
					Kind: "transform",
					Slug: "normalize-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, OutputKind: model.TransformOutputKindRows,
						OutputContract: &payload.Contract{
							Fields: []payload.ContractField{{
								Name:     "email",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							}},
						},
					},
				},
				{
					Kind: "sink",
					Slug: "acme-users-sync",
					Sink: &pipelinespec.SinkSpec{
						IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
						Input:            &pipelinespec.StepInput{FromStep: must.Slug("normalize-users")},
						InputContract: &payload.Contract{
							Representation: model.ContractRepresentationJSON,
							RootShape:      model.ContractRootShapeObject,
							Fields: []payload.ContractField{{
								Name:     "email",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							}},
						},
					},
				},
			},
		}

		diagnostics := collectEdgeDiagnostics(pipeline)
		require.Len(t, diagnostics, 2)
		require.False(t, diagnostics[1].Compatible)
		require.Equal(t, pipelinevalidate.ContractReasonRepresentationMismatch, diagnostics[1].ReasonCode)
	})

	t.Run("EDGE-RUNTIME-003_ScopesMultiInputTransformContracts", func(t *testing.T) {
		t.Parallel()

		pipeline := &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "acme-users-join",
				Name: "ACME Users Join",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: must.Slug("fetch-left-users"),
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: must.Slug("acme-postgres"),
						OperationType:  "sql",
						OperationRef:   must.Slug("list-left-users"),
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}}},
					},
				},
				{
					Kind: "query",
					Slug: must.Slug("fetch-right-users"),
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: must.Slug("acme-postgres"),
						OperationType:  "sql",
						OperationRef:   must.Slug("list-right-users"),
						OutputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "id",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}}},
					},
				},
				{
					Kind: "transform",
					Slug: must.Slug("join-users"),
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{
							"left":  {FromStep: must.Slug("fetch-left-users")},
							"right": {FromStep: must.Slug("fetch-right-users")},
						},
						InputContract: &payload.Contract{
							Fields: []payload.ContractField{
								{
									Name:     "left.id",
									Type:     model.ContractFieldTypeValueString,
									Required: true,
								},
								{
									Name:     "right.id",
									Type:     model.ContractFieldTypeValueString,
									Required: true,
								},
							},
						},
						OutputKind: model.TransformOutputKindRows,
					},
				},
			},
		}
		completedResults := map[slug.Slug]*pipelinerun.StepResult{
			must.Slug("fetch-left-users"): pipelinerun.NewStepResult(result.NewSQLResult(
				"postgres",
				&result.SQLResult{
					Rows:        []map[string]any{{"id": "left-1"}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				},
				12,
				nil,
			)),
			must.Slug("fetch-right-users"): pipelinerun.NewStepResult(result.NewSQLResult(
				"postgres",
				&result.SQLResult{
					Rows:        []map[string]any{{"id": "right-1"}},
					ColumnNames: []string{"id"},
					RowCount:    1,
				},
				12,
				nil,
			)),
		}
		annotateStepResultPayload(
			pipeline.Steps[0],
			pipeline.Steps[0],
			completedResults[must.Slug("fetch-left-users")],
			nil,
		)
		annotateStepResultPayload(
			pipeline.Steps[1],
			pipeline.Steps[1],
			completedResults[must.Slug("fetch-right-users")],
			nil,
		)

		diagnostics := runtimeEdgeDiagnostics(pipeline, completedResults)
		require.Len(t, diagnostics, 2)
		require.Equal(t, must.Slug("fetch-left-users"), diagnostics[0].FromStepSlug)
		require.Equal(t, must.Slug("join-users"), diagnostics[0].ToStepSlug)
		require.Truef(t, diagnostics[0].Compatible, "%+v", diagnostics[0])
		require.Empty(t, diagnostics[0].ReasonCode)
		require.Equal(t, must.Slug("fetch-right-users"), diagnostics[1].FromStepSlug)
		require.Equal(t, must.Slug("join-users"), diagnostics[1].ToStepSlug)
		require.Truef(t, diagnostics[1].Compatible, "%+v", diagnostics[1])
		require.Empty(t, diagnostics[1].ReasonCode)
	})
}

func TestRunner_Replay(t *testing.T) {
	t.Parallel()

	clockTimes := []time.Time{
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 0, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 0, 125_000_000, time.UTC),
	}
	clockIndex := 0
	queryCalls := 0

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		queryCalls++
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"email": "user@example.com"}},
			ColumnNames: []string{"email"},
			RowCount:    1,
		}, 12, nil), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		if clockIndex >= len(clockTimes) {
			return clockTimes[len(clockTimes)-1]
		}
		current := clockTimes[clockIndex]
		clockIndex++
		return current
	}})

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "acme-users-sync",
			Name: "ACME Users Sync",
		},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: "fetch-users",
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: "acme-postgres",
				OperationType:  "sql",
				OperationRef:   "list-active-users",
				Parameters:     map[string]any{"limit": 100},
			},
		}},
	}

	firstRun, err := runner.Run(
		context.Background(),
		RunRequest{OrgSlug: must.Slug("acme"), EnvironmentSlug: must.Slug("prod"), RunSlug: "run-001", Pipeline: pipeline},
	)
	require.NoError(t, err)
	require.NotNil(t, firstRun)

	replayRun, err := runner.Replay(context.Background(), ReplayRequest{
		OrgSlug:       must.Slug("acme"),
		SourceRunSlug: "run-001",
		ReplayRunSlug: "run-001-replay",
		FromStep:      "fetch-users",
		InputMode:     ReplayInputModePinned,
	})
	require.NoError(t, err)
	require.NotNil(t, replayRun)
	require.Equal(t, "run-001-replay", replayRun.Metadata.RunSlug)
	require.Equal(t, "succeeded", replayRun.Metadata.Status)
	require.NotNil(t, replayRun.Metadata.ReplayOfRunSlug)
	require.Equal(t, "run-001", *replayRun.Metadata.ReplayOfRunSlug)
	require.NotNil(t, replayRun.Metadata.ReplayFromStep)
	require.Equal(t, "fetch-users", *replayRun.Metadata.ReplayFromStep)
	require.NotNil(t, replayRun.Timeline.ReplayOfRunSlug)
	require.Equal(t, "run-001", *replayRun.Timeline.ReplayOfRunSlug)

	timeline, err := runner.TimelineByRunSlug(
		context.Background(),
		must.Slug("acme"),
		must.Slug("default"),
		must.Slug("prod"),
		"run-001-replay",
	)
	require.NoError(t, err)
	require.NotNil(t, timeline)
	require.NotNil(t, timeline.ReplayOfRunSlug)
	require.Equal(t, "run-001", *timeline.ReplayOfRunSlug)
	require.Equal(t, 1, queryCalls, "pinned replay should not call query executor")
}

func TestRunner_ReplayIncludesRuntimeAwareEdgeDiagnostics(t *testing.T) {
	t.Parallel()

	queryCalls := 0

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			queryCalls++
			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows:        []map[string]any{{"id": 1}},
				ColumnNames: []string{"id"},
				RowCount:    1,
			}, 12, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 24, 9, 0, 0, 0, time.UTC)
		}},
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

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "users-sync",
			Name: "Users Sync",
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
				Kind: "transform",
				Slug: "project-users",
				Transform: &pipelinespec.TransformSpec{
					Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS user_id FROM input",
					OutputKind: model.TransformOutputKindRows,
					OutputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "user_id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: true,
					}}},
				},
			},
			{
				Kind: "sink",
				Slug: "users-sync",
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger,
					Input:            &pipelinespec.StepInput{FromStep: must.Slug("project-users")},
					InputContract: &payload.Contract{Fields: []payload.ContractField{{
						Name:     "user_id",
						Type:     model.ContractFieldTypeValueInteger,
						Required: true,
					}}},
					DataSourceSlug: "acme-ftp",
					OperationType:  "ftp",
					OperationRef:   "upload-users",
					Config:         map[string]any{"path": "users-out.csv"},
				},
			},
		},
	}

	firstRun, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-002",
		Pipeline:        pipeline,
	})
	require.NoError(t, err)
	require.NotNil(t, firstRun)
	require.Equal(t, 1, sinkCalls)
	require.Len(t, firstRun.EdgeDiagnostics, 2)
	require.True(t, firstRun.EdgeDiagnostics[1].Compatible)

	replayRun, err := runner.Replay(context.Background(), ReplayRequest{
		OrgSlug:       must.Slug("acme"),
		SourceRunSlug: "run-002",
		ReplayRunSlug: "run-002-replay",
		InputMode:     ReplayInputModePinned,
	})
	require.NoError(t, err)
	require.NotNil(t, replayRun)
	require.Len(t, replayRun.EdgeDiagnostics, 2)
	require.True(t, replayRun.EdgeDiagnostics[0].Compatible)
	require.True(t, replayRun.EdgeDiagnostics[1].Compatible)
	require.Empty(t, replayRun.EdgeDiagnostics[1].ReasonCode)
	require.Equal(t, 1, queryCalls, "pinned replay should not call query executor")
}

func TestRunner_ReplayGeneratesUniqueDefaultReplayRunSlug(t *testing.T) {
	t.Parallel()

	clockTimes := []time.Time{
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 1, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 1, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 1, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 1, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 2, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 2, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 2, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 2, 125_000_000, time.UTC),
	}
	clockIndex := 0

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"email": "user@example.com"}},
			ColumnNames: []string{"email"},
			RowCount:    1,
		}, 12, nil), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		if clockIndex >= len(clockTimes) {
			return clockTimes[len(clockTimes)-1]
		}
		current := clockTimes[clockIndex]
		clockIndex++
		return current
	}})

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "acme-users-sync",
			Name: "ACME Users Sync",
		},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: "fetch-users",
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: "acme-postgres",
				OperationType:  "sql",
				OperationRef:   "list-active-users",
			},
		}},
	}

	_, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-001",
		Pipeline:        pipeline,
	})
	require.NoError(t, err)

	firstReplay, err := runner.Replay(context.Background(), ReplayRequest{
		OrgSlug:       must.Slug("acme"),
		SourceRunSlug: "run-001",
		InputMode:     ReplayInputModePinned,
	})
	require.NoError(t, err)
	require.Regexp(t, `^run-001-replay-20260313100501-[a-z0-9]{4}$`, firstReplay.Metadata.RunSlug)

	secondReplay, err := runner.Replay(context.Background(), ReplayRequest{
		OrgSlug:       must.Slug("acme"),
		SourceRunSlug: "run-001",
		InputMode:     ReplayInputModePinned,
	})
	require.NoError(t, err)
	require.Regexp(t, `^run-001-replay-20260313100502-[a-z0-9]{4}$`, secondReplay.Metadata.RunSlug)
	require.NotEqual(t, firstReplay.Metadata.RunSlug, secondReplay.Metadata.RunSlug)
	require.Less(t, firstReplay.Metadata.RunSlug, secondReplay.Metadata.RunSlug)
}

func TestRunner_Replay_UsesCheckpointPipelineSnapshot(t *testing.T) {
	t.Parallel()

	clockTimes := []time.Time{
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 0, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 0, 125_000_000, time.UTC),
	}
	clockIndex := 0

	observedQuerySlugs := make([]slug.Slug, 0, 2)
	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		observedQuerySlugs = append(observedQuerySlugs, querySlug)
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"email": "user@example.com"}},
			ColumnNames: []string{"email"},
			RowCount:    1,
		}, 12, nil), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		if clockIndex >= len(clockTimes) {
			return clockTimes[len(clockTimes)-1]
		}
		if clockIndex >= len(clockTimes) {
			return clockTimes[len(clockTimes)-1]
		}
		current := clockTimes[clockIndex]
		clockIndex++
		return current
	}})

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "acme-users-sync",
			Name: "ACME Users Sync",
		},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: "fetch-users",
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: "acme-postgres",
				OperationType:  "sql",
				OperationRef:   "list-active-users",
				Parameters:     map[string]any{"limit": 100},
			},
		}},
	}

	_, err := runner.Run(
		context.Background(),
		RunRequest{OrgSlug: must.Slug("acme"), EnvironmentSlug: must.Slug("prod"), RunSlug: "run-001", Pipeline: pipeline},
	)
	require.NoError(t, err)

	// Mutate the original pipeline after checkpoint creation.
	pipeline.Steps[0].Query.OperationRef = "list-disabled-users"

	_, err = runner.Replay(context.Background(), ReplayRequest{
		OrgSlug:       must.Slug("acme"),
		SourceRunSlug: "run-001",
		ReplayRunSlug: "run-001-replay",
		FromStep:      "fetch-users",
		InputMode:     ReplayInputModePinned,
	})
	require.NoError(t, err)
	require.Equal(t, []slug.Slug{must.Slug("list-active-users")}, observedQuerySlugs)
}

func TestRunner_ReplayPersistsReplayMetadata(t *testing.T) {
	t.Parallel()

	clockTimes := []time.Time{
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 0, 125_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 5, 0, 125_000_000, time.UTC),
	}
	clockIndex := 0

	persistedResults := make([]pipelinerun.RunResult, 0, 2)
	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			return result.NewSQLResult(
				"postgres",
				&result.SQLResult{
					Rows:     []map[string]any{{"email": "user@example.com"}},
					RowCount: 1,
				},
				12,
				nil,
			), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			if clockIndex >= len(clockTimes) {
				return clockTimes[len(clockTimes)-1]
			}
			if clockIndex >= len(clockTimes) {
				return clockTimes[len(clockTimes)-1]
			}
			current := clockTimes[clockIndex]
			clockIndex++
			return current
		}},
		&fakeRunStore{persistRunFn: func(
			_ context.Context,
			_ slug.Slug,
			_ slug.Slug,
			_ slug.Slug,
			_ *pipelinespec.Pipeline,
			runResult *pipelinerun.RunResult,
		) error {
			if runResult.Metadata.Status != "running" {
				persistedResults = append(persistedResults, *runResult)
			}
			return nil
		}},
		runnerTestDataSourceLookup{},
	)

	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: "acme-users-sync",
			Name: "ACME Users Sync",
		},
		Steps: []pipelinespec.Step{{
			Kind: "query",
			Slug: "fetch-users",
			Query: &pipelinespec.QueryRef{
				DataSourceSlug: "acme-postgres",
				OperationType:  "sql",
				OperationRef:   "list-active-users",
				Parameters:     map[string]any{"limit": 100},
			},
		}},
	}

	_, err := runner.Run(
		context.Background(),
		RunRequest{OrgSlug: must.Slug("acme"), EnvironmentSlug: must.Slug("prod"), RunSlug: "run-001", Pipeline: pipeline},
	)
	require.NoError(t, err)

	_, err = runner.Replay(context.Background(), ReplayRequest{
		OrgSlug:       must.Slug("acme"),
		SourceRunSlug: "run-001",
		ReplayRunSlug: "run-001-replay",
		FromStep:      "fetch-users",
		InputMode:     ReplayInputModePinned,
	})
	require.NoError(t, err)
	require.Len(t, persistedResults, 2)
	require.Nil(t, persistedResults[0].Metadata.ReplayOfRunSlug)
	require.NotNil(t, persistedResults[1].Metadata.ReplayOfRunSlug)
	require.Equal(t, "run-001", *persistedResults[1].Metadata.ReplayOfRunSlug)
	require.NotNil(t, persistedResults[1].Metadata.ReplayFromStep)
	require.Equal(t, "fetch-users", *persistedResults[1].Metadata.ReplayFromStep)
	require.NotNil(t, persistedResults[1].Timeline.ReplayOfRunSlug)
	require.Equal(t, "run-001", *persistedResults[1].Timeline.ReplayOfRunSlug)
}

func TestRunner_ReplayErrors(t *testing.T) {
	t.Parallel()

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{}, &testutil.FakeClock{})

	tests := []struct {
		name    string
		request ReplayRequest
		wantErr error
	}{
		{
			name:    "MissingSourceRunSlug",
			request: ReplayRequest{OrgSlug: must.Slug("acme")},
			wantErr: ErrReplayRunSlugRequired,
		},
		{
			name: "UnknownSourceRun",
			request: ReplayRequest{
				OrgSlug:       must.Slug("acme"),
				SourceRunSlug: "missing",
				InputMode:     ReplayInputModePinned,
			},
			wantErr: ErrReplayRunNotFound,
		},
		{
			name: "UnsupportedInputMode",
			request: ReplayRequest{
				OrgSlug:       must.Slug("acme"),
				SourceRunSlug: "missing",
				InputMode:     ReplayInputMode(99),
			},
			wantErr: ErrReplayUnsupportedMode,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runResult, err := runner.Replay(context.Background(), tc.request)
			require.ErrorIs(t, err, tc.wantErr)
			require.Nil(t, runResult)
		})
	}
}

func TestRunner_ReplayFromStepMismatch(t *testing.T) {
	t.Parallel()

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		return result.NewSQLResult(
			"postgres",
			&result.SQLResult{Rows: []map[string]any{}, RowCount: 0},
			1,
			nil,
		), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC)
	}})

	_, err := runner.Run(
		context.Background(),
		RunRequest{OrgSlug: must.Slug("acme"), EnvironmentSlug: must.Slug("prod"), RunSlug: "run-001", Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "acme-users-sync",
				Name: "ACME Users Sync",
			},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-active-users",
				},
			}},
		}},
	)
	require.NoError(t, err)

	replayResult, replayErr := runner.Replay(context.Background(), ReplayRequest{
		OrgSlug:       must.Slug("acme"),
		SourceRunSlug: "run-001",
		FromStep:      "unknown-step",
		InputMode:     ReplayInputModePinned,
	})
	require.ErrorIs(t, replayErr, ErrReplayStepNotFound)
	require.Nil(t, replayResult)
}

func TestRunner_ReplayPinnedInputIsDeterministic(t *testing.T) {
	t.Parallel()

	clockTimes := []time.Time{
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 100_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 0, 0, 100_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 1, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 1, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 1, 0, 100_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 1, 0, 100_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 2, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 2, 0, 0, time.UTC),
		time.Date(2026, time.March, 13, 10, 2, 0, 100_000_000, time.UTC),
		time.Date(2026, time.March, 13, 10, 2, 0, 100_000_000, time.UTC),
	}
	clockIndex := 0

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		ctx context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"email": "user@example.com"}},
			ColumnNames: []string{"email"},
			RowCount:    1,
		}, 12, nil), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		if clockIndex >= len(clockTimes) {
			return clockTimes[len(clockTimes)-1]
		}
		current := clockTimes[clockIndex]
		clockIndex++
		return current
	}})

	_, err := runner.Run(
		context.Background(),
		RunRequest{OrgSlug: must.Slug("acme"), EnvironmentSlug: must.Slug("prod"), RunSlug: "run-001", Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "acme-users-sync",
				Name: "ACME Users Sync",
			},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-active-users",
					Parameters:     map[string]any{"limit": 100},
				},
			}},
		}},
	)
	require.NoError(t, err)

	firstReplay, err := runner.Replay(context.Background(), ReplayRequest{
		OrgSlug:       must.Slug("acme"),
		SourceRunSlug: "run-001",
		ReplayRunSlug: "replay-001",
		InputMode:     ReplayInputModePinned,
	})
	require.NoError(t, err)

	secondReplay, err := runner.Replay(context.Background(), ReplayRequest{
		OrgSlug:       must.Slug("acme"),
		SourceRunSlug: "run-001",
		ReplayRunSlug: "replay-002",
		InputMode:     ReplayInputModePinned,
	})
	require.NoError(t, err)

	firstResultJSON, err := json.Marshal(firstReplay.StepResults)
	require.NoError(t, err)
	secondResultJSON, err := json.Marshal(secondReplay.StepResults)
	require.NoError(t, err)
	require.JSONEq(t, string(firstResultJSON), string(secondResultJSON))

	firstSnapshotJSON, err := json.Marshal(firstReplay.Timeline.Snapshots)
	require.NoError(t, err)
	secondSnapshotJSON, err := json.Marshal(secondReplay.Timeline.Snapshots)
	require.NoError(t, err)
	require.JSONEq(t, string(firstSnapshotJSON), string(secondSnapshotJSON))
}

func TestRunner_ReplayQueryOnlyWithoutContractDoesNotRequireSchemaLookup(t *testing.T) {
	t.Parallel()

	runner := mustNewRunner(t, &testutil.FakeQueryExecutor{ExecuteFn: func(
		_ context.Context,
		orgSlug, dsSlug, querySlug slug.Slug,
		paramValues map[string]any,
	) (*result.ExecutionResult, error) {
		require.Equal(t, must.Slug("acme"), orgSlug)
		require.Equal(t, must.Slug("acme-postgres"), dsSlug)
		require.Equal(t, must.Slug("list-active-users"), querySlug)
		require.Empty(t, paramValues)
		return result.NewSQLResult("postgres", &result.SQLResult{
			Rows:        []map[string]any{{"email": "user@example.com"}},
			ColumnNames: []string{"email"},
			RowCount:    1,
		}, 1, nil), nil
	}}, &testutil.FakeClock{NowFn: func() time.Time {
		return time.Date(2026, time.March, 25, 10, 0, 0, 0, time.UTC)
	}})

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-query-only",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{Slug: "acme-users-sync", Name: "ACME Users Sync"},
			Steps: []pipelinespec.Step{{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: "acme-postgres",
					OperationType:  "sql",
					OperationRef:   "list-active-users",
				},
			}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	replayRun, err := runner.Replay(context.Background(), ReplayRequest{
		OrgSlug:       must.Slug("acme"),
		SourceRunSlug: "run-query-only",
		ReplayRunSlug: "run-query-only-replay",
		InputMode:     ReplayInputModePinned,
	})
	require.NoError(t, err)
	require.Equal(t, "succeeded", replayRun.Metadata.Status)
	require.NotNil(t, replayRun.Metadata.ReplayOfRunSlug)
	require.Equal(t, "run-query-only", *replayRun.Metadata.ReplayOfRunSlug)
}

// fakeTransformRuntime is a test double that lets tests control what the
// transform step returns without exercising the SQL execution path.
type fakeTransformRuntime struct {
	executeFn func(spec *pipelinespec.TransformSpec, upstream *payload.Payload) (*payload.Output, error)
}

func (f *fakeTransformRuntime) Execute(
	_ context.Context,
	_ slug.Slug,
	spec *pipelinespec.TransformSpec,
	upstreams map[string]*payload.Payload,
) (*payload.Output, error) {
	return f.executeFn(spec, firstRuntimePayload(upstreams))
}

func TestRunner_RunFailsWhenTransformOutputContractViolated(t *testing.T) {
	t.Parallel()

	t.Run("RowsFieldTypeMismatch", func(t *testing.T) {
		t.Parallel()

		// Cached integer input contradicts the declared string projection before execution.
		store := &fakeRunStore{}
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
				}, 5, nil), nil
			}},
			&testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.March, 20, 9, 0, 0, 0, time.UTC)
			}},
			store,
			newRunnerPreValidationMockService(),
		)
		runner.transformRuntime = &fakeTransformRuntime{
			executeFn: func(_ *pipelinespec.TransformSpec, _ *payload.Payload) (*payload.Output, error) {
				return &payload.Output{
					OutputKind:  model.TransformOutputKindRows,
					RowCount:    1,
					ColumnNames: []string{"user_id"},
					Rows:        []map[string]any{{"user_id": 42}}, // integer, not string
				}, nil
			},
		}

		contract := &payload.Contract{Fields: []payload.ContractField{{
			Name:     "user_id",
			Type:     model.ContractFieldTypeValueString,
			Required: true,
		}}}

		runResult, runErr := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "contract-test-rows",
			Pipeline: &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{
					Slug: "contract-check",
					Name: "Contract Check",
				},
				Steps: []pipelinespec.Step{
					{
						Kind: "query",
						Slug: "fetch-users",
						Query: &pipelinespec.QueryRef{
							DataSourceSlug: "acme-postgres",
							OperationType:  "sql",
							OperationRef:   "list-users",
							// Explicit contract is validated against cached schema.
							OutputContract: &payload.Contract{Fields: []payload.ContractField{{
								Name:     "id",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							}}},
						},
					},
					{
						Kind: "transform",
						Slug: "normalize",
						Transform: &pipelinespec.TransformSpec{
							Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id AS user_id FROM input",
							OutputKind:     model.TransformOutputKindRows,
							OutputContract: contract,
						},
					},
				},
			},
		})

		require.Error(t, runErr)
		require.ErrorIs(t, runErr, transformsql.ErrContractType)
		require.ErrorContains(t, runErr, "pre-validate transforms")
		require.NotNil(t, runResult)
		recorded := store.timelines["contract-test-rows"]
		require.Equal(t, "failed", recorded.Status)
		require.NotNil(t, recorded.ExecutionContext)
		require.NotNil(t, recorded.Failure)
		require.Equal(t, runResult.Timeline.Failure, recorded.Failure)
		require.Equal(t, slug.Slug("normalize"), recorded.Failure.Step)
		require.Equal(t, failureSideTransform, recorded.Failure.Side)
		require.Equal(t, pipelinerun.FailureReasonCodeTransformContractMismatch, recorded.Failure.ReasonCode)
		require.Equal(t, "PIPELINE_TRANSFORM_CONTRACT_TYPE_MISMATCH: transform output contract type mismatch", recorded.Failure.Error)
		require.Contains(t, recorded.Failure.ActionHint, "server-backed pipeline validation")
		require.Empty(t, recorded.Snapshots)
		for _, event := range recorded.Events {
			require.NotEqual(t, "step_started", event.Type)
		}
	})

	t.Run("DocumentMissingRequiredField", func(t *testing.T) {
		t.Parallel()

		// Contract requires the field "email" but the document returned by the runtime omits it.
		// The query step carries an explicit OutputContract that must match introspected schema.
		// The transform SQL projects email, so structural pre-validation passes.
		// Only the runtime validator catches the missing required field.
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
				}, 5, nil), nil
			}},
			&testutil.FakeClock{NowFn: func() time.Time {
				return time.Date(2026, time.March, 20, 9, 0, 0, 0, time.UTC)
			}},
			NewNoopRunStore(),
			newRunnerPreValidationMockService(),
		)
		runner.transformRuntime = &fakeTransformRuntime{
			executeFn: func(_ *pipelinespec.TransformSpec, _ *payload.Payload) (*payload.Output, error) {
				return &payload.Output{
					OutputKind: model.TransformOutputKindJSON,
					RowCount:   1,
					Document:   []byte(`{"name":"Alice"}`), // "email" is absent
				}, nil
			},
		}

		contract := &payload.Contract{Fields: []payload.ContractField{{
			Name:     "email",
			Type:     model.ContractFieldTypeValueString,
			Required: true,
		}}}

		runResult, runErr := runner.Run(context.Background(), RunRequest{
			OrgSlug:         must.Slug("acme"),
			EnvironmentSlug: must.Slug("prod"),
			RunSlug:         "contract-test-doc",
			Pipeline: &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{
					Slug: "contract-check-doc",
					Name: "Contract Check Doc",
				},
				Steps: []pipelinespec.Step{
					{
						Kind: "query",
						Slug: "fetch-users",
						Query: &pipelinespec.QueryRef{
							DataSourceSlug: "acme-postgres",
							OperationType:  "sql",
							OperationRef:   "list-users",
							// Explicit contract is validated against cached schema.
							OutputContract: &payload.Contract{Fields: []payload.ContractField{
								{
									Name:     "id",
									Type:     model.ContractFieldTypeValueInteger,
									Required: true,
								},
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
						Slug: "summarize",
						Transform: &pipelinespec.TransformSpec{
							Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT email FROM input",
							OutputKind:     model.TransformOutputKindJSON,
							OutputContract: contract,
						},
					},
				},
			},
		})

		require.Error(t, runErr)
		require.ErrorIs(t, runErr, transformsql.ErrOutputContractViolation)
		require.ErrorContains(t, runErr, "execute step \"summarize\"")
		require.NotNil(t, runResult)
		require.Equal(t, "failed", runResult.Metadata.Status)
		require.NotNil(t, runResult.Metadata.FailedStep)
		require.Equal(t, must.Slug("summarize"), *runResult.Metadata.FailedStep)
	})
}

func TestRunner_RunExecutesTransformFromTransformSource(t *testing.T) {
	t.Parallel()

	runner := mustNewRunnerWithStore(
		&testutil.FakeQueryExecutor{ExecuteFn: func(
			ctx context.Context,
			orgSlug, dsSlug, querySlug slug.Slug,
			paramValues map[string]any,
		) (*result.ExecutionResult, error) {
			require.Equal(t, must.Slug("acme"), orgSlug)
			require.Equal(t, must.Slug("acme-postgres"), dsSlug)
			require.Equal(t, must.Slug("list-users"), querySlug)
			require.Empty(t, paramValues)

			return result.NewSQLResult("postgres", &result.SQLResult{
				Rows: []map[string]any{
					{"id": 1, "email": "alice@example.com"},
					{"id": 2, "email": "bob@example.com"},
				},
				ColumnNames: []string{"id", "email"},
				RowCount:    2,
			}, 8, nil), nil
		}},
		&testutil.FakeClock{NowFn: func() time.Time {
			return time.Date(2026, time.March, 22, 12, 0, 0, 0, time.UTC)
		}},
		NewNoopRunStore(),
		newRunnerPreValidationMockService(),
	)

	runResult, err := runner.Run(context.Background(), RunRequest{
		OrgSlug:         must.Slug("acme"),
		EnvironmentSlug: must.Slug("prod"),
		RunSlug:         "run-transform-chain-001",
		Pipeline: &pipelinespec.Pipeline{
			Metadata: pipelinespec.Metadata{
				Slug: "users-sync",
				Name: "Users Sync",
			},
			Steps: []pipelinespec.Step{
				{
					Kind: "query",
					Slug: "fetch-users",
					Query: &pipelinespec.QueryRef{
						DataSourceSlug: "acme-postgres",
						OperationType:  "sql",
						OperationRef:   "list-users",
					},
				},
				{
					Kind: "transform",
					Slug: "project-users",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("fetch-users")}}, SQL: "SELECT id, email FROM input",
						InputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "id",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							},
							{
								Name:     "email",
								Type:     model.ContractFieldTypeValueString,
								Required: true,
							},
						}},
						OutputKind: model.TransformOutputKindRows,
						OutputContract: &payload.Contract{Fields: []payload.ContractField{
							{
								Name:     "id",
								Type:     model.ContractFieldTypeValueInteger,
								Required: true,
							},
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
					Slug: "extract-emails",
					Transform: &pipelinespec.TransformSpec{
						Inputs: map[string]pipelinespec.StepInput{"input": {FromStep: must.Slug("project-users")}}, SQL: "SELECT email FROM input",
						InputContract: &payload.Contract{Fields: []payload.ContractField{{
							Name:     "email",
							Type:     model.ContractFieldTypeValueString,
							Required: true,
						}}},
						OutputKind: model.TransformOutputKindRows,
					},
				},
			},
		},
	})

	require.NoError(t, err)
	require.Equal(t, "succeeded", runResult.Metadata.Status)

	transformStepResult, ok := runResult.StepResults[must.Slug("extract-emails")]
	require.True(t, ok)
	transformOutput, ok := transformStepResult.Transform()
	require.True(t, ok)
	require.Equal(t, model.TransformOutputKindRows, transformOutput.OutputKind)
	require.Equal(t, []map[string]any{
		{"email": "alice@example.com"},
		{"email": "bob@example.com"},
	}, transformOutput.Rows)
}

func TestDataSourceIssueSummary_SafeDetailBoundary(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "nil"},
		{name: "plain wrapped connector", err: xerror.Wrapf("execute query: %w", xerror.ConnectionFailed(xerror.DataSource)), want: "data source connection failed"},
		{name: "structured provider", err: xerror.Wrapf("execute query: %w", xerror.InvalidInput(xerror.Query).Detail(`rejected: [{"message":"invalid: customer@example.test","name":"email"}]`)), want: "query invalid input"},
		{name: "safe URLs and timestamps", err: xerror.Wrapf("execute query: %w", xerror.Unavailable(xerror.DataSource).WithPublicDetail("Endpoint https://example.test:8443 unavailable since 2026-08-05T08:49:25Z")), want: "Endpoint https://example.test:8443 unavailable since 2026-08-05T08:49:25Z"},
		{name: "malformed provider", err: xerror.InvalidInput(xerror.Query).Detail(`{"message": broken: "email"}]`), want: "query invalid input"},
		{name: "untyped secret and customer data", err: fmt.Errorf("Authorization: Bearer test-token; contact: customer@example.test; https://user:password@example.test:8443"), want: "data source execution failed"},
		{name: "typed secret and customer data", err: xerror.Unavailable(xerror.DataSource).Detail("headers: test-token; body: customer@example.test"), want: "data source unavailable"},
		{name: "credentials override public detail", err: xerror.InvalidCredentials().WithPublicDetail("unsafe credential detail"), want: "invalid credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, dataSourceIssueSummary(tc.err))
		})
	}
}

func TestRunner_PersistsSafeProviderFailureSummary(t *testing.T) {
	t.Parallel()

	const summary = "HubSpot request failed: query invalid input"
	providerErr := xerror.InvalidInput(xerror.Query).
		Detail(`rejected: [{"message":"customer@example.test: test-token","name":"email"}]`).
		WithPublicDetail(summary)
	for _, side := range []string{failureSideSource, failureSideSink} {
		t.Run(side, func(t *testing.T) {
			t.Parallel()
			storage := &fakeRunStorageService{}
			fakeClock := &testutil.FakeClock{}
			writer := &runWriter{notifier: testutil.FakeRunSucceededNotifier{}, txm: &fakeTransactionManager{}, clock: fakeClock, runStorageSvc: storage}
			store := &fakeRunStore{persistRunFn: writer.PersistRun}
			runner := mustNewRunnerWithStore(&testutil.FakeQueryExecutor{ExecuteFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, map[string]any) (*result.ExecutionResult, error) {
				if side == failureSideSource {
					return nil, xerror.Wrapf("execute query: %w", providerErr)
				}
				return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": 1}}, ColumnNames: []string{"id"}, RowCount: 1}, 0, nil), nil
			}}, fakeClock, store, newRunnerPreValidationMockService())
			runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error {
				return xerror.Wrapf("execute sink: %w", providerErr)
			}}
			pipeline := &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{Slug: "contacts-sync", Name: "Contacts Sync"},
				Steps:    []pipelinespec.Step{{Kind: "query", Slug: "read-contacts", Query: &pipelinespec.QueryRef{DataSourceSlug: "acme-postgres", OperationType: "sql", OperationRef: "read-contacts"}}},
			}
			if side == failureSideSink {
				pipeline.Steps[0].Query.OutputContract = &payload.Contract{Representation: model.ContractRepresentationRows, Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}
				pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "sink", Slug: "write-contacts", Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassUnsafe,
					Input:            &pipelinespec.StepInput{FromStep: "read-contacts"},
					DataSourceSlug:   "acme-postgres", OperationType: "sql", OperationRef: "write-contacts",
				}})
			}
			runResult, err := runner.Run(context.Background(), RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", RunSlug: "provider-failure", Pipeline: pipeline})
			require.ErrorIs(t, err, providerErr)
			require.NotNil(t, runResult)
			require.NotNil(t, runResult.Timeline.Failure)
			require.Equal(t, summary, runResult.Timeline.Failure.Error)
			require.Equal(t, side, runResult.Timeline.Failure.Side)
			require.Equal(t, failureKindDataSource, runResult.Timeline.Failure.Kind)
			require.Equal(t, "acme-postgres", runResult.Timeline.Failure.DataSourceSlug)
			require.NotEmpty(t, runResult.Timeline.Failure.ActionHint)
			require.NotNil(t, storage.lastCreateRunParams)
			require.Equal(t, summary, *storage.lastCreateRunParams.ErrorSummary)
			require.NotEqual(t, `"email"}]`, *storage.lastCreateRunParams.ErrorSummary)
			require.NotContains(t, string(storage.lastCreateRunParams.Timeline), "customer@example.test")
			require.NotContains(t, string(storage.lastCreateRunParams.Timeline), "test-token")
		})
	}
}

func (s *fakeRunStore) PreviousSuccess(ctx context.Context, org, project, environment, pipeline slug.Slug, version int64, before time.Time) (*executionruntime.SuccessfulRun, error) {
	if s.previousSuccessFn != nil {
		return s.previousSuccessFn(ctx, org, project, environment, pipeline, version, before)
	}
	return nil, pipelinerun.ErrTimelineNotFound
}

func TestS3FailureSummaryKeepsExplicitPublicDetailOnly(t *testing.T) {
	const private = "secret-token customer@example.test"
	raw := xerror.Unavailable(xerror.DataSource).Detail(private)
	require.Equal(t, "s3 object sink write failed", dataSourceFailureSummary(raw, pipelinerun.FailureReasonCodeS3ObjectWriteFailed))
	safe := raw.WithPublicDetail("S3 SlowDown: the provider could not complete the request; verify the write outcome before retrying.")
	require.Equal(t, xerror.PublicDetail(safe), dataSourceFailureSummary(xerror.Wrapf("execute sink: %w", safe), pipelinerun.FailureReasonCodeS3ObjectWriteFailed))
	require.NotContains(t, dataSourceFailureSummary(safe, pipelinerun.FailureReasonCodeS3ObjectWriteFailed), private)
	unknown := xerror.Unexpected().Detail("private provider message mentions password").WithPublicDetail("S3 request failed. No supported provider error code was available.")
	require.NotContains(t, dataSourceActionHint(unknown), "Authentication")
	for _, err := range []error{xerror.Forbidden(xerror.DataSource), xerror.NotFound(xerror.DataSource), xerror.Timeout(xerror.DataSource)} {
		require.NotContains(t, dataSourceActionHint(err), "unexpected error")
	}
}

func (s *fakeRunStore) ObserveWorker(ctx context.Context, org, project, environment slug.Slug, run string, at time.Time) error {
	if s.observeWorkerFn != nil {
		return s.observeWorkerFn(ctx, org, project, environment, run, at)
	}
	return nil
}
