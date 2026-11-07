package pipelinegit

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
)

type RemoteWriteRequest struct {
	Auth      datasourcessvc.GitExecutionAuth `json:"auth"`
	Operation WriteFileOperation              `json:"operation"`
}

type RemoteDeleteRequest struct {
	Auth      datasourcessvc.GitExecutionAuth `json:"auth"`
	Operation DeleteFileOperation             `json:"operation"`
}

type RemoteProposalRequest struct {
	Auth      datasourcessvc.GitExecutionAuth `json:"auth"`
	Operation ProposalOperation               `json:"operation"`
}

type RemoteProposalRefreshRequest struct {
	Auth      datasourcessvc.GitExecutionAuth `json:"auth"`
	Operation ProposalRefreshOperation        `json:"operation"`
}

type RemoteRepositoryRequest struct {
	Auth      datasourcessvc.GitExecutionAuth `json:"auth"`
	Action    string                          `json:"action"`
	Operation RepositoryBootstrapOperation    `json:"operation"`
}

const (
	RepositoryActionValidate  = "validate"
	RepositoryActionBootstrap = "bootstrap"
	RepositoryActionDelete    = "delete"
)

type RemoteRepositoryExecutor interface {
	ExecuteGitRepository(context.Context, RemoteRepositoryRequest) (*RepositoryBootstrapResult, error)
}

// RemoteExecutor crosses the filesystem and outbound-network execution boundary.
type RemoteExecutor interface {
	ExecuteGitWrite(context.Context, RemoteWriteRequest) error
	ExecuteGitDelete(context.Context, RemoteDeleteRequest) error
	ExecuteGitProposal(context.Context, RemoteProposalRequest) (*ProposalResult, error)
	ExecuteGitProposalRefresh(context.Context, RemoteProposalRefreshRequest) (*ProposalResult, error)
}

type remoteFileExecutor struct {
	resolver GitExecutionAuthResolver
	executor RemoteExecutor
}

// GitExecutionAuthResolver decrypts only operation-scoped inputs on control workers.
type GitExecutionAuthResolver interface {
	ResolveGitExecutionAuth(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitExecutionAuth, error)
}

func NewRemoteFileExecutor(resolver GitExecutionAuthResolver, executor RemoteExecutor) (FileExecutor, error) {
	if resolver == nil || executor == nil {
		return nil, xerror.ExecutionUnavailable().Detail("remote git execution dependencies are required")
	}
	return &remoteFileExecutor{resolver: resolver, executor: executor}, nil
}

func (e *remoteFileExecutor) WriteFile(ctx context.Context, op WriteFileOperation) error {
	auth, err := e.resolve(ctx, op.OrganizationSlug, op.DataSourceSlug)
	if err != nil {
		return err
	}
	return e.executor.ExecuteGitWrite(ctx, RemoteWriteRequest{Auth: auth, Operation: op})
}

func (e *remoteFileExecutor) DeleteFile(ctx context.Context, op DeleteFileOperation) error {
	auth, err := e.resolve(ctx, op.OrganizationSlug, op.DataSourceSlug)
	if err != nil {
		return err
	}
	return e.executor.ExecuteGitDelete(ctx, RemoteDeleteRequest{Auth: auth, Operation: op})
}

func (e *remoteFileExecutor) resolve(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (datasourcessvc.GitExecutionAuth, error) {
	auth, err := e.resolver.ResolveGitExecutionAuth(ctx, orgSlug, dsSlug)
	if err != nil {
		return datasourcessvc.GitExecutionAuth{}, xerror.Wrapf("resolve git datasource auth: %w", err)
	}
	return *auth, nil
}

type remoteProposalExecutor struct {
	resolver GitExecutionAuthResolver
	executor RemoteExecutor
}

func NewRemoteProposalExecutor(
	resolver GitExecutionAuthResolver,
	executor RemoteExecutor,
) (ProposalExecutor, error) {
	if resolver == nil || executor == nil {
		return nil, xerror.ExecutionUnavailable().Detail("remote git execution dependencies are required")
	}
	return &remoteProposalExecutor{resolver: resolver, executor: executor}, nil
}

func (e *remoteProposalExecutor) PublishProposal(
	ctx context.Context,
	op ProposalOperation,
) (*ProposalResult, error) {
	auth, err := e.resolver.ResolveGitExecutionAuth(ctx, op.OrganizationSlug, op.DataSourceSlug)
	if err != nil {
		return nil, xerror.Wrapf("resolve git datasource auth: %w", err)
	}
	return e.executor.ExecuteGitProposal(ctx, RemoteProposalRequest{
		Auth:      *auth,
		Operation: op,
	})
}

func (e *remoteProposalExecutor) RefreshProposal(
	ctx context.Context,
	op ProposalRefreshOperation,
) (*ProposalResult, error) {
	auth, err := e.resolver.ResolveGitExecutionAuth(ctx, op.OrganizationSlug, op.DataSourceSlug)
	if err != nil {
		return nil, xerror.Wrapf("resolve git datasource auth: %w", err)
	}
	return e.executor.ExecuteGitProposalRefresh(ctx, RemoteProposalRefreshRequest{Auth: *auth, Operation: op})
}

type staticGitAuthResolver struct {
	auth datasourcessvc.GitDataSourceAuth
}

type remoteRepositoryManager struct {
	resolver GitExecutionAuthResolver
	executor RemoteRepositoryExecutor
}

func NewRemoteRepositoryManager(
	resolver GitExecutionAuthResolver,
	executor RemoteRepositoryExecutor,
) (RepositoryManager, error) {
	if resolver == nil || executor == nil {
		return nil, xerror.ExecutionUnavailable().Detail("remote repository dependencies are required")
	}
	return &remoteRepositoryManager{resolver: resolver, executor: executor}, nil
}

func (m *remoteRepositoryManager) ValidateRepository(ctx context.Context, orgSlug, dsSlug slug.Slug, branch string) error {
	_, err := m.execute(ctx, orgSlug, dsSlug, RepositoryActionValidate, RepositoryBootstrapOperation{DefaultBranch: branch})
	return err
}

func (m *remoteRepositoryManager) BootstrapRepository(
	ctx context.Context,
	op RepositoryBootstrapOperation,
) (*RepositoryBootstrapResult, error) {
	return m.execute(ctx, op.OrganizationSlug, op.DataSourceSlug, RepositoryActionBootstrap, op)
}

func (m *remoteRepositoryManager) DeleteRepository(ctx context.Context, op RepositoryBootstrapOperation) error {
	_, err := m.execute(ctx, op.OrganizationSlug, op.DataSourceSlug, RepositoryActionDelete, op)
	return err
}

func (m *remoteRepositoryManager) execute(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	action string,
	op RepositoryBootstrapOperation,
) (*RepositoryBootstrapResult, error) {
	auth, err := m.resolver.ResolveGitExecutionAuth(ctx, orgSlug, dsSlug)
	if err != nil {
		return nil, xerror.Wrapf("resolve repository execution auth: %w", err)
	}
	op.OrganizationSlug = orgSlug
	op.DataSourceSlug = dsSlug
	return m.executor.ExecuteGitRepository(ctx, RemoteRepositoryRequest{Auth: *auth, Action: action, Operation: op})
}

func (r staticGitAuthResolver) ResolveGitAuth(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*datasourcessvc.GitDataSourceAuth, error) {
	auth := r.auth
	auth.Cleanup = nil
	return &auth, nil
}

// ExecuteResolvedGitWrite runs one pre-resolved operation on an isolated host.
func ExecuteResolvedGitWrite(ctx context.Context, request RemoteWriteRequest) error {
	auth, err := datasourcessvc.PrepareGitExecutionAuth(ctx, request.Auth)
	if err != nil {
		return err
	}
	defer auth.Cleanup()
	executor, err := NewFileExecutor(staticGitAuthResolver{auth: *auth})
	if err != nil {
		return err
	}
	return executor.WriteFile(ctx, request.Operation)
}

// ExecuteResolvedGitDelete runs one pre-resolved operation on an isolated host.
func ExecuteResolvedGitDelete(ctx context.Context, request RemoteDeleteRequest) error {
	auth, err := datasourcessvc.PrepareGitExecutionAuth(ctx, request.Auth)
	if err != nil {
		return err
	}
	defer auth.Cleanup()
	executor, err := NewFileExecutor(staticGitAuthResolver{auth: *auth})
	if err != nil {
		return err
	}
	return executor.DeleteFile(ctx, request.Operation)
}

// ExecuteResolvedGitProposal runs one pre-resolved proposal operation on an isolated host.
func ExecuteResolvedGitProposal(
	ctx context.Context,
	request RemoteProposalRequest,
) (*ProposalResult, error) {
	auth, err := datasourcessvc.PrepareGitExecutionAuth(ctx, request.Auth)
	if err != nil {
		return nil, err
	}
	defer auth.Cleanup()
	executor, err := NewProposalExecutor(staticGitAuthResolver{auth: *auth})
	if err != nil {
		return nil, err
	}
	return executor.PublishProposal(ctx, request.Operation)
}

// ExecuteResolvedGitProposalRefresh reads one exact provider proposal on an isolated host.
func ExecuteResolvedGitProposalRefresh(
	ctx context.Context,
	request RemoteProposalRefreshRequest,
) (*ProposalResult, error) {
	auth, err := datasourcessvc.PrepareGitExecutionAuth(ctx, request.Auth)
	if err != nil {
		return nil, err
	}
	defer auth.Cleanup()
	executor, err := NewProposalExecutor(staticGitAuthResolver{auth: *auth})
	if err != nil {
		return nil, err
	}
	return executor.RefreshProposal(ctx, request.Operation)
}

func ExecuteResolvedGitRepository(
	ctx context.Context,
	request RemoteRepositoryRequest,
) (*RepositoryBootstrapResult, error) {
	auth, err := datasourcessvc.PrepareGitExecutionAuth(ctx, request.Auth)
	if err != nil {
		return nil, err
	}
	defer auth.Cleanup()
	manager, err := NewRepositoryManager(staticGitAuthResolver{auth: *auth})
	if err != nil {
		return nil, err
	}
	switch request.Action {
	case RepositoryActionValidate:
		return nil, manager.ValidateRepository(ctx, request.Operation.OrganizationSlug, request.Operation.DataSourceSlug, request.Operation.DefaultBranch)
	case RepositoryActionBootstrap:
		return manager.BootstrapRepository(ctx, request.Operation)
	case RepositoryActionDelete:
		return nil, manager.DeleteRepository(ctx, request.Operation)
	default:
		return nil, xerror.InvalidInput(xerror.RepoFile).Detail("repository action is invalid")
	}
}
