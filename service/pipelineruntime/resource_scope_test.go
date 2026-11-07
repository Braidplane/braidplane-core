package pipelineruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	s3connector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

func TestResourceScopeRejectionPersistsWithoutProviderOrSinkEffects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, key, bucket string }{
		{name: "traversal", key: "../private"},
		{name: "absolute", key: "/private"},
		{name: "ambiguous", key: "%2e%2e/private"},
		{name: "bucket substitution", key: "allowed.json", bucket: "private-bucket"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var providerCalls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				providerCalls.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer provider.Close()
			conn, err := s3connector.New(transportsecurity.WithPlaintext(t.Context()), s3connector.Config{Endpoint: provider.URL, Region: "test", Bucket: "fixed-bucket", Prefix: "imports", ForcePathStyle: true, AllowPrivateHosts: true}, s3connector.Credentials{AccessKeyID: "test", SecretAccessKey: "test"})
			require.NoError(t, err)
			defer func() { require.NoError(t, conn.Close()) }()
			scope := resourcescope.Scope{PolicyID: resourcescope.S3PolicyID, DataSource: "source", Operation: "orders", Contract: "orders", Revision: "v1", ObjectID: "row", Representation: model.ContractRepresentationRows, S3: &resourcescope.S3{Endpoint: provider.URL, Region: "test", Bucket: "fixed-bucket", Prefix: "imports", DynamicKey: true, ForcePathStyle: true}}
			executor := &testutil.FakeQueryExecutor{ExecuteFn: func(ctx context.Context, _, _, _ slug.Slug, _ map[string]any) (*result.ExecutionResult, error) {
				params := map[string]types.TypedParam{"key": {Value: tc.key}}
				if tc.bucket != "" {
					params["bucket"] = types.TypedParam{Value: tc.bucket}
				}
				statement := json.RawMessage(`{"type":"s3","operation":"read","key":"{{key}}","format":"json"}`)
				if tc.bucket != "" {
					statement = json.RawMessage(`{"type":"s3","operation":"read","key":"{{key}}","bucket":"{{bucket}}","format":"json"}`)
				}
				return conn.ExecuteScoped(ctx, statement, params, scope)
			}}
			store := &fakeRunStore{}
			runner := mustNewRunnerWithStore(executor, &testutil.FakeClock{NowFn: func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }}, store, runnerTestDataSourceLookup{})
			priorSink := "prior sink output"
			runner.sinkRuntime = &fakeSinkRuntime{executeFn: func(context.Context, *SinkRuntimeInput) error { priorSink = "changed"; return nil }}
			runResult, err := runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "etl", EnvironmentSlug: "staging", RunSlug: "scope-rejection", Pipeline: &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"}, Steps: []pipelinespec.Step{
				{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OperationType: "s3", OperationRef: "orders"}},
				{Kind: "sink", Slug: "store", Sink: &pipelinespec.SinkSpec{IdempotencyClass: pipelinespec.SinkIdempotencyClassDeduplicatedByLedger, Input: &pipelinespec.StepInput{FromStep: "fetch"}, InputContract: &payload.Contract{Fields: []payload.ContractField{{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true}}}, DataSourceSlug: "destination", OperationType: "sql", OperationRef: "save"}},
			}}})
			require.ErrorIs(t, err, resourcescope.ErrInvalid)
			require.NotNil(t, runResult)
			stored, err := store.Get(t.Context(), "acme", "etl", "staging", "scope-rejection")
			require.NoError(t, err)
			storage := &fakeRunStorageService{}
			writer := &runWriter{notifier: testutil.FakeRunSucceededNotifier{}, txm: &fakeTransactionManager{}, clock: runner.clock, runStorageSvc: storage}
			require.NoError(t, writer.PersistRun(t.Context(), "acme", "etl", "staging", &pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: "orders"}}, runResult))
			require.NotNil(t, storage.lastCreateRunParams)
			raw := storage.lastCreateRunParams.Timeline
			require.Equal(t, stored.Failure.Error, *storage.lastCreateRunParams.ErrorSummary)
			var restored pipelinerun.RunTimeline
			require.NoError(t, json.Unmarshal(raw, &restored))
			require.NotNil(t, restored.Failure)
			require.Equal(t, "fetch", restored.Failure.Step.String())
			require.Equal(t, failureSideSource, restored.Failure.Side)
			require.Equal(t, pipelinerun.FailureReasonCodeResourceScopeRejected, restored.Failure.ReasonCode)
			require.Equal(t, resourcescope.ErrInvalid.Message(), restored.Failure.Error)
			require.Equal(t, "The resource scope rejected this step. Use an unambiguous relative object key within the pinned scope, without traversal or absolute paths. Keep the bucket and target settings fixed to the pinned scope; review the selected operation and scope before retrying.", restored.Failure.ActionHint)
			require.NotContains(t, string(raw), tc.key)
			require.NotContains(t, string(raw), "private-bucket")
			require.Zero(t, providerCalls.Load())
			require.Equal(t, "prior sink output", priorSink)
		})
	}
}

func TestSinkResourceScopeRejectionKeepsSafeCategory(t *testing.T) {
	t.Parallel()
	err := resourcescope.ErrInvalid.Detail("private selector")
	code := sinkFailureReasonCode(err, pipelinespec.Step{Sink: &pipelinespec.SinkSpec{OperationType: "s3"}}, failureKindDataSource)
	require.Equal(t, pipelinerun.FailureReasonCodeResourceScopeRejected, code)
	require.Equal(t, resourcescope.ErrInvalid.Message(), dataSourceFailureSummary(err, code))
	require.NotContains(t, dataSourceActionHint(err), "credentials")
}
