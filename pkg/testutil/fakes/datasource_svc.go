package fakes

import (
	"context"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

var errFakeDataSourceServiceFnNotSet = xerror.Unexpected().Detail(
	"fake data source service function is not set",
)

// DataSourceService is a test double for datasourcessvc.Service.
type DataSourceService struct {
	CreateProvisionedFn              func(context.Context, slug.Slug, model.EventActor, *datasourcessvc.CreateParams, datasourcessvc.SourceProvisioner) (*datasourcessvc.DataSource, error)
	ConnectHTTPOAuthFn               func(context.Context, slug.Slug, slug.Slug, model.EventActor) (*datasourcessvc.HTTPOAuthConnection, error)
	CreateFn                         func(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, params *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error)
	StartProviderDataSourceInstallFn func(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, provider string, params *datasourcessvc.StartProviderDataSourceInstallParams) (*datasourcessvc.ProviderDataSourceInstall, error)
	TestConnectionFn                 func(ctx context.Context, orgSlug slug.Slug, params *datasourcessvc.TestConnectionParams) error
	TestExistingConnectionFn         func(ctx context.Context, orgSlug, dsSlug slug.Slug) error
	FindBySlugFn                     func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error)
	ListByOrgFn                      func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest) (model.ListResponse[datasourcessvc.DataSource], error)
	UpdateFn                         func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, params *datasourcessvc.UpdateParams) (*datasourcessvc.DataSource, error)
	UpdateCredentialsFn              func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, params *datasourcessvc.UpdateCredentialsParams) (*datasourcessvc.DataSource, error)
	CompleteProviderOAuthCallbackFn  func(ctx context.Context, params credentialssvc.CompleteOAuthCallbackParams) (credentialssvc.CompleteOAuthCallbackResult, error)
	ValidateCredentialLifecycleFn    func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, action model.EventAction) (*datasourcessvc.DataSource, error)
	RecordCredentialLifecycleEventFn func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, action model.EventAction) error
	ListBranchesFn                   func(ctx context.Context, orgSlug, dsSlug slug.Slug) ([]datasourcessvc.DataSourceBranch, error)
	IntrospectSchemaFn               func(ctx context.Context, orgSlug, dsSlug slug.Slug) (datasourcessvc.SchemaResult, error)
	RefreshSchemaFn                  func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor) (*datasourcessvc.SchemaImportResult, error)
	DeleteFn                         func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor) error
	RestoreFn                        func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor) error
	ResolveGitAuthFn                 func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.GitDataSourceAuth, error)
	ResolveGitExecutionAuthFn        func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.GitExecutionAuth, error)
}

// StartProviderDataSourceInstall starts a provider data source installation through the fake service.
func (f *DataSourceService) StartProviderDataSourceInstall(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	provider string,
	params *datasourcessvc.StartProviderDataSourceInstallParams,
) (*datasourcessvc.ProviderDataSourceInstall, error) {
	if f.StartProviderDataSourceInstallFn != nil {
		return f.StartProviderDataSourceInstallFn(ctx, orgSlug, actor, provider, params)
	}
	return nil, errFakeDataSourceServiceFnNotSet
}

func (f *DataSourceService) ListProviderRepositories(
	context.Context,
	slug.Slug,
	slug.Slug,
) ([]datasourcessvc.ProviderRepository, error) {
	return nil, errFakeDataSourceServiceFnNotSet
}

func (f *DataSourceService) SelectProviderRepository(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	datasourcessvc.SelectProviderRepositoryParams,
) (*datasourcessvc.DataSource, error) {
	return nil, errFakeDataSourceServiceFnNotSet
}

func (f *DataSourceService) Create(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	params *datasourcessvc.CreateParams,
) (*datasourcessvc.DataSource, error) {
	if f.CreateFn != nil {
		return f.CreateFn(ctx, orgSlug, actor, params)
	}
	return &datasourcessvc.DataSource{Slug: params.Slug}, nil
}

func (f *DataSourceService) TestConnection(
	ctx context.Context,
	orgSlug slug.Slug,
	params *datasourcessvc.TestConnectionParams,
) error {
	if f.TestConnectionFn != nil {
		return f.TestConnectionFn(ctx, orgSlug, params)
	}
	return nil
}

func (f *DataSourceService) GetSnowflakeOnboarding(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.SnowflakeOnboarding, error) {
	return nil, errFakeDataSourceServiceFnNotSet
}

func (f *DataSourceService) TestExistingConnection(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) error {
	if f.TestExistingConnectionFn != nil {
		return f.TestExistingConnectionFn(ctx, orgSlug, dsSlug)
	}
	return nil
}

func (f *DataSourceService) FindBySlug(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (*datasourcessvc.DataSource, error) {
	if f.FindBySlugFn != nil {
		return f.FindBySlugFn(ctx, orgSlug, dsSlug)
	}
	return &datasourcessvc.DataSource{Slug: dsSlug}, nil
}

func (f *DataSourceService) ListByOrg(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[datasourcessvc.DataSource], error) {
	if f.ListByOrgFn != nil {
		return f.ListByOrgFn(ctx, orgSlug, req)
	}
	return model.ListResponse[datasourcessvc.DataSource]{
		Items:      []datasourcessvc.DataSource{},
		HasMore:    false,
		NextCursor: nil,
	}, nil
}

func (f *DataSourceService) Update(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	params *datasourcessvc.UpdateParams,
) (*datasourcessvc.DataSource, error) {
	if f.UpdateFn != nil {
		return f.UpdateFn(ctx, orgSlug, dsSlug, actor, params)
	}
	return &datasourcessvc.DataSource{Slug: dsSlug}, nil
}

func (f *DataSourceService) UpdateCredentials(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	params *datasourcessvc.UpdateCredentialsParams,
) (*datasourcessvc.DataSource, error) {
	if f.UpdateCredentialsFn != nil {
		return f.UpdateCredentialsFn(ctx, orgSlug, dsSlug, actor, params)
	}
	return &datasourcessvc.DataSource{Slug: dsSlug}, nil
}

func (f *DataSourceService) CompleteProviderOAuthCallback(
	ctx context.Context,
	params credentialssvc.CompleteOAuthCallbackParams,
) (credentialssvc.CompleteOAuthCallbackResult, error) {
	if f.CompleteProviderOAuthCallbackFn != nil {
		return f.CompleteProviderOAuthCallbackFn(ctx, params)
	}
	return credentialssvc.CompleteOAuthCallbackResult{}, nil
}

func (f *DataSourceService) ValidateCredentialLifecycle(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	action model.EventAction,
) (*datasourcessvc.DataSource, error) {
	if f.ValidateCredentialLifecycleFn != nil {
		return f.ValidateCredentialLifecycleFn(ctx, orgSlug, dsSlug, actor, action)
	}
	return &datasourcessvc.DataSource{Slug: dsSlug}, nil
}

func (f *DataSourceService) RecordCredentialLifecycleEvent(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	action model.EventAction,
) error {
	if f.RecordCredentialLifecycleEventFn != nil {
		return f.RecordCredentialLifecycleEventFn(ctx, orgSlug, dsSlug, actor, action)
	}
	return nil
}

func (f *DataSourceService) ListBranches(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) ([]datasourcessvc.DataSourceBranch, error) {
	if f.ListBranchesFn != nil {
		return f.ListBranchesFn(ctx, orgSlug, dsSlug)
	}
	return []datasourcessvc.DataSourceBranch{}, nil
}

func (f *DataSourceService) IntrospectSchema(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (datasourcessvc.SchemaResult, error) {
	if f.IntrospectSchemaFn != nil {
		return f.IntrospectSchemaFn(ctx, orgSlug, dsSlug)
	}
	return datasourcessvc.Schema[datasourcessvc.SQLSchemaPayload]{
		SchemaTypeValue: datasourcessvc.SchemaTypeSQL,
		Type:            "postgres",
		Payload:         datasourcessvc.SQLSchemaPayload{Tables: []datasourcessvc.SchemaTable{}},
	}, nil
}

func (f *DataSourceService) RefreshSchema(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
) (*datasourcessvc.SchemaImportResult, error) {
	if f.RefreshSchemaFn != nil {
		return f.RefreshSchemaFn(ctx, orgSlug, dsSlug, actor)
	}
	return &datasourcessvc.SchemaImportResult{
		DataSourceSlug: dsSlug,
		Status:         model.DataSourceSchemaImportStatusImporting,
		Enqueued:       true,
		Job: &datasourcessvc.QueuedSchemaImportJob{
			Kind:  "data_source_schema_import",
			Queue: jobs.DataSourceSchemaImportQueue,
			State: "queued",
		},
	}, nil
}

func (f *DataSourceService) ExecuteQueuedSchemaImport(context.Context, jobs.DataSourceSchemaImportArgs) (*datasourcessvc.SchemaImportResult, error) {
	return &datasourcessvc.SchemaImportResult{Skipped: true}, nil
}

func (f *DataSourceService) Delete(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.DeleteFn != nil {
		return f.DeleteFn(ctx, orgSlug, dsSlug, actor)
	}
	return nil
}

func (f *DataSourceService) Restore(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.RestoreFn != nil {
		return f.RestoreFn(ctx, orgSlug, dsSlug, actor)
	}
	return nil
}

func (f *DataSourceService) ResolveGitAuth(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (*datasourcessvc.GitDataSourceAuth, error) {
	if f.ResolveGitAuthFn != nil {
		return f.ResolveGitAuthFn(ctx, orgSlug, dsSlug)
	}
	return &datasourcessvc.GitDataSourceAuth{Cleanup: func() {}}, nil
}

func (f *DataSourceService) ResolveGitExecutionAuth(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (*datasourcessvc.GitExecutionAuth, error) {
	if f.ResolveGitExecutionAuthFn != nil {
		return f.ResolveGitExecutionAuthFn(ctx, orgSlug, dsSlug)
	}
	return &datasourcessvc.GitExecutionAuth{}, nil
}

func (*DataSourceService) AttachOrganizationGitLabConnection(context.Context, slug.Slug, slug.Slug, model.EventActor) error {
	return errFakeDataSourceServiceFnNotSet
}

func (*DataSourceService) EnsureProofWebhook(context.Context, slug.Slug, slug.Slug) error {
	return errFakeDataSourceServiceFnNotSet
}

func (f *DataSourceService) ListGoogleWorkspaceFiles(context.Context, slug.Slug, slug.Slug, workspace.ListOptions) (*workspace.Discovery, error) {
	return nil, errFakeDataSourceServiceFnNotSet
}

func (f *DataSourceService) VerifyGoogleWorkspaceFiles(context.Context, slug.Slug, slug.Slug, model.EventActor, datasourcessvc.WorkspaceVerificationParams) (*workspace.Discovery, error) {
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (*DataSourceService) GetGoogleWorkspaceFile(context.Context, slug.Slug, slug.Slug, string) (*workspace.File, error) {
	return nil, errFakeDataSourceServiceFnNotSet
}

func (*DataSourceService) GetGoogleWorkspaceWorksheets(context.Context, slug.Slug, slug.Slug, string) (*workspace.Metadata, error) {
	return nil, errFakeDataSourceServiceFnNotSet
}

func (*DataSourceService) GetGoogleWorkspacePicker(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.WorkspacePicker, error) {
	return nil, errFakeDataSourceServiceFnNotSet
}

func (f *DataSourceService) ConnectHTTPOAuth(ctx context.Context, org, source slug.Slug, actor model.EventActor) (*datasourcessvc.HTTPOAuthConnection, error) {
	if f.ConnectHTTPOAuthFn == nil {
		return nil, xerror.Unexpected().Detail("unexpected ConnectHTTPOAuth call")
	}
	return f.ConnectHTTPOAuthFn(ctx, org, source, actor)
}

func (f *DataSourceService) CreateProvisioned(ctx context.Context, org slug.Slug, actor model.EventActor, params *datasourcessvc.CreateParams, provisioner datasourcessvc.SourceProvisioner) (*datasourcessvc.DataSource, error) {
	if f.CreateProvisionedFn == nil {
		return nil, errFakeDataSourceServiceFnNotSet
	}
	return f.CreateProvisionedFn(ctx, org, actor, params, provisioner)
}
