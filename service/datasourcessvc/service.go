package datasourcessvc

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	pipelinedrift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	repods "github.com/Braidplane/braidplane-core/postgres/datasources"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/githubapps"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	gitconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/git"
	httpconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/http"
	"github.com/Braidplane/braidplane-core/service/githubappsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

const (
	privateDataSourceHostsPolicyMessage = "allow_private_hosts is disabled for this deployment"
	connectionTestFinalizeTimeout       = 5 * time.Second
	// Allow one maximum-duration OAuth refresh and one maximum-duration connector test,
	// plus bounded persistence overhead, after the caller disconnects.
	providerCallbackContinuationTimeout = 11 * time.Minute
)

// GitDataSourceAuth carries the git authentication components needed for
// performing raw git operations (CheckRemote, CloneRemote, PushRemote).
// Cleanup must be called (via defer) when git operations are complete.
type GitDataSourceAuth struct {
	GitConfigurationHash  string
	GitWritable           bool
	GitDataSourceSlug     string
	GitCredentialSlug     string
	GitCredentialRevision string
	// Provider selects native PR/MR publication or plain Git proposal branches.
	Provider string
	// Env holds auth variables created from decrypted datasource credentials.
	// Provider HTTPS PATs are exposed here as GITHUB_TOKEN or GITLAB_TOKEN so
	// git operations and provider API calls can share the same resolved secret
	// without embedding it in RepoURL.
	Env []string
	// RepoURL is a sanitized repository URL whose native Git transport is pinned
	// through Env to the destination vetted by the data-source host policy.
	RepoURL string
	// DefaultBranch is the branch configured on the data source, may be empty.
	DefaultBranch string
	// GitHubContentsPermission is set only from a freshly minted GitHub App installation token.
	GitHubContentsPermission *string
	GitHubProposalAuthority  *GitHubProposalAuthority
	// Cleanup removes any temporary files created for this auth session (e.g. SSH key).
	Cleanup func()
}

// GitExecutionAuth carries encrypted-boundary inputs needed to construct Git
// authentication and destination pinning on the host that performs the dial.
type GitExecutionAuth struct {
	GitConfigurationHash     string                   `json:"git_configuration_hash,omitempty"`
	GitWritable              bool                     `json:"git_writable"`
	GitDataSourceSlug        string                   `json:"git_data_source_slug,omitempty"`
	GitCredentialSlug        string                   `json:"git_credential_slug,omitempty"`
	GitCredentialRevision    string                   `json:"git_credential_revision,omitempty"`
	CredentialReference      string                   `json:"credential_reference"`
	DataSourceType           model.DataSourceType     `json:"data_source_type"`
	ConfigJSON               []byte                   `json:"config_json"`
	CredentialsJSON          []byte                   `json:"credentials_json"`
	GitHubContentsPermission *string                  `json:"github_contents_permission,omitempty"`
	GitHubProposalAuthority  *GitHubProposalAuthority `json:"github_proposal_authority,omitempty"`
}

// Service defines the interface for data source business logic.
type Service interface {
	ConnectHTTPOAuth(context.Context, slug.Slug, slug.Slug, model.EventActor) (*HTTPOAuthConnection, error)
	AttachOrganizationGitLabConnection(context.Context, slug.Slug, slug.Slug, model.EventActor) error
	Create(
		ctx context.Context,
		orgSlug slug.Slug,
		actor model.EventActor,
		params *CreateParams,
	) (*DataSource, error)
	CreateProvisioned(ctx context.Context, orgSlug slug.Slug, actor model.EventActor, params *CreateParams, provisioner SourceProvisioner) (*DataSource, error)
	// StartProviderDataSourceInstall atomically creates a pending read-only provider data source and starts OAuth consent.
	StartProviderDataSourceInstall(
		ctx context.Context,
		orgSlug slug.Slug,
		actor model.EventActor,
		provider string,
		params *StartProviderDataSourceInstallParams,
	) (*ProviderDataSourceInstall, error)
	GetGoogleWorkspacePicker(context.Context, slug.Slug, slug.Slug) (*WorkspacePicker, error)
	ListGoogleWorkspaceFiles(ctx context.Context, orgSlug, dsSlug slug.Slug, options workspace.ListOptions) (*workspace.Discovery, error)
	GetGoogleWorkspaceFile(context.Context, slug.Slug, slug.Slug, string) (*workspace.File, error)
	GetGoogleWorkspaceWorksheets(context.Context, slug.Slug, slug.Slug, string) (*workspace.Metadata, error)
	VerifyGoogleWorkspaceFiles(context.Context, slug.Slug, slug.Slug, model.EventActor, WorkspaceVerificationParams) (*workspace.Discovery, error)
	ListProviderRepositories(ctx context.Context, orgSlug, dsSlug slug.Slug) ([]ProviderRepository, error)
	SelectProviderRepository(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor, params SelectProviderRepositoryParams) (*DataSource, error)
	TestConnection(ctx context.Context, orgSlug slug.Slug, params *TestConnectionParams) error
	TestExistingConnection(ctx context.Context, orgSlug, dsSlug slug.Slug) error
	GetSnowflakeOnboarding(ctx context.Context, orgSlug, dsSlug slug.Slug) (*SnowflakeOnboarding, error)
	FindBySlug(ctx context.Context, orgSlug, dsSlug slug.Slug) (*DataSource, error)
	ListByOrg(
		ctx context.Context,
		orgSlug slug.Slug,
		req model.ListRequest,
	) (model.ListResponse[DataSource], error)
	Update(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		actor model.EventActor,
		params *UpdateParams,
	) (*DataSource, error)
	UpdateCredentials(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		actor model.EventActor,
		params *UpdateCredentialsParams,
	) (*DataSource, error)
	CompleteProviderOAuthCallback(
		ctx context.Context,
		params credentialssvc.CompleteOAuthCallbackParams,
	) (credentialssvc.CompleteOAuthCallbackResult, error)
	ValidateCredentialLifecycle(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		actor model.EventActor,
		action model.EventAction,
	) (*DataSource, error)
	RecordCredentialLifecycleEvent(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		actor model.EventActor,
		action model.EventAction,
	) error
	ListBranches(ctx context.Context, orgSlug, dsSlug slug.Slug) ([]DataSourceBranch, error)
	IntrospectSchema(ctx context.Context, orgSlug, dsSlug slug.Slug) (SchemaResult, error)
	RefreshSchema(
		ctx context.Context,
		orgSlug, dsSlug slug.Slug,
		actor model.EventActor,
	) (*SchemaImportResult, error)
	ExecuteQueuedSchemaImport(ctx context.Context, args jobs.DataSourceSchemaImportArgs) (*SchemaImportResult, error)
	Delete(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor) error
	Restore(ctx context.Context, orgSlug, dsSlug slug.Slug, actor model.EventActor) error
	// ResolveGitAuth returns the authentication components needed to perform raw git
	// operations against a git-compatible data source's configured repository.
	// The returned GitDataSourceAuth.Cleanup must be called when operations complete.
	// Returns ErrDataSourceNotFound if the slug does not exist.
	// Returns ErrInvalidDataSourceType if the data source is not git, github, or gitlab.
	ResolveGitAuth(ctx context.Context, orgSlug, dsSlug slug.Slug) (*GitDataSourceAuth, error)
	EnsureProofWebhook(ctx context.Context, orgSlug, dsSlug slug.Slug) error
	ResolveGitExecutionAuth(ctx context.Context, orgSlug, dsSlug slug.Slug) (*GitExecutionAuth, error)
}

type credentialService interface {
	credentialssvc.Service
	credentialssvc.ProviderRepositoryService
}

type GitHubAppManager interface {
	Get(context.Context, slug.Slug) (*githubappsvc.Connection, error)
	Token(context.Context, slug.Slug, []int64) (githubappsvc.InstallationToken, error)
}

type noopGitHubAppManager struct{}

func NewNoopGitHubAppManager() GitHubAppManager { return noopGitHubAppManager{} }

func (noopGitHubAppManager) Get(context.Context, slug.Slug) (*githubappsvc.Connection, error) {
	return nil, xerror.Unavailable(xerror.DataSource).Detail("managed GitHub connections are unavailable")
}

func (noopGitHubAppManager) Token(context.Context, slug.Slug, []int64) (githubappsvc.InstallationToken, error) {
	return githubappsvc.InstallationToken{}, xerror.Unavailable(xerror.DataSource).Detail("managed GitHub connections are unavailable")
}

// StartProviderDataSourceInstall atomically creates a pending provider data source and starts OAuth consent.
func (s *service) StartProviderDataSourceInstall(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	provider string,
	params *StartProviderDataSourceInstallParams,
) (*ProviderDataSourceInstall, error) {
	if provider == "gitlab" {
		return nil, ErrInvalidInput.Detail("Connect GitLab in organization settings, then create a managed repository data source.")
	}
	if err := validate.Default().Struct(params); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	if provider != params.Type.String() || (params.Type != model.DataSourceTypeHubSpot && params.Type != model.DataSourceTypeGitlab && params.Type != model.DataSourceTypeBigQuery && params.Type != model.DataSourceTypeGoogleWorkspace) {
		return nil, ErrInvalidInput.Detail("provider and data source type must both be hubspot, gitlab, bigquery, or google_workspace")
	}

	slugWasGenerated := params.Slug == ""
	var err error
	params.Slug, err = params.Slug.ValidateOrGenerate(params.Name)
	if err != nil {
		return nil, ErrInvalidInput.Detailf("failed to generate slug: %w", err)
	}
	if params.Type == model.DataSourceTypeGoogleWorkspace {
		if err := decodeWorkspacePending(params.ConfigJSON); err != nil {
			return nil, err
		}
	} else if err := s.validateProviderDataSourceConfig(params.Type, params.ConfigJSON); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	readOnly := true
	if params.ReadOnly != nil {
		readOnly = *params.ReadOnly
	}

	var result ProviderDataSourceInstall
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if slugWasGenerated {
			reserved, reservationErr := s.repo.IsSlugReserved(ctx, tx, orgSlug, params.Slug)
			if reservationErr != nil {
				return xerror.Wrapf("check provider data source slug reservation: %w", reservationErr)
			}
			if reserved {
				params.Slug, err = slug.WithRandomSuffix(params.Slug, 6)
				if err != nil {
					return xerror.Unexpected().Detailf("generate unique provider data source slug: %w", err)
				}
			}
		}

		owner := credentialssvc.CredentialOwner{
			OrgSlug:        orgSlug,
			DataSourceSlug: params.Slug,
		}
		credential, err := s.credentials.CreatePendingOAuthCredential(ctx, credentialssvc.CreatePendingOAuthCredentialParams{
			Owner:    owner,
			Provider: provider,
		})
		if err != nil {
			return xerror.Wrapf("create pending data source credential: %w", err)
		}

		repoDS, err := s.repo.Create(ctx, tx, orgSlug, &repods.CreateParams{
			Slug:           params.Slug,
			Name:           params.Name,
			Type:           params.Type,
			ConfigJSON:     params.ConfigJSON,
			CredentialSlug: credential.Slug,
			ReadOnly:       readOnly,
		})
		if err != nil {
			return xerror.Wrapf("create pending provider data source in repo: %w", err)
		}

		install, err := s.credentials.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
			Owner:              owner,
			Provider:           provider,
			IncludeWriteScopes: !readOnly,
			AccessMode:         workspaceConsentMode(params.Type, params.ConfigJSON),
			Intent:             credentialssvc.OAuthInstallIntentCreate,
			Actor:              actor,
		})
		if err != nil {
			return xerror.Wrapf("start provider data source oauth install: %w", err)
		}

		dataSource, err := toDataSourceFromRepo(repoDS)
		if err != nil {
			return xerror.Wrapf("convert pending provider data source: %w", err)
		}
		dataSource.Credential = &credential
		result = ProviderDataSourceInstall{DataSource: dataSource, Install: install}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *service) ListProviderRepositories(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) ([]ProviderRepository, error) {
	ds, err := s.findRepoDataSource(ctx, orgSlug, dsSlug)
	if err != nil {
		return nil, err
	}
	if ds.Type != model.DataSourceTypeGitlab && ds.Type != model.DataSourceTypeGithub {
		return nil, ErrInvalidDataSourceType.Detail("provider repository selection requires a GitHub or GitLab data source")
	}
	if ds.Type == model.DataSourceTypeGithub {
		connection, err := s.githubAppConnection(ctx, orgSlug)
		if err != nil {
			return nil, err
		}
		result := make([]ProviderRepository, 0, len(connection.Repositories))
		for _, repository := range connection.Repositories {
			result = append(result, githubProviderRepository(repository))
		}
		return result, nil
	}
	repositories, err := s.credentials.ListProviderRepositories(ctx, credentialssvc.ListProviderRepositoriesParams{
		Owner:    dataSourceCredentialOwner(orgSlug, dsSlug),
		Provider: ds.Type.String(),
	})
	if err != nil {
		return nil, xerror.Wrapf("list provider repositories: %w", err)
	}
	result := make([]ProviderRepository, 0, len(repositories))
	for _, repository := range repositories {
		result = append(result, providerRepositoryFromCredential(repository))
	}
	return result, nil
}

func (s *service) SelectProviderRepository(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	params SelectProviderRepositoryParams,
) (*DataSource, error) {
	if err := validate.Default().Struct(params); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	current, err := s.findRepoDataSource(ctx, orgSlug, dsSlug)
	if err != nil {
		return nil, err
	}
	if current.Type != model.DataSourceTypeGitlab && current.Type != model.DataSourceTypeGithub {
		return nil, ErrInvalidDataSourceType.Detail("provider repository selection requires a GitHub or GitLab data source")
	}
	var repository ProviderRepository
	if current.Type == model.DataSourceTypeGithub {
		repository, err = s.githubRepository(ctx, orgSlug, params.RepositoryID)
	} else {
		var resolved credentialssvc.ProviderRepository
		resolved, err = s.credentials.ResolveProviderRepository(ctx, credentialssvc.ResolveProviderRepositoryParams{
			Owner:        dataSourceCredentialOwner(orgSlug, dsSlug),
			Provider:     current.Type.String(),
			RepositoryID: params.RepositoryID,
		})
		repository = providerRepositoryFromCredential(resolved)
	}
	if err != nil {
		return nil, xerror.Wrapf("resolve provider repository: %w", err)
	}
	managed := gitconnector.ProviderConfig{Repository: gitconnector.ProviderRepository{
		ID: repository.ID, Name: repository.Name, FullName: repository.FullName,
		WebURL: repository.WebURL, DefaultBranch: repository.DefaultBranch,
	}}
	var existing struct {
		BasePath string `json:"base_path"`
	}
	if err := json.Unmarshal(current.Config, &existing); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	managed.BasePath = existing.BasePath
	if err := validate.Default().Struct(managed); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	configJSON, err := json.Marshal(managed)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	err = s.txm.RunInTx(postgres.WithoutTransaction(ctx), func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.repo.LockDataSourceForUpdate(ctx, tx, orgSlug, dsSlug); err != nil {
			return err
		}
		locked, err := s.repo.FindBySlug(ctx, tx, orgSlug, dsSlug)
		if err != nil {
			return err
		}
		if locked.ConnectionTestRevision != current.ConnectionTestRevision {
			return ErrConnectionTestConflict
		}
		updated, err := s.repo.Update(ctx, tx, orgSlug, dsSlug, &repods.UpdateParams{ConfigJSON: configJSON})
		if err != nil {
			return err
		}
		return s.recordDataSourceEvent(ctx, tx, orgSlug, actor, updated, model.EventActionUpdate)
	})
	if err != nil {
		return nil, xerror.Wrapf("store provider repository selection: %w", err)
	}

	repoDS, credentialsJSON, err := s.snapshotDataSourceAndCredentials(ctx, orgSlug, dsSlug)
	if err != nil {
		return nil, err
	}
	_, err = s.testConnection(ctx, orgSlug, repoDS, credentialsJSON)
	if err != nil {
		return nil, ErrInvalidInput.Detailf("selected provider repository failed connection testing: %w", err)
	}
	stored, err := s.findRepoDataSource(ctx, orgSlug, dsSlug)
	if err != nil {
		return nil, err
	}
	return toDataSourceFromRepo(stored)
}

func (s *service) findRepoDataSource(ctx context.Context, orgSlug, dsSlug slug.Slug) (*repods.DataSource, error) {
	return postgres.InRead(postgres.WithoutTransaction(ctx), s.txm, func(ctx context.Context, db postgres.DBTX) (*repods.DataSource, error) {
		return s.repo.FindBySlug(ctx, db, orgSlug, dsSlug)
	})
}

func providerRepositoryFromCredential(repository credentialssvc.ProviderRepository) ProviderRepository {
	return ProviderRepository{
		ID:            repository.ID,
		Name:          repository.Name,
		FullName:      repository.FullName,
		WebURL:        repository.WebURL,
		DefaultBranch: repository.DefaultBranch,
	}
}

func githubProviderRepository(repository githubapps.RepositoryBinding) ProviderRepository {
	webURL := url.URL{Scheme: "https", Host: "github.com", Path: "/" + repository.FullName}
	return ProviderRepository{
		ID: strconv.FormatInt(repository.ID, 10), Name: repository.Name,
		FullName: repository.FullName, WebURL: webURL.String(),
	}
}

func (s *service) githubAppConnection(ctx context.Context, orgSlug slug.Slug) (*githubappsvc.Connection, error) {
	if s.githubApps == nil {
		return nil, xerror.Unavailable(xerror.DataSource).Detail("managed GitHub connections are unavailable")
	}
	connection, err := s.githubApps.Get(ctx, orgSlug)
	if err != nil {
		return nil, err
	}
	if connection == nil || connection.SuspendedAt != nil || connection.DeletedAt != nil {
		return nil, xerror.Unavailable(xerror.DataSource).Detail("an active organization GitHub App connection is required")
	}
	return connection, nil
}

func (s *service) githubRepository(ctx context.Context, orgSlug slug.Slug, repositoryID string) (ProviderRepository, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(repositoryID), 10, 64)
	if err != nil || id <= 0 {
		return ProviderRepository{}, ErrInvalidInput.With(err).Detail("GitHub repository ID must be a positive integer")
	}
	connection, err := s.githubAppConnection(ctx, orgSlug)
	if err != nil {
		return ProviderRepository{}, err
	}
	for _, repository := range connection.Repositories {
		if repository.ID == id {
			return githubProviderRepository(repository), nil
		}
	}
	return ProviderRepository{}, ErrInvalidInput.Detail("GitHub repository is not bound to the organization")
}

func dataSourceCredentialOwner(orgSlug, dsSlug slug.Slug) credentialssvc.CredentialOwner {
	return credentialssvc.CredentialOwner{OrgSlug: orgSlug, DataSourceSlug: dsSlug}
}

// service provides business logic for managing data sources.
type service struct {
	httpOAuthRedirectURI  string
	googleWorkspacePicker WorkspacePickerSettings
	snowflakeWIFIssuer    string
	txm                   postgres.TransactionManager
	repo                  repods.Repository
	schemaRepo            repods.SchemaMetadataRepository
	driftReports          driftimpactrepo.Repository
	credentials           credentialService
	githubApps            GitHubAppManager
	gitLabWebhooks        GitLabWebhookManager
	admission             admissionsvc.Service
	connectorFactory      connector.NewConnectorFunc
	connectionTester      ConnectionTestExecutor
	schemaIntrospector    SchemaIntrospectionExecutor
	schemaMaxBytes        int
	schemaMaxItems        int
	gitBranchLister       GitBranchExecutor
	gitBranchMaxBytes     int
	gitBranchMaxItems     int
	eventProducer         workerproducer.EventProducer
	schemaImportProducer  workerproducer.DataSourceSchemaImportProducer
	customerConfigPolicy  CustomerConfigPolicy
	prepareGitRemote      func(context.Context, string, []string, hostguard.DestinationPolicy) (string, []string, error)
}

type GitLabWebhookManager interface {
	Ensure(context.Context, slug.Slug, slug.Slug, []byte, []byte) error
}

// ConnectionTestRequest is the complete worker-side connector test payload.
// Remote implementations must encrypt it before persistence.
type ConnectionTestRequest struct {
	OrganizationSlug slug.Slug            `json:"organization_slug"`
	DataSourceSlug   slug.Slug            `json:"data_source_slug"`
	DataSourceType   model.DataSourceType `json:"data_source_type"`
	ConfigJSON       []byte               `json:"config_json"`
	CredentialsJSON  []byte               `json:"credentials_json"`
}

// ConnectionTestExecutor crosses the connector-capable execution boundary.
// Admission rejections must be marked with executiondelivery.MarkNotExecuted.
type ConnectionTestExecutor interface {
	TestConnection(context.Context, ConnectionTestRequest) error
}

// SchemaIntrospectionRequest is the operation-scoped payload required for one
// live connector schema read. Remote implementations must encrypt it before
// persistence and must not place credentials in queue arguments.
type SchemaIntrospectionRequest struct {
	OrganizationSlug slug.Slug            `json:"organization_slug"`
	DataSourceSlug   slug.Slug            `json:"data_source_slug"`
	DataSourceType   model.DataSourceType `json:"data_source_type"`
	ConfigJSON       []byte               `json:"config_json"`
	CredentialsJSON  []byte               `json:"credentials_json"`
	MaxResultBytes   int                  `json:"max_result_bytes"`
	MaxResultItems   int                  `json:"max_result_items"`
}

// SchemaIntrospectionExecutor crosses the connector-capable execution boundary.
// The result is the normalized schema JSON, rather than a provider object.
type SchemaIntrospectionExecutor interface {
	IntrospectSchema(context.Context, SchemaIntrospectionRequest) ([]byte, error)
}

type GitBranchRequest struct {
	OrganizationSlug slug.Slug        `json:"organization_slug"`
	Auth             GitExecutionAuth `json:"auth"`
	MaxResultBytes   int              `json:"max_result_bytes"`
	MaxResultItems   int              `json:"max_result_items"`
}

type GitBranchExecutor interface {
	ListGitBranches(context.Context, GitBranchRequest) ([]DataSourceBranch, error)
}

type localGitBranchExecutor struct{}

func (localGitBranchExecutor) ListGitBranches(ctx context.Context, request GitBranchRequest) ([]DataSourceBranch, error) {
	return ExecuteGitBranchList(ctx, request)
}

func ExecuteGitBranchList(ctx context.Context, request GitBranchRequest) ([]DataSourceBranch, error) {
	if request.MaxResultBytes <= 0 || request.MaxResultItems <= 0 {
		return nil, xerror.ExecutionUnavailable().Detail("Git branch result limits are required")
	}
	auth, err := PrepareGitExecutionAuth(ctx, request.Auth)
	if err != nil {
		return nil, err
	}
	defer auth.Cleanup()
	branches, err := repostore.ListRemoteBranches(ctx, auth.RepoURL, auth.Env)
	if err != nil {
		return nil, xerror.Wrapf("%w: %w", ErrConnectionFailed, err)
	}
	if len(branches) == 0 {
		return nil, ErrConnectionFailed.WithPublicDetail("git ls-remote failed: no branch heads returned").Detail("remote returned no branch heads")
	}
	if len(branches) > request.MaxResultItems {
		return nil, xerror.ExecutionUnavailable().Detail("Git branch result exceeded its item limit")
	}
	resultBytes := 0
	for _, branch := range branches {
		resultBytes += len(branch.Name)
		if resultBytes > request.MaxResultBytes {
			return nil, xerror.ExecutionUnavailable().Detail("Git branch result exceeded its byte limit")
		}
	}
	return dataSourceBranchesFromRemote(branches, auth.DefaultBranch), nil
}

type localConnectionTestExecutor struct {
	factory connector.NewConnectorFunc
}

type localSchemaIntrospectionExecutor struct {
	factory connector.NewConnectorFunc
}

func (e localSchemaIntrospectionExecutor) IntrospectSchema(
	ctx context.Context,
	request SchemaIntrospectionRequest,
) ([]byte, error) {
	return ExecuteSchemaIntrospection(ctx, e.factory, request)
}

func (e localConnectionTestExecutor) TestConnection(ctx context.Context, request ConnectionTestRequest) error {
	ctx = connector.WithDataSourceIdentity(ctx, request.OrganizationSlug, request.DataSourceSlug)
	conn, err := e.factory(ctx, request.DataSourceType, request.ConfigJSON, request.CredentialsJSON)
	if err != nil {
		return xerror.Wrapf("%w: failed to create connector: %w", ErrConnectionFailed, err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.TestConnection(ctx); err != nil {
		return xerror.Wrapf("%w: %w", ErrConnectionFailed, err)
	}
	return nil
}

// New creates a new data source service.
// If ncf (connector factory function) is nil, uses the production NewConnector function.
func New(
	txm postgres.TransactionManager,
	repo repods.Repository,
	schemaRepo repods.SchemaMetadataRepository,
	driftReports driftimpactrepo.Repository,
	credentials credentialssvc.Service,
	githubApps GitHubAppManager,
	gitLabWebhooks GitLabWebhookManager,
	admission admissionsvc.Service,
	ncf connector.NewConnectorFunc,
	eventProducer workerproducer.EventProducer,
	schemaImportProducer workerproducer.DataSourceSchemaImportProducer,
	config ServiceConfig,
) (Service, error) {
	if credentials == nil {
		return nil, xerror.Unexpected().Detail("credential service is required")
	}
	providerCredentials, ok := credentials.(credentialService)
	if !ok {
		return nil, xerror.Unexpected().Detail("credential service must support provider repository selection")
	}
	if githubApps == nil {
		return nil, xerror.Unexpected().Detail("GitHub App manager is required")
	}
	if gitLabWebhooks == nil {
		return nil, xerror.Unexpected().Detail("GitLab webhook manager is required")
	}
	if admission == nil {
		return nil, xerror.Unexpected().Detail("organization admission service is required")
	}
	if schemaImportProducer == nil {
		return nil, xerror.Unexpected().Detail("data source schema import producer is required")
	}
	if ncf == nil {
		ncf = connector.NewConnectorWithTracing(connector.NewConnector)
	}
	connectionTester := config.ConnectionTestExecutor
	if connectionTester == nil {
		connectionTester = localConnectionTestExecutor{factory: ncf}
	}
	schemaIntrospector := config.SchemaIntrospectionExecutor
	if schemaIntrospector == nil {
		schemaIntrospector = localSchemaIntrospectionExecutor{factory: ncf}
	}
	gitBranchLister := config.GitBranchExecutor
	if gitBranchLister == nil {
		gitBranchLister = localGitBranchExecutor{}
	}
	svc := &service{
		googleWorkspacePicker: config.GoogleWorkspacePicker,
		snowflakeWIFIssuer:    config.SnowflakeWIFIssuer,
		httpOAuthRedirectURI:  config.HTTPOAuthRedirectURI,
		txm:                   txm,
		repo:                  repo,
		schemaRepo:            schemaRepo,
		driftReports:          driftReports,
		credentials:           providerCredentials,
		githubApps:            githubApps,
		gitLabWebhooks:        gitLabWebhooks,
		admission:             admission,
		connectorFactory:      ncf,
		connectionTester:      connectionTester,
		schemaIntrospector:    schemaIntrospector,
		schemaMaxBytes:        positiveOrDefault(config.SchemaMaxBytes, 10*1024*1024),
		schemaMaxItems:        positiveOrDefault(config.SchemaMaxItems, 10_000),
		gitBranchLister:       gitBranchLister,
		gitBranchMaxBytes:     positiveOrDefault(config.GitBranchMaxBytes, 10*1024*1024),
		gitBranchMaxItems:     positiveOrDefault(config.GitBranchMaxItems, 10_000),
		eventProducer:         eventProducer,
		schemaImportProducer:  schemaImportProducer,
		customerConfigPolicy:  config.CustomerConfigPolicy,
		prepareGitRemote:      repostore.PrepareRemote,
	}
	return svc, nil
}

func positiveOrDefault(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

// TestConnection validates and tests draft data source configuration without persisting it.
func (s *service) TestConnection(
	ctx context.Context,
	orgSlug slug.Slug,
	params *TestConnectionParams,
) error {
	if params != nil && params.Type == model.DataSourceTypeGoogleWorkspace {
		return ErrInvalidInput.Detail("Google Workspace uses provider OAuth onboarding; static credentials and draft tests are unavailable")
	}
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}

	app, err := s.httpOAuthApp(params.Type, params.ConfigJSON, params.Credentials)
	if err != nil {
		return err
	}
	if app != nil {
		runtime, err := s.credentials.AcquireHTTPOAuthTestCredential(ctx, *app)
		if err != nil {
			return err
		}
		return s.runConnectionTest(postgres.WithoutTransaction(ctx), orgSlug, "", params.Type, params.ConfigJSON, runtime.PlaintextJSON)
	}
	configJSON := params.ConfigJSON
	credentialsJSON, err := json.Marshal(params.Credentials)
	if err != nil {
		return ErrInvalidInput.Detail("invalid credentials format")
	}
	if params.Type == model.DataSourceTypeGithub {
		configJSON, credentialsJSON, err = s.normalizeGitHubCreate(ctx, orgSlug, &CreateParams{ConfigJSON: params.ConfigJSON})
		if err != nil {
			return err
		}
	}
	if params.Type == model.DataSourceTypeGitlab {
		configJSON, credentialsJSON, _, err = s.normalizeGitLabCreate(ctx, orgSlug, &CreateParams{ConfigJSON: params.ConfigJSON, Credentials: params.Credentials, ReadOnly: true})
		if err != nil {
			return err
		}
	}

	if err := s.validateConfigAndCredentials(
		params.Type,
		configJSON,
		credentialsJSON,
	); err != nil {
		if xerror.Is(err, ErrInvalidDataSourceType) {
			return ErrInvalidDataSourceType.With(err)
		}
		return ErrInvalidInput.With(err)
	}

	return s.runConnectionTest(
		postgres.WithoutTransaction(ctx),
		orgSlug,
		"",
		params.Type,
		configJSON,
		credentialsJSON,
	)
}

// TestExistingConnection verifies the persisted data source configuration and stored credentials.
func (s *service) TestExistingConnection(ctx context.Context, orgSlug, dsSlug slug.Slug) error {
	repoDS, credentialsJSON, err := s.snapshotDataSourceAndCredentials(ctx, orgSlug, dsSlug)
	if err != nil {
		return xerror.Wrapf("prepare data source connection test: %w", err)
	}

	_, err = s.testConnection(ctx, orgSlug, repoDS, credentialsJSON)
	if err != nil {
		return err
	}
	return nil
}

// Create creates a new data source.
func (s *service) create(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	params *CreateParams,
	creator sourceCreator,
	allowUserOAuth bool,
) (*DataSource, error) {
	if params != nil && params.Type == model.DataSourceTypeGoogleWorkspace {
		return nil, ErrInvalidInput.Detail("Google Workspace uses provider OAuth onboarding; static credentials and draft tests are unavailable")
	}
	// Validate input using struct tags
	if err := validate.Default().Struct(params); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	validationConfigJSON := params.ConfigJSON
	managedProviderCreate := false
	var managedCredentialsJSON []byte
	var sharedCredentialSlug slug.Slug
	switch params.Type {
	case model.DataSourceTypeGitlab:
		var err error
		validationConfigJSON, managedCredentialsJSON, sharedCredentialSlug, err = s.normalizeGitLabCreate(ctx, orgSlug, params)
		if err != nil {
			return nil, err
		}
		managedProviderCreate = true
	case model.DataSourceTypeGithub:
		var err error
		validationConfigJSON, managedCredentialsJSON, err = s.normalizeGitHubCreate(ctx, orgSlug, params)
		if err != nil {
			return nil, err
		}
		managedProviderCreate = true
	default:
	}

	// Generate slug if not provided using ValidateOrGenerate
	var err error
	params.Slug, err = params.Slug.ValidateOrGenerate(params.Name)
	if err != nil {
		return nil, ErrInvalidInput.Detailf("failed to generate slug: %w", err)
	}

	app, err := s.httpOAuthApp(params.Type, params.ConfigJSON, params.Credentials)
	if err != nil {
		return nil, err
	}
	if app != nil {
		if !allowUserOAuth {
			return nil, ErrInvalidInput.Detail("HTTP OAuth requires user onboarding, not trusted provisioning")
		}
		return s.createHTTPOAuth(ctx, orgSlug, actor, params, *app)
	}

	credentialsJSON, err := json.Marshal(params.Credentials)
	if err != nil {
		return nil, ErrInvalidInput.Detail("invalid credentials format")
	}
	validationCredentialsJSON := credentialsJSON
	if len(managedCredentialsJSON) > 0 {
		validationCredentialsJSON = managedCredentialsJSON
	}

	if err := s.validateConfigAndCredentials(
		params.Type,
		validationConfigJSON,
		validationCredentialsJSON,
	); err != nil {
		if xerror.Is(err, ErrInvalidDataSourceType) {
			return nil, err
		}
		return nil, ErrInvalidInput.With(err)
	}

	forceReadOnly, err := shouldForceReadOnlyFromCredentialsJSON(params.Type, validationCredentialsJSON)
	if err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	if forceReadOnly {
		params.ReadOnly = true
	}

	repoParams := &repods.CreateParams{
		Slug:       params.Slug,
		Name:       params.Name,
		Type:       params.Type,
		ConfigJSON: params.ConfigJSON,
		ReadOnly:   params.ReadOnly,
	}

	var repoDS *repods.DataSource
	err = s.txm.RunInTx(postgres.WithoutTransaction(ctx), func(ctx context.Context, tx postgres.DBTX) error {
		repoParams.CredentialSlug = sharedCredentialSlug
		if sharedCredentialSlug == "" {
			credential, err := s.credentials.CreateStaticSecret(ctx, credentialssvc.CreateStaticSecretParams{
				Owner: credentialssvc.CredentialOwner{
					OrgSlug:        orgSlug,
					DataSourceSlug: params.Slug,
				},
				CredentialType: staticCredentialTypeForDataSource(params.Type),
				PlaintextJSON:  credentialsJSON,
				Actor:          actor,
			})
			if err != nil {
				return xerror.Wrapf("create data source credential: %w", err)
			}
			repoParams.CredentialSlug = credential.Slug
		}

		// Create in repository
		repoDS, err = creator.Create(ctx, tx, orgSlug, repoParams)
		if err != nil {
			return xerror.Wrapf("create data source in repo: %w", err)
		}
		if sharedCredentialSlug != "" {
			var config gitconnector.ProviderConfig
			if err := json.Unmarshal(params.ConfigJSON, &config); err != nil {
				return err
			}
			if err := s.credentials.AttachOrganizationGitLabConnection(ctx, credentialssvc.AttachOrganizationGitLabConnectionParams{OrganizationSlug: orgSlug, DataSourceSlug: params.Slug, ExpectedRepositoryID: config.Repository.ID, ExpectedCredentialSlug: sharedCredentialSlug}); err != nil {
				return err
			}
			repoDS, err = s.repo.FindBySlug(ctx, tx, orgSlug, params.Slug)
			if err != nil {
				return err
			}
		}
		if err := s.syncSnowflakeWIF(ctx, tx, orgSlug, repoDS, credentialsJSON, false); err != nil {
			return err
		}
		if err := s.recordDataSourceEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			repoDS,
			model.EventActionCreate,
		); err != nil {
			return xerror.Wrapf("record data source create event: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	testDS := repoDS
	if managedProviderCreate {
		cloned := *repoDS
		cloned.Config = validationConfigJSON
		testDS = &cloned
	}
	repoDS, err = s.testAfterSave(ctx, orgSlug, testDS, validationCredentialsJSON)
	if err != nil {
		if xerror.Is(err, ErrConnectionTestConflict) {
			return nil, err
		}
		if xerror.Is(err, ErrConnectionFailed) {
			return nil, ErrInvalidInput.
				Detailf("failed to establish connection: %w", err).
				WithPublicDetail(retainedDataSourceCreateFailureDetail(params.Slug))
		}
		return nil, ErrInvalidInput.Detailf("failed to establish connection: %w", err)
	}
	repoDS, err = s.findRepoDataSource(ctx, orgSlug, params.Slug)
	if err != nil {
		return nil, xerror.Unexpected().With(err).WithPublicDetail(
			"Data source " + params.Slug.String() + " was created, but its final state could not be read. Inspect it with `data-source get " + params.Slug.String() + "` before retrying create.",
		)
	}
	ret, err := toDataSourceFromRepo(repoDS)
	if err != nil {
		return nil, xerror.Wrapf("convert created data source: %w", err)
	}
	return ret, nil
}

func retainedDataSourceCreateFailureDetail(dsSlug slug.Slug) string {
	return "Data source " + dsSlug.String() + " was created but remains inactive because its connection test failed. " +
		"Inspect it with `data-source get " + dsSlug.String() + "`; correct it with `data-source update --file <request.json>` or `data-source credentials update --file <request.json>` using slug " + dsSlug.String() + ", then retry with `data-source test-stored " + dsSlug.String() + "`."
}

func (s *service) normalizeGitHubCreate(
	ctx context.Context,
	orgSlug slug.Slug,
	params *CreateParams,
) ([]byte, []byte, error) {
	var config struct {
		RepositoryID string `json:"repository_id"`
		BasePath     string `json:"base_path"`
	}
	if err := json.Unmarshal(params.ConfigJSON, &config); err != nil || strings.TrimSpace(config.RepositoryID) == "" {
		return nil, nil, ErrInvalidInput.Detail("GitHub repository ID is required")
	}
	repository, err := s.githubRepository(ctx, orgSlug, config.RepositoryID)
	if err != nil {
		return nil, nil, err
	}
	repositoryID, _ := strconv.ParseInt(repository.ID, 10, 64)
	token, err := s.githubApps.Token(ctx, orgSlug, []int64{repositoryID})
	if err != nil {
		return nil, nil, xerror.Wrapf("mint GitHub repository token: %w", err)
	}
	managed := gitconnector.ProviderConfig{Repository: gitconnector.ProviderRepository{
		ID: repository.ID, Name: repository.Name, FullName: repository.FullName,
		WebURL: repository.WebURL, DefaultBranch: repository.DefaultBranch,
	}, BasePath: config.BasePath}
	params.ConfigJSON, err = json.Marshal(managed)
	if err != nil {
		return nil, nil, xerror.Unexpected().With(err)
	}
	params.Credentials = map[string]any{"auth_type": "https", "username": "x-access-token"}
	cloneURL := url.URL{Scheme: "https", Host: "github.com", Path: "/" + repository.FullName + ".git"}
	runtimeConfig, err := json.Marshal(gitconnector.Config{RepoURL: cloneURL.String(), BasePath: config.BasePath})
	if err != nil {
		return nil, nil, xerror.Unexpected().With(err)
	}
	runtimeCredentials, err := json.Marshal(gitconnector.Credentials{AuthType: gitconnector.AuthTypeHTTPS, Username: "x-access-token", Token: token.Token})
	if err != nil {
		return nil, nil, xerror.Unexpected().With(err)
	}
	return runtimeConfig, runtimeCredentials, nil
}

// FindBySlug retrieves a data source by slug.
func (s *service) FindBySlug(ctx context.Context, orgSlug, dsSlug slug.Slug) (*DataSource, error) {
	return postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*DataSource, error) {
		ds, err := s.findBySlug(ctx, db, orgSlug, dsSlug)
		if err != nil {
			return nil, xerror.Wrapf("find data source: %w", err)
		}
		return ds, nil
	})
}

// ListByOrg lists all data sources in an organization.
func (s *service) ListByOrg(
	ctx context.Context,
	orgSlug slug.Slug,
	req model.ListRequest,
) (model.ListResponse[DataSource], error) {
	normalized, queryReq := model.PrepareListQuery(req, model.ListSortCreatedAt)

	var result []DataSource

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		repoList, err := s.repo.ListByOrg(ctx, tx, orgSlug, queryReq)
		if err != nil {
			return xerror.Wrapf("list data sources in repo: %w", err)
		}

		result = make([]DataSource, len(repoList))
		for i, repoDS := range repoList {
			dataSource, err := toDataSourceFromRepo(repoDS)
			if err != nil {
				return xerror.Wrapf("convert listed data source: %w", err)
			}
			result[i] = *dataSource
		}

		return nil
	})
	if err != nil {
		return model.ListResponse[DataSource]{}, err
	}

	result, hasMore := model.FinalizePage(result, normalized)
	nextCursor, prevCursor := model.BuildBoundaryCursors(
		result,
		normalized,
		hasMore,
		func(item DataSource, sortBy string) (string, string) {
			cursorValue := item.CreatedAt.Format(time.RFC3339)
			switch sortBy {
			case model.ListSortUpdatedAt:
				cursorValue = item.UpdatedAt.Format(time.RFC3339)
			case model.ListSortName:
				cursorValue = item.Name
			}
			return cursorValue, item.Slug.String()
		},
	)

	return model.ListResponse[DataSource]{
		Items:      result,
		HasMore:    hasMore,
		NextCursor: nextCursor,
		PrevCursor: prevCursor,
	}, nil
}

// Update updates an existing data source.
func (s *service) Update(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	params *UpdateParams,
) (*DataSource, error) {
	if params == nil {
		return nil, ErrInvalidInput.Detail("at least one data source field must be provided")
	}
	// Validate input using struct tags
	if err := validate.Default().Struct(params); err != nil {
		return nil, ErrInvalidInput.With(err)
	}
	if params.Name == "" && len(params.ConfigJSON) == 0 && params.IsActive == nil &&
		!params.ReadOnlySet {
		return nil, ErrInvalidInput.Detail("at least one data source field must be provided")
	}

	snapshot, credentialsJSON, err := s.snapshotDataSourceAndCredentials(ctx, orgSlug, dsSlug)
	if err != nil {
		return nil, xerror.Wrapf("prepare data source update: %w", err)
	}
	repoParamsInput := *params

	if _, managed := managedProviderGitConfig(snapshot); managed && len(params.ConfigJSON) > 0 {
		return nil, ErrInvalidInput.Detail("provider repository settings must be changed through verified repository selection")
	}

	if len(params.ConfigJSON) > 0 && params.ConfigFromCustomer {
		normalizedConfig, ok, err := connector.NormalizeCustomerConfig(snapshot.Type, repoParamsInput.ConfigJSON)
		if err != nil {
			return nil, ErrInvalidInput.With(err)
		}
		if !ok {
			return nil, ErrInvalidDataSourceType
		}
		repoParamsInput.ConfigJSON = normalizedConfig
	}

	if snapshot.Type == model.DataSourceTypeGoogleWorkspace && len(params.ConfigJSON) > 0 {
		var current, candidate workspace.Config
		if err := json.Unmarshal(snapshot.Config, &current); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		if err := current.Validate(); err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		if err := json.Unmarshal(repoParamsInput.ConfigJSON, &candidate); err != nil {
			return nil, ErrInvalidInput.With(err)
		}
		if err := candidate.Validate(); err != nil {
			return nil, ErrInvalidInput.With(err)
		}
		if candidate.AccessMode == "" {
			candidate.AccessMode = current.ConsentMode()
		}
		if candidate.ConsentMode() != current.ConsentMode() {
			return nil, ErrInvalidInput.Detail("Workspace consent changes require reconnecting Google")
		}
		repoParamsInput.ConfigJSON, err = json.Marshal(candidate)
		if err != nil {
			return nil, ErrInvalidInput.With(err)
		}
	}

	repoParams := toRepoUpdateParams(&repoParamsInput, snapshot)
	if len(repoParamsInput.ConfigJSON) > 0 {
		if err := s.validateConfigAndCredentials(
			snapshot.Type,
			repoParams.ConfigJSON,
			credentialsJSON,
		); err != nil {
			if xerror.Is(err, ErrInvalidDataSourceType) {
				return nil, err
			}
			return nil, ErrInvalidInput.With(err)
		}
	}
	forceReadOnly, err := shouldForceReadOnlyFromCredentialsJSON(snapshot.Type, credentialsJSON)
	if err != nil {
		return nil, xerror.Wrapf("check credentials for read_only enforcement: %w", err)
	}

	var repoDS *repods.DataSource

	err = s.txm.RunInTx(postgres.WithoutTransaction(ctx), func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.repo.LockDataSourceForUpdate(ctx, tx, orgSlug, dsSlug); err != nil {
			return xerror.Wrapf("lock data source for update: %w", err)
		}
		current, err := s.repo.FindBySlug(ctx, tx, orgSlug, dsSlug)
		if err != nil {
			return xerror.Wrapf("find locked data source for update in repo: %w", err)
		}
		if current.ConnectionTestRevision != snapshot.ConnectionTestRevision {
			return ErrConnectionTestConflict.Detail("data source changed while preparing update")
		}

		if err := rejectProjectBackendOwnershipBreakingUpdate(ctx, tx, s.repo, orgSlug, dsSlug, current, &repoParamsInput); err != nil {
			return err
		}

		repoDS, err = s.repo.Update(ctx, tx, orgSlug, dsSlug, repoParams)
		if err != nil {
			return xerror.Wrapf("update data source in repo: %w", err)
		}
		if len(params.ConfigJSON) > 0 || params.IsActive != nil {
			if err := s.syncSnowflakeWIF(ctx, tx, orgSlug, repoDS, credentialsJSON, params.IsActive != nil && !*params.IsActive); err != nil {
				return err
			}
		}

		if forceReadOnly {
			if err := s.repo.SetReadOnly(ctx, tx, orgSlug, dsSlug, true); err != nil {
				return xerror.Wrapf("force data source read_only in repo: %w", err)
			}
			repoDS.ReadOnly = true
		}

		if changed, err := httpOpenAPIImportConfigChanged(current, repoDS); err != nil {
			return err
		} else if changed {
			if err := s.schemaRepo.DeleteByDataSourceSlug(ctx, tx, orgSlug, dsSlug); err != nil {
				return xerror.Wrapf("delete stale HTTP OpenAPI schema metadata: %w", err)
			}
			if _, err := s.schemaRepo.ResetImportByDataSourceSlug(ctx, tx, orgSlug, dsSlug); err != nil {
				return xerror.Wrapf("reset stale HTTP OpenAPI schema import state: %w", err)
			}
		}

		if err := s.recordDataSourceEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			repoDS,
			model.EventActionUpdate,
		); err != nil {
			return xerror.Wrapf("record data source update event: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if repoDS.Type == model.DataSourceTypeSnowflake && params.IsActive != nil && !*params.IsActive {
		return toDataSourceFromRepo(repoDS)
	}
	repoDS, err = s.testAfterSave(ctx, orgSlug, repoDS, credentialsJSON)
	if err != nil {
		if xerror.Is(err, ErrConnectionTestConflict) {
			return nil, err
		}
		return nil, ErrInvalidInput.Detailf(
			"failed to establish connection with updated config: %w",
			err,
		)
	}
	ret, err := toDataSourceFromRepo(repoDS)
	if err != nil {
		return nil, xerror.Wrapf("convert updated data source: %w", err)
	}
	return ret, nil
}

func rejectProjectBackendOwnershipBreakingUpdate(
	ctx context.Context,
	tx postgres.DBTX,
	repo repods.Repository,
	orgSlug, dsSlug slug.Slug,
	current *repods.DataSource,
	params *UpdateParams,
) error {
	if current == nil {
		return nil
	}
	if current.Type != model.DataSourceTypeGit &&
		current.Type != model.DataSourceTypeGithub &&
		current.Type != model.DataSourceTypeGitlab {
		return nil
	}

	hasProjectBackendReferences, err := repo.DataSourceHasProjectBackendReferences(
		ctx,
		tx,
		orgSlug,
		dsSlug,
	)
	if err != nil {
		return xerror.Wrapf("check project pipeline backend references for update: %w", err)
	}
	if !hasProjectBackendReferences {
		return nil
	}

	if params.ReadOnlySet && params.ReadOnly {
		return ErrDataSourceUsedAsProjectBackend
	}

	if len(params.ConfigJSON) == 0 {
		return nil
	}

	var currentConfig gitconnector.Config
	if err := json.Unmarshal(current.Config, &currentConfig); err != nil {
		return xerror.Wrapf("decode current git data source config: %w", err)
	}

	var updatedConfig gitconnector.Config
	if err := json.Unmarshal(params.ConfigJSON, &updatedConfig); err != nil {
		return xerror.Wrapf("decode updated git data source config: %w", err)
	}

	if repostore.NormalizeRemoteURL(currentConfig.RepoURL) != repostore.NormalizeRemoteURL(updatedConfig.RepoURL) {
		return ErrDataSourceUsedAsProjectBackend
	}

	return nil
}

// httpOpenAPIImportConfigChanged reports whether an HTTP update changed cached import inputs.
func httpOpenAPIImportConfigChanged(
	current, updated *repods.DataSource,
) (bool, error) {
	if current == nil || updated == nil || current.Type != model.DataSourceTypeHTTP {
		return false, nil
	}
	if current.Type != updated.Type {
		return false, nil
	}

	var currentConfig httpconnector.Config
	if err := json.Unmarshal(current.Config, &currentConfig); err != nil {
		return false, xerror.Wrapf("decode current HTTP data source config: %w", err)
	}

	var updatedConfig httpconnector.Config
	if err := json.Unmarshal(updated.Config, &updatedConfig); err != nil {
		return false, xerror.Wrapf("decode updated HTTP data source config: %w", err)
	}

	return strings.TrimSpace(currentConfig.BaseURL) != strings.TrimSpace(updatedConfig.BaseURL) ||
		strings.TrimSpace(currentConfig.OpenAPIURL) != strings.TrimSpace(updatedConfig.OpenAPIURL) ||
		currentConfig.AllowPrivateHosts != updatedConfig.AllowPrivateHosts ||
		currentConfig.OpenAPIFetchTimeoutSeconds != updatedConfig.OpenAPIFetchTimeoutSeconds, nil
}

// UpdateCredentials updates credentials for an existing data source.
func (s *service) UpdateCredentials(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	params *UpdateCredentialsParams,
) (*DataSource, error) {
	// Validate input using struct tags
	if err := validate.Default().Struct(params); err != nil {
		return nil, ErrInvalidInput.With(err)
	}

	credentialsJSON, err := json.Marshal(params.Credentials)
	if err != nil {
		return nil, ErrInvalidInput.Detail("invalid credentials format")
	}

	var repoDS *repods.DataSource
	pendingOAuth := false

	err = s.txm.RunInTx(postgres.WithoutTransaction(ctx), func(ctx context.Context, tx postgres.DBTX) error {
		repoDS, err = s.repo.FindBySlug(ctx, tx, orgSlug, dsSlug)
		if err != nil {
			return xerror.Wrapf("find data source in repo: %w", err)
		}
		if repoDS.Type == model.DataSourceTypeGoogleWorkspace {
			return ErrInvalidInput.Detail("Google Workspace credentials must be changed through provider authorization")
		}
		if _, managed := managedProviderGitConfig(repoDS); managed {
			return ErrInvalidInput.Detail("managed provider credentials must be changed through provider authorization")
		}

		app, appErr := s.httpOAuthApp(repoDS.Type, repoDS.Config, params.Credentials)
		if appErr != nil {
			return appErr
		}
		if app != nil {
			pendingOAuth = true
			repoDS, err = s.replaceHTTPOAuth(ctx, tx, orgSlug, actor, repoDS, *app)
			return err
		}

		if err := s.validateConfigAndCredentials(
			repoDS.Type,
			repoDS.Config,
			credentialsJSON,
		); err != nil {
			if xerror.Is(err, ErrInvalidDataSourceType) {
				return ErrInvalidDataSourceType.With(err)
			}
			return ErrInvalidInput.With(err)
		}

		forceReadOnly, err := shouldForceReadOnlyFromCredentialsJSON(repoDS.Type, credentialsJSON)
		if err != nil {
			return ErrInvalidInput.With(err)
		}
		if forceReadOnly && !repoDS.ReadOnly && isGitDataSourceType(repoDS.Type) {
			hasProjectBackendReferences, err := s.repo.DataSourceHasProjectBackendReferences(
				ctx,
				tx,
				orgSlug,
				dsSlug,
			)
			if err != nil {
				return xerror.Wrapf("check project pipeline backend references for credentials update: %w", err)
			}
			if hasProjectBackendReferences {
				return ErrDataSourceUsedAsProjectBackend
			}
		}

		credential, err := s.credentials.CreateStaticSecret(ctx, credentialssvc.CreateStaticSecretParams{
			Owner: credentialssvc.CredentialOwner{
				OrgSlug:        orgSlug,
				DataSourceSlug: repoDS.Slug,
			},
			CredentialType: staticCredentialTypeForDataSource(repoDS.Type),
			PlaintextJSON:  credentialsJSON,
			Actor:          actor,
		})
		if err != nil {
			return xerror.Wrapf("update data source credential: %w", err)
		}
		repoDS, err = s.repo.UpdateCredentials(ctx, tx, orgSlug, dsSlug, &repods.UpdateCredentialsParams{
			CredentialSlug: credential.Slug,
		})
		if err != nil {
			return xerror.Wrapf("attach data source credential in repo: %w", err)
		}
		if err := s.syncSnowflakeWIF(ctx, tx, orgSlug, repoDS, credentialsJSON, false); err != nil {
			return err
		}

		if forceReadOnly {
			if err := s.repo.SetReadOnly(ctx, tx, orgSlug, dsSlug, true); err != nil {
				return xerror.Wrapf("force data source read_only in repo: %w", err)
			}
			repoDS.ReadOnly = true
		}

		if err := s.recordDataSourceEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			repoDS,
			model.EventActionRotate,
		); err != nil {
			return xerror.Wrapf("record data source credentials event: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if pendingOAuth {
		return s.FindBySlug(ctx, orgSlug, dsSlug)
	}

	repoDS, err = s.testAfterSave(ctx, orgSlug, repoDS, credentialsJSON)
	if err != nil {
		if xerror.Is(err, ErrConnectionTestConflict) {
			return nil, err
		}
		return nil, ErrInvalidInput.Detailf(
			"failed to establish connection with updated credentials: %w",
			err,
		)
	}
	ret, err := toDataSourceFromRepo(repoDS)
	if err != nil {
		return nil, xerror.Wrapf("convert data source with updated credentials: %w", err)
	}
	return ret, nil
}

// CompleteProviderOAuthCallback stores an OAuth grant only if the updated data source validates.
func (s *service) CompleteProviderOAuthCallback(
	ctx context.Context,
	params credentialssvc.CompleteOAuthCallbackParams,
) (credentialssvc.CompleteOAuthCallbackResult, error) {
	if params.Provider == "google_workspace" {
		params.ValidateWorkspace = s.validateWorkspaceConsent
		params.FinalizeWorkspace = s.finalizeWorkspaceConsent
	}
	completed, err := s.credentials.CompleteOAuthCallback(postgres.WithoutTransaction(ctx), params)
	if err != nil {
		return credentialssvc.CompleteOAuthCallbackResult{}, err
	}
	ctx, cancel := context.WithTimeout(
		postgres.WithoutTransaction(context.WithoutCancel(ctx)),
		providerCallbackContinuationTimeout,
	)
	defer cancel()

	var repoDS *repods.DataSource
	err = s.txm.RunInTx(postgres.WithoutTransaction(ctx), func(ctx context.Context, tx postgres.DBTX) error {
		repoDS, err = s.repo.FindBySlug(ctx, tx, completed.Owner.OrgSlug, completed.Owner.DataSourceSlug)
		if err != nil {
			return xerror.Wrapf("find data source for provider oauth callback: %w", err)
		}
		action := model.EventActionCreate
		if completed.Intent == credentialssvc.OAuthInstallIntentRotate {
			action = model.EventActionRotate
		}
		actor := completed.Actor
		if actor.Type == model.EventActorTypeUnknown {
			actor = model.ExternalEventActor()
		}
		if err := s.recordDataSourceEvent(ctx, tx, completed.Owner.OrgSlug, actor, repoDS, action); err != nil {
			return xerror.Wrapf("record provider oauth callback event: %w", err)
		}
		return nil
	})
	if err != nil {
		return credentialssvc.CompleteOAuthCallbackResult{}, err
	}
	if providerRepositorySelectionPending(repoDS) || params.Provider == "google_workspace" {
		return completed, nil
	}

	repoDS, credentialsJSON, err := s.snapshotDataSourceAndCredentials(
		ctx,
		completed.Owner.OrgSlug,
		completed.Owner.DataSourceSlug,
	)
	if err != nil {
		return credentialssvc.CompleteOAuthCallbackResult{}, xerror.Wrapf(
			"prepare provider credentials for connection test: %w",
			err,
		)
	}
	_, err = s.testConnection(ctx, completed.Owner.OrgSlug, repoDS, credentialsJSON)
	if err != nil {
		if xerror.Is(err, ErrConnectionTestConflict) {
			return credentialssvc.CompleteOAuthCallbackResult{}, err
		}
		return credentialssvc.CompleteOAuthCallbackResult{}, ErrInvalidInput.Detailf(
			"failed to establish connection with updated credentials: %w",
			err,
		)
	}

	return completed, nil
}

// ValidateCredentialLifecycle tests the currently attached credential and records the lifecycle event on success.
func (s *service) ValidateCredentialLifecycle(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	action model.EventAction,
) (*DataSource, error) {
	repoDS, credentialsJSON, err := s.snapshotDataSourceAndCredentials(ctx, orgSlug, dsSlug)
	if err != nil {
		return nil, xerror.Wrapf("prepare credentials for credential validation: %w", err)
	}
	repoDS, err = s.testConnection(ctx, orgSlug, repoDS, credentialsJSON)
	if err != nil {
		if xerror.Is(err, ErrConnectionTestConflict) {
			return nil, err
		}
		return nil, ErrInvalidInput.Detailf(
			"failed to establish connection with updated credentials: %w",
			err,
		)
	}

	err = s.txm.RunInTx(postgres.WithoutTransaction(ctx), func(ctx context.Context, tx postgres.DBTX) error {
		return s.recordDataSourceEvent(ctx, tx, orgSlug, actor, repoDS, action)
	})
	if err != nil {
		return nil, err
	}
	ret, err := toDataSourceFromRepo(repoDS)
	if err != nil {
		return nil, xerror.Wrapf("convert credential-validated data source: %w", err)
	}
	return ret, nil
}

func (s *service) EnsureProofWebhook(ctx context.Context, orgSlug, dsSlug slug.Slug) error {
	dataSource, err := s.findRepoDataSource(ctx, orgSlug, dsSlug)
	if err != nil {
		return err
	}
	_, managed := managedProviderGitConfig(dataSource)
	if !managed || dataSource.Type != model.DataSourceTypeGitlab {
		return ErrInvalidInput.Detail("Proof webhooks require a managed GitLab repository")
	}
	runtimeConfig, _, _, _, err := s.resolveRuntimeConnectorPayload(ctx, orgSlug, dataSource)
	if err != nil {
		return err
	}
	runtimeSource := *dataSource
	runtimeSource.Config = runtimeConfig
	return s.retryGitLabWebhookSetup(ctx, orgSlug, &runtimeSource)
}

func (s *service) retryGitLabWebhookSetup(ctx context.Context, orgSlug slug.Slug, dataSource *repods.DataSource) error {
	if dataSource.Type != model.DataSourceTypeGitlab {
		return nil
	}
	runtimeCredential, err := s.credentials.ResolveRuntimeCredential(
		postgres.WithoutTransaction(ctx),
		credentialssvc.ResolveRuntimeCredentialParams{Owner: credentialssvc.CredentialOwner{
			OrgSlug: orgSlug, DataSourceSlug: dataSource.Slug,
		}},
	)
	if err != nil {
		return xerror.Wrapf("resolve GitLab webhook credential: %w", err)
	}
	if runtimeCredential.Kind == credentialssvc.CredentialKindOAuth2Grant {
		var tokens credentialssvc.OAuthTokenSet
		if err := json.Unmarshal(runtimeCredential.PlaintextJSON, &tokens); err != nil {
			return xerror.InvalidCredentials().With(err)
		}
		if strings.TrimSpace(tokens.AccessToken) == "" {
			return xerror.InvalidCredentials().Detail("GitLab OAuth access token is required")
		}
	} else {
		var credentials gitconnector.Credentials
		if err := json.Unmarshal(runtimeCredential.PlaintextJSON, &credentials); err != nil {
			return xerror.InvalidCredentials().With(err)
		}
		if credentials.Username != "oauth2" || strings.TrimSpace(credentials.Token) == "" {
			return nil
		}
	}
	return s.gitLabWebhooks.Ensure(ctx, orgSlug, dataSource.Slug, dataSource.Config, runtimeCredential.PlaintextJSON)
}

// RecordCredentialLifecycleEvent records a data-source event for credential-only lifecycle changes.
func (s *service) RecordCredentialLifecycleEvent(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
	action model.EventAction,
) error {
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		repoDS, err := s.repo.FindBySlug(ctx, tx, orgSlug, dsSlug)
		if err != nil {
			return xerror.Wrapf("find data source for credential lifecycle event: %w", err)
		}
		return s.recordDataSourceEvent(ctx, tx, orgSlug, actor, repoDS, action)
	})
}

// Delete soft-deletes a data source.
func (s *service) Delete(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
) error {
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		repoDS, err := s.repo.FindBySlug(ctx, tx, orgSlug, dsSlug)
		if err != nil {
			return xerror.Wrapf("find data source for delete in repo: %w", err)
		}
		if err := s.repo.Delete(ctx, tx, orgSlug, dsSlug); err != nil {
			return err
		}
		if err := s.syncSnowflakeWIF(ctx, tx, orgSlug, repoDS, nil, true); err != nil {
			return err
		}
		return s.recordDataSourceEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			repoDS,
			model.EventActionDelete,
		)
	})
	if err != nil {
		return xerror.Wrapf("delete data source: %w", err)
	}
	return nil
}

// Restore clears the deletion tombstone for a data source.
func (s *service) Restore(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
) error {
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		if err := s.repo.Restore(ctx, tx, orgSlug, dsSlug); err != nil {
			return err
		}
		repoDS, err := s.repo.FindBySlug(ctx, tx, orgSlug, dsSlug)
		if err != nil {
			return xerror.Wrapf("find restored data source in repo: %w", err)
		}
		return s.recordDataSourceEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			repoDS,
			model.EventActionRestore,
		)
	})
	if err != nil {
		return xerror.Wrapf("restore data source: %w", err)
	}
	return nil
}

// ResolveGitAuth decrypts the data source credentials and returns the git
// authentication components needed for raw git operations (CheckRemote, CloneRemote, etc.).
func (s *service) ResolveGitAuth(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (*GitDataSourceAuth, error) {
	repoDS, err := postgres.InRead(
		postgres.WithoutTransaction(ctx),
		s.txm,
		func(ctx context.Context, db postgres.DBTX) (*repods.DataSource, error) {
			return s.repo.FindBySlug(ctx, db, orgSlug, dsSlug)
		},
	)
	if err != nil {
		return nil, err
	}
	if repoDS.Type != model.DataSourceTypeGit &&
		repoDS.Type != model.DataSourceTypeGithub &&
		repoDS.Type != model.DataSourceTypeGitlab {
		return nil, ErrInvalidDataSourceType.Detail(
			"data source must be of type git, github, or gitlab",
		)
	}
	runtimeConfig, decryptedCreds, _, installationToken, err := s.resolveRuntimeConnectorPayload(ctx, orgSlug, repoDS)
	if err != nil {
		return nil, err
	}
	var creds gitconnector.Credentials
	if err := json.Unmarshal(decryptedCreds, &creds); err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	var cfg gitconnector.Config
	if err := json.Unmarshal(runtimeConfig, &cfg); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if creds.AuthType == gitconnector.AuthTypeHTTPS && strings.TrimSpace(creds.Username) == "" {
		if repoDS.Type == model.DataSourceTypeGithub {
			creds.Username = "x-access-token"
		}
		if repoDS.Type == model.DataSourceTypeGitlab {
			creds.Username = "oauth2"
		}
	}

	env, modifyURL, cleanup, err := gitconnector.AuthEnv(creds)
	if err != nil {
		return nil, xerror.Wrapf("prepare git auth: %w", err)
	}

	env, _ = appendProviderPATEnv(repoDS.Type, creds, env)

	repoURL, err := modifyURL(cfg.RepoURL)
	if err != nil {
		cleanup()
		return nil, xerror.Wrapf("build git repo URL: %w", err)
	}
	repoURL, env, err = s.prepareGitRemote(
		ctx,
		repoURL,
		env,
		hostguard.NewDestinationPolicy(cfg.AllowPrivateHosts),
	)
	if err != nil {
		cleanup()
		return nil, xerror.Wrapf("prepare pinned Git destination: %w", err)
	}

	credentialSlug, credentialRevision := "", ""
	if repoDS.Credential != nil {
		credentialSlug = repoDS.Credential.Slug.String()
		credentialRevision = repoDS.Credential.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}

	return &GitDataSourceAuth{
		GitConfigurationHash:     model.GitConfigurationHash(repoDS.Config),
		GitWritable:              repoDS.IsActive && !repoDS.ReadOnly,
		GitDataSourceSlug:        dsSlug.String(),
		GitCredentialSlug:        credentialSlug,
		GitCredentialRevision:    credentialRevision,
		Provider:                 repoDS.Type.String(),
		Env:                      env,
		RepoURL:                  repoURL,
		DefaultBranch:            cfg.DefaultBranch,
		GitHubContentsPermission: gitHubContentsPermission(installationToken),
		GitHubProposalAuthority:  gitHubProposalAuthority(repoDS, installationToken),
		Cleanup:                  cleanup,
	}, nil
}

// ResolveGitExecutionAuth decrypts only the operation-scoped Git datasource
// inputs. It deliberately does not create temporary key files or resolve the
// destination; those actions belong on the isolated executor at dial time.
func (s *service) ResolveGitExecutionAuth(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (*GitExecutionAuth, error) {
	repoDS, err := postgres.InRead(
		postgres.WithoutTransaction(ctx),
		s.txm,
		func(ctx context.Context, db postgres.DBTX) (*repods.DataSource, error) {
			return s.repo.FindBySlug(ctx, db, orgSlug, dsSlug)
		},
	)
	if err != nil {
		return nil, err
	}
	if repoDS.Type != model.DataSourceTypeGit &&
		repoDS.Type != model.DataSourceTypeGithub &&
		repoDS.Type != model.DataSourceTypeGitlab {
		return nil, ErrInvalidDataSourceType.Detail("data source must be of type git, github, or gitlab")
	}
	runtimeConfig, decrypted, credentialReference, installationToken, err := s.resolveRuntimeConnectorPayload(ctx, orgSlug, repoDS)
	if err != nil {
		return nil, err
	}
	credentialSlug, credentialRevision := "", ""
	if repoDS.Credential != nil {
		credentialSlug = repoDS.Credential.Slug.String()
		credentialRevision = repoDS.Credential.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}

	return &GitExecutionAuth{
		GitConfigurationHash:     model.GitConfigurationHash(repoDS.Config),
		GitWritable:              repoDS.IsActive && !repoDS.ReadOnly,
		GitDataSourceSlug:        dsSlug.String(),
		GitCredentialSlug:        credentialSlug,
		GitCredentialRevision:    credentialRevision,
		CredentialReference:      credentialReference,
		DataSourceType:           repoDS.Type,
		ConfigJSON:               runtimeConfig,
		CredentialsJSON:          decrypted,
		GitHubContentsPermission: gitHubContentsPermission(installationToken),
		GitHubProposalAuthority:  gitHubProposalAuthority(repoDS, installationToken),
	}, nil
}

// PrepareGitExecutionAuth constructs ephemeral Git credentials and pins the
// destination on the executor immediately before the network operation.
func PrepareGitExecutionAuth(
	ctx context.Context,
	request GitExecutionAuth,
) (*GitDataSourceAuth, error) {
	var creds gitconnector.Credentials
	if err := json.Unmarshal(request.CredentialsJSON, &creds); err != nil {
		return nil, xerror.Unexpected().Detail("invalid Git credentials")
	}
	var cfg gitconnector.Config
	if err := json.Unmarshal(request.ConfigJSON, &cfg); err != nil {
		return nil, xerror.Unexpected().Detail("invalid Git configuration")
	}
	if creds.AuthType == gitconnector.AuthTypeHTTPS && strings.TrimSpace(creds.Username) == "" {
		if request.DataSourceType == model.DataSourceTypeGithub {
			creds.Username = "x-access-token"
		}
		if request.DataSourceType == model.DataSourceTypeGitlab {
			creds.Username = "oauth2"
		}
	}
	env, modifyURL, cleanup, err := gitconnector.AuthEnv(creds)
	if err != nil {
		return nil, xerror.Wrapf("prepare Git auth: %w", err)
	}
	env, _ = appendProviderPATEnv(request.DataSourceType, creds, env)
	repoURL, err := modifyURL(cfg.RepoURL)
	if err != nil {
		cleanup()
		return nil, xerror.Wrapf("build Git repository URL: %w", err)
	}
	repoURL, env, err = repostore.PrepareRemote(
		ctx, repoURL, env, hostguard.NewDestinationPolicy(cfg.AllowPrivateHosts),
	)
	if err != nil {
		cleanup()
		return nil, xerror.Wrapf("prepare pinned Git destination: %w", err)
	}
	return &GitDataSourceAuth{
		GitConfigurationHash:  request.GitConfigurationHash,
		GitWritable:           request.GitWritable,
		GitDataSourceSlug:     request.GitDataSourceSlug,
		GitCredentialSlug:     request.GitCredentialSlug,
		GitCredentialRevision: request.GitCredentialRevision,
		Provider:              request.DataSourceType.String(), Env: env, RepoURL: repoURL,
		DefaultBranch: cfg.DefaultBranch, GitHubContentsPermission: request.GitHubContentsPermission, GitHubProposalAuthority: request.GitHubProposalAuthority, Cleanup: cleanup,
	}, nil
}

// ListBranches returns remote branches for a git-compatible data source.
func (s *service) ListBranches(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) ([]DataSourceBranch, error) {
	auth, err := s.ResolveGitExecutionAuth(ctx, orgSlug, dsSlug)
	if err != nil {
		return nil, err
	}
	return s.gitBranchLister.ListGitBranches(ctx, GitBranchRequest{
		OrganizationSlug: orgSlug,
		Auth:             *auth,
		MaxResultBytes:   s.gitBranchMaxBytes,
		MaxResultItems:   s.gitBranchMaxItems,
	})
}

// dataSourceBranchesFromRemote maps remote git branches to service branches and applies configured defaults.
func dataSourceBranchesFromRemote(
	branches []repostore.RemoteBranch,
	configuredDefault string,
) []DataSourceBranch {
	configuredDefault = strings.TrimSpace(configuredDefault)
	defaultBranch := ""
	for _, branch := range branches {
		if branch.IsDefault {
			defaultBranch = branch.Name
			break
		}
	}
	if defaultBranch == "" {
		defaultBranch = configuredDefault
	}

	result := make([]DataSourceBranch, 0, len(branches))
	for _, branch := range branches {
		result = append(result, DataSourceBranch{
			Name:      branch.Name,
			IsDefault: branch.Name == defaultBranch,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDefault != result[j].IsDefault {
			return result[i].IsDefault
		}
		return result[i].Name < result[j].Name
	})

	return result
}

func appendProviderPATEnv(
	dataSourceType model.DataSourceType,
	creds gitconnector.Credentials,
	env []string,
) ([]string, bool) {
	if creds.AuthType != gitconnector.AuthTypeHTTPS || strings.TrimSpace(creds.Token) == "" {
		return env, false
	}

	switch dataSourceType {
	case model.DataSourceTypeGithub:
		return append(env, "GITHUB_TOKEN="+creds.Token), true
	case model.DataSourceTypeGitlab:
		return append(env, "GITLAB_TOKEN="+creds.Token), true
	default:
		return env, false
	}
}

// IntrospectSchema returns cached schema metadata without contacting the provider.
// A cache miss still verifies the data source exists, then returns that connector's
// empty schema shape so callers can render immediately and trigger RefreshSchema
// explicitly when they need live provider introspection.
func (s *service) IntrospectSchema(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (SchemaResult, error) {
	var result SchemaResult

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		cached, err := s.schemaRepo.FindByDataSourceSlug(ctx, tx, orgSlug, dsSlug)
		if err == nil {
			schema, err := decodeSchemaJSON(cached.SchemaData)
			if err == nil {
				result = schema
				return nil
			}
			// Treat malformed cache like a cache miss so reads remain non-blocking.
			// RefreshSchema is still the explicit path that rewrites bad metadata.
		} else if !xerror.Is(err, repods.ErrSchemaMetadataNotFound) {
			return xerror.Wrapf("find cached schema: %w", err)
		}

		ds, err := s.repo.FindBySlug(ctx, tx, orgSlug, dsSlug)
		if err != nil {
			return xerror.Wrapf("find data source in repo: %w", err)
		}

		schema, err := emptySchemaForType(ds.Type)
		if err != nil {
			return err
		}

		result = schema
		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// RefreshSchema queues live schema introspection and returns the persisted import state.
func (s *service) RefreshSchema(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	actor model.EventActor,
) (*SchemaImportResult, error) {
	var result *SchemaImportResult
	requestedAt := time.Now().UTC()
	attemptID := uuid.New().String()

	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		ds, err := s.repo.FindBySlug(ctx, tx, orgSlug, dsSlug)
		if err != nil {
			return xerror.Wrapf("find data source in repo: %w", err)
		}

		state, err := s.schemaRepo.StartImportByDataSourceSlug(ctx, tx, orgSlug, dsSlug, attemptID, requestedAt)
		if err != nil {
			return xerror.Wrapf("start data source schema import: %w", err)
		}

		result = schemaImportResultFromRepoState(state)
		if !state.Started {
			return nil
		}

		queued, err := s.schemaImportProducer.EnqueueDataSourceSchemaImport(
			ctx,
			tx,
			jobs.DataSourceSchemaImportArgs{
				OrganizationSlug: orgSlug,
				DataSourceSlug:   dsSlug,
				AttemptID:        attemptID,
				RequestedAt:      requestedAt,
				Actor:            actor,
			},
		)
		if err != nil {
			return xerror.Wrapf("enqueue data source schema import: %w", err)
		}
		result.Enqueued = true
		result.Job = &QueuedSchemaImportJob{
			Kind:  queued.Kind,
			Queue: queued.Queue,
			State: queued.State,
		}

		if err := s.recordDataSourceEvent(
			ctx,
			tx,
			orgSlug,
			actor,
			ds,
			model.EventActionRefresh,
		); err != nil {
			return xerror.Wrapf("record data source schema refresh event: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

// ExecuteQueuedSchemaImport performs live provider introspection for one queued job and updates cache state.
func (s *service) ExecuteQueuedSchemaImport(
	ctx context.Context,
	args jobs.DataSourceSchemaImportArgs,
) (*SchemaImportResult, error) {
	ds, previous, hasPrevious, err := s.loadSchemaImportInputs(
		ctx,
		args.OrganizationSlug,
		args.DataSourceSlug,
	)
	if err != nil {
		if xerror.Is(err, repods.ErrDataSourceNotFound) {
			return &SchemaImportResult{Skipped: true}, nil
		}
		return nil, err
	}

	schema, err := s.introspectSchema(ctx, args.OrganizationSlug, ds)
	if err != nil {
		result, markErr := s.markSchemaImportFailed(
			ctx,
			args.OrganizationSlug,
			args.DataSourceSlug,
			args.AttemptID,
			err,
		)
		if markErr != nil {
			return nil, markErr
		}
		return result, nil
	}

	return s.persistSchemaImportSuccess(
		ctx,
		args.OrganizationSlug,
		args.DataSourceSlug,
		args.AttemptID,
		schema,
		previous,
		hasPrevious,
	)
}

// loadSchemaImportInputs reads the data source and prior cached schema snapshot needed for one queued import.
func (s *service) loadSchemaImportInputs(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (*repods.DataSource, SchemaResult, bool, error) {
	var ds *repods.DataSource
	var previous SchemaResult
	var hasPrevious bool

	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		var err error
		ds, err = s.repo.FindBySlug(ctx, db, orgSlug, dsSlug)
		if err != nil {
			return xerror.Wrapf("find data source in repo: %w", err)
		}
		previous, hasPrevious, err = s.previousCachedSchema(ctx, db, orgSlug, dsSlug)
		return err
	})
	if err != nil {
		return nil, nil, false, err
	}

	return ds, previous, hasPrevious, nil
}

// persistSchemaImportSuccess stores imported schema metadata, marks the attempt ready, and records drift for current attempts.
func (s *service) persistSchemaImportSuccess(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	attemptID string,
	schema SchemaResult,
	previous SchemaResult,
	hasPrevious bool,
) (*SchemaImportResult, error) {
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	finishedAt := time.Now().UTC()
	result := &SchemaImportResult{Skipped: true}

	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		// A reset or newer import invalidates this queued attempt while provider work is running.
		if _, err := s.schemaRepo.FindCurrentImportAttemptByDataSourceSlug(
			ctx,
			tx,
			orgSlug,
			dsSlug,
			attemptID,
		); err != nil {
			if xerror.Is(err, repods.ErrSchemaImportAttemptNotCurrent) {
				return nil
			}
			return xerror.Wrapf("lock current schema import attempt: %w", err)
		}

		if _, err := s.schemaRepo.UpsertByDataSourceSlug(
			ctx,
			tx,
			orgSlug,
			dsSlug,
			&repods.UpsertSchemaMetadataParams{SchemaData: schemaJSON},
		); err != nil {
			return xerror.Wrapf("upsert schema metadata: %w", err)
		}
		state, err := s.schemaRepo.MarkImportReadyByDataSourceSlug(ctx, tx, orgSlug, dsSlug, attemptID, finishedAt)
		if err != nil {
			if xerror.Is(err, repods.ErrSchemaImportAttemptNotCurrent) {
				return nil
			}
			return xerror.Wrapf("mark schema import ready: %w", err)
		}
		result = schemaImportResultFromRepoState(state)
		if hasPrevious {
			report := sourceSchemaDriftReport(dsSlug, previous, schema)
			if report != nil {
				if err := s.recordSourceSchemaDriftReport(ctx, tx, orgSlug, dsSlug, report); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// markSchemaImportFailed stores sanitized failure diagnostics for the current attempt and ignores stale attempts.
func (s *service) markSchemaImportFailed(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	attemptID string,
	cause error,
) (*SchemaImportResult, error) {
	diagnostic := schemaImportDiagnosticFromError(cause)
	result := &SchemaImportResult{Skipped: true}
	err := s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		state, err := s.schemaRepo.MarkImportFailedByDataSourceSlug(
			ctx,
			tx,
			orgSlug,
			dsSlug,
			&repods.MarkSchemaImportFailedParams{
				AttemptID:    attemptID,
				ErrorCode:    diagnostic.Code,
				ErrorMessage: diagnostic.Message,
				FinishedAt:   time.Now().UTC(),
			},
		)
		if err != nil {
			if xerror.Is(err, repods.ErrSchemaImportAttemptNotCurrent) {
				return nil
			}
			return xerror.Wrapf("mark schema import failed: %w", err)
		}
		result = schemaImportResultFromRepoState(state)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// schemaImportDiagnosticFromError converts an import failure into an operator-safe diagnostic.
func schemaImportDiagnosticFromError(err error) SchemaImportDiagnostic {
	diagnostic := SchemaImportDiagnostic{
		Code:    "DATA_SOURCE_SCHEMA_IMPORT_FAILED",
		Message: "Schema import failed.",
	}

	var xe *xerror.Error
	if xerror.As(err, &xe) && xe != nil {
		diagnostic.Code = xe.Code()
		diagnostic.Message = xe.Message()
	}
	if detail := strings.TrimSpace(xerror.PublicDetail(err)); detail != "" {
		diagnostic.Message = detail
	}

	return diagnostic
}

// schemaImportResultFromRepoState maps persisted import state into the service result returned by refresh/status reads.
func schemaImportResultFromRepoState(state *repods.SchemaImportState) *SchemaImportResult {
	if state == nil {
		return &SchemaImportResult{Status: model.DataSourceSchemaImportStatusNotImported}
	}
	return &SchemaImportResult{
		DataSourceSlug: state.DataSourceSlug,
		Status:         state.Status,
		Diagnostic:     toServiceSchemaImportDiagnostic(state.Diagnostic),
		StartedAt:      state.StartedAt,
		FinishedAt:     state.FinishedAt,
	}
}

func (s *service) previousCachedSchema(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (SchemaResult, bool, error) {
	cached, err := s.schemaRepo.FindByDataSourceSlug(ctx, tx, orgSlug, dsSlug)
	if err != nil {
		if xerror.Is(err, repods.ErrSchemaMetadataNotFound) {
			return nil, false, nil
		}
		return nil, false, xerror.Wrapf("find previous cached schema: %w", err)
	}
	schema, err := decodeSchemaJSON(cached.SchemaData)
	if err != nil || schema == nil {
		// Treat malformed historical cache data as a miss so refresh can replace it.
		return previousCachedSchemaMiss()
	}
	return schema, true, nil
}

func previousCachedSchemaMiss() (SchemaResult, bool, error) {
	return nil, false, nil
}

func (s *service) recordSourceSchemaDriftReport(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
	report *pipelinedrift.Report,
) error {
	if s.driftReports == nil || report == nil {
		return nil
	}
	reportSlug, err := slug.WithRandomSuffix(slug.Slug("source-schema-"+dsSlug.String()), 6)
	if err != nil {
		return xerror.InvalidInput(xerror.SchemaMetadata).Detailf("generate source drift report slug: %w", err)
	}
	dataSourceSlug := dsSlug
	params, err := driftImpactCreateParams(reportSlug, &dataSourceSlug, report)
	if err != nil {
		return err
	}
	if err := s.driftReports.SupersedeOpen(ctx, tx, orgSlug, params); err != nil {
		return err
	}
	if _, err := s.driftReports.Create(ctx, tx, orgSlug, params); err != nil {
		return err
	}
	return nil
}

func driftImpactCreateParams(
	reportSlug slug.Slug,
	dataSourceSlug *slug.Slug,
	report *pipelinedrift.Report,
) (driftimpactrepo.CreateParams, error) {
	changes, err := json.Marshal(report.Changes)
	if err != nil {
		return driftimpactrepo.CreateParams{}, xerror.Wrapf("encode source drift changes: %w", err)
	}
	impacts, err := json.Marshal(report.Impacts)
	if err != nil {
		return driftimpactrepo.CreateParams{}, xerror.Wrapf("encode source drift impacts: %w", err)
	}
	return driftimpactrepo.CreateParams{
		Slug:           reportSlug,
		ReportType:     report.Type,
		Status:         report.Status,
		Severity:       report.Severity,
		Summary:        report.Summary,
		ChangesJSONB:   changes,
		ImpactsJSONB:   impacts,
		DataSourceSlug: dataSourceSlug,
	}, nil
}

func (s *service) findBySlug(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug, dsSlug slug.Slug,
) (*DataSource, error) {
	repoDS, err := s.repo.FindBySlug(ctx, tx, orgSlug, dsSlug)
	if err != nil {
		return nil, err
	}

	ret, err := toDataSourceFromRepo(repoDS)
	if err != nil {
		return nil, err
	}
	schema, err := s.schemaRepo.FindByDataSourceSlug(ctx, tx, orgSlug, dsSlug)
	if err != nil {
		if xerror.Is(err, repods.ErrSchemaMetadataNotFound) {
			ret.Schema, err = emptySchemaForType(ret.Type)
			if err != nil {
				return nil, err
			}
			return ret, nil
		}
		return nil, err
	}

	ret.Schema, err = decodeSchemaJSON(schema.SchemaData)
	if err != nil {
		ret.Schema, err = emptySchemaForType(ret.Type)
		if err != nil {
			return nil, err
		}
		return ret, nil
	}
	ret.SchemaCached = true
	return ret, nil
}

func (s *service) recordDataSourceEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	ds *repods.DataSource,
	action model.EventAction,
) error {
	if ds == nil {
		return xerror.Unexpected().Detail("data source event requires data source")
	}

	switch action {
	case model.EventActionCreate,
		model.EventActionUpdate,
		model.EventActionRotate,
		model.EventActionRevoke,
		model.EventActionDelete,
		model.EventActionRestore,
		model.EventActionRefresh:
	default:
		return xerror.InvalidInput(xerror.Event).Detail("unsupported data source event action")
	}

	if _, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, dataSourceEvent{
		dataSourceSlug: ds.Slug,
		dataSourceType: ds.Type,
		action:         action,
	}); err != nil {
		return xerror.Wrapf("enqueue required data source audit event: %w", err)
	}
	return nil
}

// testConnection performs connector I/O without a database transaction, then stores the
// outcome in a short transaction if the tested data source revision is still current.
func (s *service) testConnection(
	ctx context.Context,
	orgSlug slug.Slug,
	ds *repods.DataSource,
	credentialsJSON []byte,
) (*repods.DataSource, error) {
	testErr := s.runConnectionTest(
		postgres.WithoutTransaction(ctx),
		orgSlug,
		ds.Slug,
		ds.Type,
		append([]byte(nil), ds.Config...),
		append([]byte(nil), credentialsJSON...),
	)
	if executiondelivery.IsNotExecuted(testErr) {
		return ds, testErr
	}

	finalizeBase := postgres.WithoutTransaction(context.WithoutCancel(ctx))
	finalizeCtx, cancel := context.WithTimeout(finalizeBase, connectionTestFinalizeTimeout)
	defer cancel()
	testedAt := time.Now().UTC()

	var finalized *repods.DataSource
	finalizeErr := s.txm.RunInTx(finalizeCtx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		if testErr != nil {
			finalized, err = s.repo.MarkTestFailure(
				ctx,
				tx,
				orgSlug,
				ds.Slug,
				ds.ConnectionTestRevision,
				ConnectionTestDiagnostic(testErr),
				nil,
			)
			if err != nil {
				return xerror.Wrapf("record connection test failure: %w", err)
			}
			return nil
		}

		finalized, err = s.recordConnectionSuccess(
			ctx,
			tx,
			orgSlug,
			ds.Slug,
			&repods.MarkTestSuccessParams{
				ExpectedRevision: ds.ConnectionTestRevision,
				TestedAt:         testedAt,
			},
		)
		if err != nil {
			return xerror.Wrapf("record connection test success: %w", err)
		}
		return nil
	})
	if finalizeErr != nil {
		return nil, finalizeErr
	}
	result := *ds
	result.ConnectionTestRevision = finalized.ConnectionTestRevision
	result.LastTestedAt = finalized.LastTestedAt
	result.UpdatedAt = finalized.UpdatedAt
	result.IsActive = finalized.IsActive
	result.TestError = finalized.TestError
	if testErr != nil {
		return &result, testErr
	}
	return &result, nil
}

func (s *service) recordConnectionSuccess(ctx context.Context, tx postgres.DBTX, org, source slug.Slug, params *repods.MarkTestSuccessParams) (*repods.DataSource, error) {
	finalized, err := s.repo.MarkTestSuccess(ctx, tx, org, source, params)
	if err != nil {
		return nil, err
	}
	if err := s.admission.DataSourceConnected(ctx, org, source, params.TestedAt); err != nil {
		return nil, xerror.Wrapf("apply data-source connection policy: %w", err)
	}
	return finalized, nil
}

func (s *service) runConnectionTest(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
	dsType model.DataSourceType,
	configJSON []byte,
	credentialsJSON []byte,
) error {
	err := s.connectionTester.TestConnection(ctx, ConnectionTestRequest{
		OrganizationSlug: orgSlug,
		DataSourceSlug:   dsSlug,
		DataSourceType:   dsType,
		ConfigJSON:       configJSON,
		CredentialsJSON:  credentialsJSON,
	})
	if executiondelivery.IsNotExecuted(err) {
		return err
	}
	if err != nil {
		return ErrConnectionFailed.With(err).WithPublicDetail(ConnectionTestDiagnostic(err))
	}
	return nil
}

func shouldForceReadOnlyFromCredentialsJSON(
	dsType model.DataSourceType,
	credentialsJSON []byte,
) (bool, error) {
	if dsType != model.DataSourceTypeGit &&
		dsType != model.DataSourceTypeGithub &&
		dsType != model.DataSourceTypeGitlab {
		return false, nil
	}

	var creds gitconnector.Credentials
	if err := json.Unmarshal(credentialsJSON, &creds); err != nil {
		return false, xerror.Unexpected().With(err)
	}

	return creds.AuthType == gitconnector.AuthTypeHTTPS && creds.Username == "" && creds.Token == "", nil
}

func isGitDataSourceType(dsType model.DataSourceType) bool {
	return dsType == model.DataSourceTypeGit ||
		dsType == model.DataSourceTypeGithub ||
		dsType == model.DataSourceTypeGitlab
}

func staticCredentialTypeForDataSource(dsType model.DataSourceType) credentialssvc.StaticCredentialType {
	switch dsType {
	case model.DataSourceTypePostgres:
		return credentialssvc.StaticCredentialTypePostgres
	case model.DataSourceTypeMySQL:
		return credentialssvc.StaticCredentialTypeMySQL
	case model.DataSourceTypeBigQuery:
		return credentialssvc.StaticCredentialTypeBigQuery
	case model.DataSourceTypeSnowflake:
		return credentialssvc.StaticCredentialTypeSnowflake
	case model.DataSourceTypeHTTP:
		return credentialssvc.StaticCredentialTypeHTTP
	case model.DataSourceTypeHubSpot:
		return credentialssvc.StaticCredentialTypeHubSpot
	case model.DataSourceTypeMongoDB:
		return credentialssvc.StaticCredentialTypeMongoDB
	case model.DataSourceTypeFTP:
		return credentialssvc.StaticCredentialTypeFTP
	case model.DataSourceTypeGit, model.DataSourceTypeGithub, model.DataSourceTypeGitlab:
		return credentialssvc.StaticCredentialTypeGit
	case model.DataSourceTypeS3:
		return credentialssvc.StaticCredentialTypeS3
	default:
		return credentialssvc.StaticCredentialTypePostgres
	}
}

func (s *service) resolveCredentialPayload(
	ctx context.Context,
	orgSlug slug.Slug,
	ds *repods.DataSource,
) ([]byte, string, error) {
	runtimeCredential, err := s.credentials.ResolveRuntimeCredential(
		ctx,
		credentialssvc.ResolveRuntimeCredentialParams{
			Owner: credentialssvc.CredentialOwner{
				OrgSlug:        orgSlug,
				DataSourceSlug: ds.Slug,
			},
		},
	)
	if err != nil {
		return nil, "", err
	}
	if runtimeCredential.Kind != credentialssvc.CredentialKindOAuth2Grant || ds.Type != model.DataSourceTypeGitlab {
		return runtimeCredential.PlaintextJSON, runtimeCredential.Version, nil
	}
	var tokenSet credentialssvc.OAuthTokenSet
	if err := json.Unmarshal(runtimeCredential.PlaintextJSON, &tokenSet); err != nil {
		return nil, "", xerror.Unexpected().Detail("invalid GitLab OAuth credential")
	}
	if strings.TrimSpace(tokenSet.AccessToken) == "" {
		return nil, "", xerror.InvalidCredentials().Detail("GitLab OAuth access token is required")
	}
	payload, err := json.Marshal(gitconnector.Credentials{
		AuthType: gitconnector.AuthTypeHTTPS,
		Username: "oauth2",
		Token:    tokenSet.AccessToken,
	})
	return payload, runtimeCredential.Version, err
}

func (s *service) resolveRuntimeConnectorPayload(
	ctx context.Context,
	orgSlug slug.Slug,
	ds *repods.DataSource,
) ([]byte, []byte, string, *githubappsvc.InstallationToken, error) {
	credentialsJSON, credentialReference, err := s.resolveCredentialPayload(postgres.WithoutTransaction(ctx), orgSlug, ds)
	if err != nil {
		return nil, nil, "", nil, xerror.Wrapf("resolve credentials: %w", err)
	}

	runtimeDS := *ds
	credentialsJSON, installationToken, err := s.resolveManagedProviderGitConfig(ctx, orgSlug, &runtimeDS, credentialsJSON)
	if err != nil {
		if markErr := s.markProviderRepositoryUnavailable(ctx, orgSlug, ds); markErr != nil {
			return nil, nil, "", nil, xerror.Join(err, markErr)
		}
		return nil, nil, "", nil, err
	}

	return runtimeDS.Config, credentialsJSON, credentialReference, installationToken, nil
}

func (s *service) snapshotDataSourceAndCredentials(
	ctx context.Context,
	orgSlug, dsSlug slug.Slug,
) (*repods.DataSource, []byte, error) {
	readDataSource := func() (*repods.DataSource, error) {
		return postgres.InRead(
			postgres.WithoutTransaction(ctx),
			s.txm,
			func(ctx context.Context, db postgres.DBTX) (*repods.DataSource, error) {
				return s.repo.FindBySlug(ctx, db, orgSlug, dsSlug)
			},
		)
	}

	for range 3 {
		before, err := readDataSource()
		if err != nil {
			return nil, nil, err
		}
		if providerRepositorySelectionPending(before) || workspaceSelectionPending(before) {
			detail := "Select a provider repository before using this data source."
			if workspaceSelectionPending(before) {
				detail = "Connect Google before using this data source."
			}
			return nil, nil, ErrInvalidInput.WithPublicDetail(detail).Detail(detail)
		}
		credentialsJSON, _, err := s.resolveCredentialPayload(
			postgres.WithoutTransaction(ctx),
			orgSlug,
			before,
		)
		if err != nil {
			return nil, nil, err
		}
		after, err := readDataSource()
		if err != nil {
			return nil, nil, err
		}
		if before.ConnectionTestRevision == after.ConnectionTestRevision {
			credentialsJSON, _, err = s.resolveManagedProviderGitConfig(ctx, orgSlug, after, credentialsJSON)
			if err != nil {
				if markErr := s.markProviderRepositoryUnavailable(ctx, orgSlug, after); markErr != nil {
					return nil, nil, xerror.Join(err, markErr)
				}
				return nil, nil, err
			}
			return after, credentialsJSON, nil
		}
	}

	return nil, nil, ErrConnectionTestConflict.Detail(
		"data source changed while preparing connection test",
	)
}

func (s *service) markProviderRepositoryUnavailable(
	ctx context.Context,
	orgSlug slug.Slug,
	ds *repods.DataSource,
) error {
	if _, managed := managedProviderGitConfig(ds); !managed {
		return nil
	}
	finalizeCtx, cancel := context.WithTimeout(
		postgres.WithoutTransaction(context.WithoutCancel(ctx)),
		connectionTestFinalizeTimeout,
	)
	defer cancel()
	return s.txm.RunInTx(finalizeCtx, func(ctx context.Context, tx postgres.DBTX) error {
		_, err := s.repo.MarkTestFailure(
			ctx,
			tx,
			orgSlug,
			ds.Slug,
			ds.ConnectionTestRevision,
			"Selected provider repository is no longer accessible. Reauthorize or choose another repository.",
			nil,
		)
		return err
	})
}

func (s *service) resolveManagedProviderGitConfig(
	ctx context.Context,
	orgSlug slug.Slug,
	ds *repods.DataSource,
	credentialsJSON []byte,
) ([]byte, *githubappsvc.InstallationToken, error) {
	managed, ok := managedProviderGitConfig(ds)
	if !ok {
		return credentialsJSON, nil, nil
	}
	if ds.Type == model.DataSourceTypeGithub {
		repository, err := s.githubRepository(ctx, orgSlug, managed.Repository.ID)
		if err != nil {
			return nil, nil, xerror.Wrapf("resolve selected GitHub repository: %w", err)
		}
		repositoryID, _ := strconv.ParseInt(repository.ID, 10, 64)
		token, err := s.githubApps.Token(ctx, orgSlug, []int64{repositoryID})
		if err != nil {
			return nil, nil, xerror.Wrapf("mint GitHub repository token: %w", err)
		}
		cloneURL := url.URL{Scheme: "https", Host: "github.com", Path: "/" + repository.FullName + ".git"}
		runtimeConfig, err := json.Marshal(gitconnector.Config{RepoURL: cloneURL.String(), BasePath: managed.BasePath})
		if err != nil {
			return nil, nil, xerror.Unexpected().With(err)
		}
		ds.Config = runtimeConfig
		credentialsJSON, err := json.Marshal(gitconnector.Credentials{AuthType: gitconnector.AuthTypeHTTPS, Username: "x-access-token", Token: token.Token})
		return credentialsJSON, &token, err
	}
	var repository credentialssvc.ProviderRepository
	var err error
	if ds.Credential != nil && ds.Credential.Kind == credentialssvc.CredentialKindStaticSecret.String() {
		var credentials gitconnector.Credentials
		if decodeErr := json.Unmarshal(credentialsJSON, &credentials); decodeErr != nil {
			return nil, nil, xerror.Unexpected().With(decodeErr)
		}
		repository, err = s.credentials.ResolveProviderRepositoryWithToken(ctx, credentialssvc.ResolveProviderRepositoryWithTokenParams{
			Provider:     ds.Type.String(),
			AccessToken:  credentials.Token,
			RepositoryID: managed.Repository.ID,
		})
	} else {
		repository, err = s.credentials.ResolveProviderRepository(ctx, credentialssvc.ResolveProviderRepositoryParams{
			Owner:        dataSourceCredentialOwner(orgSlug, ds.Slug),
			Provider:     ds.Type.String(),
			RepositoryID: managed.Repository.ID,
		})
	}
	if err != nil {
		return nil, nil, xerror.Wrapf("resolve selected provider repository: %w", err)
	}
	runtimeConfig, err := json.Marshal(gitconnector.Config{
		RepoURL:       repository.CloneURL,
		DefaultBranch: repository.DefaultBranch,
		BasePath:      managed.BasePath,
	})
	if err != nil {
		return nil, nil, xerror.Unexpected().With(err)
	}
	ds.Config = runtimeConfig
	return credentialsJSON, nil, nil
}

func managedProviderGitConfig(ds *repods.DataSource) (gitconnector.ProviderConfig, bool) {
	if ds == nil || (ds.Type != model.DataSourceTypeGithub && ds.Type != model.DataSourceTypeGitlab) {
		return gitconnector.ProviderConfig{}, false
	}
	var config gitconnector.ProviderConfig
	if err := json.Unmarshal(ds.Config, &config); err != nil || strings.TrimSpace(config.Repository.ID) == "" {
		return gitconnector.ProviderConfig{}, false
	}
	return config, true
}

func providerRepositorySelectionPending(ds *repods.DataSource) bool {
	if ds == nil || (ds.Type != model.DataSourceTypeGithub && ds.Type != model.DataSourceTypeGitlab) {
		return false
	}
	if _, ok := managedProviderGitConfig(ds); ok {
		return false
	}
	var legacy gitconnector.Config
	return json.Unmarshal(ds.Config, &legacy) != nil || strings.TrimSpace(legacy.RepoURL) == ""
}

func (s *service) shouldForceReadOnlyForDataSource(
	ctx context.Context,
	orgSlug slug.Slug,
	ds *repods.DataSource,
) (bool, error) {
	if ds.Type != model.DataSourceTypeGit &&
		ds.Type != model.DataSourceTypeGithub &&
		ds.Type != model.DataSourceTypeGitlab {
		return false, nil
	}

	credentialsJSON, _, err := s.resolveCredentialPayload(postgres.WithoutTransaction(ctx), orgSlug, ds)
	if err != nil {
		return false, xerror.Wrapf("resolve credentials: %w", err)
	}
	return shouldForceReadOnlyFromCredentialsJSON(ds.Type, credentialsJSON)
}

func (s *service) introspectSchema(
	ctx context.Context,
	orgSlug slug.Slug,
	ds *repods.DataSource,
) (SchemaResult, error) {
	runtimeConfig, decryptedCreds, _, _, err := s.resolveRuntimeConnectorPayload(ctx, orgSlug, ds)
	if err != nil {
		return nil, err
	}

	encoded, err := s.schemaIntrospector.IntrospectSchema(ctx, SchemaIntrospectionRequest{
		OrganizationSlug: orgSlug,
		DataSourceSlug:   ds.Slug,
		DataSourceType:   ds.Type,
		ConfigJSON:       runtimeConfig,
		CredentialsJSON:  decryptedCreds,
		MaxResultBytes:   s.schemaMaxBytes,
		MaxResultItems:   s.schemaMaxItems,
	})
	if err != nil {
		if isSchemaDiagnosticError(err) {
			return nil, err
		}
		return nil, xerror.Wrapf("%w: %w", ErrConnectionFailed, err)
	}
	result, err := decodeSchemaJSON(encoded)
	if err != nil {
		return nil, xerror.Wrapf("decode isolated schema result: %w", err)
	}
	return result, nil
}

// ExecuteSchemaIntrospection performs one live schema read. It is exported so
// the isolated executor can own the connector while control-plane services keep
// persistence and orchestration ownership.
func ExecuteSchemaIntrospection(
	ctx context.Context,
	factory connector.NewConnectorFunc,
	request SchemaIntrospectionRequest,
) ([]byte, error) {
	if request.MaxResultBytes <= 0 {
		return nil, xerror.ExecutionUnavailable().Detail("schema result exceeds configured byte limit")
	}
	if request.MaxResultItems <= 0 {
		return nil, xerror.ExecutionUnavailable().Detail("schema result exceeds configured item limit")
	}
	ctx = schemaquota.WithQuota(ctx, request.MaxResultItems, request.MaxResultBytes)
	ctx = connector.WithDataSourceIdentity(ctx, request.OrganizationSlug, request.DataSourceSlug)
	adapter, err := adapterForType(request.DataSourceType)
	if err != nil {
		return nil, err
	}
	conn, err := factory(ctx, request.DataSourceType, request.ConfigJSON, request.CredentialsJSON)
	if err != nil {
		return nil, xerror.Wrapf("%w: %w", ErrConnectionFailed, err)
	}
	defer func() { _ = conn.Close() }()
	result, err := adapter.introspect(ctx, conn, request.DataSourceType)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, xerror.Wrapf("encode normalized schema: %w", err)
	}
	if request.MaxResultBytes <= 0 || len(encoded) > request.MaxResultBytes {
		return nil, xerror.ExecutionUnavailable().Detail("schema result exceeds configured byte limit")
	}
	if request.MaxResultItems <= 0 || schemaResultItems(result) > request.MaxResultItems {
		return nil, xerror.ExecutionUnavailable().Detail("schema result exceeds configured item limit")
	}
	return encoded, nil
}

func schemaResultItems(result SchemaResult) int {
	switch schema := result.(type) {
	case Schema[SpreadsheetSchemaPayload]:
		return len(schema.Payload.Worksheets)
	case Schema[SQLSchemaPayload]:
		return len(schema.Payload.Tables)
	case Schema[HTTPSchemaPayload]:
		return len(schema.Payload.Operations)
	case Schema[DocumentSchemaPayload]:
		return len(schema.Payload.Collections)
	case Schema[FileSchemaPayload]:
		return len(schema.Payload.Files) + len(schema.Payload.Diagnostics)
	case Schema[ObjectAPISchemaPayload]:
		return len(schema.Payload.Objects)
	default:
		return 0
	}
}

// isSchemaDiagnosticError keeps import-specific schema failures visible to callers.
func isSchemaDiagnosticError(err error) bool {
	return xerror.Is(err, ErrSchemaIntrospectionUnsupported) ||
		xerror.Is(err, ErrInvalidSchemaURL) ||
		xerror.Is(err, ErrSchemaFetchFailed) ||
		xerror.Is(err, ErrInvalidOpenAPI)
}

// validateConfigAndCredentials decodes connector payloads through the construction
// registry, enforces customer private-host policy, and validates the typed payloads.
func (s *service) validateConfigAndCredentials(
	dsType model.DataSourceType,
	configJSON, credentialsJSON []byte,
) error {
	if dsType == model.DataSourceTypeUnknown {
		return ErrInvalidDataSourceType
	}

	payloads, ok, err := connector.DecodeConnectorPayloads(dsType, configJSON, credentialsJSON)
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidDataSourceType.Detailf("unsupported data source type: %s", dsType.String())
	}

	// Deployment policy decides whether customer payloads may relax private-host SSRF guards.
	if payloads.PrivateHostsRequested && !s.customerConfigPolicy.AllowPrivateDataSourceHosts {
		return ErrInvalidInput.Detail(privateDataSourceHostsPolicyMessage)
	}

	if err := connector.ValidateTransportConfig(payloads.Config, s.customerConfigPolicy.AllowPlaintextConnectors); err != nil {
		return err
	}
	if err := validate.Default().Struct(payloads.Config); err != nil {
		return xerror.ValidationFailed().With(err)
	}
	if err := validate.Default().Struct(payloads.Credentials); err != nil {
		return xerror.ValidationFailed().With(err)
	}

	return nil
}

// validateProviderDataSourceConfig validates OAuth-first config without manufacturing a credential payload.
func (s *service) validateProviderDataSourceConfig(
	dsType model.DataSourceType,
	configJSON []byte,
) error {
	if dsType == model.DataSourceTypeGitlab {
		var config gitconnector.ProviderOnboardingConfig
		if err := json.Unmarshal(configJSON, &config); err != nil {
			return xerror.ValidationFailed().With(err)
		}
		if err := validate.Default().Struct(config); err != nil {
			return xerror.ValidationFailed().With(err)
		}
		return nil
	}
	if _, _, err := connector.NormalizeCustomerConfig(dsType, configJSON); err != nil {
		return err
	}
	payloads, ok, err := connector.DecodeConnectorPayloads(dsType, configJSON, []byte(`{}`))
	if err != nil {
		return err
	}
	if !ok {
		return ErrInvalidDataSourceType.Detailf("unsupported data source type: %s", dsType.String())
	}
	if payloads.PrivateHostsRequested && !s.customerConfigPolicy.AllowPrivateDataSourceHosts {
		return ErrInvalidInput.Detail(privateDataSourceHostsPolicyMessage)
	}
	if err := connector.ValidateTransportConfig(payloads.Config, s.customerConfigPolicy.AllowPlaintextConnectors); err != nil {
		return err
	}
	if err := validate.Default().Struct(payloads.Config); err != nil {
		return xerror.ValidationFailed().With(err)
	}
	return nil
}

func workspaceConsentMode(dsType model.DataSourceType, raw []byte) string {
	if dsType != model.DataSourceTypeGoogleWorkspace {
		return ""
	}
	var cfg workspace.Config
	_ = json.Unmarshal(raw, &cfg) // Validated before starting the install.
	return cfg.ConsentMode()
}
