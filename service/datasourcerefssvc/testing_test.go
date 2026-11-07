package datasourcerefssvc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/eventtest"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/datasourcerefs"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type testServiceConfig struct {
	dataSourceRefs       map[string]*model.DataSourceRef
	bindings             map[string]*model.DataSourceBinding
	runtimeVariables     []model.RuntimeVariable
	concreteDataSources  map[string]*datasourcessvc.DataSource
	projectEnvEnabled    *bool
	protectedBindingEnvs []model.ProjectEnvironmentProtection
}

type failingEventRecorder struct {
	err error
}

func (f failingEventRecorder) CreateEvent(
	context.Context,
	eventssvc.CreateEventParams,
) (*model.Event, error) {
	return nil, f.err
}

const defaultTestPipelineSlug slug.Slug = "acme-users-sync"

func newTestService(t *testing.T, cfg testServiceConfig) Service {
	t.Helper()
	return New(
		testutil.FakeTransactionManager{},
		newFakeDataSourceRefsRepository(cfg),
		&fakeDataSourcesService{items: cfg.concreteDataSources},
		eventtest.NewProducer(nil),
	)
}

func newTestDataSourceRef(
	refSlug slug.Slug,
	dsType model.DataSourceType,
	capabilities ...model.DataSourceRefCapability,
) *model.DataSourceRef {
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	return &model.DataSourceRef{
		PipelineSlug:         defaultTestPipelineSlug,
		Slug:                 refSlug,
		Name:                 refSlug.String(),
		DataSourceType:       dsType,
		RequiredCapabilities: capabilities,
		Timestamps:           model.Timestamps{CreatedAt: now, UpdatedAt: now},
	}
}

func newPipelineWithLogicalRef(refSlug slug.Slug) *pipelinespec.Pipeline {
	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{Slug: "acme-users-sync", Name: "ACME Users Sync"},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: "fetch-users",
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: refSlug,
					OperationType:  "sql",
					OperationRef:   "list-users",
					OutputContract: &payload.Contract{Fields: []payload.ContractField{
						{Name: "id", Type: model.ContractFieldTypeValueInteger, Required: true},
					}},
				},
			},
		},
	}
}

type fakeDataSourceRefsRepository struct {
	refs                 map[string]*model.DataSourceRef
	bindings             map[string]*model.DataSourceBinding
	runtimeVariables     []model.RuntimeVariable
	concreteDataSources  map[string]*datasourcessvc.DataSource
	projectEnvEnabled    bool
	protectedBindingEnvs []model.ProjectEnvironmentProtection
}

func newFakeDataSourceRefsRepository(cfg testServiceConfig) *fakeDataSourceRefsRepository {
	enabled := true
	if cfg.projectEnvEnabled != nil {
		enabled = *cfg.projectEnvEnabled
	}
	return &fakeDataSourceRefsRepository{
		refs:                 cloneRefMap(cfg.dataSourceRefs),
		bindings:             cloneBindingMap(cfg.bindings),
		runtimeVariables:     append([]model.RuntimeVariable(nil), cfg.runtimeVariables...),
		concreteDataSources:  cloneConcreteDataSourceMap(cfg.concreteDataSources),
		projectEnvEnabled:    enabled,
		protectedBindingEnvs: append([]model.ProjectEnvironmentProtection(nil), cfg.protectedBindingEnvs...),
	}
}

func (r *fakeDataSourceRefsRepository) CreateDataSourceRef(
	_ context.Context,
	_ postgres.DBTX,
	_, _, pipelineSlug slug.Slug,
	params datasourcerefs.CreateDataSourceRefParams,
) (*datasourcerefs.DataSourceRef, error) {
	key := refKey(pipelineSlug, params.Slug)
	if _, ok := r.refs[key]; ok {
		return nil, datasourcerefs.ErrDataSourceRefSlugTaken
	}
	ref := &model.DataSourceRef{
		PipelineSlug:         pipelineSlug,
		Slug:                 params.Slug,
		Name:                 params.Name,
		DataSourceType:       params.DataSourceType,
		RequiredCapabilities: params.RequiredCapabilities,
		Timestamps: model.Timestamps{
			CreatedAt: time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
		},
	}
	r.refs[key] = ref
	return repoRefFromModel(ref), nil
}

func (r *fakeDataSourceRefsRepository) FindDataSourceRefBySlug(
	_ context.Context,
	_ postgres.DBTX,
	_, _, pipelineSlug, refSlug slug.Slug,
) (*datasourcerefs.DataSourceRef, error) {
	ref := r.refs[refKey(pipelineSlug, refSlug)]
	if ref == nil {
		return nil, datasourcerefs.ErrDataSourceRefNotFound
	}
	return repoRefFromModel(ref), nil
}

func (r *fakeDataSourceRefsRepository) ListDataSourceRefs(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	model.ListRequest,
) ([]*datasourcerefs.DataSourceRef, error) {
	items := make([]*datasourcerefs.DataSourceRef, 0, len(r.refs))
	for _, ref := range r.refs {
		items = append(items, repoRefFromModel(ref))
	}
	return items, nil
}

func (r *fakeDataSourceRefsRepository) ListDataSourceRefsByPipeline(
	_ context.Context,
	_ postgres.DBTX,
	_, _, pipelineSlug slug.Slug,
) ([]*datasourcerefs.DataSourceRef, error) {
	items := make([]*datasourcerefs.DataSourceRef, 0, len(r.refs))
	for _, ref := range r.refs {
		if ref.PipelineSlug == pipelineSlug {
			items = append(items, repoRefFromModel(ref))
		}
	}
	return items, nil
}

func (r *fakeDataSourceRefsRepository) UpdateDataSourceRef(
	_ context.Context,
	_ postgres.DBTX,
	_, _, pipelineSlug, refSlug slug.Slug,
	params datasourcerefs.UpdateDataSourceRefParams,
) (*datasourcerefs.DataSourceRef, error) {
	ref := r.refs[refKey(pipelineSlug, refSlug)]
	if ref == nil {
		return nil, datasourcerefs.ErrDataSourceRefNotFound
	}
	ref.Name = params.Name
	ref.DataSourceType = params.DataSourceType
	ref.RequiredCapabilities = params.RequiredCapabilities
	ref.UpdatedAt = time.Date(2026, 5, 26, 11, 0, 0, 0, time.UTC)
	return repoRefFromModel(ref), nil
}

func (r *fakeDataSourceRefsRepository) DeleteDataSourceRef(
	_ context.Context,
	_ postgres.DBTX,
	_, _, pipelineSlug, refSlug slug.Slug,
) error {
	if r.refs[refKey(pipelineSlug, refSlug)] == nil {
		return datasourcerefs.ErrDataSourceRefNotFound
	}
	delete(r.refs, refKey(pipelineSlug, refSlug))
	return nil
}

func (r *fakeDataSourceRefsRepository) DeleteDataSourceRefsForPipeline(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) error {
	return nil
}

func (r *fakeDataSourceRefsRepository) ListProtectedBindingEnvironmentsForDataSourceRef(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) ([]model.ProjectEnvironmentProtection, error) {
	return append([]model.ProjectEnvironmentProtection(nil), r.protectedBindingEnvs...), nil
}

func (r *fakeDataSourceRefsRepository) ProjectEnvironmentEnabled(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) (bool, error) {
	return r.projectEnvEnabled, nil
}

func (r *fakeDataSourceRefsRepository) FindProjectEnvironmentProtection(
	_ context.Context,
	_ postgres.DBTX,
	_, _, environmentSlug slug.Slug,
) (*model.ProjectEnvironmentProtection, error) {
	if !r.projectEnvEnabled {
		return nil, datasourcerefs.ErrProjectEnvironmentNotEnabled
	}
	return &model.ProjectEnvironmentProtection{EnvironmentSlug: environmentSlug}, nil
}

func (r *fakeDataSourceRefsRepository) UpsertEnvironmentBinding(
	_ context.Context,
	_ postgres.DBTX,
	_, _, pipelineSlug, environmentSlug, refSlug slug.Slug,
	params datasourcerefs.UpsertEnvironmentBindingParams,
) (*datasourcerefs.DataSourceBinding, error) {
	ref := r.refs[refKey(pipelineSlug, refSlug)]
	if ref == nil {
		return nil, datasourcerefs.ErrDataSourceRefNotFound
	}
	dataSourceName := params.DataSourceSlug.String()
	dataSourceType := ref.DataSourceType
	dataSourceActive := true
	dataSourceReadOnly := false
	if target := r.concreteDataSources[params.DataSourceSlug.String()]; target != nil {
		dataSourceName = target.Name
		dataSourceType = target.Type
		dataSourceActive = target.IsActive
		dataSourceReadOnly = target.ReadOnly
	}
	binding := &model.DataSourceBinding{
		PipelineSlug:         pipelineSlug,
		DataSourceRefSlug:    refSlug,
		DataSourceRefName:    ref.Name,
		EnvironmentSlug:      environmentSlug,
		DataSourceSlug:       params.DataSourceSlug,
		DataSourceName:       dataSourceName,
		DataSourceType:       dataSourceType,
		DataSourceActive:     dataSourceActive,
		DataSourceReadOnly:   dataSourceReadOnly,
		RequiredCapabilities: ref.RequiredCapabilities,
		BoundAt:              time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
		UpdatedAt:            time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC),
	}
	r.bindings[scopedBindingKey(pipelineSlug, environmentSlug, refSlug)] = binding
	return repoBindingFromModel(binding), nil
}

func (r *fakeDataSourceRefsRepository) FindEnvironmentBinding(
	_ context.Context,
	_ postgres.DBTX,
	_, _, pipelineSlug, environmentSlug, refSlug slug.Slug,
) (*datasourcerefs.DataSourceBinding, error) {
	binding := r.bindings[scopedBindingKey(pipelineSlug, environmentSlug, refSlug)]
	if binding == nil {
		return nil, datasourcerefs.ErrDataSourceBindingNotFound
	}
	return repoBindingFromModel(binding), nil
}

func (r *fakeDataSourceRefsRepository) ListEnvironmentBindings(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	model.ListRequest,
) ([]*datasourcerefs.DataSourceBinding, error) {
	items := make([]*datasourcerefs.DataSourceBinding, 0, len(r.bindings))
	for _, binding := range r.bindings {
		items = append(items, repoBindingFromModel(binding))
	}
	return items, nil
}

func (r *fakeDataSourceRefsRepository) ListEnvironmentBindingsForDataSourceRef(
	_ context.Context,
	_ postgres.DBTX,
	_, _, pipelineSlug, refSlug slug.Slug,
) ([]*datasourcerefs.DataSourceBinding, error) {
	items := make([]*datasourcerefs.DataSourceBinding, 0, len(r.bindings))
	for _, binding := range r.bindings {
		if binding.PipelineSlug == pipelineSlug && binding.DataSourceRefSlug == refSlug {
			items = append(items, repoBindingFromModel(binding))
		}
	}
	return items, nil
}

func (r *fakeDataSourceRefsRepository) ResolveEnvironmentBinding(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug slug.Slug,
) (*datasourcerefs.DataSourceBinding, error) {
	if !r.projectEnvEnabled {
		return nil, datasourcerefs.ErrProjectEnvironmentNotEnabled
	}
	return r.FindEnvironmentBinding(ctx, tx, orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug)
}

func (r *fakeDataSourceRefsRepository) ListRuntimeVariables(
	context.Context,
	postgres.DBTX,
	slug.Slug,
	slug.Slug,
	slug.Slug,
) ([]model.RuntimeVariable, error) {
	return append([]model.RuntimeVariable(nil), r.runtimeVariables...), nil
}

func (r *fakeDataSourceRefsRepository) DeleteEnvironmentBinding(
	_ context.Context,
	_ postgres.DBTX,
	_, _, pipelineSlug, environmentSlug, refSlug slug.Slug,
) error {
	key := scopedBindingKey(pipelineSlug, environmentSlug, refSlug)
	if r.bindings[key] == nil {
		return datasourcerefs.ErrDataSourceBindingNotFound
	}
	delete(r.bindings, key)
	return nil
}

type fakeDataSourcesService struct {
	ConnectHTTPOAuthFn func(context.Context, slug.Slug, slug.Slug, model.EventActor) (*datasourcessvc.HTTPOAuthConnection, error)
	items              map[string]*datasourcessvc.DataSource
	createCalls        int
}

func (s *fakeDataSourcesService) FindBySlug(_ context.Context, _, dsSlug slug.Slug) (*datasourcessvc.DataSource, error) {
	item := s.items[dsSlug.String()]
	if item == nil {
		return nil, datasourcessvc.ErrDataSourceNotFound
	}
	return item, nil
}

func (s *fakeDataSourcesService) CreateProvisioned(context.Context, slug.Slug, model.EventActor, *datasourcessvc.CreateParams, datasourcessvc.SourceProvisioner) (*datasourcessvc.DataSource, error) {
	return nil, fmt.Errorf("unexpected call to fakeDataSourcesService.CreateProvisioned")
}

func (s *fakeDataSourcesService) Create(context.Context, slug.Slug, model.EventActor, *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error) {
	s.createCalls++
	return nil, datasourcessvc.ErrDataSourceNotFound
}

func (s *fakeDataSourcesService) StartProviderDataSourceInstall(
	context.Context,
	slug.Slug,
	model.EventActor,
	string,
	*datasourcessvc.StartProviderDataSourceInstallParams,
) (*datasourcessvc.ProviderDataSourceInstall, error) {
	return nil, fmt.Errorf("unexpected call to fakeDataSourcesService.StartProviderDataSourceInstall")
}

func (s *fakeDataSourcesService) ListProviderRepositories(
	context.Context,
	slug.Slug,
	slug.Slug,
) ([]datasourcessvc.ProviderRepository, error) {
	return nil, fmt.Errorf("unexpected call to fakeDataSourcesService.ListProviderRepositories")
}

func (s *fakeDataSourcesService) SelectProviderRepository(
	context.Context,
	slug.Slug,
	slug.Slug,
	model.EventActor,
	datasourcessvc.SelectProviderRepositoryParams,
) (*datasourcessvc.DataSource, error) {
	return nil, fmt.Errorf("unexpected call to fakeDataSourcesService.SelectProviderRepository")
}

func (s *fakeDataSourcesService) GetSnowflakeOnboarding(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.SnowflakeOnboarding, error) {
	return nil, fmt.Errorf("unexpected call to fakeDataSourcesService.GetSnowflakeOnboarding")
}

func (s *fakeDataSourcesService) TestConnection(context.Context, slug.Slug, *datasourcessvc.TestConnectionParams) error {
	return nil
}

func (s *fakeDataSourcesService) TestExistingConnection(context.Context, slug.Slug, slug.Slug) error {
	return nil
}

func (s *fakeDataSourcesService) ListByOrg(context.Context, slug.Slug, model.ListRequest) (model.ListResponse[datasourcessvc.DataSource], error) {
	return model.ListResponse[datasourcessvc.DataSource]{}, nil
}

func (s *fakeDataSourcesService) Update(context.Context, slug.Slug, slug.Slug, model.EventActor, *datasourcessvc.UpdateParams) (*datasourcessvc.DataSource, error) {
	return nil, datasourcessvc.ErrDataSourceNotFound
}

func (s *fakeDataSourcesService) UpdateCredentials(context.Context, slug.Slug, slug.Slug, model.EventActor, *datasourcessvc.UpdateCredentialsParams) (*datasourcessvc.DataSource, error) {
	return nil, datasourcessvc.ErrDataSourceNotFound
}

func (s *fakeDataSourcesService) CompleteProviderOAuthCallback(context.Context, credentialssvc.CompleteOAuthCallbackParams) (credentialssvc.CompleteOAuthCallbackResult, error) {
	return credentialssvc.CompleteOAuthCallbackResult{}, fmt.Errorf("unexpected call to fakeDataSourcesService.CompleteProviderOAuthCallback")
}

func (s *fakeDataSourcesService) ValidateCredentialLifecycle(context.Context, slug.Slug, slug.Slug, model.EventActor, model.EventAction) (*datasourcessvc.DataSource, error) {
	return nil, fmt.Errorf("unexpected call to fakeDataSourcesService.ValidateCredentialLifecycle")
}

func (s *fakeDataSourcesService) RecordCredentialLifecycleEvent(context.Context, slug.Slug, slug.Slug, model.EventActor, model.EventAction) error {
	return fmt.Errorf("unexpected call to fakeDataSourcesService.RecordCredentialLifecycleEvent")
}

func (s *fakeDataSourcesService) ListBranches(context.Context, slug.Slug, slug.Slug) ([]datasourcessvc.DataSourceBranch, error) {
	return nil, fmt.Errorf("unexpected call to fakeDataSourcesService.ListBranches")
}

func (s *fakeDataSourcesService) ResolveGitExecutionAuth(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitExecutionAuth, error) {
	return nil, fmt.Errorf("unexpected call to fakeDataSourcesService.ResolveGitExecutionAuth")
}

func (s *fakeDataSourcesService) IntrospectSchema(context.Context, slug.Slug, slug.Slug) (datasourcessvc.SchemaResult, error) {
	return nil, datasourcessvc.ErrDataSourceNotFound
}

func (s *fakeDataSourcesService) RefreshSchema(context.Context, slug.Slug, slug.Slug, model.EventActor) (*datasourcessvc.SchemaImportResult, error) {
	return nil, datasourcessvc.ErrDataSourceNotFound
}

func (s *fakeDataSourcesService) ExecuteQueuedSchemaImport(context.Context, jobs.DataSourceSchemaImportArgs) (*datasourcessvc.SchemaImportResult, error) {
	return nil, fmt.Errorf("unexpected call to fakeDataSourcesService.ExecuteQueuedSchemaImport")
}

func (s *fakeDataSourcesService) Delete(context.Context, slug.Slug, slug.Slug, model.EventActor) error {
	return nil
}

func (s *fakeDataSourcesService) Restore(context.Context, slug.Slug, slug.Slug, model.EventActor) error {
	return nil
}

func (s *fakeDataSourcesService) ResolveGitAuth(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.GitDataSourceAuth, error) {
	return nil, datasourcessvc.ErrDataSourceNotFound
}

func repoRefFromModel(ref *model.DataSourceRef) *datasourcerefs.DataSourceRef {
	return &datasourcerefs.DataSourceRef{
		PipelineSlug:         ref.PipelineSlug,
		Slug:                 ref.Slug,
		Name:                 ref.Name,
		DataSourceType:       ref.DataSourceType,
		RequiredCapabilities: append([]model.DataSourceRefCapability(nil), ref.RequiredCapabilities...),
		CreatedAt:            ref.CreatedAt,
		UpdatedAt:            ref.UpdatedAt,
	}
}

func repoBindingFromModel(binding *model.DataSourceBinding) *datasourcerefs.DataSourceBinding {
	return &datasourcerefs.DataSourceBinding{
		PipelineSlug:         binding.PipelineSlug,
		DataSourceRefSlug:    binding.DataSourceRefSlug,
		DataSourceRefName:    binding.DataSourceRefName,
		EnvironmentSlug:      binding.EnvironmentSlug,
		DataSourceSlug:       binding.DataSourceSlug,
		DataSourceName:       binding.DataSourceName,
		DataSourceType:       binding.DataSourceType,
		DataSourceActive:     binding.DataSourceActive,
		DataSourceReadOnly:   binding.DataSourceReadOnly,
		RequiredCapabilities: append([]model.DataSourceRefCapability(nil), binding.RequiredCapabilities...),
		CreatedAt:            binding.BoundAt,
		UpdatedAt:            binding.UpdatedAt,
	}
}

func cloneRefMap(input map[string]*model.DataSourceRef) map[string]*model.DataSourceRef {
	out := map[string]*model.DataSourceRef{}
	for _, ref := range input {
		cloned := *ref
		if cloned.PipelineSlug == "" {
			cloned.PipelineSlug = defaultTestPipelineSlug
		}
		cloned.RequiredCapabilities = append([]model.DataSourceRefCapability(nil), ref.RequiredCapabilities...)
		out[refKey(cloned.PipelineSlug, cloned.Slug)] = &cloned
	}
	return out
}

func cloneBindingMap(input map[string]*model.DataSourceBinding) map[string]*model.DataSourceBinding {
	out := map[string]*model.DataSourceBinding{}
	for _, binding := range input {
		cloned := *binding
		if cloned.PipelineSlug == "" {
			cloned.PipelineSlug = defaultTestPipelineSlug
		}
		cloned.RequiredCapabilities = append([]model.DataSourceRefCapability(nil), binding.RequiredCapabilities...)
		out[scopedBindingKey(cloned.PipelineSlug, cloned.EnvironmentSlug, cloned.DataSourceRefSlug)] = &cloned
	}
	return out
}

func cloneConcreteDataSourceMap(input map[string]*datasourcessvc.DataSource) map[string]*datasourcessvc.DataSource {
	out := map[string]*datasourcessvc.DataSource{}
	for key, dataSource := range input {
		if dataSource == nil {
			out[key] = nil
			continue
		}
		cloned := *dataSource
		out[key] = &cloned
	}
	return out
}

func refKey(pipelineSlug, refSlug slug.Slug) string {
	return pipelineSlug.String() + "/" + refSlug.String()
}

func bindingKey(environmentSlug, refSlug slug.Slug) string {
	return scopedBindingKey(defaultTestPipelineSlug, environmentSlug, refSlug)
}

func scopedBindingKey(pipelineSlug, environmentSlug, refSlug slug.Slug) string {
	return pipelineSlug.String() + "/" + environmentSlug.String() + "/" + refSlug.String()
}

func (*fakeDataSourcesService) AttachOrganizationGitLabConnection(context.Context, slug.Slug, slug.Slug, model.EventActor) error {
	return fmt.Errorf("unexpected shared GitLab attachment")
}

func (*fakeDataSourcesService) EnsureProofWebhook(context.Context, slug.Slug, slug.Slug) error {
	return fmt.Errorf("unexpected Proof webhook setup")
}

func (s *fakeDataSourcesService) ListGoogleWorkspaceFiles(context.Context, slug.Slug, slug.Slug, workspace.ListOptions) (*workspace.Discovery, error) {
	return nil, fmt.Errorf("unexpected call to fakeDataSourcesService.ListGoogleWorkspaceFiles")
}

func (s *fakeDataSourcesService) VerifyGoogleWorkspaceFiles(context.Context, slug.Slug, slug.Slug, model.EventActor, datasourcessvc.WorkspaceVerificationParams) (*workspace.Discovery, error) {
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (s *fakeDataSourcesService) GetGoogleWorkspacePicker(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.WorkspacePicker, error) {
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (s *fakeDataSourcesService) GetGoogleWorkspaceFile(context.Context, slug.Slug, slug.Slug, string) (*workspace.File, error) {
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (s *fakeDataSourcesService) GetGoogleWorkspaceWorksheets(context.Context, slug.Slug, slug.Slug, string) (*workspace.Metadata, error) {
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (f *fakeDataSourcesService) ConnectHTTPOAuth(ctx context.Context, org, source slug.Slug, actor model.EventActor) (*datasourcessvc.HTTPOAuthConnection, error) {
	if f.ConnectHTTPOAuthFn == nil {
		return nil, xerror.Unexpected().Detail("unexpected ConnectHTTPOAuth call")
	}
	return f.ConnectHTTPOAuthFn(ctx, org, source, actor)
}
