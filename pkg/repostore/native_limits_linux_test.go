//go:build linux

package repostore

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func nativeLimitCommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	ctx, err := WithNativeFileLimits(ctx, 4<<20)
	require.NoError(t, err)
	cmd := newGitCommand(ctx, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = sanitizedGitEnv(os.Environ(), []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C"})
	return cmd
}

func TestNativeGitLimitsAreInheritedByChildren(t *testing.T) {
	t.Parallel()
	cmd := nativeLimitCommand(t, "-c", "alias.native-limits=!cat /proc/self/limits", "native-limits")
	output, err := cmd.Output()
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		max  int64
	}{{"Max address space", 512 << 20}, {"Max file size", nativeGitFileBlocks(4<<20) * 512}} {
		found := false
		for _, line := range strings.Split(string(output), "\n") {
			if !strings.HasPrefix(line, tc.name) {
				continue
			}
			fields := strings.Fields(line)
			for _, field := range fields[len(fields)-3 : len(fields)-1] {
				limit, err := strconv.ParseInt(field, 10, 64)
				require.NoError(t, err)
				require.Positive(t, limit)
				require.LessOrEqual(t, limit, tc.max)
			}
			found = true
		}
		require.True(t, found, "missing kernel limit: %s", tc.name)
	}
}

func TestNativeGitFileLimitStopsChildWrites(t *testing.T) {
	t.Parallel()
	cmd := nativeLimitCommand(t, "-c", "alias.native-overflow=!head -c 100000000 /dev/zero > oversized.bin", "native-overflow")
	output, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "File size limit exceeded")
	stat, err := os.Stat(filepath.Join(cmd.Dir, "oversized.bin"))
	require.NoError(t, err)
	require.Positive(t, stat.Size())
	require.LessOrEqual(t, stat.Size(), nativeGitFileBlocks(4<<20)*512)
}

func TestNativeGitAllocationLimitRejectsBufferedInput(t *testing.T) {
	t.Parallel()
	cmd := nativeLimitCommand(t, "hash-object", "--stdin")
	cmd.Stdin = io.LimitReader(nativeLimitZeroReader{}, 32<<20)
	output, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "over limit 16777216")
}

type nativeLimitZeroReader struct{}

func (nativeLimitZeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestNativeGitDeadlineStopsTransportChildren(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	ctx, err := WithNativeFileLimits(ctx, 1<<20)
	require.NoError(t, err)
	cmd := newGitCommand(ctx, "-c", "alias.native-wait=!sleep 5", "native-wait")
	cmd.Env = sanitizedGitEnv(os.Environ(), []string{"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"})
	started := time.Now()
	require.Error(t, cmd.Run())
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	require.Less(t, time.Since(started), 2*time.Second)
}

func TestNativeGitCommitDoesNotMapTheWorktree(t *testing.T) {
	t.Parallel()
	const size = 513 << 20
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	ctx, err := WithNativeFileLimits(ctx, size)
	require.NoError(t, err)
	root := t.TempDir()
	require.NoError(t, runGit(ctx, root, "init", "--initial-branch=main"))
	require.NoError(t, runGit(ctx, root, "config", "user.name", "Fixture"))
	require.NoError(t, runGit(ctx, root, "config", "user.email", "fixture@example.test"))
	for _, config := range nativeFileCloneConfig {
		key, value, _ := strings.Cut(config, "=")
		require.NoError(t, runGit(ctx, root, "config", key, value))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte("* -text -diff -merge -filter -ident -working-tree-encoding\n"), 0o600))
	file, err := os.Create(filepath.Join(root, "large.bin"))
	require.NoError(t, err)
	require.NoError(t, file.Truncate(size))
	require.NoError(t, file.Close())
	store := checkoutRepoStore{repoRoot: root}
	require.NoError(t, store.commitPath(ctx, "large.bin", "bounded sparse-file regression"))
}
