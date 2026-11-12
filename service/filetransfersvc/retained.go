package filetransfersvc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"strconv"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
)

type Repository interface {
	Begin(context.Context, postgres.DBTX, filestream.Claim, int64, time.Time) error
	Append(context.Context, postgres.DBTX, filestream.Claim, int32, int32, []byte) error
	Seal(context.Context, postgres.DBTX, filestream.Claim, int64, string) error
	Read(context.Context, postgres.DBTX, filestream.Claim, uuid.UUID) (filestream.Manifest, error)
	ReadChunk(context.Context, postgres.DBTX, filestream.Claim, uuid.UUID, int32) ([]byte, error)
	Discard(context.Context, postgres.DBTX, filestream.Claim) error
}

// RetainedStore binds every storage operation to an execution claim. It does not
// grant authority to caller-supplied artifact IDs outside that organization.
type RetainedStore struct {
	txm     postgres.TransactionManager
	cipher  crypto.AES256
	repo    Repository
	claim   filestream.Claim
	expires time.Time
}

func NewRetainedStore(txm postgres.TransactionManager, cipher crypto.AES256, repo Repository, claim filestream.Claim, expires time.Time) (*RetainedStore, error) {
	if txm == nil || cipher == nil || repo == nil || claim.RequestID == uuid.Nil || claim.ClaimToken == uuid.Nil || claim.Organization.String() == "" || expires.IsZero() {
		return nil, xerror.ExecutionUnavailable().Detail("retained file store requires dependencies and an execution claim")
	}
	return &RetainedStore{txm: txm, cipher: cipher, repo: repo, claim: claim, expires: expires}, nil
}

func (s *RetainedStore) Retain(ctx context.Context, maxBytes int64, produce filestream.Producer) (_ *filepayload.Reference, _ *filestream.Metadata, retErr error) {
	if maxBytes <= 0 || maxBytes > filestream.MaxRetainedBytes || produce == nil {
		return nil, nil, xerror.ValidationFailed().Detail("retained files require a positive limit and producer")
	}
	ctx = postgres.WithoutTransaction(ctx)
	if err := s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		return s.repo.Begin(ctx, db, s.claim, maxBytes, s.expires)
	}); err != nil {
		return nil, nil, err
	}
	sealed := false
	defer func() {
		if sealed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err := s.txm.RunInTx(cleanupCtx, func(ctx context.Context, db postgres.DBTX) error { return s.repo.Discard(ctx, db, s.claim) })
		retErr = xerror.Join(retErr, err)
	}()
	writer := &chunkWriter{store: s, ctx: ctx, maxBytes: maxBytes, buffer: make([]byte, 0, filestream.ChunkBytes), hash: sha256.New()}
	metadata, err := produce(writer)
	if err != nil {
		return nil, nil, err
	}
	if err := writer.flush(); err != nil {
		return nil, nil, err
	}
	digest := hex.EncodeToString(writer.hash.Sum(nil))
	if metadata == nil || metadata.SizeBytes != writer.size || metadata.SHA256 != digest {
		return nil, nil, filestream.ErrUnavailable.Detail("producer metadata differs from retained bytes")
	}
	if err := s.txm.RunInTx(ctx, func(ctx context.Context, db postgres.DBTX) error {
		return s.repo.Seal(ctx, db, s.claim, writer.size, digest)
	}); err != nil {
		return nil, nil, err
	}
	sealed = true
	return &filepayload.Reference{ID: s.claim.RequestID.String(), SizeBytes: writer.size, SHA256: digest}, metadata, nil
}

type chunkWriter struct {
	store    *RetainedStore
	ctx      context.Context
	maxBytes int64
	size     int64
	sequence int32
	buffer   []byte
	hash     hash.Hash
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, xerror.Unexpected().With(err)
	}
	if int64(len(p)) > w.maxBytes-w.size {
		return 0, xerror.ExecutionResponseSizeExceeded()
	}
	written := 0
	for len(p) > 0 {
		n := min(len(p), cap(w.buffer)-len(w.buffer))
		w.buffer = append(w.buffer, p[:n]...)
		_, _ = w.hash.Write(p[:n])
		w.size += int64(n)
		written += n
		p = p[n:]
		if len(w.buffer) == cap(w.buffer) {
			if err := w.flush(); err != nil {
				return written, err
			}
		}
	}
	return written, nil
}

func chunkPrefix(org string, id uuid.UUID, sequence int32) []byte {
	return []byte(org + "\x00" + id.String() + "\x00" + strconv.FormatInt(int64(sequence), 10) + "\x00")
}

func (w *chunkWriter) flush() error {
	if len(w.buffer) == 0 {
		return nil
	}
	if err := w.ctx.Err(); err != nil {
		return xerror.Unexpected().With(err)
	}
	plainBytes := len(w.buffer)
	if plainBytes < 0 || plainBytes > filestream.ChunkBytes {
		return filestream.ErrUnavailable
	}
	chunkSize := int32(plainBytes)
	plaintext := append(chunkPrefix(w.store.claim.Organization.String(), w.store.claim.RequestID, w.sequence), w.buffer...)
	ciphertext, err := w.store.cipher.Encrypt(plaintext)
	if err != nil {
		return err
	}
	if err := w.store.txm.RunInTx(w.ctx, func(ctx context.Context, db postgres.DBTX) error {
		return w.store.repo.Append(ctx, db, w.store.claim, w.sequence, chunkSize, ciphertext)
	}); err != nil {
		return err
	}
	w.sequence++
	w.buffer = w.buffer[:0]
	return nil
}

func (s *RetainedStore) Open(ctx context.Context, ref filepayload.Reference, maxBytes int64) (io.ReadCloser, error) {
	id, err := filestream.ParseReference(ref, maxBytes)
	if err != nil {
		return nil, err
	}
	ctx = postgres.WithoutTransaction(ctx)
	var manifest filestream.Manifest
	if err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		var err error
		manifest, err = s.repo.Read(ctx, db, s.claim, id)
		return err
	}); err != nil {
		return nil, err
	}
	chunks := int64(0)
	if ref.SizeBytes > 0 {
		chunks = (ref.SizeBytes-1)/filestream.ChunkBytes + 1
	}
	if manifest.SizeBytes != ref.SizeBytes || manifest.SHA256 != ref.SHA256 || int64(manifest.ChunkCount) != chunks {
		return nil, filestream.ErrUnavailable
	}
	reader := &chunkReader{store: s, ctx: ctx, source: id, manifest: manifest, hash: sha256.New()}
	if chunks == 0 && ref.SHA256 != hex.EncodeToString(reader.hash.Sum(nil)) {
		return nil, filestream.ErrUnavailable
	}
	return reader, nil
}

type chunkReader struct {
	store    *RetainedStore
	ctx      context.Context
	source   uuid.UUID
	manifest filestream.Manifest
	sequence int32
	size     int64
	buffer   []byte
	hash     hash.Hash
	closed   bool
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, filestream.ErrUnavailable
	}
	if err := r.ctx.Err(); err != nil {
		return 0, xerror.Unexpected().With(err)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.buffer) == 0 {
		if r.sequence == r.manifest.ChunkCount {
			return 0, io.EOF
		}
		var ciphertext []byte
		err := r.store.txm.RunRead(r.ctx, func(ctx context.Context, db postgres.DBTX) error {
			var err error
			ciphertext, err = r.store.repo.ReadChunk(ctx, db, r.store.claim, r.source, r.sequence)
			return err
		})
		if err != nil {
			return 0, err
		}
		if len(ciphertext) > filestream.ChunkBytes+1024 {
			return 0, filestream.ErrUnavailable
		}
		plaintext, err := r.store.cipher.Decrypt(ciphertext)
		if err != nil {
			return 0, filestream.ErrUnavailable.With(err)
		}
		prefix := chunkPrefix(r.store.claim.Organization.String(), r.source, r.sequence)
		if !bytes.HasPrefix(plaintext, prefix) {
			return 0, filestream.ErrUnavailable
		}
		content := plaintext[len(prefix):]
		expected := min(int64(filestream.ChunkBytes), r.manifest.SizeBytes-r.size)
		if int64(len(content)) != expected {
			return 0, filestream.ErrUnavailable
		}
		_, _ = r.hash.Write(content)
		r.size += int64(len(content))
		r.sequence++
		if r.sequence == r.manifest.ChunkCount && (r.size != r.manifest.SizeBytes || hex.EncodeToString(r.hash.Sum(nil)) != r.manifest.SHA256) {
			return 0, filestream.ErrUnavailable
		}
		r.buffer = content
	}
	n := copy(p, r.buffer)
	r.buffer = r.buffer[n:]
	return n, nil
}

func (r *chunkReader) Close() error { clear(r.buffer); r.buffer = nil; r.closed = true; return nil }
