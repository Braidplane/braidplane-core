package pipelineruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	"github.com/stretchr/testify/require"
)

func TestHTTPRecordUpdatePreflight(t *testing.T) {
	cfg := &pipelinespec.RecordUpdates{IdentityField: "id", Fields: map[string]string{"source": "name"}, MaxRecords: 2}
	first := map[string]any{"id": "first", "source": "expected", "unmapped": "private"}
	for _, second := range []map[string]any{
		{"source": "value"},
		{"id": "first", "source": "value"},
		{"id": "second"},
		{"id": "..", "source": "value"},
		{"id": "second", "source": strings.Repeat("x", maxRecordUpdateBytes)},
	} {
		rows, err := prepareHTTPRecordUpdates(t.Context(), []map[string]any{first, second}, cfg, 0)
		require.Error(t, err)
		require.Nil(t, rows)
	}
	_, err := prepareHTTPRecordUpdates(t.Context(), []map[string]any{first, first, first}, cfg, 0)
	require.ErrorContains(t, err, "max_records")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = prepareHTTPRecordUpdates(ctx, []map[string]any{first}, cfg, 0)
	require.ErrorIs(t, err, context.Canceled)
}

func TestHTTPRecordUpdatesRecoverSecondAcknowledgement(t *testing.T) {
	const prefix = "/v2/accounts/"
	state := map[string]string{}
	var requests []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		id := strings.TrimPrefix(r.URL.Path, prefix)
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		if r.Method == http.MethodPut {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 {
				http.Error(w, "unexpected body", 400)
				return
			}
			state[id] = body["name"]
			if len(requests) == 2 {
				panic(http.ErrAbortHandler)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		name := state[id]
		if r.Method == http.MethodGet && len(requests) == 3 {
			name = "different"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"recordKey": id, "name": name})
	}))
	defer server.Close()
	c, err := httpconnector.New(transportsecurity.WithPlaintext(t.Context()), httpconnector.Config{BaseURL: server.URL, AllowPrivateHosts: true}, httpconnector.Credentials{})
	require.NoError(t, err)
	executor := &inlineSinkExecutor{inline: func(ctx context.Context, _, _ slug.Slug, op, _ map[string]any) (*result.ExecutionResult, error) {
		data, err := json.Marshal(op)
		require.NoError(t, err)
		return c.Execute(ctx, data, nil)
	}}
	ledger := &recoveryLedger{effects: map[string]pipelinerun.SinkEffectState{}}
	firstName := "synthetic-first-value"
	source := &incrementalCheckpointQueryExecutor{executeWithCheckpointFn: func(_ context.Context, _, _, _ slug.Slug, _ map[string]any, input *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
		output := successfulSQLResult(2)
		rows, _ := output.SQL()
		rows.ColumnNames = append(rows.ColumnNames, "label")
		rows.Rows[0]["cursor"], rows.Rows[0]["label"] = "company/a", firstName
		rows.Rows[1]["cursor"], rows.Rows[1]["label"] = "company-b", "second"
		return output, &pipelinerun.IncrementalCheckpointObservation{
			SourceStepSlug: "fetch-users", CheckpointNamespace: input.Lookup.CheckpointNamespace, CheckpointKind: "watermark",
			ObservedLowerBound: input.LowerBound, ObservedUpperBound: json.RawMessage(`{"value":"2026-05-24T11:00:00Z"}`),
		}, nil
	}}
	store := newIncrementalCheckpointTestRunStore(nil)
	runner, err := NewRunner(source, nil, store, acknowledgementDataSources{}, NewSinkRuntime(executor, ledger))
	require.NoError(t, err)
	pipeline := incrementalCheckpointPipeline()
	sink := pipeline.Steps[1].Sink
	sink.OperationType, sink.OperationRef = "http", ""
	sink.OperationInline = map[string]any{"type": "http", "method": "PUT", "path": prefix + "{{runtime.sink.record_id}}", "body": "{{input}}"}
	sink.Config = map[string]any{"record_updates": map[string]any{"identity_field": "cursor", "readback": map[string]any{"identity_field": "recordKey"}, "fields": map[string]any{"label": "name"}, "max_records": 2}}
	request := RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", RunSlug: "original", PipelineVersionID: 42, Pipeline: pipeline}
	original, err := runner.Run(t.Context(), request)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "company-b")
	require.Equal(t, pipelinerun.IncrementalCheckpointTransitionBlocked, original.IncrementalCheckpointTransitions[0].Status)
	replay := ReplayRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "prod", SourceRunSlug: "original", ReplayRunSlug: "mismatch", PipelineVersionID: 42, InputMode: ReplayInputModePinned}
	_, err = runner.Recover(t.Context(), replay)
	require.Error(t, err, "a mismatched readback must keep the batch unconfirmed")
	replay.ReplayRunSlug = "matched"
	_, err = runner.Recover(t.Context(), replay)
	require.NoError(t, err)
	require.Equal(t, pipelinerun.IncrementalCheckpointTransitionCommitted, store.persistedRuns[len(store.persistedRuns)-1].IncrementalCheckpointTransitions[0].Status)
	require.Len(t, source.checkpointInputs, 1)
	request.RunSlug = "later-delivery"
	firstName = "synthetic-later-value"
	_, err = runner.Run(t.Context(), request)
	require.NoError(t, err)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{
		"PUT " + prefix + "company%2Fa", "PUT " + prefix + "company-b",
		"GET " + prefix + "company-b", "GET " + prefix + "company-b",
		"PUT " + prefix + "company%2Fa", "PUT " + prefix + "company-b",
	}, requests)
	require.Equal(t, map[string]string{"company/a": "synthetic-later-value", "company-b": "second"}, state)
	evidence, err := json.Marshal([]any{ledger.attempts, store.timelines})
	require.NoError(t, err)
	for _, private := range []string{"company/a", "company-b", "synthetic-first-value", "synthetic-later-value"} {
		require.NotContains(t, string(evidence), private)
	}
}

type cancelAfterConfirmedRecord struct {
	recoveryLedger
	cancel context.CancelFunc
}

func (l *cancelAfterConfirmedRecord) MarkSucceeded(ctx context.Context, org, project, environment slug.Slug, attempt pipelinerun.SinkEffectAttempt) error {
	err := l.recoveryLedger.MarkSucceeded(ctx, org, project, environment, attempt)
	l.cancel()
	return err
}

func TestRecordUpdatesCanceledAfterFirstConfirmation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ledger := &cancelAfterConfirmedRecord{recoveryLedger: recoveryLedger{effects: map[string]pipelinerun.SinkEffectState{}}, cancel: cancel}
	writes := 0
	executor := &inlineSinkExecutor{inline: func(context.Context, slug.Slug, slug.Slug, map[string]any, map[string]any) (*result.ExecutionResult, error) {
		writes++
		return result.NewHTTPResult(&result.HTTPResult{StatusCode: http.StatusOK}, 0, nil), nil
	}}
	input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassDeduplicatedByLedger)
	input.SinkBackend, input.SinkOperationType, input.SinkOperationRef = model.SinkBackendHTTP, "http", ""
	input.SinkOperationInline = map[string]any{"method": "PATCH", "path": "/items/{{runtime.sink.record_id}}", "body": "{{input}}"}
	input.SinkConfig = map[string]any{"record_updates": map[string]any{"identity_field": "cursor", "readback": map[string]any{"identity_field": "key"}, "fields": map[string]any{"updated_at": "lastSync"}, "max_records": 2}}
	input.SourceResult = pipelinerun.NewStepResult(successfulSQLResult(2))
	err := NewSinkRuntime(executor, ledger).Execute(ctx, input)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, writes)
	require.Equal(t, "unknown", input.DeliveryOutcome)
}
