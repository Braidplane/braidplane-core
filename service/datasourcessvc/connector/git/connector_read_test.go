package git

import (
	"context"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
)

type fakeRepoStore struct {
	files       map[string][]byte
	lastReadMax int64
}

func newFakeRepoStore() *fakeRepoStore {
	return &fakeRepoStore{files: map[string][]byte{}}
}

func (s *fakeRepoStore) ReadFile(_ context.Context, filePath string, maxBytes int64) ([]byte, error) {
	s.lastReadMax = maxBytes
	data, ok := s.files[filePath]
	if !ok {
		return nil, repostore.ErrFileNotFound.Detailf("%s", filePath)
	}
	return append([]byte(nil), data...), nil
}

func TestGitRepoProviderForwardsReadLimitToRepoStore(t *testing.T) {
	t.Parallel()

	store := newFakeRepoStore()
	store.files["exports/users.csv"] = []byte("id\n1\n")

	result, err := (gitRepoProvider{store: store}).ReadFile(context.Background(), "exports/users.csv", 123)
	require.NoError(t, err)
	require.Equal(t, []byte("id\n1\n"), result.Content)
	require.Equal(t, int64(123), store.lastReadMax)
}

func (s *fakeRepoStore) WriteFile(_ context.Context, filePath string, data []byte, _ string) error {
	s.files[path.Clean(strings.ReplaceAll(filePath, "\\", "/"))] = append([]byte(nil), data...)
	return nil
}

func (s *fakeRepoStore) DeleteFile(_ context.Context, filePath, _ string) error {
	delete(s.files, filePath)
	return nil
}

func (s *fakeRepoStore) ListFiles(_ context.Context, dir string) ([]repostore.FileEntry, error) {
	var files []repostore.FileEntry
	dir = path.Clean(dir)
	for name, content := range s.files {
		if dir == "." || strings.HasPrefix(name, dir+"/") {
			files = append(files, repostore.FileEntry{Path: name, Size: int64(len(content))})
		}
	}
	return files, nil
}

func (s *fakeRepoStore) FileExists(_ context.Context, filePath string) (bool, error) {
	_, ok := s.files[filePath]
	return ok, nil
}

func TestValidateRepoURLScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		repoURL     string
		wantErr     error
		errContains string
	}{
		{name: "https", repoURL: "https://github.com/acme/repo.git"},
		{name: "ssh", repoURL: "ssh://git@github.com/acme/repo.git"},
		{name: "scp", repoURL: "git@github.com:acme/repo.git"},
		{name: "empty", repoURL: "", wantErr: xerror.ValidationFailed(), errContains: "repo_url is required"},
		{name: "invalid host", repoURL: "https:///repo.git", wantErr: xerror.ValidationFailed(), errContains: "valid hostname"},
		{name: "invalid scheme", repoURL: "ftp://example.com/repo.git", wantErr: xerror.ValidationFailed(), errContains: "https or ssh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateRepoURLScheme(tt.repoURL)
			if tt.wantErr != nil {
				require.Error(t, err)
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorContains(t, err, tt.errContains)
				assert.Contains(t, xerror.PublicDetail(err), tt.errContains)
				return
			}

			require.NoError(t, err)
		})
	}
}

func TestNewAndClose(t *testing.T) {
	t.Parallel()

	connector, err := New(
		context.Background(),
		Config{
			RepoURL:           "https://127.0.0.1/acme/repo.git",
			DefaultBranch:     "main",
			BasePath:          "exports",
			AllowPrivateHosts: true,
		},
		Credentials{AuthType: AuthTypeHTTPS},
	)
	require.NoError(t, err)
	require.NotNil(t, connector)
	assert.Equal(t, "main", connector.config.DefaultBranch)
	assert.NoError(t, connector.Close())
}

func TestExecuteRead(t *testing.T) {
	t.Parallel()

	t.Run("reads csv file into file result", func(t *testing.T) {
		t.Parallel()

		store := newFakeRepoStore()
		require.NoError(
			t,
			store.WriteFile(context.Background(), "exports/users.csv", []byte("id,email\n1,alice@example.com\n"), "seed"),
		)

		result, err := executeRead(
			context.Background(),
			store,
			"exports/users.csv",
			gitQueryData{Format: QueryFormatCSV},
			types.ExecutionLimits{MaxResultRows: 10, MaxResponseBytes: 1024},
		)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, gitOperationRead, result.Operation)
		assert.Equal(t, "exports/users.csv", result.TargetPath)
		assert.Equal(t, "csv", result.Format)
		require.Len(t, result.Rows, 1)
		assert.Equal(t, "alice@example.com", result.Rows[0]["email"])
	})

	t.Run("enforces response size limit", func(t *testing.T) {
		t.Parallel()

		store := newFakeRepoStore()
		require.NoError(
			t,
			store.WriteFile(context.Background(), "exports/users.json", []byte(`[{"id":1,"email":"alice@example.com"}]`), "seed"),
		)

		result, err := executeRead(
			context.Background(),
			store,
			"exports/users.json",
			gitQueryData{Format: QueryFormatJSON},
			types.ExecutionLimits{MaxResponseBytes: 8},
		)
		require.Nil(t, result)
		require.Error(t, err)
		require.ErrorIs(t, err, xerror.ExecutionResponseSizeExceeded())
	})

	t.Run("wraps read file errors", func(t *testing.T) {
		t.Parallel()

		store := newFakeRepoStore()
		result, err := executeRead(
			context.Background(),
			store,
			"missing.json",
			gitQueryData{Format: QueryFormatJSON},
			types.ExecutionLimits{},
		)
		require.Nil(t, result)
		require.Error(t, err)
		require.ErrorIs(t, err, xerror.NotFound(xerror.RepoFile))
		assert.ErrorContains(t, err, `read Git file "missing.json"`)
	})
}
