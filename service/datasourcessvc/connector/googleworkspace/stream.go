package googleworkspace

import (
	"context"
	"io"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	provider "github.com/Braidplane/braidplane-core/pkg/googleworkspace"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/remotefile"
)

func (p *driveFileProvider) ReadFileTo(ctx context.Context, _ string, maxBytes int64, dst io.Writer) (*filestream.Metadata, error) {
	metadata, err := p.client.DownloadTo(ctx, p.statement.FileID, p.limits, maxBytes, dst)
	if err != nil {
		return nil, err
	}
	metadata.ContentType = p.metadata.MIMEType
	return metadata, nil
}

func (p *driveFileProvider) WriteFileFrom(ctx context.Context, req remotefile.StreamWriteRequest) (int64, error) {
	metadata, err := p.client.UploadFrom(ctx, p.statement.FileID, p.statement.FolderID, p.statement.Name, req.ContentType, req.Body, req.Reference.SizeBytes, p.limits)
	if err != nil {
		return 0, err
	}
	p.written = metadata
	return req.Reference.SizeBytes, nil
}

func (c *Connector) streamPDF(ctx context.Context, s workspace.Statement, metadata *workspace.File, limits provider.Limits, maxBytes int64, start time.Time) (*result.ExecutionResult, error) {
	store, ok := filestream.FromContext(ctx)
	if !ok {
		return executionError(s, start, filestream.ErrUnavailable), nil
	}
	ref, _, err := store.Retain(ctx, maxBytes, func(dst io.Writer) (*filestream.Metadata, error) {
		return c.client.ExportPDFTo(ctx, s.FileID, limits, maxBytes, dst)
	})
	if err != nil {
		return executionError(s, start, err), nil
	}
	file := &filepayload.FilePayloadMetadata{Filename: filepayload.NormalizeFilePayloadFilename(metadata.Name + ".pdf"), MIMEType: "application/pdf", SizeBytes: ref.SizeBytes, DigestAlgorithm: filepayload.FilePayloadDigestSHA256, Digest: ref.SHA256}
	return result.NewWorkspaceFileResult(&result.FileResult{ResourceID: metadata.FileID, TargetPath: metadata.FileID, Name: metadata.Name, MIMEType: "application/pdf", Operation: "export", Format: "file", File: file, Reference: ref}, time.Since(start).Milliseconds(), nil), nil
}
