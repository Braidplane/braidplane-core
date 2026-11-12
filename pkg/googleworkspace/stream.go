package googleworkspace

import (
	"context"
	"io"
	"net/http"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/googleclient"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
)

type streamDownloadKey struct{}

func (c *Client) DownloadTo(ctx context.Context, id string, limits Limits, maxBytes int64, dst io.Writer) (*filestream.Metadata, error) {
	return c.downloadTo(ctx, id, limits, maxBytes, dst, false)
}

func (c *Client) ExportPDFTo(ctx context.Context, id string, limits Limits, maxBytes int64, dst io.Writer) (*filestream.Metadata, error) {
	return c.downloadTo(ctx, id, limits, maxBytes, dst, true)
}

func (c *Client) downloadTo(ctx context.Context, id string, limits Limits, maxBytes int64, dst io.Writer, export bool) (*filestream.Metadata, error) {
	if maxBytes <= 0 || dst == nil {
		return nil, xerror.ValidationFailed()
	}
	if err := c.CheckAccess(false); err != nil {
		return nil, err
	}
	ctx, cancel := operationContext(ctx, limits)
	defer cancel()
	ctx = context.WithValue(ctx, streamDownloadKey{}, true)
	var response *http.Response
	var err error
	if export {
		response, err = c.drive.Files.Export(id, "application/pdf").Context(ctx).Download()
	} else {
		response, err = c.drive.Files.Get(id).SupportsAllDrives(true).Context(ctx).Download()
	}
	if err != nil {
		googleclient.LogError(ctx, err, c.credentials.AccessToken)
		return nil, safeError(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.ContentLength > maxBytes {
		return nil, xerror.ExecutionResponseSizeExceeded()
	}
	metadata, err := filestream.Copy(ctx, dst, response.Body, maxBytes)
	if err != nil {
		return nil, err
	}
	if response.ContentLength >= 0 && metadata.SizeBytes != response.ContentLength {
		return nil, xerror.ExecutionFailed().With(io.ErrUnexpectedEOF)
	}
	metadata.ContentType = response.Header.Get("Content-Type")
	return metadata, nil
}

// UploadFrom uses bounded SDK chunks. Completion still has provider-specific
// ambiguity; a successful source read alone cannot establish delivery.
func (c *Client) UploadFrom(ctx context.Context, id, folder, name, contentType string, body io.Reader, size int64, limits Limits) (*workspace.File, error) {
	if err := c.CheckAccess(true); err != nil {
		return nil, err
	}
	if body == nil || size < 0 || size > filestream.MaxRetainedBytes {
		return nil, xerror.ValidationFailed()
	}
	ctx, cancel := operationContext(ctx, limits)
	defer cancel()
	input := &io.LimitedReader{R: body, N: size + 1}
	options := []googleapi.MediaOption{googleapi.ChunkSize(filestream.ChunkBytes), googleapi.ContentType(contentType)}
	var output *drive.File
	var err error
	if id == "" {
		output, err = c.drive.Files.Create(&drive.File{Name: name, Parents: []string{folder}, MimeType: contentType}).SupportsAllDrives(true).Fields(fileFields+",size").Media(input, options...).Context(ctx).Do()
	} else {
		output, err = c.drive.Files.Update(id, &drive.File{}).SupportsAllDrives(true).Fields(fileFields+",size").Media(input, options...).Context(ctx).Do()
	}
	if err != nil {
		googleclient.LogError(ctx, err, c.credentials.AccessToken)
		return nil, nativeWriteError(err)
	}
	if output == nil || output.Id == "" || id != "" && output.Id != id || output.Size != size || input.N != 1 {
		return nil, xerror.ExecutionFailed().WithPublicDetail("Drive file write acknowledgement is incomplete; verify the destination before retrying.")
	}
	var extra [1]byte
	if _, err := io.ReadFull(input, extra[:]); err != io.EOF {
		return nil, xerror.ExecutionFailed().With(err).WithPublicDetail("Drive file write did not consume verified complete input; verify the destination before retrying.")
	}
	return fileMetadata(output), nil
}
