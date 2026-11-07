package client

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestQuerySharesByteBudgetAcrossPages(t *testing.T) {
	first := `{"jobReference":{"jobId":"fixture-job"},"schema":{"fields":[{"name":"id","type":"STRING"}]},"rows":[{"f":[{"v":"first"}]}],"pageToken":"next"}`
	second := `{"rows":[{"f":[{"v":"second"}]}]}`
	for _, delta := range []int64{-1, 0} {
		t.Run(map[bool]string{true: "over", false: "boundary"}[delta < 0], func(t *testing.T) {
			calls := 0
			server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				body := second
				if calls == 1 {
					body = first
				}
				_, _ = io.WriteString(w, body)
			})
			client := newBudgetTestClient(t, server.URL)
			got, err := client.Query(transportsecurity.WithPlaintext(t.Context()), QueryRequest{ProjectID: "fixture", SQL: "SELECT id", MaxResponseBytes: int64(len(first)+len(second)) + delta})
			require.Equal(t, 2, calls)
			if delta < 0 {
				require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, []map[string]any{{"id": "first"}, {"id": "second"}}, got.Rows)
		})
	}
}

func TestQueryStopsAtRequestedRowSentinel(t *testing.T) {
	for _, firstPageRows := range []int{1, 3} {
		t.Run(map[bool]string{true: "provider-overrun", false: "pagination"}[firstPageRows > 2], func(t *testing.T) {
			calls := 0
			server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				rows := strings.Repeat(`{"f":[{"v":"row"}]},`, firstPageRows)
				if calls > 1 {
					rows = `{"f":[{"v":"row"}]},`
				}
				token := "next"
				if calls >= 3 {
					token = ""
				}
				_, _ = io.WriteString(w, `{"jobReference":{"jobId":"fixture-job"},"schema":{"fields":[{"name":"id","type":"STRING"}]},"rows":[`+strings.TrimSuffix(rows, ",")+`],"pageToken":"`+token+`"}`)
			})
			client := newBudgetTestClient(t, server.URL)
			got, err := client.Query(transportsecurity.WithPlaintext(t.Context()), QueryRequest{ProjectID: "fixture", SQL: "SELECT id", MaxResultRows: 2})
			require.NoError(t, err)
			require.Len(t, got.Rows, 2)
			if firstPageRows > 2 {
				require.Equal(t, 1, calls)
			} else {
				require.Equal(t, 2, calls)
			}
		})
	}
}

func TestQueryChargesEmptyContinuingPages(t *testing.T) {
	first := `{"jobReference":{"jobId":"fixture-job"},"pageToken":"same"}`
	page := `{"pageToken":"same"}`
	calls := 0
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		body := page
		if calls == 1 {
			body = first
		} else if calls >= 4 {
			body = `{}`
		}
		_, _ = io.WriteString(w, body)
	})
	client := newBudgetTestClient(t, server.URL)
	got, err := client.Query(transportsecurity.WithPlaintext(t.Context()), QueryRequest{ProjectID: "fixture", SQL: "SELECT id", MaxResponseBytes: int64(len(first) + len(page))})
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
	require.Nil(t, got)
	require.Equal(t, 2, calls, "an exhausted budget must not reset to the default on the next request")
}

func TestQueryBoundsSparseRowExpansion(t *testing.T) {
	name := strings.Repeat("column", 24)
	rows := []map[string]any{{name: nil}, {name: nil}, {name: nil}, {name: nil}}
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	body := `{"schema":{"fields":[{"name":"` + name + `","type":"STRING"}]},"rows":[{},{},{},{}]}`
	for _, delta := range []int64{-1, 0} {
		t.Run(map[bool]string{true: "over", false: "boundary"}[delta < 0], func(t *testing.T) {
			server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			})
			client := newBudgetTestClient(t, server.URL)
			got, err := client.Query(transportsecurity.WithPlaintext(t.Context()), QueryRequest{ProjectID: "fixture", SQL: "SELECT id", MaxResponseBytes: int64(len(encoded)) + delta})
			if delta < 0 {
				require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
				require.Nil(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, rows, got.Rows)
		})
	}
}

func TestQueryKeepsDuplicateNamesAndTrailingEmptyPages(t *testing.T) {
	calls := 0
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		body := `{}`
		if calls == 1 {
			body = `{"jobReference":{"jobId":"fixture-job"},"schema":{"fields":[{"name":"id","type":"STRING"},{"name":"id","type":"STRING"}]},"rows":[{"f":[{"v":"discarded"},{"v":"kept"}]}],"pageToken":"empty"}`
		}
		_, _ = io.WriteString(w, body)
	})
	client := newBudgetTestClient(t, server.URL)
	got, err := client.Query(transportsecurity.WithPlaintext(t.Context()), QueryRequest{ProjectID: "fixture", SQL: "SELECT id", MaxResultRows: 2})
	require.NoError(t, err)
	require.Equal(t, []map[string]any{{"id": "kept"}}, got.Rows)
	require.Equal(t, 2, calls)
}

func TestQueryKeepsSchemaUpdatesWithinSharedBudget(t *testing.T) {
	calls := 0
	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		body := `{"schema":{"fields":[{"name":"new","type":"STRING"}]},"rows":[{"f":[{"v":"second"}]}]}`
		if calls == 1 {
			body = `{"jobReference":{"jobId":"fixture-job"},"schema":{"fields":[{"name":"old","type":"STRING"}]},"rows":[{"f":[{"v":"first"}]}],"pageToken":"next"}`
		}
		_, _ = io.WriteString(w, body)
	})
	client := newBudgetTestClient(t, server.URL)
	got, err := client.Query(transportsecurity.WithPlaintext(t.Context()), QueryRequest{ProjectID: "fixture", SQL: "SELECT id", MaxResponseBytes: 1024})
	require.NoError(t, err)
	require.Equal(t, []Field{{Name: "new", Type: "STRING"}}, got.Schema)
	require.Equal(t, []map[string]any{{"old": "first"}, {"new": "second"}}, got.Rows)
}

func newBudgetTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := New(transportsecurity.WithPlaintext(t.Context()), Config{BaseURL: baseURL, AllowPrivateHosts: true}, Credentials{AccessToken: "synthetic-query-budget-token"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	return client
}
