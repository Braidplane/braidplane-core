package googleworkspace

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/testutil/filestreamfixture"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDriveConnectorStreamsAuthorizedFilesWithRetainedInput(t *testing.T) {
	content := bytes.Repeat([]byte("file"), 10000)
	uploads := 0
	connector := testFileConnector(t, func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.Query().Get("alt") == "media":
			output := response(string(content))
			output.ContentLength = int64(len(content))
			return output, nil
		case r.URL.Path == "/drive/v3/files/file":
			return response(`{"id":"file","name":"input.bin","mimeType":"application/octet-stream","isAppAuthorized":true,"capabilities":{"canDownload":true,"canEdit":true}}`), nil
		case r.URL.Path == "/drive/v3/files/folder":
			return response(`{"id":"folder","name":"exports","mimeType":"application/vnd.google-apps.folder","isAppAuthorized":true,"capabilities":{"canAddChildren":true}}`), nil
		case r.URL.Path == "/upload/drive/v3/files" || r.URL.Path == "/upload/drive/v3/files/file":
			uploads++
			body, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			assert.True(t, bytes.Contains(body, content))
			id := "created"
			if strings.HasSuffix(r.URL.Path, "/file") {
				assert.Equal(t, http.MethodPatch, r.Method)
				id = "file"
			} else {
				assert.Equal(t, http.MethodPost, r.Method)
			}
			return response(fmt.Sprintf(`{"id":"%s","name":"output.bin","mimeType":"application/octet-stream","size":"%d"}`, id, len(content))), nil
		default:
			return nil, &url.Error{Err: io.ErrUnexpectedEOF}
		}
	})
	ctx := filestream.WithStore(t.Context(), filestreamfixture.New())
	limits := types.ExecutionLimits{MaxResponseBytes: 1024, MaxFileBytes: int64(len(content))}
	source, err := connector.Execute(ctx, []byte(`{"type":"google_workspace","resource":"file","operation":"read","file_id":"file","format":"file"}`), nil, limits)
	require.NoError(t, err)
	require.NoError(t, source.Error())
	file, ok := source.File()
	require.True(t, ok)
	require.NotNil(t, file.Reference)
	require.Empty(t, file.Content)
	params := map[string]types.TypedParam{sinkbinding.Input: {Value: *file.Reference}, sinkbinding.InternalIdempotencyClass: {Value: "unsafe"}}
	for _, tc := range []struct {
		operation string
		id        string
	}{
		{`{"type":"google_workspace","resource":"file","operation":"create","folder_id":"folder","name":"output.bin","format":"file","content":"{{input}}"}`, "created"},
		{`{"type":"google_workspace","resource":"file","operation":"update","file_id":"file","format":"file","content":"{{input}}"}`, "file"},
	} {
		sink, err := connector.Execute(ctx, []byte(tc.operation), params, limits)
		require.NoError(t, err)
		require.NoError(t, sink.Error())
		written, ok := sink.File()
		require.True(t, ok)
		require.Equal(t, tc.id, written.ResourceID)
		require.Equal(t, len(content), *written.WrittenBytes)
		require.Empty(t, written.Content)
	}
	require.Equal(t, 2, uploads)
}

func TestNativePDFExportUsesRetainedStreaming(t *testing.T) {
	const content = "%PDF-synthetic"
	connector := testFileConnector(t, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/export") {
			output := response(content)
			output.ContentLength = int64(len(content))
			output.Header.Set("Content-Type", "application/pdf")
			return output, nil
		}
		return response(`{"id":"native","name":"Report","mimeType":"application/vnd.google-apps.document","isAppAuthorized":true,"capabilities":{"canDownload":true}}`), nil
	})
	store := filestreamfixture.New()
	ctx := filestream.WithStore(t.Context(), store)
	output, err := connector.Execute(ctx, []byte(`{"type":"google_workspace","resource":"document","operation":"export","file_id":"native"}`), nil, types.ExecutionLimits{MaxFileBytes: 1024})
	require.NoError(t, err)
	require.NoError(t, output.Error())
	file, ok := output.File()
	require.True(t, ok)
	require.NotNil(t, file.Reference)
	require.Empty(t, file.Content)
	require.Equal(t, "Report.pdf", file.File.Filename)
	reader, err := store.Open(ctx, *file.Reference, 1024)
	require.NoError(t, err)
	defer func() { require.NoError(t, reader.Close()) }()
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, content, string(body))
}
