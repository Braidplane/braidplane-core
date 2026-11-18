package ftp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
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
	for _, name := range []string{" report.csv ", " .. ", "nested/ raport pól.csv ", "2026-11-18T12:34:00Z.json"} {
		target, err := joinFTPPath("/exports", name)
		require.NoError(t, err)
		require.Equal(t, "/exports/"+name, target)
	}
}

func TestFTPPathValidation(t *testing.T) {
	t.Parallel()

	for _, invalid := range []string{"reports\t", "report\x00.csv", "report\x7f.csv", "nested\\report.csv", "report\xff.csv"} {
		_, err := New(t.Context(), Config{BasePath: invalid}, Credentials{})
		require.ErrorIs(t, err, xerror.InvalidInput(xerror.DataSource))
		_, err = joinFTPPath("/exports", invalid)
		require.ErrorIs(t, err, xerror.InvalidInput(xerror.Query))
	}
}

func TestFTPExecuteValidatesResolvedPath(t *testing.T) {
	t.Parallel()

	connector := &Connector{basePath: "/exports"}
	for _, tc := range []struct{ name, statement, param, value string }{
		{"substitution", `{"path":"{{name}}"}`, "name", "report\t.csv"},
		{"write override", `{"operation":"write","path":"report.csv"}`, "path", "report\t.csv"},
		{"blank substitution", `{"path":"{{name}}"}`, "name", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := connector.Execute(t.Context(), []byte(tc.statement), map[string]types.TypedParam{
				tc.param: {Value: tc.value, DataType: "text"},
			})
			require.NoError(t, err)
			require.ErrorIs(t, result.Error(), xerror.InvalidInput(xerror.Query))
		})
	}
	result, err := connector.Execute(t.Context(),
		[]byte(`{"operation":"write","path":"{{name}}","format":"json","content":null}`),
		map[string]types.TypedParam{"name": {Value: "  ", DataType: "text"}},
	)
	require.NoError(t, err)
	require.ErrorContains(t, result.Error(), "scalar and null content")
}
