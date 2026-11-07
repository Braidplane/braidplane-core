package depsports

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitTestPrivateKeyPath_ResolvesRelativePathFromRepoRoot(t *testing.T) {
	tmp := t.TempDir()
	repoRoot := filepath.Join(tmp, "repo")
	nested := filepath.Join(repoRoot, "service", "datasourcessvc", "connector", "git", "e2e")
	keyPath := filepath.Join(repoRoot, ".worktree", "git-keys", "id_ed25519")

	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o750); err != nil {
		t.Fatalf("mkdir key dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(repoRoot, "go.mod"),
		[]byte("module example.com/test\n"),
		0o600,
	); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte("test-key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	t.Chdir(nested)

	t.Setenv("GIT_TEST_SSH_KEY_PATH", filepath.Join(".worktree", "git-keys", "id_ed25519"))
	t.Setenv("GIT_TEST_SSH_PRIVATE_KEY_PATH", "")

	resolved := GitTestPrivateKeyPath()
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		t.Fatalf("stat resolved path: %v", err)
	}
	expectedInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat expected path: %v", err)
	}
	if !os.SameFile(resolvedInfo, expectedInfo) {
		t.Fatalf("expected same file as %s, got %s", keyPath, resolved)
	}
}

func TestGitTestPrivateKeyPath_PrefersExistingRelativePathInCurrentDir(t *testing.T) {
	tmp := t.TempDir()
	keyPath := filepath.Join(tmp, "keys", "id_ed25519")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o750); err != nil {
		t.Fatalf("mkdir key dir: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte("test-key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	t.Chdir(tmp)

	t.Setenv("GIT_TEST_SSH_KEY_PATH", filepath.Join("keys", "id_ed25519"))
	t.Setenv("GIT_TEST_SSH_PRIVATE_KEY_PATH", "")

	resolved := GitTestPrivateKeyPath()
	expected := filepath.Join("keys", "id_ed25519")
	if resolved != expected {
		t.Fatalf("expected %s, got %s", expected, resolved)
	}
}
