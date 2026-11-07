//go:build !linux && !darwin

package repostore

import (
	"context"

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

// readRepositoryFile fails closed on platforms without the required no-follow primitives.
func readRepositoryFile(string, string, int64) ([]byte, error) {
	return nil, ErrRepositorySetup.Detail("secure repository file access is unsupported on this platform")
}

// writeRepositoryFile fails closed on platforms without the required no-follow primitives.
func writeRepositoryFile(string, string, []byte) error {
	return ErrRepositorySetup.Detail("secure repository file access is unsupported on this platform")
}

// deleteRepositoryFile fails closed on platforms without the required no-follow primitives.
func deleteRepositoryFile(string, string) error {
	return ErrRepositorySetup.Detail("secure repository file access is unsupported on this platform")
}

// repositoryFileExists fails closed on platforms without the required no-follow primitives.
func repositoryFileExists(string, string) (bool, error) {
	return false, ErrRepositorySetup.Detail("secure repository file access is unsupported on this platform")
}

// listRepositoryFiles fails closed on platforms without the required no-follow primitives.
func listRepositoryFiles(context.Context, string, string) ([]FileEntry, error) {
	return nil, ErrRepositorySetup.Detail("secure repository file access is unsupported on this platform")
}

// mapRepositoryPathError redacts platform access failures from callers.
func mapRepositoryPathError(ctx context.Context, err error) error {
	logging.LogError(ctx, "access repository path", err)
	if xerror.Is(err, ErrInvalidPath) || xerror.Is(err, xerror.ExecutionResponseSizeExceeded()) {
		return err
	}
	return ErrRepositorySetup.Detail("access repository path")
}
