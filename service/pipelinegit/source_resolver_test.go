package pipelinegit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

type fakeProjectBackend struct {
	getFn func(context.Context, slug.Slug, slug.Slug) (*model.Project, error)
}

func (f fakeProjectBackend) GetProject(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
) (*model.Project, error) {
	return f.getFn(ctx, orgSlug, projectSlug)
}

type fakeGitAuthResolver struct {
	resolveFn func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitDataSourceAuth, error)
}

func (f fakeGitAuthResolver) ResolveGitAuth(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (*datasourcessvc.GitDataSourceAuth, error) {
	return f.resolveFn(ctx, orgSlug, dsSlug)
}

func TestResolver_UsesProjectPipelineBackend(t *testing.T) {
	t.Parallel()

	backendSlug := slug.Slug("pipelines-git")
	var resolvedSlug slug.Slug
	authCleanupCalled := false
	cloneCleanupCalled := false
	repoStore, repoRoot, commit := newCommittedSourceRepo(t)
	remote := &recordingRemoteGit{
		repoStore: repoStore,
		repoRoot:  repoRoot,
		cleanup: func() {
			cloneCleanupCalled = true
		},
	}
	resolver := &resolver{
		projectBackend: fakeProjectBackend{
			getFn: func(_ context.Context, orgSlug, projectSlug slug.Slug) (*model.Project, error) {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				require.Equal(t, slug.Slug("default"), projectSlug)
				return &model.Project{
					Slug: projectSlug,
					PipelineBackend: &model.ProjectPipelineBackend{
						DataSourceSlug: backendSlug,
						Repo:           "https://example.com/acme/pipelines.git",
						TrackedBranch:  "main",
					},
				}, nil
			},
		},
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(
				_ context.Context,
				orgSlug, dsSlug slug.Slug,
			) (*datasourcessvc.GitDataSourceAuth, error) {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				resolvedSlug = dsSlug
				return &datasourcessvc.GitDataSourceAuth{
					RepoURL:       "https://token@example.com/acme/pipelines.git",
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

	source, err := resolver.Resolve(context.Background(), slug.Slug("acme"), SourceRequest{
		ProjectSlug: slug.Slug("default"),
	})

	require.NoError(t, err)
	require.NotNil(t, source)
	require.Equal(t, backendSlug, resolvedSlug)
	require.Equal(t, "https://token@example.com/acme/pipelines.git", remote.checkedRepoURL)
	require.Equal(t, []string{"GIT_ASKPASS=/tmp/askpass"}, remote.checkedEnv)
	require.Equal(t, "main", remote.clonedBranch)
	require.Equal(t, backendSlug, source.DataSourceSlug)
	require.Equal(t, "https://example.com/acme/pipelines.git", *source.SourceRepo)
	require.Equal(t, "refs/heads/main", *source.SourceRef)
	require.Equal(t, commit, *source.LastAppliedCommit)

	source.Cleanup()
	require.True(t, cloneCleanupCalled)
	require.True(t, authCleanupCalled)
}

func TestResolver_RequiresConfiguredProjectPipelineBackend(t *testing.T) {
	t.Parallel()

	resolver := &resolver{
		projectBackend: fakeProjectBackend{
			getFn: func(_ context.Context, _, projectSlug slug.Slug) (*model.Project, error) {
				return &model.Project{Slug: projectSlug}, nil
			},
		},
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitDataSourceAuth, error) {
				t.Fatal("git auth must not be resolved when the project backend is unset")
				return nil, context.Canceled
			},
		},
	}

	_, err := resolver.Resolve(
		context.Background(),
		slug.Slug("acme"),
		SourceRequest{
			ProjectSlug: slug.Slug("default"),
		},
	)

	require.ErrorIs(t, err, ErrSourceRequired)
}

func TestResolver_UsesTrackedBranchFromProjectBackendIdentity(t *testing.T) {
	t.Parallel()

	backendSlug := slug.Slug("pipelines-git")
	repoStore, repoRoot, commit := newCommittedSourceRepo(t)
	remote := &recordingRemoteGit{
		repoStore: repoStore,
		repoRoot:  repoRoot,
	}
	resolver := &resolver{
		projectBackend: fakeProjectBackend{
			getFn: func(_ context.Context, orgSlug, projectSlug slug.Slug) (*model.Project, error) {
				require.Equal(t, slug.Slug("acme"), orgSlug)
				require.Equal(t, slug.Slug("default"), projectSlug)
				return &model.Project{
					Slug: projectSlug,
					PipelineBackend: &model.ProjectPipelineBackend{
						DataSourceSlug: backendSlug,
						Repo:           "https://example.com/acme/pipelines.git",
						TrackedBranch:  "release",
					},
				}, nil
			},
		},
		gitAuthResolver: fakeGitAuthResolver{
			resolveFn: func(_ context.Context, _, dsSlug slug.Slug) (*datasourcessvc.GitDataSourceAuth, error) {
				require.Equal(t, backendSlug, dsSlug)
				return &datasourcessvc.GitDataSourceAuth{
					RepoURL:       "https://token@example.com/acme/pipelines.git",
					Env:           []string{"GIT_ASKPASS=/tmp/askpass"},
					DefaultBranch: "main",
				}, nil
			},
		},
		remoteGit: remote,
	}

	source, err := resolver.Resolve(context.Background(), slug.Slug("acme"), SourceRequest{
		ProjectSlug: slug.Slug("default"),
	})

	require.NoError(t, err)
	require.NotNil(t, source)
	require.Equal(t, "release", remote.clonedBranch)
	require.Equal(t, "refs/heads/release", *source.SourceRef)
	require.Equal(t, commit, *source.LastAppliedCommit)
}

func newCommittedSourceRepo(t *testing.T) (repostore.RepoStore, string, string) {
	t.Helper()

	ctx := context.Background()
	repoRoot := t.TempDir()
	runSourceResolverGit(t, ctx, repoRoot, "init")
	runSourceResolverGit(t, ctx, repoRoot, "config", "user.name", "source-fixture")
	runSourceResolverGit(t, ctx, repoRoot, "config", "user.email", "source-fixture@example.com")
	runSourceResolverGit(t, ctx, repoRoot, "config", "commit.gpgsign", "false")

	pipelinePath := filepath.Join(repoRoot, "pipelines", "daily-sync.pipeline.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(pipelinePath), 0o750))
	require.NoError(t, os.WriteFile(pipelinePath, []byte("metadata:\n  slug: daily-sync\n"), 0o600))
	runSourceResolverGit(t, ctx, repoRoot, "add", "-A")
	runSourceResolverGit(t, ctx, repoRoot, "commit", "-m", "seed pipeline")

	metadata := repostore.ResolveGitMetadata(ctx, repoRoot)
	require.NotNil(t, metadata.LastCommit)

	return &sourceResolverRepoStore{}, repoRoot, *metadata.LastCommit
}

type sourceResolverRepoStore struct{}

func (sourceResolverRepoStore) ReadFile(context.Context, string, int64) ([]byte, error) {
	return nil, nil
}

func (sourceResolverRepoStore) WriteFile(context.Context, string, []byte, string) error {
	return nil
}

func (sourceResolverRepoStore) DeleteFile(context.Context, string, string) error {
	return nil
}

func (sourceResolverRepoStore) ListFiles(context.Context, string) ([]repostore.FileEntry, error) {
	return nil, nil
}

func (sourceResolverRepoStore) FileExists(context.Context, string) (bool, error) {
	return false, nil
}

func runSourceResolverGit(t *testing.T, ctx context.Context, repoRoot string, args ...string) {
	t.Helper()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoRoot
	cmd.Env = sourceResolverGitEnv()
	output, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s failed: %s", strings.Join(args, " "), string(output))
}

func sourceResolverGitEnv() []string {
	base := os.Environ()
	filtered := make([]string, 0, len(base))
	for _, entry := range base {
		if strings.HasPrefix(entry, "GIT_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=commit.gpgsign",
		"GIT_CONFIG_VALUE_0=false",
	)
}
