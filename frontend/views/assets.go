package views

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func versionedAssetPath(path string) string {
	cleanPath := "/" + strings.TrimLeft(path, "/")

	versioned := cleanPath
	if digest := assetDigest(cleanPath); digest != "" {
		versioned = cleanPath + "?v=" + digest
	}
	return versioned
}

// VersionedAssetPath returns the canonical content-versioned asset URL.
func VersionedAssetPath(path string) string {
	return versionedAssetPath(path)
}

func assetDigest(path string) string {
	relPath := strings.TrimPrefix(path, "/assets/")
	if relPath == path || relPath == "" {
		return ""
	}

	content, err := os.ReadFile(assetDiskPath(relPath))
	if err != nil {
		return ""
	}

	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])[:12]
}

// assetDiskPath resolves an asset URL to its authoritative repository file.
func assetDiskPath(relPath string) string {
	if strings.HasPrefix(relPath, "icons/") {
		return filepath.Join(repoRootPath(), "brand", filepath.FromSlash(relPath))
	}

	// A consuming distribution may own frontend assets while sharing core icons.
	if workingDirectory, err := os.Getwd(); err == nil {
		for candidate := workingDirectory; ; candidate = filepath.Dir(candidate) {
			assetRoot := filepath.Join(candidate, "frontend", "assets")
			if info, err := os.Stat(assetRoot); err == nil && info.IsDir() {
				return filepath.Join(assetRoot, filepath.FromSlash(relPath))
			}
			if filepath.Dir(candidate) == candidate {
				break
			}
		}
	}
	return filepath.Join(repoRootPath(), "frontend", "assets", filepath.FromSlash(relPath))
}

func repoRootPath() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return repoRootPathFromCaller(file)
}

// Runtime files take precedence so version hashes describe the bytes served by the consuming distribution.
func repoRootPathFromCaller(file string) string {
	if workingDirectory, err := os.Getwd(); err == nil {
		for candidate := workingDirectory; ; candidate = filepath.Dir(candidate) {
			if assetRootExists(candidate) {
				return candidate
			}
			if filepath.Dir(candidate) == candidate {
				break
			}
		}
	}
	if filepath.IsAbs(file) {
		candidate := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
		if assetRootExists(candidate) {
			return candidate
		}
	}
	return "."
}

// assetRootExists reports whether a directory contains both runtime asset roots.
func assetRootExists(root string) bool {
	for _, path := range []string{"brand/icons", "frontend/assets"} {
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}
