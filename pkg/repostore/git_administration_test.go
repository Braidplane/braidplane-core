package repostore_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
)

func TestRemoteGitCloneSessionRejectsAdministrativePaths(t *testing.T) {
	ctx := t.Context()
	remoteDir, sourceDir := t.TempDir(), t.TempDir()
	initBareRemote(t, remoteDir)
	initSourceRepo(t, ctx, sourceDir, map[string]string{
		".gitignore":                 "# synthetic fixture\n",
		".gitattributes":             "*.txt text\n",
		".github/workflows/test.yml": "name: synthetic\n",
		"reports/foo.git":            "ordinary content\n",
	})
	pushSourceRepo(t, ctx, sourceDir, remoteDir, "HEAD:main")
	store, cloneDir, cleanup, err := repostore.CloneRemote(ctx, remoteDir, "main", nil)
	require.NoError(t, err)
	t.Cleanup(cleanup)
	configPath := filepath.Join(cloneDir, ".git", "config")
	originalConfig, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "metadata", ".GiT"), 0o750))
	nestedConfigPath := filepath.Join(cloneDir, "metadata", ".GiT", "config")
	nestedConfig := []byte("synthetic administration")
	require.NoError(t, os.WriteFile(nestedConfigPath, nestedConfig, 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(cloneDir, "unicode"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, "unicode", ".g\u200cit"), []byte("synthetic administration file"), 0o600))

	gitDir := t.TempDir()
	gitMarker := filepath.Join(gitDir, "invoked")
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "git"), []byte("#!/bin/sh\n: > \"$BRAIDPLANE_TEST_GIT_MARKER\"\nexit 1\n"), 0o700))
	t.Setenv("BRAIDPLANE_TEST_GIT_MARKER", gitMarker)
	t.Setenv("PATH", gitDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, tc := range []struct {
		name string
		file string
		dir  string
	}{
		{name: "direct", file: ".git/config", dir: ".git"},
		{name: "case", file: ".GiT/config", dir: ".GiT"},
		{name: "dot", file: "./.git/config", dir: "./.git"},
		{name: "separators", file: `.git\config`, dir: `.git\.`},
		{name: "cleaned", file: `folder/..\.gIt/config`, dir: `folder/..\.gIt`},
		{name: "nested", file: "metadata/.GiT/config", dir: "metadata/.GiT"},
		{name: "hfs-zero-width", file: ".g\u200cit/config", dir: ".g\u200cit"},
		{name: "hfs-direction", file: "\u202a.GIT\ufeff/config", dir: "\u202a.GIT\ufeff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, operation := range []string{"read", "write", "delete", "exists", "list"} {
				t.Run(operation, func(t *testing.T) {
					t.Cleanup(func() {
						// #nosec G703 -- configPath belongs to this package-created synthetic clone.
						require.NoError(t, os.WriteFile(configPath, originalConfig, 0o600))
						require.NoError(t, os.WriteFile(nestedConfigPath, nestedConfig, 0o600))
					})
					if removeErr := os.Remove(gitMarker); removeErr != nil {
						require.ErrorIs(t, removeErr, os.ErrNotExist)
					}
					var operationErr error
					switch operation {
					case "read":
						_, operationErr = store.ReadFile(ctx, tc.file, 0)
					case "write":
						operationErr = store.WriteFile(ctx, tc.file, []byte("[synthetic]\nvalue = blocked\n"), "blocked write")
					case "delete":
						operationErr = store.DeleteFile(ctx, tc.file, "blocked delete")
					case "exists":
						_, operationErr = store.FileExists(ctx, tc.file)
					case "list":
						_, operationErr = store.ListFiles(ctx, tc.dir)
					}
					require.ErrorIs(t, operationErr, repostore.ErrInvalidPath)
					config, readErr := os.ReadFile(configPath)
					require.NoError(t, readErr)
					assert.Equal(t, originalConfig, config)
					nested, nestedErr := os.ReadFile(nestedConfigPath)
					require.NoError(t, nestedErr)
					assert.Equal(t, nestedConfig, nested)
					_, markerErr := os.Stat(gitMarker)
					assert.ErrorIs(t, markerErr, os.ErrNotExist, "Git must not run after path denial")
				})
			}
		})
	}

	for _, dir := range []string{"", "."} {
		entries, listErr := store.ListFiles(ctx, dir)
		require.NoError(t, listErr)
		require.Len(t, entries, 4)
		for _, entry := range entries {
			for _, component := range strings.Split(entry.Path, "/") {
				require.False(t, strings.EqualFold(component, ".git"))
			}
			_, readErr := store.ReadFile(ctx, entry.Path, 0)
			require.NoError(t, readErr)
		}
	}
}
