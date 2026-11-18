package repostore

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const gitReadFetchMaxBytes int64 = 256 << 20

type fileCheckout struct {
	path               string
	maxBytes           int64
	preserveAttributes bool
}

// CloneNativeFileRemote materializes only the selected raw blob, checking its
// size before checkout when maxBytes is positive. A missing target allows creation.
func CloneNativeFileRemote(ctx context.Context, repoURL, branch, target string, maxBytes int64, env []string) (RepoStore, string, func(), error) {
	if maxBytes < 0 {
		return nil, "", nil, xerror.ValidationFailed().Detail("native Git checkout allowance cannot be negative")
	}
	return cloneRemoteFile(ctx, repoURL, branch, env, fileCheckout{path: target, maxBytes: maxBytes})
}

// CloneReadFileRemote fetches a bounded shallow snapshot and checks out only
// the requested file with the repository's normal attribute conversions.
func CloneReadFileRemote(ctx context.Context, repoURL, branch, target string, maxBytes int64, env []string) (RepoStore, func(), error) {
	ctx = context.WithValue(ctx, gitWriteLimitKey{}, gitReadFetchMaxBytes)
	store, _, cleanup, err := cloneRemoteFile(ctx, repoURL, branch, env, fileCheckout{
		path: target, maxBytes: min(repositoryReadLimit(maxBytes), gitReadFetchMaxBytes), preserveAttributes: true,
	})
	return store, cleanup, err
}

func cloneRemoteFile(ctx context.Context, repoURL, branch string, env []string, checkout fileCheckout) (RepoStore, string, func(), error) {
	target, err := normalizeRepoPath(checkout.path)
	if err != nil {
		return nil, "", nil, err
	}
	for _, component := range strings.Split(target, "/") {
		if strings.EqualFold(component, ".git") || strings.ContainsRune(component, 0) {
			return nil, "", nil, ErrInvalidPath
		}
	}
	repoURL, env, cleanup, err := providerPATGitEnv(repoURL, env, DetectProvider(repoURL))
	if err != nil {
		return nil, "", nil, err
	}
	defer cleanup()
	checkout.path = target
	return cloneRemote(ctx, repoURL, branch, env, &checkout)
}

var nativeFileCloneConfig = []string{
	"core.bigFileThreshold=1m",
	"core.packedGitWindowSize=1m",
	"core.packedGitLimit=8m",
	"core.deltaBaseCacheLimit=8m",
	"pack.windowMemory=8m",
	"pack.threads=1",
	"gc.auto=0",
	"maintenance.auto=false",
	"fetch.unpackLimit=1",
}

func prepareFileCheckout(ctx context.Context, directory string, target fileCheckout, env []string) error {
	if !target.preserveAttributes {
		root, err := os.OpenRoot(directory)
		if err != nil {
			return ErrRepositorySetup.With(err)
		}
		defer func() { _ = root.Close() }()
		if err := root.MkdirAll(".git/info", 0o750); err != nil {
			return ErrRepositorySetup.With(err)
		}
		// Repository attributes must not convert verified opaque bytes or run filters.
		if err := root.WriteFile(".git/info/attributes", []byte("* -text -diff -merge -filter -ident -working-tree-encoding\n"), 0o600); err != nil {
			return ErrRepositorySetup.With(err)
		}
	}
	exitCode, _, err := runGitWithExitCode(ctx, directory, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return err
	}
	if exitCode == 1 {
		return runGit(ctx, directory, "read-tree", "--empty")
	}
	if exitCode != 0 {
		return ErrRepositorySetup.Detail("file checkout has no valid HEAD")
	}
	if err := runGit(ctx, directory, "read-tree", "HEAD"); err != nil {
		return err
	}
	entry, _, err := runGitCommandOutput(ctx, directory, nil, "--literal-pathspecs", "ls-tree", "-z", "HEAD", "--", target.path)
	if err != nil {
		return ErrRepositorySetup.With(err)
	}
	if entry == "" {
		return nil
	}
	header, name, ok := strings.Cut(entry, "\t")
	fields := strings.Fields(header)
	if !ok || strings.TrimSuffix(name, "\x00") != target.path || len(fields) != 3 || fields[1] != "blob" || fields[0] != "100644" && fields[0] != "100755" {
		return ErrInvalidPath
	}
	if target.preserveAttributes {
		ctx = context.WithValue(ctx, gitWriteLimitKey{}, target.maxBytes)
		env = append(append([]string(nil), env...), "LC_ALL=C")
		stderr, err := runGitCommand(ctx, directory, env, "--literal-pathspecs", "checkout-index", "--force", "--", target.path)
		if err != nil {
			if strings.Contains(stderr, "File too large") || gitFileLimitSignal(err) {
				return xerror.ExecutionResponseSizeExceeded().Detail("Git checkout exceeds the file allowance")
			}
			return ErrRepositorySetup.With(err).Detail(sanitizeGitOutput(stderr))
		}
		return nil
	}
	if target.maxBytes > 0 {
		sizeText, _, err := runGitCommandOutput(ctx, directory, nil, "cat-file", "-s", fields[2])
		if err != nil {
			return ErrRepositorySetup.With(err)
		}
		size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
		if err != nil || size < 0 {
			return ErrRepositorySetup.Detail("native blob has an invalid size")
		}
		if size > target.maxBytes {
			return xerror.ExecutionResponseSizeExceeded().Detail("Git blob exceeds the file allowance before checkout")
		}
	}
	return runGit(ctx, directory, "--literal-pathspecs", "checkout-index", "--force", "--", target.path)
}
