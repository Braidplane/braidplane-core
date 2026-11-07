package pipelineruntime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/catalog"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

type nativeWorkspaceTransport func(*http.Request) (*http.Response, error)

func (f nativeWorkspaceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeWorkspaceSinkPreservesIdentityAndBlocksRedelivery(t *testing.T) {
	for _, resource := range []string{"document", "presentation"} {
		for _, operation := range []string{"create", "update"} {
			t.Run(resource+operation, func(t *testing.T) {
				original := http.DefaultTransport
				t.Cleanup(func() { http.DefaultTransport = original })
				writes := 0
				mime := "application/vnd.google-apps." + resource
				http.DefaultTransport = nativeWorkspaceTransport(func(r *http.Request) (*http.Response, error) {
					body := `{"id":"native","name":"Native","mimeType":"` + mime + `","capabilities":{"canEdit":true}}`
					if r.Method == http.MethodGet && operation == "create" {
						body = `{"id":"folder","mimeType":"application/vnd.google-apps.folder","capabilities":{"canAddChildren":true}}`
					}
					if r.Method == http.MethodPost {
						writes++
						if operation == "create" {
							body = `{"id":"created","name":"Blank","mimeType":"` + mime + `"}`
						} else {
							var request struct {
								Requests []struct{ ReplaceAllText struct{ ReplaceText string } }
							}
							require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
							require.Len(t, request.Requests, 1)
							require.JSONEq(t, `[{"customer":"Alice"}]`, request.Requests[0].ReplaceAllText.ReplaceText)
							idKey := "documentId"
							if resource == "presentation" {
								idKey = "presentationId"
							}
							body = `{"` + idKey + `":"native","replies":[{"replaceAllText":{"occurrencesChanged":1}}]}`
						}
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})
				executor := &workspaceInlineSinkExecutor{inline: func(ctx context.Context, _, _ slug.Slug, inline, values map[string]any) (*result.ExecutionResult, error) {
					require.Equal(t, "unsafe", values[sinkbinding.InternalIdempotencyClass])
					client, err := connector.NewConnector(ctx, model.DataSourceTypeGoogleWorkspace, []byte(`{"access_mode":"selected_files"}`), []byte(`{"access_token":"synthetic","granted_scopes":["https://www.googleapis.com/auth/drive.file"]}`))
					require.NoError(t, err)
					defer func() { require.NoError(t, client.Close()) }()
					raw, err := json.Marshal(inline)
					require.NoError(t, err)
					params := map[string]types.TypedParam{}
					for key, value := range values {
						params[key] = types.TypedParam{Value: value}
					}
					return client.Execute(ctx, raw, params)
				}}
				ledger := &fakeSinkEffectLedger{effect: pipelinerun.SinkEffect{Claimed: true}}
				input := sinkEffectTestInput(pipelinespec.SinkIdempotencyClassUnsafe)
				input.SinkBackend = model.SinkBackendHTTP
				input.SinkOperationType = catalog.OperationTypeGoogleWorkspace
				input.SinkOperationRef = ""
				input.SinkOperationInline = map[string]any{"type": "google_workspace", "resource": resource, "operation": operation}
				if operation == "create" {
					input.SinkOperationInline["folder_id"] = "folder"
					input.SinkOperationInline["name"] = "Blank"
				} else {
					input.SinkOperationInline["file_id"] = "native"
					input.SinkOperationInline["match_text"] = "Customer"
					input.SinkOperationInline["content"] = "{{input}}"
				}
				input.SourceResult = pipelinerun.NewStepResult(result.NewObjectRowsResult("postgres", &result.ObjectRowsResult{Rows: []map[string]any{{"customer": "Alice"}}, ColumnNames: []string{"customer"}, RowCount: 1}, 0, nil))
				runtime := NewSinkRuntime(executor, ledger)
				require.NoError(t, runtime.Execute(t.Context(), input))
				rows, ok := input.SinkResult.ObjectRows()
				require.True(t, ok)
				require.Equal(t, operation, rows.Operation)
				id := "native"
				if operation == "create" {
					id = "created"
				}
				require.Equal(t, id, rows.Rows[0]["file_id"])
				require.NotContains(t, rows.Rows[0], "content")
				ledger.effect.Claimed = false
				require.ErrorIs(t, runtime.Execute(t.Context(), input), errSinkReplayBlocked)
				require.Equal(t, 1, writes)
			})
		}
	}
}
