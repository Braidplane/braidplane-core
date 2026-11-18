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
		rows, err := prepareHTTPRecordUpdates(t.Context(), []map[string]any{first, second}, cfg)
		require.Error(t, err)
		require.Nil(t, rows)
	}
	_, err := prepareHTTPRecordUpdates(t.Context(), []map[string]any{first, first, first}, cfg)
	require.ErrorContains(t, err, "max_records")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = prepareHTTPRecordUpdates(ctx, []map[string]any{first}, cfg)
	require.ErrorIs(t, err, context.Canceled)
}

func TestHTTPRecordUpdatesRecoverSecondAcknowledgement(t *testing.T) {
	const prefix = "/crm/v3/objects/companies/"
	state := map[string]string{}
	var requests []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		id := strings.TrimPrefix(r.URL.Path, prefix)
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		if r.Method == http.MethodPatch {
			var body struct {
				Properties map[string]string `json:"properties"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Properties) != 1 {
				http.Error(w, "unexpected body", 400)
				return
			}
			state[id] = body.Properties["name"]
			if len(requests) == 2 {
				panic(http.ErrAbortHandler)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		name := state[id]
		if r.Method == http.MethodGet && len(requests) == 3 {
			name = "different"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "properties": map[string]string{"name": name}})
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
	sink.OperationInline = map[string]any{"type": "http", "method": "PATCH", "path": prefix + "{{record_id}}", "body": map[string]any{"properties": "{{input}}"}}
	sink.Config = map[string]any{"record_updates": map[string]any{"identity_field": "cursor", "fields": map[string]any{"label": "name"}, "max_records": 2}}
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
		"PATCH " + prefix + "company%2Fa", "PATCH " + prefix + "company-b",
		"GET " + prefix + "company-b", "GET " + prefix + "company-b",
		"PATCH " + prefix + "company%2Fa", "PATCH " + prefix + "company-b",
	}, requests)
	require.Equal(t, map[string]string{"company/a": "synthetic-later-value", "company-b": "second"}, state)
	evidence, err := json.Marshal([]any{ledger.attempts, store.timelines})
	require.NoError(t, err)
	for _, private := range []string{"company/a", "company-b", "synthetic-first-value", "synthetic-later-value"} {
		require.NotContains(t, string(evidence), private)
	}
}
