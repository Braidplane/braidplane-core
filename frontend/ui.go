package frontend

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

type Router struct {
	workspacePrivacyURL            string
	apiClient                      api.ClientWithResponsesInterface
	apiBaseURL                     string
	logger                         *slog.Logger
	sessionStore                   *session.Store
	selfServiceRegistrationEnabled bool
	allowPrivateDataSourceHosts    bool
	documentationURL               string
	applicationURL                 string
	publicOrigins                  []string
	ga4Destinations                map[string]string
	secureCookies                  bool
}

func NewRouter(
	frontendURL string,
	logger *slog.Logger,
	sessionStore *session.Store,
) (*Router, error) {
	if logger == nil {
		return nil, xerror.Unexpected().Detail("frontend logger is required")
	}

	if sessionStore == nil {
		return nil, xerror.Unexpected().Detail("frontend session store is required")
	}

	apiBaseURL := fmt.Sprintf("%s/api", strings.TrimRight(frontendURL, "/"))
	httpClient := newAPIHTTPClient()
	client, err := api.NewClientWithResponses(apiBaseURL, api.WithHTTPClient(httpClient))
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	return &Router{
		apiClient:                      client,
		apiBaseURL:                     apiBaseURL,
		applicationURL:                 strings.TrimRight(frontendURL, "/"),
		logger:                         logger,
		sessionStore:                   sessionStore,
		selfServiceRegistrationEnabled: true,
		secureCookies:                  strings.HasPrefix(strings.ToLower(strings.TrimSpace(frontendURL)), "https://"),
	}, nil
}

// SetDocumentationURL configures the validated public documentation destination.
func (r *Router) SetDocumentationURL(documentationURL string) {
	r.documentationURL = documentationURL
}

// SetApplicationURL keeps account links on the browser origin, including when the landing page uses another host.
func (r *Router) SetApplicationURL(applicationURL string) {
	r.applicationURL = strings.TrimRight(applicationURL, "/")
}

// SetPublicOrigins configures the validated browser origins used for public URLs.
func (r *Router) SetPublicOrigins(publicOrigins []string) {
	r.publicOrigins = append([]string(nil), publicOrigins...)
}

// SetSelfServiceRegistrationEnabled controls whether unauthenticated users can create accounts.
func (r *Router) SetSelfServiceRegistrationEnabled(enabled bool) {
	r.selfServiceRegistrationEnabled = enabled
}

// SetAllowPrivateDataSourceHosts controls private-host controls on data source forms.
func (r *Router) SetAllowPrivateDataSourceHosts(enabled bool) {
	r.allowPrivateDataSourceHosts = enabled
}

// RoutePresentation supplies an explicit hosted or Community route set to the core frontend.
// The composition root must choose exactly one implementation.
type RoutePresentation interface {
	RegisterPublicRoutes(*fiber.App, ManagedHost)
	RegisterWorkspaceRoutes(fiber.Router, ManagedHost)
}

// ManagedHost is the narrow core frontend contract available to route presentations.
// It preserves core authentication, shell, rendering, and request-forwarding behavior.
type ManagedHost interface {
	OrganizationPage(fiber.Ctx, string, slug.Slug) OrganizationPage
	ProjectPage(fiber.Ctx, string, slug.Slug, slug.Slug) ProjectPage
	RequestEditor(fiber.Ctx) func(context.Context, *http.Request) error
	Render(fiber.Ctx, templ.Component) error
	RedirectToLogin(fiber.Ctx, string) error
	PublicPresentation(fiber.Ctx) PublicPresentation
	APIBaseURL() string
}

// OrganizationPage supplies the shell and permissions for a distribution-owned organization page.
type OrganizationPage struct {
	Shell     views.AuthenticatedShellData
	CanDelete bool
}

// ProjectPage is the core shell and authorization state required by a managed project page.
type ProjectPage struct {
	Shell             views.AuthenticatedShellData
	CanUpdateProjects bool
}

// PublicPresentation is the configured public-page state made available to hosted routes.
type PublicPresentation struct {
	GA4MeasurementID    string
	PublicOrigin        string
	DocumentationURL    string
	ApplicationURL      string
	RegistrationEnabled bool
}

func (r *Router) RegisterRoutes(app *fiber.App, presentation RoutePresentation) {
	app.Use(r.ensureFrontendRequestID)
	app.Use(r.securityMiddleware())

	presentation.RegisterPublicRoutes(app, routerManagedHost{router: r})
	app.All("/roadmap", retiredPublicPathNotFound)
	app.All("/wishlist", retiredPublicPathNotFound)
	app.All("/wishlist/*", retiredPublicPathNotFound)
	app.All("/api/wishlist-registrations", retiredPublicPathNotFound)
	app.All("/api/wishlist-registrations/*", retiredPublicPathNotFound)
	app.Get("/auth/login", r.loadAuthenticationMethods, r.handleLoginPage)
	app.Post("/auth/login", r.loadAuthenticationMethods, r.handleLoginSubmit)
	app.Post("/auth/mfa", r.handleMFASubmit)
	app.Get("/auth/signup", r.loadAuthenticationMethods, r.handleSignupPage)
	app.Post("/auth/signup", r.loadAuthenticationMethods, r.handleSignupSubmit)
	app.Get("/auth/forgot-password", r.handleForgotPasswordPage)
	app.Post("/auth/forgot-password", r.handleForgotPasswordSubmit)
	app.Get("/auth/reset-password", r.handleResetPasswordPage)
	app.Post("/auth/reset-password", r.handleResetPasswordSubmit)
	app.Post("/auth/resend-verification", r.handleResendVerificationEmail)
	app.Get("/auth/error", r.handleAuthErrorPage)
	app.Get("/verify-email", r.handleVerifyEmail)

	protected := app.Group("", r.requireAuth)
	protected.Post("/auth/logout", r.handleLogout)
	protected.Get("/account", r.handleAccountPage)
	protected.Post("/account/deletion", r.handleAccountDeletionSubmit)
	protected.Post("/account/profile", r.handleAccountProfileSubmit)
	protected.Post("/account/security", r.handleAccountSecuritySubmit)
	protected.Post("/account/security/password", r.handleAccountPasswordSubmit)
	protected.Post("/account/security/personal-access-tokens", r.handleCreateAccountPATSubmit)
	protected.Post("/account/security/personal-access-tokens/:patSlug/update", r.handleUpdateAccountPATSubmit)
	protected.Post("/account/security/personal-access-tokens/:patSlug/rotate", r.handleRotateAccountPATSubmit)
	protected.Post("/account/security/personal-access-tokens/:patSlug/revoke", r.handleRevokeAccountPATSubmit)
	protected.Post("/account/security/personal-access-tokens/:patSlug/delete", r.handleDeleteAccountPATSubmit)
	protected.Post("/account/member-invitations/:slug/:invitationSlug/accept", r.handleAcceptAccountMemberInvitationSubmit)
	protected.Post("/account/member-invitations/:slug/:invitationSlug/decline", r.handleDeclineAccountMemberInvitationSubmit)
	protected.Get("/member-invitations/accept", r.handleMemberInvitationLandingPage)
	protected.Post("/member-invitations/accept", r.handleAcceptMemberInvitationSubmit)
	protected.Post("/member-invitations/decline", r.handleDeclineMemberInvitationSubmit)
	protected.Get("/workspace", r.handleWorkspaceGatePage)
	protected.Post("/workspace", r.handleWorkspaceGateSubmit)
	protected.Post("/organizations/:slug/switch", r.handleSwitchOrganization)

	protected.Use(r.requireWorkspace)
	protected.Get("/dashboard", r.handleDashboardPage)
	protected.Get("/builder", r.handleBuilderBlueprintPage)
	protected.Get("/builder/schema", r.handleBuilderSchema)
	protected.Post("/builder/mapping", r.handleBuilderMapping)
	protected.Get("/builder/run-evidence", r.handleBuilderRunEvidence)
	protected.Post("/builder/export", r.handleBuilderExport)
	protected.Post("/builder/validate", r.handleBuilderValidate)
	protected.Post("/builder/import", r.handleBuilderImport)
	protected.Get("/orgs/:slug/projects/:projectSlug/contracts", r.handleODCSCatalog)
	presentation.RegisterWorkspaceRoutes(protected, routerManagedHost{router: r})
	protected.Get("/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/environments/:environmentSlug/recency", r.handlePipelineRecency)
	protected.Post("/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/environments/:environmentSlug/recency", r.handlePipelineRecency)
	protected.Get("/orgs/:slug/projects/:projectSlug/pipelines", r.handleProjectPipelinesPage)
	protected.Get("/orgs/:slug/projects/:projectSlug/pipeline-sync", r.handlePipelineSync)
	protected.Post("/orgs/:slug/projects/:projectSlug/pipeline-sync", r.handlePipelineSync)
	protected.Get("/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/bindings", r.handlePipelineBindingSetup)
	protected.Get("/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/deploy", r.handlePipelineDeploy)
	protected.Post("/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/deploy", r.handlePipelineDeploy)
	protected.Get("/orgs/:slug/projects/:projectSlug/schedules", r.handleProjectPipelineSchedulesPage)
	protected.Get("/orgs/:slug/projects/:projectSlug/schedules/:triggerSlug", r.handlePipelineScheduleDetailPage)
	protected.Post("/orgs/:slug/projects/:projectSlug/schedules/:triggerSlug/deliveries/:deliveryID/replay", r.handlePipelineWebhookDeliveryReplaySubmit)
	protected.Get("/orgs/:slug/projects/:projectSlug/environments", r.handleProjectEnvironmentsPage)
	protected.Get(
		"/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/deployment",
		r.handleProjectPipelineDeploymentDetailPage,
	)
	protected.Get(
		"/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug",
		r.handleProjectPipelineDetailPage,
	)
	protected.Get("/organizations/:slug/projects", r.handleOrganizationProjectsPage)
	protected.Get("/organizations/:slug/projects/:projectSlug", r.handleOrganizationProjectDetailPage)
	protected.Post("/organizations/:slug/projects", r.handleCreateProjectSubmit)
	protected.Post(
		"/organizations/:slug/projects/:projectSlug/environments/enable",
		r.handleProjectOverviewEnableEnvironmentSubmit,
	)
	protected.Post(
		"/organizations/:slug/projects/:projectSlug/environments/:environmentSlug/default",
		r.handleProjectOverviewSetDefaultEnvironmentSubmit,
	)
	protected.Post(
		"/organizations/:slug/projects/:projectSlug/environments/:environmentSlug/disable",
		r.handleProjectOverviewDisableEnvironmentSubmit,
	)
	protected.Post(
		"/organizations/:slug/projects/:projectSlug/environments/:environmentSlug/data-source-bindings/:dataSourceRefSlug/upsert",
		r.handleProjectOverviewUpsertDataSourceBindingSubmit,
	)
	protected.Post(
		"/organizations/:slug/projects/:projectSlug/environments/:environmentSlug/data-source-bindings/:dataSourceRefSlug/delete",
		r.handleProjectOverviewDeleteDataSourceBindingSubmit,
	)
	protected.Get("/orgs/:slug/projects/:projectSlug/proof-receipts", r.handleProofReceiptHistoryPage)
	protected.Get("/orgs/:slug/projects/:projectSlug/environments/:environmentSlug/proof-receipts/:receiptSlug", r.handleProofReceiptPage)
	protected.Get("/organizations/:slug/github", r.handleOrganizationGitHubPage)
	protected.Get("/organizations/:slug/connections", r.handleProviderConnectionsPage)
	protected.Post("/organizations/:slug/gitlab/connect", r.handleOrganizationGitLabConnect)
	protected.Post("/organizations/:slug/gitlab/disconnect", r.handleOrganizationGitLabDisconnect)
	protected.Get("/organizations/:slug/proof", r.handleProofConfigurationPage)
	protected.Get("/orgs/:slug/data-sources/:dsSlug/proof", r.handleProofConfigurationPage)
	protected.Post("/orgs/:slug/data-sources/:dsSlug/proof", r.handleProofConfigurationSave)
	protected.Post("/orgs/:slug/data-sources/:dsSlug/gitlab-connection", r.handleAttachOrganizationGitLabConnection)
	protected.Post("/organizations/:slug/github/connect", r.handleOrganizationGitHubConnect)
	protected.Post("/organizations/:slug/github/authorize-existing", r.handleOrganizationGitHubAuthorizeExisting)
	protected.Get("/github/callback", r.handleOrganizationGitHubCallback)
	protected.Post("/organizations/:slug/github/disconnect", r.handleOrganizationGitHubDisconnect)
	protected.Post("/orgs/:slug/projects/:projectSlug/pipelines/save", r.handleBuilderSave)
	protected.Post(
		"/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/publish",
		r.handleBuilderPublishPipelineDraft,
	)
	protected.Post(
		"/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/proposal-refresh",
		r.handleBuilderRefreshPipelineProposal,
	)
	protected.Get(
		"/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/proposal-review",
		r.handleBuilderProposalReview,
	)
	protected.Get(
		"/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/canonical-state",
		r.handleGetCanonicalPipelineState,
	)
	protected.Post(
		"/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/draft/discard",
		r.handleBuilderDiscardPipelineDraft,
	)
	protected.Post(
		"/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/draft/delete",
		r.handleDeletePipelineDraftSubmit,
	)
	protected.Post(
		"/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/execute",
		r.handleExecutePipelineSubmit,
	)
	protected.Post(
		"/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/validation/revalidate",
		r.handleRevalidatePipelineSubmit,
	)
	protected.Post(
		"/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/environments/:environmentSlug/data-source-bindings/:dataSourceRefSlug/upsert",
		r.handlePipelineUpsertDataSourceBindingSubmit,
	)
	protected.Post(
		"/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/environments/:environmentSlug/data-source-bindings/:dataSourceRefSlug/delete",
		r.handlePipelineDeleteDataSourceBindingSubmit,
	)
	protected.Post("/orgs/:slug/projects/:projectSlug/pipelines/:pipelineSlug/bootstrap-checkpoints", r.handleBootstrapPipelineCheckpointsSubmit)
	protected.Get("/orgs/:slug/members", r.handleMembersPage)
	protected.Get("/orgs/:slug/events", r.handleEventsPage)
	protected.Get("/orgs/:slug/events/export", r.handleSecurityAuditEventsExport)
	protected.Get("/orgs/:slug/notifications", r.handleNotificationsPage)
	protected.Get("/orgs/:slug/alerts", r.handleAlertsPage)
	protected.Get("/orgs/:slug/alerts/connections", r.handleNotificationConnectionsPage)
	protected.Post("/orgs/:slug/alerts/connections", r.handleCreateNotificationConnection)
	protected.Post("/orgs/:slug/alerts/connections/:notification_connection_slug/connect", r.handleConnectNotificationConnection)
	protected.Post("/orgs/:slug/alerts/connections/:notification_connection_slug/disconnect", r.handleDisconnectNotificationConnection)
	protected.Get("/orgs/:slug/data-sources", r.handleDataSourcesPage)
	protected.Get("/orgs/:slug/data-sources/:dsSlug", r.handleDataSourceDetailPage)
	protected.Get("/orgs/:slug/data-sources/:dsSlug/snowflake-onboarding", r.handleSnowflakeOnboarding)
	protected.Post("/orgs/:slug/data-sources/:dsSlug/snowflake-identity", r.handleSnowflakeIdentity)
	protected.Get("/orgs/:slug/queries", r.handleQueriesPage)
	protected.Post("/orgs/:slug/projects/:projectSlug/runs/:runSlug/recover", r.handleRecoverPipelineRunSubmit)
	protected.Get("/orgs/:slug/projects/:projectSlug/runs", r.handleProjectPipelineRunsPage)
	protected.Get(
		"/orgs/:slug/projects/:projectSlug/runs/:runSlug",
		r.handleProjectPipelineRunDetailPage,
	)
	protected.Get("/orgs/:slug/data-sources/:dsSlug/queries/:querySlug", r.handleQueryDetailPage)
	protected.Get("/orgs/:slug/data-sources/:dsSlug/queries/:querySlug/pipeline", r.handleQueryPipelineSetup)
	protected.Post("/orgs/:slug/data-sources/:dsSlug/queries/:querySlug/pipeline", r.handleQueryPipelineSetup)
	protected.Get("/orgs/:slug/projects/:projectSlug/pipeline-creations/:creationSlug", r.handlePipelineCreationProgress)
	protected.Get("/orgs/:slug/projects/:projectSlug/pipeline-creations", r.handlePipelineCreationList)
	protected.Post("/orgs/:slug/projects/:projectSlug/pipeline-creations", r.handleBuilderCreatePipeline)
	protected.Post("/orgs/:slug/projects/:projectSlug/pipeline-creations/:creationSlug", r.handlePipelineCreationProgress)
	protected.Get("/organizations", r.handleOrganizationsPage)
	protected.Get("/organizations/:slug/members", r.handleMembersPage)
	protected.Get("/organizations/:slug/roles", r.handleOrganizationRolesPage)
	protected.Get("/organizations/:slug/security", r.handleOrganizationSecurityPage)
	protected.Post("/orgs/:slug/queries", r.handleCreateQuerySubmit)
	protected.Post(
		"/orgs/:slug/data-sources/:dsSlug/queries/:querySlug/update",
		r.handleUpdateQuerySubmit,
	)
	protected.Post(
		"/orgs/:slug/data-sources/:dsSlug/queries/:querySlug/delete",
		r.handleDeleteQuerySubmit,
	)
	protected.Post(
		"/orgs/:slug/data-sources/:dsSlug/queries/:querySlug/restore",
		r.handleRestoreQuerySubmit,
	)
	protected.Post(
		"/orgs/:slug/data-sources/:dsSlug/queries/:querySlug/purge",
		r.handlePurgeQuerySubmit,
	)
	protected.Post(
		"/orgs/:slug/data-sources/:dsSlug/queries/:querySlug/execute",
		r.handleExecuteQuerySubmit,
	)
	protected.Post("/orgs/:slug/data-sources", r.handleCreateDataSourceSubmit)
	protected.Post("/orgs/:slug/data-sources/hubspot/oauth", r.handleStartHubSpotDataSourceOAuthSubmit)
	protected.Post("/orgs/:slug/data-sources/bigquery/oauth", r.handleStartBigQueryDataSourceOAuthSubmit)
	protected.Post("/orgs/:slug/data-sources/google_workspace/oauth", r.handleStartWorkspaceDataSourceOAuthSubmit)
	protected.Post("/orgs/:slug/data-sources/:dsSlug/google_workspace/oauth", r.handleStartExistingWorkspaceOAuthSubmit)
	protected.Get("/orgs/:slug/data-sources/:dsSlug/google_workspace/worksheets", r.handleWorkspaceWorksheets)
	protected.Get("/orgs/:slug/data-sources/:dsSlug/google_workspace/files", r.handleWorkspaceDiscovery)
	protected.Get("/orgs/:slug/data-sources/:dsSlug/google_workspace/picker", r.handleWorkspacePicker)
	protected.Post("/orgs/:slug/data-sources/:dsSlug/google_workspace/files/verify", r.handleWorkspaceVerify)
	protected.Post("/orgs/:slug/data-sources/:dsSlug/provider-repository", r.handleSelectDataSourceProviderRepositorySubmit)
	protected.Post("/orgs/:slug/data-sources/:dsSlug/http/oauth", r.handleConnectHTTPOAuthSubmit)
	protected.Post("/orgs/:slug/data-sources/:dsSlug/hubspot/oauth", r.handleStartExistingHubSpotOAuthSubmit)
	protected.Post("/orgs/:slug/data-sources/:dsSlug/bigquery/oauth", r.handleStartExistingBigQueryOAuthSubmit)
	protected.Post("/orgs/:slug/data-sources/parse-uri", r.handleCreateDataSourceParseURI)
	protected.Post(
		"/orgs/:slug/data-sources/test-connection",
		r.handleTestDataSourceConnectionSubmit,
	)
	protected.Post(
		"/orgs/:slug/data-sources/:dsSlug/test-connection",
		r.handleTestExistingDataSourceConnectionSubmit,
	)
	protected.Post("/orgs/:slug/data-sources/:dsSlug/update", r.handleUpdateDataSourceSubmit)
	protected.Post(
		"/orgs/:slug/data-sources/:dsSlug/credentials/update",
		r.handleUpdateDataSourceCredentialsSubmit,
	)
	protected.Post(
		"/orgs/:slug/data-sources/:dsSlug/schema/refresh",
		r.handleRefreshDataSourceSchemaSubmit,
	)
	protected.Post("/orgs/:slug/data-sources/:dsSlug/delete", r.handleDeleteDataSourceSubmit)
	protected.Post("/orgs/:slug/members/invite", r.handleCreateMemberSubmit)
	protected.Post("/organizations/:slug/members/invite", r.handleCreateMemberSubmit)
	protected.Post("/orgs/:slug/member-invitations/:invitationSlug/resend", r.handleResendMemberInvitationSubmit)
	protected.Post("/organizations/:slug/member-invitations/:invitationSlug/resend", r.handleResendMemberInvitationSubmit)
	protected.Post("/orgs/:slug/member-invitations/:invitationSlug/revoke", r.handleRevokeMemberInvitationSubmit)
	protected.Post("/organizations/:slug/member-invitations/:invitationSlug/revoke", r.handleRevokeMemberInvitationSubmit)
	protected.Post("/orgs/:slug/members/:userSlug/role", r.handleUpdateMemberRoleSubmit)
	protected.Post("/organizations/:slug/members/:userSlug/role", r.handleUpdateMemberRoleSubmit)
	protected.Post("/orgs/:slug/members/:userSlug/remove", r.handleRemoveMemberSubmit)
	protected.Post("/organizations/:slug/members/:userSlug/remove", r.handleRemoveMemberSubmit)
	protected.Post("/orgs/:slug/alerts/destinations", r.handleCreateAlertDestinationSubmit)
	protected.Post("/orgs/:slug/alerts/destinations/:destinationSlug/test", r.handleTestAlertDestinationSubmit)
	protected.Post("/orgs/:slug/alerts/destinations/:destinationSlug/update", r.handleUpdateAlertDestinationSubmit)
	protected.Post("/orgs/:slug/alerts/destinations/:destinationSlug/delete", r.handleDeleteAlertDestinationSubmit)
	protected.Get("/orgs/:slug/alerts/policies/selector-options", r.handleAlertPolicySelectorOptions)
	protected.Post("/orgs/:slug/alerts/policies", r.handleCreateAlertPolicySubmit)
	protected.Post("/orgs/:slug/alerts/policies/:policySlug/update", r.handleUpdateAlertPolicySubmit)
	protected.Post("/orgs/:slug/alerts/policies/:policySlug/delete", r.handleDeleteAlertPolicySubmit)
	protected.Post(
		"/orgs/:slug/notifications/:notificationSlug/acknowledge",
		r.handleAcknowledgeNotificationSubmit,
	)
	protected.Post(
		"/orgs/:slug/notifications/:notificationSlug/resolve",
		r.handleResolveNotificationSubmit,
	)
	protected.Post("/orgs/:slug/projects/:projectSlug/environments/enable", r.handleEnableProjectEnvironmentSubmit)
	protected.Post(
		"/orgs/:slug/projects/:projectSlug/environments/:environmentSlug/default",
		r.handleSetDefaultProjectEnvironmentSubmit,
	)
	protected.Post(
		"/orgs/:slug/projects/:projectSlug/environments/:environmentSlug/disable",
		r.handleDisableProjectEnvironmentSubmit,
	)
	protected.Get("/organizations/new", r.handleCreateOrganizationPage)
	protected.Get("/organizations/:slug", r.handleOrganizationDetailPage)
	protected.Post("/organizations/:slug/delete", r.handleOrganizationDeletionSubmit)
	protected.Get("/organizations/:slug/retention", r.handleOrganizationRetentionPage)
	protected.Post("/organizations/:slug/retention", r.handleOrganizationRetentionSubmit)
	protected.Get("/organizations/:slug/environments", r.handleOrganizationEnvironmentsPage)
	protected.Get("/organizations/:slug/environments/:environmentSlug", r.handleOrganizationEnvironmentDetailPage)
	protected.Post("/organizations/:slug/environments", r.handleCreateEnvironmentSubmit)
	protected.Post(
		"/organizations/:slug/environments/:environmentSlug/update",
		r.handleUpdateEnvironmentSubmit,
	)
	protected.Post(
		"/organizations/:slug/environments/:environmentSlug/delete",
		r.handleDeleteEnvironmentSubmit,
	)
	protected.Post(
		"/organizations/:slug/environments/:environmentSlug/runtime-variables",
		r.handleUpsertEnvironmentRuntimeVariableSubmit,
	)
	protected.Post(
		"/organizations/:slug/environments/:environmentSlug/runtime-variables/:runtimeVariableKey/delete",
		r.handleDeleteEnvironmentRuntimeVariableSubmit,
	)
	protected.Post(
		"/organizations/:slug/environments/:environmentSlug/secrets",
		r.handleUpsertEnvironmentSecretSubmit,
	)
	protected.Post(
		"/organizations/:slug/environments/:environmentSlug/secrets/:environmentSecretKey/delete",
		r.handleDeleteEnvironmentSecretSubmit,
	)
	protected.Get("/organizations/:slug/projects/:projectSlug/settings/pipeline-backend/branches", r.handleProjectBackendBranches)
	protected.Post(
		"/organizations/:slug/projects/:projectSlug/settings/pipeline-backend",
		r.handleUpdateProjectPipelineBackendSubmit,
	)
	protected.Post(
		"/organizations/:slug/projects/:projectSlug/delete",
		r.handleDeleteProjectSubmit,
	)
	protected.Post("/organizations/:slug/roles", r.handleCreateRoleSubmit)
	protected.Post(
		"/organizations/:slug/roles/:roleSlug/update",
		r.handleUpdateRoleSubmit,
	)
	protected.Post(
		"/organizations/:slug/roles/:roleSlug/archive",
		r.handleArchiveRoleSubmit,
	)
	protected.Post(
		"/organizations/:slug/roles/:roleSlug/delete",
		r.handleArchiveRoleSubmit,
	)
	protected.Post(
		"/organizations/:slug/roles/:roleSlug/restore",
		r.handleRestoreRoleSubmit,
	)
	protected.Post("/organizations/:slug/security/api-keys", r.handleCreateAPIKeySubmit)
	protected.Post(
		"/organizations/:slug/security/api-keys/:apiKeySlug/update",
		r.handleUpdateAPIKeySubmit,
	)
	protected.Post(
		"/organizations/:slug/security/api-keys/:apiKeySlug/rotate",
		r.handleRotateAPIKeySubmit,
	)
	protected.Post(
		"/organizations/:slug/security/api-keys/:apiKeySlug/revoke",
		r.handleRevokeAPIKeySubmit,
	)
	protected.Post(
		"/organizations/:slug/security/api-keys/:apiKeySlug/delete",
		r.handleDeleteAPIKeySubmit,
	)
	protected.Post("/organizations", r.handleCreateOrganizationSubmit)

	protected.Get("/organizations/:slug/datasources/new", r.handleDataSourceNewPage)
}

func retiredPublicPathNotFound(c fiber.Ctx) error {
	return c.SendStatus(fiber.StatusNotFound)
}

func (r *Router) memberInvitationsEnabled() bool {
	return true
}

func (r *Router) requireAuth(c fiber.Ctx) error {
	userSlug := reqctx.GetUserSlug(c, r.sessionStore)
	if userSlug == "" {
		return redirectToLogin(c, authenticatedReturnTo(c))
	}

	return c.Next()
}

// authenticatedReturnTo maps protected POST-only account actions back to their GET-able recovery pages.
func authenticatedReturnTo(c fiber.Ctx) string {
	if c.Method() == fiber.MethodPost && c.Path() == "/account/deletion" {
		return "/account"
	}
	if c.Method() == fiber.MethodPost && c.Path() == "/account/security/password" {
		return views.AccountTabHref(views.AccountTabSecurity)
	}
	return c.OriginalURL()
}

func renderTempl(c fiber.Ctx, component templ.Component) error {
	c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
	// The same URL can render a document or a fragment; browser history must not reuse the wrong variant.
	c.Vary("HX-Request", "HX-Target", "HX-History-Restore-Request")
	pageURL := c.OriginalURL()
	if c.Method() != fiber.MethodGet {
		if currentURL := safeCurrentRequestPath(c.Get("HX-Current-URL"), c.Host()); currentURL != "" {
			pageURL = currentURL
		}
	}
	return component.Render(views.WithEntityPageURL(c.Context(), pageURL), c.Response().BodyWriter())
}

func isHTMXRequest(c fiber.Ctx) bool {
	return strings.EqualFold(c.Get("HX-Request"), "true")
}

func setHTMLResponseStatus(c fiber.Ctx, status int) {
	if isHTMXRequest(c) && status >= fiber.StatusBadRequest {
		c.Status(fiber.StatusOK)
		return
	}
	c.Status(status)
}

func redirectHTMXOrSeeOther(c fiber.Ctx, to string) error {
	if isHTMXRequest(c) {
		c.Set("HX-Redirect", to)
		return c.SendStatus(fiber.StatusOK)
	}

	return c.Redirect().Status(fiber.StatusSeeOther).To(to)
}

// SetPublicAnalytics binds validated measurement IDs to their public origins.
func (r *Router) SetPublicAnalytics(destinations map[string]string) {
	r.ga4Destinations = destinations
}

// SetWorkspacePrivacyURL configures workspace privacy help.
func (r *Router) SetWorkspacePrivacyURL(value string) { r.workspacePrivacyURL = value }
