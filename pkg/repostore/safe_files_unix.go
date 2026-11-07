//go:build linux || darwin

package repostore

import (
	"context"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const defaultRepositoryReadMaxBytes int64 = 10 * 1024 * 1024

// repositoryReadLimit supplies the repository-wide ceiling for callers without a narrower limit.
func repositoryReadLimit(maxBytes int64) int64 {
	if maxBytes > 0 {
		return maxBytes
	}
	return defaultRepositoryReadMaxBytes
}

// readRepositoryFile reads one regular repository file without resolving links.
func readRepositoryFile(repoRoot, filePath string, maxBytes int64) ([]byte, error) {
	parent, name, err := openRepositoryParent(repoRoot, filePath)
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
		return nil, ErrInvalidPath.Detail("repository path must identify a regular file")
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return nil, xerror.ExecutionResponseSizeExceeded().Detailf(
			"file content exceeds %d byte limit",
			maxBytes,
		)
	}

	return readFileWithinLimit(file, maxBytes)
}

// writeRepositoryFile creates parent directories and writes through descriptor-pinned paths.
func writeRepositoryFile(repoRoot, filePath string, data []byte) error {
	parent, name, err := ensureRepositoryParent(repoRoot, filePath)
	if err != nil {
		return err
	}
	defer closeRepositoryFile(parent)

	file, err := openRepositoryEntry(parent, name, unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer closeRepositoryFile(file)

	for len(data) > 0 {
		written, writeErr := file.Write(data)
		if writeErr != nil {
			return ErrRepositorySetup.With(writeErr)
		}
		data = data[written:]
	}

	return nil
}

// deleteRepositoryFile removes a regular file after pinning its parent directory.
func deleteRepositoryFile(repoRoot, filePath string) error {
	parent, name, err := openRepositoryParent(repoRoot, filePath)
	if err != nil {
		return err
	}
	defer closeRepositoryFile(parent)

	file, err := openRepositoryEntry(parent, name, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		return ErrRepositorySetup.With(statErr)
	}
	if closeErr != nil {
		return ErrRepositorySetup.With(closeErr)
	}
	if !info.Mode().IsRegular() {
		return ErrInvalidPath.Detail("repository path must identify a regular file")
	}

	// #nosec G115 -- os.File.Fd returns the Unix descriptor required by unlinkat.
	if err := unix.Unlinkat(int(parent.Fd()), name, 0); err != nil {
		return sanitizeNoFollowError(err)
	}
	return nil
}

// repositoryFileExists checks a path through no-follow descriptor resolution.
func repositoryFileExists(repoRoot, filePath string) (bool, error) {
	parent, name, err := openRepositoryParent(repoRoot, filePath)
	if err != nil {
		return false, err
	}
	defer closeRepositoryFile(parent)

	file, err := openRepositoryEntry(parent, name, unix.O_RDONLY, 0)
	if err != nil {
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, ErrRepositorySetup.With(err)
	}
	return true, nil
}

// listRepositoryFiles recursively lists regular files without descending into links.
func listRepositoryFiles(ctx context.Context, repoRoot, dir string) ([]FileEntry, error) {
	directory, err := openRepositoryDirectory(repoRoot, dir)
	if err != nil {
		return nil, err
	}
	defer closeRepositoryFile(directory)

	entries := make([]FileEntry, 0)
	if err := listRepositoryDirectory(ctx, directory, dir, &entries, 0); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})

	return entries, nil
}

// listRepositoryDirectory reads entries from an already-pinned directory descriptor.
func listRepositoryDirectory(ctx context.Context, directory *os.File, relativePath string, entries *[]FileEntry, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if schemaquota.Attached(ctx) && depth > 32 {
		return ErrInvalidPath.Detail("schema discovery exceeds 32 directory levels")
	}
	for {
		children, err := directory.ReadDir(128)
		if err != nil && err != io.EOF {
			return ErrRepositorySetup.With(err)
		}
		for _, child := range children {
			if err := ctx.Err(); err != nil {
				return err
			}
			if isGitAdministrationComponent(child.Name()) {
				continue
			}
			if child.Type()&os.ModeSymlink != 0 {
				continue
			}

			if err := schemaquota.Charge(ctx, 1, len(child.Name())+len(relativePath)); err != nil {
				return err
			}
			childFile, openErr := openRepositoryEntry(directory, child.Name(), unix.O_RDONLY, 0)
			if openErr != nil {
				if xerror.Is(openErr, ErrInvalidPath) {
					continue
				}
				return openErr
			}

			info, statErr := childFile.Stat()
			if statErr != nil {
				_ = childFile.Close()
				return ErrRepositorySetup.With(statErr)
			}

			childPath := path.Join(relativePath, child.Name())
			if info.IsDir() {
				if recurseErr := listRepositoryDirectory(ctx, childFile, childPath, entries, depth+1); recurseErr != nil {
					_ = childFile.Close()
					return recurseErr
				}
			} else if info.Mode().IsRegular() {
				*entries = append(*entries, FileEntry{Path: childPath, Size: info.Size()})
			}
			if closeErr := childFile.Close(); closeErr != nil {
				return ErrRepositorySetup.With(closeErr)
			}
		}

		if err == io.EOF {
			break
		}
	}
	return nil
}

// openRepositoryParent returns a pinned parent directory and validated final entry name.
func openRepositoryParent(repoRoot, filePath string) (*os.File, string, error) {
	components := strings.Split(filePath, "/")
	parent, err := openRepositoryDirectory(repoRoot, strings.Join(components[:len(components)-1], "/"))
	if err != nil {
		return nil, "", err
	}
	return parent, components[len(components)-1], nil
}

// ensureRepositoryParent creates missing directories while refusing all symlink components.
func ensureRepositoryParent(repoRoot, filePath string) (*os.File, string, error) {
	components := strings.Split(filePath, "/")
	directory, err := openRepositoryRoot(repoRoot)
	if err != nil {
		return nil, "", err
	}

	for _, component := range components[:len(components)-1] {
		next, openErr := openRepositoryEntry(directory, component, unix.O_RDONLY, 0)
		if xerror.Is(openErr, os.ErrNotExist) {
			// #nosec G115 -- os.File.Fd returns the Unix descriptor required by mkdirat.
			if mkdirErr := unix.Mkdirat(int(directory.Fd()), component, 0o750); mkdirErr != nil && !xerror.Is(mkdirErr, unix.EEXIST) {
				_ = directory.Close()
				return nil, "", sanitizeNoFollowError(mkdirErr)
			}
			next, openErr = openRepositoryEntry(directory, component, unix.O_RDONLY, 0)
		}
		if openErr != nil {
			_ = directory.Close()
			return nil, "", openErr
		}

		info, statErr := next.Stat()
		if statErr == nil && !info.IsDir() {
			statErr = ErrInvalidPath.Detail("repository path contains a non-directory component")
		}
		if statErr != nil {
			_ = next.Close()
			_ = directory.Close()
			return nil, "", ErrRepositorySetup.With(statErr)
		}
		if closeErr := directory.Close(); closeErr != nil {
			_ = next.Close()
			return nil, "", ErrRepositorySetup.With(closeErr)
		}
		directory = next
	}

	return directory, components[len(components)-1], nil
}

// openRepositoryDirectory resolves a relative directory from the checkout root without links.
func openRepositoryDirectory(repoRoot, relativePath string) (*os.File, error) {
	directory, err := openRepositoryRoot(repoRoot)
	if err != nil {
		return nil, err
	}
	if relativePath == "" {
		return directory, nil
	}

	for _, component := range strings.Split(relativePath, "/") {
		next, openErr := openRepositoryEntry(directory, component, unix.O_RDONLY, 0)
		if openErr != nil {
			_ = directory.Close()
			return nil, openErr
		}
		info, statErr := next.Stat()
		if statErr == nil && !info.IsDir() {
			statErr = ErrInvalidPath.Detail("repository path must identify a directory")
		}
		if statErr != nil {
			_ = next.Close()
			_ = directory.Close()
			return nil, ErrRepositorySetup.With(statErr)
		}
		if closeErr := directory.Close(); closeErr != nil {
			_ = next.Close()
			return nil, ErrRepositorySetup.With(closeErr)
		}
		directory = next
	}

	return directory, nil
}

// openRepositoryRoot opens the clone root before descriptor-relative resolution begins.
func openRepositoryRoot(repoRoot string) (*os.File, error) {
	directory, err := os.Open(repoRoot)
	if err != nil {
		return nil, ErrRepositorySetup.With(err)
	}
	info, err := directory.Stat()
	if err != nil {
		_ = directory.Close()
		return nil, ErrRepositorySetup.With(err)
	}
	if !info.IsDir() {
		_ = directory.Close()
		return nil, ErrRepositorySetup.Detail("checked-out repository root is not a directory")
	}
	return directory, nil
}

// openRepositoryEntry opens one child entry with the platform no-follow primitive.
func openRepositoryEntry(directory *os.File, name string, flags int, mode uint32) (*os.File, error) {
	// #nosec G115 -- os.File.Fd returns the Unix descriptor required by openat.
	fd, err := openNoSymlinkAt(int(directory.Fd()), name, flags, mode)
	if err != nil {
		return nil, sanitizeNoFollowError(err)
	}
	// #nosec G115 -- Unix open calls return an int file descriptor for os.NewFile.
	return os.NewFile(uintptr(fd), name), nil
}

// readFileWithinLimit reads through a bounded reader so a file that grows after Stat stays bounded.
func readFileWithinLimit(file *os.File, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		data, err := io.ReadAll(file)
		if err != nil {
			return nil, ErrRepositorySetup.With(err)
		}
		return data, nil
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, ErrRepositorySetup.With(err)
	}
	if int64(len(data)) > maxBytes {
		return nil, xerror.ExecutionResponseSizeExceeded().Detailf(
			"file content exceeds %d byte limit",
			maxBytes,
		)
	}
	return data, nil
}

// sanitizeNoFollowError turns kernel resolution errors into safe typed repository errors.
func sanitizeNoFollowError(err error) error {
	if xerror.Is(err, unix.ELOOP) || xerror.Is(err, unix.EXDEV) || xerror.Is(err, unix.ENOTDIR) {
		return ErrInvalidPath.With(err).Detail("repository paths cannot contain symbolic links or non-directory components")
	}
	return err
}

// mapRepositoryPathError preserves safe typed errors while redacting filesystem details.
func mapRepositoryPathError(ctx context.Context, err error) error {
	logging.LogError(ctx, "access repository path", err)
	if xerror.Is(err, ErrInvalidPath) || xerror.Is(err, xerror.ExecutionResponseSizeExceeded()) || xerror.Is(err, xerror.ExecutionUnavailable()) || xerror.Is(err, context.Canceled) || xerror.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrRepositorySetup.Detail("access repository path")
}

// closeRepositoryFile closes descriptor-pinned files after an operation has completed.
func closeRepositoryFile(file *os.File) {
	_ = file.Close()
}
