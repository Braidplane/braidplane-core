package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const streamPartBytes = 8 * 1024 * 1024

// ReadObjectTo consumes one finite object with backpressure from the destination.
// The caller owns the writer and must discard partial output on any error.
func (c *Client) ReadObjectTo(ctx context.Context, req GetObjectRequest, dst io.Writer) (*filestream.Metadata, error) {
	if req.MaxBytes <= 0 || dst == nil {
		return nil, xerror.ValidationFailed().Detail("streaming reads require a positive byte limit and destination")
	}
	resp, err := c.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(req.Bucket), Key: aws.String(req.Key)})
	if err != nil {
		return nil, mapProviderError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.ContentLength != nil && *resp.ContentLength > req.MaxBytes {
		return nil, xerror.ExecutionResponseSizeExceeded()
	}
	metadata, err := filestream.Copy(ctx, dst, resp.Body, req.MaxBytes)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength != nil && metadata.SizeBytes != *resp.ContentLength {
		return nil, xerror.Unexpected().With(io.ErrUnexpectedEOF)
	}
	metadata.ContentType = aws.ToString(resp.ContentType)
	return metadata, nil
}

// StreamWriteRequest requires the length and digest of retained, verified input.
type StreamWriteRequest struct {
	Bucket      string
	Key         string
	Body        io.Reader
	SizeBytes   int64
	MaxBytes    int64
	SHA256      string
	ContentType string
	Metadata    map[string]string
	Overwrite   bool
}

// StreamWriteResult distinguishes a complete acknowledgement from an uncertain one.
type StreamWriteResult struct {
	WrittenBytes   int64
	SHA256         string
	OutcomeUnknown bool
}

// PutObjectStream uploads sequential bounded parts, publishing only after input
// verification. An error after completion starts requires independent readback;
// callers must never infer that an existing object can safely be overwritten.
func (c *Client) PutObjectStream(ctx context.Context, req StreamWriteRequest) (_ *StreamWriteResult, retErr error) {
	digest, err := hex.DecodeString(req.SHA256)
	if req.Body == nil || req.SizeBytes < 0 || req.MaxBytes <= 0 || req.SizeBytes > req.MaxBytes || err != nil || len(digest) != sha256.Size || req.SizeBytes > int64(streamPartBytes)*10000 {
		return nil, xerror.ValidationFailed().Detail("streaming writes require verified input within the byte and multipart limits")
	}
	created, err := c.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(req.Bucket), Key: aws.String(req.Key), ContentType: aws.String(req.ContentType), Metadata: cloneMetadata(req.Metadata),
	}, func(o *s3.Options) { o.RetryMaxAttempts = 1 })
	if err != nil {
		return nil, mapProviderError(err)
	}
	if aws.ToString(created.UploadId) == "" {
		return nil, xerror.Unexpected().Detail("S3 returned no multipart upload ID")
	}
	completed := false
	defer func() {
		if completed {
			return
		}
		// Cancellation must not prevent releasing provider-side partial parts.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, abortErr := c.client.AbortMultipartUpload(cleanupCtx, &s3.AbortMultipartUploadInput{Bucket: aws.String(req.Bucket), Key: aws.String(req.Key), UploadId: created.UploadId})
		if abortErr != nil {
			retErr = xerror.Unexpected().With(xerror.Join(retErr, mapProviderError(abortErr))).Detail("multipart cleanup failed; provider lifecycle cleanup is required")
		}
	}()
	hash := sha256.New()
	remaining := req.SizeBytes
	buffer := make([]byte, streamPartBytes)
	parts := make([]s3types.CompletedPart, 0)
	for remaining > 0 || len(parts) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, mapProviderError(err)
		}
		size := min(remaining, int64(len(buffer)))
		part := buffer[:int(size)]
		if _, err := io.ReadFull(req.Body, part); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		_, _ = hash.Write(part)
		// #nosec G115 -- the size bound caps the upload at 10,000 parts.
		number := int32(len(parts) + 1)
		uploaded, err := c.client.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(req.Bucket), Key: aws.String(req.Key), UploadId: created.UploadId, PartNumber: aws.Int32(number), Body: bytes.NewReader(part), ContentLength: aws.Int64(size)})
		if err != nil {
			return nil, mapProviderError(err)
		}
		if aws.ToString(uploaded.ETag) == "" {
			return nil, xerror.Unexpected().Detail("S3 returned no multipart part ETag")
		}
		parts = append(parts, s3types.CompletedPart{ETag: uploaded.ETag, PartNumber: aws.Int32(number)})
		remaining -= size
	}
	var extra [1]byte
	if _, err := io.ReadFull(req.Body, extra[:]); err != io.EOF {
		if err == nil {
			return nil, xerror.ValidationFailed().Detail("stream input exceeds verified length")
		}
		return nil, xerror.Unexpected().With(err)
	}
	if !bytes.Equal(hash.Sum(nil), digest) {
		return nil, xerror.ValidationFailed().Detail("stream input checksum differs from retained input")
	}
	input := &s3.CompleteMultipartUploadInput{Bucket: aws.String(req.Bucket), Key: aws.String(req.Key), UploadId: created.UploadId, MultipartUpload: &s3types.CompletedMultipartUpload{Parts: parts}}
	if !req.Overwrite {
		input.IfNoneMatch = aws.String("*")
	}
	// A lost completion acknowledgement cannot authorize a second publication.
	_, err = c.client.CompleteMultipartUpload(ctx, input, func(o *s3.Options) { o.RetryMaxAttempts = 1 })
	if err != nil {
		return &StreamWriteResult{OutcomeUnknown: true}, mapProviderError(err)
	}
	completed = true
	return &StreamWriteResult{WrittenBytes: req.SizeBytes, SHA256: hex.EncodeToString(digest)}, nil
}
