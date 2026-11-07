package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/model"
	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testHTTPWatermarkCommittedAndBlockedCheckpoints(t *testing.T, stack *testStack) {
	org, _, _ := createOrgUserAndPostgresDataSource(t, stack, "HTTP Timestamp Checkpoints")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var mu sync.Mutex
	run := 0
	target := map[int]string{}
	lowerBounds := [][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			_, _ = fmt.Fprint(w, `{}`)
		case "/items":
			lower := r.URL.Query().Get("updated_since")
			lowerBounds[run] = append(lowerBounds[run], lower)
			if run <= 1 {
				assert.False(t, r.URL.Query().Has("updated_since"))
			} else {
				expectedLower := "2026-05-24T09:55:00Z"
				if run >= 4 {
					expectedLower = "2026-05-24T10:55:00Z"
				}
				assert.Equal(t, expectedLower, lower)
			}
			records := []map[string]any{
				{"id": 1, "updated_at": "2026-05-24T10:00:00Z"},
				{"id": 2, "updated_at": "2026-05-24T10:00:00Z"},
			}
			if run > 1 {
				records[1]["updated_at"] = "2026-05-24T11:00:00Z"
			}
			if run == 4 {
				records = append(records, map[string]any{"id": 3, "updated_at": "2026-05-24T10:58:00Z"})
			}
			if run == 0 || run == 5 {
				records = nil
			}
			selected := make([]map[string]any, 0)
			for _, record := range records {
				timestamp, err := time.Parse(time.RFC3339Nano, record["updated_at"].(string))
				assert.NoError(t, err)
				since, err := time.Parse(time.RFC3339Nano, lower)
				if lower != "" {
					assert.NoError(t, err)
					if timestamp.Before(since) {
						continue
					}
				}
				selected = append(selected, record)
			}
			offset := 0
			if after := r.URL.Query().Get("after"); after != "" {
				assert.Equal(t, "page-2", after)
				offset = 1
			}
			var next any
			batch := selected
			if len(selected) > 0 {
				batch = selected[offset : offset+1]
				if offset+1 < len(selected) {
					next = "page-2"
				}
			}
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"items": batch, "next": next}))

		case "/upsert":
			if run == 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprint(w, `{"ok":false}`)
				return
			}
			var body struct {
				Items []struct {
					ID        int    `json:"id"`
					UpdatedAt string `json:"updated_at"`
				} `json:"items"`
			}
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			for _, item := range body.Items {
				target[item.ID] = item.UpdatedAt
			}
			_, _ = fmt.Fprint(w, `{"ok":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	ds := createHTTPDataSource(t, stack, org, server.URL)
	query := createQuery(t, stack, org, ds, "Timestamp Source", map[string]any{
		"type": "http", "method": "GET", "path": "/items",
		"query_params": map[string]any{"updated_since": "{{runtime.incremental.lower_bound.value}}", "updated_until": "{{runtime.incremental.upper_bound.value}}"},
		"pagination":   map[string]any{"cursor_param": "after", "records_path": "items", "next_cursor_path": "next", "max_pages": 3, "timeout_seconds": 5},
	}, 30, nil)
	sink := createQuery(t, stack, org, ds, "Idempotent Target", map[string]any{
		"type": "http", "method": "POST", "path": "/upsert", "body": "{{input}}",
		"headers":         map[string]any{"Idempotency-Key": "{{runtime.sink.idempotency_key}}"},
		"acknowledgement": map[string]any{"status_codes": []int{200}, "success": []any{map[string]any{"path": "ok", "equals": true}}},
	}, 30, nil)
	contract := &payload.Contract{Name: "items", Representation: model.ContractRepresentationJSON, RootShape: model.ContractRootShapeObject, Fields: []payload.ContractField{
		{Name: "items[].updated_at", Type: model.ContractFieldTypeValueDatetime, Required: true},
	}}
	pipeline := incrementalWatermarkSourceOnlyPipeline(must.Slug("http-timestamp-checkpoints"), "HTTP timestamps", ds, query, "http", "items[].updated_at", nil, contract)
	pipeline.Steps[0].Query.Incremental.LookbackWindow = "5m"
	pipeline.Steps = append(pipeline.Steps, pipelinespec.Step{Kind: "sink", Slug: must.Slug("persist-users"), Sink: &pipelinespec.SinkSpec{
		Input: &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")}, InputContract: contract, DataSourceSlug: ds, OperationType: "http", OperationRef: sink, IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
	}})
	runner := newE2EPipelineRunner(t, stack)
	for index := 0; index <= 5; index++ {
		mu.Lock()
		run = index
		lowerBounds = append(lowerBounds, nil)
		mu.Unlock()
		output, err := runner.Run(ctx, newPipelineRunRequest(t, stack, org, fmt.Sprintf("http-timestamp-%d", index), pipeline))
		if index == 2 {
			require.Error(t, err)
			require.Equal(t, "failed", output.Metadata.Status)
		} else {
			require.NoError(t, err)
			require.Equal(t, "succeeded", output.Metadata.Status)
		}
		require.Len(t, output.IncrementalCheckpointTransitions, 1)
		upper, status := currentConnectorE2EIncrementalCheckpoint(t, stack.pool, org, defaultProjectSlug, pipeline.Metadata.Slug, must.Slug("fetch-users"))
		require.Equal(t, "committed", status)
		expected := `{"value":"2026-05-24T10:00:00Z"}`
		if index == 0 {
			expected = `null`
		}
		if index >= 3 {
			expected = `{"value":"2026-05-24T11:00:00Z"}`
		}
		require.JSONEq(t, expected, string(upper))
		if index == 2 {
			var blocked int
			err = stack.pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_incremental_checkpoints WHERE pipeline_slug=$1 AND commit_status='blocked'`, pipeline.Metadata.Slug.String()).Scan(&blocked)
			require.NoError(t, err)
			require.Equal(t, 1, blocked)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, [][]string{{""}, {"", ""}, {"2026-05-24T09:55:00Z", "2026-05-24T09:55:00Z"}, {"2026-05-24T09:55:00Z", "2026-05-24T09:55:00Z"}, {"2026-05-24T10:55:00Z", "2026-05-24T10:55:00Z"}, {"2026-05-24T10:55:00Z"}}, lowerBounds)
	require.Len(t, target, 3)
	require.Equal(t, "2026-05-24T10:58:00Z", target[3])
	require.Equal(t, "2026-05-24T11:00:00Z", target[2])
}
