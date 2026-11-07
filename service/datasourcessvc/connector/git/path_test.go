package git

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJoinGitPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		basePath    string
		relPath     string
		wantSuffix  string // expected suffix of the returned path
		wantExact   string // exact expected value (used when wantSuffix is empty)
		wantErrText string
	}{
		{
			name:       "ValidRelativePath",
			basePath:   "/tmp/repo",
			relPath:    "data/file.csv",
			wantSuffix: "data/file.csv",
		},
		{
			name:       "ValidSingleSegment",
			basePath:   "/tmp/repo",
			relPath:    "file.json",
			wantSuffix: "file.json",
		},
		{
			name:      "EmptyRelativePathReturnsBase",
			basePath:  "/tmp/repo",
			relPath:   "",
			wantExact: "/tmp/repo",
		},
		{
			name:        "AbsolutePathRejected",
			basePath:    "/tmp/repo",
			relPath:     "/etc/passwd",
			wantErrText: "absolute paths are not allowed",
		},
		{
			name:        "PathTraversalDotDotRejected",
			basePath:    "/tmp/repo",
			relPath:     "../secret",
			wantErrText: "path traversal is not allowed",
		},
		{
			name:        "DeepPathTraversalRejected",
			basePath:    "/tmp/repo",
			relPath:     "sub/../../etc/passwd",
			wantErrText: "path traversal is not allowed",
		},
		{
			name:        "BackslashTraversalRejected",
			basePath:    "/tmp/repo/exports",
			relPath:     `..\secret.json`,
			wantErrText: "path traversal is not allowed",
		},
		{
			name:        "BackslashAbsolutePathRejected",
			basePath:    "/tmp/repo/exports",
			relPath:     `\secret.json`,
			wantErrText: "absolute paths are not allowed",
		},
		{
			name:      "BackslashNestedPath",
			basePath:  "/tmp/repo",
			relPath:   `exports\nested\file.json`,
			wantExact: "/tmp/repo/exports/nested/file.json",
		},
		{
			name:       "NestedValidPath",
			basePath:   "/tmp/repo",
			relPath:    "a/b/c.csv",
			wantSuffix: "a/b/c.csv",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := joinGitPath(tc.basePath, tc.relPath)

			if tc.wantErrText != "" {
				require.ErrorContains(t, err, tc.wantErrText)
				return
			}

			require.NoError(t, err)

			if tc.wantExact != "" {
				require.Equal(t, tc.wantExact, got)
			} else {
				require.Contains(t, got, tc.wantSuffix)
			}
		})
	}
}
