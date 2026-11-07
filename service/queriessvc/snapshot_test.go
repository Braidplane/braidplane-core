package queriessvc_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/deploymentplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	runtimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/stretchr/testify/require"
)

type snapshotCaptureExecutor struct {
	captured  *queriessvc.ConnectorExecutionRequest
	remaining time.Duration
}

func TestScopedSnapshotPassesPolicyAndRejectsUnsupportedSource(t *testing.T) {
	t.Parallel()
	for _, sourceType := range []model.DataSourceType{model.DataSourceTypeS3, model.DataSourceTypePostgres} {
		ds := &fakes.DataSourceService{FindBySlugFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error) {
			return &datasourcessvc.DataSource{Slug: "store", Type: sourceType, IsActive: true, Config: json.RawMessage(`{}`)}, nil
		}}
		executor := &snapshotCaptureExecutor{}
		svc, err := queriessvc.NewWithExecutionLimits(&testutil.FakeTransactionManager{}, &testutil.FakeQueryRepository{}, ds, executor, newFakeCredentialService(), &testutil.FakeClock{}, testExecutionLimits(), eventtest.NewProducer(&fakeQueryEventRecorder{}))
		require.NoError(t, err)
		scope := &resourcescope.Scope{PolicyID: resourcescope.S3PolicyID, DataSource: "store", Operation: "read", Contract: "orders", Revision: "v1", ObjectID: "row", Representation: model.ContractRepresentationRows, S3: &resourcescope.S3{Endpoint: "https://storage.example", Region: "test", Bucket: "bucket", Prefix: "imports", DynamicKey: true}}
		content := contractplan.OperationContent{Data: json.RawMessage(`{"statement":"{\"type\":\"s3\",\"operation\":\"read\",\"key\":\"{{partition}}/orders.json\",\"format\":\"json\"}"}`), ResourceScope: scope, Parameters: []contractplan.OperationParameter{{Name: "partition", DataType: "text"}}}
		_, _, err = svc.ExecuteSnapshot(t.Context(), "acme", "store", content, map[string]any{"partition": "2026-10-18"}, nil)
		if sourceType == model.DataSourceTypeS3 {
			require.NoError(t, err)
			require.NotNil(t, executor.captured)
			require.Equal(t, scope, executor.captured.ResourceScope)
		} else {
			require.ErrorIs(t, err, resourcescope.ErrInvalid)
			require.Nil(t, executor.captured)
		}
	}
}

func (e *snapshotCaptureExecutor) ExecuteConnector(ctx context.Context, r queriessvc.ConnectorExecutionRequest) (*result.ExecutionResult, error) {
	e.captured = &r
	if deadline, ok := ctx.Deadline(); ok {
		e.remaining = time.Until(deadline)
	}
	return result.NewSQLResult("postgres", &result.SQLResult{Rows: []map[string]any{{"id": 1}}, ColumnNames: []string{"id"}, RowCount: 1}, 0, nil), nil
}

func TestSnapshotExecutionUsesCapturedDefaultsAndBudgetsWithoutSavedLookup(t *testing.T) {
	ds := &fakes.DataSourceService{FindBySlugFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error) {
		return &datasourcessvc.DataSource{Slug: "db", Type: model.DataSourceTypePostgres, IsActive: true, Config: json.RawMessage(`{}`)}, nil
	}}
	executor := &snapshotCaptureExecutor{}
	svc, err := queriessvc.NewWithExecutionLimits(&testutil.FakeTransactionManager{}, &testutil.FakeQueryRepository{}, ds, executor, newFakeCredentialService(), &testutil.FakeClock{}, testExecutionLimits(), eventtest.NewProducer(&fakeQueryEventRecorder{}))
	require.NoError(t, err)
	defaultValue := "7"
	snapshot := contractplan.OperationContent{Data: json.RawMessage(`{"statement":"{\"sql\":\"SELECT {{selection}}\"}","row_limit":3,"timeout_seconds":2}`), Parameters: []contractplan.OperationParameter{{Name: "selection", DataType: "integer", Default: &defaultValue}}}
	output, _, err := svc.ExecuteSnapshot(t.Context(), "acme", "db", snapshot, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, output)
	require.NotNil(t, executor.captured)
	require.JSONEq(t, `{"sql":"SELECT {{selection}}"}`, string(executor.captured.StatementJSON))
	require.Equal(t, "integer", executor.captured.Parameters["selection"].DataType)
	require.Equal(t, "7", executor.captured.Parameters["selection"].Value)
	require.Equal(t, 3, executor.captured.Limits.MaxResultRows)
	require.Positive(t, executor.remaining)
	require.LessOrEqual(t, executor.remaining, 2*time.Second)
	require.False(t, executor.captured.SinkExecution)
	_, _, err = svc.ExecuteSnapshot(runtimevars.WithSinkExecution(t.Context()), "acme", "db", snapshot, nil, nil)
	require.NoError(t, err)
	require.True(t, executor.captured.SinkExecution)
}

func TestAttemptDispatchRejectsConfigDriftAndRecordsOnlyLifecycleReference(t *testing.T) {
	t.Parallel()
	original := json.RawMessage(`{"host":"original","port":5432,"database":"orders"}`)
	changed := false
	ds := &fakes.DataSourceService{FindBySlugFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error) {
		config := original
		if changed {
			config = json.RawMessage(`{"host":"replacement","port":5432,"database":"orders"}`)
		}
		return &datasourcessvc.DataSource{Slug: "db", Type: model.DataSourceTypePostgres, IsActive: true, Config: config}, nil
	}}
	digest, err := connector.PublicConfigDigest(model.DataSourceTypePostgres, original)
	require.NoError(t, err)
	ctx := deploymentplan.WithDispatch(t.Context(), "acme", []deploymentplan.Target{{Step: "fetch", Role: "query", DataSource: "db", ConfigDigest: digest}})
	ctx = deploymentplan.WithStep(ctx, "fetch")
	executor := &snapshotCaptureExecutor{}
	credentials := newFakeCredentialService()
	credentials.defaultRuntime.Version = "credential/2026-10-18T12:00:00Z"
	svc, err := queriessvc.NewWithExecutionLimits(&testutil.FakeTransactionManager{}, &testutil.FakeQueryRepository{}, ds, executor, credentials, &testutil.FakeClock{}, testExecutionLimits(), eventtest.NewProducer(&fakeQueryEventRecorder{}))
	require.NoError(t, err)
	content := contractplan.OperationContent{Data: json.RawMessage(`{"statement":"{\"sql\":\"SELECT 1\"}"}`)}
	_, _, err = svc.ExecuteSnapshot(ctx, "acme", "db", content, nil, nil)
	require.NoError(t, err)
	require.Equal(t, digest, executor.captured.PinnedConfigDigest)
	credentials.defaultRuntime.Version = "credential/2026-10-18T13:00:00Z"
	credentials.defaultRuntime.PlaintextJSON = []byte(`{"password":"rotated-secret"}`)
	_, _, err = svc.ExecuteSnapshot(ctx, "acme", "db", content, nil, nil)
	require.NoError(t, err)
	admissions := deploymentplan.Admissions(ctx)
	require.Len(t, admissions, 2)
	require.NotEqual(t, admissions[0].CredentialReference, admissions[1].CredentialReference)
	encoded, err := json.Marshal(admissions)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret")
	executor.captured = nil
	_, err = svc.ExecuteInline(ctx, "acme", "db", map[string]any{"type": "postgres", "sql": "SELECT 1"}, nil)
	require.NoError(t, err)

	executor.captured = nil
	changed = true
	_, _, err = svc.ExecuteSnapshot(ctx, "acme", "db", content, nil, nil)
	require.ErrorIs(t, err, deploymentplan.ErrInvalid)
	require.Nil(t, executor.captured)
	require.Len(t, deploymentplan.Admissions(ctx), 3)
}

func TestPipelineIdentityAndHistoryReachSavedAndInlineConnectorBindings(t *testing.T) {
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, inline := range []bool{false, true} {
		ds := &fakes.DataSourceService{FindBySlugFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error) {
			return &datasourcessvc.DataSource{Slug: "db", Type: model.DataSourceTypePostgres, IsActive: true, Config: json.RawMessage(`{}`)}, nil
		}}
		executor := &snapshotCaptureExecutor{}
		svc, err := queriessvc.NewWithExecutionLimits(&testutil.FakeTransactionManager{}, &testutil.FakeQueryRepository{}, ds, executor, newFakeCredentialService(), &testutil.FakeClock{}, testExecutionLimits(), eventtest.NewProducer(&fakeQueryEventRecorder{}))
		require.NoError(t, err)
		ctx := runtimevars.WithExecutionContext(t.Context(), runtimevars.ExecutionContext{RunSlug: "attempt", At: at})
		sql := "SELECT {{runtime.run.slug}}, {{runtime.previous_success.exists}}, {{runtime.previous_success.finished_at}}"
		if inline {
			_, err = svc.ExecuteInline(ctx, "acme", "db", map[string]any{"type": "postgres", "sql": sql}, nil)
		} else {
			statement, encodeErr := json.Marshal(map[string]any{"sql": sql})
			require.NoError(t, encodeErr)
			data, encodeErr := json.Marshal(map[string]any{"statement": string(statement)})
			require.NoError(t, encodeErr)
			_, _, err = svc.ExecuteSnapshot(ctx, "acme", "db", contractplan.OperationContent{Data: data}, nil, nil)
		}
		require.NoError(t, err)
		require.NotNil(t, executor.captured)
		require.Equal(t, "attempt", executor.captured.Parameters[runtimevars.RunSlugParamName].Value)
		require.Equal(t, false, executor.captured.Parameters["runtime.previous_success.exists"].Value)
		require.Nil(t, executor.captured.Parameters["runtime.previous_success.finished_at"].Value)
		require.Contains(t, string(executor.captured.StatementJSON), "{{runtime.run.slug}}", "SQL keeps named bindings")
	}
}
