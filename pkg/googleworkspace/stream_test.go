package googleworkspace

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDriveStreamingDownloadDoesNotUseResponseEnvelopeLimit(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("file"), 300000)
	client := newDriveTestClient(t, Credentials{AccessToken: "fixture", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}}, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "media", r.URL.Query().Get("alt"))
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(content)
	})
	var output bytes.Buffer
	metadata, err := client.DownloadTo(t.Context(), "fixture", Limits{MaxResponseBytes: 8}, int64(len(content)), &output)
	require.NoError(t, err)
	require.Equal(t, content, output.Bytes())
	require.Equal(t, int64(len(content)), metadata.SizeBytes)
	_, err = client.DownloadTo(t.Context(), "fixture", Limits{MaxResponseBytes: 8}, int64(len(content)-1), io.Discard)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

func TestDriveStreamingUploadUsesBoundedResumableChunks(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("file"), 700000)
	uploaded := 0
	client := newDriveTestClient(t, Credentials{AccessToken: "fixture", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("uploadType") == "resumable" {
			w.Header().Set("Location", "https://drive.test/session")
			return
		}
		assert.Equal(t, http.MethodPost, r.Method)
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.LessOrEqual(t, len(body), filestream.ChunkBytes)
		assert.Equal(t, content[uploaded:uploaded+len(body)], body)
		uploaded += len(body)
		if strings.HasSuffix(r.Header.Get("Content-Range"), "/*") {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", uploaded-1))
			w.Header().Set("X-Http-Status-Code-Override", "308")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"created","name":"fixture.bin","size":"%d"}`, len(content))
	})
	output, err := client.UploadFrom(t.Context(), "", "folder", "fixture.bin", "application/octet-stream", bytes.NewReader(content), int64(len(content)), Limits{MaxRequestBytes: 2 * filestream.ChunkBytes})
	require.NoError(t, err)
	require.Equal(t, "created", output.FileID)
	require.Equal(t, len(content), uploaded)
}

func TestDriveStreamingUploadRefusesPrematureCompletion(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("file"), 700000)
	body := bytes.NewReader(content)
	uploaded := 0
	client := newDriveTestClient(t, Credentials{AccessToken: "fixture", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}}, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("uploadType") == "resumable" {
			w.Header().Set("Location", "https://drive.test/session")
			return
		}
		n, err := io.Copy(io.Discard, r.Body)
		assert.NoError(t, err)
		uploaded += int(n)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"created","name":"fixture.bin","size":"%d"}`, len(content))
	})
	output, err := client.UploadFrom(t.Context(), "", "folder", "fixture.bin", "application/octet-stream", body, int64(len(content)), Limits{MaxRequestBytes: 2 * filestream.ChunkBytes})
	require.Less(t, uploaded, len(content), "fixture must acknowledge before the complete input is sent")
	require.Error(t, err)
	require.Nil(t, output)
}

func TestDriveStreamingUploadChecksInputLengthDespiteAcknowledgement(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"ab", "abcd", "abcde"} {
		t.Run(body, func(t *testing.T) {
			client := newDriveTestClient(t, Credentials{AccessToken: "fixture", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}}, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"created","name":"fixture.bin","size":"4"}`)
			})
			output, err := client.UploadFrom(t.Context(), "", "folder", "fixture.bin", "application/octet-stream", strings.NewReader(body), 4, Limits{})
			if len(body) == 4 {
				require.NoError(t, err)
				require.NotNil(t, output)
			} else {
				require.Error(t, err)
				require.Nil(t, output)
			}
		})
	}
}

func TestDriveStreamingDownloadRejectsBrokenOrCancelledBody(t *testing.T) {
	t.Parallel()
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprint(blocked), func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "16")
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = io.WriteString(w, "part")
				w.(http.Flusher).Flush()
				if blocked {
					<-r.Context().Done()
				}
			}))
			t.Cleanup(server.Close)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			client, err := New(ctx, Credentials{AccessToken: "fixture", GrantedScopes: []string{"https://www.googleapis.com/auth/drive.file"}})
			require.NoError(t, err)
			client.client.Transport = server.Client().Transport
			client.drive.BasePath = server.URL + "/drive/"
			var output bytes.Buffer
			var dst io.Writer = &output
			if blocked {
				dst = cancelDownloadWriter{dst: &output, cancel: cancel}
			}
			metadata, err := client.DownloadTo(ctx, "file", Limits{MaxResponseBytes: 1}, 16, dst)
			require.Error(t, err)
			require.Nil(t, metadata)
			require.Equal(t, "part", output.String())
			if blocked {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorIs(t, err, io.ErrUnexpectedEOF)
			}
		})
	}
}

type cancelDownloadWriter struct {
	dst    io.Writer
	cancel context.CancelFunc
}

func (w cancelDownloadWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	w.cancel()
	return n, err
}
