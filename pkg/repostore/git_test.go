package repostore_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func TestRemoteGitOperations_CloneSessionReadWriteListDeleteAndPush(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	remoteDir := t.TempDir()
	sourceDir := t.TempDir()

	initBareRemote(t, remoteDir)
	initSourceRepo(t, ctx, sourceDir, map[string]string{
		"fixtures/users.csv":             "id,email\n1,a@example.com\n",
		"pipelines/alpha.pipeline.yaml":  "a",
		"pipelines/beta.pipeline.yaml":   "bb",
		"pipelines/nested/gamma.yaml":    "ccc",
		"pipelines/ignored/hidden.json":  "{}",
		"pipelines/ignored/hidden2.json": "{}",
	})
	pushSourceRepo(t, ctx, sourceDir, remoteDir, "HEAD:main")
	pushSourceRepo(t, ctx, sourceDir, remoteDir, "HEAD:release")
	setBareRemoteHead(t, remoteDir, "main")

	err := repostore.CheckRemote(ctx, remoteDir, nil)
	require.NoError(t, err)

	branches, err := repostore.ListRemoteBranches(ctx, remoteDir, nil)
	require.NoError(t, err)
	require.Equal(t, []repostore.RemoteBranch{
		{Name: "main", IsDefault: true},
		{Name: "release", IsDefault: false},
	}, branches)

	cloneStore, cloneDir, cleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	data, err := cloneStore.ReadFile(ctx, "fixtures/users.csv", 0)
	require.NoError(t, err)
	require.Equal(t, "id,email\n1,a@example.com\n", string(data))

	exists, err := cloneStore.FileExists(ctx, "pipelines/alpha.pipeline.yaml")
	require.NoError(t, err)
	require.True(t, exists)

	_, quotaErr := cloneStore.ListFiles(schemaquota.WithQuota(ctx, 1, 1024), "pipelines")
	require.ErrorContains(t, quotaErr, "item limit")
	entries, err := cloneStore.ListFiles(ctx, "pipelines")
	require.NoError(t, err)
	require.Equal(t, []repostore.FileEntry{
		{Path: "pipelines/alpha.pipeline.yaml", Size: 1},
		{Path: "pipelines/beta.pipeline.yaml", Size: 2},
		{Path: "pipelines/ignored/hidden.json", Size: 2},
		{Path: "pipelines/ignored/hidden2.json", Size: 2},
		{Path: "pipelines/nested/gamma.yaml", Size: 3},
	}, entries)

	err = cloneStore.WriteFile(ctx, "pipelines/delta.pipeline.yaml", []byte("dddd"), "add delta")
	require.NoError(t, err)
	err = cloneStore.DeleteFile(ctx, "pipelines/beta.pipeline.yaml", "remove beta")
	require.NoError(t, err)
	err = repostore.PushRemote(ctx, cloneDir, remoteDir, nil, repostore.PushOptions{
		Force:   true,
		Refspec: "HEAD:main",
	})
	require.NoError(t, err)

	verifyStore, _, verifyCleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
	require.NoError(t, err)
	t.Cleanup(verifyCleanup)

	data, err = verifyStore.ReadFile(ctx, "pipelines/delta.pipeline.yaml", 0)
	require.NoError(t, err)
	require.Equal(t, "dddd", string(data))
	exists, err = verifyStore.FileExists(ctx, "pipelines/beta.pipeline.yaml")
	require.NoError(t, err)
	require.False(t, exists)
	require.Equal(t, "remove beta", latestGitCommitMessage(t, cloneDir))
}

func TestRemoteGitCloneSessionRejectsEscapingPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	remoteDir := t.TempDir()
	sourceDir := t.TempDir()

	initBareRemote(t, remoteDir)
	initSourceRepo(t, ctx, sourceDir, map[string]string{
		"fixtures/users.csv": "id,email\n1,a@example.com\n",
	})
	pushSourceRepo(t, ctx, sourceDir, remoteDir, "HEAD:main")

	store, _, cleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	err = store.WriteFile(ctx, "../escape.yaml", []byte("x"), "bad")
	require.Error(t, err)
	require.ErrorIs(t, err, repostore.ErrInvalidPath)
}

func TestRemoteGitCloneSessionRejectsExternalSymlinksForFileOperations(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires additional privileges on Windows")
	}

	ctx := context.Background()
	remoteDir := t.TempDir()
	sourceDir := t.TempDir()
	outsideDir := t.TempDir()
	secret := "SYNTHETIC_SECRET_DO_NOT_DISCLOSE"
	secretPath := filepath.Join(outsideDir, "secret.txt")
	require.NoError(t, os.WriteFile(secretPath, []byte(secret), 0o600))

	initBareRemote(t, remoteDir)
	initSourceRepo(t, ctx, sourceDir, map[string]string{"inside.txt": "inside"})
	pushSourceRepo(t, ctx, sourceDir, remoteDir, "HEAD:main")

	store, cloneDir, cleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	relativeOutside, err := filepath.Rel(cloneDir, outsideDir)
	require.NoError(t, err)

	tests := []struct {
		name       string
		linkPath   string
		target     string
		operation  string
		filePath   string
		verifySafe func(t *testing.T)
	}{
		{
			name:      "ReadAbsoluteFinal",
			linkPath:  "absolute-final",
			target:    secretPath,
			operation: "read",
			filePath:  "absolute-final",
		},
		{
			name:      "ReadRelativeFinal",
			linkPath:  "relative-final",
			target:    filepath.Join(relativeOutside, "secret.txt"),
			operation: "read",
			filePath:  "relative-final",
		},
		{
			name:      "WriteAbsoluteIntermediate",
			linkPath:  "absolute-directory",
			target:    outsideDir,
			operation: "write",
			filePath:  "absolute-directory/new.txt",
			verifySafe: func(t *testing.T) {
				t.Helper()
				_, statErr := os.Stat(filepath.Join(outsideDir, "new.txt"))
				require.ErrorIs(t, statErr, os.ErrNotExist)
			},
		},
		{
			name:      "WriteRelativeIntermediate",
			linkPath:  "relative-directory",
			target:    relativeOutside,
			operation: "write",
			filePath:  "relative-directory/new.txt",
			verifySafe: func(t *testing.T) {
				t.Helper()
				_, statErr := os.Stat(filepath.Join(outsideDir, "new.txt"))
				require.ErrorIs(t, statErr, os.ErrNotExist)
			},
		},
		{
			name:      "DeleteFinal",
			linkPath:  "delete-final",
			target:    secretPath,
			operation: "delete",
			filePath:  "delete-final",
		},
		{
			name:      "DeleteIntermediate",
			linkPath:  "delete-directory",
			target:    outsideDir,
			operation: "delete",
			filePath:  "delete-directory/secret.txt",
		},
		{
			name:      "ExistsFinal",
			linkPath:  "exists-final",
			target:    secretPath,
			operation: "exists",
			filePath:  "exists-final",
		},
		{
			name:      "ExistsIntermediate",
			linkPath:  "exists-directory",
			target:    outsideDir,
			operation: "exists",
			filePath:  "exists-directory/secret.txt",
		},
	}

	// Subtests share one checkout, whose RepoStore contract is sequential-only.

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.NoError(t, os.Symlink(tc.target, filepath.Join(cloneDir, tc.linkPath)))

			var operationErr error
			switch tc.operation {
			case "read":
				_, operationErr = store.ReadFile(ctx, tc.filePath, 1024)
			case "write":
				operationErr = store.WriteFile(ctx, tc.filePath, []byte("unsafe"), "unsafe write")
			case "delete":
				operationErr = store.DeleteFile(ctx, tc.filePath, "unsafe delete")
			case "exists":
				var exists bool
				exists, operationErr = store.FileExists(ctx, tc.filePath)
				require.False(t, exists)
			}

			requireUnsafePathError(t, operationErr, outsideDir, secret)
			content, readErr := os.ReadFile(secretPath)
			require.NoError(t, readErr)
			require.Equal(t, secret, string(content))
			if tc.verifySafe != nil {
				tc.verifySafe(t)
			}
		})
	}
}

func TestRemoteGitCloneSessionListFilesSkipsSymlinksAndRejectsLinkedDirectory(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires additional privileges on Windows")
	}

	ctx := context.Background()
	remoteDir := t.TempDir()
	sourceDir := t.TempDir()
	outsideDir := t.TempDir()
	secret := "SYNTHETIC_SECRET_DO_NOT_DISCLOSE"
	require.NoError(t, os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte(secret), 0o600))

	initBareRemote(t, remoteDir)
	initSourceRepo(t, ctx, sourceDir, map[string]string{"inside.txt": "inside"})
	pushSourceRepo(t, ctx, sourceDir, remoteDir, "HEAD:main")

	store, cloneDir, cleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, os.Symlink(filepath.Join(outsideDir, "secret.txt"), filepath.Join(cloneDir, "secret-link")))
	require.NoError(t, os.Symlink(outsideDir, filepath.Join(cloneDir, "directory-link")))

	entries, err := store.ListFiles(ctx, "")
	require.NoError(t, err)
	require.Equal(t, []repostore.FileEntry{{Path: "inside.txt", Size: 6}}, entries)

	_, err = store.ListFiles(ctx, "directory-link")
	requireUnsafePathError(t, err, outsideDir, secret)
}

func TestRemoteGitCloneSessionRejectsBrokenAndCyclicSymlinks(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires additional privileges on Windows")
	}

	ctx := context.Background()
	remoteDir := t.TempDir()
	sourceDir := t.TempDir()
	initBareRemote(t, remoteDir)
	initSourceRepo(t, ctx, sourceDir, map[string]string{"inside.txt": "inside"})
	pushSourceRepo(t, ctx, sourceDir, remoteDir, "HEAD:main")

	store, cloneDir, cleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, os.Symlink("missing-target", filepath.Join(cloneDir, "broken")))
	require.NoError(t, os.Symlink("missing-target", filepath.Join(cloneDir, "broken-directory")))
	require.NoError(t, os.Symlink("cycle-b", filepath.Join(cloneDir, "cycle-a")))
	require.NoError(t, os.Symlink("cycle-a", filepath.Join(cloneDir, "cycle-b")))
	require.NoError(t, os.Symlink("cycle-directory-b", filepath.Join(cloneDir, "cycle-directory-a")))
	require.NoError(t, os.Symlink("cycle-directory-a", filepath.Join(cloneDir, "cycle-directory-b")))

	// Subtests share one checkout, whose RepoStore contract is sequential-only.

	for _, filePath := range []string{
		"broken",
		"broken-directory/file.txt",
		"cycle-a",
		"cycle-directory-a/file.txt",
	} {
		t.Run(filePath, func(t *testing.T) {
			_, readErr := store.ReadFile(ctx, filePath, 1024)
			require.ErrorIs(t, readErr, repostore.ErrInvalidPath)
			writeErr := store.WriteFile(ctx, filePath, []byte("unsafe"), "unsafe write")
			require.ErrorIs(t, writeErr, repostore.ErrInvalidPath)
			deleteErr := store.DeleteFile(ctx, filePath, "unsafe delete")
			require.ErrorIs(t, deleteErr, repostore.ErrInvalidPath)
			_, existsErr := store.FileExists(ctx, filePath)
			require.ErrorIs(t, existsErr, repostore.ErrInvalidPath)
		})
	}
}

func TestRemoteGitCloneSessionRejectsProcSymlinks(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("/proc magic links are Linux-specific")
	}

	ctx := context.Background()
	remoteDir := t.TempDir()
	sourceDir := t.TempDir()
	initBareRemote(t, remoteDir)
	initSourceRepo(t, ctx, sourceDir, map[string]string{"inside.txt": "inside"})
	pushSourceRepo(t, ctx, sourceDir, remoteDir, "HEAD:main")

	store, cloneDir, cleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, os.Symlink("/proc/self/cmdline", filepath.Join(cloneDir, "proc-file")))
	require.NoError(t, os.Symlink("/proc/self", filepath.Join(cloneDir, "proc-directory")))

	// Subtests share one checkout, whose RepoStore contract is sequential-only.

	for _, filePath := range []string{"proc-file", "proc-directory/cmdline"} {
		t.Run(filePath, func(t *testing.T) {
			_, readErr := store.ReadFile(ctx, filePath, 1024)
			requireUnsafePathError(t, readErr, "/proc", "")
			writeErr := store.WriteFile(ctx, filePath, []byte("unsafe"), "unsafe write")
			requireUnsafePathError(t, writeErr, "/proc", "")
			deleteErr := store.DeleteFile(ctx, filePath, "unsafe delete")
			requireUnsafePathError(t, deleteErr, "/proc", "")
			_, existsErr := store.FileExists(ctx, filePath)
			requireUnsafePathError(t, existsErr, "/proc", "")
		})
	}

	_, listErr := store.ListFiles(ctx, "proc-directory")
	requireUnsafePathError(t, listErr, "/proc", "")
}

func TestRemoteGitCloneSessionReadFileEnforcesLimitBeforeRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	remoteDir := t.TempDir()
	sourceDir := t.TempDir()
	initBareRemote(t, remoteDir)
	initSourceRepo(t, ctx, sourceDir, map[string]string{"large.txt": "123456789"})
	pushSourceRepo(t, ctx, sourceDir, remoteDir, "HEAD:main")

	store, _, cleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	content, err := store.ReadFile(ctx, "large.txt", 8)
	require.Nil(t, content)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

func TestRemoteGitCloneSessionReadFileUsesDefaultLimitWhenUnset(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	remoteDir := t.TempDir()
	sourceDir := t.TempDir()
	initBareRemote(t, remoteDir)
	initSourceRepo(t, ctx, sourceDir, map[string]string{
		"large.txt": strings.Repeat("x", 10*1024*1024+1),
	})
	pushSourceRepo(t, ctx, sourceDir, remoteDir, "HEAD:main")

	store, _, cleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	content, err := store.ReadFile(ctx, "large.txt", 0)
	require.Nil(t, content)
	require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
}

func requireUnsafePathError(t *testing.T, err error, forbiddenPath, forbiddenContent string) {
	t.Helper()
	require.ErrorIs(t, err, repostore.ErrInvalidPath)
	require.NotContains(t, err.Error(), forbiddenPath)
	if forbiddenContent != "" {
		require.NotContains(t, err.Error(), forbiddenContent)
	}
}

func TestRemoteGitCloneSessionWriteFileReturnsContextErrorWhenCanceled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	remoteDir := t.TempDir()
	sourceDir := t.TempDir()

	initBareRemote(t, remoteDir)
	initSourceRepo(t, ctx, sourceDir, map[string]string{
		"fixtures/users.csv": "id,email\n1,a@example.com\n",
	})
	pushSourceRepo(t, ctx, sourceDir, remoteDir, "HEAD:main")

	store, _, cleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err = store.WriteFile(canceledCtx, "pipelines/a.pipeline.yaml", []byte("name: a"), "add pipeline a")
	require.Error(t, err)
	require.ErrorContains(t, err, "context canceled")
}

func TestRemoteGitCloneSessionEnsuresCommitIdentity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	remoteDir := t.TempDir()
	sourceDir := t.TempDir()

	initBareRemote(t, remoteDir)
	initSourceRepo(t, ctx, sourceDir, map[string]string{
		"fixtures/users.csv": "id,email\n1,a@example.com\n",
	})
	pushSourceRepo(t, ctx, sourceDir, remoteDir, "HEAD:main")

	store, cloneDir, cleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	require.Equal(t, "Braidplane", readLocalGitConfig(t, cloneDir, "user.name"))
	require.Equal(t, "no-reply@braidplane.com", readLocalGitConfig(t, cloneDir, "user.email"))
	require.Equal(t, "false", readLocalGitConfig(t, cloneDir, "commit.gpgsign"))

	err = store.WriteFile(
		ctx,
		"fixtures/extra.csv",
		[]byte("id,email\n2,b@example.com\n"),
		"write fixture",
	)
	require.NoError(t, err)
}

func initBareRemote(t *testing.T, remoteDir string) {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), "git", "init", "--bare", remoteDir)
	cmd.Env = repostore.SanitizedGitEnv(os.Environ(), nil)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git init --bare failed: %s", string(output))
}

func setBareRemoteHead(t *testing.T, remoteDir, branch string) {
	t.Helper()

	cmd := exec.CommandContext(
		context.Background(),
		"git",
		"--git-dir",
		remoteDir,
		"symbolic-ref",
		"HEAD",
		"refs/heads/"+branch,
	)
	cmd.Env = repostore.SanitizedGitEnv(os.Environ(), nil)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git symbolic-ref HEAD failed: %s", string(output))
}

func initSourceRepo(t *testing.T, ctx context.Context, repoDir string, files map[string]string) {
	t.Helper()

	runGitTest(t, ctx, repoDir, "init")
	runGitTest(t, ctx, repoDir, "config", "user.name", "fixture")
	runGitTest(t, ctx, repoDir, "config", "user.email", "fixture@example.com")
	runGitTest(t, ctx, repoDir, "config", "commit.gpgsign", "false")
	for path, content := range files {
		fullPath := filepath.Join(repoDir, filepath.FromSlash(path))
		require.NoError(t, os.MkdirAll(filepath.Dir(fullPath), 0o750))
		require.NoError(t, os.WriteFile(fullPath, []byte(content), 0o600))
	}
	runGitTest(t, ctx, repoDir, "add", "-A")
	runGitTest(t, ctx, repoDir, "commit", "-m", "seed fixtures")
}

func pushSourceRepo(t *testing.T, ctx context.Context, sourceDir, remoteDir, refspec string) {
	t.Helper()

	err := repostore.PushRemote(ctx, sourceDir, remoteDir, nil, repostore.PushOptions{
		Force:   true,
		Refspec: refspec,
	})
	require.NoError(t, err)
}

func runGitTest(t *testing.T, ctx context.Context, repoDir string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoDir
	cmd.Env = repostore.SanitizedGitEnv(os.Environ(), nil)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s failed: %s", strings.Join(args, " "), string(output))
}

func readLocalGitConfig(t *testing.T, dir, key string) string {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), "git", "config", "--local", "--get", key)
	cmd.Dir = dir
	cmd.Env = repostore.SanitizedGitEnv(os.Environ(), nil)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git config --local --get %s failed: %s", key, string(output))

	return strings.TrimSpace(string(output))
}

func latestGitCommitMessage(t *testing.T, repoDir string) string {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), "git", "log", "-1", "--pretty=%B")
	cmd.Dir = repoDir
	cmd.Env = repostore.SanitizedGitEnv(os.Environ(), nil)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git log failed: %s", string(output))

	return strings.TrimSpace(string(output))
}

func TestSanitizedGitEnv_RemovesRepoContextVars(t *testing.T) {
	t.Parallel()

	base := []string{
		"HOME=/home/user",
		"PATH=/usr/bin",
		// repo-context vars that must be stripped
		"GIT_DIR=/some/repo/.git",
		"GIT_WORK_TREE=/some/repo",
		"GIT_INDEX_FILE=/some/repo/.git/index",
		"GIT_OBJECT_DIRECTORY=/some/repo/.git/objects",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/alt/objects",
		"GIT_COMMON_DIR=/some/repo/.git",
		"GIT_NAMESPACE=feature-ns",
		"GIT_CEILING_DIRECTORIES=/some",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=1",
		"GIT_PREFIX=subdir/",
		"GIT_INTERNAL_SUPER_PREFIX=parent/",
		"GIT_CONFIG=/tmp/gitconfig",
		"GIT_CONFIG_GLOBAL=/home/user/.gitconfig",
		"GIT_CONFIG_SYSTEM=/etc/gitconfig",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_PARAMETERS='user.name=test'",
		"GIT_GRAFT_FILE=/tmp/grafts",
		"GIT_IMPLICIT_WORK_TREE=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_REPLACE_REF_BASE=refs/replace",
		"GIT_SHALLOW_FILE=/tmp/shallow",
		"GIT_CONFIG_KEY_0=user.name",
		"GIT_CONFIG_VALUE_0=test",
		"GIT_CONFIG_KEY_1=user.email",
		"GIT_CONFIG_VALUE_1=test@example.com",
		// identity/auth vars that must be preserved
		"GIT_AUTHOR_NAME=Author",
		"GIT_COMMITTER_NAME=Committer",
		"GIT_SSH_COMMAND=ssh -i /key",
		"SSH_AUTH_SOCK=/tmp/ssh.sock",
	}
	extra := []string{"GIT_ASKPASS=echo"}

	result := repostore.SanitizedGitEnv(base, extra)
	resultStr := strings.Join(result, "\n")

	// stripped
	require.NotContains(t, resultStr, "GIT_DIR=")
	require.NotContains(t, resultStr, "GIT_WORK_TREE=")
	require.NotContains(t, resultStr, "GIT_INDEX_FILE=")
	require.NotContains(t, resultStr, "GIT_OBJECT_DIRECTORY=")
	require.NotContains(t, resultStr, "GIT_ALTERNATE_OBJECT_DIRECTORIES=")
	require.NotContains(t, resultStr, "GIT_COMMON_DIR=")
	require.NotContains(t, resultStr, "GIT_NAMESPACE=")
	require.NotContains(t, resultStr, "GIT_CEILING_DIRECTORIES=")
	require.NotContains(t, resultStr, "GIT_DISCOVERY_ACROSS_FILESYSTEM=")
	require.NotContains(t, resultStr, "GIT_PREFIX=")
	require.NotContains(t, resultStr, "GIT_INTERNAL_SUPER_PREFIX=")
	require.NotContains(t, resultStr, "GIT_CONFIG=")
	require.NotContains(t, resultStr, "GIT_CONFIG_GLOBAL=")
	require.NotContains(t, resultStr, "GIT_CONFIG_SYSTEM=")
	require.NotContains(t, resultStr, "GIT_CONFIG_NOSYSTEM=")
	require.NotContains(t, resultStr, "GIT_CONFIG_COUNT=")
	require.NotContains(t, resultStr, "GIT_CONFIG_PARAMETERS=")
	require.NotContains(t, resultStr, "GIT_GRAFT_FILE=")
	require.NotContains(t, resultStr, "GIT_IMPLICIT_WORK_TREE=")
	require.NotContains(t, resultStr, "GIT_NO_REPLACE_OBJECTS=")
	require.NotContains(t, resultStr, "GIT_REPLACE_REF_BASE=")
	require.NotContains(t, resultStr, "GIT_SHALLOW_FILE=")
	require.NotContains(t, resultStr, "GIT_CONFIG_KEY_0=")
	require.NotContains(t, resultStr, "GIT_CONFIG_VALUE_0=")
	require.NotContains(t, resultStr, "GIT_CONFIG_KEY_1=")
	require.NotContains(t, resultStr, "GIT_CONFIG_VALUE_1=")

	// preserved
	require.Contains(t, resultStr, "HOME=/home/user")
	require.Contains(t, resultStr, "GIT_AUTHOR_NAME=Author")
	require.Contains(t, resultStr, "GIT_COMMITTER_NAME=Committer")
	require.Contains(t, resultStr, "GIT_SSH_COMMAND=ssh -i /key")
	require.Contains(t, resultStr, "SSH_AUTH_SOCK=/tmp/ssh.sock")

	// extra appended
	require.Contains(t, resultStr, "GIT_ASKPASS=echo")
}

func TestSanitizedGitEnv_ExtraValuesReplaceBaseValues(t *testing.T) {
	t.Parallel()

	base := []string{
		"HOME=/home/user",
		"PATH=/usr/bin",
		"XDG_CONFIG_HOME=/home/user/.config",
	}
	extra := []string{
		"HOME=/tmp/isolated-home",
		"XDG_CONFIG_HOME=/tmp/isolated-config",
		"GIT_CONFIG_NOSYSTEM=1",
	}

	result := repostore.SanitizedGitEnv(base, extra)
	resultStr := strings.Join(result, "\n")

	require.NotContains(t, resultStr, "HOME=/home/user")
	require.NotContains(t, resultStr, "XDG_CONFIG_HOME=/home/user/.config")
	require.Contains(t, resultStr, "PATH=/usr/bin")
	require.Contains(t, resultStr, "HOME=/tmp/isolated-home")
	require.Contains(t, resultStr, "XDG_CONFIG_HOME=/tmp/isolated-config")
	require.Contains(t, resultStr, "GIT_CONFIG_NOSYSTEM=1")
}
