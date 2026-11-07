package pipelinegit

import (
	"context"
	"sort"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

type snapshotRepoStore struct{ files map[string][]byte }

// NewSnapshotRepoStore creates a read-only in-memory repository from an
// executor-produced snapshot. Callers can reconcile Git content without
// materializing tenant-controlled files in the control-plane process.
func NewSnapshotRepoStore(files map[string][]byte) repostore.RepoStore {
	cloned := make(map[string][]byte, len(files))
	for path, data := range files {
		cloned[path] = append([]byte(nil), data...)
	}
	return &snapshotRepoStore{files: cloned}
}

func (s *snapshotRepoStore) ReadFile(_ context.Context, path string, maxBytes int64) ([]byte, error) {
	data, ok := s.files[path]
	if !ok {
		return nil, repostore.ErrFileNotFound
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, xerror.ExecutionUnavailable().Detail("repository file exceeds byte limit")
	}
	return append([]byte(nil), data...), nil
}

func (*snapshotRepoStore) WriteFile(context.Context, string, []byte, string) error {
	return xerror.ExecutionUnavailable().Detail("remote source snapshot is read-only")
}

func (*snapshotRepoStore) DeleteFile(context.Context, string, string) error {
	return xerror.ExecutionUnavailable().Detail("remote source snapshot is read-only")
}

func (s *snapshotRepoStore) ListFiles(_ context.Context, dir string) ([]repostore.FileEntry, error) {
	prefix := strings.Trim(strings.TrimSpace(dir), "/")
	if prefix != "" && prefix != "." {
		prefix += "/"
	} else {
		prefix = ""
	}
	entries := make([]repostore.FileEntry, 0, len(s.files))
	for path, data := range s.files {
		if strings.HasPrefix(path, prefix) {
			entries = append(entries, repostore.FileEntry{Path: path, Size: int64(len(data))})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func (s *snapshotRepoStore) FileExists(_ context.Context, path string) (bool, error) {
	_, ok := s.files[path]
	return ok, nil
}

func ExecuteGitSourceSnapshot(ctx context.Context, request RemoteSourceRequest) (*RemoteSourceSnapshot, error) {
	if request.MaxBytes <= 0 || request.MaxFiles <= 0 {
		return nil, xerror.ExecutionUnavailable().Detail("Git source snapshot limits are required")
	}
	auth, err := datasourcessvc.PrepareGitExecutionAuth(ctx, request.Auth)
	if err != nil {
		return nil, err
	}
	defer auth.Cleanup()
	remote := repostore.NewRemoteGit()
	if err := remote.CheckRemote(ctx, auth.RepoURL, auth.Env); err != nil {
		return nil, xerror.Wrapf("Git source pre-flight check: %w", err)
	}
	store, root, cleanup, err := remote.CloneRemote(ctx, auth.RepoURL, request.Branch, auth.Env)
	if err != nil {
		return nil, xerror.Wrapf("clone Git source: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	entries, err := store.ListFiles(ctx, "")
	if err != nil {
		return nil, err
	}
	if len(entries) > request.MaxFiles {
		return nil, xerror.ExecutionUnavailable().Detail("Git source exceeds file-count limit")
	}
	files := make(map[string][]byte, len(entries))
	var total int64
	for _, entry := range entries {
		remaining := request.MaxBytes - total
		if remaining <= 0 || entry.Size > remaining {
			return nil, xerror.ExecutionUnavailable().Detail("Git source exceeds byte limit")
		}
		data, err := store.ReadFile(ctx, entry.Path, remaining)
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		files[entry.Path] = data
	}
	metadata := repostore.ResolveGitMetadata(ctx, root)
	sourceRef := metadata.SourceRef
	if request.Branch != "" {
		ref := "refs/heads/" + request.Branch
		sourceRef = &ref
	}
	normalized := repostore.NormalizeRemoteURL(auth.RepoURL)
	return &RemoteSourceSnapshot{
		Files: files, SourceRepo: &normalized, SourceRef: sourceRef,
		LastAppliedCommit: metadata.LastCommit,
	}, nil
}
