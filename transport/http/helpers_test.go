package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/xerror"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/server"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	testfakes "github.com/Braidplane/braidplane-core/pkg/testutil/fakes"
	"github.com/Braidplane/braidplane-core/postgres/proofreceipts"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/apikeyssvc"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/githubappsvc"
	"github.com/Braidplane/braidplane-core/service/memberssvc"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/odcscontractsvc"
	"github.com/Braidplane/braidplane-core/service/oidc"
	"github.com/Braidplane/braidplane-core/service/orgsvc"
	"github.com/Braidplane/braidplane-core/service/patssvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/pipelinetriggersvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/retentionsvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

var (
	errFakeRolesServiceFnNotSet            = fmt.Errorf("fake roles service function is not set")
	errFakeDataSourcesServiceFnNotSet      = fmt.Errorf("fake data sources service function is not set")
	errFakeDataSourceRefsServiceFnNotSet   = fmt.Errorf("fake data source refs service function is not set")
	errFakeQueriesServiceFnNotSet          = fmt.Errorf("fake queries service function is not set")
	errFakePipelinesServiceFnNotSet        = fmt.Errorf("fake pipelines service function is not set")
	errFakePipelineTriggersServiceFnNotSet = fmt.Errorf("fake pipeline triggers service function is not set")
	errFakeEventsServiceFnNotSet           = fmt.Errorf("fake events service function is not set")
	errFakeNotificationsServiceFnNotSet    = fmt.Errorf("fake notifications service function is not set")
	errFakeProjectsServiceFnNotSet         = fmt.Errorf("fake projects service function is not set")
	errFakeEnvironmentsServiceFnNotSet     = fmt.Errorf("fake environments service function is not set")
	errFakeAPIKeysServiceFnNotSet          = fmt.Errorf("fake api keys service function is not set")
	errFakeCredentialsServiceFnNotSet      = fmt.Errorf("fake credentials service function is not set")
	errFakeProofReceiptServiceFnNotSet     = fmt.Errorf("fake proof receipt service function is not set")
)

// --- Fake service implementations ---

type fakeCredentialsService struct {
	CreateNotificationConnectionFn       func(context.Context, credentialssvc.CreateNotificationConnectionParams) (credentialssvc.NotificationConnection, error)
	ListNotificationConnectionsFn        func(context.Context, slug.Slug) ([]credentialssvc.NotificationConnection, error)
	ConnectNotificationConnectionFn      func(context.Context, credentialssvc.CredentialOwner, model.EventActor) (credentialssvc.ProviderConnectorInstall, error)
	ConnectHTTPOAuthFn                   func(context.Context, credentialssvc.StartOAuthInstallParams) (*credentialssvc.ProviderConnectorInstall, error)
	AcquireHTTPOAuthTestCredentialFn     func(context.Context, credentialssvc.HTTPOAuthApp) (credentialssvc.RuntimeCredential, error)
	ConfigureHTTPOAuthAppFn              func(context.Context, credentialssvc.ConfigureHTTPOAuthAppParams) error
	ConnectHTTPOAuthClientCredentialsFn  func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.CredentialMetadata, error)
	CreateOrganizationGitLabConnectionFn func(ctx context.Context, params credentialssvc.CreateOrganizationGitLabConnectionParams) (credentialssvc.OrganizationGitLabConnection, error)
	GetOrganizationGitLabConnectionFn    func(ctx context.Context, org slug.Slug) (credentialssvc.OrganizationGitLabConnection, error)
	ListOrganizationGitLabRepositoriesFn func(ctx context.Context, org slug.Slug) ([]credentialssvc.ProviderRepository, error)
	AttachOrganizationGitLabConnectionFn func(ctx context.Context, params credentialssvc.AttachOrganizationGitLabConnectionParams) error

	StartOAuthInstallFn        func(context.Context, credentialssvc.StartOAuthInstallParams) (credentialssvc.ProviderConnectorInstall, error)
	FindOAuthInstallFn         func(context.Context, credentialssvc.FindOAuthInstallParams) (credentialssvc.CredentialInstall, error)
	ConsumeOAuthInstallFn      func(context.Context, credentialssvc.ConsumeOAuthInstallParams) (credentialssvc.CredentialInstall, error)
	CompleteOAuthCallbackFn    func(context.Context, credentialssvc.CompleteOAuthCallbackParams) (credentialssvc.CompleteOAuthCallbackResult, error)
	RefreshFn                  func(context.Context, credentialssvc.RefreshGrantParams) (credentialssvc.CredentialMetadata, error)
	RevokeFn                   func(context.Context, credentialssvc.RevokeParams) (credentialssvc.CredentialMetadata, error)
	RotateFn                   func(context.Context, credentialssvc.RotateParams) (credentialssvc.ProviderConnectorInstall, error)
	CreateStaticSecretFn       func(context.Context, credentialssvc.CreateStaticSecretParams) (credentialssvc.CredentialMetadata, error)
	CreatePendingOAuthFn       func(context.Context, credentialssvc.CreatePendingOAuthCredentialParams) (credentialssvc.CredentialMetadata, error)
	ResolveRuntimeCredentialFn func(context.Context, credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.RuntimeCredential, error)
}

func (f *fakeCredentialsService) CreatePendingOAuthCredential(
	ctx context.Context,
	params credentialssvc.CreatePendingOAuthCredentialParams,
) (credentialssvc.CredentialMetadata, error) {
	if f.CreatePendingOAuthFn == nil {
		return credentialssvc.CredentialMetadata{}, errFakeCredentialsServiceFnNotSet
	}
	return f.CreatePendingOAuthFn(ctx, params)
}

func (f *fakeCredentialsService) CreateStaticSecret(
	ctx context.Context,
	params credentialssvc.CreateStaticSecretParams,
) (credentialssvc.CredentialMetadata, error) {
	if f.CreateStaticSecretFn == nil {
		return credentialssvc.CredentialMetadata{}, errFakeCredentialsServiceFnNotSet
	}
	return f.CreateStaticSecretFn(ctx, params)
}

func (f *fakeCredentialsService) StartOAuthInstall(
	ctx context.Context,
	params credentialssvc.StartOAuthInstallParams,
) (credentialssvc.ProviderConnectorInstall, error) {
	if f.StartOAuthInstallFn == nil {
		return credentialssvc.ProviderConnectorInstall{}, errFakeCredentialsServiceFnNotSet
	}
	return f.StartOAuthInstallFn(ctx, params)
}

func (f *fakeCredentialsService) FindOAuthInstall(
	ctx context.Context,
	params credentialssvc.FindOAuthInstallParams,
) (credentialssvc.CredentialInstall, error) {
	if f.FindOAuthInstallFn == nil {
		return credentialssvc.CredentialInstall{}, errFakeCredentialsServiceFnNotSet
	}
	return f.FindOAuthInstallFn(ctx, params)
}

func (f *fakeCredentialsService) ConsumeOAuthInstall(
	ctx context.Context,
	params credentialssvc.ConsumeOAuthInstallParams,
) (credentialssvc.CredentialInstall, error) {
	if f.ConsumeOAuthInstallFn == nil {
		return credentialssvc.CredentialInstall{}, errFakeCredentialsServiceFnNotSet
	}
	return f.ConsumeOAuthInstallFn(ctx, params)
}

func (f *fakeCredentialsService) CompleteOAuthCallback(
	ctx context.Context,
	params credentialssvc.CompleteOAuthCallbackParams,
) (credentialssvc.CompleteOAuthCallbackResult, error) {
	if f.CompleteOAuthCallbackFn == nil {
		return credentialssvc.CompleteOAuthCallbackResult{}, errFakeCredentialsServiceFnNotSet
	}
	return f.CompleteOAuthCallbackFn(ctx, params)
}

func (f *fakeCredentialsService) ResolveRuntimeCredential(
	ctx context.Context,
	params credentialssvc.ResolveRuntimeCredentialParams,
) (credentialssvc.RuntimeCredential, error) {
	if f.ResolveRuntimeCredentialFn == nil {
		return credentialssvc.RuntimeCredential{}, errFakeCredentialsServiceFnNotSet
	}
	return f.ResolveRuntimeCredentialFn(ctx, params)
}

func (f *fakeCredentialsService) Refresh(
	ctx context.Context,
	params credentialssvc.RefreshGrantParams,
) (credentialssvc.CredentialMetadata, error) {
	if f.RefreshFn == nil {
		return credentialssvc.CredentialMetadata{}, errFakeCredentialsServiceFnNotSet
	}
	return f.RefreshFn(ctx, params)
}

func (f *fakeCredentialsService) Revoke(
	ctx context.Context,
	params credentialssvc.RevokeParams,
) (credentialssvc.CredentialMetadata, error) {
	if f.RevokeFn == nil {
		return credentialssvc.CredentialMetadata{}, errFakeCredentialsServiceFnNotSet
	}
	return f.RevokeFn(ctx, params)
}

func (f *fakeCredentialsService) Rotate(
	ctx context.Context,
	params credentialssvc.RotateParams,
) (credentialssvc.ProviderConnectorInstall, error) {
	if f.RotateFn == nil {
		return credentialssvc.ProviderConnectorInstall{}, errFakeCredentialsServiceFnNotSet
	}
	return f.RotateFn(ctx, params)
}

type fakeUsersService struct {
	CreateUserFn              func(ctx context.Context, newUser model.NewUser) (*model.User, error)
	BeginLoginFn              func(ctx context.Context, email, password string) (*model.SessionLoginResult, error)
	LoginUserFn               func(ctx context.Context, email, password string) (*model.User, error)
	GetUserBySlugFn           func(ctx context.Context, slug slug.Slug) (*model.User, error)
	LoginOIDCUserFn           func(ctx context.Context, oidcUser *model.OIDCUserInfo) (*model.User, error)
	LoginOrCreateOIDCUserFn   func(ctx context.Context, oidcUser *model.OIDCUserInfo) (*model.User, error)
	VerifyEmailFn             func(ctx context.Context, token string) error
	RequestPasswordResetFn    func(ctx context.Context, email string) error
	ResetPasswordFn           func(ctx context.Context, token, password string) error
	ChangePasswordFn          func(ctx context.Context, userSlug slug.Slug, currentPassword, newPassword string) error
	ResendVerificationEmailFn func(ctx context.Context, email string) error
	UpdateProfileFn           func(ctx context.Context, userSlug slug.Slug, update model.UpdateUserProfile) (*model.User, error)
	VerifySessionMFAFn        func(ctx context.Context, challengeToken, code string) (*model.User, error)
}

func (f *fakeUsersService) CreateUser(
	ctx context.Context,
	newUser model.NewUser,
) (*model.User, error) {
	return f.CreateUserFn(ctx, newUser)
}

func (f *fakeUsersService) LoginUser(
	ctx context.Context,
	email, password string,
) (*model.User, error) {
	return f.LoginUserFn(ctx, email, password)
}

func (f *fakeUsersService) BeginLogin(
	ctx context.Context,
	email, password string,
) (*model.SessionLoginResult, error) {
	if f.BeginLoginFn != nil {
		return f.BeginLoginFn(ctx, email, password)
	}
	user, err := f.LoginUser(ctx, email, password)
	if err != nil {
		return nil, err
	}
	return &model.SessionLoginResult{User: user}, nil
}

func (f *fakeUsersService) GetUserBySlug(ctx context.Context, slug slug.Slug) (*model.User, error) {
	return f.GetUserBySlugFn(ctx, slug)
}

func (f *fakeUsersService) LoginOIDCUser(
	ctx context.Context,
	oidcUser *model.OIDCUserInfo,
) (*model.User, error) {
	return f.LoginOIDCUserFn(ctx, oidcUser)
}

func (f *fakeUsersService) LoginOrCreateOIDCUser(
	ctx context.Context,
	oidcUser *model.OIDCUserInfo,
) (*model.User, error) {
	return f.LoginOrCreateOIDCUserFn(ctx, oidcUser)
}

func (f *fakeUsersService) VerifyEmail(ctx context.Context, token string) error {
	return f.VerifyEmailFn(ctx, token)
}

func (f *fakeUsersService) RequestPasswordReset(ctx context.Context, email string) error {
	return f.RequestPasswordResetFn(ctx, email)
}

func (f *fakeUsersService) ResetPassword(ctx context.Context, token, password string) error {
	return f.ResetPasswordFn(ctx, token, password)
}

func (f *fakeUsersService) ChangePassword(
	ctx context.Context,
	userSlug slug.Slug,
	currentPassword,
	newPassword string,
) error {
	return f.ChangePasswordFn(ctx, userSlug, currentPassword, newPassword)
}

func (f *fakeUsersService) ResendVerificationEmail(ctx context.Context, email string) error {
	return f.ResendVerificationEmailFn(ctx, email)
}

func (f *fakeUsersService) UpdateProfile(
	ctx context.Context,
	userSlug slug.Slug,
	update model.UpdateUserProfile,
) (*model.User, error) {
	return f.UpdateProfileFn(ctx, userSlug, update)
}

func (f *fakeUsersService) VerifySessionMFA(
	ctx context.Context,
	challengeToken string,
	code string,
) (*model.User, error) {
	return f.VerifySessionMFAFn(ctx, challengeToken, code)
}

type fakeOrgService struct {
	DeleteOrganizationFn    func(context.Context, slug.Slug) error
	CreateOrganizationFn    func(ctx context.Context, userSlug slug.Slug, actor model.EventActor, newOrg model.NewOrganization) (*model.Organization, error)
	ListOrganizationsFn     func(ctx context.Context, userSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.OrganizationWithRole], error)
	GetOrganizationFn       func(ctx context.Context, slug slug.Slug) (*model.Organization, error)
	GetRetentionPolicyFn    func(ctx context.Context, slug slug.Slug) (model.OrganizationRetentionPolicy, error)
	UpdateRetentionPolicyFn func(ctx context.Context, slug slug.Slug, actor model.EventActor, policy model.OrganizationRetentionPolicy) (model.OrganizationRetentionPolicy, error)
	UpdateOrganizationFn    func(ctx context.Context, slug slug.Slug, actor model.EventActor, update model.UpdateOrganization) (*model.Organization, error)
}

func (f *fakeOrgService) DeleteOrganization(ctx context.Context, s slug.Slug) error {
	return f.DeleteOrganizationFn(ctx, s)
}

func (f *fakeOrgService) CreateOrganization(
	ctx context.Context,
	userSlug slug.Slug,
	actor model.EventActor,
	newOrg model.NewOrganization,
) (*model.Organization, error) {
	return f.CreateOrganizationFn(ctx, userSlug, actor, newOrg)
}

func (f *fakeOrgService) ListOrganizations(
	ctx context.Context,
	userSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.OrganizationWithRole], error) {
	return f.ListOrganizationsFn(ctx, userSlug, req)
}

func (f *fakeOrgService) GetOrganization(
	ctx context.Context,
	slug slug.Slug,
) (*model.Organization, error) {
	return f.GetOrganizationFn(ctx, slug)
}

func (f *fakeOrgService) GetRetentionPolicy(
	ctx context.Context,
	orgSlug slug.Slug,
) (model.OrganizationRetentionPolicy, error) {
	return f.GetRetentionPolicyFn(ctx, orgSlug)
}

func (f *fakeOrgService) UpdateRetentionPolicy(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	policy model.OrganizationRetentionPolicy,
) (model.OrganizationRetentionPolicy, error) {
	return f.UpdateRetentionPolicyFn(ctx, orgSlug, actor, policy)
}

func (f *fakeOrgService) UpdateOrganization(
	ctx context.Context,
	slug slug.Slug,
	actor model.EventActor,
	update model.UpdateOrganization,
) (*model.Organization, error) {
	return f.UpdateOrganizationFn(ctx, slug, actor, update)
}

type fakeRetentionService struct {
	PruneExpiredCustomerDataFn func(context.Context, retentionsvc.MaintenanceExecution) (retentionsvc.Result, error)
	ListMaintenanceRunsFn      func(context.Context, slug.Slug) ([]model.OrganizationRetentionRun, error)
}

func (f *fakeRetentionService) PruneExpiredCustomerData(
	ctx context.Context,
	execution retentionsvc.MaintenanceExecution,
) (retentionsvc.Result, error) {
	if f.PruneExpiredCustomerDataFn != nil {
		return f.PruneExpiredCustomerDataFn(ctx, execution)
	}
	return retentionsvc.Result{}, nil
}

func (f *fakeRetentionService) ListMaintenanceRuns(
	ctx context.Context,
	orgSlug slug.Slug,
) ([]model.OrganizationRetentionRun, error) {
	if f.ListMaintenanceRunsFn != nil {
		return f.ListMaintenanceRunsFn(ctx, orgSlug)
	}
	return []model.OrganizationRetentionRun{}, nil
}

type fakeMembersService struct {
	ListMembersFn               func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.Member], error)
	ListMemberInvitationsFn     func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.MemberInvitation], error)
	ListMyMemberInvitationsFn   func(ctx context.Context, userSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.MemberInvitation], error)
	GetMemberInvitationFn       func(ctx context.Context, orgSlug, invitationSlug slug.Slug) (*model.MemberInvitation, error)
	CreateMemberInvitationFn    func(ctx context.Context, orgSlug, inviterUserSlug slug.Slug, actor model.EventActor, newInvitation model.NewMemberInvitation) (*model.MemberInvitation, error)
	ResendMemberInvitationFn    func(ctx context.Context, orgSlug, invitationSlug slug.Slug, actor model.EventActor) (*model.MemberInvitation, error)
	RevokeMemberInvitationFn    func(ctx context.Context, orgSlug, invitationSlug slug.Slug, actor model.EventActor) (*model.MemberInvitation, error)
	AcceptMemberInvitationFn    func(ctx context.Context, userSlug slug.Slug, token string, actor model.EventActor) (*model.MemberInvitationAcceptance, error)
	AcceptMyMemberInvitationFn  func(ctx context.Context, userSlug, orgSlug, invitationSlug slug.Slug, actor model.EventActor) (*model.MemberInvitationAcceptance, error)
	DeclineMemberInvitationFn   func(ctx context.Context, userSlug slug.Slug, token string, actor model.EventActor) (*model.MemberInvitation, error)
	DeclineMyMemberInvitationFn func(ctx context.Context, userSlug, orgSlug, invitationSlug slug.Slug, actor model.EventActor) (*model.MemberInvitation, error)
	CreateMemberFn              func(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, newMember model.NewMember) (*model.Member, error)
	CreateBootstrapMemberFn     func(ctx context.Context, orgSlug slug.Slug, newMember model.NewMember) (*model.Member, error)
	UpdateMemberFn              func(ctx context.Context, orgSlug, userSlug slug.Slug, actor model.EventActor, update model.UpdateMember) (*model.Member, error)
	RemoveMemberFn              func(ctx context.Context, orgSlug, userSlug slug.Slug, actor model.EventActor) error
}

type fakeAPIKeysService struct {
	ListAPIKeysFn         func(ctx context.Context, orgSlug slug.Slug, roleFilter *slug.Slug, req model.ListRequest) (model.ListResponse[model.APIKey], error)
	CreateAPIKeyFn        func(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, newAPIKey model.NewAPIKey) (*model.APIKeyWithToken, error)
	GetAPIKeyFn           func(ctx context.Context, orgSlug, apiKeySlug slug.Slug) (*model.APIKey, error)
	UpdateAPIKeyFn        func(ctx context.Context, orgSlug, apiKeySlug slug.Slug, actor model.EventActor, update model.UpdateAPIKey) (*model.APIKey, error)
	RevokeAPIKeyFn        func(ctx context.Context, orgSlug, apiKeySlug slug.Slug, actor model.EventActor) error
	DeleteRevokedAPIKeyFn func(ctx context.Context, orgSlug, apiKeySlug slug.Slug, actor model.EventActor) error
	RotateAPIKeyFn        func(ctx context.Context, orgSlug, apiKeySlug slug.Slug, actor model.EventActor) (*model.APIKeyWithToken, error)
	AuthenticateFn        func(ctx context.Context, token string) (*model.APIKeyPrincipal, error)
}

type fakePATsService struct {
	ListPersonalAccessTokensFn         func(ctx context.Context, userSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.PersonalAccessToken], error)
	CreatePersonalAccessTokenFn        func(ctx context.Context, userSlug slug.Slug, newPAT model.NewPersonalAccessToken) (*model.PersonalAccessTokenWithToken, error)
	GetPersonalAccessTokenFn           func(ctx context.Context, userSlug, patSlug slug.Slug) (*model.PersonalAccessToken, error)
	UpdatePersonalAccessTokenFn        func(ctx context.Context, userSlug, patSlug slug.Slug, update model.UpdatePersonalAccessToken) (*model.PersonalAccessToken, error)
	RevokePersonalAccessTokenFn        func(ctx context.Context, userSlug, patSlug slug.Slug) error
	DeleteRevokedPersonalAccessTokenFn func(ctx context.Context, userSlug, patSlug slug.Slug) error
	RotatePersonalAccessTokenFn        func(ctx context.Context, userSlug, patSlug slug.Slug) (*model.PersonalAccessTokenWithToken, error)
	AuthenticateFn                     func(ctx context.Context, token string) (*model.UserPrincipal, error)
}

type fakeProjectsService struct {
	CreateProjectFn             func(ctx context.Context, orgSlug slug.Slug, newProject model.NewProject, actor model.EventActor) (*model.Project, error)
	GetProjectFn                func(ctx context.Context, orgSlug, projectSlug slug.Slug) (*model.Project, error)
	ListProjectsFn              func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.Project], error)
	UpdateProjectFn             func(ctx context.Context, orgSlug, projectSlug slug.Slug, update model.UpdateProject, actor model.EventActor) (*model.Project, error)
	SetProjectPipelineBackendFn func(ctx context.Context, orgSlug, projectSlug slug.Slug, params model.SetProjectPipelineBackend) (*model.Project, error)
	DeleteProjectFn             func(ctx context.Context, orgSlug, projectSlug slug.Slug, options model.DeleteProjectOptions, actor model.EventActor) error
}

func (f *fakeProjectsService) CreateProject(
	ctx context.Context,
	orgSlug slug.Slug,
	newProject model.NewProject,
	actor model.EventActor,
) (*model.Project, error) {
	if f.CreateProjectFn != nil {
		return f.CreateProjectFn(ctx, orgSlug, newProject, actor)
	}
	return nil, errFakeProjectsServiceFnNotSet
}

func (f *fakeProjectsService) GetProject(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
) (*model.Project, error) {
	if f.GetProjectFn != nil {
		return f.GetProjectFn(ctx, orgSlug, projectSlug)
	}
	return nil, errFakeProjectsServiceFnNotSet
}

func (f *fakeProjectsService) ListProjects(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.Project], error) {
	if f.ListProjectsFn != nil {
		return f.ListProjectsFn(ctx, orgSlug, req)
	}
	return model.ListResponse[model.Project]{}, errFakeProjectsServiceFnNotSet
}

func (f *fakeProjectsService) UpdateProject(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	update model.UpdateProject,
	actor model.EventActor,
) (*model.Project, error) {
	if f.UpdateProjectFn != nil {
		return f.UpdateProjectFn(ctx, orgSlug, projectSlug, update, actor)
	}
	return nil, errFakeProjectsServiceFnNotSet
}

func (f *fakeProjectsService) DeleteProject(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	options model.DeleteProjectOptions,
	actor model.EventActor,
) error {
	if f.DeleteProjectFn != nil {
		return f.DeleteProjectFn(ctx, orgSlug, projectSlug, options, actor)
	}
	return errFakeProjectsServiceFnNotSet
}

func (f *fakeProjectsService) SetProjectPipelineBackend(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	params model.SetProjectPipelineBackend,
) (*model.Project, error) {
	if f.SetProjectPipelineBackendFn != nil {
		return f.SetProjectPipelineBackendFn(ctx, orgSlug, projectSlug, params)
	}
	return nil, errFakeProjectsServiceFnNotSet
}

type fakeEnvironmentsService struct {
	CreateDefaultEnvironmentsFn func(ctx context.Context, orgSlug slug.Slug) error
	CreateEnvironmentFn         func(ctx context.Context, orgSlug slug.Slug, newEnvironment model.NewEnvironment, actor model.EventActor) (*model.Environment, error)
	GetEnvironmentFn            func(ctx context.Context, orgSlug, environmentSlug slug.Slug) (*model.Environment, error)
	ListEnvironmentsFn          func(ctx context.Context, orgSlug slug.Slug, req model.ListEnvironmentsRequest) (model.ListResponse[model.Environment], error)
	UpdateEnvironmentFn         func(ctx context.Context, orgSlug, environmentSlug slug.Slug, update model.UpdateEnvironment, actor model.EventActor) (*model.Environment, error)
	DeleteEnvironmentFn         func(
		ctx context.Context,
		orgSlug, environmentSlug slug.Slug,
		deleted model.DeleteEnvironment,
		actor model.EventActor,
	) error
	EnableProjectEnvironmentFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		params model.EnableProjectEnvironment,
		actor model.EventActor,
	) (*model.ProjectEnvironment, error)
	ListProjectEnvironmentsFn   func(ctx context.Context, orgSlug, projectSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.ProjectEnvironment], error)
	DisableProjectEnvironmentFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		confirmation *slug.Slug,
		actor model.EventActor,
	) error
	UpsertRuntimeVariableFn   func(ctx context.Context, orgSlug, environmentSlug slug.Slug, params model.UpsertRuntimeVariable, actor model.EventActor) (*model.RuntimeVariable, error)
	GetRuntimeVariableFn      func(ctx context.Context, orgSlug, environmentSlug slug.Slug, key string) (*model.RuntimeVariable, error)
	ListRuntimeVariablesFn    func(ctx context.Context, orgSlug, environmentSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.RuntimeVariable], error)
	DeleteRuntimeVariableFn   func(ctx context.Context, orgSlug, environmentSlug slug.Slug, key string, confirmation *slug.Slug, actor model.EventActor) error
	UpsertEnvironmentSecretFn func(ctx context.Context, orgSlug, environmentSlug slug.Slug, params model.UpsertEnvironmentSecret, actor model.EventActor) (*model.EnvironmentSecret, error)
	GetEnvironmentSecretFn    func(ctx context.Context, orgSlug, environmentSlug slug.Slug, key string) (*model.EnvironmentSecret, error)
	ListEnvironmentSecretsFn  func(ctx context.Context, orgSlug, environmentSlug slug.Slug, req model.ListRequest) (model.ListResponse[model.EnvironmentSecret], error)
	DeleteEnvironmentSecretFn func(ctx context.Context, orgSlug, environmentSlug slug.Slug, key string, confirmation *slug.Slug, actor model.EventActor) error
	ResolveRuntimeSecretsFn   func(ctx context.Context, orgSlug, environmentSlug slug.Slug) ([]model.RuntimeSecretValue, error)
}

func (f *fakeEnvironmentsService) CreateDefaultEnvironments(ctx context.Context, orgSlug slug.Slug) error {
	if f.CreateDefaultEnvironmentsFn != nil {
		return f.CreateDefaultEnvironmentsFn(ctx, orgSlug)
	}
	return errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) CreateEnvironment(
	ctx context.Context,
	orgSlug slug.Slug,
	newEnvironment model.NewEnvironment,
	actor model.EventActor,
) (*model.Environment, error) {
	if f.CreateEnvironmentFn != nil {
		return f.CreateEnvironmentFn(ctx, orgSlug, newEnvironment, actor)
	}
	return nil, errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) GetEnvironment(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
) (*model.Environment, error) {
	if f.GetEnvironmentFn != nil {
		return f.GetEnvironmentFn(ctx, orgSlug, environmentSlug)
	}
	return nil, errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) ListEnvironments(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListEnvironmentsRequest,
) (model.ListResponse[model.Environment], error) {
	if f.ListEnvironmentsFn != nil {
		return f.ListEnvironmentsFn(ctx, orgSlug, req)
	}
	return model.ListResponse[model.Environment]{}, errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) UpdateEnvironment(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	update model.UpdateEnvironment,
	actor model.EventActor,
) (*model.Environment, error) {
	if f.UpdateEnvironmentFn != nil {
		return f.UpdateEnvironmentFn(ctx, orgSlug, environmentSlug, update, actor)
	}
	return nil, errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) DeleteEnvironment(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	deleted model.DeleteEnvironment,
	actor model.EventActor,
) error {
	if f.DeleteEnvironmentFn != nil {
		return f.DeleteEnvironmentFn(ctx, orgSlug, environmentSlug, deleted, actor)
	}
	return errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) EnableProjectEnvironment(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	params model.EnableProjectEnvironment,
	actor model.EventActor,
) (*model.ProjectEnvironment, error) {
	if f.EnableProjectEnvironmentFn != nil {
		return f.EnableProjectEnvironmentFn(ctx, orgSlug, projectSlug, environmentSlug, params, actor)
	}
	return nil, errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) ListProjectEnvironments(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.ProjectEnvironment], error) {
	if f.ListProjectEnvironmentsFn != nil {
		return f.ListProjectEnvironmentsFn(ctx, orgSlug, projectSlug, req)
	}
	return model.ListResponse[model.ProjectEnvironment]{}, errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) DisableProjectEnvironment(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	confirmation *slug.Slug,
	actor model.EventActor,
) error {
	if f.DisableProjectEnvironmentFn != nil {
		return f.DisableProjectEnvironmentFn(ctx, orgSlug, projectSlug, environmentSlug, confirmation, actor)
	}
	return errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) UpsertRuntimeVariable(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	params model.UpsertRuntimeVariable,
	actor model.EventActor,
) (*model.RuntimeVariable, error) {
	if f.UpsertRuntimeVariableFn != nil {
		return f.UpsertRuntimeVariableFn(ctx, orgSlug, environmentSlug, params, actor)
	}
	return nil, errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) GetRuntimeVariable(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	key string,
) (*model.RuntimeVariable, error) {
	if f.GetRuntimeVariableFn != nil {
		return f.GetRuntimeVariableFn(ctx, orgSlug, environmentSlug, key)
	}
	return nil, errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) ListRuntimeVariables(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.RuntimeVariable], error) {
	if f.ListRuntimeVariablesFn != nil {
		return f.ListRuntimeVariablesFn(ctx, orgSlug, environmentSlug, req)
	}
	return model.ListResponse[model.RuntimeVariable]{}, errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) DeleteRuntimeVariable(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	key string,
	confirmation *slug.Slug,
	actor model.EventActor,
) error {
	if f.DeleteRuntimeVariableFn != nil {
		return f.DeleteRuntimeVariableFn(ctx, orgSlug, environmentSlug, key, confirmation, actor)
	}
	return errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) UpsertEnvironmentSecret(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	params model.UpsertEnvironmentSecret,
	actor model.EventActor,
) (*model.EnvironmentSecret, error) {
	if f.UpsertEnvironmentSecretFn != nil {
		return f.UpsertEnvironmentSecretFn(ctx, orgSlug, environmentSlug, params, actor)
	}
	return nil, errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) GetEnvironmentSecret(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	key string,
) (*model.EnvironmentSecret, error) {
	if f.GetEnvironmentSecretFn != nil {
		return f.GetEnvironmentSecretFn(ctx, orgSlug, environmentSlug, key)
	}
	return nil, errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) ListEnvironmentSecrets(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.EnvironmentSecret], error) {
	if f.ListEnvironmentSecretsFn != nil {
		return f.ListEnvironmentSecretsFn(ctx, orgSlug, environmentSlug, req)
	}
	return model.ListResponse[model.EnvironmentSecret]{}, errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) DeleteEnvironmentSecret(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
	key string,
	confirmation *slug.Slug,
	actor model.EventActor,
) error {
	if f.DeleteEnvironmentSecretFn != nil {
		return f.DeleteEnvironmentSecretFn(ctx, orgSlug, environmentSlug, key, confirmation, actor)
	}
	return errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeEnvironmentsService) ResolveRuntimeSecrets(
	ctx context.Context,
	orgSlug, environmentSlug slug.Slug,
) ([]model.RuntimeSecretValue, error) {
	if f.ResolveRuntimeSecretsFn != nil {
		return f.ResolveRuntimeSecretsFn(ctx, orgSlug, environmentSlug)
	}
	return nil, errFakeEnvironmentsServiceFnNotSet
}

func (f *fakeMembersService) ListMembers(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.Member], error) {
	return f.ListMembersFn(ctx, orgSlug, req)
}

func (f *fakeMembersService) ListMemberInvitations(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.MemberInvitation], error) {
	return f.ListMemberInvitationsFn(ctx, orgSlug, req)
}

func (f *fakeMembersService) ListMyMemberInvitations(
	ctx context.Context,
	userSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.MemberInvitation], error) {
	return f.ListMyMemberInvitationsFn(ctx, userSlug, req)
}

func (f *fakeMembersService) GetMemberInvitation(
	ctx context.Context,
	orgSlug, invitationSlug slug.Slug,
) (*model.MemberInvitation, error) {
	return f.GetMemberInvitationFn(ctx, orgSlug, invitationSlug)
}

func (f *fakeMembersService) CreateMemberInvitation(
	ctx context.Context,
	orgSlug slug.Slug,
	inviterUserSlug slug.Slug,
	actor model.EventActor,
	newInvitation model.NewMemberInvitation,
) (*model.MemberInvitation, error) {
	return f.CreateMemberInvitationFn(ctx, orgSlug, inviterUserSlug, actor, newInvitation)
}

func (f *fakeMembersService) ResendMemberInvitation(
	ctx context.Context,
	orgSlug, invitationSlug slug.Slug,
	actor model.EventActor,
) (*model.MemberInvitation, error) {
	return f.ResendMemberInvitationFn(ctx, orgSlug, invitationSlug, actor)
}

func (f *fakeMembersService) RevokeMemberInvitation(
	ctx context.Context,
	orgSlug, invitationSlug slug.Slug,
	actor model.EventActor,
) (*model.MemberInvitation, error) {
	return f.RevokeMemberInvitationFn(ctx, orgSlug, invitationSlug, actor)
}

func (f *fakeMembersService) AcceptMemberInvitation(
	ctx context.Context,
	userSlug slug.Slug,
	token string,
	actor model.EventActor,
) (*model.MemberInvitationAcceptance, error) {
	return f.AcceptMemberInvitationFn(ctx, userSlug, token, actor)
}

func (f *fakeMembersService) AcceptMyMemberInvitation(
	ctx context.Context,
	userSlug, orgSlug, invitationSlug slug.Slug,
	actor model.EventActor,
) (*model.MemberInvitationAcceptance, error) {
	return f.AcceptMyMemberInvitationFn(ctx, userSlug, orgSlug, invitationSlug, actor)
}

func (f *fakeMembersService) DeclineMemberInvitation(
	ctx context.Context,
	userSlug slug.Slug,
	token string,
	actor model.EventActor,
) (*model.MemberInvitation, error) {
	return f.DeclineMemberInvitationFn(ctx, userSlug, token, actor)
}

func (f *fakeMembersService) DeclineMyMemberInvitation(
	ctx context.Context,
	userSlug, orgSlug, invitationSlug slug.Slug,
	actor model.EventActor,
) (*model.MemberInvitation, error) {
	return f.DeclineMyMemberInvitationFn(ctx, userSlug, orgSlug, invitationSlug, actor)
}

func (f *fakeMembersService) CreateMember(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	newMember model.NewMember,
) (*model.Member, error) {
	return f.CreateMemberFn(ctx, orgSlug, actor, newMember)
}

func (f *fakeMembersService) CreateBootstrapMember(
	ctx context.Context,
	orgSlug slug.Slug,
	newMember model.NewMember,
) (*model.Member, error) {
	if f.CreateBootstrapMemberFn != nil {
		return f.CreateBootstrapMemberFn(ctx, orgSlug, newMember)
	}
	return &model.Member{UserSlug: newMember.UserSlug, Role: newMember.Role}, nil
}

func (f *fakeMembersService) UpdateMember(
	ctx context.Context,
	orgSlug, userSlug slug.Slug,
	actor model.EventActor,
	update model.UpdateMember,
) (*model.Member, error) {
	return f.UpdateMemberFn(ctx, orgSlug, userSlug, actor, update)
}

func (f *fakeMembersService) RemoveMember(
	ctx context.Context,
	orgSlug, userSlug slug.Slug,
	actor model.EventActor,
) error {
	return f.RemoveMemberFn(ctx, orgSlug, userSlug, actor)
}

func (f *fakeAPIKeysService) ListAPIKeys(
	ctx context.Context,
	orgSlug slug.Slug,
	roleFilter *slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.APIKey], error) {
	if f.ListAPIKeysFn != nil {
		return f.ListAPIKeysFn(ctx, orgSlug, roleFilter, req)
	}
	return model.ListResponse[model.APIKey]{}, errFakeAPIKeysServiceFnNotSet
}

func (f *fakeAPIKeysService) CreateAPIKey(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	newAPIKey model.NewAPIKey,
) (*model.APIKeyWithToken, error) {
	if f.CreateAPIKeyFn != nil {
		return f.CreateAPIKeyFn(ctx, orgSlug, actor, newAPIKey)
	}
	return nil, errFakeAPIKeysServiceFnNotSet
}

func (f *fakeAPIKeysService) GetAPIKey(
	ctx context.Context,
	orgSlug, apiKeySlug slug.Slug,
) (*model.APIKey, error) {
	if f.GetAPIKeyFn != nil {
		return f.GetAPIKeyFn(ctx, orgSlug, apiKeySlug)
	}
	return nil, errFakeAPIKeysServiceFnNotSet
}

func (f *fakeAPIKeysService) UpdateAPIKey(
	ctx context.Context,
	orgSlug, apiKeySlug slug.Slug,
	actor model.EventActor,
	update model.UpdateAPIKey,
) (*model.APIKey, error) {
	if f.UpdateAPIKeyFn != nil {
		return f.UpdateAPIKeyFn(ctx, orgSlug, apiKeySlug, actor, update)
	}
	return nil, errFakeAPIKeysServiceFnNotSet
}

func (f *fakeAPIKeysService) RevokeAPIKey(
	ctx context.Context,
	orgSlug, apiKeySlug slug.Slug,
	actor model.EventActor,
) error {
	if f.RevokeAPIKeyFn != nil {
		return f.RevokeAPIKeyFn(ctx, orgSlug, apiKeySlug, actor)
	}
	return errFakeAPIKeysServiceFnNotSet
}

func (f *fakeAPIKeysService) DeleteRevokedAPIKey(
	ctx context.Context,
	orgSlug, apiKeySlug slug.Slug,
	actor model.EventActor,
) error {
	if f.DeleteRevokedAPIKeyFn != nil {
		return f.DeleteRevokedAPIKeyFn(ctx, orgSlug, apiKeySlug, actor)
	}
	return errFakeAPIKeysServiceFnNotSet
}

func (f *fakeAPIKeysService) RotateAPIKey(
	ctx context.Context,
	orgSlug, apiKeySlug slug.Slug,
	actor model.EventActor,
) (*model.APIKeyWithToken, error) {
	if f.RotateAPIKeyFn != nil {
		return f.RotateAPIKeyFn(ctx, orgSlug, apiKeySlug, actor)
	}
	return nil, errFakeAPIKeysServiceFnNotSet
}

func (f *fakeAPIKeysService) Authenticate(
	ctx context.Context,
	token string,
) (*model.APIKeyPrincipal, error) {
	if f.AuthenticateFn != nil {
		return f.AuthenticateFn(ctx, token)
	}
	return nil, apikeyssvc.ErrInvalidToken
}

func (f *fakePATsService) ListPersonalAccessTokens(
	ctx context.Context,
	userSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.PersonalAccessToken], error) {
	if f.ListPersonalAccessTokensFn != nil {
		return f.ListPersonalAccessTokensFn(ctx, userSlug, req)
	}
	return model.ListResponse[model.PersonalAccessToken]{}, errFakeAPIKeysServiceFnNotSet
}

func (f *fakePATsService) CreatePersonalAccessToken(
	ctx context.Context,
	userSlug slug.Slug,
	newPAT model.NewPersonalAccessToken,
) (*model.PersonalAccessTokenWithToken, error) {
	if f.CreatePersonalAccessTokenFn != nil {
		return f.CreatePersonalAccessTokenFn(ctx, userSlug, newPAT)
	}
	return nil, errFakeAPIKeysServiceFnNotSet
}

func (f *fakePATsService) GetPersonalAccessToken(
	ctx context.Context,
	userSlug, patSlug slug.Slug,
) (*model.PersonalAccessToken, error) {
	if f.GetPersonalAccessTokenFn != nil {
		return f.GetPersonalAccessTokenFn(ctx, userSlug, patSlug)
	}
	return nil, errFakeAPIKeysServiceFnNotSet
}

func (f *fakePATsService) UpdatePersonalAccessToken(
	ctx context.Context,
	userSlug, patSlug slug.Slug,
	update model.UpdatePersonalAccessToken,
) (*model.PersonalAccessToken, error) {
	if f.UpdatePersonalAccessTokenFn != nil {
		return f.UpdatePersonalAccessTokenFn(ctx, userSlug, patSlug, update)
	}
	return nil, errFakeAPIKeysServiceFnNotSet
}

func (f *fakePATsService) RevokePersonalAccessToken(
	ctx context.Context,
	userSlug, patSlug slug.Slug,
) error {
	if f.RevokePersonalAccessTokenFn != nil {
		return f.RevokePersonalAccessTokenFn(ctx, userSlug, patSlug)
	}
	return errFakeAPIKeysServiceFnNotSet
}

func (f *fakePATsService) DeleteRevokedPersonalAccessToken(
	ctx context.Context,
	userSlug, patSlug slug.Slug,
) error {
	if f.DeleteRevokedPersonalAccessTokenFn != nil {
		return f.DeleteRevokedPersonalAccessTokenFn(ctx, userSlug, patSlug)
	}
	return errFakeAPIKeysServiceFnNotSet
}

func (f *fakePATsService) RotatePersonalAccessToken(
	ctx context.Context,
	userSlug, patSlug slug.Slug,
) (*model.PersonalAccessTokenWithToken, error) {
	if f.RotatePersonalAccessTokenFn != nil {
		return f.RotatePersonalAccessTokenFn(ctx, userSlug, patSlug)
	}
	return nil, errFakeAPIKeysServiceFnNotSet
}

func (f *fakePATsService) Authenticate(
	ctx context.Context,
	token string,
) (*model.UserPrincipal, error) {
	if f.AuthenticateFn != nil {
		return f.AuthenticateFn(ctx, token)
	}
	return nil, patssvc.ErrInvalidToken
}

type fakeRolesService struct {
	CreateRoleWithPermissionsFn func(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, newRole model.NewRole, perms []model.RolePermission) (*model.OrgRole, error)
	CreateDefaultRolesFn        func(ctx context.Context, orgSlug slug.Slug) error
	UpdateRoleWithPermissionsFn func(ctx context.Context, orgSlug, roleSlug slug.Slug, actor model.EventActor, update model.UpdateRole, perms []model.RolePermission) error
	DeleteRoleFn                func(ctx context.Context, orgSlug, roleSlug slug.Slug, actor model.EventActor) error
	RestoreRoleFn               func(ctx context.Context, orgSlug, roleSlug slug.Slug, actor model.EventActor) error
	GetRoleFn                   func(ctx context.Context, orgSlug, roleSlug slug.Slug) (*model.OrgRole, error)
	ListRolesFn                 func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest, includeDeleted, onlyDeleted bool) (model.ListResponse[model.OrgRole], error)
	GetUserPermissionsInOrgFn   func(ctx context.Context, orgSlug, userSlug slug.Slug) ([]model.RolePermission, error)
	ListRolePermissionsFn       func(ctx context.Context, orgSlug, roleSlug slug.Slug) ([]model.RolePermission, error)
}

func (f *fakeRolesService) CreateRoleWithPermissions(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	newRole model.NewRole,
	permissions []model.RolePermission,
) (*model.OrgRole, error) {
	if f.CreateRoleWithPermissionsFn != nil {
		return f.CreateRoleWithPermissionsFn(ctx, orgSlug, actor, newRole, permissions)
	}
	return nil, errFakeRolesServiceFnNotSet
}

func (f *fakeRolesService) CreateDefaultRoles(
	ctx context.Context,
	orgSlug slug.Slug,
) error {
	if f.CreateDefaultRolesFn != nil {
		return f.CreateDefaultRolesFn(ctx, orgSlug)
	}
	return nil
}

func (f *fakeRolesService) UpdateRoleWithPermissions(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
	actor model.EventActor,
	update model.UpdateRole,
	permissions []model.RolePermission,
) error {
	if f.UpdateRoleWithPermissionsFn != nil {
		return f.UpdateRoleWithPermissionsFn(ctx, orgSlug, roleSlug, actor, update, permissions)
	}
	return nil
}

func (f *fakeRolesService) DeleteRole(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.DeleteRoleFn != nil {
		return f.DeleteRoleFn(ctx, orgSlug, roleSlug, actor)
	}
	return nil
}

func (f *fakeRolesService) RestoreRole(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.RestoreRoleFn != nil {
		return f.RestoreRoleFn(ctx, orgSlug, roleSlug, actor)
	}
	return nil
}

func (f *fakeRolesService) GetRole(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
) (*model.OrgRole, error) {
	if f.GetRoleFn != nil {
		return f.GetRoleFn(ctx, orgSlug, roleSlug)
	}
	return nil, errFakeRolesServiceFnNotSet
}

func (f *fakeRolesService) ListRoles(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
	includeDeleted bool,
	onlyDeleted bool,
) (model.ListResponse[model.OrgRole], error) {
	if f.ListRolesFn != nil {
		return f.ListRolesFn(ctx, orgSlug, req, includeDeleted, onlyDeleted)
	}
	return model.ListResponse[model.OrgRole]{}, errFakeRolesServiceFnNotSet
}

func (f *fakeRolesService) GetUserPermissionsInOrg(
	ctx context.Context,
	orgSlug, userSlug slug.Slug,
) ([]model.RolePermission, error) {
	if f.GetUserPermissionsInOrgFn != nil {
		return f.GetUserPermissionsInOrgFn(ctx, orgSlug, userSlug)
	}
	return []model.RolePermission{}, nil
}

func (f *fakeRolesService) ListRolePermissions(
	ctx context.Context,
	orgSlug, roleSlug slug.Slug,
) ([]model.RolePermission, error) {
	if f.ListRolePermissionsFn != nil {
		return f.ListRolePermissionsFn(ctx, orgSlug, roleSlug)
	}
	return []model.RolePermission{}, nil
}

type fakeDataSourcesService struct {
	ConnectHTTPOAuthFn                   func(context.Context, slug.Slug, slug.Slug, model.EventActor) (*datasourcessvc.HTTPOAuthConnection, error)
	VerifyGoogleWorkspaceFilesFn         func(context.Context, slug.Slug, slug.Slug, model.EventActor, datasourcessvc.WorkspaceVerificationParams) (*workspace.Discovery, error)
	ListGoogleWorkspaceFilesFn           func(context.Context, slug.Slug, slug.Slug, workspace.ListOptions) (*workspace.Discovery, error)
	GetSnowflakeOnboardingFn             func(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.SnowflakeOnboarding, error)
	AttachOrganizationGitLabConnectionFn func(context.Context, slug.Slug, slug.Slug, model.EventActor) error
	CreateFn                             func(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, params *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error)
	StartProviderDataSourceInstallFn     func(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, provider string, params *datasourcessvc.StartProviderDataSourceInstallParams) (*datasourcessvc.ProviderDataSourceInstall, error)
	TestConnectionFn                     func(ctx context.Context, orgSlug slug.Slug, params *datasourcessvc.TestConnectionParams) error
	TestExistingConnectionFn             func(ctx context.Context, orgSlug, dsSlug slug.Slug) error
	FindBySlugFn                         func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.DataSource, error)
	ListByOrgFn                          func(ctx context.Context, orgSlug slug.Slug, req model.ListRequest) (model.ListResponse[datasourcessvc.DataSource], error)
	UpdateFn                             func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, params *datasourcessvc.UpdateParams) (*datasourcessvc.DataSource, error)
	UpdateCredentialsFn                  func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, params *datasourcessvc.UpdateCredentialsParams) (*datasourcessvc.DataSource, error)
	CompleteProviderOAuthCallbackFn      func(ctx context.Context, params credentialssvc.CompleteOAuthCallbackParams) (credentialssvc.CompleteOAuthCallbackResult, error)
	ListProviderRepositoriesFn           func(ctx context.Context, orgSlug, dsSlug slug.Slug) ([]datasourcessvc.ProviderRepository, error)
	SelectProviderRepositoryFn           func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, params datasourcessvc.SelectProviderRepositoryParams) (*datasourcessvc.DataSource, error)
	ValidateCredentialLifecycleFn        func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, action model.EventAction) (*datasourcessvc.DataSource, error)
	RecordCredentialLifecycleEventFn     func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, action model.EventAction) error
	ListBranchesFn                       func(ctx context.Context, orgSlug, dsSlug slug.Slug) ([]datasourcessvc.DataSourceBranch, error)
	IntrospectSchemaFn                   func(ctx context.Context, orgSlug, dsSlug slug.Slug) (datasourcessvc.SchemaResult, error)
	RefreshSchemaFn                      func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor) (*datasourcessvc.SchemaImportResult, error)
	DeleteFn                             func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor) error
	RestoreFn                            func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor) error
	ResolveGitAuthFn                     func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.GitDataSourceAuth, error)
	ResolveGitExecutionAuthFn            func(ctx context.Context, orgSlug, dsSlug slug.Slug) (*datasourcessvc.GitExecutionAuth, error)
}

func (f *fakeDataSourcesService) AttachOrganizationGitLabConnection(ctx context.Context, org, ds slug.Slug, actor model.EventActor) error {
	if f.AttachOrganizationGitLabConnectionFn == nil {
		return errFakeDataSourcesServiceFnNotSet
	}
	return f.AttachOrganizationGitLabConnectionFn(ctx, org, ds, actor)
}

func (f *fakeDataSourcesService) ListProviderRepositories(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) ([]datasourcessvc.ProviderRepository, error) {
	if f.ListProviderRepositoriesFn != nil {
		return f.ListProviderRepositoriesFn(ctx, orgSlug, dsSlug)
	}
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) SelectProviderRepository(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	params datasourcessvc.SelectProviderRepositoryParams,
) (*datasourcessvc.DataSource, error) {
	if f.SelectProviderRepositoryFn != nil {
		return f.SelectProviderRepositoryFn(ctx, orgSlug, dsSlug, actor, params)
	}
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) StartProviderDataSourceInstall(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	provider string,
	params *datasourcessvc.StartProviderDataSourceInstallParams,
) (*datasourcessvc.ProviderDataSourceInstall, error) {
	if f.StartProviderDataSourceInstallFn != nil {
		return f.StartProviderDataSourceInstallFn(ctx, orgSlug, actor, provider, params)
	}
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) CreateProvisioned(context.Context, slug.Slug, model.EventActor, *datasourcessvc.CreateParams, datasourcessvc.SourceProvisioner) (*datasourcessvc.DataSource, error) {
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) Create(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	params *datasourcessvc.CreateParams,
) (*datasourcessvc.DataSource, error) {
	if f.CreateFn != nil {
		return f.CreateFn(ctx, orgSlug, actor, params)
	}
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) TestConnection(
	ctx context.Context,
	orgSlug slug.Slug,
	params *datasourcessvc.TestConnectionParams,
) error {
	if f.TestConnectionFn != nil {
		return f.TestConnectionFn(ctx, orgSlug, params)
	}
	return nil
}

func (f *fakeDataSourcesService) TestExistingConnection(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) error {
	if f.TestExistingConnectionFn != nil {
		return f.TestExistingConnectionFn(ctx, orgSlug, dsSlug)
	}
	return nil
}

func (f *fakeDataSourcesService) FindBySlug(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (*datasourcessvc.DataSource, error) {
	if f.FindBySlugFn != nil {
		return f.FindBySlugFn(ctx, orgSlug, dsSlug)
	}
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) ListByOrg(
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

func (f *fakeDataSourcesService) Update(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	params *datasourcessvc.UpdateParams,
) (*datasourcessvc.DataSource, error) {
	if f.UpdateFn != nil {
		return f.UpdateFn(ctx, orgSlug, dsSlug, actor, params)
	}
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) UpdateCredentials(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	params *datasourcessvc.UpdateCredentialsParams,
) (*datasourcessvc.DataSource, error) {
	if f.UpdateCredentialsFn != nil {
		return f.UpdateCredentialsFn(ctx, orgSlug, dsSlug, actor, params)
	}
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) CompleteProviderOAuthCallback(
	ctx context.Context,
	params credentialssvc.CompleteOAuthCallbackParams,
) (credentialssvc.CompleteOAuthCallbackResult, error) {
	if f.CompleteProviderOAuthCallbackFn != nil {
		return f.CompleteProviderOAuthCallbackFn(ctx, params)
	}
	return credentialssvc.CompleteOAuthCallbackResult{}, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) ValidateCredentialLifecycle(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	action model.EventAction,
) (*datasourcessvc.DataSource, error) {
	if f.ValidateCredentialLifecycleFn != nil {
		return f.ValidateCredentialLifecycleFn(ctx, orgSlug, dsSlug, actor, action)
	}
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) RecordCredentialLifecycleEvent(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	action model.EventAction,
) error {
	if f.RecordCredentialLifecycleEventFn != nil {
		return f.RecordCredentialLifecycleEventFn(ctx, orgSlug, dsSlug, actor, action)
	}
	return errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) ListBranches(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) ([]datasourcessvc.DataSourceBranch, error) {
	if f.ListBranchesFn != nil {
		return f.ListBranchesFn(ctx, orgSlug, dsSlug)
	}
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) IntrospectSchema(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (datasourcessvc.SchemaResult, error) {
	if f.IntrospectSchemaFn != nil {
		return f.IntrospectSchemaFn(ctx, orgSlug, dsSlug)
	}
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) RefreshSchema(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
) (*datasourcessvc.SchemaImportResult, error) {
	if f.RefreshSchemaFn != nil {
		return f.RefreshSchemaFn(ctx, orgSlug, dsSlug, actor)
	}
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) ExecuteQueuedSchemaImport(
	context.Context,
	jobs.DataSourceSchemaImportArgs,
) (*datasourcessvc.SchemaImportResult, error) {
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) Delete(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.DeleteFn != nil {
		return f.DeleteFn(ctx, orgSlug, dsSlug, actor)
	}
	return nil
}

func (f *fakeDataSourcesService) Restore(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.RestoreFn != nil {
		return f.RestoreFn(ctx, orgSlug, dsSlug, actor)
	}
	return nil
}

func (f *fakeDataSourcesService) ResolveGitAuth(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (*datasourcessvc.GitDataSourceAuth, error) {
	if f.ResolveGitAuthFn != nil {
		return f.ResolveGitAuthFn(ctx, orgSlug, dsSlug)
	}
	return nil, errFakeDataSourcesServiceFnNotSet
}

func (f *fakeDataSourcesService) ResolveGitExecutionAuth(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (*datasourcessvc.GitExecutionAuth, error) {
	if f.ResolveGitExecutionAuthFn != nil {
		return f.ResolveGitExecutionAuthFn(ctx, orgSlug, dsSlug)
	}
	return nil, errFakeDataSourcesServiceFnNotSet
}

type fakeDataSourceRefsService struct {
	CreateDataSourceRefFn             func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.NewDataSourceRef, model.EventActor) (*model.DataSourceRef, error)
	GetDataSourceRefFn                func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.DataSourceRef, error)
	ListDataSourceRefsFn              func(context.Context, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[model.DataSourceRef], error)
	UpdateDataSourceRefFn             func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, model.UpdateDataSourceRef, model.EventActor) (*model.DataSourceRef, error)
	DeleteDataSourceRefFn             func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, model.EventActor) error
	UpsertEnvironmentBindingFn        func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, slug.Slug, model.UpsertDataSourceBinding, model.EventActor) (*model.DataSourceBinding, error)
	GetEnvironmentBindingFn           func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.DataSourceBinding, error)
	ListEnvironmentBindingsFn         func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[model.DataSourceBinding], error)
	DeleteEnvironmentBindingFn        func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, slug.Slug, *slug.Slug, model.EventActor) error
	ResolvePipelineDataSourceRefsFn   func(context.Context, slug.Slug, slug.Slug, slug.Slug, *pipelinespec.Pipeline) (*datasourcerefssvc.ResolvedPipeline, error)
	SyncPipelineDataSourceRefsFn      func(context.Context, slug.Slug, slug.Slug, *pipelinespec.Pipeline, model.EventActor) (*datasourcerefssvc.PipelineDataSourceRefSyncResult, error)
	DeleteDataSourceRefsForPipelineFn func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.EventActor) error
}

func (f *fakeDataSourceRefsService) CreateDataSourceRef(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	newRef model.NewDataSourceRef,
	actor model.EventActor,
) (*model.DataSourceRef, error) {
	if f.CreateDataSourceRefFn != nil {
		return f.CreateDataSourceRefFn(ctx, orgSlug, projectSlug, pipelineSlug, newRef, actor)
	}
	return nil, errFakeDataSourceRefsServiceFnNotSet
}

func (f *fakeDataSourceRefsService) GetDataSourceRef(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, refSlug slug.Slug,
) (*model.DataSourceRef, error) {
	if f.GetDataSourceRefFn != nil {
		return f.GetDataSourceRefFn(ctx, orgSlug, projectSlug, pipelineSlug, refSlug)
	}
	return nil, errFakeDataSourceRefsServiceFnNotSet
}

func (f *fakeDataSourceRefsService) ListDataSourceRefs(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.DataSourceRef], error) {
	if f.ListDataSourceRefsFn != nil {
		return f.ListDataSourceRefsFn(ctx, orgSlug, projectSlug, req)
	}
	return model.ListResponse[model.DataSourceRef]{Items: []model.DataSourceRef{}}, nil
}

func (f *fakeDataSourceRefsService) UpdateDataSourceRef(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, refSlug slug.Slug,
	update model.UpdateDataSourceRef,
	actor model.EventActor,
) (*model.DataSourceRef, error) {
	if f.UpdateDataSourceRefFn != nil {
		return f.UpdateDataSourceRefFn(ctx, orgSlug, projectSlug, pipelineSlug, refSlug, update, actor)
	}
	return nil, errFakeDataSourceRefsServiceFnNotSet
}

func (f *fakeDataSourceRefsService) DeleteDataSourceRef(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, refSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.DeleteDataSourceRefFn != nil {
		return f.DeleteDataSourceRefFn(ctx, orgSlug, projectSlug, pipelineSlug, refSlug, actor)
	}
	return errFakeDataSourceRefsServiceFnNotSet
}

func (f *fakeDataSourceRefsService) UpsertEnvironmentBinding(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug slug.Slug,
	binding model.UpsertDataSourceBinding,
	actor model.EventActor,
) (*model.DataSourceBinding, error) {
	if f.UpsertEnvironmentBindingFn != nil {
		return f.UpsertEnvironmentBindingFn(ctx, orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, binding, actor)
	}
	return nil, errFakeDataSourceRefsServiceFnNotSet
}

func (f *fakeDataSourceRefsService) GetEnvironmentBinding(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug slug.Slug,
) (*model.DataSourceBinding, error) {
	if f.GetEnvironmentBindingFn != nil {
		return f.GetEnvironmentBindingFn(ctx, orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug)
	}
	return nil, errFakeDataSourceRefsServiceFnNotSet
}

func (f *fakeDataSourceRefsService) ListEnvironmentBindings(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.DataSourceBinding], error) {
	if f.ListEnvironmentBindingsFn != nil {
		return f.ListEnvironmentBindingsFn(ctx, orgSlug, projectSlug, environmentSlug, req)
	}
	return model.ListResponse[model.DataSourceBinding]{Items: []model.DataSourceBinding{}}, nil
}

func (f *fakeDataSourceRefsService) DeleteEnvironmentBinding(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug slug.Slug,
	confirmation *slug.Slug,
	actor model.EventActor,
) error {
	if f.DeleteEnvironmentBindingFn != nil {
		return f.DeleteEnvironmentBindingFn(ctx, orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, confirmation, actor)
	}
	return errFakeDataSourceRefsServiceFnNotSet
}

func (f *fakeDataSourceRefsService) ResolvePipelineDataSourceRefs(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
) (*datasourcerefssvc.ResolvedPipeline, error) {
	if f.ResolvePipelineDataSourceRefsFn != nil {
		return f.ResolvePipelineDataSourceRefsFn(ctx, orgSlug, projectSlug, environmentSlug, pipeline)
	}
	return &datasourcerefssvc.ResolvedPipeline{Pipeline: pipeline}, nil
}

func (f *fakeDataSourceRefsService) SyncPipelineDataSourceRefs(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	actor model.EventActor,
) (*datasourcerefssvc.PipelineDataSourceRefSyncResult, error) {
	if f.SyncPipelineDataSourceRefsFn != nil {
		return f.SyncPipelineDataSourceRefsFn(ctx, orgSlug, projectSlug, pipeline, actor)
	}
	return &datasourcerefssvc.PipelineDataSourceRefSyncResult{
		Items: []datasourcerefssvc.PipelineDataSourceRefSyncItem{},
	}, nil
}

func (f *fakeDataSourceRefsService) ReconcilePipelineDataSourceRefs(
	ctx context.Context,
	orgSlug, projectSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
	actor model.EventActor,
) (*datasourcerefssvc.PipelineDataSourceRefSyncResult, error) {
	return f.SyncPipelineDataSourceRefs(ctx, orgSlug, projectSlug, pipeline, actor)
}

func (f *fakeDataSourceRefsService) DeleteDataSourceRefsForPipeline(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.DeleteDataSourceRefsForPipelineFn != nil {
		return f.DeleteDataSourceRefsForPipelineFn(ctx, orgSlug, projectSlug, pipelineSlug, actor)
	}
	return errFakeDataSourceRefsServiceFnNotSet
}

type fakePipelineTriggersService struct {
	CreatePipelineTriggerFn               func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.NewPipelineTrigger, model.EventActor) (*model.PipelineTrigger, error)
	GetPipelineTriggerFn                  func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.PipelineTrigger, error)
	ListPipelineTriggersFn                func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[model.PipelineTrigger], error)
	ListPipelineScheduleFiresFn           func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[model.PipelineScheduleFire], error)
	ListPipelineRunTriggerRetryAttemptsFn func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) ([]model.PipelineTriggerRetryAttempt, error)
	UpdatePipelineTriggerFn               func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, model.UpdatePipelineTrigger, model.EventActor) (*model.PipelineTrigger, error)
	DeletePipelineTriggerFn               func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, model.EventActor) error
	RotatePipelineWebhookSecretFn         func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, model.EventActor) (model.PipelineWebhookSecret, error)
}

func (f *fakePipelineTriggersService) CreatePipelineTrigger(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	newTrigger model.NewPipelineTrigger,
	actor model.EventActor,
) (*model.PipelineTrigger, error) {
	if f.CreatePipelineTriggerFn != nil {
		return f.CreatePipelineTriggerFn(ctx, orgSlug, projectSlug, environmentSlug, newTrigger, actor)
	}
	return nil, errFakePipelineTriggersServiceFnNotSet
}

func (f *fakePipelineTriggersService) GetPipelineTrigger(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
) (*model.PipelineTrigger, error) {
	if f.GetPipelineTriggerFn != nil {
		return f.GetPipelineTriggerFn(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug)
	}
	return nil, errFakePipelineTriggersServiceFnNotSet
}

func (f *fakePipelineTriggersService) ListPipelineTriggers(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.PipelineTrigger], error) {
	if f.ListPipelineTriggersFn != nil {
		return f.ListPipelineTriggersFn(ctx, orgSlug, projectSlug, environmentSlug, req)
	}
	return model.ListResponse[model.PipelineTrigger]{Items: []model.PipelineTrigger{}}, nil
}

func (f *fakePipelineTriggersService) ListPipelineScheduleFires(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[model.PipelineScheduleFire], error) {
	if f.ListPipelineScheduleFiresFn != nil {
		return f.ListPipelineScheduleFiresFn(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, req)
	}
	return model.ListResponse[model.PipelineScheduleFire]{Items: []model.PipelineScheduleFire{}}, nil
}

func (f *fakePipelineTriggersService) ListPipelineRunTriggerRetryAttempts(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, runSlug slug.Slug,
) ([]model.PipelineTriggerRetryAttempt, error) {
	if f.ListPipelineRunTriggerRetryAttemptsFn != nil {
		return f.ListPipelineRunTriggerRetryAttemptsFn(ctx, orgSlug, projectSlug, environmentSlug, runSlug)
	}
	return []model.PipelineTriggerRetryAttempt{}, nil
}

func (f *fakePipelineTriggersService) UpdatePipelineTrigger(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	update model.UpdatePipelineTrigger,
	actor model.EventActor,
) (*model.PipelineTrigger, error) {
	if f.UpdatePipelineTriggerFn != nil {
		return f.UpdatePipelineTriggerFn(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, update, actor)
	}
	return nil, errFakePipelineTriggersServiceFnNotSet
}

func (f *fakePipelineTriggersService) DeletePipelineTrigger(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.DeletePipelineTriggerFn != nil {
		return f.DeletePipelineTriggerFn(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, actor)
	}
	return errFakePipelineTriggersServiceFnNotSet
}

func (f *fakePipelineTriggersService) RotatePipelineWebhookSecret(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, triggerSlug slug.Slug,
	actor model.EventActor,
) (model.PipelineWebhookSecret, error) {
	if f.RotatePipelineWebhookSecretFn != nil {
		return f.RotatePipelineWebhookSecretFn(ctx, orgSlug, projectSlug, environmentSlug, triggerSlug, actor)
	}
	return model.PipelineWebhookSecret{}, errFakePipelineTriggersServiceFnNotSet
}

type fakeQueriesService struct {
	queriessvc.Service
	CreateFn                   func(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, params *queriessvc.CreateParams) (*queriessvc.Query, error)
	FindBySlugFn               func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug) (*queriessvc.Query, error)
	GetExpectedInputContractFn func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug) (*payload.Contract, error)
	ListExecutionsFn           func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, req model.ListRequest) (model.ListResponse[*queriessvc.QueryExecution], error)
	GetExecutionFn             func(ctx context.Context, orgSlug, dsSlug, querySlug, executionSlug slug.Slug) (*queriessvc.QueryExecution, error)
	GetExecutionSummaryFn      func(context.Context, slug.Slug) (*queriessvc.QueryExecutionIdentity, error)
	ListByDataSourceFn         func(ctx context.Context, orgSlug, dsSlug slug.Slug, req model.ListRequest, includeDeleted bool) (model.ListResponse[*queriessvc.Query], error)
	UpdateFn                   func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor, params *queriessvc.UpdateParams) (*queriessvc.Query, error)
	DeleteFn                   func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor) error
	RestoreFn                  func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor) error
	PurgeFn                    func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor) error
	ExecuteFn                  func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, actor model.EventActor, paramValues map[string]any) (*result.ExecutionResult, error)
	ExecuteInlineFn            func(ctx context.Context, orgSlug, dsSlug slug.Slug, operationInline, paramValues map[string]any) (*result.ExecutionResult, error)
	ExecuteIncrementalFn       func(ctx context.Context, orgSlug, dsSlug, querySlug slug.Slug, paramValues map[string]any, checkpoint *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)
	ExecuteInlineIncrementalFn func(ctx context.Context, orgSlug, dsSlug slug.Slug, operationInline, paramValues map[string]any, checkpoint *pipelinerun.IncrementalCheckpointInput) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error)
}

func (f *fakeQueriesService) Create(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	params *queriessvc.CreateParams,
) (*queriessvc.Query, error) {
	if f.CreateFn != nil {
		return f.CreateFn(ctx, orgSlug, dsSlug, actor, params)
	}
	return nil, errFakeQueriesServiceFnNotSet
}

func (f *fakeQueriesService) FindBySlug(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
) (*queriessvc.Query, error) {
	if f.FindBySlugFn != nil {
		return f.FindBySlugFn(ctx, orgSlug, dsSlug, querySlug)
	}
	return nil, queriessvc.ErrQueryNotFound
}

func (f *fakeQueriesService) GetExpectedInputContract(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
) (*payload.Contract, error) {
	if f.GetExpectedInputContractFn != nil {
		return f.GetExpectedInputContractFn(ctx, orgSlug, dsSlug, querySlug)
	}
	return &payload.Contract{}, nil
}

func (f *fakeQueriesService) ListExecutions(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[*queriessvc.QueryExecution], error) {
	if f.ListExecutionsFn != nil {
		return f.ListExecutionsFn(ctx, orgSlug, dsSlug, querySlug, req)
	}
	return model.ListResponse[*queriessvc.QueryExecution]{
		Items:      []*queriessvc.QueryExecution{},
		HasMore:    false,
		NextCursor: nil,
	}, nil
}

func (f *fakeQueriesService) GetExecution(
	ctx context.Context,
	orgSlug, dsSlug, querySlug, executionSlug slug.Slug,
) (*queriessvc.QueryExecution, error) {
	if f.GetExecutionFn != nil {
		return f.GetExecutionFn(ctx, orgSlug, dsSlug, querySlug, executionSlug)
	}
	return nil, errFakeQueriesServiceFnNotSet
}

func (f *fakeQueriesService) GetExecutionSummary(
	ctx context.Context,
	orgSlug slug.Slug,
) (*queriessvc.QueryExecutionIdentity, error) {
	if f.GetExecutionSummaryFn != nil {
		return f.GetExecutionSummaryFn(ctx, orgSlug)
	}
	return nil, errFakeQueriesServiceFnNotSet
}

func (f *fakeQueriesService) ListByDataSource(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	req model.ListRequest,
	includeDeleted bool,
) (model.ListResponse[*queriessvc.Query], error) {
	if f.ListByDataSourceFn != nil {
		return f.ListByDataSourceFn(ctx, orgSlug, dsSlug, req, includeDeleted)
	}
	return model.ListResponse[*queriessvc.Query]{
		Items:      []*queriessvc.Query{},
		HasMore:    false,
		NextCursor: nil,
	}, nil
}

func (f *fakeQueriesService) Update(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	actor model.EventActor,
	params *queriessvc.UpdateParams,
) (*queriessvc.Query, error) {
	if f.UpdateFn != nil {
		return f.UpdateFn(ctx, orgSlug, dsSlug, querySlug, actor, params)
	}
	return nil, errFakeQueriesServiceFnNotSet
}

func (f *fakeQueriesService) Delete(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	actor model.EventActor,
) error {
	if f.DeleteFn != nil {
		return f.DeleteFn(ctx, orgSlug, dsSlug, querySlug, actor)
	}
	return nil
}

func (f *fakeQueriesService) Restore(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	actor model.EventActor,
) error {
	if f.RestoreFn != nil {
		return f.RestoreFn(ctx, orgSlug, dsSlug, querySlug, actor)
	}
	return nil
}

func (f *fakeQueriesService) Purge(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	actor model.EventActor,
) error {
	if f.PurgeFn != nil {
		return f.PurgeFn(ctx, orgSlug, dsSlug, querySlug, actor)
	}
	return nil
}

func (f *fakeQueriesService) Execute(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	actor model.EventActor,
	paramValues map[string]any,
	_ ...queriessvc.ExecuteOption,
) (*result.ExecutionResult, error) {
	if f.ExecuteFn != nil {
		return f.ExecuteFn(ctx, orgSlug, dsSlug, querySlug, actor, paramValues)
	}
	return nil, errFakeQueriesServiceFnNotSet
}

func (f *fakeQueriesService) ExecuteInline(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
) (*result.ExecutionResult, error) {
	if f.ExecuteInlineFn != nil {
		return f.ExecuteInlineFn(ctx, orgSlug, dsSlug, operationInline, paramValues)
	}
	return nil, errFakeQueriesServiceFnNotSet
}

func (f *fakeQueriesService) ExecuteIncremental(
	ctx context.Context,
	orgSlug, dsSlug, querySlug slug.Slug,
	paramValues map[string]any,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	if f.ExecuteIncrementalFn != nil {
		return f.ExecuteIncrementalFn(ctx, orgSlug, dsSlug, querySlug, paramValues, checkpoint)
	}
	return nil, nil, errFakeQueriesServiceFnNotSet
}

func (f *fakeQueriesService) ExecuteInlineIncremental(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	operationInline map[string]any,
	paramValues map[string]any,
	checkpoint *pipelinerun.IncrementalCheckpointInput,
) (*result.ExecutionResult, *pipelinerun.IncrementalCheckpointObservation, error) {
	if f.ExecuteInlineIncrementalFn != nil {
		return f.ExecuteInlineIncrementalFn(ctx, orgSlug, dsSlug, operationInline, paramValues, checkpoint)
	}
	return nil, nil, errFakeQueriesServiceFnNotSet
}

type fakeEventsService struct {
	CreateEventFn func(ctx context.Context, params eventssvc.CreateEventParams) (*model.Event, error)
	GetEventFn    func(ctx context.Context, orgSlug, eventSlug slug.Slug) (*model.Event, error)
	ListEventsFn  func(
		ctx context.Context,
		orgSlug slug.Slug,
		filters eventssvc.ListEventsFilters,
		req model.ListRequest,
	) (model.ListResponse[*model.Event], error)
	ListSourceMetadataFn func(
		ctx context.Context,
		orgSlug slug.Slug,
		filters eventssvc.SourceMetadataFilters,
	) (model.EventSourceMetadataResponse, error)
	PruneExpiredEventsFn func(ctx context.Context, retention time.Duration) (int64, error)
}

func (f *fakeEventsService) CreateEvent(
	ctx context.Context,
	params eventssvc.CreateEventParams,
) (*model.Event, error) {
	if f.CreateEventFn != nil {
		return f.CreateEventFn(ctx, params)
	}
	return nil, errFakeEventsServiceFnNotSet
}

func (f *fakeEventsService) GetEvent(
	ctx context.Context,
	orgSlug, eventSlug slug.Slug,
) (*model.Event, error) {
	if f.GetEventFn != nil {
		return f.GetEventFn(ctx, orgSlug, eventSlug)
	}
	return nil, errFakeEventsServiceFnNotSet
}

func (f *fakeEventsService) ListEvents(
	ctx context.Context,
	orgSlug slug.Slug,
	filters eventssvc.ListEventsFilters,
	req model.ListRequest,
) (model.ListResponse[*model.Event], error) {
	if f.ListEventsFn != nil {
		return f.ListEventsFn(ctx, orgSlug, filters, req)
	}
	return model.ListResponse[*model.Event]{}, errFakeEventsServiceFnNotSet
}

func (f *fakeEventsService) ListSourceMetadata(
	ctx context.Context,
	orgSlug slug.Slug,
	filters eventssvc.SourceMetadataFilters,
) (model.EventSourceMetadataResponse, error) {
	if f.ListSourceMetadataFn != nil {
		return f.ListSourceMetadataFn(ctx, orgSlug, filters)
	}
	return model.EventSourceMetadataResponse{}, errFakeEventsServiceFnNotSet
}

func (f *fakeEventsService) PruneExpiredEvents(ctx context.Context, retention time.Duration) (int64, error) {
	if f.PruneExpiredEventsFn != nil {
		return f.PruneExpiredEventsFn(ctx, retention)
	}
	return 0, errFakeEventsServiceFnNotSet
}

type fakeNotificationsService struct {
	ListAlertDestinationsFn func(
		ctx context.Context,
		principal notificationsvc.AlertManagementPrincipal,
		orgSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[*model.AlertDestination], error)
	GetAlertDestinationFn func(
		ctx context.Context,
		principal notificationsvc.AlertManagementPrincipal,
		orgSlug slug.Slug,
		destinationSlug slug.Slug,
	) (*model.AlertDestination, error)
	TestAlertDestinationFn func(
		ctx context.Context,
		principal notificationsvc.AlertManagementPrincipal,
		params notificationsvc.TestAlertDestinationParams,
	) (*notificationsvc.AlertDestinationTestResult, error)
	CreateAlertDestinationFn func(
		ctx context.Context,
		principal notificationsvc.AlertManagementPrincipal,
		params notificationsvc.CreateAlertDestinationParams,
	) (*model.AlertDestination, error)
	UpdateAlertDestinationFn func(
		ctx context.Context,
		principal notificationsvc.AlertManagementPrincipal,
		params notificationsvc.UpdateAlertDestinationParams,
	) (*model.AlertDestination, error)
	DeleteAlertDestinationFn func(
		ctx context.Context,
		principal notificationsvc.AlertManagementPrincipal,
		orgSlug slug.Slug,
		destinationSlug slug.Slug,
	) error
	ListAlertPoliciesFn func(
		ctx context.Context,
		principal notificationsvc.AlertManagementPrincipal,
		orgSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[*model.AlertPolicy], error)
	GetAlertPolicyFn func(
		ctx context.Context,
		principal notificationsvc.AlertManagementPrincipal,
		orgSlug slug.Slug,
		policySlug slug.Slug,
	) (*model.AlertPolicy, error)
	CreateAlertPolicyFn func(
		ctx context.Context,
		principal notificationsvc.AlertManagementPrincipal,
		params notificationsvc.CreateAlertPolicyParams,
	) (*model.AlertPolicy, error)
	UpdateAlertPolicyFn func(
		ctx context.Context,
		principal notificationsvc.AlertManagementPrincipal,
		params notificationsvc.UpdateAlertPolicyParams,
	) (*model.AlertPolicy, error)
	DeleteAlertPolicyFn func(
		ctx context.Context,
		principal notificationsvc.AlertManagementPrincipal,
		orgSlug slug.Slug,
		policySlug slug.Slug,
	) error
	ListNotificationsFn func(
		ctx context.Context,
		orgSlug slug.Slug,
		filters notificationsvc.ListNotificationsFilters,
		req model.ListRequest,
	) (model.ListResponse[*model.NotificationListItem], error)
	GetNotificationFn func(
		ctx context.Context,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
	) (*model.NotificationListItem, error)
	AcknowledgeNotificationFn func(
		ctx context.Context,
		principal notificationsvc.AlertManagementPrincipal,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
	) (*model.NotificationListItem, error)
	ResolveNotificationFn func(
		ctx context.Context,
		principal notificationsvc.AlertManagementPrincipal,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
	) (*model.NotificationListItem, error)
	ListAlertDeliveryAttemptsFn func(
		ctx context.Context,
		orgSlug slug.Slug,
		recordSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[*model.AlertDeliveryAttempt], error)
	ListRecentAlertDeliveryAttemptsForRecordsFn func(
		ctx context.Context,
		orgSlug slug.Slug,
		recordSlugs []slug.Slug,
		limitPerRecord int,
	) ([]*model.AlertDeliveryAttempt, error)
}

func (f *fakeNotificationsService) CreateAlertDestination(
	ctx context.Context,
	principal notificationsvc.AlertManagementPrincipal,
	params notificationsvc.CreateAlertDestinationParams,
) (*model.AlertDestination, error) {
	if f.CreateAlertDestinationFn != nil {
		return f.CreateAlertDestinationFn(ctx, principal, params)
	}
	return nil, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) ListAlertDestinations(
	ctx context.Context,
	principal notificationsvc.AlertManagementPrincipal,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[*model.AlertDestination], error) {
	if f.ListAlertDestinationsFn != nil {
		return f.ListAlertDestinationsFn(ctx, principal, orgSlug, req)
	}
	return model.ListResponse[*model.AlertDestination]{}, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) GetAlertDestination(
	ctx context.Context,
	principal notificationsvc.AlertManagementPrincipal,
	orgSlug slug.Slug,
	destinationSlug slug.Slug,
) (*model.AlertDestination, error) {
	if f.GetAlertDestinationFn != nil {
		return f.GetAlertDestinationFn(ctx, principal, orgSlug, destinationSlug)
	}
	return nil, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) TestAlertDestination(
	ctx context.Context,
	principal notificationsvc.AlertManagementPrincipal,
	params notificationsvc.TestAlertDestinationParams,
) (*notificationsvc.AlertDestinationTestResult, error) {
	if f.TestAlertDestinationFn != nil {
		return f.TestAlertDestinationFn(ctx, principal, params)
	}
	return nil, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) UpdateAlertDestination(
	ctx context.Context,
	principal notificationsvc.AlertManagementPrincipal,
	params notificationsvc.UpdateAlertDestinationParams,
) (*model.AlertDestination, error) {
	if f.UpdateAlertDestinationFn != nil {
		return f.UpdateAlertDestinationFn(ctx, principal, params)
	}
	return nil, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) DeleteAlertDestination(
	ctx context.Context,
	principal notificationsvc.AlertManagementPrincipal,
	orgSlug, destinationSlug slug.Slug,
) error {
	if f.DeleteAlertDestinationFn != nil {
		return f.DeleteAlertDestinationFn(ctx, principal, orgSlug, destinationSlug)
	}
	return errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) CreateAlertPolicy(
	ctx context.Context,
	principal notificationsvc.AlertManagementPrincipal,
	params notificationsvc.CreateAlertPolicyParams,
) (*model.AlertPolicy, error) {
	if f.CreateAlertPolicyFn != nil {
		return f.CreateAlertPolicyFn(ctx, principal, params)
	}
	return nil, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) ListAlertPolicies(
	ctx context.Context,
	principal notificationsvc.AlertManagementPrincipal,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[*model.AlertPolicy], error) {
	if f.ListAlertPoliciesFn != nil {
		return f.ListAlertPoliciesFn(ctx, principal, orgSlug, req)
	}
	return model.ListResponse[*model.AlertPolicy]{}, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) GetAlertPolicy(
	ctx context.Context,
	principal notificationsvc.AlertManagementPrincipal,
	orgSlug slug.Slug,
	policySlug slug.Slug,
) (*model.AlertPolicy, error) {
	if f.GetAlertPolicyFn != nil {
		return f.GetAlertPolicyFn(ctx, principal, orgSlug, policySlug)
	}
	return nil, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) UpdateAlertPolicy(
	ctx context.Context,
	principal notificationsvc.AlertManagementPrincipal,
	params notificationsvc.UpdateAlertPolicyParams,
) (*model.AlertPolicy, error) {
	if f.UpdateAlertPolicyFn != nil {
		return f.UpdateAlertPolicyFn(ctx, principal, params)
	}
	return nil, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) DeleteAlertPolicy(
	ctx context.Context,
	principal notificationsvc.AlertManagementPrincipal,
	orgSlug slug.Slug,
	policySlug slug.Slug,
) error {
	if f.DeleteAlertPolicyFn != nil {
		return f.DeleteAlertPolicyFn(ctx, principal, orgSlug, policySlug)
	}
	return errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) CreateCandidate(
	context.Context,
	notificationsvc.CreateCandidateParams,
) (*notificationsvc.CreateCandidateResult, error) {
	return nil, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) UpsertRecord(
	context.Context,
	notificationsvc.UpsertRecordParams,
) (*model.NotificationRecord, error) {
	return nil, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) ListNotifications(
	ctx context.Context,
	orgSlug slug.Slug,
	filters notificationsvc.ListNotificationsFilters,
	req model.ListRequest,
) (model.ListResponse[*model.NotificationListItem], error) {
	if f.ListNotificationsFn != nil {
		return f.ListNotificationsFn(ctx, orgSlug, filters, req)
	}
	return model.ListResponse[*model.NotificationListItem]{}, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) GetNotification(
	ctx context.Context,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
) (*model.NotificationListItem, error) {
	if f.GetNotificationFn != nil {
		return f.GetNotificationFn(ctx, orgSlug, recordSlug)
	}
	return nil, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) AcknowledgeNotification(
	ctx context.Context,
	principal notificationsvc.AlertManagementPrincipal,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
) (*model.NotificationListItem, error) {
	if f.AcknowledgeNotificationFn != nil {
		return f.AcknowledgeNotificationFn(ctx, principal, orgSlug, recordSlug)
	}
	return nil, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) ResolveNotification(
	ctx context.Context,
	principal notificationsvc.AlertManagementPrincipal,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
) (*model.NotificationListItem, error) {
	if f.ResolveNotificationFn != nil {
		return f.ResolveNotificationFn(ctx, principal, orgSlug, recordSlug)
	}
	return nil, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) ListAlertDeliveryAttempts(
	ctx context.Context,
	orgSlug slug.Slug,
	recordSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[*model.AlertDeliveryAttempt], error) {
	if f.ListAlertDeliveryAttemptsFn != nil {
		return f.ListAlertDeliveryAttemptsFn(ctx, orgSlug, recordSlug, req)
	}
	return model.ListResponse[*model.AlertDeliveryAttempt]{}, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) ListRecentAlertDeliveryAttemptsForRecords(
	ctx context.Context,
	orgSlug slug.Slug,
	recordSlugs []slug.Slug,
	limitPerRecord int,
) ([]*model.AlertDeliveryAttempt, error) {
	if f.ListRecentAlertDeliveryAttemptsForRecordsFn != nil {
		return f.ListRecentAlertDeliveryAttemptsForRecordsFn(ctx, orgSlug, recordSlugs, limitPerRecord)
	}
	return nil, errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) DeliverNotification(
	context.Context,
	notificationsvc.DeliverNotificationParams,
) error {
	return errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) MarkAlertDeliveryAttemptSent(
	context.Context,
	notificationsvc.MarkAlertDeliveryAttemptSentParams,
) error {
	return errFakeNotificationsServiceFnNotSet
}

func (f *fakeNotificationsService) MarkAlertDeliveryAttemptFailed(
	context.Context,
	notificationsvc.MarkAlertDeliveryAttemptFailedParams,
) error {
	return errFakeNotificationsServiceFnNotSet
}

type fakeAuthService struct {
	CanFn func(ctx context.Context, input authorization.Input) (bool, error)
}

type fakePipelinesService struct {
	GetRunProgressFn       func(context.Context, slug.Slug, slug.Slug, slug.Slug, string, int64, int) (*pipelinesvc.RunProgress, error)
	GetRunCompletionFn     func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*model.PipelineRunCompletion, error)
	BootstrapCheckpointsFn func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, slug.Slug, *slug.Slug, model.EventActor, map[string]any, map[slug.Slug]json.RawMessage) error
	StartBackfillFn        func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, *slug.Slug, *slug.Slug, model.EventActor, pipelinerun.RunSourceSummary, map[string]any, map[slug.Slug]pipelinerun.IncrementalSourceWindow) (*pipelinesvc.PipelineRunDetail, error)

	recoverRunFn func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, *slug.Slug, model.EventActor, pipelinerun.RunSourceSummary) (*pipelinesvc.PipelineRunDetail, error)
	ListFn       func(
		ctx context.Context,
		orgSlug, projectSlug, memberSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[*pipelinesvc.PipelineSummary], error)
	GetFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	) (*pipelinesvc.PipelineDetail, error)
	GetDraftFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	) (*pipelinesvc.PipelineDraftDetail, error)
	SaveDraftFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
		actor model.EventActor,
		pipeline *pipelinespec.Pipeline,
		expectedSpecHash *string,
	) (*pipelinesvc.PipelineDraftDetail, error)
	PublishDraftFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
		actor model.EventActor,
		expectedSpecHash *string,
		force bool,
	) (*pipelinesvc.PipelineDetail, error)
	RefreshDraftProposalFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
		actor model.EventActor,
	) (*pipelinesvc.PipelineDetail, error)
	DeleteDraftFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
		actor model.EventActor,
	) error
	ListVersionsFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
	) ([]*pipelinesvc.PipelineVersionSummary, error)
	GetVersionFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		specHash string,
	) (*pipelinesvc.PipelineVersionDetail, error)
	ListDeploymentsFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
	) ([]*pipelinesvc.PipelineDeploymentDetail, error)
	GetDeploymentFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	) (*pipelinesvc.PipelineDeploymentDetail, error)
	DeployFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
		actor model.EventActor,
		req pipelinesvc.DeployPipelineRequest,
	) (*pipelinesvc.PipelineDeploymentDetail, error)
	UpdateFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
		actor model.EventActor,
		pipeline *pipelinespec.Pipeline,
	) (*pipelinesvc.PipelineDetail, error)
	ReconcilePipelinesFn func(
		ctx context.Context,
		orgSlug, projectSlug, memberSlug slug.Slug,
		actor model.EventActor,
		req pipelinesvc.ReconcilePipelinesRequest,
	) (*pipelinesvc.ReconcilePipelinesResult, error)
	ValidateFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug, memberSlug slug.Slug,
		pipeline *pipelinespec.Pipeline,
	) (*pipelinesvc.PipelineValidationResult, error)
	RecordValidationResultFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		principal pipelinesvc.ValidationHistoryPrincipal,
		source pipelinesvc.PipelineValidationResultSource,
		pipeline *pipelinespec.Pipeline,
		result *pipelinesvc.PipelineValidationResult,
	) (*pipelinesvc.PipelineValidationResultRecord, error)
	ListValidationResultsFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		principal pipelinesvc.ValidationHistoryPrincipal,
		filters pipelinesvc.PipelineValidationResultFilters,
		req model.ListRequest,
	) (model.ListResponse[*pipelinesvc.PipelineValidationResultRecord], error)
	GetValidationResultFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		principal pipelinesvc.ValidationHistoryPrincipal,
		resultSlug slug.Slug,
	) (*pipelinesvc.PipelineValidationResultRecord, error)
	RevalidatePipelineFn func(
		ctx context.Context,
		orgSlug, projectSlug, environmentSlug slug.Slug,
		principal pipelinesvc.ValidationHistoryPrincipal,
		pipelineSlug, memberSlug slug.Slug,
	) (*pipelinesvc.PipelineValidationResultRecord, error)
	ListDriftImpactReportsFn func(
		ctx context.Context,
		orgSlug slug.Slug,
		filters pipelinesvc.DriftImpactReportFilters,
	) ([]*pipelinesvc.DriftImpactReport, error)
	GetDriftImpactReportFn func(
		ctx context.Context,
		orgSlug, projectSlug, reportSlug slug.Slug,
	) (*pipelinesvc.DriftImpactReport, error)
	DeleteFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		actor model.EventActor,
	) error
	RestoreFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		actor model.EventActor,
	) error
	PurgeFn func(
		ctx context.Context,
		orgSlug, projectSlug, pipelineSlug slug.Slug,
		actor model.EventActor,
	) error
	ExecuteFn func(
		ctx context.Context,
		orgSlug slug.Slug,
		projectSlug slug.Slug,
		environmentSlug slug.Slug,
		pipelineSlug slug.Slug,
		runSlug *slug.Slug,
		protectedEnvironmentConfirmation *slug.Slug,
		actor model.EventActor,
		source pipelinerun.RunSourceSummary,
		runtimeInput map[string]any,
	) (*pipelinesvc.PipelineRunDetail, error)
	StartRunFn func(
		ctx context.Context,
		orgSlug slug.Slug,
		projectSlug slug.Slug,
		environmentSlug slug.Slug,
		pipelineSlug slug.Slug,
		runSlug *slug.Slug,
		protectedEnvironmentConfirmation *slug.Slug,
		actor model.EventActor,
		source pipelinerun.RunSourceSummary,
		runtimeInput map[string]any,
	) (*pipelinesvc.PipelineRunDetail, error)
	ExecuteQueuedPipelineRunFn func(ctx context.Context, args jobs.PipelineRunExecutionArgs) error
	ExecuteVersionFn           func(
		ctx context.Context,
		orgSlug slug.Slug,
		projectSlug slug.Slug,
		environmentSlug slug.Slug,
		pipelineSlug slug.Slug,
		specHash string,
		runSlug *slug.Slug,
		protectedEnvironmentConfirmation *slug.Slug,
		actor model.EventActor,
		source pipelinerun.RunSourceSummary,
		runtimeInput map[string]any,
	) (*pipelinesvc.PipelineRunDetail, error)
	ListRunsFn func(
		ctx context.Context,
		orgSlug slug.Slug,
		projectSlug slug.Slug,
		environmentSlug slug.Slug,
		filters *pipelinesvc.RunFilters,
		req model.ListRequest,
	) (model.ListResponse[*pipelinesvc.PipelineRunMetadata], error)
	GetRunFn func(
		ctx context.Context,
		orgSlug slug.Slug,
		projectSlug slug.Slug,
		environmentSlug slug.Slug,
		runSlug string,
	) (*pipelinesvc.PipelineRunDetail, error)
}

func (*fakePipelinesService) AuthorizeProposalPublish(
	context.Context, jobs.PipelineGitPublishProposalArgs,
) error {
	return nil
}

func (f *fakePipelinesService) List(
	ctx context.Context,
	orgSlug, projectSlug, memberSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[*pipelinesvc.PipelineSummary], error) {
	if f.ListFn != nil {
		return f.ListFn(ctx, orgSlug, projectSlug, memberSlug, req)
	}

	return model.ListResponse[*pipelinesvc.PipelineSummary]{}, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) Get(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) (*pipelinesvc.PipelineDetail, error) {
	if f.GetFn != nil {
		return f.GetFn(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) GetDraft(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
) (*pipelinesvc.PipelineDraftDetail, error) {
	if f.GetDraftFn != nil {
		return f.GetDraftFn(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) SaveDraft(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	actor model.EventActor,
	pipeline *pipelinespec.Pipeline,
	expectedSpecHash *string,
) (*pipelinesvc.PipelineDraftDetail, error) {
	if f.SaveDraftFn != nil {
		return f.SaveDraftFn(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug, actor, pipeline, expectedSpecHash)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) PublishDraft(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	actor model.EventActor,
	expectedSpecHash *string,
	force bool,
) (*pipelinesvc.PipelineDetail, error) {
	if f.PublishDraftFn != nil {
		return f.PublishDraftFn(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug, actor, expectedSpecHash, force)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) DeleteDraft(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.DeleteDraftFn != nil {
		return f.DeleteDraftFn(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug, actor)
	}

	return errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) RefreshDraftProposal(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	actor model.EventActor,
) (*pipelinesvc.PipelineDetail, error) {
	if f.RefreshDraftProposalFn != nil {
		return f.RefreshDraftProposalFn(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug, actor)
	}
	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) ListVersions(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
) ([]*pipelinesvc.PipelineVersionSummary, error) {
	if f.ListVersionsFn != nil {
		return f.ListVersionsFn(ctx, orgSlug, projectSlug, pipelineSlug)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) GetVersion(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	specHash string,
) (*pipelinesvc.PipelineVersionDetail, error) {
	if f.GetVersionFn != nil {
		return f.GetVersionFn(ctx, orgSlug, projectSlug, pipelineSlug, specHash)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) ListDeployments(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
) ([]*pipelinesvc.PipelineDeploymentDetail, error) {
	if f.ListDeploymentsFn != nil {
		return f.ListDeploymentsFn(ctx, orgSlug, projectSlug, environmentSlug)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) GetDeployment(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
) (*pipelinesvc.PipelineDeploymentDetail, error) {
	if f.GetDeploymentFn != nil {
		return f.GetDeploymentFn(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) Deploy(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	actor model.EventActor,
	req pipelinesvc.DeployPipelineRequest,
) (*pipelinesvc.PipelineDeploymentDetail, error) {
	if f.DeployFn != nil {
		return f.DeployFn(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, actor, req)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) Update(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug, memberSlug slug.Slug,
	actor model.EventActor,
	pipeline *pipelinespec.Pipeline,
) (*pipelinesvc.PipelineDetail, error) {
	if f.UpdateFn != nil {
		return f.UpdateFn(ctx, orgSlug, projectSlug, pipelineSlug, memberSlug, actor, pipeline)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) ReconcilePipelines(
	ctx context.Context,
	orgSlug, projectSlug, memberSlug slug.Slug,
	actor model.EventActor,
	req pipelinesvc.ReconcilePipelinesRequest,
) (*pipelinesvc.ReconcilePipelinesResult, error) {
	if f.ReconcilePipelinesFn != nil {
		return f.ReconcilePipelinesFn(ctx, orgSlug, projectSlug, memberSlug, actor, req)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) Validate(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug, memberSlug slug.Slug,
	pipeline *pipelinespec.Pipeline,
) (*pipelinesvc.PipelineValidationResult, error) {
	if f.ValidateFn != nil {
		return f.ValidateFn(ctx, orgSlug, projectSlug, environmentSlug, memberSlug, pipeline)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) RecordValidationResult(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	principal pipelinesvc.ValidationHistoryPrincipal,
	source pipelinesvc.PipelineValidationResultSource,
	pipeline *pipelinespec.Pipeline,
	result *pipelinesvc.PipelineValidationResult,
) (*pipelinesvc.PipelineValidationResultRecord, error) {
	if f.RecordValidationResultFn != nil {
		return f.RecordValidationResultFn(ctx, orgSlug, projectSlug, environmentSlug, principal, source, pipeline, result)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) ListValidationResults(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	principal pipelinesvc.ValidationHistoryPrincipal,
	filters pipelinesvc.PipelineValidationResultFilters,
	req model.ListRequest,
) (model.ListResponse[*pipelinesvc.PipelineValidationResultRecord], error) {
	if f.ListValidationResultsFn != nil {
		return f.ListValidationResultsFn(ctx, orgSlug, projectSlug, environmentSlug, principal, filters, req)
	}

	return model.ListResponse[*pipelinesvc.PipelineValidationResultRecord]{}, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) GetValidationResult(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	principal pipelinesvc.ValidationHistoryPrincipal,
	resultSlug slug.Slug,
) (*pipelinesvc.PipelineValidationResultRecord, error) {
	if f.GetValidationResultFn != nil {
		return f.GetValidationResultFn(ctx, orgSlug, projectSlug, environmentSlug, principal, resultSlug)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) RevalidatePipeline(
	ctx context.Context,
	orgSlug, projectSlug, environmentSlug slug.Slug,
	principal pipelinesvc.ValidationHistoryPrincipal,
	pipelineSlug, memberSlug slug.Slug,
) (*pipelinesvc.PipelineValidationResultRecord, error) {
	if f.RevalidatePipelineFn != nil {
		return f.RevalidatePipelineFn(ctx, orgSlug, projectSlug, environmentSlug, principal, pipelineSlug, memberSlug)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) ListDriftImpactReports(
	ctx context.Context,
	orgSlug slug.Slug,
	filters pipelinesvc.DriftImpactReportFilters,
) ([]*pipelinesvc.DriftImpactReport, error) {
	if f.ListDriftImpactReportsFn != nil {
		return f.ListDriftImpactReportsFn(ctx, orgSlug, filters)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) GetDriftImpactReport(
	ctx context.Context,
	orgSlug, projectSlug, reportSlug slug.Slug,
) (*pipelinesvc.DriftImpactReport, error) {
	if f.GetDriftImpactReportFn != nil {
		return f.GetDriftImpactReportFn(ctx, orgSlug, projectSlug, reportSlug)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) Delete(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.DeleteFn != nil {
		return f.DeleteFn(ctx, orgSlug, projectSlug, pipelineSlug, actor)
	}

	return errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) Restore(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.RestoreFn != nil {
		return f.RestoreFn(ctx, orgSlug, projectSlug, pipelineSlug, actor)
	}

	return errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) Purge(
	ctx context.Context,
	orgSlug, projectSlug, pipelineSlug slug.Slug,
	actor model.EventActor,
) error {
	if f.PurgeFn != nil {
		return f.PurgeFn(ctx, orgSlug, projectSlug, pipelineSlug, actor)
	}

	return errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) Execute(
	ctx context.Context,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	environmentSlug slug.Slug,
	pipelineSlug slug.Slug,
	runSlug *slug.Slug,
	protectedEnvironmentConfirmation *slug.Slug,
	actor model.EventActor,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
) (*pipelinesvc.PipelineRunDetail, error) {
	if f.ExecuteFn != nil {
		return f.ExecuteFn(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, runSlug, protectedEnvironmentConfirmation, actor, source, runtimeInput)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) ExecuteVerifiedEvent(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	*slug.Slug,
	model.EventActor,
	pipelinerun.RunSourceSummary,
	map[string]any,
	json.RawMessage,
) (*pipelinesvc.PipelineRunDetail, error) {
	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) StartRun(
	ctx context.Context,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	environmentSlug slug.Slug,
	pipelineSlug slug.Slug,
	runSlug *slug.Slug,
	protectedEnvironmentConfirmation *slug.Slug,
	actor model.EventActor,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
) (*pipelinesvc.PipelineRunDetail, error) {
	if f.StartRunFn != nil {
		return f.StartRunFn(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, runSlug, protectedEnvironmentConfirmation, actor, source, runtimeInput)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) ExecuteQueuedPipelineRun(ctx context.Context, args jobs.PipelineRunExecutionArgs) error {
	if f.ExecuteQueuedPipelineRunFn != nil {
		return f.ExecuteQueuedPipelineRunFn(ctx, args)
	}

	return errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) ExecuteVersion(
	ctx context.Context,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	environmentSlug slug.Slug,
	pipelineSlug slug.Slug,
	specHash string,
	runSlug *slug.Slug,
	protectedEnvironmentConfirmation *slug.Slug,
	actor model.EventActor,
	source pipelinerun.RunSourceSummary,
	runtimeInput map[string]any,
) (*pipelinesvc.PipelineRunDetail, error) {
	if f.ExecuteVersionFn != nil {
		return f.ExecuteVersionFn(ctx, orgSlug, projectSlug, environmentSlug, pipelineSlug, specHash, runSlug, protectedEnvironmentConfirmation, actor, source, runtimeInput)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) ExecuteVerifiedEventVersion(
	context.Context,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	slug.Slug,
	string,
	*slug.Slug,
	model.EventActor,
	pipelinerun.RunSourceSummary,
	map[string]any,
	json.RawMessage,
) (*pipelinesvc.PipelineRunDetail, error) {
	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) ListRuns(
	ctx context.Context,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	environmentSlug slug.Slug,
	filters *pipelinesvc.RunFilters,
	req model.ListRequest,
) (model.ListResponse[*pipelinesvc.PipelineRunMetadata], error) {
	if f.ListRunsFn != nil {
		return f.ListRunsFn(ctx, orgSlug, projectSlug, environmentSlug, filters, req)
	}

	return model.ListResponse[*pipelinesvc.PipelineRunMetadata]{}, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) GetRun(
	ctx context.Context,
	orgSlug slug.Slug,
	projectSlug slug.Slug,
	environmentSlug slug.Slug,
	runSlug string,
) (*pipelinesvc.PipelineRunDetail, error) {
	if f.GetRunFn != nil {
		return f.GetRunFn(ctx, orgSlug, projectSlug, environmentSlug, runSlug)
	}

	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakeAuthService) Can(ctx context.Context, input authorization.Input) (bool, error) {
	if f.CanFn != nil {
		return f.CanFn(ctx, input)
	}
	return true, nil
}

type fakeOIDCService struct {
	GoogleDisabled         bool
	ExchangeCodeForTokenFn func(ctx context.Context, code, redirectURI string) (*oidc.User, error)
	GetAuthURLFn           func(state, redirectURI string) string
	GetAuthURLError        error
}

func (f *fakeOIDCService) Configured() bool { return !f.GoogleDisabled }

type fakeProofReceiptService struct {
	CompileFn func(context.Context, slug.Slug, slug.Slug) (*proofreceipts.Receipt, error)
	GetFn     func(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) (*proofreceiptsvc.ReceiptDetail, error)
	ListFn    func(context.Context, slug.Slug, slug.Slug, slug.Slug, model.ListRequest) (model.ListResponse[*proofreceiptsvc.ReceiptDetail], error)
}

func (f *fakeProofReceiptService) List(ctx context.Context, orgSlug, projectSlug, environmentSlug slug.Slug, req model.ListRequest) (model.ListResponse[*proofreceiptsvc.ReceiptDetail], error) {
	if f.ListFn == nil {
		return model.ListResponse[*proofreceiptsvc.ReceiptDetail]{}, errFakeProofReceiptServiceFnNotSet
	}
	return f.ListFn(ctx, orgSlug, projectSlug, environmentSlug, req)
}

func (f *fakeProofReceiptService) Compile(ctx context.Context, orgSlug, candidateSlug slug.Slug) (*proofreceipts.Receipt, error) {
	if f.CompileFn == nil {
		return nil, errFakeProofReceiptServiceFnNotSet
	}
	return f.CompileFn(ctx, orgSlug, candidateSlug)
}

func (f *fakeProofReceiptService) Get(ctx context.Context, orgSlug, projectSlug, environmentSlug, receiptSlug slug.Slug) (*proofreceiptsvc.ReceiptDetail, error) {
	if f.GetFn == nil {
		return nil, errFakeProofReceiptServiceFnNotSet
	}
	return f.GetFn(ctx, orgSlug, projectSlug, environmentSlug, receiptSlug)
}

func (f *fakeOIDCService) ExchangeCodeForToken(
	ctx context.Context,
	code, redirectURI string,
) (*oidc.User, error) {
	return f.ExchangeCodeForTokenFn(ctx, code, redirectURI)
}

func (f *fakeOIDCService) GetAuthURL(_ context.Context, state, redirectURI string) (string, error) {
	if f.GetAuthURLError != nil {
		return "", f.GetAuthURLError
	}
	if f.GetAuthURLFn != nil {
		return f.GetAuthURLFn(state, redirectURI), nil
	}
	return "https://accounts.google.com/o/oauth2/auth?state=" + state, nil
}

// --- Test app setup ---

type testServerConfig struct {
	githubAppService               *githubappsvc.Service
	runRecencyService              runRecencyService
	memberAdmission                memberAdmissionService
	compiledPlansService           compiledPlanService
	usersService                   userssvc.UsersService
	orgService                     orgsvc.Service
	admissionService               admissionsvc.Service
	projectsService                projectssvc.Service
	repositorySetupService         projectRepositorySetupService
	pipelineCreationsService       pipelineCreationService
	odcsContractsService           odcscontractsvc.Service
	environmentsService            environmentssvc.Service
	membersService                 memberssvc.Service
	rolesService                   rolessvc.Service
	apiKeysService                 apikeyssvc.Service
	patsService                    patssvc.Service
	credentialsService             credentialssvc.Service
	datasourcesService             datasourcessvc.Service
	dataSourceRefsService          datasourcerefssvc.Service
	queriesService                 queriessvc.Service
	eventsService                  eventssvc.Service
	notificationsService           notificationsvc.Service
	retentionService               retentionsvc.Service
	pipelinesService               pipelinesvc.Service
	proofReceiptService            proofreceiptsvc.Service
	proofObservationService        *proofreceiptsvc.LiveCompiler
	disableProofReceipts           bool
	pipelineTriggersService        pipelinetriggersvc.Service
	pipelineWebhookService         pipelineWebhookService
	authService                    fakeAuthService
	oidcService                    oidc.Service
	middleware                     []fiber.Handler
	selfServiceRegistrationEnabled *bool
	externalOnboardingEnabled      *bool
}

// newTestApp creates a Fiber app with routes registered and the apierror middleware.
// It returns the app and the session store for session manipulation.
func newTestApp(t *testing.T, cfg testServerConfig) (*fiber.App, *session.Store) {
	if cfg.memberAdmission == nil {
		cfg.memberAdmission = fakeMemberAdmission{}
	}
	t.Helper()

	store := session.NewStore()
	if cfg.runRecencyService == nil {
		cfg.runRecencyService = fakeRunRecencyService{}
	}

	if cfg.compiledPlansService == nil {
		cfg.compiledPlansService = fakeCompiledPlans{}
	}
	if cfg.usersService == nil {
		cfg.usersService = &fakeUsersService{}
	}
	if cfg.orgService == nil {
		cfg.orgService = &fakeOrgService{}
	}
	if cfg.admissionService == nil {
		cfg.admissionService = &testfakes.AdmissionService{}
	}
	if cfg.membersService == nil {
		cfg.membersService = &fakeMembersService{}
	}
	if cfg.projectsService == nil {
		cfg.projectsService = &fakeProjectsService{}
	}
	if cfg.environmentsService == nil {
		cfg.environmentsService = &fakeEnvironmentsService{}
	}
	if cfg.rolesService == nil {
		cfg.rolesService = &fakeRolesService{}
	}
	if cfg.apiKeysService == nil {
		cfg.apiKeysService = &fakeAPIKeysService{}
	}
	if cfg.patsService == nil {
		cfg.patsService = &fakePATsService{}
	}
	if cfg.credentialsService == nil {
		cfg.credentialsService = &fakeCredentialsService{}
	}
	if cfg.datasourcesService == nil {
		cfg.datasourcesService = &fakeDataSourcesService{}
	}
	if cfg.dataSourceRefsService == nil {
		cfg.dataSourceRefsService = &fakeDataSourceRefsService{}
	}
	if cfg.queriesService == nil {
		cfg.queriesService = &fakeQueriesService{}
	}
	if cfg.eventsService == nil {
		cfg.eventsService = &fakeEventsService{}
	}
	if cfg.notificationsService == nil {
		cfg.notificationsService = &fakeNotificationsService{}
	}
	if cfg.retentionService == nil {
		cfg.retentionService = &fakeRetentionService{}
	}
	if cfg.pipelinesService == nil {
		cfg.pipelinesService = &fakePipelinesService{}
	}
	if cfg.proofReceiptService == nil && !cfg.disableProofReceipts {
		cfg.proofReceiptService = &fakeProofReceiptService{}
	}
	if cfg.pipelineTriggersService == nil {
		cfg.pipelineTriggersService = &fakePipelineTriggersService{}
	}
	if cfg.oidcService == nil {
		cfg.oidcService = &fakeOIDCService{}
	}
	selfServiceRegistrationEnabled := true
	if cfg.selfServiceRegistrationEnabled != nil {
		selfServiceRegistrationEnabled = *cfg.selfServiceRegistrationEnabled
	}
	externalOnboardingEnabled := true
	if cfg.externalOnboardingEnabled != nil {
		externalOnboardingEnabled = *cfg.externalOnboardingEnabled
	}

	srv := &server{
		memberAdmission:                cfg.memberAdmission,
		githubAppService:               cfg.githubAppService,
		runRecencyService:              cfg.runRecencyService,
		usersService:                   cfg.usersService,
		oidcService:                    cfg.oidcService,
		sessionStore:                   store,
		authorizationService:           &cfg.authService,
		orgService:                     cfg.orgService,
		admissionService:               cfg.admissionService,
		projectsService:                cfg.projectsService,
		repositorySetupService:         cfg.repositorySetupService,
		pipelineCreationsService:       cfg.pipelineCreationsService,
		odcsContractsService:           cfg.odcsContractsService,
		environmentsService:            cfg.environmentsService,
		membersService:                 cfg.membersService,
		rolesService:                   cfg.rolesService,
		apiKeysService:                 cfg.apiKeysService,
		patsService:                    cfg.patsService,
		credentialsService:             cfg.credentialsService,
		datasourcesService:             cfg.datasourcesService,
		dataSourceRefsService:          cfg.dataSourceRefsService,
		queriesService:                 cfg.queriesService,
		eventsService:                  cfg.eventsService,
		notificationsService:           cfg.notificationsService,
		retentionService:               cfg.retentionService,
		pipelinesService:               cfg.pipelinesService,
		compiledPlansService:           cfg.compiledPlansService,
		proofReceiptService:            cfg.proofReceiptService,
		proofObservationService:        cfg.proofObservationService,
		pipelineTriggersService:        cfg.pipelineTriggersService,
		pipelineWebhookService:         cfg.pipelineWebhookService,
		frontendURL:                    "http://localhost:3000",
		googleRedirectURI:              "http://localhost:8080/auth/google/callback",
		selfServiceRegistrationEnabled: selfServiceRegistrationEnabled,
		externalOnboardingEnabled:      externalOnboardingEnabled,
	}

	app := fiber.New(fiber.Config{
		ErrorHandler: apierror.Middleware(),
	})
	for _, middleware := range cfg.middleware {
		app.Use(middleware)
	}

	api.RegisterHandlers(app, srv)

	return app, store
}

// authenticatedRequest sends an HTTP request with an active session for the given user slug.
func authenticatedRequest(
	t *testing.T,
	app *fiber.App,
	store *session.Store,
	userSlug slug.Slug,
	req *http.Request,
) *http.Response {
	t.Helper()

	// Create a session by hitting a temporary login route
	app.Post("/_test/login", func(c fiber.Ctx) error {
		if err := reqctx.ResetAndSetAuthenticatedUser(c, store, userSlug, 1, true); err != nil {
			return err
		}
		return c.SendStatus(fiber.StatusOK)
	})

	loginReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/_test/login", nil)
	loginResp, err := app.Test(loginReq)
	require.NoError(t, err)
	require.Equal(t, fiber.StatusOK, loginResp.StatusCode)

	// Copy session cookie to the actual request
	for _, cookie := range loginResp.Cookies() {
		req.AddCookie(cookie)
	}

	resp, err := app.Test(req)
	require.NoError(t, err)

	return resp
}

// jsonRequest creates an HTTP request with a JSON body.
// Pass nil for requests without a body (GET, DELETE).
func jsonRequest(t *testing.T, method, url string, body any) *http.Request {
	t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(data)
	}

	req := httptest.NewRequestWithContext(context.Background(), method, url, reader)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// parseResponse unmarshals the JSON response body into the given type.
func parseResponse[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, resp.Body.Close())
	}()

	var v T
	require.NoError(
		t,
		json.Unmarshal(body, &v),
		"failed to unmarshal response body: %s",
		string(body),
	)
	return v
}

// assertResponse unmarshals the JSON response body into T and asserts equality with the expected
// value.
func assertResponse[T any](t *testing.T, resp *http.Response, expected T) {
	t.Helper()
	got := parseResponse[T](t, resp)
	assert.Equal(t, expected, got)
}

func assertUserEventActor(t *testing.T, actor model.EventActor, wantSlug slug.Slug) {
	t.Helper()
	require.Equal(t, model.EventActorTypeUser, actor.Type)
	require.NotNil(t, actor.Slug)
	assert.Equal(t, wantSlug, *actor.Slug)
}

// assertErrorResponse parses the structured error response and asserts both code and message.
func assertErrorResponse(t *testing.T, resp *http.Response, wantErr *apierror.Error) {
	t.Helper()
	errResp := parseResponse[apierror.ErrorResponse](t, resp)
	require.NotNil(t, errResp.Error, "expected error in response body")
	assert.Equal(t, wantErr.Code, errResp.Error.Code)
	assert.Equal(t, wantErr.Message, errResp.Error.Message)
}

func (*fakeDataSourcesService) EnsureProofWebhook(context.Context, slug.Slug, slug.Slug) error {
	return nil
}

func (f *fakeCredentialsService) CreateOrganizationGitLabConnection(ctx context.Context, params credentialssvc.CreateOrganizationGitLabConnectionParams) (credentialssvc.OrganizationGitLabConnection, error) {
	if f.CreateOrganizationGitLabConnectionFn == nil {
		return credentialssvc.OrganizationGitLabConnection{}, credentialssvc.ErrCredentialNotFound
	}
	return f.CreateOrganizationGitLabConnectionFn(ctx, params)
}

func (f *fakeCredentialsService) GetOrganizationGitLabConnection(ctx context.Context, org slug.Slug) (credentialssvc.OrganizationGitLabConnection, error) {
	if f.GetOrganizationGitLabConnectionFn == nil {
		return credentialssvc.OrganizationGitLabConnection{}, credentialssvc.ErrCredentialNotFound
	}
	return f.GetOrganizationGitLabConnectionFn(ctx, org)
}

func (f *fakeCredentialsService) ListOrganizationGitLabRepositories(ctx context.Context, org slug.Slug) ([]credentialssvc.ProviderRepository, error) {
	if f.ListOrganizationGitLabRepositoriesFn == nil {
		return nil, credentialssvc.ErrCredentialNotFound
	}
	return f.ListOrganizationGitLabRepositoriesFn(ctx, org)
}

func (f *fakeCredentialsService) AttachOrganizationGitLabConnection(ctx context.Context, params credentialssvc.AttachOrganizationGitLabConnectionParams) error {
	if f.AttachOrganizationGitLabConnectionFn == nil {
		return credentialssvc.ErrCredentialNotFound
	}
	return f.AttachOrganizationGitLabConnectionFn(ctx, params)
}

func (f *fakeDataSourcesService) ListGoogleWorkspaceFiles(ctx context.Context, org, ds slug.Slug, page workspace.ListOptions) (*workspace.Discovery, error) {
	if f.ListGoogleWorkspaceFilesFn != nil {
		return f.ListGoogleWorkspaceFilesFn(ctx, org, ds, page)
	}
	panic("unexpected spreadsheet discovery")
}

func (f *fakeDataSourcesService) VerifyGoogleWorkspaceFiles(ctx context.Context, org, source slug.Slug, actor model.EventActor, params datasourcessvc.WorkspaceVerificationParams) (*workspace.Discovery, error) {
	if f.VerifyGoogleWorkspaceFilesFn != nil {
		return f.VerifyGoogleWorkspaceFilesFn(ctx, org, source, actor, params)
	}
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (*fakeDataSourcesService) GetGoogleWorkspaceFile(context.Context, slug.Slug, slug.Slug, string) (*workspace.File, error) {
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (*fakeDataSourcesService) GetGoogleWorkspaceWorksheets(context.Context, slug.Slug, slug.Slug, string) (*workspace.Metadata, error) {
	return nil, xerror.Unavailable(xerror.DataSource)
}

func (f *fakePipelinesService) RecoverRun(ctx context.Context, org, project, environment, run slug.Slug, confirmation *slug.Slug, actor model.EventActor, source pipelinerun.RunSourceSummary) (*pipelinesvc.PipelineRunDetail, error) {
	if f.recoverRunFn == nil {
		return nil, fmt.Errorf("unexpected recovery call")
	}
	return f.recoverRunFn(ctx, org, project, environment, run, confirmation, actor, source)
}

func (*fakeDataSourcesService) GetGoogleWorkspacePicker(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.WorkspacePicker, error) {
	return &datasourcessvc.WorkspacePicker{WorkspacePickerSettings: datasourcessvc.WorkspacePickerSettings{ClientID: "picker-client", DeveloperKey: "public-key", AppID: "123"}, AccountEmail: "test@example.com", ExpectedRevision: 7}, nil
}

func (f *fakeCredentialsService) ConfigureHTTPOAuthApp(ctx context.Context, params credentialssvc.ConfigureHTTPOAuthAppParams) error {
	if f.ConfigureHTTPOAuthAppFn == nil {
		return xerror.Unexpected().Detail("unexpected ConfigureHTTPOAuthApp call")
	}
	return f.ConfigureHTTPOAuthAppFn(ctx, params)
}

func (f *fakeCredentialsService) ConnectHTTPOAuthClientCredentials(ctx context.Context, params credentialssvc.ResolveRuntimeCredentialParams) (credentialssvc.CredentialMetadata, error) {
	if f.ConnectHTTPOAuthClientCredentialsFn == nil {
		return credentialssvc.CredentialMetadata{}, xerror.Unexpected().Detail("unexpected ConnectHTTPOAuthClientCredentials call")
	}
	return f.ConnectHTTPOAuthClientCredentialsFn(ctx, params)
}

func (f *fakeCredentialsService) ConnectHTTPOAuth(ctx context.Context, p credentialssvc.StartOAuthInstallParams) (*credentialssvc.ProviderConnectorInstall, error) {
	if f.ConnectHTTPOAuthFn == nil {
		return nil, xerror.Unexpected().Detail("unexpected ConnectHTTPOAuth call")
	}
	return f.ConnectHTTPOAuthFn(ctx, p)
}

func (f *fakeCredentialsService) AcquireHTTPOAuthTestCredential(ctx context.Context, p credentialssvc.HTTPOAuthApp) (credentialssvc.RuntimeCredential, error) {
	if f.AcquireHTTPOAuthTestCredentialFn == nil {
		return credentialssvc.RuntimeCredential{}, xerror.Unexpected().Detail("unexpected AcquireHTTPOAuthTestCredential call")
	}
	return f.AcquireHTTPOAuthTestCredentialFn(ctx, p)
}

func (f *fakeDataSourcesService) ConnectHTTPOAuth(ctx context.Context, org, source slug.Slug, actor model.EventActor) (*datasourcessvc.HTTPOAuthConnection, error) {
	if f.ConnectHTTPOAuthFn == nil {
		return nil, xerror.Unexpected().Detail("unexpected ConnectHTTPOAuth call")
	}
	return f.ConnectHTTPOAuthFn(ctx, org, source, actor)
}

func (f *fakePipelinesService) StartBackfill(ctx context.Context, org, project, environment, pipeline slug.Slug, run, confirmation *slug.Slug, actor model.EventActor, source pipelinerun.RunSourceSummary, input map[string]any, windows map[slug.Slug]pipelinerun.IncrementalSourceWindow) (*pipelinesvc.PipelineRunDetail, error) {
	if f.StartBackfillFn != nil {
		return f.StartBackfillFn(ctx, org, project, environment, pipeline, run, confirmation, actor, source, input, windows)
	}
	return nil, errFakePipelinesServiceFnNotSet
}

func (f *fakePipelinesService) BootstrapCheckpoints(ctx context.Context, org, project, environment, pipeline, request slug.Slug, confirmation *slug.Slug, actor model.EventActor, input map[string]any, seeds map[slug.Slug]json.RawMessage) error {
	if f.BootstrapCheckpointsFn != nil {
		return f.BootstrapCheckpointsFn(ctx, org, project, environment, pipeline, request, confirmation, actor, input, seeds)
	}
	return errFakePipelinesServiceFnNotSet
}

func (f *fakeCredentialsService) CreateNotificationConnection(ctx context.Context, params credentialssvc.CreateNotificationConnectionParams) (credentialssvc.NotificationConnection, error) {
	if f.CreateNotificationConnectionFn == nil {
		return credentialssvc.NotificationConnection{}, errFakeCredentialsServiceFnNotSet
	}
	return f.CreateNotificationConnectionFn(ctx, params)
}

func (f *fakeCredentialsService) ListNotificationConnections(ctx context.Context, org slug.Slug) ([]credentialssvc.NotificationConnection, error) {
	if f.ListNotificationConnectionsFn == nil {
		return nil, errFakeCredentialsServiceFnNotSet
	}
	return f.ListNotificationConnectionsFn(ctx, org)
}

func (f *fakeCredentialsService) ConnectNotificationConnection(ctx context.Context, owner credentialssvc.CredentialOwner, actor model.EventActor) (credentialssvc.ProviderConnectorInstall, error) {
	if f.ConnectNotificationConnectionFn == nil {
		return credentialssvc.ProviderConnectorInstall{}, errFakeCredentialsServiceFnNotSet
	}
	return f.ConnectNotificationConnectionFn(ctx, owner, actor)
}

func (f *fakeNotificationsService) NotifyRunSucceeded(context.Context, slug.Slug, slug.Slug, slug.Slug, string) error {
	return nil
}

func (s *fakePipelineTriggersService) CheckCompletionSnapshot(context.Context, slug.Slug, slug.Slug, slug.Slug, int) (*model.PipelineCompletionSnapshot, error) {
	panic("unexpected completion snapshot call")
}

func (s *fakePipelinesService) ValidateCompletionExecutionContext(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot) error {
	panic("unexpected completion validation call")
}

func (*fakePipelinesService) AcceptCompletionRun(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot) (jobs.PipelineRunExecutionArgs, error) {
	panic("unexpected completion admission")
}

func (*fakePipelinesService) AcceptCompletionRetryRun(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot, slug.Slug, pipelinerun.RunSourceSummary, time.Time) (jobs.PipelineRunExecutionArgs, error) {
	panic("unexpected completion retry admission")
}

func (*fakePipelinesService) ExecuteCompletionPipelineRun(context.Context, jobs.PipelineRunExecutionArgs, pipelinesvc.CompletionAuthorityChecker) error {
	panic("unexpected completion execution")
}

func (f *fakePipelinesService) GetRunCompletion(ctx context.Context, org, project, environment, run slug.Slug) (*model.PipelineRunCompletion, error) {
	if f.GetRunCompletionFn == nil {
		return nil, errFakePipelinesServiceFnNotSet
	}
	return f.GetRunCompletionFn(ctx, org, project, environment, run)
}

func (f *fakePipelinesService) GetRunProgress(ctx context.Context, org, project, environment slug.Slug, run string, after int64, limit int) (*pipelinesvc.RunProgress, error) {
	if f.GetRunProgressFn != nil {
		return f.GetRunProgressFn(ctx, org, project, environment, run, after, limit)
	}
	return nil, errFakePipelinesServiceFnNotSet
}

func (*fakePipelinesService) AcceptQueuedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, slug.Slug, pipelinerun.RunSourceSummary, time.Time, int64) error {
	return errFakePipelinesServiceFnNotSet
}

func (*fakePipelinesService) StartQueuedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug, time.Time) error {
	return errFakePipelinesServiceFnNotSet
}

func (*fakePipelinesService) ReconcileInterruptedRuns(context.Context, time.Time) error {
	return errFakePipelinesServiceFnNotSet
}

func (*fakePipelinesService) FailUnadmittedRun(context.Context, slug.Slug, slug.Slug, slug.Slug, slug.Slug) error {
	return nil
}
