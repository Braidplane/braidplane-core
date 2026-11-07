package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	climodel "github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/pipelineruns"
	"github.com/Braidplane/braidplane-core/postgres/pipelinesinkeffects"
	"github.com/Braidplane/braidplane-core/service/pipelineruntime"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPNextLinkRetryLedgerAndCheckpoint(t *testing.T) {
	if testing.Short() {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	limits := e2eExecutionLimits()
	limits.HTTPRetryAttempts = 2
	limits.HTTPRetryBackoff = time.Millisecond
	stack, err := newTestStack(ctx, limits)
	require.NoError(t, err)
	defer stack.pool.Close()
	defer func() { require.NoError(t, stack.runtimePool.Close()) }()
	org, _, _ := createOrgUserAndPostgresDataSource(t, stack, "HTTP Retry Ledger")
	var mu sync.Mutex
	sourceCalls, writeCalls, logicalEffects := 0, 0, 0
	failWrites := false
	effectBodies := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			_, _ = fmt.Fprint(w, `{}`)
		case "/items":
			sourceCalls++
			if r.URL.Query().Get("page") == "2" {
				if sourceCalls == 2 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(429)
					return
				}
				_, _ = fmt.Fprint(w, `{"items":[{"updated_at":"2026-05-24T11:00:00Z"}],"next":null}`)
			} else {
				query := r.URL.Query()
				query.Set("page", "2")
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"items": []map[string]string{{"updated_at": "2026-05-24T10:00:00Z"}}, "next": "?" + query.Encode()}))
			}
		case "/records":
			writeCalls++
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			key := r.Header.Get("Idempotency-Key")
			assert.NotEmpty(t, key)
			if previous, exists := effectBodies[key]; exists {
				assert.Equal(t, previous, string(body))
			} else {
				effectBodies[key] = string(body)
				logicalEffects++
			}
			if failWrites {
				w.WriteHeader(503)
				return
			}
			if writeCalls == 1 {
				conn, _, err := w.(http.Hijacker).Hijack()
				assert.NoError(t, err)
				_ = conn.Close()
				return
			}
			_, _ = fmt.Fprint(w, `{"ok":true}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	ds := createHTTPDataSource(t, stack, org, server.URL)
	source := createQuery(t, stack, org, ds, "Next-link Source", map[string]any{
		"type": "http", "method": "GET", "path": "/items",
		"query_params": map[string]any{"updated_since": "{{runtime.incremental.lower_bound.value}}", "updated_until": "{{runtime.incremental.upper_bound.value}}"},
		"pagination":   map[string]any{"next_link_path": "next", "records_path": "items", "max_pages": 3, "timeout_seconds": 5},
	}, 30, nil)
	sink := createQuery(t, stack, org, ds, "Keyed Sink", map[string]any{
		"type": "http", "method": "POST", "path": "/records", "body": "{{input}}",
		"headers":         map[string]any{"Idempotency-Key": "{{runtime.sink.idempotency_key}}"},
		"write_retry":     map[string]any{"verified": true, "provider_contract_url": "https://provider.invalid/idempotency", "key_header": "Idempotency-Key", "retention_seconds": 3600, "payload_conflict": "reject"},
		"acknowledgement": map[string]any{"status_codes": []int{200}, "success": []any{map[string]any{"path": "ok", "equals": true}}},
	}, 30, nil)
	contract := &payload.Contract{Name: "items", Representation: climodel.ContractRepresentationJSON, RootShape: climodel.ContractRootShapeObject, Fields: []payload.ContractField{{Name: "items[].updated_at", Type: climodel.ContractFieldTypeValueDatetime, Required: true}}}
	pipeline := incrementalWatermarkSourceOnlyPipeline(must.Slug("http-next-link-retries"), "HTTP retries", ds, source, "http", "items[].updated_at", nil, contract)
	pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "sink", Slug: must.Slug("write-records"), Sink: &pipelinespec.SinkSpec{Input: &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")}, InputContract: contract, DataSourceSlug: ds, OperationType: "http", OperationRef: sink, IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey}})
	txm := postgres.NewTransactionManager(stack.pool)
	ledger, err := pipelinesvc.NewSinkEffectLedger(txm, pipelinesinkeffects.New())
	require.NoError(t, err)
	runner, err := pipelineruntime.NewRunner(runnerQueryExecutorAdapter{executor: stack.queriesSvc}, clock.New(), pipelineruntime.NewRunStore(txm, stack.pool, clock.New(), testutil.FakeRunSucceededNotifier{}, pipelineruns.New()), stack.datasourcesSvc, pipelineruntime.NewSinkRuntime(stack.queriesSvc, ledger))
	require.NoError(t, err)
	firstRequest := newPipelineRunRequest(t, stack, org, "retry-confirmed", pipeline)
	first, err := runner.Run(ctx, firstRequest)
	require.NoError(t, err)
	require.Equal(t, "succeeded", first.Metadata.Status)
	mu.Lock()
	require.Equal(t, 3, sourceCalls)
	require.Equal(t, 2, writeCalls)
	require.Equal(t, 1, logicalEffects)
	failWrites = true
	mu.Unlock()
	failedRequest := newPipelineRunRequest(t, stack, org, "retry-unconfirmed", pipeline)
	failed, err := runner.Run(ctx, failedRequest)
	require.Error(t, err)
	require.Equal(t, "failed", failed.Metadata.Status)
	var state string
	var started time.Time
	err = stack.pool.QueryRow(ctx, `SELECT state,started_at FROM pipeline_sink_effects WHERE run_slug=$1`, failedRequest.RunSlug).Scan(&state, &started)
	require.NoError(t, err)
	require.Equal(t, "in_progress", state)
	upper, status := currentConnectorE2EIncrementalCheckpoint(t, stack.pool, org, defaultProjectSlug, pipeline.Metadata.Slug, must.Slug("fetch-users"))
	require.Equal(t, "committed", status)
	require.JSONEq(t, `{"value":"2026-05-24T11:00:00Z"}`, string(upper))
	var blocked int
	require.NoError(t, stack.pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_incremental_checkpoints WHERE pipeline_slug=$1 AND commit_status='blocked'`, pipeline.Metadata.Slug.String()).Scan(&blocked))
	require.Equal(t, 1, blocked)
	mu.Lock()
	failWrites = false
	beforeRecoverySourceCalls := sourceCalls
	beforeRecoveryEffects := logicalEffects
	mu.Unlock()
	recovered, err := runner.Recover(ctx, pipelineruntime.ReplayRequest{OrgSlug: org, ProjectSlug: defaultProjectSlug, EnvironmentSlug: must.Slug("prod"), SourceRunSlug: failedRequest.RunSlug, ReplayRunSlug: "retry-recovery", InputMode: pipelineruntime.ReplayInputModePinned, PipelineVersionID: failedRequest.PipelineVersionID})
	require.NoError(t, err)
	require.Equal(t, "succeeded", recovered.Metadata.Status)
	var afterStarted time.Time
	require.NoError(t, stack.pool.QueryRow(ctx, `SELECT state,started_at FROM pipeline_sink_effects WHERE run_slug=$1`, failedRequest.RunSlug).Scan(&state, &afterStarted))
	require.Equal(t, "succeeded", state)
	require.Equal(t, started, afterStarted)
	mu.Lock()
	require.Equal(t, beforeRecoverySourceCalls, sourceCalls)
	require.Equal(t, beforeRecoveryEffects, logicalEffects)
	mu.Unlock()
}
