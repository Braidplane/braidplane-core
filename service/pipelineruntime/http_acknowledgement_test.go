package pipelineruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/stretchr/testify/require"
)

type acknowledgementDataSources struct {
	incrementalCheckpointDataSourceService
}

func (d acknowledgementDataSources) FindBySlug(ctx context.Context, org, source slug.Slug) (*datasourcessvc.DataSource, error) {
	if source == "warehouse" {
		return &datasourcessvc.DataSource{Type: model.DataSourceTypeHTTP}, nil
	}
	return d.incrementalCheckpointDataSourceService.FindBySlug(ctx, org, source)
}

func TestHTTPAcknowledgementControlsSinkEffectAndCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       error
	}{
		{"success", `{"status":{"code":"OK"}}`, nil},
		{"rejection", `{"status":{"code":"ERROR"}}`, xerror.HTTPAcknowledgementRejected()},
		{"uncertain", `{}`, xerror.HTTPAcknowledgementUncertain()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			connector, err := httpconnector.New(transportsecurity.WithPlaintext(t.Context()), httpconnector.Config{BaseURL: server.URL, AllowPrivateHosts: true}, httpconnector.Credentials{})
			require.NoError(t, err)
			sinkExecutor := &fakeSinkQueryExecutor{executeFn: func(ctx context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any, _ ...queriessvc.ExecuteOption) (*result.ExecutionResult, error) {
				return connector.Execute(ctx, json.RawMessage(`{"method":"POST","path":"/records","acknowledgement":{"status_codes":[200],"success":[{"path":"status.code","equals":"OK"}],"empty_fields":["errors"]}}`), nil)
			}}
			ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{Claimed: true, State: pipelinerun.SinkEffectStateInProgress}}
			sourceExecutor := &incrementalCheckpointQueryExecutor{executeWithCheckpointFn: func(_ context.Context, _, _, _ slug.Slug, _ map[string]any, input *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
				return successfulSQLResult(1), &pipelinerun.IncrementalCheckpointObservation{SourceStepSlug: "fetch-users", CheckpointNamespace: "normal", CheckpointKind: "watermark", ObservedLowerBound: input.LowerBound, ObservedUpperBound: json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`)}, nil
			}}
			store := newIncrementalCheckpointTestRunStore(nil)
			runner, err := NewRunner(sourceExecutor, nil, store, acknowledgementDataSources{}, NewSinkRuntime(sinkExecutor, ledger))
			require.NoError(t, err)
			pipeline := incrementalCheckpointPipeline()
			pipeline.Steps[1].Sink.OperationType = "http"
			_, err = runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", RunSlug: "ack-run", PipelineVersionID: 42, Pipeline: pipeline})
			require.Len(t, store.persistedRuns, 1)
			require.Len(t, store.persistedRuns[0].IncrementalCheckpointTransitions, 1)
			transition := store.persistedRuns[0].IncrementalCheckpointTransitions[0]
			if tc.want == nil {
				require.NoError(t, err)
				require.Equal(t, pipelinerun.SinkEffectStateSucceeded, ledger.effect.State)
				require.Equal(t, pipelinerun.IncrementalCheckpointTransitionCommitted, transition.Status)
			} else {
				require.ErrorIs(t, err, tc.want)
				require.Equal(t, pipelinerun.SinkEffectStateInProgress, ledger.effect.State)
				require.Zero(t, ledger.markCalls)
				require.Equal(t, pipelinerun.IncrementalCheckpointTransitionBlocked, transition.Status)
				require.Empty(t, transition.CommittingRunSlug)
				timeline := store.timelines["ack-run"]
				require.NotNil(t, timeline.Failure)
				require.True(t, timeline.Failure.AutomaticRetryProhibited)
				require.Contains(t, timeline.Failure.ActionHint, "Verify remote state")
			}
			require.Equal(t, 1, requests)
		})
	}
}

func TestHTTPNonSuccessStatusProducesSafeRunDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		status int
		label  string
	}{
		{http.StatusUnauthorized, "401 Unauthorized"},
		{http.StatusForbidden, "403 Forbidden"},
		{http.StatusServiceUnavailable, "503 Service Unavailable"},
		{599, "599"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.Header().Set("X-Private-Provider-Detail", "private-provider-header")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, "private-provider-body")
			}))
			defer server.Close()
			connector, err := httpconnector.New(transportsecurity.WithPlaintext(t.Context()), httpconnector.Config{BaseURL: server.URL, AllowPrivateHosts: true}, httpconnector.Credentials{})
			require.NoError(t, err)
			sinkExecutor := &fakeSinkQueryExecutor{executeFn: func(ctx context.Context, _, _, _ slug.Slug, _ model.EventActor, _ map[string]any, _ ...queriessvc.ExecuteOption) (*result.ExecutionResult, error) {
				return connector.Execute(ctx, json.RawMessage(`{"method":"POST","path":"/records"}`), nil)
			}}
			sourceExecutor := &incrementalCheckpointQueryExecutor{executeWithCheckpointFn: func(_ context.Context, _, _, _ slug.Slug, _ map[string]any, input *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
				return successfulSQLResult(1), &pipelinerun.IncrementalCheckpointObservation{SourceStepSlug: "fetch-users", CheckpointNamespace: "normal", CheckpointKind: "watermark", ObservedLowerBound: input.LowerBound, ObservedUpperBound: json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`)}, nil
			}}
			ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{Claimed: true, State: pipelinerun.SinkEffectStateInProgress}}
			store := newIncrementalCheckpointTestRunStore(nil)
			runner, err := NewRunner(sourceExecutor, nil, store, acknowledgementDataSources{}, NewSinkRuntime(sinkExecutor, ledger))
			require.NoError(t, err)
			pipeline := incrementalCheckpointPipeline()
			pipeline.Steps[1].Sink.OperationType = "http"
			_, err = runner.Run(t.Context(), RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", RunSlug: "http-status-run", PipelineVersionID: 42, Pipeline: pipeline})
			require.ErrorContains(t, err, "http sink returned non-2xx status")
			require.Len(t, store.persistedRuns, 1)
			failure := store.persistedRuns[0].Timeline.Failure
			require.NotNil(t, failure)
			require.Equal(t, "data_source", failure.Kind)
			require.Equal(t, "HTTP destination returned status "+tc.label+". Review the destination request settings and verify remote state before retrying.", failure.Error)
			require.NotContains(t, failure.Error, "private-provider")
			require.NotContains(t, failure.Error, server.URL)
			require.Equal(t, pipelinerun.SinkEffectStateInProgress, ledger.effect.State)
			require.Zero(t, ledger.markCalls)
			require.Equal(t, pipelinerun.IncrementalCheckpointTransitionBlocked, store.persistedRuns[0].IncrementalCheckpointTransitions[0].Status)
			require.Equal(t, 1, requests)
		})
	}
}
