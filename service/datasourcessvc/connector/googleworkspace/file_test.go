package googleworkspace

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	provider "github.com/Braidplane/braidplane-core/pkg/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

func TestDriveFileOperationsRejectBeforeContentDispatch(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		params map[string]types.TypedParam
		limits types.ExecutionLimits
		want   error
	}{
		{
			name: "unpicked selected file",
			raw:  `{"type":"google_workspace","resource":"file","operation":"read","file_id":"private","format":"json"}`,
			want: xerror.Forbidden(xerror.DataSource),
		},
		{
			name:   "read only create",
			raw:    `{"type":"google_workspace","resource":"file","operation":"create","folder_id":"folder","name":"crm.json","format":"json","content":"{{input}}"}`,
			limits: types.ExecutionLimits{ReadOnly: true},
			want:   xerror.DataSourceReadOnly(),
		},
		{
			name: "unsafe create policy",
			raw:  `{"type":"google_workspace","resource":"file","operation":"create","folder_id":"folder","name":"crm.json","format":"json","content":"{{input}}"}`,
			params: map[string]types.TypedParam{
				sinkbinding.InternalIdempotencyClass: {Value: "naturally_idempotent"},
			},
			want: xerror.ReplayUnsafe(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			conn := testFileConnector(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Query().Get("alt") == "media" {
					t.Error("download dispatched before file authorization")
				}
				return response(`{"id":"private","name":"private.json","mimeType":"application/json","isAppAuthorized":false}`), nil
			})
			res, err := conn.Execute(t.Context(), json.RawMessage(tt.raw), tt.params, tt.limits)
			require.NoError(t, err)
			require.ErrorIs(t, res.Error(), tt.want)
			if tt.name == "unpicked selected file" {
				require.Equal(t, 1, calls)
			} else {
				require.Zero(t, calls)
			}
		})
	}
}

func TestDriveFileCreateBindsObjectRowsAndUsesNewFileID(t *testing.T) {
	uploads := 0
	conn := testFileConnector(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/drive/v3/files/folder":
			return response(`{"id":"folder","name":"exports","mimeType":"application/vnd.google-apps.folder","isAppAuthorized":true,"capabilities":{"canAddChildren":true}}`), nil
		case "/upload/drive/v3/files":
			uploads++
			require.Equal(t, http.MethodPost, r.Method)
			require.Equal(t, "multipart", r.URL.Query().Get("uploadType"))
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Contains(t, string(body), `"name":"crm.json"`)
			require.Contains(t, string(body), `"id":7`)
			return response(`{"id":"new-file","name":"crm.json","mimeType":"application/json"}`), nil
		default:
			return nil, &url.Error{Err: io.ErrUnexpectedEOF}
		}
	})
	params := map[string]types.TypedParam{
		"input":                              {Value: []map[string]any{{"id": 7, "name": "Ada"}}},
		sinkbinding.InternalIdempotencyClass: {Value: "unsafe"},
	}
	res, err := conn.Execute(t.Context(), json.RawMessage(`{"type":"google_workspace","resource":"file","operation":"create","folder_id":"folder","name":"crm.json","format":"json","content":"{{input}}"}`), params)
	require.NoError(t, err)
	require.NoError(t, res.Error())
	require.Equal(t, 1, uploads)
	file, ok := res.File()
	require.True(t, ok)
	require.Equal(t, "new-file", file.ResourceID)
	require.Equal(t, "crm.json", file.Name)
}

func TestDriveFileReadSupportsJSONCSVAndOpaqueBytes(t *testing.T) {
	tests := []struct {
		name, format, content string
	}{
		{"json", "json", `[{"id":1,"name":"Ada"}]`},
		{"csv", "csv", "id,name\n1,Ada\n"},
		{"opaque", "file", "raw\x00bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := testFileConnector(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Query().Get("alt") == "media" {
					return response(tt.content), nil
				}
				return response(`{"id":"file","name":"remote.` + tt.format + `","mimeType":"application/octet-stream","isAppAuthorized":true,"capabilities":{"canDownload":true}}`), nil
			})
			res, err := conn.Execute(t.Context(), json.RawMessage(`{"type":"google_workspace","resource":"file","operation":"read","file_id":"file","format":"`+tt.format+`"}`), nil)
			require.NoError(t, err)
			require.NoError(t, res.Error())
			file, ok := res.File()
			require.True(t, ok)
			require.Equal(t, "file", file.ResourceID)
			require.Equal(t, "read", file.Operation)
		})
	}
}

func testFileConnector(t *testing.T, handler roundTripper) *Connector {
	t.Helper()
	original := http.DefaultTransport
	http.DefaultTransport = handler
	t.Cleanup(func() { http.DefaultTransport = original })
	conn, err := New(t.Context(), workspace.Config{}, provider.Credentials{
		AccessToken: "synthetic", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"},
	})
	require.NoError(t, err)
	return conn
}

func TestFailedDriveOperationsRetainResultKindAndOperation(t *testing.T) {
	for _, raw := range []string{
		`{"type":"google_workspace","resource":"file","operation":"list"}`,
		`{"type":"google_workspace","resource":"file","operation":"create","folder_id":"folder","name":"crm.json","format":"json","content":{}}`,
		`{"type":"google_workspace","resource":"file","operation":"update","file_id":"file","format":"json","content":{}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			conn := testFileConnector(t, func(_ *http.Request) (*http.Response, error) {
				r := response(`{}`)
				r.StatusCode = http.StatusForbidden
				return r, nil
			})
			res, err := conn.Execute(t.Context(), json.RawMessage(raw), nil)
			require.NoError(t, err)
			require.Error(t, res.Error())
			var statement workspace.Statement
			require.NoError(t, json.Unmarshal([]byte(raw), &statement))
			if statement.Operation == "list" {
				rows, ok := res.ObjectRows()
				require.True(t, ok)
				require.Equal(t, "list", rows.Operation)
			} else {
				file, ok := res.File()
				require.True(t, ok)
				require.Equal(t, statement.Operation, file.Operation)
				require.Equal(t, statement.FileID, file.ResourceID)
			}
		})
	}
}
