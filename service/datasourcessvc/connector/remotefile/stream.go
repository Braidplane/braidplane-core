package remotefile

import (
	"context"
	"io"
	"math"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
)

// StreamReader retains backpressure instead of materializing provider bodies.
type StreamReader interface {
	ReadFileTo(context.Context, string, int64, io.Writer) (*filestream.Metadata, error)
}

// StreamWriteRequest supplies verified retained input to a provider adapter.
type StreamWriteRequest struct {
	Target      string
	Body        io.Reader
	Reference   filepayload.Reference
	ContentType string
	Metadata    map[string]string
	Overwrite   bool
}

type StreamWriter interface {
	WriteFileFrom(context.Context, StreamWriteRequest) (int64, error)
}

func (adapter Adapter) executeStreamRead(ctx context.Context, provider Provider, target string, maxBytes int64) (*result.FileResult, error) {
	store, ok := filestream.FromContext(ctx)
	if !ok {
		return nil, filestream.ErrUnavailable
	}
	reader, ok := provider.(StreamReader)
	if !ok {
		return nil, filestream.ErrUnavailable.WithPublicDetail("This file provider does not support bounded streaming reads.")
	}
	ref, metadata, err := store.Retain(ctx, maxBytes, func(dst io.Writer) (*filestream.Metadata, error) {
		return reader.ReadFileTo(ctx, target, maxBytes, dst)
	})
	if err != nil {
		return nil, err
	}
	return &result.FileResult{Format: "file", Operation: OperationRead, TargetPath: target, File: streamFileMetadata(target, metadata.ContentType, *ref), Reference: ref}, nil
}

func (adapter Adapter) executeStreamWrite(ctx context.Context, provider Provider, target string, stmt Statement, ref filepayload.Reference, maxBytes int64) (*result.FileResult, error) {
	if stmt.Format != filedata.QueryFormatFile {
		return nil, adapter.queryErrorf("retained file input requires format: file")
	}
	if _, err := filestream.ParseReference(ref, maxBytes); err != nil {
		return nil, err
	}
	if ref.SizeBytes > int64(math.MaxInt) {
		return nil, filestream.ErrUnavailable
	}
	store, ok := filestream.FromContext(ctx)
	if !ok {
		return nil, filestream.ErrUnavailable
	}
	writer, ok := provider.(StreamWriter)
	if !ok {
		return nil, filestream.ErrUnavailable.WithPublicDetail("This file provider does not support bounded streaming writes.")
	}
	body, err := store.Open(ctx, ref, maxBytes)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	overwrite := true
	if stmt.Overwrite != nil {
		overwrite = *stmt.Overwrite
	}
	contentType := strings.TrimSpace(stmt.ContentType)
	if contentType == "" {
		contentType = filedata.MIMETypeForPath(target, "")
	}
	written, err := writer.WriteFileFrom(ctx, StreamWriteRequest{Target: target, Body: body, Reference: ref, ContentType: contentType, Metadata: stmt.Metadata, Overwrite: overwrite})
	if err != nil {
		return nil, err
	}
	if written != ref.SizeBytes {
		return nil, filestream.ErrUnavailable.WithPublicDetail("Destination byte count differs from retained input; verify the write outcome before recovery.")
	}
	writtenBytes := int(ref.SizeBytes)
	return &result.FileResult{Format: "file", Operation: OperationWrite, TargetPath: target, WrittenBytes: &writtenBytes, File: streamFileMetadata(target, contentType, ref)}, nil
}

func streamFileMetadata(target, contentType string, ref filepayload.Reference) *filepayload.FilePayloadMetadata {
	return &filepayload.FilePayloadMetadata{Filename: filepayload.NormalizeFilePayloadFilename(target), MIMEType: filedata.MIMETypeForPath(target, contentType), SizeBytes: ref.SizeBytes, DigestAlgorithm: filepayload.FilePayloadDigestSHA256, Digest: ref.SHA256}
}
