package pipelinegit

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

var ErrSourceRequired = xerror.New(
	xerror.KindInvalidInput,
	"PIPELINE_RECONCILE_SOURCE_REQUIRED",
	"pipeline reconcile source required",
)

const MaxRemoteSourceSnapshotFiles = 10_000

// ProjectBackendService resolves project-level pipeline backend configuration.
type ProjectBackendService interface {
	GetProject(
		ctx context.Context,
		orgSlug, projectSlug slug.Slug,
	) (*model.Project, error)
}

// DataSourceGitAuthResolver resolves git authentication for data sources.
type DataSourceGitAuthResolver interface {
	ResolveGitAuth(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
	) (*datasourcessvc.GitDataSourceAuth, error)
}

// SourceResolver resolves the git repository used by inbound pipeline reconciliation.
//
// This is intentionally limited to git-to-DB reconcile. Outbound DB-to-git side
// effects use the async pipeline git executor path.
type SourceResolver interface {
	Resolve(ctx context.Context, orgSlug slug.Slug, req SourceRequest) (*Source, error)
}

// SourceRequest selects the project whose configured pipeline backend should be used.
type SourceRequest struct {
	ProjectSlug slug.Slug
}

// Source is a resolved git source ready for pipeline reconciliation.
//
// Cleanup belongs to the current synchronous reconcile source lifecycle. Outbound
// worker-owned repository lifecycles do not leak to service callers.
type Source struct {
	RepoStore         repostore.RepoStore
	Cleanup           func()
	DataSourceSlug    slug.Slug
	SourceRepo        *string
	SourceRef         *string
	LastAppliedCommit *string
}

type resolver struct {
	projectBackend  ProjectBackendService
	gitAuthResolver DataSourceGitAuthResolver
	remoteGit       repostore.RemoteGit
}

type RemoteSourceRequest struct {
	OrganizationSlug slug.Slug                       `json:"organization_slug"`
	DataSourceSlug   slug.Slug                       `json:"data_source_slug"`
	Auth             datasourcessvc.GitExecutionAuth `json:"auth"`
	Branch           string                          `json:"branch"`
	MaxBytes         int64                           `json:"max_bytes"`
	MaxFiles         int                             `json:"max_files"`
}

type RemoteSourceSnapshot struct {
	Files             map[string][]byte `json:"files"`
	SourceRepo        *string           `json:"source_repo,omitempty"`
	SourceRef         *string           `json:"source_ref,omitempty"`
	LastAppliedCommit *string           `json:"last_applied_commit,omitempty"`
}

type RemoteSourceExecutor interface {
	ExecuteGitSourceSnapshot(context.Context, RemoteSourceRequest) (*RemoteSourceSnapshot, error)
}

type remoteSourceResolver struct {
	projectBackend  ProjectBackendService
	gitAuthResolver GitExecutionAuthResolver
	executor        RemoteSourceExecutor
	maxBytes        int64
	maxFiles        int
}

func NewRemoteSourceResolver(
	projectBackend ProjectBackendService,
	gitAuthResolver GitExecutionAuthResolver,
	executor RemoteSourceExecutor,
	maxBytes int64,
) (SourceResolver, error) {
	if projectBackend == nil || gitAuthResolver == nil || executor == nil || maxBytes <= 0 {
		return nil, xerror.ExecutionUnavailable().Detail("remote Git source dependencies are required")
	}
	return &remoteSourceResolver{
		projectBackend: projectBackend, gitAuthResolver: gitAuthResolver,
		executor: executor, maxBytes: maxBytes, maxFiles: MaxRemoteSourceSnapshotFiles,
	}, nil
}

func (r *remoteSourceResolver) Resolve(
	ctx context.Context,
	orgSlug slug.Slug,
	req SourceRequest,
) (*Source, error) {
	backend, err := resolveProjectBackend(ctx, r.projectBackend, orgSlug, req)
	if err != nil {
		return nil, err
	}
	auth, err := r.gitAuthResolver.ResolveGitExecutionAuth(ctx, orgSlug, backend.DataSourceSlug)
	if err != nil {
		return nil, xerror.Wrapf("resolve Git execution auth: %w", err)
	}
	snapshot, err := r.executor.ExecuteGitSourceSnapshot(ctx, RemoteSourceRequest{
		OrganizationSlug: orgSlug, DataSourceSlug: backend.DataSourceSlug,
		Auth: *auth, Branch: strings.TrimSpace(backend.TrackedBranch),
		MaxBytes: r.maxBytes, MaxFiles: r.maxFiles,
	})
	if err != nil {
		return nil, err
	}
	return &Source{
		RepoStore: NewSnapshotRepoStore(snapshot.Files), Cleanup: func() {},
		DataSourceSlug: backend.DataSourceSlug, SourceRepo: snapshot.SourceRepo,
		SourceRef: snapshot.SourceRef, LastAppliedCommit: snapshot.LastAppliedCommit,
	}, nil
}

// NewSourceResolver creates a production git source resolver.
func NewSourceResolver(
	projectBackend ProjectBackendService,
	gitAuthResolver DataSourceGitAuthResolver,
) (SourceResolver, error) {
	if projectBackend == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("project backend service is required"),
		)
	}
	if gitAuthResolver == nil {
		return nil, xerror.ExecutionUnavailable().With(
			xerror.Unexpected().Detail("git auth resolver is required"),
		)
	}

	return &resolver{
		projectBackend:  projectBackend,
		gitAuthResolver: gitAuthResolver,
		remoteGit:       repostore.NewRemoteGit(),
	}, nil
}

func (r *resolver) Resolve(
	ctx context.Context,
	orgSlug slug.Slug,
	req SourceRequest,
) (*Source, error) {
	backend, err := r.resolveProjectBackend(ctx, orgSlug, req)
	if err != nil {
		return nil, err
	}

	auth, err := r.gitAuthResolver.ResolveGitAuth(ctx, orgSlug, backend.DataSourceSlug)
	if err != nil {
		return nil, xerror.Wrapf("resolve git datasource auth: %w", err)
	}

	cleanup := auth.Cleanup
	branch := strings.TrimSpace(backend.TrackedBranch)

	if err := r.remoteGit.CheckRemote(ctx, auth.RepoURL, auth.Env); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, xerror.Wrapf("git datasource pre-flight check: %w", err)
	}

	repoStore, repoRoot, cloneCleanup, err := r.remoteGit.CloneRemote(
		ctx,
		auth.RepoURL,
		branch,
		auth.Env,
	)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, xerror.Wrapf("clone git datasource: %w", err)
	}

	cleanup = combineCleanup(cleanup, cloneCleanup)
	metadata := repostore.ResolveGitMetadata(ctx, repoRoot)
	sourceRef := metadata.SourceRef
	if branch != "" {
		ref := "refs/heads/" + branch
		sourceRef = &ref
	}

	normalizedRepo := repostore.NormalizeRemoteURL(auth.RepoURL)

	return &Source{
		RepoStore:         repoStore,
		Cleanup:           cleanup,
		DataSourceSlug:    backend.DataSourceSlug,
		SourceRepo:        &normalizedRepo,
		SourceRef:         sourceRef,
		LastAppliedCommit: metadata.LastCommit,
	}, nil
}

func (r *resolver) resolveProjectBackend(
	ctx context.Context,
	orgSlug slug.Slug,
	req SourceRequest,
) (*model.ProjectPipelineBackend, error) {
	return resolveProjectBackend(ctx, r.projectBackend, orgSlug, req)
}

func resolveProjectBackend(
	ctx context.Context,
	projectBackend ProjectBackendService,
	orgSlug slug.Slug,
	req SourceRequest,
) (*model.ProjectPipelineBackend, error) {
	project, err := projectBackend.GetProject(ctx, orgSlug, req.ProjectSlug)
	if err != nil {
		return nil, xerror.Wrapf("get project pipeline backend: %w", err)
	}
	if project == nil || project.PipelineBackend == nil {
		return nil, ErrSourceRequired.
			Detail("pipeline reconcile requires a project pipeline backend")
	}
	if strings.TrimSpace(project.PipelineBackend.TrackedBranch) == "" {
		return nil, ErrSourceRequired.
			Detail("pipeline reconcile requires a project pipeline backend tracked branch")
	}

	return project.PipelineBackend, nil
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
