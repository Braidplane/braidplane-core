package repostore

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	defaultCommitMessage = "update file"
	defaultGitUserName   = "Braidplane"
	defaultGitUserEmail  = "no-reply@braidplane.com"
	gitCommandTimeout    = 10 * time.Second
	gitCommandOutputMax  = 10 * 1024 * 1024
	gitCommandErrorMax   = 64 * 1024
	gitRemoteBranchMax   = 10_000
)

var errGitCommandOutputExceeded = xerror.ExecutionUnavailable().Detail("git command output exceeded its byte limit")

// checkoutRepoStore provides file operations for a temporary checked-out clone.
type checkoutRepoStore struct {
	repoRoot string
}

// CheckRemote verifies a remote repository is reachable.
func CheckRemote(ctx context.Context, repoURL string, env []string) error {
	adapter := remoteAdapterForRepoURL(repoURL)
	return adapter.CheckRemote(ctx, repoURL, env)
}

// ListRemoteBranches returns visible heads from a remote repository.
func ListRemoteBranches(ctx context.Context, repoURL string, env []string) ([]RemoteBranch, error) {
	adapter := remoteAdapterForRepoURL(repoURL)
	return adapter.ListRemoteBranches(ctx, repoURL, env)
}

// CloneRemote performs a shallow clone of repoURL into targetDir.
func CloneRemote(
	ctx context.Context,
	repoURL, branch string,
	env []string,
) (RepoStore, string, func(), error) {
	adapter := remoteAdapterForRepoURL(repoURL)
	return adapter.CloneRemote(ctx, repoURL, branch, env)
}

// PushRemote pushes local commits from repoDir to repoURL.
func PushRemote(
	ctx context.Context,
	repoDir, repoURL string,
	env []string,
	opts PushOptions,
) error {
	adapter := remoteAdapterForRepoURL(repoURL)
	return adapter.PushRemote(ctx, repoDir, repoURL, env, opts)
}

func newCheckoutRepoStore(ctx context.Context, repoRoot string) (RepoStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(repoRoot) == "" {
		return nil, ErrInvalidPath.Detail("repo root cannot be empty")
	}

	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, ErrRepositorySetup.With(err)
	}

	if err := ensureCheckedOutGitRepository(ctx, absRoot); err != nil {
		return nil, err
	}

	return &checkoutRepoStore{repoRoot: absRoot}, nil
}

// ReadFile reads a regular file beneath the checkout without traversing links.
func (s *checkoutRepoStore) ReadFile(ctx context.Context, filePath string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	normalized, err := normalizeRepoPath(filePath)
	if err != nil {
		return nil, err
	}

	data, err := readRepositoryFile(s.repoRoot, normalized, repositoryReadLimit(maxBytes))
	if err != nil {
		if xerror.Is(err, os.ErrNotExist) {
			return nil, ErrFileNotFound.Detailf("%s", filePath)
		}
		return nil, mapRepositoryPathError(ctx, err)
	}

	return data, nil
}

func (s *checkoutRepoStore) WriteFile(
	ctx context.Context,
	filePath string,
	data []byte,
	message string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	normalized, err := normalizeRepoPath(filePath)
	if err != nil {
		return err
	}

	if err := writeRepositoryFile(s.repoRoot, normalized, data); err != nil {
		return mapRepositoryPathError(ctx, err)
	}

	return s.commitPath(ctx, normalized, message)
}

func (s *checkoutRepoStore) DeleteFile(ctx context.Context, filePath, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	normalized, err := normalizeRepoPath(filePath)
	if err != nil {
		return err
	}

	if err := deleteRepositoryFile(s.repoRoot, normalized); err != nil {
		if xerror.Is(err, os.ErrNotExist) {
			return ErrFileNotFound.Detailf("%s", filePath)
		}
		return mapRepositoryPathError(ctx, err)
	}

	return s.commitPath(ctx, normalized, message)
}

func (s *checkoutRepoStore) ListFiles(ctx context.Context, dir string) ([]FileEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	normalizedDir := ""
	if strings.TrimSpace(dir) != "" && dir != "." {
		var err error
		normalizedDir, err = normalizeRepoPath(dir)
		if err != nil {
			return nil, err
		}
	}

	result, err := listRepositoryFiles(ctx, s.repoRoot, normalizedDir)
	if err != nil {
		if xerror.Is(err, os.ErrNotExist) {
			return []FileEntry{}, nil
		}
		return nil, mapRepositoryPathError(ctx, err)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})

	return result, nil
}

func (s *checkoutRepoStore) FileExists(ctx context.Context, filePath string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	normalized, err := normalizeRepoPath(filePath)
	if err != nil {
		return false, err
	}

	exists, err := repositoryFileExists(s.repoRoot, normalized)
	if err == nil {
		return exists, nil
	}
	if xerror.Is(err, os.ErrNotExist) {
		return false, nil
	}

	return false, mapRepositoryPathError(ctx, err)
}

func normalizeRepoPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", ErrInvalidPath.Detail("path cannot be empty")
	}

	normalized := filepath.ToSlash(filepath.Clean(strings.ReplaceAll(value, "\\", "/")))
	if normalized == "." || normalized == "" || strings.HasPrefix(normalized, "/") {
		return "", ErrInvalidPath.Detail("path must be relative")
	}
	if strings.HasPrefix(normalized, "../") || normalized == ".." {
		return "", ErrInvalidPath.Detail("path cannot escape root")
	}
	for _, component := range strings.Split(normalized, "/") {
		if isGitAdministrationComponent(component) {
			return "", ErrInvalidPath.Detail("Git administration paths are not repository content")
		}
	}

	return normalized, nil
}

func isGitAdministrationComponent(component string) bool {
	// HFS+ ignores these formatting characters when comparing file names.
	component = strings.Map(func(r rune) rune {
		switch {
		case r >= '\u200c' && r <= '\u200f', r >= '\u202a' && r <= '\u202e',
			r >= '\u206a' && r <= '\u206f', r == '\ufeff':
			return -1
		default:
			return r
		}
	}, component)
	return strings.EqualFold(component, ".git")
}

func (s *checkoutRepoStore) commitPath(ctx context.Context, filePath, message string) error {
	if err := runGit(ctx, s.repoRoot, "add", "-A", "--", filePath); err != nil {
		return err
	}

	hasChanges, err := hasStagedChanges(ctx, s.repoRoot, filePath)
	if err != nil {
		return err
	}
	if !hasChanges {
		return nil
	}

	commitMessage := strings.TrimSpace(message)
	if commitMessage == "" {
		commitMessage = defaultCommitMessage
	}

	return runGit(ctx, s.repoRoot, "commit", "-m", commitMessage, "--", filePath)
}

func ensureCheckedOutGitRepository(ctx context.Context, repoRoot string) error {
	gitDir := filepath.Join(repoRoot, ".git")
	if stat, err := os.Stat(gitDir); err == nil && stat.IsDir() {
		return ensureCommitIdentity(ctx, repoRoot)
	}

	return ErrRepositorySetup.Detail("checked-out git repository is required")
}

func ensureCommitIdentity(ctx context.Context, repoRoot string) error {
	// Braidplane-created commits are intentionally unsigned. Temporary clones
	// must not inherit a workstation's global signing key or agent dependency.
	if err := runGit(ctx, repoRoot, "config", "commit.gpgsign", "false"); err != nil {
		return err
	}

	hasName, err := hasGitConfig(ctx, repoRoot, "user.name")
	if err != nil {
		return err
	}
	if !hasName {
		if err := runGit(ctx, repoRoot, "config", "user.name", defaultGitUserName); err != nil {
			return err
		}
	}

	hasEmail, err := hasGitConfig(ctx, repoRoot, "user.email")
	if err != nil {
		return err
	}
	if !hasEmail {
		if err := runGit(ctx, repoRoot, "config", "user.email", defaultGitUserEmail); err != nil {
			return err
		}
	}

	return nil
}

func hasGitConfig(ctx context.Context, repoRoot, key string) (bool, error) {
	exitCode, stderrMsg, err := runGitWithExitCode(ctx, repoRoot, "config", "--local", "--get", key)
	if err != nil {
		return false, ErrRepositorySetup.With(err)
	}

	if exitCode == 0 {
		return true, nil
	}
	if exitCode == 1 {
		return false, nil
	}

	if stderrMsg == "" {
		stderrMsg = "unexpected git config exit code"
	}

	return false, ErrRepositorySetup.Detailf("read git config %q: %s", key, stderrMsg)
}

func hasStagedChanges(ctx context.Context, repoRoot, filePath string) (bool, error) {
	exitCode, stderrMsg, err := runGitWithExitCode(
		ctx,
		repoRoot,
		"diff",
		"--cached",
		"--quiet",
		"--",
		filePath,
	)
	if err != nil {
		return false, ErrRepositorySetup.With(err)
	}

	if exitCode == 0 {
		return false, nil
	}
	if exitCode == 1 {
		return true, nil
	}

	if stderrMsg == "" {
		stderrMsg = "unexpected git diff exit code"
	}
	return false, ErrRepositorySetup.Detailf("check staged changes: %s", stderrMsg)
}

// gitEnvBlocklist contains git repository-context environment variables that
// must be stripped from child subprocess environments. Under git hook execution
// (e.g. pre-push), git exports these vars pointing at the parent repository's
// .git directory. Without sanitization, child git commands inherit them and
// operate on the parent repo instead of the intended temp directory, causing
// config-lock contention when tests run in parallel.
var gitEnvBlocklist = map[string]struct{}{
	"GIT_DIR":                          {},
	"GIT_WORK_TREE":                    {},
	"GIT_INDEX_FILE":                   {},
	"GIT_OBJECT_DIRECTORY":             {},
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": {},
	"GIT_COMMON_DIR":                   {},
	"GIT_NAMESPACE":                    {},
	"GIT_CEILING_DIRECTORIES":          {},
	"GIT_DISCOVERY_ACROSS_FILESYSTEM":  {},
	"GIT_PREFIX":                       {},
	"GIT_INTERNAL_SUPER_PREFIX":        {},
	"GIT_CONFIG":                       {},
	"GIT_CONFIG_GLOBAL":                {},
	"GIT_CONFIG_SYSTEM":                {},
	"GIT_CONFIG_NOSYSTEM":              {},
	"GIT_CONFIG_COUNT":                 {},
	"GIT_CONFIG_PARAMETERS":            {},
	"GIT_GRAFT_FILE":                   {},
	"GIT_IMPLICIT_WORK_TREE":           {},
	"GIT_NO_REPLACE_OBJECTS":           {},
	"GIT_REPLACE_REF_BASE":             {},
	"GIT_SHALLOW_FILE":                 {},
}

var gitEnvPrefixBlocklist = []string{
	"GIT_CONFIG_KEY_",
	"GIT_CONFIG_VALUE_",
}

// sanitizedGitEnv returns base with git repo-context variables stripped,
// followed by extra appended. Keys provided by extra replace base values so
// callers can isolate HOME, XDG_CONFIG_HOME, and similar process settings
// without leaving duplicate environment entries.
func sanitizedGitEnv(base, extra []string) []string {
	extraKeys := make(map[string]struct{}, len(extra))
	for _, e := range extra {
		extraKeys[envKey(e)] = struct{}{}
	}

	result := make([]string, 0, len(base)+len(extra))
	for _, e := range base {
		key := envKey(e)
		if _, ok := gitEnvBlocklist[key]; ok {
			continue
		}
		if _, ok := extraKeys[key]; ok {
			continue
		}
		prefixBlocked := false
		for _, prefix := range gitEnvPrefixBlocklist {
			if strings.HasPrefix(key, prefix) {
				prefixBlocked = true
				break
			}
		}
		if prefixBlocked {
			continue
		}
		result = append(result, e)
	}
	return append(result, extra...)
}

// SanitizedGitEnv returns a child-process environment with inherited Git
// repository context removed and explicit overrides appended.
func SanitizedGitEnv(base, extra []string) []string {
	return sanitizedGitEnv(base, extra)
}

// envKey returns the variable name portion of an environment entry.
func envKey(entry string) string {
	if i := strings.IndexByte(entry, '='); i >= 0 {
		return entry[:i]
	}
	return entry
}

func runGit(ctx context.Context, repoRoot string, args ...string) error {
	exitCode, stderrMsg, err := runGitWithExitCode(ctx, repoRoot, args...)
	if err != nil {
		return ErrRepositorySetup.With(err)
	}
	if exitCode == 0 {
		return nil
	}

	if stderrMsg == "" {
		stderrMsg = "command failed"
	}

	return ErrRepositorySetup.Detailf("git %s: %s", strings.Join(args, " "), stderrMsg)
}

func runGitWithExitCode(
	ctx context.Context,
	repoRoot string,
	args ...string,
) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoRoot
	cmd.Env = sanitizedGitEnv(os.Environ(), nil)

	stderr := limitedBuffer{remaining: gitCommandErrorMax}
	stderrWriter := cancelOnOutputOverflow{writer: &stderr, cancel: cancel}
	cmd.Stderr = &stderrWriter

	err := cmd.Run()
	if overflowErr := stderrWriter.OverflowError(); overflowErr != nil {
		return -1, strings.TrimSpace(stderr.String()), overflowErr
	}
	if err == nil {
		return 0, "", nil
	}

	message := strings.TrimSpace(stderr.String())
	var exitErr *exec.ExitError
	if xerror.As(err, &exitErr) {
		return exitErr.ExitCode(), message, nil
	}

	if message == "" {
		message = err.Error()
	}

	return -1, message, ErrRepositorySetup.Detailf("run git %s: %w", strings.Join(args, " "), err)
}

func runGitCommand(
	ctx context.Context,
	repoRoot string,
	env []string,
	args ...string,
) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	if strings.TrimSpace(repoRoot) != "" {
		cmd.Dir = repoRoot
	}
	cmd.Env = sanitizedGitEnv(os.Environ(), env)

	stderr := limitedBuffer{remaining: gitCommandErrorMax}
	stderrWriter := cancelOnOutputOverflow{writer: &stderr, cancel: cancel}
	cmd.Stderr = &stderrWriter

	err := cmd.Run()
	if overflowErr := stderrWriter.OverflowError(); overflowErr != nil {
		return strings.TrimSpace(stderr.String()), overflowErr
	}
	return strings.TrimSpace(stderr.String()), err
}

// runGitCommandOutput executes git and returns both stdout and stderr for callers that need command data.
func runGitCommandOutput(
	ctx context.Context,
	repoRoot string,
	env []string,
	args ...string,
) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	if strings.TrimSpace(repoRoot) != "" {
		cmd.Dir = repoRoot
	}
	cmd.Env = sanitizedGitEnv(os.Environ(), env)

	stdout := limitedBuffer{remaining: gitCommandOutputMax}
	stderr := limitedBuffer{remaining: gitCommandErrorMax}
	stdoutWriter := cancelOnOutputOverflow{writer: &stdout, cancel: cancel}
	stderrWriter := cancelOnOutputOverflow{writer: &stderr, cancel: cancel}
	cmd.Stdout = &stdoutWriter
	cmd.Stderr = &stderrWriter

	err := cmd.Run()
	if overflowErr := stdoutWriter.OverflowError(); overflowErr != nil {
		return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), overflowErr
	}
	if overflowErr := stderrWriter.OverflowError(); overflowErr != nil {
		return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), overflowErr
	}
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

type cancelOnOutputOverflow struct {
	writer io.Writer
	cancel context.CancelFunc
	err    error
}

func (w *cancelOnOutputOverflow) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	if err == nil {
		return n, nil
	}
	err = xerror.Unexpected().With(err)
	if xerror.Is(err, errGitCommandOutputExceeded) {
		w.err = err
		w.cancel()
	}
	return n, err
}

func (w *cancelOnOutputOverflow) OverflowError() error { return w.err }

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if len(data) <= b.remaining {
		b.remaining -= len(data)
		_, _ = b.buffer.Write(data)
		return len(data), nil
	}
	if b.remaining > 0 {
		accepted := b.remaining
		_, _ = b.buffer.Write(data[:accepted])
		b.remaining = 0
		return accepted, errGitCommandOutputExceeded
	}
	return 0, errGitCommandOutputExceeded
}

func (b *limitedBuffer) String() string { return b.buffer.String() }
