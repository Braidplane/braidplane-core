package http

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

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	runtimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	writeRetryPolicy    = `{"verified":true,"provider_contract_url":"https://provider.invalid/idempotency","key_header":"Idempotency-Key","retention_seconds":3600,"payload_conflict":"reject"}`
	writeRetryOperation = `{"method":"POST","path":"/records","headers":{"Idempotency-Key":"{{runtime.sink.idempotency_key}}"},"body":"{{input}}","write_retry":` + writeRetryPolicy + `}`
)

func writeRetryParams() map[string]types.TypedParam {
	return map[string]types.TypedParam{
		sinkbinding.Input:                    {Value: map[string]any{"id": 1}},
		"runtime.sink.idempotency_key":       {Value: "fixture-key"},
		sinkbinding.InternalIdempotencyKey:   {Value: "fixture-key"},
		sinkbinding.InternalIdempotencyClass: {Value: "idempotent_with_key"},
		sinkbinding.InternalEffectStartedAt:  {Value: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)},
	}
}

func TestKeyedWriteRetryPreservesEffect(t *testing.T) {
	for _, first := range []string{"429", "503", "connection loss", "timeout"} {
		t.Run(first, func(t *testing.T) {
			var mu sync.Mutex
			calls, effects := 0, 0
			var savedBody, savedKey string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				mu.Lock()
				calls++
				attempt := calls
				if savedKey == "" {
					savedKey, savedBody = r.Header.Get("Idempotency-Key"), string(body)
					effects++
				}
				assert.Equal(t, savedKey, r.Header.Get("Idempotency-Key"))
				assert.Equal(t, savedBody, string(body))
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if attempt == 1 {
					switch first {
					case "429":
						w.Header().Set("Retry-After", "0")
						w.WriteHeader(429)
						return
					case "503":
						w.WriteHeader(503)
						return
					case "connection loss":
						conn, _, err := w.(http.Hijacker).Hijack()
						if !assert.NoError(t, err) {
							return
						}
						_ = conn.Close()
						return
					case "timeout":
						<-r.Context().Done()
						return
					}
				}
				_, _ = fmt.Fprint(w, `{"ok":true}`)
			}))
			defer server.Close()
			ctx := runtimevars.WithSinkExecution(transportsecurity.WithPlaintext(context.Background()))
			connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			connector.client.Timeout = 30 * time.Millisecond
			execution, err := connector.Execute(ctx, []byte(writeRetryOperation), writeRetryParams(), types.ExecutionLimits{HTTPRetryAttempts: 3, HTTPTimeout: time.Second})
			require.NoError(t, err)
			require.NoError(t, execution.Error())
			mu.Lock()
			require.Equal(t, 2, calls)
			require.Equal(t, 1, effects)
			mu.Unlock()
		})
	}
}

func TestWriteRetryRefusesUnverifiedEffects(t *testing.T) {
	for _, change := range []string{"missing key", "unsafe", "ledger", "expired", "missing claim", "unverified", "conflict", "volatile payload", "reserved header", "hop header", "redirect"} {
		t.Run(change, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
			}))
			defer server.Close()
			ctx := runtimevars.WithSinkExecution(transportsecurity.WithPlaintext(context.Background()))
			connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			params := writeRetryParams()
			var op map[string]any
			require.NoError(t, json.Unmarshal([]byte(writeRetryOperation), &op))
			switch change {
			case "missing key":
				delete(params, sinkbinding.InternalIdempotencyKey)
			case "unsafe":
				params[sinkbinding.InternalIdempotencyClass] = types.TypedParam{Value: "unsafe"}
			case "ledger":
				params[sinkbinding.InternalIdempotencyClass] = types.TypedParam{Value: "deduplicated_by_ledger"}
			case "expired":
				params[sinkbinding.InternalEffectStartedAt] = types.TypedParam{Value: time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)}
			case "missing claim":
				delete(params, sinkbinding.InternalEffectStartedAt)
			case "unverified":
				op["write_retry"].(map[string]any)["verified"] = false
			case "conflict":
				op["write_retry"].(map[string]any)["payload_conflict"] = "overwrite"
			case "volatile payload":
				op["body"] = "{{runtime.run.slug}}"
				params["runtime.run.slug"] = types.TypedParam{Value: "new-run"}
			case "reserved header":
				op["write_retry"].(map[string]any)["key_header"] = "Host"
				op["headers"].(map[string]any)["Host"] = "{{runtime.sink.idempotency_key}}"
			case "hop header":
				op["headers"].(map[string]any)["Connection"] = "Idempotency-Key"
			}
			data, err := json.Marshal(op)
			require.NoError(t, err)
			execution, err := connector.Execute(ctx, data, params, types.ExecutionLimits{HTTPRetryAttempts: 3})
			require.NoError(t, err)
			require.Error(t, execution.Error())
			if change == "redirect" {
				require.Equal(t, 1, calls)
				require.ErrorIs(t, execution.Error(), xerror.HTTPAcknowledgementUncertain())
			} else {
				require.Zero(t, calls)
			}
		})
	}
}

func TestWriteRetryExhaustionAndConflict(t *testing.T) {
	for _, status := range []int{429, 503, 409} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
			}))
			defer server.Close()
			ctx := runtimevars.WithSinkExecution(transportsecurity.WithPlaintext(t.Context()))
			connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			execution, err := connector.Execute(ctx, []byte(writeRetryOperation), writeRetryParams(), types.ExecutionLimits{HTTPRetryAttempts: 3})
			require.NoError(t, err)
			if status == 409 {
				require.Equal(t, 1, calls)
				require.ErrorIs(t, execution.Error(), xerror.HTTPAcknowledgementRejected())
			} else {
				require.Equal(t, 3, calls)
				require.ErrorIs(t, execution.Error(), xerror.HTTPAcknowledgementUncertain())
			}
			require.NotContains(t, execution.Error().Error(), "fixture-key")
		})
	}
}

func TestKeyedMultipartRecoveryPreservesWirePayload(t *testing.T) {
	var bodies, contentTypes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		bodies = append(bodies, string(body))
		contentTypes = append(contentTypes, r.Header.Get("Content-Type"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	ctx := runtimevars.WithSinkExecution(transportsecurity.WithPlaintext(t.Context()))
	connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	var operation map[string]any
	require.NoError(t, json.Unmarshal([]byte(writeRetryOperation), &operation))
	operation["headers"].(map[string]any)["Content-Type"] = "multipart/form-data"
	data, err := json.Marshal(operation)
	require.NoError(t, err)
	params := writeRetryParams()
	for attempt := 0; attempt < 2; attempt++ {
		execution, err := connector.Execute(ctx, data, params)
		require.NoError(t, err)
		require.NoError(t, execution.Error())
	}
	require.Len(t, bodies, 2)
	require.Equal(t, bodies[0], bodies[1])
	require.Equal(t, contentTypes[0], contentTypes[1])
	require.NotContains(t, contentTypes[0], "fixture-key")
}

func TestArbitraryKeyDoesNotEnableWriteRetry(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusServiceUnavailable) }))
			defer server.Close()
			ctx := transportsecurity.WithPlaintext(t.Context())
			connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			data := []byte(fmt.Sprintf(`{"method":%q,"path":"/records","headers":{"Idempotency-Key":"unverified"}}`, method))
			execution, err := connector.Execute(ctx, data, nil, types.ExecutionLimits{HTTPRetryAttempts: 3})
			require.NoError(t, err)
			require.NoError(t, execution.Error())
			require.Equal(t, 1, calls)
		})
	}
}
