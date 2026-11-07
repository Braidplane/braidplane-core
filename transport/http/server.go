package http

import (
	"context"
	"log/slog"
	"time"

	"github.com/Braidplane/braidplane-core/service/runrecencysvc"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/executiondelivery"
	obslogging "github.com/Braidplane/braidplane-core/pkg/logging"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/accountclosuresvc"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/apikeyssvc"
	"github.com/Braidplane/braidplane-core/service/authorization"
	"github.com/Braidplane/braidplane-core/service/contractplansvc"
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
	"github.com/Braidplane/braidplane-core/service/pipelinewebhooksvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
	"github.com/Braidplane/braidplane-core/service/proofconfigsvc"
	"github.com/Braidplane/braidplane-core/service/proofreceiptsvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/retentionsvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
)

type compiledPlanService interface {
	CompileVersion(context.Context, slug.Slug, slug.Slug, slug.Slug, string, slug.Slug, *odcs.ProjectionRequest, []contractplansvc.OperationScope) (*contractplansvc.Summary, error)
	Get(context.Context, slug.Slug, slug.Slug, slug.Slug, string) (*contractplansvc.Summary, error)
}

type projectRepositorySetupService interface {
	RejectGitHubAuthorization(context.Context, slug.Slug, slug.Slug, model.EventActor, string, string) (*model.ProjectRepositorySetup, error)
	RejectGitLabAuthorization(context.Context, slug.Slug, slug.Slug, slug.Slug, model.EventActor, string, string) (*model.ProjectRepositorySetup, error)
	CompleteGitLab(context.Context, slug.Slug, slug.Slug, slug.Slug, model.EventActor, model.GitLabRepositoryAuthorization) (*model.ProjectRepositorySetup, error)
	GetAttempt(context.Context, slug.Slug, slug.Slug, slug.Slug) (*model.ProjectRepositorySetup, error)
	Abandon(context.Context, slug.Slug, slug.Slug, slug.Slug, bool, model.EventActor) (*model.ProjectRepositorySetup, error)
	CompleteGitHub(context.Context, slug.Slug, slug.Slug, model.EventActor, model.GitHubRepositoryAuthorization) (*model.ProjectRepositorySetup, error)
	Get(context.Context, slug.Slug, slug.Slug) (*model.ProjectRepositorySetup, error)
	Create(context.Context, slug.Slug, slug.Slug, model.CreateProjectRepository, model.EventActor) (*model.ProjectRepositorySetup, error)
}

type runRecencyService interface {
	Inspect(context.Context, runrecencysvc.Scope, time.Time) (*runrecencysvc.Inspection, error)
	Update(context.Context, runrecencysvc.Scope, int64, time.Duration, bool, time.Time) (*runrecencysvc.Expectation, error)
}

type memberAdmissionService interface {
	CheckMemberAdmission(context.Context, slug.Slug) error
}

// RequestAuthorization exposes the request-scoped authentication and authorization
// boundary needed by an optional HTTP extension without exposing server state.
type RequestAuthorization interface {
	CurrentUser(fiber.Ctx) (reqctx.UserPrincipal, bool)
	Can(fiber.Ctx, model.Action, model.Resource) error
	CanOrg(fiber.Ctx, slug.Slug, model.Action, model.Resource) error
}

// Server implements the shared HTTP handlers.
type Server = server

func NewServer(
	usersService userssvc.UsersService,
	accountClosureService *accountclosuresvc.Service,
	oidcService oidc.Service,
	sessionStore *session.Store,
	authorizationService authorization.Service,
	orgService orgsvc.Service,
	admissionService admissionsvc.Service,
	projectsService projectssvc.Service,
	repositorySetupService projectRepositorySetupService,
	pipelineCreationsService pipelineCreationService,
	odcsContractsService odcscontractsvc.Service,
	compiledPlansService compiledPlanService,
	odcsArtifactImporter *odcscontractsvc.ArtifactImporter,
	environmentsService environmentssvc.Service,
	membersService memberssvc.Service,
	rolesService rolessvc.Service,
	apiKeysService apikeyssvc.Service,
	patsService patssvc.Service,
	credentialsService credentialssvc.Service,
	datasourcesService datasourcessvc.Service,
	dataSourceRefsService datasourcerefssvc.Service,
	queriesService queriessvc.Service,
	eventsService eventssvc.Service,
	githubAppService *githubappsvc.Service,
	notificationsService notificationsvc.Service,
	retentionService retentionsvc.Service,
	pipelinesService pipelinesvc.Service,
	proofReceiptService proofreceiptsvc.Service,
	proofConfigurationService *proofconfigsvc.Service,
	proofObservationService *proofreceiptsvc.LiveCompiler,
	pipelineTriggersService pipelinetriggersvc.Service,
	pipelineWebhookService *pipelinewebhooksvc.Service,
	runRecencyService runRecencyService,
	frontendURL string,
	googleRedirectURI string,
	selfServiceRegistrationEnabled bool,
	externalOnboardingEnabled bool,
	memberAdmission memberAdmissionService,
) *Server {
	return &server{
		memberAdmission:                memberAdmission,
		usersService:                   usersService,
		accountClosureService:          accountClosureService,
		oidcService:                    oidcService,
		sessionStore:                   sessionStore,
		authorizationService:           authorizationService,
		orgService:                     orgService,
		admissionService:               admissionService,
		projectsService:                projectsService,
		repositorySetupService:         repositorySetupService,
		pipelineCreationsService:       pipelineCreationsService,
		compiledPlansService:           compiledPlansService,
		odcsContractsService:           odcsContractsService,
		odcsArtifactImporter:           odcsArtifactImporter,
		environmentsService:            environmentsService,
		membersService:                 membersService,
		rolesService:                   rolesService,
		apiKeysService:                 apiKeysService,
		patsService:                    patsService,
		credentialsService:             credentialsService,
		datasourcesService:             datasourcesService,
		dataSourceRefsService:          dataSourceRefsService,
		queriesService:                 queriesService,
		eventsService:                  eventsService,
		githubAppService:               githubAppService,
		notificationsService:           notificationsService,
		retentionService:               retentionService,
		pipelinesService:               pipelinesService,
		proofConfigurationService:      proofConfigurationService,
		proofReceiptService:            proofReceiptService,
		proofObservationService:        proofObservationService,
		pipelineTriggersService:        pipelineTriggersService,
		pipelineWebhookService:         pipelineWebhookService,
		runRecencyService:              runRecencyService,
		frontendURL:                    frontendURL,
		googleRedirectURI:              googleRedirectURI,
		selfServiceRegistrationEnabled: selfServiceRegistrationEnabled,
		externalOnboardingEnabled:      externalOnboardingEnabled,
	}
}

type server struct {
	memberAdmission                memberAdmissionService
	runRecencyService              runRecencyService
	proofConfigurationService      *proofconfigsvc.Service
	usersService                   userssvc.UsersService
	accountClosureService          *accountclosuresvc.Service
	oidcService                    oidc.Service
	sessionStore                   *session.Store
	authorizationService           authorization.Service
	orgService                     orgsvc.Service
	admissionService               admissionsvc.Service
	projectsService                projectssvc.Service
	repositorySetupService         projectRepositorySetupService
	pipelineCreationsService       pipelineCreationService
	compiledPlansService           compiledPlanService
	odcsContractsService           odcscontractsvc.Service
	odcsArtifactImporter           *odcscontractsvc.ArtifactImporter
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
	githubAppService               *githubappsvc.Service
	notificationsService           notificationsvc.Service
	retentionService               retentionsvc.Service
	pipelinesService               pipelinesvc.Service
	proofReceiptService            proofreceiptsvc.Service
	proofObservationService        *proofreceiptsvc.LiveCompiler
	pipelineTriggersService        pipelinetriggersvc.Service
	pipelineWebhookService         pipelineWebhookService
	frontendURL                    string
	googleRedirectURI              string
	selfServiceRegistrationEnabled bool
	externalOnboardingEnabled      bool
}

func (s *server) checkMemberAdmission(ctx context.Context, org slug.Slug) error {
	if s.memberAdmission == nil {
		return xerror.New(xerror.KindUnavailable, "MEMBER_ADMISSION_UNAVAILABLE", "Member admission is not configured")
	}
	return s.memberAdmission.CheckMemberAdmission(ctx, org)
}

// RequestAuthorization returns the request-scoped core authorization boundary.
func (s *server) RequestAuthorization() RequestAuthorization { return s }

// CurrentUser returns the authenticated user principal from the configured session store.
func (s *server) CurrentUser(c fiber.Ctx) (reqctx.UserPrincipal, bool) {
	return reqctx.GetUserPrincipal(c, s.sessionStore)
}

// Can authorizes a global user action through the shared authorization service.
func (s *server) Can(c fiber.Ctx, action model.Action, resource model.Resource) error {
	return s.can(c, action, resource)
}

// CanOrg authorizes an organization-scoped action through the shared authorization service.
func (s *server) CanOrg(c fiber.Ctx, org slug.Slug, action model.Action, resource model.Resource) error {
	return s.canOrg(c, org, action, resource)
}

// Authorization helpers

func (s *server) can(c fiber.Ctx, action model.Action, resource model.Resource) error {
	userSlug := reqctx.GetUserSlug(c, s.sessionStore)
	return s.canWithInput(c, authorization.Input{
		UserSlug: userSlug,
		Action:   action,
		Resource: resource,
	})
}

// canOrg checks authorization for organization-scoped operations.
func (s *server) canOrg(
	c fiber.Ctx,
	orgSlug slug.Slug,
	action model.Action,
	resource model.Resource,
) error {
	if principal, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		if principal.OrgSlug != orgSlug {
			return apierror.Forbidden(resource.String(), action.String())
		}
		return s.canWithInput(c, authorization.Input{
			APIKeySlug:     principal.Slug,
			APIKeyRoleSlug: principal.Role,
			Action:         action,
			Resource:       resource,
			OrgSlug:        &orgSlug,
		})
	}

	userSlug := reqctx.GetUserSlug(c, s.sessionStore)
	return s.canWithInput(c, authorization.Input{
		UserSlug: userSlug,
		Action:   action,
		Resource: resource,
		OrgSlug:  &orgSlug,
	})
}

func (s *server) canOrgUserSession(
	c fiber.Ctx,
	orgSlug slug.Slug,
	action model.Action,
	resource model.Resource,
) error {
	if _, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		return apierror.Unauthorized()
	}

	userPrincipal, ok := reqctx.GetUserPrincipal(c, s.sessionStore)
	if !ok {
		return apierror.Unauthorized()
	}
	if userPrincipal.Source != reqctx.UserPrincipalSourceSession {
		return apierror.UserPrincipalRequired()
	}

	return s.canWithInput(c, authorization.Input{
		UserSlug: userPrincipal.Slug,
		Action:   action,
		Resource: resource,
		OrgSlug:  &orgSlug,
	})
}

// canOrgAPIKeyManager authorizes session users or explicitly opted-in API keys.
// PAT user principals remain separate from organization machine-principal administration.
func (s *server) canOrgAPIKeyManager(
	c fiber.Ctx,
	orgSlug slug.Slug,
	action model.Action,
) error {
	if principal, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		if principal.OrgSlug != orgSlug || !principal.CanManageAPIKeys {
			return apierror.Forbidden(model.ResourceAPIKeys.String(), action.String())
		}
		return s.canWithInput(c, authorization.Input{
			APIKeySlug:     principal.Slug,
			APIKeyRoleSlug: principal.Role,
			Action:         action,
			Resource:       model.ResourceAPIKeys,
			OrgSlug:        &orgSlug,
		})
	}

	userPrincipal, ok := reqctx.GetUserPrincipal(c, s.sessionStore)
	if !ok {
		return apierror.Unauthorized()
	}
	if userPrincipal.Source != reqctx.UserPrincipalSourceSession {
		return apierror.UserPrincipalRequired()
	}

	return s.canWithInput(c, authorization.Input{
		UserSlug: userPrincipal.Slug,
		Action:   action,
		Resource: model.ResourceAPIKeys,
		OrgSlug:  &orgSlug,
	})
}

func (s *server) canOrgUserPrincipal(
	c fiber.Ctx,
	orgSlug slug.Slug,
	action model.Action,
	resource model.Resource,
) (slug.Slug, error) {
	if _, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		return "", apierror.UserPrincipalRequired()
	}

	userPrincipal, ok := reqctx.GetUserPrincipal(c, s.sessionStore)
	if !ok {
		return "", apierror.Unauthorized()
	}

	err := s.canWithInput(c, authorization.Input{
		UserSlug: userPrincipal.Slug,
		Action:   action,
		Resource: resource,
		OrgSlug:  &orgSlug,
	})
	if err != nil {
		return "", err
	}

	return userPrincipal.Slug, nil
}

func (s *server) canSessionProfile(c fiber.Ctx, action model.Action) (slug.Slug, error) {
	if _, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		return "", apierror.UserPrincipalRequired()
	}

	userPrincipal, ok := reqctx.GetUserPrincipal(c, s.sessionStore)
	if !ok {
		return "", apierror.Unauthorized()
	}
	if userPrincipal.Source != reqctx.UserPrincipalSourceSession {
		return "", apierror.UserPrincipalRequired()
	}

	err := s.canWithInput(c, authorization.Input{
		UserSlug:          userPrincipal.Slug,
		Action:            action,
		Resource:          model.ResourceProfile,
		ResourceOwnerSlug: &userPrincipal.Slug,
	})
	if err != nil {
		return "", err
	}

	return userPrincipal.Slug, nil
}

// canPersonalAccessTokenManager authorizes session users or PATs that were
// explicitly opted into same-user personal access token management.
func (s *server) canPersonalAccessTokenManager(
	c fiber.Ctx,
	action model.Action,
) (reqctx.UserPrincipal, error) {
	if _, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		return reqctx.UserPrincipal{}, apierror.UserPrincipalRequired()
	}

	principal, ok := reqctx.GetUserPrincipal(c, s.sessionStore)
	if !ok {
		return reqctx.UserPrincipal{}, apierror.Unauthorized()
	}
	if principal.Source == reqctx.UserPrincipalSourcePersonalAccessToken &&
		!principal.CanManagePersonalAccessTokens {
		return reqctx.UserPrincipal{}, apierror.Forbidden(model.ResourceProfile.String(), action.String())
	}

	err := s.canWithInput(c, authorization.Input{
		UserSlug:          principal.Slug,
		Action:            action,
		Resource:          model.ResourceProfile,
		ResourceOwnerSlug: &principal.Slug,
	})
	if err != nil {
		return reqctx.UserPrincipal{}, err
	}

	return principal, nil
}

func (s *server) canProfile(c fiber.Ctx, action model.Action) error {
	userSlug := reqctx.GetUserSlug(c, s.sessionStore)
	return s.canWithInput(c, authorization.Input{
		UserSlug:          userSlug,
		Action:            action,
		Resource:          model.ResourceProfile,
		ResourceOwnerSlug: &userSlug,
	})
}

func (s *server) canWithInput(c fiber.Ctx, input authorization.Input) error {
	if input.UserSlug == "" && input.APIKeySlug == "" {
		return apierror.Unauthorized()
	}

	can, err := s.authorizationService.Can(c.Context(), input)
	if err != nil {
		slog.LogAttrs(
			c.Context(),
			slog.LevelError,
			"authorization check failed",
			authorizationErrorAttrs(c.Context(), err)...,
		)
		return apierror.InternalServerError()
	}

	if !can {
		return apierror.Forbidden(input.Resource.String(), input.Action.String())
	}
	if input.OrgSlug != nil &&
		c.Method() != fiber.MethodGet &&
		c.Method() != fiber.MethodHead &&
		c.Method() != fiber.MethodOptions {
		if err := s.admissionService.CheckWriteAccess(c.Context(), *input.OrgSlug); err != nil {
			return mapServiceError(err)
		}
	}

	c.SetContext(executiondelivery.WithInitiatingUser(c.Context(), input.UserSlug))
	return nil
}

func (s *server) eventActor(c fiber.Ctx) model.EventActor {
	if principal, ok := reqctx.GetAPIKeyPrincipal(c); ok {
		return model.APIKeyEventActor(principal.Slug)
	}

	return model.UserEventActor(reqctx.GetUserSlug(c, s.sessionStore))
}

func authorizationErrorAttrs(ctx context.Context, err error) []slog.Attr {
	return append(
		[]slog.Attr{slog.Any("error", err)},
		obslogging.ContextAttrs(ctx)...,
	)
}

// Sessions exposes the shared browser state to distribution route composition.
func (s *server) Sessions() *session.Store { return s.sessionStore }
