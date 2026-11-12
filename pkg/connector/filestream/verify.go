package filestream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// VerifiedReader withholds the final read until exact length, EOF and digest
// agree. Providers must not publish partial input after any read error.
type VerifiedReader struct {
	ctx      context.Context
	src      io.Reader
	expected int64
	digest   string
	hash     hash.Hash
	size     int64
	done     bool
	failed   error
}

// Complete reports whether exact length, EOF and digest have been verified.
func (r *VerifiedReader) Complete() bool { return r.done }

func NewVerifiedReader(ctx context.Context, src io.Reader, size int64, digest string) (*VerifiedReader, error) {
	decoded, err := hex.DecodeString(digest)
	if src == nil || size < 0 || size > MaxRetainedBytes || err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != digest {
		return nil, ErrUnavailable
	}
	return &VerifiedReader{ctx: ctx, src: src, expected: size, digest: digest, hash: sha256.New()}, nil
}

func (r *VerifiedReader) Read(p []byte) (int, error) {
	if r.failed != nil {
		return 0, r.failed
	}
	if r.done {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if err := r.ctx.Err(); err != nil {
		r.failed = ErrUnavailable.With(err)
		return 0, r.failed
	}
	p = p[:min(int64(len(p)), r.expected-r.size)]
	var n int
	var err error
	if r.size < r.expected {
		n, err = r.src.Read(p)
	}
	r.size += int64(n)
	_, _ = r.hash.Write(p[:n])
	if err != nil && !xerror.Is(err, io.EOF) {
		r.failed = ErrUnavailable.With(err)
		return 0, r.failed
	}
	if r.size == r.expected {
		var extra [1]byte
		_, endErr := io.ReadFull(r.src, extra[:])
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			r.failed = ErrUnavailable.With(ctxErr)
			return 0, r.failed
		}
		if endErr != io.EOF || hex.EncodeToString(r.hash.Sum(nil)) != r.digest {
			r.failed = ErrUnavailable.With(endErr)
			return 0, r.failed
		}
		r.done = true
		return n, io.EOF
	}
	if xerror.Is(err, io.EOF) {
		r.failed = ErrUnavailable.With(io.ErrUnexpectedEOF)
		return 0, r.failed
	}
	return n, nil
}
