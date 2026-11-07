package views

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVersionedAssetPathUsesContentHash(t *testing.T) {
	t.Parallel()

	got := versionedAssetPath("/assets/js/builder_canvas.js")

	require.True(t, strings.HasPrefix(got, "/assets/js/builder_canvas.js?v="))
	require.NotContains(t, got, "20260427")
	require.Len(t, strings.TrimPrefix(got, "/assets/js/builder_canvas.js?v="), 12)
}

func TestVersionedAssetPathUsesBrandIconLibrary(t *testing.T) {
	t.Parallel()

	got := versionedAssetPath("/assets/icons/app/favicon-16.png")

	require.True(t, strings.HasPrefix(got, "/assets/icons/app/favicon-16.png?v="))
	require.Len(t, strings.TrimPrefix(got, "/assets/icons/app/favicon-16.png?v="), 12)
}

func TestRepoRootPathFromCallerUsesAvailableAssetRoots(t *testing.T) {
	sourceRoot := repoRootPathFromCaller("github.com/Braidplane/braidplane-core/frontend/views/assets.go")
	require.DirExists(t, filepath.Join(sourceRoot, "frontend", "assets"))
	require.DirExists(t, filepath.Join(sourceRoot, "brand", "icons"))
	sourceFile := filepath.Join(sourceRoot, "frontend", "views", "assets.go")

	t.Chdir(t.TempDir())
	require.Equal(t, sourceRoot, repoRootPathFromCaller(sourceFile))
	require.Equal(t, ".", repoRootPathFromCaller("github.com/Braidplane/braidplane-core/frontend/views/assets.go"))

	runtimeRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(runtimeRoot, "frontend", "assets"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(runtimeRoot, "brand", "icons"), 0o750))
	t.Chdir(runtimeRoot)
	require.Equal(t, runtimeRoot, repoRootPathFromCaller(sourceFile))
	require.Equal(t, runtimeRoot, repoRootPathFromCaller("github.com/Braidplane/braidplane-core/frontend/views/assets.go"))
	require.Equal(t, runtimeRoot, repoRootPathFromCaller(filepath.Join(t.TempDir(), "frontend", "views", "assets.go")))
}

func TestVersionedAssetPathHashesDistributionBytesWithoutPrivateIconCopy(t *testing.T) {
	runtimeRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(runtimeRoot, "frontend", "assets", "js"), 0o750))
	content := []byte("synthetic consuming distribution asset")
	require.NoError(t, os.WriteFile(filepath.Join(runtimeRoot, "frontend", "assets", "js", "builder_canvas.js"), content, 0o600))
	t.Chdir(runtimeRoot)

	sum := sha256.Sum256(content)
	require.Equal(t, fmt.Sprintf("/assets/js/builder_canvas.js?v=%x", sum[:6]), VersionedAssetPath("/assets/js/builder_canvas.js"))
	require.Equal(t, "/assets/missing.js", VersionedAssetPath("/assets/missing.js"))
}

func TestSiteWebmanifestUsesContentVersionedIcons(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile(assetDiskPath("site.webmanifest"))
	require.NoError(t, err)

	manifest := struct {
		Icons []struct {
			Source string `json:"src"`
		} `json:"icons"`
	}{}
	require.NoError(t, json.Unmarshal(content, &manifest))
	require.Len(t, manifest.Icons, 2)
	require.Equal(t, []string{
		versionedAssetPath("/assets/icons/app/logo-192.png"),
		versionedAssetPath("/assets/icons/app/logo-512.png"),
	}, []string{manifest.Icons[0].Source, manifest.Icons[1].Source})
}
