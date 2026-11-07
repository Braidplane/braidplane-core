package datasourcessvc

import (
	"context"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type noopService struct{}

type noopGitLabWebhookManager struct{}

func (noopService) AttachOrganizationGitLabConnection(context.Context, slug.Slug, slug.Slug, model.EventActor) error {
	return nil
}

func NewNoopGitLabWebhookManager() GitLabWebhookManager { return noopGitLabWebhookManager{} }

func (noopGitLabWebhookManager) Ensure(context.Context, slug.Slug, slug.Slug, []byte, []byte) error {
	return nil
}

var errNoopServiceUnavailable = xerror.Unavailable(xerror.DataSource).With(
	xerror.Unexpected().Detail("noop data source service is not available for this operation"),
)

// NewNoopService returns a Service implementation that intentionally performs no work.
// This is exported for testing purposes only. Do not use in production code.
func NewNoopService() Service {
	return noopService{}
}

func (noopService) Create(context.Context, slug.Slug, model.EventActor, *CreateParams) (*DataSource, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) StartProviderDataSourceInstall(
	context.Context,
	slug.Slug,
	model.EventActor,
	string,
	*StartProviderDataSourceInstallParams,
) (*ProviderDataSourceInstall, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) ListProviderRepositories(context.Context, slug.Slug, slug.Slug) ([]ProviderRepository, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) SelectProviderRepository(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	SelectProviderRepositoryParams,
) (*DataSource, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) TestConnection(context.Context, slug.Slug, *TestConnectionParams) error {
	return errNoopServiceUnavailable
}

func (noopService) TestExistingConnection(context.Context, slug.Slug, slug.Slug) error {
	return errNoopServiceUnavailable
}

func (noopService) FindBySlug(_ context.Context, _, dsSlug slug.Slug) (*DataSource, error) {
	// Return a default non-read-only DataSource so runners that look up a sink
	// data source for the ReadOnly flag can proceed without error.
	return &DataSource{Slug: dsSlug}, nil
}

func (noopService) ListByOrg(
	context.Context,
	slug.Slug,
	model.ListRequest,
) (model.ListResponse[DataSource], error) {
	return model.ListResponse[DataSource]{}, errNoopServiceUnavailable
}

func (noopService) Update(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	*UpdateParams,
) (*DataSource, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) UpdateCredentials(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	*UpdateCredentialsParams,
) (*DataSource, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) CompleteProviderOAuthCallback(
	context.Context,
	credentialssvc.CompleteOAuthCallbackParams,
) (credentialssvc.CompleteOAuthCallbackResult, error) {
	return credentialssvc.CompleteOAuthCallbackResult{}, errNoopServiceUnavailable
}

func (noopService) ValidateCredentialLifecycle(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	model.EventAction,
) (*DataSource, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) RecordCredentialLifecycleEvent(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	model.EventAction,
) error {
	return errNoopServiceUnavailable
}

func (noopService) ListBranches(context.Context, slug.Slug, slug.Slug) ([]DataSourceBranch, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) IntrospectSchema(context.Context, slug.Slug, slug.Slug) (SchemaResult, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) RefreshSchema(context.Context, slug.Slug, slug.Slug, model.EventActor) (*SchemaImportResult, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) ExecuteQueuedSchemaImport(context.Context, jobs.DataSourceSchemaImportArgs) (*SchemaImportResult, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) Delete(context.Context, slug.Slug, slug.Slug, model.EventActor) error {
	return errNoopServiceUnavailable
}

func (noopService) Restore(context.Context, slug.Slug, slug.Slug, model.EventActor) error {
	return errNoopServiceUnavailable
}

func (noopService) ResolveGitAuth(_ context.Context, _, _ slug.Slug) (*GitDataSourceAuth, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) ResolveGitExecutionAuth(
	context.Context,
	slug.Slug,
	slug.Slug,
) (*GitExecutionAuth, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) GetSnowflakeOnboarding(context.Context, slug.Slug, slug.Slug) (*SnowflakeOnboarding, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) EnsureProofWebhook(context.Context, slug.Slug, slug.Slug) error { return nil }

func (noopService) ListGoogleWorkspaceFiles(context.Context, slug.Slug, slug.Slug, workspace.ListOptions) (*workspace.Discovery, error) {
	return nil, errNoopServiceUnavailable
}

func (noopService) VerifyGoogleWorkspaceFiles(context.Context, slug.Slug, slug.Slug, model.EventActor, WorkspaceVerificationParams) (*workspace.Discovery, error) {
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (noopService) GetGoogleWorkspaceFile(context.Context, slug.Slug, slug.Slug, string) (*workspace.File, error) {
	return nil, ErrInvalidInput
}

func (noopService) GetGoogleWorkspaceWorksheets(context.Context, slug.Slug, slug.Slug, string) (*workspace.Metadata, error) {
	return nil, ErrInvalidInput
}

func (noopService) GetGoogleWorkspacePicker(context.Context, slug.Slug, slug.Slug) (*WorkspacePicker, error) {
	return nil, errNoopServiceUnavailable
}

func (f noopService) ConnectHTTPOAuth(ctx context.Context, org, source slug.Slug, actor model.EventActor) (*HTTPOAuthConnection, error) {
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (noopService) CreateProvisioned(context.Context, slug.Slug, model.EventActor, *CreateParams, SourceProvisioner) (*DataSource, error) {
	return nil, errNoopServiceUnavailable
}
