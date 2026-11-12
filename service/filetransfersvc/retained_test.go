package filetransfersvc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/stretchr/testify/require"
)

type memoryTx struct{}

func (memoryTx) RunInTx(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	return fn(ctx, nil)
}

func (memoryTx) RunRead(ctx context.Context, fn func(context.Context, postgres.DBTX) error) error {
	return fn(ctx, nil)
}

type memoryRepo struct {
	manifest    filestream.Manifest
	chunks      map[int32][]byte
	sealed      bool
	discarded   bool
	readCount   int
	appendError error
}

func (r *memoryRepo) Begin(_ context.Context, _ postgres.DBTX, _ filestream.Claim, _ int64, expires time.Time) error {
	r.chunks = make(map[int32][]byte)
	r.manifest.ExpiresAt = expires
	return nil
}

func (r *memoryRepo) Append(_ context.Context, _ postgres.DBTX, _ filestream.Claim, seq, size int32, body []byte) error {
	if r.appendError != nil {
		return r.appendError
	}
	r.chunks[seq] = bytes.Clone(body)
	r.manifest.ChunkCount++
	r.manifest.SizeBytes += int64(size)
	return nil
}

func (r *memoryRepo) Seal(_ context.Context, _ postgres.DBTX, _ filestream.Claim, size int64, digest string) error {
	r.manifest.SizeBytes = size
	r.manifest.SHA256 = digest
	r.sealed = true
	return nil
}

func (r *memoryRepo) Read(_ context.Context, _ postgres.DBTX, _ filestream.Claim, _ uuid.UUID) (filestream.Manifest, error) {
	if !r.sealed {
		return filestream.Manifest{}, filestream.ErrUnavailable
	}
	return r.manifest, nil
}

func (r *memoryRepo) ReadChunk(_ context.Context, _ postgres.DBTX, _ filestream.Claim, _ uuid.UUID, seq int32) ([]byte, error) {
	r.readCount++
	body, ok := r.chunks[seq]
	if !ok {
		return nil, filestream.ErrUnavailable
	}
	return bytes.Clone(body), nil
}

func (r *memoryRepo) Discard(context.Context, postgres.DBTX, filestream.Claim) error {
	r.discarded = true
	r.chunks = nil
	return nil
}

func testRetainedStore(t *testing.T, repo *memoryRepo) *RetainedStore {
	t.Helper()
	cipher, err := crypto.NewAES256(bytes.Repeat([]byte{7}, 32))
	require.NoError(t, err)
	store, err := NewRetainedStore(memoryTx{}, cipher, repo, filestream.Claim{RequestID: uuid.New(), ClaimToken: uuid.New(), Organization: slug.Slug("fixture")}, time.Now().Add(time.Hour))
	require.NoError(t, err)
	return store
}

func retainBytes(t *testing.T, store *RetainedStore, content []byte, maxBytes int64) *filepayload.Reference {
	t.Helper()
	ref, metadata, err := store.Retain(t.Context(), maxBytes, func(dst io.Writer) (*filestream.Metadata, error) {
		return filestream.Copy(t.Context(), dst, bytes.NewReader(content), maxBytes)
	})
	require.NoError(t, err)
	require.Equal(t, int64(len(content)), metadata.SizeBytes)
	return ref
}

func TestRetainedFileRoundTripAndLazyReads(t *testing.T) {
	t.Parallel()
	for _, size := range []int{0, 1, filestream.ChunkBytes, 3*filestream.ChunkBytes + 7} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			repo := &memoryRepo{}
			store := testRetainedStore(t, repo)
			content := bytes.Repeat([]byte{0xff}, size)
			ref := retainBytes(t, store, content, int64(size+1))
			require.True(t, repo.sealed)
			require.False(t, repo.discarded)
			for _, chunk := range repo.chunks {
				require.LessOrEqual(t, len(chunk), filestream.ChunkBytes+1024)
				require.NotContains(t, chunk, bytes.Repeat([]byte{0xff}, 128))
			}
			consumer := testRetainedStore(t, repo)
			reader, err := consumer.Open(t.Context(), *ref, int64(size+1))
			require.NoError(t, err)
			require.Zero(t, repo.readCount)
			got, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.Equal(t, content, got)
			require.Equal(t, int(repo.manifest.ChunkCount), repo.readCount)
			require.NoError(t, reader.Close())
			_, err = reader.Read(make([]byte, 1))
			require.ErrorIs(t, err, filestream.ErrUnavailable)
		})
	}
}

func TestRetainedFileRefusesAlteredInput(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"ciphertext", "sequence", "organization", "digest", "reference", "missing", "unsealed"} {
		t.Run(scenario, func(t *testing.T) {
			repo := &memoryRepo{}
			store := testRetainedStore(t, repo)
			ref := retainBytes(t, store, bytes.Repeat([]byte("a"), filestream.ChunkBytes+1), 2*filestream.ChunkBytes)
			switch scenario {
			case "ciphertext":
				repo.chunks[0][20] ^= 1
			case "sequence":
				repo.chunks[0], repo.chunks[1] = repo.chunks[1], repo.chunks[0]
			case "organization":
				store.claim.Organization = "other"
			case "digest":
				sum := sha256.Sum256([]byte("other"))
				ref.SHA256 = hex.EncodeToString(sum[:])
				repo.manifest.SHA256 = ref.SHA256
			case "reference":
				ref.SizeBytes--
			case "missing":
				delete(repo.chunks, 1)
			case "unsealed":
				repo.sealed = false
			}
			reader, err := store.Open(t.Context(), *ref, 2*filestream.ChunkBytes)
			if err == nil {
				_, err = io.Copy(io.Discard, reader)
				require.NoError(t, reader.Close())
			}
			require.ErrorIs(t, err, filestream.ErrUnavailable)
		})
	}
}

func TestRetainedFileFailureDiscardsUnsealedInput(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"limit", "producer", "storage", "metadata", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			repo := &memoryRepo{}
			store := testRetainedStore(t, repo)
			if scenario == "storage" {
				repo.appendError = xerror.Unexpected().With(io.ErrClosedPipe)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			ref, metadata, err := store.Retain(ctx, filestream.ChunkBytes, func(dst io.Writer) (*filestream.Metadata, error) {
				if scenario == "cancel" {
					cancel()
				}
				content := []byte("payload")
				if scenario == "limit" {
					content = bytes.Repeat([]byte("x"), filestream.ChunkBytes+1)
				}
				result, err := filestream.Copy(ctx, dst, bytes.NewReader(content), int64(len(content)+1))
				if err != nil {
					return nil, err
				}
				if scenario == "producer" {
					return nil, xerror.Unexpected().With(io.ErrUnexpectedEOF)
				}
				if scenario == "metadata" {
					result.SizeBytes++
				}
				return result, nil
			})
			require.Error(t, err)
			require.Nil(t, ref)
			require.Nil(t, metadata)
			require.False(t, repo.sealed)
			require.True(t, repo.discarded)
		})
	}
}
