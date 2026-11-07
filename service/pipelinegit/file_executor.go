package pipelinegit

import (
	"bytes"
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// FileExecutor applies outbound pipeline file changes to a git data source.
type FileExecutor interface {
	WriteFile(ctx context.Context, op WriteFileOperation) error
	DeleteFile(ctx context.Context, op DeleteFileOperation) error
}

// WriteFileOperation describes one outbound pipeline file write.
type WriteFileOperation struct {
	OrganizationSlug slug.Slug
	DataSourceSlug   slug.Slug
	PipelineSlug     slug.Slug
	Branch           *string
	Path             string
	Content          []byte
	CommitMessage    string
	FailIfExists     bool
}

// DeleteFileOperation describes one outbound pipeline file delete.
type DeleteFileOperation struct {
	OrganizationSlug slug.Slug
	DataSourceSlug   slug.Slug
	PipelineSlug     slug.Slug
	Branch           *string
	Path             string
	CommitMessage    string
}

type fileExecutor struct {
	gitAuthResolver DataSourceGitAuthResolver
	remoteGit       repostore.RemoteGit
}

// NewFileExecutor creates the production executor for outbound pipeline git file operations.
func NewFileExecutor(gitAuthResolver DataSourceGitAuthResolver) (FileExecutor, error) {
	if gitAuthResolver == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("git auth resolver is required"),
		)
	}

	return &fileExecutor{
		gitAuthResolver: gitAuthResolver,
		remoteGit:       repostore.NewRemoteGit(),
	}, nil
}

func (e *fileExecutor) WriteFile(ctx context.Context, op WriteFileOperation) error {
	return e.withClonedRepo(
		ctx,
		op.OrganizationSlug,
		op.DataSourceSlug,
		op.Branch,
		func(ctx context.Context, repoStore repostore.RepoStore) (bool, error) {
			current, err := repoStore.ReadFile(ctx, op.Path, 0)
			if err != nil && !xerror.Is(err, repostore.ErrFileNotFound) {
				return false, err
			}
			if err == nil && bytes.Equal(current, op.Content) {
				return false, nil
			}
			if err == nil && op.FailIfExists {
				return false, repostore.ErrFileExists.Detailf("pipeline git source path %s already exists", op.Path)
			}
			if err := repoStore.WriteFile(ctx, op.Path, op.Content, op.CommitMessage); err != nil {
				return false, err
			}
			return true, nil
		},
	)
}

func (e *fileExecutor) DeleteFile(ctx context.Context, op DeleteFileOperation) error {
	return e.withClonedRepo(
		ctx,
		op.OrganizationSlug,
		op.DataSourceSlug,
		op.Branch,
		func(ctx context.Context, repoStore repostore.RepoStore) (bool, error) {
			if err := repoStore.DeleteFile(ctx, op.Path, op.CommitMessage); err != nil {
				if xerror.Is(err, repostore.ErrFileNotFound) {
					return false, nil
				}
				return false, err
			}
			return true, nil
		},
	)
}

func (e *fileExecutor) withClonedRepo(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	branchOverride *string,
	mutate func(context.Context, repostore.RepoStore) (bool, error),
) error {
	auth, err := e.gitAuthResolver.ResolveGitAuth(ctx, orgSlug, dsSlug)
	if err != nil {
		return xerror.Wrapf("resolve git datasource auth: %w", err)
	}
	if auth.Cleanup != nil {
		defer auth.Cleanup()
	}

	branch := strings.TrimSpace(auth.DefaultBranch)
	if branchOverride != nil && strings.TrimSpace(*branchOverride) != "" {
		branch = strings.TrimSpace(*branchOverride)
	}

	if err := e.remoteGit.CheckRemote(ctx, auth.RepoURL, auth.Env); err != nil {
		return xerror.Wrapf("git datasource pre-flight check: %w", err)
	}

	repoStore, repoRoot, cleanup, err := e.remoteGit.CloneRemote(
		ctx,
		auth.RepoURL,
		branch,
		auth.Env,
	)
	if err != nil {
		return xerror.Wrapf("clone git datasource: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	changed, err := mutate(ctx, repoStore)
	if err != nil {
		return xerror.Wrapf("mutate git repository: %w", err)
	}
	if !changed {
		return nil
	}

	if err := e.remoteGit.PushRemote(
		ctx,
		repoRoot,
		auth.RepoURL,
		auth.Env,
		pushOptionsForBranch(branch),
	); err != nil {
		return xerror.Wrapf("push git changes: %w", err)
	}

	return nil
}

func pushOptionsForBranch(branch string) repostore.PushOptions {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return repostore.PushOptions{}
	}

	return repostore.PushOptions{Refspec: "HEAD:refs/heads/" + branch}
}
