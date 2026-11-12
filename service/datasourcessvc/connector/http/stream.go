package http

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

type streamRequestKey struct{}

// Close may race with the transport's body reader. Cancel blocked chunk reads
// before acquiring the lock, then release the underlying retained reader.
type streamRequestBody struct {
	mu       sync.Mutex
	reader   io.ReadCloser
	verified *filestream.VerifiedReader
	cancel   context.CancelFunc
	closed   bool
	complete atomic.Bool
}

func (b *streamRequestBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, filestream.ErrUnavailable
	}
	n, err := b.verified.Read(p)
	if b.verified.Complete() {
		b.complete.Store(true)
	}
	return n, err
}

func (b *streamRequestBody) Close() error {
	b.cancel()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	if err := b.reader.Close(); err != nil {
		return xerror.Unexpected().With(err)
	}
	return nil
}

func openStreamRequest(ctx context.Context, ref filepayload.Reference, maxBytes int64) (*streamRequestBody, error) {
	if _, err := filestream.ParseReference(ref, maxBytes); err != nil {
		return nil, err
	}
	store, ok := filestream.FromContext(ctx)
	if !ok {
		return nil, filestream.ErrUnavailable
	}
	bodyCtx, cancel := context.WithCancel(ctx)
	reader, err := store.Open(bodyCtx, ref, maxBytes)
	if err != nil {
		cancel()
		return nil, err
	}
	verified, err := filestream.NewVerifiedReader(bodyCtx, reader, ref.SizeBytes, ref.SHA256)
	if err != nil {
		cancel()
		_ = reader.Close()
		return nil, err
	}
	return &streamRequestBody{reader: reader, verified: verified, cancel: cancel}, nil
}

func httpResponseHeaders(headers http.Header) map[string]string {
	output := make(map[string]string, len(headers))
	for name := range headers {
		output[name] = headers.Get(name)
	}
	return output
}

func (c *Connector) streamFileResponse(ctx context.Context, response *http.Response, method string, limits types.ExecutionLimits, start time.Time) (*result.ExecutionResult, error) {
	fail := func(err error) (*result.ExecutionResult, error) {
		return result.NewHTTPResult(nil, time.Since(start).Milliseconds(), err), nil
	}
	if c.responseExposesSecret(response.Header, nil) {
		return fail(xerror.HTTPAcknowledgementUncertain().WithPublicDetail("HTTP response contained credential material and was withheld. Verify remote state before retrying a write."))
	}
	store, ok := filestream.FromContext(ctx)
	if !ok {
		return fail(filestream.ErrUnavailable)
	}
	if method != http.MethodHead && response.ContentLength > limits.MaxFileBytes {
		return fail(xerror.ExecutionResponseSizeExceeded())
	}
	ref, metadata, err := store.Retain(ctx, limits.MaxFileBytes, func(dst io.Writer) (*filestream.Metadata, error) {
		if secrets := c.credentialSecrets(); len(secrets) > 0 {
			dst = newSecretStreamWriter(dst, secrets)
		}
		metadata, err := filestream.Copy(ctx, dst, response.Body, limits.MaxFileBytes)
		if err != nil {
			return nil, err
		}
		if method != http.MethodHead && response.ContentLength >= 0 && metadata.SizeBytes != response.ContentLength {
			return nil, xerror.ExecutionFailed().With(io.ErrUnexpectedEOF)
		}
		return metadata, nil
	})
	if err != nil {
		return fail(err)
	}
	headers := httpResponseHeaders(response.Header)
	file := &filepayload.FilePayloadMetadata{Filename: httpResponseFilename(headers), MIMEType: filepayload.NormalizeFilePayloadMIMEType(response.Header.Get("Content-Type")), SizeBytes: metadata.SizeBytes, DigestAlgorithm: filepayload.FilePayloadDigestSHA256, Digest: metadata.SHA256}
	return result.NewHTTPResult(&result.HTTPResult{StatusCode: response.StatusCode, Headers: headers, File: file, Reference: ref}, time.Since(start).Milliseconds(), nil), nil
}
