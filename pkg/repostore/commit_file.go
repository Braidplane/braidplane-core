package repostore

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// ReadCommitFile reads a regular file from a full local commit ID without checkout or fetch.
func ReadCommitFile(ctx context.Context, commit, filePath string) ([]byte, error) {
	if _, err := hex.DecodeString(commit); err != nil || (len(commit) != 40 && len(commit) != 64) {
		return nil, xerror.ValidationFailed().Detail("Git candidate requires a full commit ID")
	}
	filePath, err := normalizeRepoPath(filePath)
	if err != nil {
		return nil, err
	}
	env := []string{"GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL="}
	resolved, _, err := runGitCommandOutput(ctx, "", env, "--no-replace-objects", "rev-parse", "--verify", commit+"^{commit}")
	if err != nil || resolved != strings.ToLower(commit) {
		return nil, xerror.ValidationFailed().Detail("Git candidate commit is unavailable in the local repository")
	}
	mode, _, err := runGitCommandOutput(ctx, "", env, "--no-replace-objects", "--literal-pathspecs",
		"ls-tree", "--full-tree", "--format=%(objectmode)", resolved, "--", filePath)
	if err != nil || (mode != "100644" && mode != "100755") {
		return nil, ErrFileNotFound.Detail("Git candidate path must name a regular file in the commit")
	}

	content, _, err := runGitCommandRawOutput(ctx, "", env, "--no-replace-objects", "cat-file", "blob", resolved+":"+filePath)
	if err != nil {
		return nil, ErrRepositorySetup.Detail("could not read the Git candidate file")
	}
	return []byte(content), nil
}
