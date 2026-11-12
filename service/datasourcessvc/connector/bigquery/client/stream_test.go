package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueryPagesStreamsOneJobAndCancelsOnFailure(t *testing.T) {
	for _, mode := range []string{"complete", "row-limit", "consumer-error", "cancel", "page-limit", "schema-change"} {
		t.Run(mode, func(t *testing.T) {
			requests, pages, cancellations := 0, 0, 0
			ctx, cancel := context.WithTimeout(transportsecurity.WithPlaintext(t.Context()), 5*time.Second)
			defer cancel()
			server := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/cancel") {
					cancellations++
					_, _ = io.WriteString(w, `{}`)
					return
				}
				requests++
				switch requests {
				case 1:
					var payload struct {
						DryRun bool `json:"dryRun"`
					}
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
					assert.True(t, payload.DryRun)
					_, _ = io.WriteString(w, `{"statementType":"SELECT"}`)
				case 2:
					_, _ = io.WriteString(w, `{"jobReference":{"jobId":"fixture-job","location":"EU"},"jobComplete":false}`)
				case 3, 4:
					assert.Equal(t, "/bigquery/v2/projects/fixture/queries/fixture-job", r.URL.Path)
					assert.Equal(t, "EU", r.URL.Query().Get("location"))
					token := "next"
					if requests == 4 {
						assert.Equal(t, "next", r.URL.Query().Get("pageToken"))
						token = ""
					}
					value := strings.Repeat("x", 150)
					if mode == "page-limit" {
						value = strings.Repeat("x", 500)
					}
					name := "id"
					if mode == "schema-change" && requests == 4 {
						name = "changed"
					}
					_, _ = io.WriteString(w, `{"jobComplete":true,"schema":{"fields":[{"name":"`+name+`","type":"STRING"}]},"rows":[{"f":[{"v":"`+value+`"}]}],"pageToken":"`+token+`"}`)
				default:
					t.Error("unexpected provider request")
				}
			})
			client := newBudgetTestClient(t, server.URL)
			rowLimit := 2
			if mode == "row-limit" {
				rowLimit = 1
			}
			err := client.QueryPages(ctx, QueryRequest{ProjectID: "fixture", SQL: "SELECT id", MaxResultRows: rowLimit, MaxResponseBytes: 400}, func(page *QueryResult) error {
				pages++
				require.Len(t, page.Rows, 1)
				if mode == "consumer-error" {
					return io.ErrClosedPipe
				}
				if mode == "cancel" {
					cancel()
				}
				return nil
			})
			switch mode {
			case "complete":
				require.NoError(t, err)
				require.Equal(t, 2, pages, "the response budget resets per page, not per result")
				require.Zero(t, cancellations)
			case "row-limit":
				require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
				require.Equal(t, 1, pages, "overflow must fail instead of returning a truncated file")
			case "consumer-error":
				require.ErrorIs(t, err, io.ErrClosedPipe)
				require.Equal(t, 3, requests)
			case "cancel":
				require.ErrorIs(t, err, context.Canceled)
				require.Equal(t, 3, requests)
			case "schema-change":
				require.Error(t, err)
				require.Equal(t, 1, pages)
			case "page-limit":
				require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
				require.Zero(t, pages)
			}
			if mode != "complete" {
				require.Equal(t, 1, cancellations)
			}
		})
	}
}

func TestQueryPagesRefusesWritesBeforeExecution(t *testing.T) {
	for _, statementType := range []string{"INSERT", "SCRIPT", ""} {
		t.Run(statementType, func(t *testing.T) {
			calls := 0
			server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				_, _ = io.WriteString(w, `{"statementType":"`+statementType+`"}`)
			})
			client := newBudgetTestClient(t, server.URL)
			err := client.QueryPages(transportsecurity.WithPlaintext(t.Context()), QueryRequest{ProjectID: "fixture", SQL: "provider validated SQL", MaxResultRows: 1}, func(*QueryResult) error {
				t.Fatal("non-SELECT query produced rows")
				return nil
			})
			require.Error(t, err)
			require.Equal(t, 1, calls)
		})
	}
}
