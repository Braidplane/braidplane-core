//go:build linux || darwin

package repostore

import (
	"context"
	"io"
	"os"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"golang.org/x/sys/unix"
)

func streamRepositoryFile(ctx context.Context, root, path string, maxBytes int64, dst io.Writer) (*filestream.Metadata, error) {
	parent, name, err := openRepositoryParent(root, path)
	if err != nil {
		return nil, err
	}
	defer closeRepositoryFile(parent)
	file, err := openRepositoryEntry(parent, name, unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer closeRepositoryFile(file)
	info, err := file.Stat()
	if err != nil {
		return nil, ErrRepositorySetup.With(err)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrInvalidPath
	}
	if maxBytes <= 0 || info.Size() > maxBytes {
		return nil, xerror.ExecutionResponseSizeExceeded()
	}
	return filestream.Copy(ctx, dst, file, maxBytes)
}

func writeRepositoryStream(ctx context.Context, root, path string, src io.Reader, ref filepayload.Reference, overwrite bool) error {
	if err := ctx.Err(); err != nil {
		return ErrRepositorySetup.With(err)
	}
	parent, name, err := ensureRepositoryParent(root, path)
	if err != nil {
		return err
	}
	defer closeRepositoryFile(parent)
	existing, openErr := openRepositoryEntry(parent, name, unix.O_RDONLY, 0)
	if openErr != nil && !xerror.Is(openErr, os.ErrNotExist) {
		return openErr
	}
	if existing != nil {
		info, statErr := existing.Stat()
		closeErr := existing.Close()
		if statErr != nil {
			return ErrRepositorySetup.With(statErr)
		}
		if closeErr != nil {
			return ErrRepositorySetup.With(closeErr)
		}
		if !info.Mode().IsRegular() {
			return ErrInvalidPath
		}
		if !overwrite {
			return ErrFileExists
		}
	}
	tempName := ".braidplane-stream-" + uuid.New().String() + ".part"
	file, err := openRepositoryEntry(parent, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer closeRepositoryFile(file)
	// #nosec G115 -- descriptor-pinned unlinkat uses os.File.Fd's Unix descriptor.
	defer func() { _ = unix.Unlinkat(int(parent.Fd()), tempName, 0) }()
	metadata, err := filestream.Copy(ctx, file, src, max(ref.SizeBytes, int64(1)))
	if err != nil {
		return err
	}
	if metadata.SizeBytes != ref.SizeBytes || metadata.SHA256 != ref.SHA256 {
		return filestream.ErrUnavailable
	}
	if err := file.Sync(); err != nil {
		return ErrRepositorySetup.With(err)
	}
	if err := file.Close(); err != nil {
		return ErrRepositorySetup.With(err)
	}
	if err := ctx.Err(); err != nil {
		return ErrRepositorySetup.With(err)
	}
	// #nosec G115 -- both descriptors are pinned repository parent descriptors.
	fd := int(parent.Fd())
	if !overwrite {
		// linkat gives an atomic no-replace publication without following the target.
		if err := unix.Linkat(fd, tempName, fd, name, 0); err != nil {
			if xerror.Is(err, unix.EEXIST) {
				return ErrFileExists
			}
			return sanitizeNoFollowError(err)
		}
		return nil
	}
	// Replacing a link changes only its directory entry, never its external target.
	if err := unix.Renameat(fd, tempName, fd, name); err != nil {
		return sanitizeNoFollowError(err)
	}
	return nil
}
