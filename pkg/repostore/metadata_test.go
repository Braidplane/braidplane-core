package repostore_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
)

func TestResolveGitMetadata_IgnoresInheritedGitRepositoryEnvironment(t *testing.T) {
	ctx := context.Background()
	parentRepo := initMetadataTestRepo(t, ctx, "parent")
	targetRepo := initMetadataTestRepo(t, ctx, "target")

	parentCommit := gitOutput(t, ctx, parentRepo, "rev-parse", "HEAD")
	targetCommit := gitOutput(t, ctx, targetRepo, "rev-parse", "HEAD")
	require.NotEqual(t, parentCommit, targetCommit)

	t.Setenv("GIT_DIR", filepath.Join(parentRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", parentRepo)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(parentRepo, ".git", "index"))

	metadata := repostore.ResolveGitMetadata(ctx, targetRepo)

	require.NotNil(t, metadata.LastCommit)
	require.Equal(t, targetCommit, *metadata.LastCommit)
	require.NotEqual(t, parentCommit, *metadata.LastCommit)
}

func initMetadataTestRepo(t *testing.T, ctx context.Context, name string) string {
	t.Helper()

	repoRoot := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.MkdirAll(repoRoot, 0o750))
	runMetadataGit(t, ctx, repoRoot, "init")
	runMetadataGit(t, ctx, repoRoot, "config", "user.name", "metadata-fixture")
	runMetadataGit(t, ctx, repoRoot, "config", "user.email", "metadata-fixture@example.com")
	runMetadataGit(t, ctx, repoRoot, "config", "commit.gpgsign", "false")

	require.NoError(t, os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte(name+"\n"), 0o600))
	runMetadataGit(t, ctx, repoRoot, "add", "README.md")
	runMetadataGit(t, ctx, repoRoot, "commit", "-m", "initial commit")

	return repoRoot
}

func runMetadataGit(t *testing.T, ctx context.Context, repoRoot string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoRoot
	cmd.Env = repostore.SanitizedGitEnv(os.Environ(), nil)
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s failed: %s", strings.Join(args, " "), string(output))
}

func gitOutput(t *testing.T, ctx context.Context, repoRoot string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoRoot
	cmd.Env = repostore.SanitizedGitEnv(os.Environ(), nil)
	output, err := cmd.Output()
	require.NoError(t, err)

	return strings.TrimSpace(string(output))
}
