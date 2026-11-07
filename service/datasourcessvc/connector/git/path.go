package git

import (
	"path/filepath"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// joinGitPath joins basePath and relativePath, preventing absolute paths and path traversal.
func joinGitPath(basePath, relativePath string) (string, error) {
	base := filepath.Clean(strings.TrimSpace(basePath))
	// Match RepoStore's separators before checking for traversal.
	rel := strings.ReplaceAll(strings.TrimSpace(relativePath), "\\", "/")

	if rel == "" {
		return base, nil
	}

	if filepath.IsAbs(rel) {
		return "", xerror.Unexpected().Detail("absolute paths are not allowed")
	}

	for _, segment := range strings.Split(rel, "/") {
		if segment == ".." {
			return "", xerror.Unexpected().Detail("path traversal is not allowed")
		}
	}

	joined := filepath.Clean(filepath.Join(base, rel))

	if base != "." && base != "/" && joined != base &&
		!strings.HasPrefix(joined, base+string(filepath.Separator)) {
		return "", xerror.Unexpected().Detail("path escapes configured base path")
	}

	return joined, nil
}
