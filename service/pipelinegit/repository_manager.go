package pipelinegit

import (
	"context"
	"fmt"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	githubprovider "github.com/Braidplane/braidplane-core/pkg/gitprovider/github"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var ErrRepositoryBootstrapUnsupported = xerror.InvalidInput(xerror.RepoFile).
	Detail("repository bootstrap is supported only for GitHub connections")

type RepositoryBootstrapOperation struct {
	OrganizationSlug slug.Slug
	DataSourceSlug   slug.Slug
	Owner            string
	Name             string
	RepositoryID     string
	DefaultBranch    string
	PipelineDir      string
}

type RepositoryBootstrapResult struct {
	Provider        string
	Owner           string
	OwnerKind       string
	Repository      string
	RepositoryID    string
	RemoteURL       string
	DefaultBranch   string
	PipelineDir     string
	FirstCommitSHA  string
	TemplateVersion string
}

type RepositoryManager interface {
	ValidateRepository(context.Context, slug.Slug, slug.Slug, string) error
	BootstrapRepository(context.Context, RepositoryBootstrapOperation) (*RepositoryBootstrapResult, error)
	DeleteRepository(context.Context, RepositoryBootstrapOperation) error
}

type repositoryManager struct{ gitAuthResolver DataSourceGitAuthResolver }

func NewRepositoryManager(resolver DataSourceGitAuthResolver) (RepositoryManager, error) {
	if resolver == nil {
		return nil, xerror.ExecutionUnavailable().Detail("git auth resolver is required")
	}
	return &repositoryManager{gitAuthResolver: resolver}, nil
}

func (m *repositoryManager) ValidateRepository(ctx context.Context, orgSlug, dsSlug slug.Slug, branch string) error {
	client, cleanup, err := m.client(ctx, orgSlug, dsSlug, nil)
	if err != nil {
		return err
	}
	defer cleanup()
	return client.ValidateRepository(ctx, branch)
}

func (m *repositoryManager) BootstrapRepository(
	ctx context.Context,
	op RepositoryBootstrapOperation,
) (*RepositoryBootstrapResult, error) {
	target := repositoryTarget(op.Owner, op.Name)
	client, cleanup, err := m.client(ctx, op.OrganizationSlug, op.DataSourceSlug, &target)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	result, err := client.BootstrapRepository(ctx, gitprovider.RepositoryBootstrapRequest{
		Owner:         strings.TrimSpace(op.Owner),
		Name:          strings.TrimSpace(op.Name),
		DefaultBranch: strings.TrimSpace(op.DefaultBranch),
		Files:         RepositoryTemplate(strings.TrimSpace(op.PipelineDir)),
	})
	if err != nil {
		return nil, err
	}
	return &RepositoryBootstrapResult{
		Provider:        string(repostore.ProviderGitHub),
		Owner:           result.Repository.Owner,
		OwnerKind:       result.OwnerKind,
		Repository:      result.Repository.FullName,
		RepositoryID:    result.RepositoryID,
		RemoteURL:       result.RemoteURL,
		DefaultBranch:   strings.TrimSpace(op.DefaultBranch),
		PipelineDir:     strings.TrimSpace(op.PipelineDir),
		FirstCommitSHA:  result.HeadSHA,
		TemplateVersion: model.PipelineRepositoryTemplateVersion,
	}, nil
}

func (m *repositoryManager) DeleteRepository(ctx context.Context, op RepositoryBootstrapOperation) error {
	target := repositoryTarget(op.Owner, op.Name)
	client, cleanup, err := m.client(ctx, op.OrganizationSlug, op.DataSourceSlug, &target)
	if err != nil {
		return err
	}
	defer cleanup()
	return client.DeleteRepository(ctx, op.RepositoryID)
}

func repositoryTarget(owner, name string) gitprovider.RepositoryRef {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	fullName := owner + "/" + name
	return gitprovider.RepositoryRef{
		Provider:    repostore.ProviderGitHub,
		Owner:       owner,
		Name:        name,
		ProjectPath: fullName,
		FullName:    fullName,
	}
}

func (m *repositoryManager) client(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	target *gitprovider.RepositoryRef,
) (gitprovider.RepositoryClient, func(), error) {
	auth, err := m.gitAuthResolver.ResolveGitAuth(ctx, orgSlug, dsSlug)
	if err != nil {
		return nil, nil, xerror.Wrapf("resolve repository connection: %w", err)
	}
	cleanup := auth.Cleanup
	if cleanup == nil {
		cleanup = func() {}
	}
	if auth.Provider != string(repostore.ProviderGitHub) {
		cleanup()
		return nil, nil, ErrRepositoryBootstrapUnsupported
	}
	token := tokenFromEnv(auth.Env, "GITHUB_TOKEN")
	if token == "" {
		cleanup()
		return nil, nil, xerror.InvalidCredentials().Detail("GitHub connection token is required")
	}
	ref, err := gitprovider.RepositoryRefFromRemoteURL(repostore.ProviderGitHub, auth.RepoURL)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if target != nil {
		ref = *target
	}
	client, err := githubprovider.New(ctx, gitprovider.Config{}, gitprovider.Credentials{
		Token: token, GitHubContentsPermission: auth.GitHubContentsPermission,
	}, ref)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return client, cleanup, nil
}

// RepositoryTemplate initializes the same Git-owned layout for each supported provider.
func RepositoryTemplate(pipelineDir string) []gitprovider.RepositoryFile {
	return []gitprovider.RepositoryFile{
		{
			Path:    ".braidplane/template.yaml",
			Content: fmt.Sprintf("version: %s\npipeline_dir: %q\n", model.PipelineRepositoryTemplateVersion, pipelineDir),
		},
		{
			Path:    pipelineDir + "/README.md",
			Content: "# Pipeline definitions\n\nAdd canonical `*.pipeline.yaml` or `*.pipeline.json` files here. Keep credentials and environment bindings in Braidplane.\n",
		},
	}
}
