package repostore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/connector/filestream"
	filepayload "github.com/Braidplane/braidplane-core/pkg/payload/file"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/stretchr/testify/require"
)

func TestNativeCloneOnlyMaterializesTheSelectedBlob(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	source, remote := t.TempDir(), t.TempDir()
	initSourceRepo(t, ctx, source, map[string]string{"wanted.bin": "four", "unrelated.bin": strings.Repeat("x", 128<<10)})
	initBareRemote(t, remote)
	pushSourceRepo(t, ctx, source, remote, "HEAD:main")
	for _, tc := range []struct {
		name  string
		limit int64
	}{{"Below", 3}, {"At", 4}, {"Above", 5}} {
		t.Run(tc.name, func(t *testing.T) {
			store, root, cleanup, err := repostore.CloneNativeFileRemote(ctx, remote, "main", "wanted.bin", tc.limit, nil)
			if tc.limit < 4 {
				require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
				require.Nil(t, store)
				require.Empty(t, root)
				return
			}
			require.NoError(t, err)
			t.Cleanup(cleanup)
			content, err := os.ReadFile(filepath.Join(root, "wanted.bin"))
			require.NoError(t, err)
			require.Equal(t, "four", string(content))
			_, err = os.Stat(filepath.Join(root, "unrelated.bin"))
			require.ErrorIs(t, err, os.ErrNotExist, "unrelated worktree data must not be materialized")
		})
	}
}

func TestNativeCloneEmptyRepositoryAllowsFirstFile(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	remote := t.TempDir()
	initBareRemote(t, remote)
	store, root, cleanup, err := repostore.CloneNativeFileRemote(ctx, remote, "", "first.bin", 100, nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NoError(t, store.WriteFile(ctx, "first.bin", []byte("first"), "initial native file"))
	require.NoError(t, repostore.PushRemote(ctx, root, remote, nil, repostore.PushOptions{}))
	command := exec.CommandContext(ctx, "git", "show", "HEAD:first.bin")
	command.Dir = remote
	command.Env = repostore.SanitizedGitEnv(os.Environ(), nil)
	content, err := command.Output()
	require.NoError(t, err)
	require.Equal(t, "first", string(content))
}

func TestNativeCloneRejectsMetadataTargetsBeforeRemoteAccess(t *testing.T) {
	t.Parallel()
	for _, target := range []string{".git/config", "nested/.GIT/config", "bad\x00name"} {
		_, _, _, err := repostore.CloneNativeFileRemote(t.Context(), "unreachable-fixture", "main", target, 100, nil)
		require.ErrorIs(t, err, repostore.ErrInvalidPath)
	}
	_, _, _, err := repostore.CloneNativeFileRemote(t.Context(), "unreachable-fixture", "main", "file.bin", -1, nil)
	require.ErrorIs(t, err, xerror.ValidationFailed())
}

func TestNativeCheckoutLiteralPathPreservesUnmaterializedFiles(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	source, remote := t.TempDir(), t.TempDir()
	const target = "nested/[data]*.bin"
	initSourceRepo(t, ctx, source, map[string]string{target: "old", "nested/d.bin": "prior-good"})
	initBareRemote(t, remote)
	pushSourceRepo(t, ctx, source, remote, "HEAD:main")
	store, root, cleanup, err := repostore.CloneNativeFileRemote(ctx, remote, "main", target, 100, nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	_, err = os.Stat(filepath.Join(root, "nested/d.bin"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, store.WriteFile(ctx, target, []byte("new"), "literal native file"))
	require.NoError(t, repostore.PushRemote(ctx, root, remote, nil, repostore.PushOptions{}))
	for path, want := range map[string]string{target: "new", "nested/d.bin": "prior-good"} {
		command := exec.CommandContext(ctx, "git", "show", "refs/heads/main:"+path)
		command.Dir = remote
		command.Env = repostore.SanitizedGitEnv(os.Environ(), nil)
		content, err := command.Output()
		require.NoError(t, err)
		require.Equal(t, want, string(content))
	}
}

func TestNativeGitFileTransferPreservesBlobBytes(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	source, remote := t.TempDir(), t.TempDir()
	original := []byte("original\n$Id$\n")
	initSourceRepo(t, ctx, source, map[string]string{
		".gitattributes": "*.bin text eol=crlf ident filter=reject\n",
		"nested/raw.bin": string(original),
	})
	initBareRemote(t, remote)
	pushSourceRepo(t, ctx, source, remote, "HEAD:main")
	template := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(template, "config"), []byte("[filter \"reject\"]\n clean = false\n smudge = false\n required = true\n"), 0o600))
	env := []string{"GIT_TEMPLATE_DIR=" + template}
	_, _, _, err := repostore.CloneRemote(ctx, remote, "main", env)
	require.ErrorContains(t, err, "filter", "fixture must reject an ordinary filtered checkout")
	base, checkout, cleanup, err := repostore.CloneNativeFileRemote(ctx, remote, "main", "nested/raw.bin", 100, env)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	stream := base.(repostore.StreamingStore)
	var body bytes.Buffer
	_, err = stream.ReadFileTo(ctx, "nested/raw.bin", 100, &body)
	require.NoError(t, err)
	require.Equal(t, original, body.Bytes(), "native reads must preserve the stored blob rather than checkout conversions")
	replacement := []byte("replacement\r\n$Id$\r\n")
	digest := sha256.Sum256(replacement)
	ref := filepayload.Reference{ID: "ea946443-7b39-4f64-8faf-eb3fac36c17f", SizeBytes: int64(len(replacement)), SHA256: hex.EncodeToString(digest[:])}
	require.NoError(t, stream.WriteFileFrom(ctx, "nested/raw.bin", bytes.NewReader(replacement), ref, true, "native bytes"))
	require.NoError(t, repostore.PushRemote(ctx, checkout, remote, nil, repostore.PushOptions{}))
	command := exec.CommandContext(ctx, "git", "show", "refs/heads/main:nested/raw.bin")
	command.Dir = remote
	command.Env = repostore.SanitizedGitEnv(os.Environ(), nil)
	stored, err := command.Output()
	require.NoError(t, err)
	require.Equal(t, replacement, stored, "commit filters must not change verified native bytes")
	legacy, _, legacyCleanup, err := repostore.CloneRemote(ctx, remote, "main", nil)
	require.NoError(t, err)
	t.Cleanup(legacyCleanup)
	converted, err := legacy.ReadFile(ctx, "nested/raw.bin", 100)
	require.NoError(t, err)
	require.Contains(t, string(converted), "$Id:", "ordinary checkouts must retain repository conversion behavior")
}

func TestCheckoutStreamingPreservesPriorGoodAndRejectsUnsafePaths(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	root := t.TempDir()
	initSourceRepo(t, ctx, root, map[string]string{"prior.bin": "prior-good"})
	remote := t.TempDir()
	initBareRemote(t, remote)
	pushSourceRepo(t, ctx, root, remote, "HEAD:main")
	base, checkout, cleanup, err := repostore.CloneRemote(ctx, remote, "main", nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	root = checkout
	store, ok := base.(repostore.StreamingStore)
	require.True(t, ok)
	content := bytes.Repeat([]byte("stream"), 200000)
	digest := sha256.Sum256(content)
	ref := filepayload.Reference{ID: "ea946443-7b39-4f64-8faf-eb3fac36c17f", SizeBytes: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	var output bytes.Buffer
	metadata, err := store.ReadFileTo(ctx, "prior.bin", 10, &output)
	require.NoError(t, err)
	require.Equal(t, "prior-good", output.String())
	require.Equal(t, int64(10), metadata.SizeBytes)
	_, err = store.ReadFileTo(ctx, "prior.bin", 9, io.Discard)
	require.Error(t, err)
	require.ErrorIs(t, store.WriteFileFrom(ctx, "prior.bin", bytes.NewReader(content), ref, false, "write"), repostore.ErrFileExists)
	invalid := ref
	invalid.SHA256 = strings.Repeat("0", 64)
	require.ErrorIs(t, store.WriteFileFrom(ctx, "prior.bin", bytes.NewReader(content), invalid, true, "write"), filestream.ErrUnavailable)
	prior, err := os.ReadFile(filepath.Join(root, "prior.bin"))
	require.NoError(t, err)
	require.Equal(t, "prior-good", string(prior))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.ErrorIs(t, store.WriteFileFrom(cancelled, "prior.bin", bytes.NewReader(content), ref, true, "write"), context.Canceled)
	require.NoError(t, store.WriteFileFrom(ctx, "prior.bin", bytes.NewReader(content), ref, true, "streamed replacement"))
	output.Reset()
	metadata, err = store.ReadFileTo(ctx, "prior.bin", ref.SizeBytes, &output)
	require.NoError(t, err)
	require.Equal(t, ref.SHA256, metadata.SHA256)
	require.Equal(t, content, output.Bytes())
	leftovers, err := filepath.Glob(filepath.Join(root, ".braidplane-stream-*.part"))
	require.NoError(t, err)
	require.Empty(t, leftovers)
	outside := filepath.Join(t.TempDir(), "outside.bin")
	require.NoError(t, os.WriteFile(outside, []byte("private"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link.bin")))
	_, err = store.ReadFileTo(ctx, "link.bin", 100, io.Discard)
	require.Error(t, err)
	require.Error(t, store.WriteFileFrom(ctx, "link.bin", bytes.NewReader(content), ref, true, "unsafe"))
	_, err = store.ReadFileTo(ctx, "../outside.bin", 100, io.Discard)
	require.ErrorIs(t, err, repostore.ErrInvalidPath)
	private, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "private", string(private))
}
