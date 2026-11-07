package pipelineruntime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestRunnerPersistsRecognizedSourceFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cause   *xerror.Error
		reason  pipelinerun.FailureReasonCode
		summary string
		hint    string
	}{
		{"invalid query", xerror.SourceQueryInvalid(), pipelinerun.FailureReasonCodeSourceQueryInvalid, "The source query is invalid.", "query syntax"},
		{"schema reference", xerror.SourceSchemaReferenceInvalid(), pipelinerun.FailureReasonCodeSourceSchemaReferenceInvalid, "The source query references an unavailable schema object.", "column, relation and schema"},
		{"permission", xerror.Forbidden(xerror.DataSource), pipelinerun.FailureReasonCodeDataSourcePermissionDenied, "The data source denied permission for the selected operation.", "permission for the selected operation"},
		{"authentication", xerror.InvalidCredentials(), pipelinerun.FailureReasonCodeDataSourceCredentialsFailed, "invalid credentials", "Verify the credentials"},
		{"connection", xerror.ConnectionFailed(xerror.DataSource), pipelinerun.FailureReasonCodeDataSourceConnectionFailed, "The data source endpoint could not be reached.", "could not be reached"},
		{"timeout", xerror.Timeout(xerror.DataSource), pipelinerun.FailureReasonCodeDataSourceTimeout, "The data source operation timed out.", "timed out"},
		{"query timeout", xerror.Timeout(xerror.Query), pipelinerun.FailureReasonCodeDataSourceTimeout, "The data source operation timed out.", "timed out"},
		{"unknown", xerror.Unavailable(xerror.DataSource), pipelinerun.FailureReasonCodeSourceQueryFailed, "data source unavailable", "unexpected error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const private = "permission denied password=synthetic-secret https://private.test SELECT private"
			cause := tc.cause.Detail(private)
			storage := &fakeRunStorageService{}
			clock := &testutil.FakeClock{}
			writer := &runWriter{notifier: testutil.FakeRunSucceededNotifier{}, txm: &fakeTransactionManager{}, clock: clock, runStorageSvc: storage}
			runner := mustNewRunnerWithStore(&testutil.FakeQueryExecutor{ExecuteFn: func(context.Context, slug.Slug, slug.Slug, slug.Slug, map[string]any) (*result.ExecutionResult, error) {
				return nil, xerror.Wrapf("execute query: %w", cause)
			}}, clock, &fakeRunStore{persistRunFn: writer.PersistRun}, newRunnerPreValidationMockService())
			pipeline := &pipelinespec.Pipeline{
				Metadata: pipelinespec.Metadata{Slug: "orders", Name: "Orders"},
				Steps:    []pipelinespec.Step{{Kind: "query", Slug: "read-orders", Query: &pipelinespec.QueryRef{DataSourceSlug: "reader", OperationType: "sql", OperationRef: "select-orders"}}},
			}
			_, err := runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "dev", RunSlug: "source-failure", Pipeline: pipeline})
			require.ErrorIs(t, err, cause)
			require.NotNil(t, storage.lastCreateRunParams)
			require.Equal(t, slug.Slug("acme"), storage.lastCreateRunOrgSlug)
			encoded := storage.lastCreateRunParams.Timeline
			require.NotContains(t, string(encoded), "synthetic-secret")
			require.NotContains(t, string(encoded), "private.test")
			require.NotContains(t, string(encoded), "SELECT private")
			var stored pipelinerun.RunTimeline
			require.NoError(t, json.Unmarshal(encoded, &stored))
			require.Equal(t, tc.reason, stored.Failure.ReasonCode)
			require.Equal(t, tc.summary, stored.Failure.Error)
			require.Contains(t, stored.Failure.ActionHint, tc.hint)
			require.Equal(t, "reader", stored.Failure.DataSourceSlug)
			require.Equal(t, slug.Slug("read-orders"), stored.Failure.Step)
			require.Equal(t, "source", stored.Failure.Side)
			require.Equal(t, tc.summary, *storage.lastCreateRunParams.ErrorSummary)
		})
	}
}
