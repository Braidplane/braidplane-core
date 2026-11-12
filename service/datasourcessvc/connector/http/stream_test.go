package http

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/transportsecurity"
	runtimevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/testutil/filestreamfixture"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPStreamsNativeFilesAndReopensRetainedInputForKeyedRetry(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat([]byte("file"), 10000)
	var uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(content)
			return
		}
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Equal(t, content, body)
		w.Header().Set("Content-Type", "application/json")
		if uploads.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, `{"accepted":true}`)
	}))
	t.Cleanup(server.Close)
	store := filestreamfixture.New()
	ctx := filestream.WithStore(transportsecurity.WithPlaintext(t.Context()), store)
	connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connector.Close()) })
	limits := types.ExecutionLimits{MaxResponseBytes: 1024, HTTPMaxResponseBytes: 1024, HTTPMaxRequestBytes: 1024, MaxFileBytes: int64(len(content)), HTTPRetryAttempts: 2}
	source, err := connector.Execute(ctx, []byte(`{"method":"GET","path":"/file"}`), nil, limits)
	require.NoError(t, err)
	require.NoError(t, source.Error())
	file, ok := source.HTTP()
	require.True(t, ok)
	require.Empty(t, file.RawBody)
	require.NotNil(t, file.Reference)
	require.Equal(t, int64(len(content)), file.File.SizeBytes)
	params := writeRetryParams()
	params[sinkbinding.Input] = types.TypedParam{Value: *file.Reference}
	params[sinkbinding.InternalInputRepresentation] = types.TypedParam{Value: "file"}
	params[sinkbinding.InternalInputFileMIMEType] = types.TypedParam{Value: "application/octet-stream"}
	delivered, err := connector.Execute(runtimevars.WithSinkExecution(ctx), []byte(writeRetryOperation), params, limits)
	require.NoError(t, err)
	require.NoError(t, delivered.Error())
	require.Equal(t, int32(2), uploads.Load())
	require.Equal(t, int32(2), store.Opens.Load())
	limits.MaxFileBytes--
	failed, err := connector.Execute(ctx, []byte(`{"method":"GET","path":"/file"}`), nil, limits)
	require.NoError(t, err)
	require.ErrorIs(t, failed.Error(), xerror.ExecutionResponseSizeExceeded())
}

func TestHTTPStreamingWithholdsEscapedCredentialEcho(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, `{"value":"\u0063redential"}`)
	}))
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		name        string
		credentials Credentials
	}{
		{"secret header", Credentials{SecretHeaders: map[string]string{"X-Fixture": "credential"}}},
		{"bearer", Credentials{BearerToken: "credential"}},
		{"API key", Credentials{APIKey: "credential", APIKeyHeader: "X-Api-Key"}},
		{"basic password", Credentials{Username: "fixture", Password: "credential"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := filestream.WithStore(transportsecurity.WithPlaintext(t.Context()), filestreamfixture.New())
			connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, tc.credentials)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, connector.Close()) })
			output, err := connector.Execute(ctx, []byte(`{"method":"GET","path":"/file"}`), nil, types.ExecutionLimits{MaxResponseBytes: 8, MaxFileBytes: 1024})
			require.NoError(t, err)
			require.ErrorIs(t, output.Error(), xerror.HTTPAcknowledgementUncertain())
		})
	}
}

func TestHTTPStreamingWriteRefusesRedirectAndRejectedAcknowledgement(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusFound, http.StatusBadRequest} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var redirected atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/destination" {
					redirected.Add(1)
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("Location", "/destination")
				w.WriteHeader(status)
			}))
			t.Cleanup(server.Close)
			store := filestreamfixture.New()
			ctx := filestream.WithStore(transportsecurity.WithPlaintext(t.Context()), store)
			ref, _, err := store.Retain(ctx, 1024, func(dst io.Writer) (*filestream.Metadata, error) {
				return filestream.Copy(ctx, dst, strings.NewReader("fixture"), 1024)
			})
			require.NoError(t, err)
			connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, connector.Close()) })
			params := map[string]types.TypedParam{sinkbinding.Input: {Value: *ref}, sinkbinding.InternalInputRepresentation: {Value: "file"}}
			output, err := connector.Execute(ctx, []byte(`{"method":"POST","path":"/file","body":"{{input}}"}`), params, types.ExecutionLimits{MaxFileBytes: 1024})
			require.NoError(t, err)
			if status == http.StatusFound {
				require.ErrorIs(t, output.Error(), xerror.HTTPAcknowledgementUncertain())
			} else {
				require.ErrorIs(t, output.Error(), xerror.HTTPAcknowledgementRejected())
			}
			require.Zero(t, redirected.Load())
		})
	}
}

func TestHTTPStreamingWriteRefusesPrematureSuccess(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var first [1]byte
		_, _ = io.ReadFull(r.Body, first[:])
		w.Header().Set("Connection", "close")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"accepted":true}`)
	}))
	t.Cleanup(server.Close)
	store := filestreamfixture.New()
	ctx, cancel := context.WithTimeout(transportsecurity.WithPlaintext(t.Context()), 5*time.Second)
	defer cancel()
	ctx = filestream.WithStore(ctx, store)
	const size = 8 << 20
	ref, _, err := store.Retain(ctx, size, func(dst io.Writer) (*filestream.Metadata, error) {
		return filestream.Copy(ctx, dst, strings.NewReader(strings.Repeat("x", size)), size)
	})
	require.NoError(t, err)
	connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connector.Close()) })
	params := map[string]types.TypedParam{sinkbinding.Input: {Value: *ref}, sinkbinding.InternalInputRepresentation: {Value: "file"}}
	output, err := connector.Execute(ctx, []byte(`{"method":"POST","path":"/file","body":"{{input}}"}`), params, types.ExecutionLimits{MaxFileBytes: size})
	require.NoError(t, err)
	require.ErrorIs(t, output.Error(), xerror.HTTPAcknowledgementUncertain())
}

func TestHTTPStreamRequestCompletionRequiresVerifiedEOF(t *testing.T) {
	t.Parallel()
	store := filestreamfixture.New()
	ctx := filestream.WithStore(t.Context(), store)
	ref, _, err := store.Retain(ctx, 4, func(dst io.Writer) (*filestream.Metadata, error) {
		return filestream.Copy(ctx, dst, strings.NewReader("good"), 4)
	})
	require.NoError(t, err)
	for _, valid := range []bool{true, false} {
		input := *ref
		if !valid {
			input.SHA256 = strings.Repeat("0", 64)
		}
		body, err := openStreamRequest(ctx, input, 4)
		require.NoError(t, err)
		_, readErr := io.ReadAll(body)
		if valid {
			require.NoError(t, readErr)
		} else {
			require.ErrorIs(t, readErr, filestream.ErrUnavailable)
		}
		require.Equal(t, valid, body.complete.Load())
		require.NoError(t, body.Close())
	}
}

func TestHTTPStreamingWriteAcceptsVerifiedEmptyInput(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Empty(t, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(server.Close)
	store := filestreamfixture.New()
	ctx := filestream.WithStore(transportsecurity.WithPlaintext(t.Context()), store)
	ref, _, err := store.Retain(ctx, 1, func(dst io.Writer) (*filestream.Metadata, error) {
		return filestream.Copy(ctx, dst, strings.NewReader(""), 1)
	})
	require.NoError(t, err)
	connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connector.Close()) })
	params := map[string]types.TypedParam{sinkbinding.Input: {Value: *ref}, sinkbinding.InternalInputRepresentation: {Value: "file"}}
	output, err := connector.Execute(ctx, []byte(`{"method":"POST","path":"/file","body":"{{input}}"}`), params, types.ExecutionLimits{MaxFileBytes: 1})
	require.NoError(t, err)
	require.NoError(t, output.Error())
}

func TestHTTPStreamingReadRefusesTruncatedFile(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "16")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "part")
	}))
	t.Cleanup(server.Close)
	ctx := filestream.WithStore(transportsecurity.WithPlaintext(t.Context()), filestreamfixture.New())
	connector, err := New(ctx, Config{BaseURL: server.URL, AllowPrivateHosts: true}, Credentials{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connector.Close()) })
	output, err := connector.Execute(ctx, []byte(`{"method":"GET","path":"/file"}`), nil, types.ExecutionLimits{MaxResponseBytes: 1, MaxFileBytes: 16})
	require.NoError(t, err)
	require.ErrorIs(t, output.Error(), io.ErrUnexpectedEOF)
	file, ok := output.HTTP()
	require.True(t, ok)
	require.Nil(t, file, "partial content must not produce a file reference")
}
