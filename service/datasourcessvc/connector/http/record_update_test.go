package http

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordUpdateWithholdsUnconfirmedResponses(t *testing.T) {
	const operation = `{"type":"http","method":"PATCH","path":"/crm/v3/objects/companies/{{runtime.sink.record_id}}","body":{"properties":"{{input}}"}}`
	for _, tc := range []struct {
		name string
		code int
		body string
	}{
		{"wrong company", 200, `{"id":"other","properties":{"name":"sensitive"}}`},
		{"pending acknowledgement", 202, `{"id":"record/one","properties":{"name":"sensitive"}}`},
		{"root field cannot satisfy nested mapping", 200, `{"id":"record/one","name":"sensitive","properties":{}}`},
		{"null property", 200, `{"id":"record/one","properties":{"name":null}}`},
		{"missing property", 200, `{"id":"record/one","properties":{}}`},
		{"conflicting acknowledgement", 200, `{"id":"record/one","properties":{"name":"other"}}`},
		{"excess bytes", 200, strings.Repeat("a", 151)},
		{"credential echo", 200, `{"id":"record/one","properties":{"name":"sensitive"},"token":"fixture-secret"}`},
		{"redirect", 307, ""},
		{"transient failure", 503, "sensitive"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, "/base/crm/v3/objects/companies/record%2Fone", r.URL.EscapedPath())
				w.Header().Set("Location", "/redirect")
				w.WriteHeader(tc.code)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			ctx := transportsecurity.WithPlaintext(t.Context())
			c, err := New(ctx, Config{BaseURL: server.URL + "/base", AllowPrivateHosts: true}, Credentials{BearerToken: "fixture-secret"})
			require.NoError(t, err)
			ctx = sinkbinding.WithHTTPRecordUpdate(sinkbinding.WithSinkExecution(ctx), "record/one", map[string]string{"name": "sensitive"}, false, sinkbinding.RecordReadback{IdentityField: "id", FieldsObject: "properties", SelectParameter: "properties"})
			response, err := c.Execute(ctx, []byte(operation), nil, types.ExecutionLimits{HTTPMaxResponseBytes: 150, HTTPRetryAttempts: 3})
			require.NoError(t, err)
			require.ErrorIs(t, response.Error(), xerror.HTTPAcknowledgementUncertain())
			require.NotContains(t, response.Error().Error(), "sensitive")
			require.NotContains(t, response.Error().Error(), "record/one")
			require.Equal(t, 1, calls)
		})
	}
}

func TestRecordUpdateFlatPUT(t *testing.T) {
	const operation = `{"type":"http","method":"PUT","path":"inventory/v2/items/{{runtime.sink.record_id}}","body":"{{input}}"}`
	var writes, reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/inventory/v2/items/part%3F1", r.URL.EscapedPath())
		assert.Empty(t, r.URL.RawQuery)
		switch r.Method {
		case http.MethodPut:
			writes.Add(1)
			raw, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			assert.JSONEq(t, `{"unit-price":"12","description":""}`, string(raw))
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			reads.Add(1)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
		_, _ = fmt.Fprint(w, `{"partCode":"part?1","description":"","unit-price":"12","supplier":{"code":"kept"}}`)
	}))
	defer server.Close()
	ctx := transportsecurity.WithPlaintext(t.Context())
	c, err := New(ctx, Config{BaseURL: server.URL + "/api", AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	for _, reconcile := range []bool{false, true} {
		ctx := sinkbinding.WithHTTPRecordUpdate(sinkbinding.WithSinkExecution(ctx), "part?1", map[string]string{"unit-price": "12", "description": ""}, reconcile, sinkbinding.RecordReadback{IdentityField: "partCode"})
		result, err := c.Execute(ctx, []byte(operation), nil)
		require.NoError(t, err)
		require.NoError(t, result.Error())
		reply, ok := result.HTTP()
		require.True(t, ok)
		require.Equal(t, http.StatusOK, reply.StatusCode)
		require.Empty(t, reply.Body)
	}
	require.EqualValues(t, 1, reads.Load())
	require.EqualValues(t, 1, writes.Load())
}

func TestRecordWriteWithoutContentReadsBackOnce(t *testing.T) {
	const operation = `{"method":"PUT","path":"/assets/{{runtime.sink.record_id}}","body":"{{input}}"}`
	for _, tc := range []struct {
		name      string
		status    int
		state     string
		confirmed bool
	}{
		{"matching", 200, `{"key":"a","value":""}`, true},
		{"different", 200, `{"key":"a","value":"stale"}`, false},
		{"null", 200, `{"key":"a","value":null}`, false},
		{"empty read", 204, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					assert.Equal(t, http.MethodPut, r.Method)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				assert.Equal(t, http.MethodGet, r.Method)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.state)
			}))
			defer server.Close()
			ctx := transportsecurity.WithPlaintext(t.Context())
			c, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			ctx = sinkbinding.WithHTTPRecordUpdate(sinkbinding.WithSinkExecution(ctx), "a", map[string]string{"value": ""}, false, sinkbinding.RecordReadback{IdentityField: "key"})
			out, err := c.Execute(ctx, []byte(operation), nil)
			require.NoError(t, err)
			if tc.confirmed {
				require.NoError(t, out.Error())
				reply, ok := out.HTTP()
				require.True(t, ok)
				require.Equal(t, http.StatusOK, reply.StatusCode)
			} else {
				require.ErrorIs(t, out.Error(), xerror.HTTPAcknowledgementUncertain())
			}
			require.EqualValues(t, 2, calls.Load())
		})
	}
}
