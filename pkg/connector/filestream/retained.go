package filestream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"time"

	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	ChunkBytes       = 1024 * 1024
	MaxRetainedBytes = int64(1<<31-1) * ChunkBytes
)

var ErrUnavailable = xerror.ExecutionUnavailable().WithPublicDetail("Retained file input is unavailable, expired or invalid; recovery cannot proceed.")

// Claim scopes retained input access to one live execution request.
type Claim struct {
	RequestID    uuid.UUID
	ClaimToken   uuid.UUID
	Organization slug.Slug
}

// Manifest is readable only after successful source consumption and sealing.
type Manifest struct {
	SizeBytes  int64
	ChunkCount int32
	SHA256     string
	ExpiresAt  time.Time
}

// Producer writes one complete finite file into retained storage.
type Producer func(io.Writer) (*Metadata, error)

// Store is the connector-neutral retained-input boundary. References belong only
// in encrypted broker traffic and process-local payloads.
type Store interface {
	Retain(context.Context, int64, Producer) (*filepayload.Reference, *Metadata, error)
	Open(context.Context, filepayload.Reference, int64) (io.ReadCloser, error)
}

type storeKey struct{}

func WithStore(ctx context.Context, store Store) context.Context {
	return context.WithValue(ctx, storeKey{}, store)
}

func FromContext(ctx context.Context) (Store, bool) {
	store, ok := ctx.Value(storeKey{}).(Store)
	return store, ok && store != nil
}

// ParseReference validates the transport shape and effective file allowance.
// Database capabilities separately check ownership, retention and live claims.
func ParseReference(ref filepayload.Reference, maxBytes int64) (uuid.UUID, error) {
	id, err := uuid.FromString(ref.ID)
	digest, digestErr := hex.DecodeString(ref.SHA256)
	if err != nil || id == uuid.Nil || maxBytes <= 0 || ref.SizeBytes < 0 || ref.SizeBytes > MaxRetainedBytes || digestErr != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != ref.SHA256 {
		return uuid.Nil, ErrUnavailable
	}
	if ref.SizeBytes > maxBytes {
		return uuid.Nil, xerror.ExecutionResponseSizeExceeded().Detailf("file exceeds %d byte transfer limit", maxBytes).WithPublicDetail("File exceeds the configured transfer byte limit.")
	}
	return id, nil
}
