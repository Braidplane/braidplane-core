package repostore

import (
	"context"
	"io"
	"os"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// StreamingStore extends checkout storage without requiring snapshot stores to
// materialize large inputs or pretend to support streaming writes.
type StreamingStore interface {
	ReadFileTo(context.Context, string, int64, io.Writer) (*filestream.Metadata, error)
	WriteFileFrom(context.Context, string, io.Reader, filepayload.Reference, bool, string) error
}

func (s *checkoutRepoStore) ReadFileTo(ctx context.Context, filePath string, maxBytes int64, dst io.Writer) (*filestream.Metadata, error) {
	normalized, err := normalizeRepoPath(filePath)
	if err != nil {
		return nil, err
	}
	metadata, err := streamRepositoryFile(ctx, s.repoRoot, normalized, maxBytes, dst)
	if err != nil {
		if xerror.Is(err, os.ErrNotExist) {
			return nil, ErrFileNotFound
		}
		return nil, mapRepositoryPathError(ctx, err)
	}
	return metadata, nil
}

func (s *checkoutRepoStore) WriteFileFrom(ctx context.Context, filePath string, src io.Reader, ref filepayload.Reference, overwrite bool, message string) error {
	if _, err := filestream.ParseReference(ref, filestream.MaxRetainedBytes); err != nil {
		return err
	}
	normalized, err := normalizeRepoPath(filePath)
	if err != nil {
		return err
	}
	if err := writeRepositoryStream(ctx, s.repoRoot, normalized, src, ref, overwrite); err != nil {
		return err
	}
	return s.commitPath(ctx, normalized, message)
}
