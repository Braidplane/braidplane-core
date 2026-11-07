package s3

import (
	"context"
	"io"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/writegate"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
	s3client "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/s3/client"
)

type streamingClient interface {
	ReadObjectTo(context.Context, s3client.GetObjectRequest, io.Writer) (*filestream.Metadata, error)
	PutObjectStream(context.Context, s3client.StreamWriteRequest) (*s3client.StreamWriteResult, error)
}

func (p s3ObjectProvider) ReadFileTo(ctx context.Context, target string, maxBytes int64, dst io.Writer) (*filestream.Metadata, error) {
	client, ok := p.client.(streamingClient)
	if !ok {
		return nil, filestream.ErrUnavailable
	}
	return client.ReadObjectTo(ctx, s3client.GetObjectRequest{Bucket: p.bucket, Key: target, MaxBytes: maxBytes}, dst)
}

func (p s3ObjectProvider) WriteFileFrom(ctx context.Context, req remotefile.StreamWriteRequest) (int64, error) {
	client, ok := p.client.(streamingClient)
	if !ok {
		return 0, filestream.ErrUnavailable
	}
	if err := writegate.BeforeWrite(ctx); err != nil {
		return 0, err
	}
	result, err := client.PutObjectStream(ctx, s3client.StreamWriteRequest{Bucket: p.bucket, Key: req.Target, Body: req.Body, SizeBytes: req.Reference.SizeBytes, MaxBytes: max(req.Reference.SizeBytes, int64(1)), SHA256: req.Reference.SHA256, ContentType: req.ContentType, Metadata: req.Metadata, Overwrite: req.Overwrite})
	if err != nil {
		return 0, err
	}
	if result == nil || result.OutcomeUnknown {
		return 0, filestream.ErrUnavailable
	}
	return result.WrittenBytes, nil
}
