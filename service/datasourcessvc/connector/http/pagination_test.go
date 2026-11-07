package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const paginatedOperation = `{"method":"GET","path":"/items","pagination":{"cursor_param":"after","records_path":"data.items","next_cursor_path":"paging.next","max_pages":3,"timeout_seconds":2}}`

func TestPaginationDrainsEnvelope(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer test", r.Header.Get("Authorization"))
		if r.URL.Path == "/items" {
			http.Redirect(w, r, "/data?"+r.URL.RawQuery, http.StatusFound)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after") == "" {
			_, _ = fmt.Fprint(w, `{"data":{"items":[{"id":9007199254740993}]},"paging":{"next":"{{opaque}}"},"checkpoint":"first"}`)
		} else {
			assert.Equal(t, "{{opaque}}", r.URL.Query().Get("after"))
			_, _ = fmt.Fprint(w, `{"data":{"items":[{"id":2}]},"paging":{"next":null},"checkpoint":"complete"}`)
		}
	}))
	defer server.Close()
	connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{BearerToken: "test"})
	require.NoError(t, err)
	execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), []byte(paginatedOperation), nil, types.ExecutionLimits{MaxResultRows: 2})
	require.NoError(t, err)
	require.NoError(t, execution.Error())
	response, ok := execution.HTTP()
	require.True(t, ok)
	require.JSONEq(t, `{"data":{"items":[{"id":9007199254740993},{"id":2}]},"paging":{"next":null},"checkpoint":"complete"}`, string(response.Body))
	require.Equal(t, int32(2), requests.Load())
}

func TestPaginationRejectsPartialResults(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		limits     types.ExecutionLimits
		want       error
	}{
		{name: "repeated cursor", want: xerror.InvalidInput(xerror.Query), body: `{"data":{"items":[1]},"paging":{"next":"again"}}`},
		{name: "empty continuing page", want: xerror.InvalidInput(xerror.Query), body: `{"data":{"items":[]},"paging":{"next":"again"}}`},
		{name: "wrong records shape", want: xerror.InvalidInput(xerror.Query), body: `{"data":{"items":{}},"paging":{}}`},
		{name: "wrong cursor shape", want: xerror.InvalidInput(xerror.Query), body: `{"data":{"items":[1]},"paging":{"next":3}}`},
		{name: "row limit", want: xerror.ExecutionRowLimitExceeded(), body: `{"data":{"items":[1,2]},"paging":{}}`, limits: types.ExecutionLimits{MaxResultRows: 1}},
		{name: "byte limit", want: xerror.ExecutionResponseSizeExceeded(), body: `{"data":{"items":[1]},"paging":{}}`, limits: types.ExecutionLimits{HTTPMaxResponseBytes: 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), []byte(paginatedOperation), nil, tc.limits)
			require.NoError(t, err)
			require.True(t, xerror.Is(execution.Error(), tc.want))
			if xerror.Is(tc.want, xerror.InvalidInput(xerror.Query)) {
				require.NotEmpty(t, xerror.PublicDetail(execution.Error()))
				require.NotContains(t, xerror.PublicDetail(execution.Error()), "again")
			}
			response, _ := execution.HTTP()
			require.Nil(t, response)
		})
	}
}

func TestPaginationRetriesRateLimitAndCancels(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":{"items":[1]},"paging":{}}`)
	}))
	defer server.Close()
	connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), []byte(paginatedOperation), nil, types.ExecutionLimits{HTTPRetryAttempts: 2, HTTPRetryBackoff: time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, execution.Error())
	require.Equal(t, int32(2), requests.Load())
	ctx, cancel := context.WithCancel(transportsecurity.WithPlaintext(context.Background()))
	cancel()
	execution, err = connector.Execute(ctx, []byte(paginatedOperation), nil)
	require.NoError(t, err)
	require.True(t, xerror.Is(execution.Error(), xerror.Timeout(xerror.DataSource)))
	require.Equal(t, int32(2), requests.Load())
}

func TestPaginationRejectsCrossOriginRedirect(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") == "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"data":{"items":[1]},"paging":{"next":"page-2"}}`)
			return
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{BearerToken: "private"})
	require.NoError(t, err)
	execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), json.RawMessage(paginatedOperation), nil, types.ExecutionLimits{HTTPMaxRedirects: 3})
	require.NoError(t, err)
	require.True(t, xerror.Is(execution.Error(), xerror.ConnectionFailed(xerror.DataSource)))
	require.Contains(t, xerror.PublicDetail(execution.Error()), "original origin")
	require.Zero(t, forwarded.Load())
}

func TestPaginationCumulativeLimits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limits   types.ExecutionLimits
		maxPages int
		want     error
	}{
		{name: "rows across pages", want: xerror.ExecutionRowLimitExceeded(), limits: types.ExecutionLimits{MaxResultRows: 1}, maxPages: 3},
		{name: "bytes across pages", want: xerror.ExecutionResponseSizeExceeded(), limits: types.ExecutionLimits{HTTPMaxResponseBytes: 80}, maxPages: 3},
		{name: "page limit", want: xerror.InvalidInput(xerror.Query), maxPages: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"data":{"items":[%d]},"paging":{"next":"%d"}}`, n, n)
			}))
			defer server.Close()
			connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			var definition map[string]any
			require.NoError(t, json.Unmarshal([]byte(paginatedOperation), &definition))
			definition["pagination"].(map[string]any)["max_pages"] = tc.maxPages
			data, err := json.Marshal(definition)
			require.NoError(t, err)
			execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), data, nil, tc.limits)
			require.NoError(t, err)
			require.True(t, xerror.Is(execution.Error(), tc.want))
			require.LessOrEqual(t, calls.Load(), int32(2))
		})
	}
}

func TestPaginationDeadlineIncludesBodyRead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(transportsecurity.WithPlaintext(context.Background()), 20*time.Millisecond)
	defer cancel()
	execution, err := connector.Execute(ctx, []byte(paginatedOperation), nil)
	require.NoError(t, err)
	require.True(t, xerror.Is(execution.Error(), xerror.Timeout(xerror.DataSource)))
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
}

func TestPaginationSyntheticProviderCheck(t *testing.T) {
	const pages = 25
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := calls.Add(1)
		assert.Equal(t, fmt.Sprint(page-1), r.URL.Query().Get("after"))
		w.Header().Set("Content-Type", "application/json")
		next := fmt.Sprintf(`"%d"`, page)
		if page == pages {
			next = "null"
		}
		_, _ = fmt.Fprintf(w, `{"data":{"items":[{"id":%d}]},"paging":{"next":%s},"checkpoint":"batch-complete"}`, page, next)
	}))
	defer server.Close()
	connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	operation := fmt.Sprintf(`{"method":"GET","path":"/items","query_params":{"after":"0"},"pagination":{"cursor_param":"after","records_path":"data.items","next_cursor_path":"paging.next","max_pages":%d,"timeout_seconds":5}}`, pages)
	execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), []byte(operation), nil, types.ExecutionLimits{MaxResultRows: pages, HTTPMaxResponseBytes: 8192})
	require.NoError(t, err)
	require.NoError(t, execution.Error())
	response, ok := execution.HTTP()
	require.True(t, ok)
	require.Equal(t, pages, response.RowCount())
	require.Equal(t, int32(pages), calls.Load())
	t.Logf("synthetic provider: %d pages, %d records, %d combined bytes; final checkpoint retained", calls.Load(), response.RowCount(), len(response.Body))
}

func TestPaginationInvalidDefinitionNeverSendsRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"items":[1],"next":"must-not-advance"}`)
	}))
	defer server.Close()
	connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	for _, pagination := range []string{
		`null`, `"cursor"`, `[]`, `{}`, `{"max_pages":"3"}`,
		`{"cursor_param":"after","records_path":"data[0]","next_cursor_path":"paging.next","max_pages":3,"timeout_seconds":2}`,
		`{"cursor_param":"after","records_path":"data.items","next_cursor_path":"paging.*","max_pages":3,"timeout_seconds":2}`,
		`{"cursor_param":"after","records_path":"data.items","next_cursor_path":"paging.next","max_pages":3,"timeout_seconds":2,"next_url":true}`,
	} {
		execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), []byte(`{"method":"GET","path":"/items","pagination":`+pagination+`}`), nil)
		require.NoError(t, err)
		require.True(t, xerror.Is(execution.Error(), xerror.InvalidInput(xerror.Query)))
		require.NotEmpty(t, xerror.PublicDetail(execution.Error()))
		response, _ := execution.HTTP()
		require.Nil(t, response)
	}
	require.Zero(t, calls.Load())
}

func TestPaginationCancellationDuringRetryBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(transportsecurity.WithPlaintext(context.Background()))
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		cancel()
	}))
	defer server.Close()
	connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	execution, err := connector.Execute(ctx, []byte(paginatedOperation), nil, types.ExecutionLimits{HTTPRetryAttempts: 3, HTTPRetryBackoff: time.Minute})
	require.NoError(t, err)
	require.True(t, xerror.Is(execution.Error(), xerror.Timeout(xerror.DataSource)))
	require.Equal(t, int32(1), calls.Load())
}

func TestPaginationNetworkFailureDoesNotExposeCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") == "" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"data":{"items":[1]},"paging":{"next":"sensitive-cursor"}}`)
			return
		}
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()
	connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), []byte(paginatedOperation), nil)
	require.NoError(t, err)
	require.True(t, xerror.Is(execution.Error(), xerror.ConnectionFailed(xerror.DataSource)))
	require.NotContains(t, execution.Error().Error(), "sensitive-cursor")
}

func TestPaginationPathsHonorCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(transportsecurity.WithPlaintext(context.Background()))
	cancel()
	envelope := map[string]json.RawMessage{"data": json.RawMessage(`{"items":[1],"keep":"unchanged"}`)}
	value, found, err := paginationPath(ctx, envelope, "data.items")
	require.True(t, xerror.Is(err, xerror.Timeout(xerror.DataSource)))
	require.True(t, xerror.Is(err, context.Canceled))
	require.Nil(t, value)
	require.False(t, found)
	err = setPaginationPath(ctx, envelope, []string{"data", "items"}, json.RawMessage(`[1,2]`))
	require.True(t, xerror.Is(err, xerror.Timeout(xerror.DataSource)))
	require.JSONEq(t, `{"items":[1],"keep":"unchanged"}`, string(envelope["data"]))
	require.NoError(t, setPaginationPath(transportsecurity.WithPlaintext(context.Background()), envelope, []string{"data", "items"}, json.RawMessage(`[1,2]`)))
	require.JSONEq(t, `{"items":[1,2],"keep":"unchanged"}`, string(envelope["data"]))
	err = setPaginationPath(transportsecurity.WithPlaintext(context.Background()), envelope, nil, json.RawMessage(`[3]`))
	require.True(t, xerror.Is(err, xerror.InvalidInput(xerror.Query)))
	require.JSONEq(t, `{"items":[1,2],"keep":"unchanged"}`, string(envelope["data"]))
}

func TestPaginationDeadlineBoundsDeepEnvelopeProcessing(t *testing.T) {
	const depth = 512
	body := strings.Repeat(`{"a":`, depth) + `{"items":["` + strings.Repeat("x", 1<<20) + `"],"next":null}` + strings.Repeat("}", depth)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	defer server.Close()
	connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	operation, err := json.Marshal(map[string]any{
		"method": "GET", "path": "/items",
		"pagination": map[string]any{"cursor_param": "after", "records_path": strings.Repeat("a.", depth) + "items", "next_cursor_path": strings.Repeat("a.", depth) + "next", "max_pages": 1, "timeout_seconds": 1},
	})
	require.NoError(t, err)
	started := time.Now()
	execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), operation, nil)
	require.NoError(t, err)
	require.True(t, xerror.Is(execution.Error(), xerror.Timeout(xerror.DataSource)))
	require.Contains(t, xerror.PublicDetail(execution.Error()), "deadline")
	require.Less(t, time.Since(started), 5*time.Second)
	response, _ := execution.HTTP()
	require.Nil(t, response)
}
