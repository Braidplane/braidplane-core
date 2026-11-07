// Package filestream provides bounded byte transport shared by native file
// readers and producers that serialize records incrementally.
package filestream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// Metadata describes fully consumed bytes. It never contains the file body.
type Metadata struct {
	SizeBytes   int64
	SHA256      string
	ContentType string
}

// Copy applies synchronous backpressure and never writes beyond maxBytes.
// Any error invalidates the partial destination; callers must not publish it.
// Readers and writers must honor their operation context while blocked in I/O.
func Copy(ctx context.Context, dst io.Writer, src io.Reader, maxBytes int64) (*Metadata, error) {
	if src == nil || dst == nil || maxBytes <= 0 {
		return nil, xerror.ValidationFailed().Detail("file streams require a source, destination and positive byte limit")
	}
	hash := sha256.New()
	writer := &contextWriter{ctx: ctx, dst: io.MultiWriter(dst, hash)}
	n, err := io.CopyBuffer(writer, io.LimitReader(&contextReader{ctx: ctx, src: src}, maxBytes), make([]byte, 64*1024))
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	var extra [1]byte
	if err := ctx.Err(); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if _, err := io.ReadFull(src, extra[:]); err != io.EOF {
		if err == nil {
			return nil, xerror.ExecutionResponseSizeExceeded()
		}
		return nil, xerror.Unexpected().With(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return &Metadata{SizeBytes: n, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

type contextReader struct {
	ctx context.Context
	src io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.src.Read(p)
	if err == io.EOF {
		return n, io.EOF
	}
	if err != nil {
		return n, xerror.Unexpected().With(err)
	}
	return n, nil
}

type contextWriter struct {
	ctx context.Context
	dst io.Writer
}

func (w *contextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.dst.Write(p)
	if err != nil {
		return n, xerror.Unexpected().With(err)
	}
	return n, nil
}
