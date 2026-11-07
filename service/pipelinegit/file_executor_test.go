package pipelinegit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

func TestFileExecutor_WriteFileClonesMutatesPushesAndCleansUp(t *testing.T) {
	t.Parallel()

	authCleanupCalled := false
	cloneCleanupCalled := false
	repo := &recordingOperationRepoStore{}
	remote := &recordingRemoteGit{
		repoStore: repo,
		repoRoot:  "/tmp/pipelines-repo",
		cleanup: func() {
			cloneCleanupCalled = true
		},
	}
	executor := &fileExecutor{
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(
				_ context.Context,
				orgSlug, dsSlug slug.Slug,
			) (*datasourcessvc.GitDataSourceAuth, error) {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				require.Equal(t, slug.Slug("pipelines-git"), dsSlug)
				return &datasourcessvc.GitDataSourceAuth{
					RepoURL:       "https://example.com/acme/pipelines.git",
					Env:           []string{"GIT_ASKPASS=/tmp/askpass"},
					DefaultBranch: "main",
					Cleanup: func() {
						authCleanupCalled = true
					},
				}, nil
			},
		},
		remoteGit: remote,
	}

	err := executor.WriteFile(context.Background(), WriteFileOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: daily-sync\n"),
		CommitMessage:    "export pipeline daily-sync",
	})

	require.NoError(t, err)
	require.Equal(t, "https://example.com/acme/pipelines.git", remote.checkedRepoURL)
	require.Equal(t, []string{"GIT_ASKPASS=/tmp/askpass"}, remote.checkedEnv)
	require.Equal(t, "main", remote.clonedBranch)
	require.Zero(t, repo.readMaxBytes)
	require.Equal(t, "pipelines/daily-sync.pipeline.yaml", repo.writtenPath)
	require.Equal(t, []byte("metadata:\n  slug: daily-sync\n"), repo.writtenContent)
	require.Equal(t, "export pipeline daily-sync", repo.writeMessage)
	require.Equal(t, "/tmp/pipelines-repo", remote.pushedRepoDir)
	require.Equal(t, "https://example.com/acme/pipelines.git", remote.pushedRepoURL)
	require.Equal(t, []string{"GIT_ASKPASS=/tmp/askpass"}, remote.pushedEnv)
	require.Equal(t, repostore.PushOptions{Refspec: "HEAD:refs/heads/main"}, remote.pushedOptions)
	require.True(t, cloneCleanupCalled)
	require.True(t, authCleanupCalled)
}

func TestFileExecutor_WriteFileTreatsIdenticalContentAsSuccess(t *testing.T) {
	t.Parallel()

	repo := &recordingOperationRepoStore{
		readContent: []byte("metadata:\n  slug: daily-sync\n"),
	}
	remote := &recordingRemoteGit{repoStore: repo, repoRoot: "/tmp/pipelines-repo"}
	executor := &fileExecutor{
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
			) (*datasourcessvc.GitDataSourceAuth, error) {
				return &datasourcessvc.GitDataSourceAuth{
					RepoURL:       "https://example.com/acme/pipelines.git",
					DefaultBranch: "main",
				}, nil
			},
		},
		remoteGit: remote,
	}

	err := executor.WriteFile(context.Background(), WriteFileOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: daily-sync\n"),
		CommitMessage:    "export pipeline daily-sync",
	})

	require.NoError(t, err)
	require.Equal(t, "pipelines/daily-sync.pipeline.yaml", repo.readPath)
	require.Zero(t, repo.readMaxBytes)
	require.Empty(t, repo.writtenPath)
	require.Equal(t, 0, remote.pushCalls)
}

func TestFileExecutor_WriteFileFailsWhenTargetExistsAndRequested(t *testing.T) {
	t.Parallel()

	repo := &recordingOperationRepoStore{
		readContent: []byte("metadata:\n  slug: daily-sync\n"),
	}
	remote := &recordingRemoteGit{repoStore: repo, repoRoot: "/tmp/pipelines-repo"}
	executor := &fileExecutor{
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
			) (*datasourcessvc.GitDataSourceAuth, error) {
				return &datasourcessvc.GitDataSourceAuth{
					RepoURL:       "https://example.com/acme/pipelines.git",
					DefaultBranch: "main",
				}, nil
			},
		},
		remoteGit: remote,
	}

	err := executor.WriteFile(context.Background(), WriteFileOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: other-sync\n"),
		CommitMessage:    "export pipeline daily-sync",
		FailIfExists:     true,
	})

	require.ErrorIs(t, err, repostore.ErrFileExists)
	require.Equal(t, "pipelines/daily-sync.pipeline.yaml", repo.readPath)
	require.Zero(t, repo.readMaxBytes)
	require.Empty(t, repo.writtenPath)
	require.Equal(t, 0, remote.pushCalls)
}

func TestFileExecutor_WriteFileTreatsIdenticalContentAsSuccessWhenFailIfExists(t *testing.T) {
	t.Parallel()

	repo := &recordingOperationRepoStore{
		readContent: []byte("metadata:\n  slug: daily-sync\n"),
	}
	remote := &recordingRemoteGit{repoStore: repo, repoRoot: "/tmp/pipelines-repo"}
	executor := &fileExecutor{
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
			) (*datasourcessvc.GitDataSourceAuth, error) {
				return &datasourcessvc.GitDataSourceAuth{
					RepoURL:       "https://example.com/acme/pipelines.git",
					DefaultBranch: "main",
				}, nil
			},
		},
		remoteGit: remote,
	}

	err := executor.WriteFile(context.Background(), WriteFileOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: daily-sync\n"),
		CommitMessage:    "export pipeline daily-sync",
		FailIfExists:     true,
	})

	require.NoError(t, err)
	require.Equal(t, "pipelines/daily-sync.pipeline.yaml", repo.readPath)
	require.Zero(t, repo.readMaxBytes)
	require.Empty(t, repo.writtenPath)
	require.Equal(t, 0, remote.pushCalls)
}

func TestFileExecutor_WriteFileWritesMissingFile(t *testing.T) {
	t.Parallel()

	repo := &recordingOperationRepoStore{
		readErr: repostore.ErrFileNotFound.Detail("pipelines/daily-sync.pipeline.yaml"),
	}
	remote := &recordingRemoteGit{repoStore: repo, repoRoot: "/tmp/pipelines-repo"}
	executor := &fileExecutor{
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
			) (*datasourcessvc.GitDataSourceAuth, error) {
				return &datasourcessvc.GitDataSourceAuth{
					RepoURL:       "https://example.com/acme/pipelines.git",
					DefaultBranch: "main",
				}, nil
			},
		},
		remoteGit: remote,
	}

	err := executor.WriteFile(context.Background(), WriteFileOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: daily-sync\n"),
		CommitMessage:    "export pipeline daily-sync",
	})

	require.NoError(t, err)
	require.Equal(t, "pipelines/daily-sync.pipeline.yaml", repo.readPath)
	require.Zero(t, repo.readMaxBytes)
	require.Equal(t, "pipelines/daily-sync.pipeline.yaml", repo.writtenPath)
	require.Equal(t, 1, remote.pushCalls)
}

func TestFileExecutor_DeleteFileUsesExplicitBranch(t *testing.T) {
	t.Parallel()

	branch := "release"
	repo := &recordingOperationRepoStore{}
	remote := &recordingRemoteGit{repoStore: repo, repoRoot: "/tmp/pipelines-repo"}
	executor := &fileExecutor{
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
			) (*datasourcessvc.GitDataSourceAuth, error) {
				return &datasourcessvc.GitDataSourceAuth{
					RepoURL:       "https://example.com/acme/pipelines.git",
					DefaultBranch: "main",
				}, nil
			},
		},
		remoteGit: remote,
	}

	err := executor.DeleteFile(context.Background(), DeleteFileOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Branch:           &branch,
		Path:             "pipelines/daily-sync.pipeline.yaml",
		CommitMessage:    "remove pipeline daily-sync",
	})

	require.NoError(t, err)
	require.Equal(t, branch, remote.clonedBranch)
	require.Equal(t, "pipelines/daily-sync.pipeline.yaml", repo.deletedPath)
	require.Equal(t, "remove pipeline daily-sync", repo.deleteMessage)
	require.Equal(t, repostore.PushOptions{Refspec: "HEAD:refs/heads/release"}, remote.pushedOptions)
}

func TestFileExecutor_DeleteFileTreatsMissingFileAsSuccess(t *testing.T) {
	t.Parallel()

	repo := &recordingOperationRepoStore{
		deleteErr: repostore.ErrFileNotFound.Detail("pipelines/daily-sync.pipeline.yaml"),
	}
	remote := &recordingRemoteGit{repoStore: repo, repoRoot: "/tmp/pipelines-repo"}
	executor := &fileExecutor{
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
			) (*datasourcessvc.GitDataSourceAuth, error) {
				return &datasourcessvc.GitDataSourceAuth{
					RepoURL:       "https://example.com/acme/pipelines.git",
					DefaultBranch: "main",
				}, nil
			},
		},
		remoteGit: remote,
	}

	err := executor.DeleteFile(context.Background(), DeleteFileOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Path:             "pipelines/daily-sync.pipeline.yaml",
		CommitMessage:    "remove pipeline daily-sync",
	})

	require.NoError(t, err)
	require.Equal(t, "pipelines/daily-sync.pipeline.yaml", repo.deletedPath)
	require.Equal(t, "remove pipeline daily-sync", repo.deleteMessage)
	require.Equal(t, 0, remote.pushCalls)
}

func TestFileExecutor_ReturnsPushErrorForRetry(t *testing.T) {
	t.Parallel()

	pushErr := xerror.Unexpected().Detail("moved head")
	executor := &fileExecutor{
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(
				context.Context,
				slug.Slug,
				slug.Slug,
			) (*datasourcessvc.GitDataSourceAuth, error) {
				return &datasourcessvc.GitDataSourceAuth{
					RepoURL:       "https://example.com/acme/pipelines.git",
					DefaultBranch: "main",
				}, nil
			},
		},
		remoteGit: &recordingRemoteGit{
			repoStore: &recordingOperationRepoStore{},
			repoRoot:  "/tmp/pipelines-repo",
			pushErr:   pushErr,
		},
	}

	err := executor.WriteFile(context.Background(), WriteFileOperation{
		OrganizationSlug: slug.Slug("acme"),
		DataSourceSlug:   slug.Slug("pipelines-git"),
		PipelineSlug:     slug.Slug("daily-sync"),
		Path:             "pipelines/daily-sync.pipeline.yaml",
		Content:          []byte("metadata:\n  slug: daily-sync\n"),
		CommitMessage:    "export pipeline daily-sync",
	})

	require.ErrorIs(t, err, pushErr)
	require.ErrorContains(t, err, "push git changes")
}

type recordingRemoteGit struct {
	repoStore        repostore.RepoStore
	repoRoot         string
	cleanup          func()
	pushErr          error
	cloneErrByBranch map[string]error

	checkedRepoURL string
	checkedEnv     []string
	clonedBranch   string
	pushedRepoDir  string
	pushedRepoURL  string
	pushedEnv      []string
	pushedOptions  repostore.PushOptions
	pushCalls      int
}

func (r *recordingRemoteGit) CheckRemote(
	_ context.Context,
	repoURL string,
	env []string,
) error {
	r.checkedRepoURL = repoURL
	r.checkedEnv = env
	return nil
}

func (r *recordingRemoteGit) ListRemoteBranches(
	_ context.Context,
	_ string,
	_ []string,
) ([]repostore.RemoteBranch, error) {
	return []repostore.RemoteBranch{}, nil
}

func (r *recordingRemoteGit) CloneRemote(
	_ context.Context,
	_ string,
	branch string,
	_ []string,
) (repostore.RepoStore, string, func(), error) {
	r.clonedBranch = branch
	if err := r.cloneErrByBranch[branch]; err != nil {
		return nil, "", nil, err
	}
	return r.repoStore, r.repoRoot, r.cleanup, nil
}

func (r *recordingRemoteGit) PushRemote(
	_ context.Context,
	repoDir, repoURL string,
	env []string,
	opts repostore.PushOptions,
) error {
	r.pushCalls++
	r.pushedRepoDir = repoDir
	r.pushedRepoURL = repoURL
	r.pushedEnv = env
	r.pushedOptions = opts
	return r.pushErr
}

type recordingOperationRepoStore struct {
	readPath       string
	readMaxBytes   int64
	readContent    []byte
	readErr        error
	writtenPath    string
	writtenContent []byte
	writeMessage   string
	deletedPath    string
	deleteMessage  string
	deleteErr      error
}

func (r *recordingOperationRepoStore) ReadFile(_ context.Context, path string, maxBytes int64) ([]byte, error) {
	r.readPath = path
	r.readMaxBytes = maxBytes
	return r.readContent, r.readErr
}

func (r *recordingOperationRepoStore) WriteFile(
	_ context.Context,
	path string,
	data []byte,
	message string,
) error {
	r.writtenPath = path
	r.writtenContent = data
	r.writeMessage = message
	return nil
}

func (r *recordingOperationRepoStore) DeleteFile(
	_ context.Context,
	path, message string,
) error {
	r.deletedPath = path
	r.deleteMessage = message
	return r.deleteErr
}

func (r *recordingOperationRepoStore) ListFiles(
	context.Context,
	string,
) ([]repostore.FileEntry, error) {
	return nil, nil
}

func (r *recordingOperationRepoStore) FileExists(context.Context, string) (bool, error) {
	return false, nil
}
