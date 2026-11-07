package ftp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJoinFTPPath(t *testing.T) {
	t.Parallel()

	t.Run("JoinsRelativePathUnderBase", func(t *testing.T) {
		t.Parallel()

		joined, err := joinFTPPath("/exports", "users/file.csv")
		require.NoError(t, err)
		require.Equal(t, "/exports/users/file.csv", joined)
	})

	t.Run("AllowsRelativePathWhenBaseIsRoot", func(t *testing.T) {
		t.Parallel()

		joined, err := joinFTPPath("/", "users.csv")
		require.NoError(t, err)
		require.Equal(t, "/users.csv", joined)
	})

	t.Run("RejectsAbsolutePath", func(t *testing.T) {
		t.Parallel()

		_, err := joinFTPPath("/exports", "/etc/passwd")
		require.ErrorContains(t, err, "absolute paths are not allowed")
	})

	t.Run("RejectsTraversalSegment", func(t *testing.T) {
		t.Parallel()

		_, err := joinFTPPath("/exports", "../../etc/passwd")
		require.ErrorContains(t, err, "path traversal is not allowed")
	})
}

func TestNew_RejectsBlockedFTPHost(t *testing.T) {
	t.Parallel()

	_, err := New(context.Background(), Config{Host: "127.0.0.1", Port: 21}, Credentials{
		Username: "test",
		Password: "test",
	})
	require.ErrorContains(t, err, "non-public ip")
}

func TestJoinFTPPathPreservesLiteralEntryNames(t *testing.T) {
	for _, name := range []string{" report.csv ", " .. ", "nested/ report.csv "} {
		target, err := joinFTPPath("/exports", name)
		require.NoError(t, err)
		require.Equal(t, "/exports/"+name, target)
	}
}
