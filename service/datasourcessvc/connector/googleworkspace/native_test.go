package googleworkspace

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

func TestNativeOperations(t *testing.T) {
	for _, resource := range []string{"document", "presentation"} {
		for _, operation := range []string{"read", "create", "update", "export"} {
			t.Run(resource+operation, func(t *testing.T) {
				mutation := 0
				conn := testFileConnector(t, func(r *http.Request) (*http.Response, error) {
					require.Equal(t, "Bearer synthetic", r.Header.Get("Authorization"))
					if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/drive/v3/files/") && !strings.HasSuffix(r.URL.Path, "/export") {
						id, mime := "native", (workspace.Statement{Resource: resource}).NativeMIMEType()
						if operation == "create" {
							id, mime = "folder", "application/vnd.google-apps.folder"
						}
						return response(`{"id":"` + id + `","name":"Native","mimeType":"` + mime + `","capabilities":{"canEdit":true,"canDownload":true,"canAddChildren":true}}`), nil
					}
					switch operation {
					case "read":
						if resource == "document" {
							require.Equal(t, "true", r.URL.Query().Get("includeTabsContent"))
							return response(`{"documentId":"native","tabs":[{"documentTab":{"body":{"content":[]}}}]}`), nil
						}
						return response(`{"presentationId":"native","slides":[{"objectId":"slide"}]}`), nil
					case "create":
						mutation++
						require.Equal(t, "POST", r.Method)
						var file map[string]any
						require.NoError(t, json.NewDecoder(r.Body).Decode(&file))
						require.Equal(t, []any{"folder"}, file["parents"])
						require.Equal(t, (workspace.Statement{Resource: resource}).NativeMIMEType(), file["mimeType"])
						return response(`{"id":"created","name":"Blank","mimeType":"` + (workspace.Statement{Resource: resource}).NativeMIMEType() + `"}`), nil
					case "update":
						mutation++
						require.True(t, strings.HasSuffix(r.URL.Path, "/native:batchUpdate"))
						var payload struct {
							Requests []struct {
								ReplaceAllText struct {
									ContainsText struct {
										Text      string
										MatchCase bool
									}
									ReplaceText string
								}
							}
						}
						require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
						require.Len(t, payload.Requests, 1)
						require.Equal(t, "{{name}}", payload.Requests[0].ReplaceAllText.ContainsText.Text)
						require.True(t, payload.Requests[0].ReplaceAllText.ContainsText.MatchCase)
						require.Equal(t, "Alice", payload.Requests[0].ReplaceAllText.ReplaceText)
						idKey := "documentId"
						if resource == "presentation" {
							idKey = "presentationId"
						}
						return response(`{"` + idKey + `":"native","replies":[{"replaceAllText":{"occurrencesChanged":2}}]}`), nil
					case "export":
						require.Equal(t, "application/pdf", r.URL.Query().Get("mimeType"))
						return response("%PDF-synthetic"), nil
					}
					t.Fatal("unexpected provider call")
					return nil, xerror.ExecutionFailed()
				})
				fields := `,"file_id":"native"`
				if operation == "create" {
					fields = `,"folder_id":"folder","name":"Blank"`
				}
				if operation == "update" {
					fields += `,"match_text":"{{name}}","content":"{{input}}"`
				}
				params := map[string]types.TypedParam{"input": {Value: "Alice"}, sinkbinding.InternalIdempotencyClass: {Value: "unsafe"}}
				res, err := conn.Execute(t.Context(), json.RawMessage(`{"type":"google_workspace","resource":"`+resource+`","operation":"`+operation+`"`+fields+`}`), params)
				require.NoError(t, err)
				require.NoError(t, res.Error())
				if operation == "export" {
					f, ok := res.File()
					require.True(t, ok)
					require.Equal(t, "native", f.ResourceID)
					require.Equal(t, "application/pdf", f.File.MIMEType)
					require.Equal(t, []byte("%PDF-synthetic"), f.Content)
					payload, ok := pipelinerun.NewStepResult(res).Payload()
					require.True(t, ok)
					require.Equal(t, model.ContractRepresentationFile, payload.Representation)
					require.Equal(t, f.Content, payload.FileBytes)
					require.Equal(t, f.File.Digest, payload.File.Digest)
				} else {
					rows, ok := res.ObjectRows()
					require.True(t, ok)
					require.Equal(t, operation, rows.Operation)
					require.Len(t, rows.Rows, 1)
					id := "native"
					if operation == "create" {
						id = "created"
					}
					require.Equal(t, id, rows.Rows[0]["file_id"])
				}
				require.Equal(t, map[bool]int{true: 1, false: 0}[operation == "create" || operation == "update"], mutation)
			})
		}
	}
}

func TestNativeFailuresAndSingleWriteAttempt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		limits   types.ExecutionLimits
		params   map[string]types.TypedParam
		status   int
		mime     string
		expected error
		dispatch int
	}{
		{name: "read only", limits: types.ExecutionLimits{ReadOnly: true}, expected: xerror.DataSourceReadOnly()},
		{name: "replay", params: map[string]types.TypedParam{sinkbinding.InternalIdempotencyClass: {Value: "naturally_idempotent"}}, expected: xerror.ReplayUnsafe()},
		{name: "binary input", params: map[string]types.TypedParam{"input": {Value: []byte("binary")}}, expected: xerror.ValidationFailed()},
		{name: "cannot edit", expected: xerror.Forbidden(xerror.DataSource)},
		{name: "wrong native type", mime: "application/vnd.google-apps.presentation", expected: xerror.ValidationFailed()},
		{name: "forbidden", status: 403, expected: xerror.Forbidden(xerror.DataSource), dispatch: 1},
		{name: "quota", status: 429, expected: xerror.ExecutionFailed(), dispatch: 1},
		{name: "server error", status: 503, expected: xerror.ExecutionFailed(), dispatch: 1},
		{name: "lost response", status: -1, expected: xerror.ExecutionFailed(), dispatch: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dispatched := 0
			conn := testFileConnector(t, func(r *http.Request) (*http.Response, error) {
				if r.Method == "GET" {
					mime := tc.mime
					if mime == "" {
						mime = "application/vnd.google-apps.document"
					}
					edit := "true"
					if tc.name == "cannot edit" {
						edit = "false"
					}
					return response(`{"id":"native","mimeType":"` + mime + `","capabilities":{"canEdit":` + edit + `}}`), nil
				}
				dispatched++
				if tc.status == -1 {
					return nil, io.EOF
				}
				result := response(`{"error":{"message":"private provider content"}}`)
				result.StatusCode = tc.status
				return result, nil
			})
			content := `"y"`
			if tc.name == "binary input" {
				content = `"{{input}}"`
			}
			res, err := conn.Execute(t.Context(), json.RawMessage(`{"type":"google_workspace","resource":"document","operation":"update","file_id":"native","match_text":"x","content":`+content+`}`), tc.params, tc.limits)
			require.NoError(t, err)
			require.ErrorIs(t, res.Error(), tc.expected)
			require.NotContains(t, res.Error().Error(), "private provider content")
			require.Equal(t, tc.dispatch, dispatched)
		})
	}
}

func TestNativeLimitsRejectBeforeWriteDispatch(t *testing.T) {
	for _, limits := range []types.ExecutionLimits{{HTTPMaxRequestBytes: 8}, {MaxResponseBytes: 300}} {
		t.Run("limits", func(t *testing.T) {
			writes := 0
			conn := testFileConnector(t, func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					writes++
				}
				return response(`{"id":"folder","mimeType":"application/vnd.google-apps.folder","capabilities":{"canAddChildren":true}}`), nil
			})
			res, err := conn.Execute(t.Context(), json.RawMessage(`{"type":"google_workspace","resource":"document","operation":"create","folder_id":"folder","name":"Blank"}`), nil, limits)
			require.NoError(t, err)
			require.Error(t, res.Error())
			require.Equal(t, 0, writes)
		})
	}
}

func TestFailedNativeExportRetainsFileResultContract(t *testing.T) {
	conn := testFileConnector(t, func(r *http.Request) (*http.Response, error) {
		res := response(`{"error":{"message":"private"}}`)
		res.StatusCode = http.StatusForbidden
		return res, nil
	})
	res, err := conn.Execute(t.Context(), json.RawMessage(`{"type":"google_workspace","resource":"document","operation":"export","file_id":"native"}`), nil)
	require.NoError(t, err)
	require.ErrorIs(t, res.Error(), xerror.Forbidden(xerror.DataSource))
	file, ok := res.File()
	require.True(t, ok)
	require.Equal(t, "export", file.Operation)
	require.Equal(t, "file", file.Format)
	require.Equal(t, "application/pdf", file.MIMEType)
	require.Equal(t, "native", file.ResourceID)
}
