package s3

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	"github.com/Braidplane/braidplane-core/pkg/connector/writegate"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/testutil/filestreamfixture"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3ConnectorStreamsRetainedFileThroughSDKAndWriteGuard(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("file"), 10000)
	published := false
	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			assert.Equal(t, "/fixture/scoped/input.bin", r.URL.Path)
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(content)
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			uploads++
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>fixture-upload</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut:
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			assert.Equal(t, content, body)
			w.Header().Set("ETag", `"part"`)
		case r.Method == http.MethodPost:
			assert.Equal(t, "*", r.Header.Get("If-None-Match"))
			published = true
			_, _ = io.WriteString(w, `<CompleteMultipartUploadResult><ETag>"complete"</ETag></CompleteMultipartUploadResult>`)
		default:
			t.Errorf("unexpected request: %s", r.Method)
			w.WriteHeader(400)
		}
	}))
	t.Cleanup(server.Close)
	ctx := filestream.WithStore(transportsecurity.WithPlaintext(t.Context()), filestreamfixture.New())
	connector, err := New(ctx, Config{Endpoint: server.URL, Region: "us-east-1", Bucket: "fixture", Prefix: "scoped", ForcePathStyle: true, AllowPrivateHosts: true}, Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connector.Close()) })
	limits := types.ExecutionLimits{MaxResponseBytes: 1024, MaxFileBytes: int64(len(content))}
	source, err := connector.Execute(ctx, []byte(`{"operation":"read","key":"input.bin","format":"file"}`), nil, limits)
	require.NoError(t, err)
	require.NoError(t, source.Error())
	file, ok := source.File()
	require.True(t, ok)
	require.NotNil(t, file.Reference)
	require.Empty(t, file.Content)
	operation := []byte(`{"operation":"write","key":"output.bin","format":"file","content":"{{input}}","overwrite":false}`)
	params := map[string]types.TypedParam{sinkbinding.Input: {Value: *file.Reference}}
	prepared, err := connector.Execute(writegate.WithBeforeWrite(ctx, func(_ context.Context) error { return writegate.ErrSkipped }), operation, params, limits)
	require.NoError(t, err)
	require.ErrorIs(t, prepared.Error(), writegate.ErrSkipped)
	require.Zero(t, uploads)
	output, err := connector.Execute(ctx, operation, params, limits)
	require.NoError(t, err)
	require.NoError(t, output.Error())
	require.True(t, published)
	require.Equal(t, 1, uploads)
	written, ok := output.File()
	require.True(t, ok)
	require.Equal(t, len(content), *written.WrittenBytes)
	require.Empty(t, written.Content)
}
