package projectssvc

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/ptr"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	gitconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/git"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

var (
	commitLikeBranchPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	githubOwnerPattern      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	githubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

type resolvedProjectPipelineBackend struct {
	requireUnconfigured bool
	dataSourceSlug      slug.Slug
	provider            model.DataSourceType
	repo                string
	trackedBranch       string
	pipelineDir         string
	firstCommitSHA      *string
	createdRepoURL      string
	proposalAuthority   *datasourcessvc.GitHubProposalAuthority
}

func (s *service) SetProjectPipelineBackend(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	params model.SetProjectPipelineBackend,
) (*model.Project, error) {
	if err := validate.Default().Struct(params); err != nil {
		return nil, xerror.ValidationFailed().With(err)
	}

	trackedBranch, err := validateTrackedBranch(params.TrackedBranch)
	if err != nil {
		return nil, err
	}
	pipelineDir, err := validatePipelineDir(params.PipelineDir)
	if err != nil {
		return nil, err
	}

	backend, err := s.resolvePipelineBackend(ctx, orgSlug, params.DataSourceSlug, trackedBranch, pipelineDir)
	if err != nil {
		return nil, err
	}

	if params.ExpectedRepository != "" && !sameSetupRepository(backend.repo, params.ExpectedRepository) {
		return nil, xerror.AlreadyExists(xerror.Project).Detail("repository source changed during project setup")
	}
	backend.requireUnconfigured = params.RequireUnconfigured
	switch params.Mode {
	case model.SetProjectPipelineBackendModeAdoptExisting:
		if backend.provider == model.DataSourceTypeGithub {
			if s.repositoryManager == nil {
				return nil, xerror.ExecutionUnavailable().Detail("repository validation is unavailable")
			}
			if err := s.repositoryManager.ValidateRepository(ctx, orgSlug, backend.dataSourceSlug, backend.trackedBranch); err != nil {
				return nil, xerror.Wrapf("validate project pipeline repository permissions: %w", err)
			}
		}
		return s.adoptProjectPipelineBackend(ctx, orgSlug, projectSlug, backend)
	case model.SetProjectPipelineBackendModeCreateFromTemplate:
		return s.createProjectPipelineBackend(ctx, orgSlug, projectSlug, params, backend)
	default:
		return nil, xerror.ValidationFailed().Detail("pipeline backend mode is required")
	}
}

func (s *service) adoptProjectPipelineBackend(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	backend resolvedProjectPipelineBackend,
) (*model.Project, error) {
	repoStore, lastAppliedCommit, cleanup, err := s.openProjectBackendRepo(ctx, orgSlug, backend)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	if lastAppliedCommit == nil || strings.TrimSpace(ptr.Value(lastAppliedCommit)) == "" {
		return nil, ErrPipelineBackendReconcileFailed.Detail("repository branch has no commit to register")
	}
	if backend.firstCommitSHA != nil &&
		!strings.EqualFold(strings.TrimSpace(ptr.Value(backend.firstCommitSHA)), strings.TrimSpace(ptr.Value(lastAppliedCommit))) {
		return nil, ErrPipelineBackendReconcileFailed.Detail("repository changed before its first commit could be validated")
	}
	backend.firstCommitSHA = lastAppliedCommit

	if err := s.reconcileAdoptedProjectPipelineBackend(
		ctx,
		orgSlug,
		projectSlug,
		backend,
		repoStore,
		lastAppliedCommit,
	); err != nil {
		return nil, err
	}

	return s.GetProject(ctx, orgSlug, projectSlug)
}

func (s *service) createProjectPipelineBackend(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	params model.SetProjectPipelineBackend,
	backend resolvedProjectPipelineBackend,
) (*model.Project, error) {
	if s.repositoryManager == nil {
		return nil, xerror.ExecutionUnavailable().Detail("repository bootstrap is unavailable")
	}
	if backend.provider != model.DataSourceTypeGithub {
		return nil, pipelinegit.ErrRepositoryBootstrapUnsupported
	}
	if backend.proposalAuthority != nil {
		return nil, pipelinegit.ErrRepositoryBootstrapUnsupported.Detail("managed GitHub sources are bound to one selected repository; use adopt_existing")
	}
	owner := strings.TrimSpace(params.ProviderOwner)
	name := strings.TrimSpace(params.RepositoryName)
	if owner == "" || name == "" {
		return nil, xerror.ValidationFailed().Detail("provider_owner and repository_name are required")
	}
	if !githubOwnerPattern.MatchString(owner) || !githubRepositoryPattern.MatchString(name) || name == "." || name == ".." {
		return nil, xerror.ValidationFailed().Detail("provider_owner or repository_name is not a valid GitHub repository target")
	}
	backend.repo = repostore.NormalizeRemoteURL("https://github.com/" + owner + "/" + name + ".git")
	current, err := s.GetProject(ctx, orgSlug, projectSlug)
	if err != nil {
		return nil, err
	}
	if current.PipelineBackend != nil && current.PipelineBackend.DataSourceSlug == backend.dataSourceSlug &&
		strings.EqualFold(current.PipelineBackend.Repo, backend.repo) &&
		current.PipelineBackend.TrackedBranch == backend.trackedBranch &&
		current.PipelineBackend.PipelineDir == backend.pipelineDir {
		return current, nil
	}
	created, err := s.repositoryManager.BootstrapRepository(ctx, pipelinegit.RepositoryBootstrapOperation{
		OrganizationSlug: orgSlug,
		DataSourceSlug:   backend.dataSourceSlug,
		Owner:            owner,
		Name:             name,
		DefaultBranch:    backend.trackedBranch,
		PipelineDir:      backend.pipelineDir,
	})
	if err != nil {
		return nil, xerror.Wrapf("bootstrap project pipeline repository: %w", err)
	}
	bootstrapOp := pipelinegit.RepositoryBootstrapOperation{
		OrganizationSlug: orgSlug,
		DataSourceSlug:   backend.dataSourceSlug,
		Owner:            owner,
		Name:             name,
	}
	var repositoryID int64
	if created != nil {
		bootstrapOp.RepositoryID = created.RepositoryID
		repositoryID, _ = strconv.ParseInt(created.RepositoryID, 10, 64)
	}
	cleanupBootstrap := func(cause error) error {
		if repositoryID <= 0 {
			return xerror.Join(cause, gitprovider.ErrRepositoryCleanupFailed.Detail("provider returned no valid created repository ID; automatic cleanup was not attempted"))
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if cleanupErr := s.repositoryManager.DeleteRepository(cleanupCtx, bootstrapOp); cleanupErr != nil {
			return xerror.Join(cause, gitprovider.ErrRepositoryCleanupFailed.With(cleanupErr))
		}
		return cause
	}
	if created == nil || repositoryID <= 0 || !strings.EqualFold(repostore.NormalizeRemoteURL(created.RemoteURL), backend.repo) ||
		created.FirstCommitSHA != strings.TrimSpace(created.FirstCommitSHA) || created.FirstCommitSHA == "" {
		return nil, cleanupBootstrap(xerror.Unexpected().Detail("provider returned inconsistent repository provenance"))
	}
	backend.repo = repostore.NormalizeRemoteURL(created.RemoteURL)
	backend.firstCommitSHA = &created.FirstCommitSHA
	backend.createdRepoURL = created.RemoteURL
	project, err := s.adoptProjectPipelineBackend(ctx, orgSlug, projectSlug, backend)
	if err == nil {
		return project, nil
	}
	return nil, cleanupBootstrap(err)
}

func (s *service) reconcileAdoptedProjectPipelineBackend(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	backend resolvedProjectPipelineBackend,
	repoStore repostore.RepoStore,
	lastAppliedCommit *string,
) error {
	// Keep the backend, authority, and imported specs atomic so failed attachment cannot grant publishing access.
	return s.txm.RunInTx(ctx, func(ctx context.Context, _ postgres.DBTX) error {
		if err := s.persistProjectPipelineBackendIdentity(ctx, orgSlug, projectSlug, backend); err != nil {
			return err
		}
		result, err := s.pipelineStore.ReconcileFromGit(ctx, orgSlug, projectSlug, repoStore, pipelinesvc.ReconcileFromGitParams{
			PipelineDir: backend.pipelineDir, SourceRef: branchRefPtr(backend.trackedBranch), LastAppliedCommit: lastAppliedCommit,
		})
		if err != nil {
			return err
		}
		return projectPipelineBackendReconcileFailure(result)
	})
}

func projectPipelineBackendReconcileFailure(result *pipelinesvc.ReconcileFromGitResult) error {
	if result == nil || result.Failed == 0 {
		return nil
	}

	detail := fmt.Sprintf(
		"project pipeline backend reconcile failed with %d file failure(s)",
		result.Failed,
	)
	if len(result.Failures) > 0 {
		failure := result.Failures[0]
		detail = fmt.Sprintf(
			"%s; first failure path=%q stage=%q: %s",
			detail,
			failure.PipelinePath,
			failure.Stage,
			failure.Message,
		)
	}

	return ErrPipelineBackendReconcileFailed.Detail(detail)
}

func (s *service) enqueueProjectRemoteCleanup(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug slug.Slug,
) error {
	project, err := s.projectsRepo.FindBySlugForUpdate(ctx, tx, orgSlug, projectSlug)
	if err != nil {
		return xerror.Wrapf("lock project backend for remote cleanup: %w", err)
	}
	if project.PipelineBackendDataSourceSlug == nil || project.PipelineBackendTrackedBranch == nil {
		return nil
	}
	if s.pipelineGitProducer == nil {
		return xerror.ExecutionUnavailable().Detail("pipeline git producer is required for remote cleanup")
	}

	specs, err := s.pipelineStore.List(ctx, orgSlug, projectSlug, model.ListRequest{})
	if err != nil {
		return xerror.Wrapf("list project pipelines for remote cleanup: %w", err)
	}

	branch := strings.TrimSpace(*project.PipelineBackendTrackedBranch)
	for _, spec := range specs {
		if spec.SourcePath == nil {
			continue
		}

		path := strings.TrimSpace(*spec.SourcePath)
		if path == "" {
			continue
		}

		if _, err := s.pipelineGitProducer.EnqueueDeleteFile(
			ctx,
			tx,
			orgSlug,
			*project.PipelineBackendDataSourceSlug,
			spec.Slug,
			&branch,
			path,
			"delete pipeline "+spec.Slug.String(),
		); err != nil {
			return xerror.Wrapf("enqueue project pipeline remote cleanup %s: %w", spec.Slug, err)
		}
	}

	return nil
}

func (s *service) persistProjectPipelineBackendIdentity(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	backend resolvedProjectPipelineBackend,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if backend.requireUnconfigured {
			current, err := s.projectsRepo.FindBySlugForUpdate(ctx, tx, orgSlug, projectSlug)
			if err != nil {
				return err
			}
			if current.PipelineBackendDataSourceSlug != nil {
				return xerror.AlreadyExists(xerror.Project).Detail("project pipeline backend was configured during repository setup")
			}
		}
		if err := s.projectsRepo.SetPipelineBackend(ctx, tx, &projects.SetPipelineBackendParams{
			OrgSlug:                       orgSlug,
			Slug:                          projectSlug,
			PipelineBackendDataSourceSlug: backend.dataSourceSlug,
			PipelineBackendRepo:           backend.repo,
			PipelineBackendTrackedBranch:  backend.trackedBranch,
			PipelineBackendPipelineDir:    backend.pipelineDir,
			PipelineBackendFirstCommitSHA: backend.firstCommitSHA,
		}); err != nil {
			switch {
			case xerror.Is(err, projects.ErrPipelineBackendDataSourceUnavailable):
				return ErrPipelineBackendDataSourceUnavailable
			case xerror.Is(err, projects.ErrPipelineBackendTaken):
				return ErrPipelineBackendTaken.WithPublicDetail("This repository and tracked branch are already connected to another project. Choose another repository or branch; changing the pipeline path does not create a separate backend.")
			default:
				return xerror.Wrapf("set project pipeline backend: %w", err)
			}
		}

		var authority model.PipelineProposalAuthority
		var credentialSlug slug.Slug
		if backend.proposalAuthority != nil {
			authority = backend.proposalAuthority.Authority
			credentialSlug = backend.proposalAuthority.CredentialSlug
		}
		binding := projects.BindProposalAuthorityParams{
			OrgSlug: orgSlug, ProjectSlug: projectSlug,
			DataSourceSlug: backend.dataSourceSlug, CredentialSlug: credentialSlug, Authority: authority,
		}
		if err := s.projectsRepo.RevokeChangedProposalAuthority(ctx, tx, binding); err != nil {
			return err
		}
		if backend.proposalAuthority != nil {
			if err := s.projectsRepo.BindGitHubProposalAuthority(ctx, tx, binding); err != nil {
				return err
			}
		} else if backend.provider == model.DataSourceTypeGit {
			if err := s.projectsRepo.BindGitBranchProposalAuthority(ctx, tx, binding); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *service) resolvePipelineBackend(
	ctx context.Context,
	orgSlug, dataSourceSlug slug.Slug,
	trackedBranch, pipelineDir string,
) (resolvedProjectPipelineBackend, error) {
	ds, err := s.dataSourcesService.FindBySlug(ctx, orgSlug, dataSourceSlug)
	if err != nil {
		return resolvedProjectPipelineBackend{}, xerror.Wrapf(
			"find project pipeline backend data source: %w",
			err,
		)
	}
	if ds == nil || !ds.IsActive {
		return resolvedProjectPipelineBackend{}, ErrPipelineBackendDataSourceUnavailable
	}
	if ds.ReadOnly {
		return resolvedProjectPipelineBackend{}, ErrPipelineBackendReadOnly.Detail(
			"project pipeline backend data source must be write-capable",
		)
	}
	if ds.Type != model.DataSourceTypeGit &&
		ds.Type != model.DataSourceTypeGithub &&
		ds.Type != model.DataSourceTypeGitlab {
		return resolvedProjectPipelineBackend{}, ErrPipelineBackendDataSourceTypeNotAllowed.Detail(
			"project pipeline backend data source must be git, github, or gitlab",
		)
	}

	var cfg gitconnector.Config
	var providerConfig gitconnector.ProviderConfig
	if err := json.Unmarshal(ds.Config, &cfg); err != nil {
		return resolvedProjectPipelineBackend{}, xerror.Wrapf(
			"decode project pipeline backend config: %w",
			err,
		)
	}
	if ds.Type != model.DataSourceTypeGit {
		if err := json.Unmarshal(ds.Config, &providerConfig); err != nil {
			return resolvedProjectPipelineBackend{}, xerror.Wrapf("decode managed pipeline backend config: %w", err)
		}
		cfg.RepoURL = providerConfig.Repository.WebURL
	}
	if strings.TrimSpace(cfg.RepoURL) == "" {
		return resolvedProjectPipelineBackend{}, ErrPipelineBackendDataSourceUnavailable.Detail("pipeline backend repository is required")
	}

	var proposalAuthority *datasourcessvc.GitHubProposalAuthority
	if ds.Type == model.DataSourceTypeGithub {
		if providerConfig.Repository.ID != "" {
			auth, err := s.dataSourcesService.ResolveGitExecutionAuth(ctx, orgSlug, dataSourceSlug)
			if err != nil {
				return resolvedProjectPipelineBackend{}, err
			}
			if auth == nil || auth.GitHubProposalAuthority == nil {
				return resolvedProjectPipelineBackend{}, pipelinesvc.ErrPipelineProposalAuthorityDenied
			}
			proposalAuthority = auth.GitHubProposalAuthority
			if err := proposalAuthority.Authority.ValidateForPersistence(); err != nil {
				return resolvedProjectPipelineBackend{}, err
			}
		}
	}
	return resolvedProjectPipelineBackend{
		proposalAuthority: proposalAuthority,
		dataSourceSlug:    dataSourceSlug,
		provider:          ds.Type,
		repo:              repostore.NormalizeRemoteURL(cfg.RepoURL),
		trackedBranch:     trackedBranch,
		pipelineDir:       pipelineDir,
	}, nil
}

func (s *service) openProjectBackendRepo(
	ctx context.Context,
	orgSlug slug.Slug,
	backend resolvedProjectPipelineBackend,
) (
	repostore.RepoStore,
	*string,
	func(),
	error,
) {
	if s.remoteGitExecutor != nil {
		return s.openRemoteProjectBackendRepo(ctx, orgSlug, backend)
	}
	auth, err := s.dataSourcesService.ResolveGitAuth(ctx, orgSlug, backend.dataSourceSlug)
	if err != nil {
		return nil, nil, nil, xerror.Wrapf("resolve project pipeline backend auth: %w", err)
	}
	if err := validateProjectBackendAuthRepo(backend, auth); err != nil {
		if auth.Cleanup != nil {
			auth.Cleanup()
		}
		return nil, nil, nil, err
	}
	if backend.createdRepoURL != "" {
		auth.RepoURL = backend.createdRepoURL
	}

	remoteGit := repostore.NewRemoteGit()
	if err := remoteGit.CheckRemote(ctx, auth.RepoURL, auth.Env); err != nil {
		if auth.Cleanup != nil {
			auth.Cleanup()
		}
		return nil, nil, nil, xerror.Wrapf("check project pipeline backend remote: %w", err)
	}

	repoStore, repoRoot, cloneCleanup, err := remoteGit.CloneRemote(
		ctx,
		auth.RepoURL,
		backend.trackedBranch,
		auth.Env,
	)
	if err != nil {
		if auth.Cleanup != nil {
			auth.Cleanup()
		}
		return nil, nil, nil, xerror.Wrapf("clone project pipeline backend remote: %w", err)
	}
	metadata := repostore.ResolveGitMetadata(ctx, repoRoot)
	return repoStore, metadata.LastCommit, combineCleanup(auth.Cleanup, cloneCleanup), nil
}

func (s *service) openRemoteProjectBackendRepo(
	ctx context.Context,
	orgSlug slug.Slug,
	backend resolvedProjectPipelineBackend,
) (repostore.RepoStore, *string, func(), error) {
	if s.remoteGitMaxBytes <= 0 {
		return nil, nil, nil, xerror.ExecutionUnavailable().Detail(
			"project pipeline backend snapshot byte limit is required",
		)
	}
	auth, err := s.dataSourcesService.ResolveGitExecutionAuth(ctx, orgSlug, backend.dataSourceSlug)
	if err != nil {
		return nil, nil, nil, xerror.Wrapf("resolve project pipeline backend execution auth: %w", err)
	}
	if auth == nil {
		return nil, nil, nil, ErrPipelineBackendDataSourceUnavailable
	}
	var cfg gitconnector.Config
	if err := json.Unmarshal(auth.ConfigJSON, &cfg); err != nil {
		return nil, nil, nil, xerror.InvalidInput(xerror.DataSource).Detail("Git connection config is invalid")
	}
	if err := validateProjectBackendAuthRepo(backend, &datasourcessvc.GitDataSourceAuth{RepoURL: cfg.RepoURL}); err != nil {
		return nil, nil, nil, err
	}
	if backend.createdRepoURL != "" {
		auth.ConfigJSON, err = gitExecutionConfigForRepository(auth.ConfigJSON, backend.createdRepoURL)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	snapshot, err := s.remoteGitExecutor.ExecuteGitSourceSnapshot(ctx, pipelinegit.RemoteSourceRequest{
		OrganizationSlug: orgSlug,
		DataSourceSlug:   backend.dataSourceSlug,
		Auth:             *auth,
		Branch:           backend.trackedBranch,
		MaxBytes:         s.remoteGitMaxBytes,
		MaxFiles:         pipelinegit.MaxRemoteSourceSnapshotFiles,
	})
	if err != nil {
		return nil, nil, nil, xerror.Wrapf("snapshot project pipeline backend remote: %w", err)
	}
	if snapshot == nil || snapshot.SourceRepo == nil ||
		repostore.NormalizeRemoteURL(*snapshot.SourceRepo) != backend.repo {
		return nil, nil, nil, ErrPipelineBackendDataSourceChanged.Detail(
			"project pipeline backend snapshot source does not match the selected data source",
		)
	}
	return pipelinegit.NewSnapshotRepoStore(snapshot.Files), snapshot.LastAppliedCommit, func() {}, nil
}

func validateProjectBackendAuthRepo(
	backend resolvedProjectPipelineBackend,
	auth *datasourcessvc.GitDataSourceAuth,
) error {
	if auth == nil {
		return ErrPipelineBackendDataSourceUnavailable.Detail(
			"project pipeline backend data source auth is required",
		)
	}

	actualRepo := repostore.NormalizeRemoteURL(auth.RepoURL)
	if actualRepo == backend.repo || backend.createdRepoURL != "" {
		return nil
	}

	return ErrPipelineBackendDataSourceChanged.Detailf(
		"project pipeline backend data source repo changed during attach: expected %q, got %q",
		backend.repo,
		actualRepo,
	)
}

func gitExecutionConfigForRepository(configJSON []byte, repo string) ([]byte, error) {
	var config map[string]any
	if err := json.Unmarshal(configJSON, &config); err != nil {
		return nil, xerror.InvalidInput(xerror.DataSource).Detail("GitHub connection config is invalid")
	}
	config["repo_url"] = repo
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, xerror.Wrapf("encode GitHub repository execution config: %w", err)
	}
	return encoded, nil
}

func validateTrackedBranch(value string) (string, error) {
	branch := strings.TrimSpace(value)
	if branch == "" {
		return "", ErrPipelineBackendTrackedBranchInvalid.Detail("tracked_branch is required")
	}
	if strings.HasPrefix(branch, "refs/") {
		return "", ErrPipelineBackendTrackedBranchInvalid.Detail(
			"tracked_branch must be a branch name, not a full ref",
		)
	}
	if commitLikeBranchPattern.MatchString(branch) {
		return "", ErrPipelineBackendTrackedBranchInvalid.Detail(
			"tracked_branch must be a branch name, not a commit SHA",
		)
	}
	if len(branch) > 255 || branch == "@" || strings.HasPrefix(branch, "-") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, "~^:?*[\\") || strings.IndexFunc(branch, func(r rune) bool { return r <= 32 || r == 127 }) >= 0 {
		return "", ErrPipelineBackendTrackedBranchInvalid.Detail("tracked_branch must be a valid Git branch name")
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return "", ErrPipelineBackendTrackedBranchInvalid.Detail("tracked_branch must be a valid Git branch name")
		}
	}
	return branch, nil
}

func validatePipelineDir(value string) (string, error) {
	dir := strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	cleaned := path.Clean(dir)
	if dir == "" || cleaned == "." || strings.HasPrefix(cleaned, "/") ||
		cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", xerror.InvalidInput(xerror.Project).Detail(
			"pipeline_dir must be a repository-relative directory",
		)
	}
	if len(cleaned) > 1024 || strings.IndexFunc(cleaned, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
		return "", xerror.InvalidInput(xerror.Project).Detail("pipeline_dir must be a valid repository-relative directory")
	}
	for _, part := range strings.Split(cleaned, "/") {
		if strings.EqualFold(part, ".git") {
			return "", xerror.InvalidInput(xerror.Project).Detail("pipeline_dir cannot use the Git metadata directory")
		}
	}
	return cleaned, nil
}

func branchRefPtr(branch string) *string {
	if strings.TrimSpace(branch) == "" {
		return nil
	}
	ref := "refs/heads/" + strings.TrimSpace(branch)
	return &ref
}

func combineCleanup(cleanups ...func()) func() {
	return func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			if cleanups[i] != nil {
				cleanups[i]()
			}
		}
	}
}
