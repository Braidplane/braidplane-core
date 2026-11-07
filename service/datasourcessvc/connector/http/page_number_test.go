package http

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

const pageNumberOperation = `{"method":"GET","path":"/items","query_params":{"page":99,"pageSize":99},"pagination":{"page_param":"page","page_size_param":"pageSize","page_size":1,"records_path":"data.items","total_pages_path":"paging.totalPages","page_number_path":"paging.page","max_pages":3,"timeout_seconds":2}}`

func TestPageNumberPaginationDrainsWithRetry(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "synthetic-key", r.Header.Get("X-Api-Key"))
		assert.Equal(t, "1", r.URL.Query().Get("pageSize"))
		if requests.Add(1) == 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		assert.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"items":[{"id":%d}]},"paging":{"totalPages":3,"page":%d},"checkpoint":"run-complete"}`, page, page)
	}))
	defer server.Close()
	connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{APIKey: "synthetic-key", APIKeyHeader: "X-Api-Key"})
	require.NoError(t, err)
	execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), []byte(pageNumberOperation), nil, types.ExecutionLimits{MaxResultRows: 3, HTTPRetryAttempts: 2, HTTPRetryBackoff: time.Millisecond})
	require.NoError(t, err)
	require.NoError(t, execution.Error())
	response, ok := execution.HTTP()
	require.True(t, ok)
	require.JSONEq(t, `{"data":{"items":[{"id":1},{"id":2},{"id":3}]},"paging":{"totalPages":3,"page":3},"checkpoint":"run-complete"}`, string(response.Body))
	require.Equal(t, 3, *response.PaginationRecordCount)
	require.Equal(t, int32(4), requests.Load())
}

func TestPageNumberPaginationFailsWithoutPartialResults(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		limits     types.ExecutionLimits
		want       error
	}{
		{name: "repeated page", body: `{"data":{"items":[1]},"paging":{"totalPages":3,"page":1}}`, want: xerror.InvalidInput(xerror.Query)},
		{name: "missing page number", body: `{"data":{"items":[1]},"paging":{"totalPages":1}}`, want: xerror.InvalidInput(xerror.Query)},
		{name: "wrong page number", body: `{"data":{"items":[1]},"paging":{"totalPages":1,"page":2}}`, want: xerror.InvalidInput(xerror.Query)},
		{name: "null page number", body: `{"data":{"items":[1]},"paging":{"totalPages":1,"page":null}}`, want: xerror.InvalidInput(xerror.Query)},
		{name: "string page number", body: `{"data":{"items":[1]},"paging":{"totalPages":1,"page":"1"}}`, want: xerror.InvalidInput(xerror.Query)},
		{name: "empty continuing page", body: `{"data":{"items":[]},"paging":{"totalPages":3}}`, want: xerror.InvalidInput(xerror.Query)},
		{name: "missing total", body: `{"data":{"items":[1]},"paging":{}}`, want: xerror.InvalidInput(xerror.Query)},
		{name: "null total", body: `{"data":{"items":[1]},"paging":{"totalPages":null}}`, want: xerror.InvalidInput(xerror.Query)},
		{name: "string total", body: `{"data":{"items":[1]},"paging":{"totalPages":"1"}}`, want: xerror.InvalidInput(xerror.Query)},
		{name: "negative total", body: `{"data":{"items":[]},"paging":{"totalPages":-1}}`, want: xerror.InvalidInput(xerror.Query)},
		{name: "fractional total", body: `{"data":{"items":[1]},"paging":{"totalPages":1.5}}`, want: xerror.InvalidInput(xerror.Query)},
		{name: "zero with records", body: `{"data":{"items":[1]},"paging":{"totalPages":0}}`, want: xerror.InvalidInput(xerror.Query)},
		{name: "row limit", body: `{"data":{"items":[1,2]},"paging":{"totalPages":1}}`, limits: types.ExecutionLimits{MaxResultRows: 1}, want: xerror.ExecutionRowLimitExceeded()},
		{name: "byte limit", body: `{"data":{"items":[1]},"paging":{"totalPages":1}}`, limits: types.ExecutionLimits{HTTPMaxResponseBytes: 20}, want: xerror.ExecutionResponseSizeExceeded()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), []byte(pageNumberOperation), nil, tc.limits)
			require.NoError(t, err)
			require.True(t, xerror.Is(execution.Error(), tc.want), "%v", execution.Error())
			response, _ := execution.HTTP()
			require.Nil(t, response)
		})
	}
}

func TestPageNumberPaginationTerminationAndBounds(t *testing.T) {
	for _, scenario := range []string{"empty", "page limit", "changing total", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(transportsecurity.WithPlaintext(context.Background()))
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				w.Header().Set("Content-Type", "application/json")
				switch scenario {
				case "empty":
					_, _ = fmt.Fprint(w, `{"data":{"items":[]},"paging":{"totalPages":0,"page":1}}`)
				case "changing total":
					_, _ = fmt.Fprintf(w, `{"data":{"items":[%d]},"paging":{"totalPages":%d,"page":%d}}`, page, 4-page, page)
				case "cancel":
					cancel()
					<-r.Context().Done()
				default:
					_, _ = fmt.Fprintf(w, `{"data":{"items":[%d]},"paging":{"totalPages":4,"page":%d}}`, page, page)
				}
			}))
			defer server.Close()
			connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			execution, err := connector.Execute(ctx, []byte(pageNumberOperation), nil)
			require.NoError(t, err)
			if scenario == "empty" {
				require.NoError(t, execution.Error())
				response, _ := execution.HTTP()
				require.Equal(t, 0, *response.PaginationRecordCount)
			} else {
				require.Error(t, execution.Error())
				response, _ := execution.HTTP()
				require.Nil(t, response)
			}
		})
	}
}

func TestPageNumberPaginationRejectsInvalidOptions(t *testing.T) {
	connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: "http://127.0.0.1", AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	for _, operation := range []string{
		strings.Replace(pageNumberOperation, `"page_size":1`, `"page_size":0`, 1),
		strings.Replace(pageNumberOperation, `"page_size_param":"pageSize"`, `"page_size_param":"page"`, 1),
		strings.Replace(pageNumberOperation, `"page_param":"page"`, `"cursor_param":"after","page_param":"page"`, 1),
		strings.Replace(pageNumberOperation, `"paging.totalPages"`, `"paging[0]"`, 1),
		strings.Replace(pageNumberOperation, `"method":"GET"`, `"method":"POST"`, 1),
	} {
		execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), []byte(operation), nil)
		require.NoError(t, err)
		require.Error(t, execution.Error())
	}
}

func TestPageNumberPaginationPreservesIdenticalRecordsOnDifferentPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, err := strconv.Atoi(r.URL.Query().Get("page"))
		assert.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"items":[{"status":"same"}]},"paging":{"totalPages":3,"page":%d}}`, page)
	}))
	defer server.Close()
	connector, err := New(transportsecurity.WithPlaintext(context.Background()), Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	operation := strings.Replace(pageNumberOperation, `"page":99`, `"":2,"page":99`, 1)
	execution, err := connector.Execute(transportsecurity.WithPlaintext(context.Background()), []byte(operation), nil)
	require.NoError(t, err)
	require.NoError(t, execution.Error())
	response, _ := execution.HTTP()
	require.JSONEq(t, `{"data":{"items":[{"status":"same"},{"status":"same"},{"status":"same"}]},"paging":{"totalPages":3,"page":3}}`, string(response.Body))
}
