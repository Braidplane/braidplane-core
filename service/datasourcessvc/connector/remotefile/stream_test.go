package remotefile

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/testutil/filestreamfixture"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/stretchr/testify/require"
)

type noBufferedProvider struct{}

func (noBufferedProvider) ReadFile(context.Context, string, int64) (*ReadResult, error) {
	panic("buffered read must not be used")
}

func (noBufferedProvider) WriteFile(context.Context, WriteRequest) (*WriteResult, error) {
	panic("buffered write must not be used")
}

type streamProviderFixture struct {
	noBufferedProvider
	content   []byte
	lastWrite StreamWriteRequest
	delta     int64
}

func (p *streamProviderFixture) ReadFileTo(ctx context.Context, _ string, maxBytes int64, dst io.Writer) (*filestream.Metadata, error) {
	return filestream.Copy(ctx, dst, bytes.NewReader(p.content), maxBytes)
}

func (p *streamProviderFixture) WriteFileFrom(ctx context.Context, req StreamWriteRequest) (int64, error) {
	p.lastWrite = req
	metadata, err := filestream.Copy(ctx, io.Discard, req.Body, max(req.Reference.SizeBytes, int64(1)))
	if err != nil {
		return 0, err
	}
	if metadata.SHA256 != req.Reference.SHA256 {
		return 0, filestream.ErrUnavailable
	}
	return metadata.SizeBytes + p.delta, nil
}

func TestAdapterStreamsRetainedFilesWithoutBufferedProviderCalls(t *testing.T) {
	t.Parallel()
	adapter := Adapter{ConnectorName: "fixture"}
	store := filestreamfixture.New()
	provider := &streamProviderFixture{content: bytes.Repeat([]byte("file"), 1000)}
	ctx := filestream.WithStore(t.Context(), store)
	limits := types.ExecutionLimits{MaxResponseBytes: 8, MaxFileBytes: int64(len(provider.content))}
	source, err := adapter.ExecuteRead(ctx, provider, "input.bin", Statement{Format: filedata.QueryFormatFile}, limits)
	require.NoError(t, err)
	require.Empty(t, source.Content)
	require.NotNil(t, source.Reference)
	require.Equal(t, source.File.SizeBytes, source.Reference.SizeBytes)
	overwrite := false
	sink, err := adapter.ExecuteWrite(ctx, provider, "output.bin", Statement{Format: filedata.QueryFormatFile, Content: *source.Reference, Overwrite: &overwrite}, limits)
	require.NoError(t, err)
	require.Empty(t, sink.Content)
	require.Nil(t, sink.Reference)
	require.Equal(t, len(provider.content), *sink.WrittenBytes)
	require.Equal(t, int32(1), store.Opens.Load())
	require.False(t, provider.lastWrite.Overwrite)
	provider.delta = -1
	_, err = adapter.ExecuteWrite(ctx, provider, "output.bin", Statement{Format: filedata.QueryFormatFile, Content: *source.Reference}, limits)
	require.ErrorIs(t, err, filestream.ErrUnavailable)
	limits.MaxFileBytes--
	_, err = adapter.ExecuteRead(ctx, provider, "input.bin", Statement{Format: filedata.QueryFormatFile}, limits)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
	_, err = adapter.ExecuteWrite(ctx, provider, "output.bin", Statement{Format: filedata.QueryFormatFile, Content: *source.Reference}, limits)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

func TestAdapterRefusesUnavailableStreamingInsteadOfBuffering(t *testing.T) {
	t.Parallel()
	adapter := Adapter{ConnectorName: "fixture"}
	limits := types.ExecutionLimits{MaxFileBytes: 1024}
	_, err := adapter.ExecuteRead(t.Context(), noBufferedProvider{}, "file.bin", Statement{Format: filedata.QueryFormatFile}, limits)
	require.ErrorIs(t, err, filestream.ErrUnavailable)
	ctx := filestream.WithStore(t.Context(), filestreamfixture.New())
	_, err = adapter.ExecuteRead(ctx, noBufferedProvider{}, "file.bin", Statement{Format: filedata.QueryFormatFile}, limits)
	require.ErrorIs(t, err, filestream.ErrUnavailable)
}
