package projectssvc

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/repostore"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/repositorysetups"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	gitconnector "github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/git"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

var gitLabPathSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type narrowSetupRepository interface {
	ProjectForSetup(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (slug.Slug, error)
	Create(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, json.RawMessage) (bool, error)
	Get(context.Context, postgres.DBTX, slug.Slug, slug.Slug) (*repositorysetups.Row, error)
	GetAttempt(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug) (*repositorysetups.Row, error)
	Claim(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) (*repositorysetups.Row, error)
	Abandon(context.Context, postgres.DBTX, slug.Slug, slug.Slug, slug.Slug, string) (*repositorysetups.Row, error)
	Update(context.Context, postgres.DBTX, slug.Slug, slug.Slug, string, *repositorysetups.Row) error
	Release(context.Context, postgres.DBTX, slug.Slug, slug.Slug, string) error
}

type narrowProjectsService interface {
	GetProject(context.Context, slug.Slug, slug.Slug) (*model.Project, error)
	SetProjectPipelineBackend(context.Context, slug.Slug, slug.Slug, model.SetProjectPipelineBackend) (*model.Project, error)
}

// RepositorySourceService retains core creation while distributions select persistence.
type RepositorySourceService interface {
	Create(context.Context, slug.Slug, model.EventActor, *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error)
	CreateProvisioned(context.Context, slug.Slug, model.EventActor, *datasourcessvc.CreateParams, datasourcessvc.SourceProvisioner) (*datasourcessvc.DataSource, error)
}

type narrowDataSourceService interface {
	RepositorySourceService
	FindBySlug(context.Context, slug.Slug, slug.Slug) (*datasourcessvc.DataSource, error)
}

type narrowGitLabBootstrapper interface {
	BootstrapGitLabRepository(context.Context, slug.Slug, gitprovider.RepositoryBootstrapRequest) (*gitprovider.RepositoryBootstrapResult, error)
}

type repositorySetupGitHub interface {
	ConsumeRejectedInstall(context.Context, slug.Slug, slug.Slug, model.EventActor, string) error
	StartRepositorySetup(context.Context, slug.Slug, model.EventActor, slug.Slug) (string, error)
	RepositorySetupForState(context.Context, model.EventActor, string) (slug.Slug, slug.Slug, error)
	CompleteRepositorySetup(context.Context, slug.Slug, slug.Slug, model.EventActor, string, string, int64, gitprovider.RepositoryBootstrapRequest) (*gitprovider.RepositoryBootstrapResult, error)
}

type repositorySetupCredentials interface {
	ConsumeOAuthInstall(context.Context, credentialssvc.ConsumeOAuthInstallParams) (credentialssvc.CredentialInstall, error)
	GetOrganizationGitLabConnection(context.Context, slug.Slug) (credentialssvc.OrganizationGitLabConnection, error)
	CreateOrganizationGitLabConnection(context.Context, credentialssvc.CreateOrganizationGitLabConnectionParams) (credentialssvc.OrganizationGitLabConnection, error)
	StartOAuthInstall(context.Context, credentialssvc.StartOAuthInstallParams) (credentialssvc.ProviderConnectorInstall, error)
	FindOAuthInstall(context.Context, credentialssvc.FindOAuthInstallParams) (credentialssvc.CredentialInstall, error)
	CompleteOAuthCallback(context.Context, credentialssvc.CompleteOAuthCallbackParams) (credentialssvc.CompleteOAuthCallbackResult, error)
}

type repositoryPreparationPolicy interface {
	CheckSetup(context.Context, slug.Slug, slug.Slug) error
	CreateRepositorySource(context.Context, RepositorySourceService, slug.Slug, slug.Slug, model.EventActor, *datasourcessvc.CreateParams) (*datasourcessvc.DataSource, error)
}

type RepositorySetupService struct {
	preparationPolicy repositoryPreparationPolicy
	txm               postgres.TransactionManager
	repo              narrowSetupRepository
	projects          narrowProjectsService
	sources           narrowDataSourceService
	bootstrapper      narrowGitLabBootstrapper
	github            repositorySetupGitHub
	credentials       repositorySetupCredentials
	events            workerproducer.EventProducer
}

func NewRepositorySetupService(txm postgres.TransactionManager, repo narrowSetupRepository, projects narrowProjectsService, sources narrowDataSourceService, bootstrapper narrowGitLabBootstrapper, github repositorySetupGitHub, credentials repositorySetupCredentials, events workerproducer.EventProducer, preparationPolicy repositoryPreparationPolicy) (*RepositorySetupService, error) {
	if preparationPolicy == nil || txm == nil || repo == nil || projects == nil || sources == nil || bootstrapper == nil || github == nil || credentials == nil || events == nil {
		return nil, xerror.Unexpected().Detail("project repository setup dependencies are required")
	}
	return &RepositorySetupService{preparationPolicy: preparationPolicy, txm: txm, repo: repo, projects: projects, sources: sources, bootstrapper: bootstrapper, github: github, credentials: credentials, events: events}, nil
}

func (s *RepositorySetupService) Get(ctx context.Context, org, project slug.Slug) (*model.ProjectRepositorySetup, error) {
	if _, err := s.projects.GetProject(ctx, org, project); err != nil {
		return nil, err
	}
	var row *repositorysetups.Row
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		var err error
		row, err = s.repo.Get(ctx, db, org, project)
		if xerror.Is(err, repositorysetups.ErrNotFound) {
			row = nil
			return nil
		}
		return err
	})
	if err != nil || row == nil {
		return nil, err
	}
	return setupFromRow(project, row)
}

func (s *RepositorySetupService) Create(ctx context.Context, org, project slug.Slug, request model.CreateProjectRepository, actor model.EventActor) (*model.ProjectRepositorySetup, error) {
	request, err := normalizeRepositorySetupRequest(request)
	if err != nil {
		return nil, err
	}
	if request.Provider == model.DataSourceTypeGithub && actor.UserSlugValue() == "" {
		return nil, xerror.ValidationFailed().Detail("GitHub repository creation requires a user session or personal access token; an organization API key cannot consent for a person.")
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	var row *repositorysetups.Row
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		setupSlug, err := slug.WithRandomSuffix("repo", 18)
		if err != nil {
			return xerror.Unexpected().With(err)
		}
		created, err := s.repo.Create(ctx, tx, org, project, setupSlug, requestJSON)
		if err != nil {
			return err
		}
		if created {
			_, err = s.events.EnqueueRequired(ctx, tx, org, actor, projectEvent{projectSlug: project, action: model.EventActionUpdate})
			if err != nil {
				return xerror.Wrapf("record project repository setup event: %w", err)
			}
		}
		row, err = s.repo.Get(ctx, tx, org, project)
		return err
	})
	if err != nil {
		return nil, err
	}
	stored, err := setupFromRow(project, row)
	if err != nil {
		return nil, err
	}
	if !sameRepositorySetupRequest(stored.Request, request) {
		return nil, xerror.AlreadyExists(xerror.Project).Detail("project repository setup already exists with a different target")
	}

	return s.runSetup(ctx, org, project, stored, actor, nil)
}

func (s *RepositorySetupService) GetAttempt(ctx context.Context, org, project, setup slug.Slug) (*model.ProjectRepositorySetup, error) {
	row, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*repositorysetups.Row, error) {
		return s.repo.GetAttempt(ctx, db, org, project, setup)
	})
	if err != nil {
		return nil, err
	}
	return setupFromRow(project, row)
}

func (s *RepositorySetupService) Abandon(ctx context.Context, org, project, setup slug.Slug, preserveResources bool, actor model.EventActor) (*model.ProjectRepositorySetup, error) {
	if !preserveResources {
		return nil, xerror.ValidationFailed().Detail("Confirm that existing or partially created repositories and connections will be kept before changing the target.")
	}
	row, err := postgres.InTx(ctx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*repositorysetups.Row, error) {
		stored, err := s.repo.GetAttempt(ctx, tx, org, project, setup)
		if err != nil || stored.AbandonedAt != nil {
			return stored, err
		}
		current, err := s.projects.GetProject(ctx, org, project)
		if err != nil {
			return nil, err
		}
		if stored.Status == model.ProjectRepositorySetupStatusAttached.String() || current.PipelineBackend != nil {
			return nil, xerror.AlreadyExists(xerror.Project).Detail("The project already has a connected repository. Change its backend deliberately instead of abandoning setup.")
		}
		lease, err := slug.WithRandomSuffix("lease", 18)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		if _, err := s.repo.Claim(ctx, tx, org, project, setup, lease.String()); err != nil {
			return nil, err
		}
		archived, err := s.repo.Abandon(ctx, tx, org, project, setup, lease.String())
		if err != nil {
			return nil, err
		}
		if _, err := s.events.EnqueueRequired(ctx, tx, org, actor, projectEvent{projectSlug: project, action: model.EventActionUpdate}); err != nil {
			return nil, xerror.Wrapf("record abandoned repository setup event: %w", err)
		}
		return archived, nil
	})
	if err != nil {
		return nil, err
	}
	return setupFromRow(project, row)
}

func (s *RepositorySetupService) CompleteGitHub(ctx context.Context, org, setup slug.Slug, actor model.EventActor, authorization model.GitHubRepositoryAuthorization) (*model.ProjectRepositorySetup, error) {
	intentOrg, intentSetup, err := s.github.RepositorySetupForState(ctx, actor, authorization.State)
	if err != nil {
		return nil, err
	}
	if intentOrg != org || intentSetup != setup || setup == "" {
		return nil, xerror.ValidationFailed().Detail("GitHub authorization does not match this project repository setup")
	}
	project, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (slug.Slug, error) {
		return s.repo.ProjectForSetup(ctx, db, org, setup)
	})
	if err != nil {
		return nil, err
	}
	stored, err := s.Get(ctx, org, project)
	if err != nil {
		return nil, err
	}
	if stored == nil || stored.Slug != setup || stored.Request.Provider != model.DataSourceTypeGithub {
		return nil, xerror.ValidationFailed().Detail("GitHub repository setup is not pending")
	}
	return s.runSetup(ctx, org, project, stored, actor, &authorization)
}

func (s *RepositorySetupService) RejectGitHubAuthorization(ctx context.Context, org, setup slug.Slug, actor model.EventActor, state, providerError string) (*model.ProjectRepositorySetup, error) {
	intentOrg, intentSetup, err := s.github.RepositorySetupForState(ctx, actor, state)
	if err != nil {
		return nil, err
	}
	if intentOrg != org || intentSetup != setup || setup == "" {
		return nil, xerror.ValidationFailed().Detail("GitHub authorization does not match this repository setup")
	}
	project, err := postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (slug.Slug, error) {
		return s.repo.ProjectForSetup(ctx, db, org, setup)
	})
	if err != nil {
		return nil, err
	}
	return s.rejectAuthorization(ctx, org, project, setup, actor, model.DataSourceTypeGithub, state, providerError)
}

func (s *RepositorySetupService) RejectGitLabAuthorization(ctx context.Context, org, project, setup slug.Slug, actor model.EventActor, state, providerError string) (*model.ProjectRepositorySetup, error) {
	install, err := s.credentials.FindOAuthInstall(ctx, credentialssvc.FindOAuthInstallParams{Provider: "gitlab", State: state})
	if err != nil {
		return nil, err
	}
	if install.Provider != "gitlab" || install.Owner.Kind != credentialssvc.CredentialOwnerKindOrganizationGitLab || install.Owner.OrgSlug != org || install.RepositorySetupProjectSlug != project || install.RepositorySetupSlug != setup || install.Actor.UserSlugValue() == "" || install.Actor.UserSlugValue() != actor.UserSlugValue() || install.ConsumedAt != nil {
		return nil, credentialssvc.ErrOAuthStateInvalid
	}
	return s.rejectAuthorization(ctx, org, project, setup, actor, model.DataSourceTypeGitlab, state, providerError)
}

func (s *RepositorySetupService) rejectAuthorization(ctx context.Context, org, project, setup slug.Slug, actor model.EventActor, provider model.DataSourceType, state, providerError string) (*model.ProjectRepositorySetup, error) {
	lease, err := slug.WithRandomSuffix("lease", 18)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	var row *repositorysetups.Row
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var claimErr error
		row, claimErr = s.repo.Claim(ctx, tx, org, project, setup, lease.String())
		return claimErr
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = s.releaseSetupLease(ctx, org, project, lease.String()) }()
	stored, err := setupFromRow(project, row)
	if err != nil {
		return nil, err
	}
	if stored.AbandonedAt != nil || stored.Status == model.ProjectRepositorySetupStatusAttached || stored.Request.Provider != provider {
		return nil, xerror.ValidationFailed().Detail("repository authorization is no longer pending for this setup")
	}
	if provider == model.DataSourceTypeGithub {
		err = s.github.ConsumeRejectedInstall(ctx, org, setup, actor, state)
	} else {
		_, err = s.credentials.ConsumeOAuthInstall(ctx, credentialssvc.ConsumeOAuthInstallParams{Provider: "gitlab", State: state})
	}
	if err != nil {
		return nil, err
	}
	stored.FailureCode = "authorization_failed"
	if providerError == "access_denied" {
		stored.FailureCode = "authorization_denied"
	}
	if err := s.persistProgress(ctx, org, project, lease.String(), stored, stored.FailureCode); err != nil {
		return nil, err
	}
	return stored, nil
}

func (s *RepositorySetupService) runSetup(ctx context.Context, org, project slug.Slug, stored *model.ProjectRepositorySetup, actor model.EventActor, authorization *model.GitHubRepositoryAuthorization) (*model.ProjectRepositorySetup, error) {
	if err := s.preparationPolicy.CheckSetup(ctx, org, project); err != nil {
		return nil, err
	}

	request := stored.Request
	var row *repositorysetups.Row

	leaseToken, err := slug.WithRandomSuffix("lease", 18)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	leaseOwner := leaseToken.String()
	err = s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		var err error
		row, err = s.repo.Claim(ctx, tx, org, project, stored.Slug, leaseOwner)
		return err
	})
	if err != nil {
		return nil, err
	}
	opCtx, opCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer opCancel()
	defer func() { _ = s.releaseSetupLease(ctx, org, project, leaseOwner) }()
	stored, err = setupFromRow(project, row)
	if err != nil {
		return nil, err
	}
	if stored.Status == model.ProjectRepositorySetupStatusAttached {
		return s.verifyAttached(opCtx, org, project, stored)
	}

	current, err := s.projects.GetProject(opCtx, org, project)
	if err != nil {
		return nil, err
	}
	if current.PipelineBackend != nil {
		if stored.Status >= model.ProjectRepositorySetupStatusSourceCreated && current.PipelineBackend.DataSourceSlug == stored.DataSourceSlug && current.PipelineBackend.TrackedBranch == request.TrackedBranch && current.PipelineBackend.PipelineDir == request.PipelineDir && sameSetupRepository(current.PipelineBackend.Repo, stored.RepositoryURL) {
			if _, err := s.findManagedSource(opCtx, org, stored); err != nil {
				return nil, err
			}
			stored.Status, stored.FailureCode = model.ProjectRepositorySetupStatusAttached, ""
			if err := s.persistProgress(opCtx, org, project, leaseOwner, stored, ""); err != nil {
				return nil, err
			}
			return stored, nil
		}
		return nil, xerror.AlreadyExists(xerror.Project).Detail("project already has an unrelated pipeline backend")
	}
	if request.Provider == model.DataSourceTypeGitlab {
		stored.AuthorizationURL, err = s.prepareGitLabAuthorization(opCtx, org, project, stored.Slug, actor)
		if err != nil {
			return stored, err
		}
	} else if stored.Status == model.ProjectRepositorySetupStatusPending && authorization == nil {
		stored.AuthorizationURL, err = s.github.StartRepositorySetup(opCtx, org, actor, stored.Slug)
		if err != nil {
			return stored, err
		}
	}
	if stored.AuthorizationURL != "" {
		stored.FailureCode = ""
		return stored, s.persistProgress(opCtx, org, project, leaseOwner, stored, "")
	}
	if stored.Status < model.ProjectRepositorySetupStatusRepositoryCreated {
		bootstrapRequest := gitprovider.RepositoryBootstrapRequest{Owner: request.Owner, Name: request.RepositoryName, DefaultBranch: request.TrackedBranch, Files: pipelinegit.RepositoryTemplate(request.PipelineDir), Marker: stored.Slug.String(), ExpectedRepositoryID: stored.RepositoryID}
		var result *gitprovider.RepositoryBootstrapResult
		var bootstrapErr error
		if request.Provider == model.DataSourceTypeGithub {
			if authorization == nil {
				return nil, xerror.ValidationFailed().Detail("Authorize GitHub before resuming repository creation")
			}
			result, bootstrapErr = s.github.CompleteRepositorySetup(opCtx, org, stored.Slug, actor, authorization.State, authorization.Code, authorization.InstallationID, bootstrapRequest)
		} else {
			result, bootstrapErr = s.bootstrapper.BootstrapGitLabRepository(opCtx, org, bootstrapRequest)
		}
		if result != nil && validRepositoryIdentity(result, request) && (result.RemoteURL == "" || validSetupRepositoryURL(result.RemoteURL, request)) {
			if stored.RepositoryID != "" && stored.RepositoryID != result.RepositoryID {
				return stored, xerror.Unexpected().Detail("Provider returned a different repository ID")
			}
			stored.RepositoryID, stored.RepositoryURL, stored.FirstCommitSHA = result.RepositoryID, result.RemoteURL, result.HeadSHA
			if completedRepositoryResult(result, request) {
				stored.Status = model.ProjectRepositorySetupStatusRepositoryCreated
			}
		}
		if bootstrapErr == nil && !completedRepositoryResult(result, request) {
			bootstrapErr = xerror.Unexpected().Detail("Provider returned incomplete repository provenance")
		}
		stored.FailureCode = failureCode(bootstrapErr)
		if err := s.persistProgress(opCtx, org, project, leaseOwner, stored, stored.FailureCode); err != nil {
			return nil, err
		}
		if bootstrapErr != nil {
			return stored, xerror.Wrapf("bootstrap project repository: %w", bootstrapErr)
		}
	}
	if stored.Status < model.ProjectRepositorySetupStatusSourceCreated {
		ds, err := s.managedSource(opCtx, org, stored, request, actor)
		if err != nil {
			stored.FailureCode = "source_create_failed"
			if xerror.Is(err, ErrRepositorySetupSourceTestRequired) {
				stored.FailureCode = "source_test_required"
			}
			_ = s.persistProgress(opCtx, org, project, leaseOwner, stored, stored.FailureCode)
			return stored, err
		}
		stored.DataSourceSlug, stored.Status = ds.Slug, model.ProjectRepositorySetupStatusSourceCreated
		if err := s.persistProgress(opCtx, org, project, leaseOwner, stored, ""); err != nil {
			return nil, err
		}
	}
	if _, err := s.findManagedSource(opCtx, org, stored); err != nil {
		if xerror.Is(err, ErrRepositorySetupSourceTestRequired) {
			stored.FailureCode = "source_test_required"
			_ = s.persistProgress(opCtx, org, project, leaseOwner, stored, stored.FailureCode)
		}
		return stored, err
	}
	if _, err := s.projects.SetProjectPipelineBackend(opCtx, org, project, model.SetProjectPipelineBackend{RequireUnconfigured: true, ExpectedRepository: stored.RepositoryURL, DataSourceSlug: stored.DataSourceSlug, TrackedBranch: request.TrackedBranch, PipelineDir: request.PipelineDir, Mode: model.SetProjectPipelineBackendModeAdoptExisting}); err != nil {
		stored.FailureCode = "backend_attach_failed"
		_ = s.persistProgress(opCtx, org, project, leaseOwner, stored, stored.FailureCode)
		return stored, err
	}
	stored.Status, stored.FailureCode = model.ProjectRepositorySetupStatusAttached, ""
	if err := s.persistProgress(opCtx, org, project, leaseOwner, stored, ""); err != nil {
		return nil, err
	}
	return stored, nil
}

func (s *RepositorySetupService) CompleteGitLab(ctx context.Context, org, project, setup slug.Slug, actor model.EventActor, authorization model.GitLabRepositoryAuthorization) (*model.ProjectRepositorySetup, error) {
	opCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	install, err := s.credentials.FindOAuthInstall(opCtx, credentialssvc.FindOAuthInstallParams{Provider: "gitlab", State: authorization.State})
	if err != nil {
		return nil, err
	}
	if setup == "" || actor.UserSlugValue() == "" || install.Actor.UserSlugValue() != actor.UserSlugValue() || install.Provider != "gitlab" || install.Owner.Kind != credentialssvc.CredentialOwnerKindOrganizationGitLab || install.Owner.OrgSlug != org || install.RepositorySetupProjectSlug != project || install.RepositorySetupSlug != setup {
		return nil, credentialssvc.ErrOAuthStateInvalid
	}
	lease, err := slug.WithRandomSuffix("lease", 18)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	row, err := postgres.InTx(opCtx, s.txm, func(ctx context.Context, tx postgres.DBTX) (*repositorysetups.Row, error) {
		return s.repo.Claim(ctx, tx, org, project, setup, lease.String())
	})
	if err != nil {
		return nil, err
	}
	released := false
	defer func() {
		if !released {
			_ = s.releaseSetupLease(ctx, org, project, lease.String())
		}
	}()
	stored, err := setupFromRow(project, row)
	if err != nil {
		return nil, err
	}
	if stored.Request.Provider != model.DataSourceTypeGitlab || stored.Status == model.ProjectRepositorySetupStatusAttached {
		return nil, credentialssvc.ErrOAuthStateInvalid
	}
	if _, err := s.credentials.CompleteOAuthCallback(opCtx, credentialssvc.CompleteOAuthCallbackParams{Provider: "gitlab", State: authorization.State, Code: authorization.Code, Issuer: authorization.Issuer, Actor: actor}); err != nil {
		return stored, err
	}
	if err := s.releaseSetupLease(ctx, org, project, lease.String()); err != nil {
		return stored, err
	}
	released = true
	return s.runSetup(ctx, org, project, stored, actor, nil)
}

func (s *RepositorySetupService) releaseSetupLease(ctx context.Context, org, project slug.Slug, leaseOwner string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return s.txm.RunInTx(cleanupCtx, func(ctx context.Context, tx postgres.DBTX) error {
		return s.repo.Release(ctx, tx, org, project, leaseOwner)
	})
}

func (s *RepositorySetupService) prepareGitLabAuthorization(ctx context.Context, org, project, setup slug.Slug, actor model.EventActor) (string, error) {
	connection, err := s.credentials.GetOrganizationGitLabConnection(ctx, org)
	if err != nil && !xerror.Is(err, credentialssvc.ErrCredentialNotFound) {
		return "", err
	}
	if err == nil && connection.Credential.Status == credentialssvc.CredentialStatusActive && slices.Contains(connection.Credential.OAuth.Scopes, "api") {
		return "", nil
	}
	if actor.UserSlugValue() == "" {
		return "", xerror.ValidationFailed().Detail("Authorize the organization GitLab connection using a user session or personal access token, then resume this setup with your organization API key.")
	}
	connection, err = s.credentials.CreateOrganizationGitLabConnection(ctx, credentialssvc.CreateOrganizationGitLabConnectionParams{OrganizationSlug: org, Provider: "gitlab"})
	if err != nil {
		return "", err
	}
	intent := credentialssvc.OAuthInstallIntentRotate
	if connection.Credential.Status == credentialssvc.CredentialStatusPending {
		intent = credentialssvc.OAuthInstallIntentCreate
	}
	install, err := s.credentials.StartOAuthInstall(ctx, credentialssvc.StartOAuthInstallParams{
		Owner:               credentialssvc.CredentialOwner{Kind: credentialssvc.CredentialOwnerKindOrganizationGitLab, OrgSlug: org},
		RepositorySetupSlug: setup, RepositorySetupProjectSlug: project, Provider: "gitlab", IncludeWriteScopes: true, Intent: intent, Actor: actor,
	})
	if err != nil {
		return "", err
	}
	return install.AuthorizationURL, nil
}

func (s *RepositorySetupService) managedSource(ctx context.Context, org slug.Slug, setup *model.ProjectRepositorySetup, request model.CreateProjectRepository, actor model.EventActor) (*datasourcessvc.DataSource, error) {
	ds, err := s.sources.FindBySlug(ctx, org, setup.Slug)
	if err == nil {
		return verifyManagedSource(ds, setup.RepositoryID, setup.Request.Provider)
	}
	if !xerror.Is(err, datasourcessvc.ErrDataSourceNotFound) {
		return nil, err
	}
	selectionKey := "project_id"
	if request.Provider == model.DataSourceTypeGithub {
		selectionKey = "repository_id"
	}
	config, err := json.Marshal(map[string]string{selectionKey: setup.RepositoryID})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	ds, err = s.preparationPolicy.CreateRepositorySource(ctx, s.sources, org, setup.ProjectSlug, actor, &datasourcessvc.CreateParams{Name: request.RepositoryName, Slug: setup.Slug, Type: request.Provider, ConfigJSON: config, ReadOnly: false})
	if err != nil && !xerror.Is(err, datasourcessvc.ErrSlugTaken) {
		return nil, err
	}
	if err != nil {
		var findErr error
		ds, findErr = s.sources.FindBySlug(ctx, org, setup.Slug)
		if findErr != nil {
			return nil, err
		}
	}
	return verifyManagedSource(ds, setup.RepositoryID, setup.Request.Provider)
}

func (s *RepositorySetupService) persist(ctx context.Context, org, project slug.Slug, leaseOwner string, setup *model.ProjectRepositorySetup, code string) error {
	result, err := json.Marshal(setupResult{RepositoryID: setup.RepositoryID, RepositoryURL: setup.RepositoryURL, FirstCommitSHA: setup.FirstCommitSHA})
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return s.txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return s.repo.Update(ctx, tx, org, project, leaseOwner, &repositorysetups.Row{Result: result, Status: setup.Status.String(), FailureCode: code})
	})
}

func (s *RepositorySetupService) persistProgress(ctx context.Context, org, project slug.Slug, leaseOwner string, setup *model.ProjectRepositorySetup, code string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return s.persist(cleanupCtx, org, project, leaseOwner, setup, code)
}

type setupResult struct {
	RepositoryID   string `json:"repository_id"`
	RepositoryURL  string `json:"repository_url"`
	FirstCommitSHA string `json:"first_commit_sha"`
}

func setupFromRow(project slug.Slug, row *repositorysetups.Row) (*model.ProjectRepositorySetup, error) {
	var request model.CreateProjectRepository
	var result setupResult
	if json.Unmarshal(row.Request, &request) != nil || json.Unmarshal(row.Result, &result) != nil && len(row.Result) != 0 {
		return nil, xerror.Unexpected().Detail("project repository setup state is invalid")
	}
	status, err := model.ProjectRepositorySetupStatusString(row.Status)
	if err != nil || status == model.ProjectRepositorySetupStatusUnknown {
		return nil, xerror.Unexpected().With(err).Detail("project repository setup status is invalid")
	}
	return &model.ProjectRepositorySetup{Slug: row.Slug, ProjectSlug: project, Request: request, Status: status, DataSourceSlug: row.Slug, RepositoryID: result.RepositoryID, RepositoryURL: result.RepositoryURL, FirstCommitSHA: result.FirstCommitSHA, FailureCode: row.FailureCode, AbandonedAt: row.AbandonedAt}, nil
}

func normalizeRepositorySetupRequest(request model.CreateProjectRepository) (model.CreateProjectRepository, error) {
	request.Owner, request.RepositoryName = strings.TrimSpace(request.Owner), strings.TrimSpace(request.RepositoryName)
	if strings.TrimSpace(request.TrackedBranch) == "" {
		request.TrackedBranch = "main"
	}
	if strings.TrimSpace(request.PipelineDir) == "" {
		request.PipelineDir = "pipelines"
	}
	if !request.CreatePrivateRepository || (request.Provider != model.DataSourceTypeGitlab && request.Provider != model.DataSourceTypeGithub) {
		return request, xerror.ValidationFailed().Detail("GitHub or GitLab private repository creation consent is required")
	}
	if request.Owner == "" || len(request.Owner) > 255 || request.RepositoryName == "" || len(request.RepositoryName) > 100 || request.RepositoryName == "." || request.RepositoryName == ".." {
		return request, xerror.ValidationFailed().Detail("Repository owner and name are required")
	}
	if request.Provider == model.DataSourceTypeGithub && (!githubOwnerPattern.MatchString(request.Owner) || !githubRepositoryPattern.MatchString(request.RepositoryName)) {
		return request, xerror.ValidationFailed().Detail("GitHub owner or repository name is invalid")
	}
	if request.Provider == model.DataSourceTypeGitlab {
		for _, segment := range append(strings.Split(request.Owner, "/"), request.RepositoryName) {
			if !gitLabPathSegment.MatchString(segment) {
				return request, xerror.ValidationFailed().Detail("GitLab owner or repository name is invalid")
			}
		}
	}
	var err error
	request.TrackedBranch, err = validateTrackedBranch(request.TrackedBranch)
	if err != nil {
		return request, err
	}
	request.PipelineDir, err = validatePipelineDir(request.PipelineDir)
	return request, err
}

func sameRepositorySetupRequest(a, b model.CreateProjectRepository) bool {
	return a.Provider == b.Provider && a.CreatePrivateRepository == b.CreatePrivateRepository && strings.EqualFold(a.Owner, b.Owner) && strings.EqualFold(a.RepositoryName, b.RepositoryName) && a.TrackedBranch == b.TrackedBranch && a.PipelineDir == b.PipelineDir
}

func validRepositoryIdentity(result *gitprovider.RepositoryBootstrapResult, request model.CreateProjectRepository) bool {
	if result == nil {
		return false
	}
	id, err := strconv.ParseInt(result.RepositoryID, 10, 64)
	return err == nil && id > 0 && string(result.Repository.Provider) == request.Provider.String() && strings.EqualFold(result.Repository.Owner, request.Owner) && strings.EqualFold(result.Repository.Name, request.RepositoryName)
}

func completedRepositoryResult(result *gitprovider.RepositoryBootstrapResult, request model.CreateProjectRepository) bool {
	if !validRepositoryIdentity(result, request) || strings.TrimSpace(result.HeadSHA) == "" {
		return false
	}
	return validSetupRepositoryURL(result.RemoteURL, request)
}

func validSetupRepositoryURL(remote string, request model.CreateProjectRepository) bool {
	u, err := url.Parse(remote)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && strings.EqualFold(strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git"), request.Owner+"/"+request.RepositoryName)
}

func failureCode(err error) string {
	if err == nil {
		return ""
	}
	return "repository_bootstrap_failed"
}

func verifyManagedSource(ds *datasourcessvc.DataSource, repositoryID string, provider model.DataSourceType) (*datasourcessvc.DataSource, error) {
	var config gitconnector.ProviderConfig
	if ds == nil || ds.Type != provider || ds.ReadOnly || json.Unmarshal(ds.Config, &config) != nil || config.Repository.ID != repositoryID {
		return nil, xerror.AlreadyExists(xerror.DataSource).Detail("managed source does not match the repository setup")
	}
	if !ds.IsActive {
		return nil, ErrRepositorySetupSourceTestRequired
	}
	return ds, nil
}

func (s *RepositorySetupService) verifyAttached(ctx context.Context, org, project slug.Slug, setup *model.ProjectRepositorySetup) (*model.ProjectRepositorySetup, error) {
	current, err := s.projects.GetProject(ctx, org, project)
	if err != nil {
		return nil, err
	}
	if current.PipelineBackend == nil || current.PipelineBackend.DataSourceSlug != setup.DataSourceSlug || current.PipelineBackend.TrackedBranch != setup.Request.TrackedBranch || current.PipelineBackend.PipelineDir != setup.Request.PipelineDir || !sameSetupRepository(current.PipelineBackend.Repo, setup.RepositoryURL) {
		return nil, xerror.Unexpected().Detail("attached project repository setup does not match the project backend")
	}
	if _, err := s.findManagedSource(ctx, org, setup); err != nil {
		return nil, err
	}
	return setup, nil
}

func sameSetupRepository(a, b string) bool {
	return a != "" && b != "" && strings.EqualFold(repostore.NormalizeRemoteURL(a), repostore.NormalizeRemoteURL(b))
}

func (s *RepositorySetupService) findManagedSource(ctx context.Context, org slug.Slug, setup *model.ProjectRepositorySetup) (*datasourcessvc.DataSource, error) {
	ds, err := s.sources.FindBySlug(ctx, org, setup.DataSourceSlug)
	if err != nil {
		return nil, err
	}
	return verifyManagedSource(ds, setup.RepositoryID, setup.Request.Provider)
}
