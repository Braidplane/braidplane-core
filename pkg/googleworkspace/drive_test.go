package googleworkspace

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newDriveTestClient(t *testing.T, credentials Credentials, handler http.HandlerFunc) *Client {
	t.Helper()
	client, err := New(t.Context(), credentials)
	require.NoError(t, err)
	client.client.Transport = roundTripper(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler(recorder, request)
		return recorder.Result(), nil
	})
	client.drive.BasePath = "https://drive.test/drive/"
	return client
}

func TestDriveDiscoveryIncludesAccountAccessibleFilesAndUsesSharedDriveParameters(t *testing.T) {
	var gotQuery string
	client := newDriveTestClient(t, Credentials{
		AccessToken: "synthetic", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"},
	}, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/drive/files", r.URL.Path)
		gotQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"nextPageToken":"next","files":[{"id":"picked","name":"picked.json","mimeType":"application/json","isAppAuthorized":true},{"id":"unpicked","name":"unpicked.json","mimeType":"application/json","isAppAuthorized":false}]}`)
	})

	page, err := client.Discovery(t.Context(), workspace.ListOptions{
		FolderID: "folder-1", DriveID: "drive-1", MIMEType: "application/json", PageToken: "before",
	}, Limits{}, 20)
	require.NoError(t, err)
	require.Equal(t, []workspace.File{{FileID: "picked", Name: "picked.json", MIMEType: "application/json"}, {FileID: "unpicked", Name: "unpicked.json", MIMEType: "application/json"}}, page.Files)
	require.Equal(t, "next", page.NextPageToken)
	assert.Contains(t, gotQuery, "corpora=drive")
	assert.Contains(t, gotQuery, "driveId=drive-1")
	assert.Contains(t, gotQuery, "includeItemsFromAllDrives=true")
	assert.Contains(t, gotQuery, "supportsAllDrives=true")
	assert.Contains(t, gotQuery, "pageToken=before")
	assert.Contains(t, gotQuery, "pageSize=20")
	assert.Contains(t, gotQuery, "trashed%3Dfalse")
	assert.Contains(t, gotQuery, "folder-1")
	assert.Contains(t, gotQuery, "mimeType")
}

func TestDriveMetadataRejectsMissingScopesBeforeProviderAccess(t *testing.T) {
	calls := 0
	client := newDriveTestClient(t, Credentials{
		AccessToken: "synthetic",
	}, func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "/drive/files/unpicked", r.URL.Path)
		_, _ = io.WriteString(w, `{"id":"unpicked","name":"private.json","mimeType":"application/json","isAppAuthorized":false}`)
	})

	_, err := client.FileMetadata(t.Context(), "unpicked", Limits{})
	require.ErrorIs(t, err, xerror.Forbidden(xerror.DataSource))
	require.Contains(t, err.Error(), "selected-file access")
	require.Zero(t, calls)
}

func TestDriveUploadUsesOneMultipartRequestAndStableUpdateID(t *testing.T) {
	tests := []struct {
		name, id, folder, filename string
		wantMethod, wantPath       string
		wantMetadata               []string
		notMetadata                []string
	}{
		{
			name: "create duplicate filename", folder: "folder-1", filename: "crm.json",
			wantMethod: http.MethodPost, wantPath: "/upload/drive/v3/files",
			wantMetadata: []string{`"name":"crm.json"`, `"parents":["folder-1"]`},
		},
		{
			name: "update stable ID", id: "file-1", filename: "ignored.json",
			wantMethod: http.MethodPatch, wantPath: "/upload/drive/v3/files/file-1",
			notMetadata: []string{`"name"`, `"parents"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			responseID := tt.id
			if responseID == "" {
				responseID = "new-id"
			}
			client := newDriveTestClient(t, Credentials{AccessToken: "synthetic", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}}, func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, tt.wantMethod, r.Method)
				assert.Equal(t, tt.wantPath, r.URL.Path)
				assert.Equal(t, "multipart", r.URL.Query().Get("uploadType"))
				assert.Equal(t, "true", r.URL.Query().Get("supportsAllDrives"))
				assert.True(t, strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/related;"))
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				for _, value := range tt.wantMetadata {
					assert.Contains(t, string(body), value)
				}
				for _, value := range tt.notMetadata {
					assert.NotContains(t, string(body), value)
				}
				assert.Contains(t, string(body), "customer")
				_, _ = io.WriteString(w, `{"id":"`+responseID+`","name":"crm.json","mimeType":"application/json"}`)
			})

			file, err := client.Upload(t.Context(), tt.id, tt.folder, tt.filename, "application/json", []byte(`{"customer":"acme"}`), Limits{RetryAttempts: 4})
			require.NoError(t, err)
			require.Equal(t, responseID, file.FileID)
			require.Equal(t, 1, calls)
		})
	}
}

func TestDriveFileLimitsAndProviderFailures(t *testing.T) {
	t.Run("download response limit", func(t *testing.T) {
		client := newDriveTestClient(t, Credentials{AccessToken: "synthetic", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}}, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "too-large")
		})
		_, err := client.Download(t.Context(), "file-1", Limits{MaxResponseBytes: 3})
		require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
	})

	t.Run("revoked access", func(t *testing.T) {
		client := newDriveTestClient(t, Credentials{AccessToken: "synthetic", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}}, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		_, err := client.FileMetadata(t.Context(), "file-1", Limits{})
		require.ErrorIs(t, err, xerror.Forbidden(xerror.DataSource))
	})

	t.Run("upload network failure is not retried", func(t *testing.T) {
		calls := 0
		client, err := New(context.Background(), Credentials{AccessToken: "synthetic", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}})
		require.NoError(t, err)
		client.client.Transport = roundTripper(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, io.ErrUnexpectedEOF
		})
		_, err = client.Upload(t.Context(), "", "folder-1", "crm.json", "application/json", []byte("{}"), Limits{RetryAttempts: 5})
		require.ErrorIs(t, err, xerror.Unavailable(xerror.DataSource))
		require.Equal(t, 1, calls)
	})
}

func TestDriveDiscoveryRejectsProviderPageBeyondRequestedLimit(t *testing.T) {
	client := newDriveTestClient(t, Credentials{AccessToken: "synthetic", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}}, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "1", r.URL.Query().Get("pageSize"))
		_, _ = io.WriteString(w, `{"files":[{"id":"a"},{"id":"b"}]}`)
	})
	_, err := client.Discovery(t.Context(), workspace.ListOptions{}, Limits{}, 1)
	require.ErrorIs(t, err, xerror.ExecutionRowLimitExceeded())
}

func TestDriveUploadRequestLimitFailsBeforeProviderDispatch(t *testing.T) {
	calls := 0
	client := newDriveTestClient(t, Credentials{AccessToken: "synthetic", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}}, func(http.ResponseWriter, *http.Request) { calls++ })
	_, err := client.Upload(t.Context(), "", "folder", "crm.json", "application/json", []byte("{}"), Limits{MaxRequestBytes: 10})
	require.ErrorIs(t, err, xerror.ExecutionRequestSizeExceeded())
	require.NotContains(t, err.Error(), "ambiguous")
	require.Zero(t, calls)
}
