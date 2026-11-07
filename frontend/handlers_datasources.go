package frontend

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/dsparser"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	validationPortMustBeIntegerMessage      = "Validation: port must be a valid integer."
	validationConnectTimeoutPositiveMessage = "Validation: connect timeout must be a positive integer."
	validationTimeoutSecondsPositiveMessage = "Validation: timeout seconds must be a positive integer."
	validationUnsupportedTypeMessage        = "Validation: unsupported data source type."
	validationCredentialsRequiredMessage    = "Validation: username and password are required."
	dataSourceTypePostgresString            = "postgres"
	gitAuthTypeHTTPS                        = "https"
	gitAuthTypeSSH                          = "ssh"
	hubSpotAccessModeField                  = "hubspot_access_mode"
	hubSpotAccessModeWrite                  = "write"
	gitLabAccessModeField                   = "gitlab_access_mode"
	gitLabAccessModeWrite                   = "write"
	snowflakeAuthTypePassword               = "password"
	snowflakeAuthTypeWorkloadIdentity       = "workload_identity"
	booleanTrueString                       = "true"
	feedbackKindError                       = "error"
	databaseAllowPrivateHostsField          = "database_allow_private_hosts"
)

func (r *Router) handleDataSourcesPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	if isHTMXRequest(c) && c.Get("HX-History-Restore-Request") != "true" && c.Get("HX-Target") == "data-source-side-panel" {
		selected, panel, connection, form, status, summary, _, unauthorized := r.loadSelectedDataSourcePanel(c, orgSlug, strings.TrimSpace(c.Query("selected")))
		if unauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		setHTMLResponseStatus(c, status)
		return renderTempl(c, views.DataSourceDetailPanelSlot(orgSlug.String(), requestRuntimeEnvironmentSlug(c), selected, panel, connection, form, views.DefaultOrganizationPermissions(), summary, feedbackKindError))
	}

	pageError := strings.TrimSpace(c.Query("error"))
	providerOAuthOutcome := c.Query("provider_oauth")
	c.Request().URI().QueryArgs().Del("provider_oauth")
	c.Request().SetRequestURIBytes(c.Request().URI().RequestURI())
	pageFeedback := validatedDataSourceOAuthFeedback(providerOAuthOutcome, nil)
	pageErrorDetails := ""
	activeFilter := views.NormalizeDataSourceListFilter(c.Query("filter"))
	list := &api.DataSourceListResponse{}
	summaryDataSources := []api.DataSource{}
	var status int
	var errorResponse *api.ErrorResponse
	var listErr error

	if activeFilter != "" {
		summaryDataSources, status, errorResponse, listErr = r.listAllDataSourcesViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
		)
		if listErr == nil {
			var filteredDataSources []api.DataSource
			filteredDataSources, status, errorResponse, listErr = r.filterDataSourcesForList(
				c.Context(),
				c.Get(fiber.HeaderCookie),
				orgSlug.String(),
				summaryDataSources,
				activeFilter,
			)
			list = &api.DataSourceListResponse{
				DataSources: filteredDataSources,
				HasMore:     false,
			}
		}
	} else {
		params := &api.ListDataSourcesParams{}
		limit := api.LimitParam(20)
		params.Limit = &limit

		cursor := strings.TrimSpace(c.Query("cursor"))
		if cursor != "" {
			params.Cursor = &cursor
		}

		list, status, errorResponse, listErr = r.listDataSourcesViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			params,
		)
		if listErr == nil {
			summaryDataSources = list.DataSources
		}
	}
	if listErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		if status == fiber.StatusForbidden && pageError == "" {
			pageError = "You do not have access to this organization's data sources."
		} else if pageError == "" {
			message := messageFromErrorResponse(errorResponse)
			if message == "" {
				message = "Something went wrong while loading data sources."
			}
			pageError = message
		}

		failureAction := "list data sources"
		var responseErr *backendAPIResponseError
		if activeFilter != "" && xerror.As(listErr, &responseErr) && responseErr != nil {
			failureAction = responseErr.action
		}
		pageErrorDetails = backendErrorDetails(failureAction, status, errorResponse, listErr)
		r.logRenderedBackendAPIFailure(c, failureAction, errorResponse, listErr, orgSlug.String(), "")

		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Data sources",
			orgSlug.String(),
			pageError,
			pageErrorDetails,
			c.Get(fiber.HeaderCookie),
		)
		shellData.Subtitle = "Connector inventory, credential-safe setup, connection readiness, and schema entry points."
		permissions := r.resolveOrganizationPermissions(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
		)
		formData := r.defaultDataSourceCreateFormData()
		if c.Query("type") == string(api.DataSourceTypeBigquery) || c.Query("type") == string(api.DataSourceTypeS3) {
			formData.Type = c.Query("type")
			formData.Name = strings.TrimSpace(c.Query("name"))
			formData.Slug = strings.TrimSpace(c.Query("slug"))
		}
		r.loadManagedGitRepositories(c, orgSlug.String(), &formData)
		c.Status(status)
		return renderTempl(
			c,
			views.DataSourcesPage(
				shellData,
				[]api.DataSource{},
				[]api.DataSource{},
				false,
				"",
				"",
				orgSlug.String(),
				"",
				nil,
				views.DefaultDataSourceSchemaPanelData(),
				nil,
				views.DataSourceUpdateFormData{},
				formData,
				permissions,
				"",
				"",
				pageFeedback,
			),
		)
	}

	nextCursor := ""
	prevCursor := ""
	if list.NextCursor != nil {
		nextCursor = *list.NextCursor
	}
	if list.PrevCursor != nil {
		prevCursor = *list.PrevCursor
	}

	selectedDataSource, schemaPanel, connectionInfo, updateFormData, selectedStatus, selectedSummary, selectedDetails, selectedUnauthorized := r.loadSelectedDataSourcePanel(
		c,
		orgSlug,
		strings.TrimSpace(c.Query("selected")),
	)
	if selectedUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if selectedSummary != "" && pageError == "" {
		pageError = selectedSummary
	}
	if selectedDetails != "" {
		pageErrorDetails = mergeErrorDetails(pageErrorDetails, selectedDetails)
	}
	if selectedStatus >= http.StatusBadRequest {
		setHTMLResponseStatus(c, selectedStatus)
	}
	selectedFeedbackMessage := ""
	selectedFeedbackKind := ""
	if selectedStatus >= http.StatusBadRequest && selectedSummary != "" {
		selectedFeedbackMessage = selectedSummary
		selectedFeedbackKind = feedbackKindError
	}
	pageFeedback = validatedDataSourceOAuthFeedback(providerOAuthOutcome, selectedDataSource)

	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Data sources",
		orgSlug.String(),
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Connector inventory, credential-safe setup, connection readiness, and schema entry points."
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	formData := r.defaultDataSourceCreateFormData()
	if c.Query("type") == string(api.DataSourceTypeBigquery) || c.Query("type") == string(api.DataSourceTypeS3) {
		formData.Type = c.Query("type")
		formData.Name = strings.TrimSpace(c.Query("name"))
		formData.Slug = strings.TrimSpace(c.Query("slug"))
	}
	r.loadManagedGitRepositories(c, orgSlug.String(), &formData)

	return renderTempl(
		c,
		views.DataSourcesPage(
			shellData,
			list.DataSources,
			summaryDataSources,
			list.HasMore,
			nextCursor,
			prevCursor,
			orgSlug.String(),
			activeFilter,
			selectedDataSource,
			schemaPanel,
			connectionInfo,
			updateFormData,
			formData,
			permissions,
			selectedFeedbackMessage,
			selectedFeedbackKind,
			pageFeedback,
		),
	)
}

// validatedDataSourceOAuthFeedback accepts success only when persisted OAuth metadata proves activation.
func validatedDataSourceOAuthFeedback(outcome string, dataSource *api.DataSource) views.DataSourcePageFeedback {
	if dataSource != nil && dataSource.Type == api.DataSourceTypeHttp {
		if !views.UsesHTTPOAuth(*dataSource) {
			return views.DataSourcePageFeedback{}
		}
		if strings.EqualFold(strings.TrimSpace(outcome), "success") && (!dataSource.IsActive || dataSource.Credential.Status != api.CredentialStatusActive) {
			return views.DataSourcePageFeedback{}
		}
		feedback := views.DataSourceOAuthFeedback(outcome)
		feedback.Message = strings.ReplaceAll(feedback.Message, "HubSpot", "HTTP API")
		return feedback
	}

	if dataSource != nil && dataSource.Type == api.DataSourceTypeGoogleWorkspace {
		if strings.EqualFold(outcome, "success") {
			if dataSource.Credential == nil || dataSource.Credential.Kind != api.Oauth2Grant || dataSource.Credential.Status != api.CredentialStatusActive {
				return views.DataSourcePageFeedback{}
			}
			message := "Google connected. The Workspace account is ready for queries and pipelines."
			if dataSource.IsActive {
				message = "Google reconnected successfully."
			}
			return views.DataSourcePageFeedback{Message: message, Kind: "success"}
		}
		feedback := views.DataSourceOAuthFeedback(outcome)
		feedback.Message = strings.ReplaceAll(feedback.Message, "HubSpot", "Google")
		return feedback
	}

	if strings.EqualFold(strings.TrimSpace(outcome), "select_repository") {
		if dataSource == nil || dataSource.Type != api.DataSourceTypeGitlab || dataSource.IsActive ||
			dataSource.Credential == nil || dataSource.Credential.Kind != api.Oauth2Grant ||
			dataSource.Credential.Status != api.CredentialStatusActive {
			return views.DataSourcePageFeedback{}
		}
		return views.DataSourcePageFeedback{
			Message: "GitLab authorization succeeded. Choose the project for this data source.",
			Kind:    "warning",
		}
	}
	if strings.EqualFold(strings.TrimSpace(outcome), "success") {
		if dataSource == nil ||
			(dataSource.Type != api.DataSourceTypeHubspot && dataSource.Type != api.DataSourceTypeGitlab && dataSource.Type != api.DataSourceTypeBigquery) ||
			!dataSource.IsActive ||
			dataSource.Credential == nil ||
			dataSource.Credential.Kind != api.Oauth2Grant ||
			dataSource.Credential.Status != api.CredentialStatusActive {
			return views.DataSourcePageFeedback{}
		}
		if dataSource.Type == api.DataSourceTypeGitlab {
			return views.DataSourcePageFeedback{Message: "GitLab connected successfully.", Kind: "success"}
		}
	}
	feedback := views.DataSourceOAuthFeedback(outcome)
	if dataSource != nil && dataSource.Type == api.DataSourceTypeBigquery {
		feedback.Message = strings.ReplaceAll(feedback.Message, "HubSpot", "BigQuery")
	}
	return feedback
}

func (r *Router) handleSelectDataSourceProviderRepositorySubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	dsSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}
	repositoryID := strings.TrimSpace(c.FormValue("repository_id"))
	if repositoryID == "" {
		return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(dataSourceReturnURL(c, orgSlug.String(), dsSlug.String()), "error", "Choose a repository."))
	}
	status, errorResponse, selectErr := r.selectProviderRepositoryViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dsSlug.String(),
		repositoryID,
	)
	if selectErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		r.logRenderedBackendAPIFailure(c, "select provider repository", errorResponse, selectErr, orgSlug.String(), dsSlug.String())
		return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(dataSourceReturnURL(c, orgSlug.String(), dsSlug.String()), "error", "GitLab project selection failed. Choose the project again."))
	}
	return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(dataSourceReturnURL(c, orgSlug.String(), dsSlug.String()), "notice", "Repository selected."))
}

func (r *Router) loadSelectedDataSourcePanel(
	c fiber.Ctx,
	orgSlug slug.Slug,
	selectedDataSourceSlug string,
) (*api.DataSource, views.DataSourceSchemaPanelData, *views.ConnectionInfo, views.DataSourceUpdateFormData, int, string, string, bool) {
	if strings.TrimSpace(selectedDataSourceSlug) == "" {
		return nil, views.DefaultDataSourceSchemaPanelData(), nil, views.DataSourceUpdateFormData{}, http.StatusOK, "", "", false
	}

	dataSourceSlug, err := slug.New(selectedDataSourceSlug)
	if err != nil {
		return nil, views.DefaultDataSourceSchemaPanelData(), nil, views.DataSourceUpdateFormData{}, http.StatusBadRequest, "Select a valid data source.", "", false
	}

	dataSource, status, errorResponse, getErr := r.getDataSourceViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return nil, views.DefaultDataSourceSchemaPanelData(), nil, views.DataSourceUpdateFormData{}, status, "", "", true
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while loading data source details."
		}
		r.logRenderedBackendAPIFailure(c, "get data source", errorResponse, getErr, orgSlug.String(), dataSourceSlug.String())
		return nil, views.DefaultDataSourceSchemaPanelData(), nil, views.DataSourceUpdateFormData{}, status, message, backendErrorDetails("get data source", status, errorResponse, getErr), false
	}

	schemaRefreshError := strings.TrimSpace(c.Query("schema_error"))

	return dataSource,
		dataSourceSchemaPanelData(dataSource, schemaRefreshError),
		extractConnectionInfo(dataSource),
		r.dataSourceUpdateFormDataFromDataSource(dataSource),
		http.StatusOK,
		"",
		"",
		false
}

// dataSourceSchemaPanelData builds the SSR schema panel from cached detail data.
func dataSourceSchemaPanelData(dataSource *api.DataSource, schemaRefreshError string) views.DataSourceSchemaPanelData {
	panel := views.DefaultDataSourceSchemaPanelData()
	if dataSource == nil {
		return panel
	}

	panel.Status = views.NormalizeDataSourceSchemaStatus(dataSource.SchemaStatus)
	if panel.Status != api.DataSourceSchemaStatusNotImported {
		panel.Title, panel.Lines, panel.Error = formatSchemaForDisplay(&dataSource.Schema, dataSource)
		if dataSource.Type == api.DataSourceTypeHttp {
			if schema, err := dataSource.Schema.AsHTTPSchemaResponse(); err == nil {
				panel.Operations = schema.Operations
			}
		}
		if panel.Error != "" {
			panel.Status = api.DataSourceSchemaStatusFailed
		}
	}
	if panel.Status == api.DataSourceSchemaStatusFailed && dataSource.SchemaDiagnostic != nil {
		if diagnostic := schemaImportDiagnosticMessage(dataSource.SchemaDiagnostic); diagnostic != "" {
			panel.Error = diagnostic
		}
	}
	if schemaRefreshError := strings.TrimSpace(schemaRefreshError); schemaRefreshError != "" {
		panel.Status = api.DataSourceSchemaStatusFailed
		panel.Error = schemaRefreshError
	}

	return panel
}

func (r *Router) handleCreateDataSourceSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	formData := r.dataSourceCreateFormDataFromRequest(c)

	payload, validationErr := buildCreateDataSourcePayloadWithPrivateHosts(c, r.allowPrivateDataSourceHosts)
	if validationErr != "" {
		formData.TestMessage = validationErr
		formData.TestSuccess = false
		return r.renderDataSourcesPageWithError(
			c,
			orgSlug,
			http.StatusBadRequest,
			"Something went wrong while creating a data source.",
			validationErr,
			formData,
		)
	}

	created, status, errorResponse, createErr := r.createDataSourceViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		payload,
	)
	if createErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := formFeedbackFromErrorResponse(errorResponse, "Failed to create data source.")
		formData.TestMessage = message
		formData.TestSuccess = false
		r.logRenderedBackendAPIFailure(c, "create data source", errorResponse, createErr, orgSlug.String(), "")

		return r.renderDataSourcesPageWithError(
			c,
			orgSlug,
			status,
			"Something went wrong while creating a data source.",
			backendErrorDetails("create data source", status, errorResponse, createErr),
			formData,
		)
	}

	selected := ""
	if created != nil && created.Type == api.DataSourceTypeSnowflake {
		selected = created.Slug
	}
	if created != nil && views.UsesHTTPOAuth(*created) {
		return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(views.DataSourceDetailHref(orgSlug.String(), dataSourceActiveEnvironmentSlug(c), created.Slug), "section", "settings"))
	}
	return redirectHTMXOrSeeOther(c, dataSourceListURL(
		orgSlug.String(),
		dataSourceActiveEnvironmentSlug(c),
		selected,
	))
}

// handleStartHubSpotDataSourceOAuthSubmit starts HubSpot OAuth-first onboarding.
func (r *Router) handleStartHubSpotDataSourceOAuthSubmit(c fiber.Ctx) error {
	return r.handleStartProviderDataSourceOAuthSubmit(c, "hubspot", "HubSpot")
}

// handleStartBigQueryDataSourceOAuthSubmit starts BigQuery OAuth-first onboarding.
func (r *Router) handleStartBigQueryDataSourceOAuthSubmit(c fiber.Ctx) error {
	return r.handleStartProviderDataSourceOAuthSubmit(c, "bigquery", "Google")
}

// handleStartProviderDataSourceOAuthSubmit validates provider-specific form input and redirects to consent.
func (r *Router) handleStartProviderDataSourceOAuthSubmit(c fiber.Ctx, provider, providerLabel string) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	formData := r.dataSourceCreateFormDataFromRequest(c)
	if formData.Type != provider {
		formData.TestMessage = validationUnsupportedTypeMessage
		formData.InvalidField = "type"
		return r.renderDataSourcesPageWithError(c, orgSlug, http.StatusBadRequest, "", "", formData)
	}
	if formData.Name == "" {
		formData.TestMessage = validationNameRequiredMessage
		formData.InvalidField = "name"
		return r.renderDataSourcesPageWithError(c, orgSlug, http.StatusBadRequest, "", "", formData)
	}
	var payloadSlug *api.Slug
	if formData.Slug != "" {
		validatedSlug, err := slug.New(formData.Slug)
		if err != nil {
			formData.TestMessage = "Validation: slug is invalid."
			formData.InvalidField = "slug"
			return r.renderDataSourcesPageWithError(c, orgSlug, http.StatusBadRequest, "", "", formData)
		}
		value := validatedSlug.String()
		payloadSlug = &value
	}

	var providerConfig api.StartProviderDataSourceInstallRequest_Config
	readOnly := formData.HubSpotReadOnly
	switch provider {
	case "hubspot":
		config, validationErr := parseHubSpotConfigForm(c)
		if validationErr != "" {
			formData.TestMessage = validationErr
			formData.InvalidField = "timeout_seconds"
			return r.renderDataSourcesPageWithError(c, orgSlug, http.StatusBadRequest, "", "", formData)
		}
		if err := providerConfig.FromHubSpotConfig(config); err != nil {
			formData.TestMessage = "Failed to prepare HubSpot authorization."
			return r.renderDataSourcesPageWithError(c, orgSlug, http.StatusInternalServerError, "", "", formData)
		}
	case "google_workspace":
		mode := api.GoogleWorkspaceAccessMode(c.FormValue("workspace_access_mode", "selected_files"))
		if mode != api.GoogleWorkspaceAccessModeSelectedFiles {
			formData.TestMessage = "Validation: Google Workspace requires selected-file access."
			c.Set("HX-Reswap", "outerHTML")
			return r.renderDataSourcesPageWithError(c, orgSlug, http.StatusBadRequest, "", "", formData)
		}
		if err := providerConfig.FromGoogleWorkspacePendingInstallConfig(api.GoogleWorkspacePendingInstallConfig{AccessMode: &mode}); err != nil {
			return err
		}
		readOnly = c.FormValue("workspace_write") != "true"
		formData.WorkspaceReadOnly = readOnly
	case "bigquery":
		config, validationErr := parseBigQueryConfigForm(c)
		if validationErr == "" && (config.DefaultDataset == nil || strings.TrimSpace(*config.DefaultDataset) == "") {
			validationErr = "Validation: choose a BigQuery dataset."
		}
		if validationErr != "" {
			formData.TestMessage = validationErr
			return r.renderDataSourcesPageWithError(c, orgSlug, http.StatusBadRequest, "", "", formData)
		}
		if err := providerConfig.FromBigQueryConfig(config); err != nil {
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		readOnly = formData.BigQueryReadOnly
	default:
		return c.SendStatus(fiber.StatusNotFound)
	}
	payload := api.StartProviderDataSourceInstallJSONRequestBody{
		Name:     formData.Name,
		Slug:     payloadSlug,
		Type:     api.StartProviderDataSourceInstallRequestType(provider),
		Config:   providerConfig,
		ReadOnly: &readOnly,
	}

	install, status, errorResponse, startErr := r.startProviderDataSourceOAuthViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		provider,
		payload,
	)
	if startErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		formData.TestMessage = formFeedbackFromErrorResponse(errorResponse, "Failed to start "+providerLabel+" authorization.")
		formData.InvalidField = formValidationField(errorResponse, "name", "slug", "type")
		r.logRenderedBackendAPIFailure(c, "start "+providerLabel+" data source authorization", errorResponse, startErr, orgSlug.String(), "")
		return r.renderDataSourcesPageWithError(
			c,
			orgSlug,
			status,
			"Something went wrong while connecting "+providerLabel+".",
			backendErrorDetails("start "+providerLabel+" data source authorization", status, errorResponse, startErr),
			formData,
		)
	}

	return redirectHTMXOrSeeOther(c, install.Install.AuthorizationUrl)
}

// handleStartExistingHubSpotOAuthSubmit restarts or rotates OAuth for a stored HubSpot source.
func (r *Router) handleStartExistingHubSpotOAuthSubmit(c fiber.Ctx) error {
	return r.handleStartExistingProviderOAuthSubmit(c, "hubspot")
}

func (r *Router) handleStartExistingBigQueryOAuthSubmit(c fiber.Ctx) error {
	return r.handleStartExistingProviderOAuthSubmit(c, "bigquery")
}

func (r *Router) handleStartExistingProviderOAuthSubmit(c fiber.Ctx, provider string) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	dataSourceSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}

	dataSource, status, errorResponse, getErr := r.getDataSourceViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := formFeedbackFromErrorResponse(errorResponse, "Failed to load the provider data source.")
		r.logRenderedBackendAPIFailure(c, "get data source", errorResponse, getErr, orgSlug.String(), dataSourceSlug.String())
		return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(
			dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
			"error",
			message,
		))
	}
	if string(dataSource.Type) != provider {
		return c.Status(fiber.StatusBadRequest).SendString("data source does not match the provider")
	}

	payload := api.StartProviderConnectorInstallJSONRequestBody{
		DataSourceSlug: dataSourceSlug.String(),
		Intent:         views.DataSourceProviderOAuthIntent(*dataSource),
	}
	if provider == "google_workspace" {
		_, err := dataSource.Config.AsGoogleWorkspaceConfig()
		if err != nil {
			return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(
				dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
				"error",
				"Failed to load the Workspace configuration.",
			))
		}
		mode := api.GoogleWorkspaceAccessModeSelectedFiles
		if requested := c.FormValue("workspace_access_mode"); requested != "" && requested != string(mode) {
			return c.Status(fiber.StatusBadRequest).SendString("Google Workspace requires selected-file access")
		}
		payload.AccessMode = &mode
	}
	install, status, errorResponse, startErr := r.startExistingProviderOAuthViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		provider,
		payload,
	)
	if startErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		message := formFeedbackFromErrorResponse(errorResponse, "Failed to start provider authorization.")
		r.logRenderedBackendAPIFailure(c, "start existing provider authorization", errorResponse, startErr, orgSlug.String(), dataSourceSlug.String())
		if dataSourceShouldRenderSidePanel(c) {
			return r.renderDataSourceSidePanel(
				c,
				orgSlug,
				dataSourceSlug.String(),
				dataSourceActiveEnvironmentSlug(c),
				message,
				"error",
				status,
			)
		}
		return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(
			dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
			"error",
			message,
		))
	}

	return redirectHTMXOrSeeOther(c, install.AuthorizationUrl)
}

func (r *Router) handleTestDataSourceConnectionSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	formData := r.dataSourceCreateFormDataFromRequest(c)

	createPayload, validationErr := buildCreateDataSourcePayloadWithPrivateHosts(c, r.allowPrivateDataSourceHosts)
	if validationErr != "" {
		formData.TestMessage = validationErr
		formData.TestSuccess = false
		return r.renderDataSourcesPageWithError(c, orgSlug, http.StatusBadRequest, "", "", formData)
	}

	testPayload, err := toTestDataSourceConnectionPayload(createPayload)
	if err != nil {
		formData.TestMessage = "Validation: failed to prepare test connection request."
		formData.TestSuccess = false
		return r.renderDataSourcesPageWithError(c, orgSlug, http.StatusBadRequest, "", "", formData)
	}

	status, errorResponse, testErr := r.testDataSourceConnectionViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		testPayload,
	)
	if testErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := formFeedbackFromErrorResponse(errorResponse, "Connection test failed.")

		formData.TestMessage = message
		formData.TestSuccess = false
		r.logRenderedBackendAPIFailure(c, "test data source connection", errorResponse, testErr, orgSlug.String(), "")
		return r.renderDataSourcesPageWithError(
			c,
			orgSlug,
			status,
			message,
			backendErrorDetails("test data source connection", status, errorResponse, testErr),
			formData,
		)
	}

	formData.TestMessage = "Connection successful."
	formData.TestSuccess = true
	return r.renderDataSourcesPageWithError(c, orgSlug, http.StatusOK, "", "", formData)
}

func (r *Router) handleTestExistingDataSourceConnectionSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	dataSourceSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}

	status, errorResponse, testErr := r.testExistingDataSourceConnectionViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
	)
	if testErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := formFeedbackFromErrorResponse(errorResponse, "Connection test failed.")
		r.logRenderedBackendAPIFailure(c, "test existing data source connection", errorResponse, testErr, orgSlug.String(), dataSourceSlug.String())
		if dataSourceShouldRenderSidePanel(c) {
			return r.renderDataSourceSidePanel(
				c,
				orgSlug,
				dataSourceSlug.String(),
				dataSourceActiveEnvironmentSlug(c),
				message,
				"error",
				status,
			)
		}

		return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(
			dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
			"error",
			message,
		))
	}

	if dataSourceShouldRenderSidePanel(c) {
		return r.renderDataSourceSidePanel(
			c,
			orgSlug,
			dataSourceSlug.String(),
			dataSourceActiveEnvironmentSlug(c),
			"Connection test passed.",
			"success",
			http.StatusOK,
		)
	}

	return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(
		dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
		"notice",
		"Connection test passed.",
	))
}

func (r *Router) handleDeleteDataSourceSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	dataSourceSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}

	status, errorResponse, deleteErr := r.deleteDataSourceViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
	)
	if deleteErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		r.logRenderedBackendAPIFailure(c, "delete data source", errorResponse, deleteErr, orgSlug.String(), dataSourceSlug.String())

		return r.renderDataSourcesPageWithError(
			c,
			orgSlug,
			status,
			"Something went wrong while deleting a data source.",
			backendErrorDetails("delete data source", status, errorResponse, deleteErr),
			r.defaultDataSourceCreateFormData(),
		)
	}

	return redirectHTMXOrSeeOther(c, dataSourceListURL(
		orgSlug.String(),
		dataSourceActiveEnvironmentSlug(c),
		"",
	))
}

func (r *Router) handleDataSourceDetailPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	dataSourceSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}

	dataSource, status, errorResponse, getErr := r.getDataSourceViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		pageError := messageFromErrorResponse(errorResponse)
		if pageError == "" {
			pageError = "Something went wrong while loading data source details."
		}
		r.logRenderedBackendAPIFailure(c, "get data source", errorResponse, getErr, orgSlug.String(), dataSourceSlug.String())

		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Data source details",
			orgSlug.String(),
			pageError,
			backendErrorDetails("get data source", status, errorResponse, getErr),
			c.Get(fiber.HeaderCookie),
		)
		shellData.Breadcrumbs = buildOrganizationDetailBreadcrumbs(
			shellData.ActiveOrganizationName,
			orgSlug.String(),
			"Data sources",
			"/orgs/"+orgSlug.String()+"/data-sources",
			dataSourceSlug.String(),
		)
		c.Status(status)
		return renderTempl(
			c,
			views.DataSourceDetailPage(
				shellData,
				orgSlug.String(),
				nil,
				views.DefaultDataSourceSchemaPanelData(),
				nil,
				views.DataSourceUpdateFormData{},
				views.DefaultOrganizationPermissions(),
				views.DataSourceOperationsPanelData{},
				"",
				"",
			),
		)
	}

	pageError := strings.TrimSpace(c.Query("error"))
	pageFeedback := strings.TrimSpace(c.Query("notice"))
	pageFeedbackKind := "success"
	schemaRefreshError := strings.TrimSpace(c.Query("schema_error"))
	pageErrorDetails := ""
	schemaPanel := dataSourceSchemaPanelData(dataSource, schemaRefreshError)
	if schemaRefreshError != "" && pageError == "" {
		pageError = schemaRefreshError
	}

	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Data source details",
		orgSlug.String(),
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Inspect connection metadata and browse introspected schema."
	shellData.Breadcrumbs = buildOrganizationDetailBreadcrumbs(
		shellData.ActiveOrganizationName,
		orgSlug.String(),
		"Data sources",
		"/orgs/"+orgSlug.String()+"/data-sources",
		dataSource.Name,
	)

	connectionInfo := extractConnectionInfo(dataSource)
	updateFormData := r.dataSourceUpdateFormDataFromDataSource(dataSource)
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	operationsPanel := views.DataSourceOperationsPanelData{}
	if c.Query("section") == "operations" && permissions.Can("queries", "read") {
		limit := api.LimitParam(20)
		queries, queryStatus, queryErrorResponse, queryErr := r.listQueriesViaAPI(
			c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), dataSourceSlug.String(),
			&api.ListQueriesParams{Limit: &limit},
		)
		if queryErr != nil {
			if queryStatus == fiber.StatusUnauthorized {
				return redirectToLogin(c, c.OriginalURL())
			}
			operationsPanel.Error = messageFromErrorResponse(queryErrorResponse)
			if operationsPanel.Error == "" {
				operationsPanel.Error = "Saved queries could not be loaded. Try opening the query library again."
			}
			r.logRenderedBackendAPIFailure(c, "list queries", queryErrorResponse, queryErr, orgSlug.String(), dataSourceSlug.String())
		} else {
			operationsPanel.Queries = queries.Queries
			operationsPanel.HasMore = queries.HasMore
		}
	}
	if c.Query("section") == "settings" && permissions.Can("datasources", "update") {
		r.loadPendingProviderRepositories(c, orgSlug.String(), dataSource.Slug, &updateFormData)
	}
	return renderTempl(
		c,
		views.DataSourceDetailPage(
			shellData,
			orgSlug.String(),
			dataSource,
			schemaPanel,
			connectionInfo,
			updateFormData,
			permissions,
			operationsPanel,
			pageFeedback,
			pageFeedbackKind,
		),
	)
}

func (r *Router) loadPendingProviderRepositories(
	c fiber.Ctx,
	organizationSlug, dataSourceSlug string,
	form *views.DataSourceUpdateFormData,
) {
	if form == nil || !form.ProviderSelectionPending {
		return
	}
	repositories, _, errorResponse, err := r.listProviderRepositoriesViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		organizationSlug,
		dataSourceSlug,
	)
	if err != nil {
		form.ProviderRepositoryLoadError = "Repositories could not be loaded. Retry this page or reconnect the provider."
		r.logRenderedBackendAPIFailure(c, "list provider repositories", errorResponse, err, organizationSlug, dataSourceSlug)
		return
	}
	form.ProviderRepositories = repositories.Repositories
}

func (r *Router) handleRefreshDataSourceSchemaSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	dataSourceSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}

	status, errorResponse, refreshErr := r.refreshDataSourceSchemaViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
	)
	if refreshErr != nil {
		r.logRenderedBackendAPIFailure(c, "refresh data source schema", errorResponse, refreshErr, orgSlug.String(), dataSourceSlug.String())
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := schemaRefreshDiagnosticMessage(errorResponse)
		if message == "" {
			message = "Schema refresh failed."
		}
		if dataSourceShouldRenderSidePanel(c) {
			return r.renderDataSourceSidePanelWithSchemaRefreshError(
				c,
				orgSlug,
				dataSourceSlug.String(),
				dataSourceActiveEnvironmentSlug(c),
				message,
				"error",
				message,
			)
		}
		return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(
			dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
			"schema_error",
			message,
		))
	}

	if dataSourceShouldRenderSidePanel(c) {
		return r.renderDataSourceSidePanel(
			c,
			orgSlug,
			dataSourceSlug.String(),
			dataSourceActiveEnvironmentSlug(c),
			"Schema import queued.",
			"success",
			http.StatusOK,
		)
	}

	return redirectHTMXOrSeeOther(
		c,
		dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
	)
}

func (r *Router) handleUpdateDataSourceSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	dataSourceSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}

	payload, validationErr := buildUpdateDataSourcePayloadWithPrivateHosts(c, r.allowPrivateDataSourceHosts)
	if validationErr != "" {
		if dataSourceShouldRenderSidePanel(c) {
			return r.renderDataSourceSidePanel(
				c,
				orgSlug,
				dataSourceSlug.String(),
				dataSourceActiveEnvironmentSlug(c),
				validationErr,
				"error",
				http.StatusBadRequest,
			)
		}
		redirectURL := appendDataSourceReturnQuery(
			dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
			"error",
			validationErr,
		)
		return redirectHTMXOrSeeOther(c, redirectURL)
	}

	_, status, errorResponse, updateErr := r.updateDataSourceViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
		payload,
	)
	if updateErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Failed to update data source settings."
		}
		r.logRenderedBackendAPIFailure(c, "update data source", errorResponse, updateErr, orgSlug.String(), dataSourceSlug.String())
		if dataSourceShouldRenderSidePanel(c) {
			return r.renderDataSourceSidePanel(
				c,
				orgSlug,
				dataSourceSlug.String(),
				dataSourceActiveEnvironmentSlug(c),
				message,
				"error",
				status,
			)
		}
		redirectURL := appendDataSourceReturnQuery(
			dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
			"error",
			message,
		)
		return redirectHTMXOrSeeOther(c, redirectURL)
	}

	if dataSourceShouldRenderSidePanel(c) {
		return r.renderDataSourceSidePanel(
			c,
			orgSlug,
			dataSourceSlug.String(),
			dataSourceActiveEnvironmentSlug(c),
			"Connection settings saved.",
			"success",
			http.StatusOK,
		)
	}

	return redirectHTMXOrSeeOther(
		c,
		dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
	)
}

func (r *Router) handleUpdateDataSourceCredentialsSubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	dataSourceSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}

	dataSource, status, errorResponse, getErr := r.getDataSourceViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Failed to load data source."
		}
		r.logRenderedBackendAPIFailure(c, "get data source", errorResponse, getErr, orgSlug.String(), dataSourceSlug.String())
		if dataSourceShouldRenderSidePanel(c) {
			return r.renderDataSourceSidePanel(
				c,
				orgSlug,
				dataSourceSlug.String(),
				dataSourceActiveEnvironmentSlug(c),
				message,
				"error",
				status,
			)
		}
		redirectURL := appendDataSourceReturnQuery(
			dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
			"error",
			message,
		)
		return redirectHTMXOrSeeOther(c, redirectURL)
	}

	payload, validationErr := buildUpdateDataSourceCredentialsPayloadForDataSource(c, dataSource)
	if validationErr != "" {
		if dataSourceShouldRenderSidePanel(c) {
			return r.renderDataSourceSidePanel(
				c,
				orgSlug,
				dataSourceSlug.String(),
				dataSourceActiveEnvironmentSlug(c),
				validationErr,
				"error",
				http.StatusBadRequest,
			)
		}
		redirectURL := appendDataSourceReturnQuery(
			dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
			"error",
			validationErr,
		)
		return redirectHTMXOrSeeOther(c, redirectURL)
	}

	_, status, errorResponse, updateErr := r.updateDataSourceCredentialsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
		payload,
	)
	if updateErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Failed to update credentials."
		}
		r.logRenderedBackendAPIFailure(c, "update data source credentials", errorResponse, updateErr, orgSlug.String(), dataSourceSlug.String())
		if dataSourceShouldRenderSidePanel(c) {
			return r.renderDataSourceSidePanel(
				c,
				orgSlug,
				dataSourceSlug.String(),
				dataSourceActiveEnvironmentSlug(c),
				message,
				"error",
				status,
			)
		}
		redirectURL := appendDataSourceReturnQuery(
			dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
			"error",
			message,
		)
		return redirectHTMXOrSeeOther(c, redirectURL)
	}

	if dataSourceShouldRenderSidePanel(c) {
		return r.renderDataSourceSidePanel(
			c,
			orgSlug,
			dataSourceSlug.String(),
			dataSourceActiveEnvironmentSlug(c),
			"Credentials updated.",
			"success",
			http.StatusOK,
		)
	}

	return redirectHTMXOrSeeOther(
		c,
		dataSourceReturnURL(c, orgSlug.String(), dataSourceSlug.String()),
	)
}

func (r *Router) listDataSourcesViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	params *api.ListDataSourcesParams,
) (*api.DataSourceListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListDataSourcesWithResponse(
		ctx,
		organizationSlug,
		params,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, unexpectedBackendAPIResponse("list data sources", resp.StatusCode())
}

// listAllDataSourcesViaAPI walks the paginated data-source API for SSR binding selectors.
func (r *Router) listAllDataSourcesViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
) ([]api.DataSource, int, *api.ErrorResponse, error) {
	limit := api.LimitParam(100)
	direction := api.ListDataSourcesParamsDirectionNext
	params := &api.ListDataSourcesParams{
		Limit: &limit,
	}
	dataSources := []api.DataSource{}
	seenCursors := map[string]struct{}{}

	for {
		dataSourceList, status, errorResponse, err := r.listDataSourcesViaAPI(
			ctx,
			cookieHeader,
			organizationSlug,
			params,
		)
		if err != nil {
			return nil, status, errorResponse, err
		}

		dataSources = append(dataSources, dataSourceList.DataSources...)
		if !dataSourceList.HasMore || dataSourceList.NextCursor == nil {
			return dataSources, http.StatusOK, nil, nil
		}
		nextCursor := strings.TrimSpace(*dataSourceList.NextCursor)
		if nextCursor == "" {
			return dataSources, http.StatusOK, nil, nil
		}
		if _, exists := seenCursors[nextCursor]; exists {
			return nil, http.StatusInternalServerError, nil, xerror.Unexpected().
				Detailf("list data sources pagination returned repeated cursor %q", nextCursor)
		}
		seenCursors[nextCursor] = struct{}{}

		params = &api.ListDataSourcesParams{
			Limit:     &limit,
			Cursor:    &nextCursor,
			Direction: &direction,
		}
	}
}

// filterDataSourcesForList applies dashboard-linked inventory filters to loaded sources.
func (r *Router) filterDataSourcesForList(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSources []api.DataSource,
	filter string,
) ([]api.DataSource, int, *api.ErrorResponse, error) {
	filter = views.NormalizeDataSourceListFilter(filter)
	if filter == "" {
		return dataSources, http.StatusOK, nil, nil
	}
	if !views.DataSourceListFilterNeedsQueryInventory(filter) {
		filtered := make([]api.DataSource, 0, len(dataSources))
		for _, dataSource := range dataSources {
			if views.DataSourceMatchesListFilter(dataSource, filter) {
				filtered = append(filtered, dataSource)
			}
		}
		return filtered, http.StatusOK, nil, nil
	}

	queryCounts, status, errorResponse, err := r.loadDataSourceQueryCounts(
		ctx,
		cookieHeader,
		organizationSlug,
		dataSources,
	)
	if err != nil {
		return nil, status, errorResponse, err
	}

	filtered := make([]api.DataSource, 0, len(dataSources))
	for _, dataSource := range dataSources {
		hasQueries := queryCounts[dataSource.Slug] > 0
		if filter == views.DataSourceListFilterWithQueries && hasQueries {
			filtered = append(filtered, dataSource)
		}
		if filter == views.DataSourceListFilterWithoutQueries && !hasQueries {
			filtered = append(filtered, dataSource)
		}
	}
	return filtered, http.StatusOK, nil, nil
}

func (r *Router) createDataSourceViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	payload api.CreateDataSourceJSONRequestBody,
) (*api.DataSource, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.CreateDataSourceWithResponse(
		ctx,
		organizationSlug,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusCreated && resp.JSON201 != nil {
		return resp.JSON201, http.StatusCreated, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, unexpectedBackendAPIResponse("create data source", resp.StatusCode())
}

// startProviderDataSourceOAuthViaAPI starts OAuth-first onboarding through the authenticated backend API.
func (r *Router) startProviderDataSourceOAuthViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	provider string,
	payload api.StartProviderDataSourceInstallJSONRequestBody,
) (*api.ProviderDataSourceInstall, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.StartProviderDataSourceInstallWithResponse(
		ctx,
		organizationSlug,
		provider,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusCreated && resp.JSON201 != nil {
		return resp.JSON201, http.StatusCreated, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, unexpectedBackendAPIResponse("start "+provider+" data source authorization", resp.StatusCode())
}

func (r *Router) listProviderRepositoriesViaAPI(
	ctx context.Context,
	cookieHeader, organizationSlug, dataSourceSlug string,
) (*api.ProviderRepositoryListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListDataSourceProviderRepositoriesWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}
	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}
	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, unexpectedBackendAPIResponse("list provider repositories", resp.StatusCode())
}

func (r *Router) selectProviderRepositoryViaAPI(
	ctx context.Context,
	cookieHeader, organizationSlug, dataSourceSlug, repositoryID string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.SelectDataSourceProviderRepositoryWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
		api.SelectDataSourceProviderRepositoryJSONRequestBody{RepositoryId: repositoryID},
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}
	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return http.StatusOK, nil, nil
	}
	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, unexpectedBackendAPIResponse("select provider repository", resp.StatusCode())
}

// startExistingProviderOAuthViaAPI starts an OAuth install for a stored HubSpot source.
func (r *Router) startExistingProviderOAuthViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	provider string,
	payload api.StartProviderConnectorInstallJSONRequestBody,
) (*api.ProviderConnectorInstall, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.StartProviderConnectorInstallWithResponse(
		ctx,
		organizationSlug,
		provider,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusCreated && resp.JSON201 != nil {
		return resp.JSON201, http.StatusCreated, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, unexpectedBackendAPIResponse("start existing provider authorization", resp.StatusCode())
}

func (r *Router) deleteDataSourceViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.DeleteDataSourceWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, unexpectedBackendAPIResponse("delete data source", resp.StatusCode())
}

func (r *Router) testDataSourceConnectionViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	payload api.TestDataSourceConnectionJSONRequestBody,
) (int, *api.ErrorResponse, error) {
	requestCtx, cancel := frontendAPIExecutionContext(ctx, 0)
	defer cancel()
	resp, err := r.apiClient.TestDataSourceConnectionWithResponse(
		requestCtx,
		organizationSlug,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, unexpectedBackendAPIResponse("test data source connection", resp.StatusCode())
}

func (r *Router) testExistingDataSourceConnectionViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
) (int, *api.ErrorResponse, error) {
	requestCtx, cancel := frontendAPIExecutionContext(ctx, 0)
	defer cancel()
	resp, err := r.apiClient.TestStoredDataSourceConnectionWithResponse(
		requestCtx,
		organizationSlug,
		dataSourceSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, unexpectedBackendAPIResponse("test existing data source connection", resp.StatusCode())
}

func (r *Router) getDataSourceViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
) (*api.DataSource, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetDataSourceWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, unexpectedBackendAPIResponse("get data source", resp.StatusCode())
}

// listDataSourceBranchesViaAPI calls the backend branch-list endpoint for a Git data source.
func (r *Router) listDataSourceBranchesViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
) (*api.DataSourceBranchListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListDataSourceBranchesWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"list data source branches api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) updateDataSourceViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
	payload api.UpdateDataSourceJSONRequestBody,
) (*api.DataSource, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpdateDataSourceWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, unexpectedBackendAPIResponse("update data source", resp.StatusCode())
}

func (r *Router) updateDataSourceCredentialsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
	payload api.UpdateDataSourceCredentialsJSONRequestBody,
) (*api.DataSource, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpdateDataSourceCredentialsWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
		payload,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, unexpectedBackendAPIResponse("update data source credentials", resp.StatusCode())
}

func (r *Router) refreshDataSourceSchemaViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.RefreshDataSourceSchemaWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusAccepted {
		return http.StatusAccepted, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, unexpectedBackendAPIResponse("refresh data source schema", resp.StatusCode())
}

func formatSchemaForDisplay(schema *api.IntrospectSchemaResponse, dataSource *api.DataSource) (string, []string, string) {
	if schema == nil {
		return "", nil, ""
	}

	schemaType, err := schema.Discriminator()
	if err != nil {
		return "", nil, "Failed to parse schema response."
	}

	switch schemaType {
	case "sql":
		sqlSchema, err := schema.AsSQLSchemaResponse()
		if err != nil {
			return "", nil, "Failed to parse SQL schema response."
		}

		lines := make([]string, 0, len(sqlSchema.Tables)*2)
		for _, table := range sqlSchema.Tables {
			lines = append(lines, "table "+table.Name)
			if len(table.Columns) == 0 {
				lines = append(lines, "  (no columns)")
				continue
			}
			for _, col := range table.Columns {
				lines = append(lines, "  - "+col.Name+" ("+col.Type+")")
			}
		}
		if len(lines) == 0 {
			lines = append(lines, "No tables discovered.")
		}

		return "Schema type: SQL (" + string(sqlSchema.Type) + ")", lines, ""

	case "http":
		httpSchema, err := schema.AsHTTPSchemaResponse()
		if err != nil {
			return "", nil, "Failed to parse HTTP schema response."
		}

		lines := make([]string, 0, len(httpSchema.Operations)*4)
		for _, op := range httpSchema.Operations {
			lines = append(lines, strings.ToUpper(string(op.Method))+" "+op.Path+" ["+op.Id+"]")
			if op.Description != nil && strings.TrimSpace(*op.Description) != "" {
				lines = append(lines, "  description: "+strings.TrimSpace(*op.Description))
			}
			if op.RequestBody != nil {
				lines = append(lines, "  request body: "+compactJSONString(op.RequestBody))
			}
			if op.Responses != nil {
				lines = append(lines, "  responses: "+compactJSONString(op.Responses))
			}
		}
		if len(lines) == 0 {
			if httpDataSourceHasOpenAPIURL(dataSource) {
				lines = append(lines, "No operations were discovered in the imported schema.")
			} else {
				lines = append(lines, "No OpenAPI spec URL configured.")
			}
		}

		if httpDataSourceHasOpenAPIURL(dataSource) {
			return "Schema type: HTTP - OpenAPI import completed", lines, ""
		}
		return "Schema type: HTTP", lines, ""

	case "document":
		documentSchema, err := schema.AsDocumentSchemaResponse()
		if err != nil {
			return "", nil, "Failed to parse document schema response."
		}

		lines := make([]string, 0, len(documentSchema.Collections)*2)
		for _, collection := range documentSchema.Collections {
			lines = append(lines, "collection "+collection.Name)
			if len(collection.Fields) == 0 {
				lines = append(lines, "  (no fields)")
				continue
			}
			for _, field := range collection.Fields {
				lines = append(lines, "  - "+field.Name+" ("+field.Type+")")
			}
		}
		if len(lines) == 0 {
			lines = append(lines, "No collections discovered.")
		}

		return "Schema type: Document (" + string(documentSchema.Type) + ")", lines, ""
	case "object_api":
		objectAPISchema, err := schema.AsObjectAPISchemaResponse()
		if err != nil {
			return "", nil, "Failed to parse object API schema response."
		}

		lines := make([]string, 0, len(objectAPISchema.Objects)*2)
		for _, object := range objectAPISchema.Objects {
			lines = append(lines, "object "+object.Name)
			if len(object.Fields) == 0 {
				lines = append(lines, "  (no fields)")
				continue
			}
			for _, field := range object.Fields {
				label := field.Name
				if field.Label != nil && strings.TrimSpace(*field.Label) != "" {
					label = *field.Label
				}
				lines = append(lines, "  - "+label+" ("+field.Type+")")
			}
		}
		if len(lines) == 0 {
			lines = append(lines, "No objects discovered.")
		}

		return "Schema type: Object API (" + objectAPISchema.Type + ")", lines, ""
	case "file":
		fileSchema, err := schema.AsFileSchemaResponse()
		if err != nil {
			return "", nil, "Failed to parse file schema response."
		}

		warnings := make(map[string]string)
		if fileSchema.Diagnostics != nil {
			for _, diagnostic := range *fileSchema.Diagnostics {
				warnings[diagnostic.Name] = diagnostic.Message
			}
		}
		lines := make([]string, 0, len(fileSchema.Files)*2)
		for _, file := range fileSchema.Files {
			lines = append(lines, "file "+file.Name)
			if file.ResourceId != nil {
				lines = append(lines, "  ID: "+*file.ResourceId)
			}
			if file.MimeType != nil {
				lines = append(lines, "  Type: "+*file.MimeType)
			}
			if len(file.Fields) == 0 && dataSource != nil && dataSource.Type == api.DataSourceTypeGoogleWorkspace {
				continue
			}
			if len(file.Fields) == 0 {
				if warning := warnings[file.Name]; warning != "" {
					lines = append(lines, "  Fields not inferred · "+warning)
				} else {
					lines = append(lines, "  Raw file · format: file")
				}
				continue
			}
			lines = append(lines, "  Structured file · inferred fields")
			for _, field := range file.Fields {
				lines = append(lines, "  - "+field.Name+" ("+field.Type+")")
			}
		}
		if len(lines) == 0 {
			lines = append(lines, "No files discovered.")
		}

		return "Schema type: File (" + string(fileSchema.Type) + ")", lines, ""
	default:
		return "", nil, "Unsupported schema type: " + schemaType
	}
}

// httpDataSourceHasOpenAPIURL reports whether the HTTP data source is configured for import.
func httpDataSourceHasOpenAPIURL(dataSource *api.DataSource) bool {
	if dataSource == nil || dataSource.Type != api.DataSourceTypeHttp {
		return false
	}

	config, err := dataSource.Config.AsHTTPConfig()
	if err != nil || config.OpenapiUrl == nil {
		return false
	}

	return strings.TrimSpace(*config.OpenapiUrl) != ""
}

// schemaRefreshDiagnosticMessage formats refresh errors that must survive redirects.
func schemaRefreshDiagnosticMessage(errorResponse *api.ErrorResponse) string {
	if errorResponse == nil {
		return ""
	}

	message := messageFromErrorResponse(errorResponse)
	if detailed := detailedErrorFromErrorResponse(errorResponse); detailed != "" {
		message = detailed
	}
	if message == "" {
		message = "Schema refresh failed."
	}

	code := strings.TrimSpace(errorResponse.Error.Code)
	prefix := "Schema refresh failed"
	switch code {
	case "INVALID_SCHEMA_URL":
		prefix = "OpenAPI URL rejected"
	case "INVALID_OPENAPI", "SCHEMA_FETCH_FAILED":
		prefix = "OpenAPI import failed"
	}
	if code == "" {
		return prefix + ": " + message
	}
	return prefix + ": " + message + " (" + code + ")"
}

func schemaImportDiagnosticMessage(diagnostic *api.DataSourceSchemaDiagnostic) string {
	if diagnostic == nil {
		return ""
	}
	message := strings.TrimSpace(diagnostic.Message)
	if message == "" {
		message = "Schema import failed."
	}
	code := strings.TrimSpace(diagnostic.Code)
	if code == "" {
		return message
	}
	return message + " (" + code + ")"
}

func compactJSONString(raw any) string {
	data, err := json.Marshal(raw)
	if err != nil {
		return fmt.Sprintf("%v", raw)
	}

	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return string(data)
	}

	compact, err := json.Marshal(decoded)
	if err != nil {
		return string(data)
	}
	return string(compact)
}

// buildCreateDataSourcePayloadWithPrivateHosts converts create form fields into an API payload.
func buildCreateDataSourcePayloadWithPrivateHosts(
	c fiber.Ctx,
	allowPrivateDataSourceHosts bool,
) (api.CreateDataSourceJSONRequestBody, string) {
	typeInput := strings.TrimSpace(c.FormValue("type"))
	if typeInput == "" {
		return api.CreateDataSourceJSONRequestBody{}, "Validation: type is required."
	}
	dsType := api.DataSourceType(typeInput)

	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return api.CreateDataSourceJSONRequestBody{}, validationNameRequiredMessage
	}

	var apiSlugValue *api.Slug
	slugInput := strings.TrimSpace(c.FormValue("slug"))
	if slugInput != "" {
		slugValue, err := slug.New(slugInput)
		if err != nil {
			return api.CreateDataSourceJSONRequestBody{}, "Validation: slug is invalid."
		}
		value := api.Slug(slugValue)
		apiSlugValue = &value
	}

	switch dsType {
	case api.DataSourceTypePostgres:
		config, credentials, err := parseSQLFormWithPrivateHosts(c, 5432, allowPrivateDataSourceHosts)
		if err != "" {
			return api.CreateDataSourceJSONRequestBody{}, err
		}
		var payload api.CreateDataSourceJSONRequestBody
		setErr := payload.FromCreatePostgresDataSourceRequest(api.CreatePostgresDataSourceRequest{
			Name:        name,
			Slug:        apiSlugValue,
			Config:      config,
			Credentials: credentials,
		})
		if setErr != nil {
			return api.CreateDataSourceJSONRequestBody{}, "Validation: invalid postgres data source payload."
		}
		return payload, ""
	case api.DataSourceTypeMysql:
		config, credentials, err := parseMySQLFormWithPrivateHosts(c, 3306, allowPrivateDataSourceHosts)
		if err != "" {
			return api.CreateDataSourceJSONRequestBody{}, err
		}
		var payload api.CreateDataSourceJSONRequestBody
		setErr := payload.FromCreateMySQLDataSourceRequest(api.CreateMySQLDataSourceRequest{
			Name:        name,
			Slug:        apiSlugValue,
			Config:      config,
			Credentials: credentials,
		})
		if setErr != nil {
			return api.CreateDataSourceJSONRequestBody{}, "Validation: invalid mysql data source payload."
		}
		return payload, ""
	case api.DataSourceTypeS3:
		config, errMsg := parseS3ConfigFormWithPrivateHosts(c, allowPrivateDataSourceHosts)
		if errMsg != "" {
			return api.CreateDataSourceJSONRequestBody{}, errMsg
		}
		credentials, errMsg := parseS3CredentialsForm(c)
		if errMsg != "" {
			return api.CreateDataSourceJSONRequestBody{}, errMsg
		}
		var payload api.CreateDataSourceJSONRequestBody
		if err := payload.FromCreateS3DataSourceRequest(api.CreateS3DataSourceRequest{Name: name, Slug: apiSlugValue, Config: config, Credentials: credentials}); err != nil {
			return api.CreateDataSourceJSONRequestBody{}, "Validation: invalid S3 data source payload."
		}
		return payload, ""
	case api.DataSourceTypeBigquery:
		return api.CreateDataSourceJSONRequestBody{}, "BigQuery data sources must use Google OAuth."
	case api.DataSourceTypeSnowflake:
		config, credentials, errMsg := parseSnowflakeForm(c)
		if errMsg != "" {
			return api.CreateDataSourceJSONRequestBody{}, errMsg
		}
		var payload api.CreateDataSourceJSONRequestBody
		if err := payload.FromCreateSnowflakeDataSourceRequest(api.CreateSnowflakeDataSourceRequest{
			Name: name, Slug: apiSlugValue, Config: config, Credentials: credentials, ReadOnly: snowflakeReadOnly(config),
		}); err != nil {
			return api.CreateDataSourceJSONRequestBody{}, "Validation: invalid snowflake data source payload."
		}
		return payload, ""
	case api.DataSourceTypeMongodb:
		config, credentials, err := parseMongoFormWithPrivateHosts(c, 27017, allowPrivateDataSourceHosts)
		if err != "" {
			return api.CreateDataSourceJSONRequestBody{}, err
		}
		var payload api.CreateDataSourceJSONRequestBody
		setErr := payload.FromCreateMongoDBDataSourceRequest(api.CreateMongoDBDataSourceRequest{
			Name:        name,
			Slug:        apiSlugValue,
			Config:      config,
			Credentials: credentials,
		})
		if setErr != nil {
			return api.CreateDataSourceJSONRequestBody{}, "Validation: invalid mongodb data source payload."
		}
		return payload, ""
	case api.DataSourceTypeHttp:
		config, credentials, err := parseHTTPFormWithPrivateHosts(c, allowPrivateDataSourceHosts)
		if err != "" {
			return api.CreateDataSourceJSONRequestBody{}, err
		}
		var payload api.CreateDataSourceJSONRequestBody
		setErr := payload.FromCreateHTTPDataSourceRequest(api.CreateHTTPDataSourceRequest{
			Name:        name,
			Slug:        apiSlugValue,
			Config:      config,
			Credentials: credentials,
		})
		if setErr != nil {
			return api.CreateDataSourceJSONRequestBody{}, "Validation: invalid http data source payload."
		}
		return payload, ""
	case api.DataSourceTypeHubspot:
		config, credentials, err := parseHubSpotForm(c)
		if err != "" {
			return api.CreateDataSourceJSONRequestBody{}, err
		}
		readOnly := dataSourceCreateFormDataFromRequest(c).HubSpotReadOnly
		var payload api.CreateDataSourceJSONRequestBody
		setErr := payload.FromCreateHubSpotDataSourceRequest(api.CreateHubSpotDataSourceRequest{
			Name:        name,
			Slug:        apiSlugValue,
			ReadOnly:    &readOnly,
			Config:      config,
			Credentials: credentials,
		})
		if setErr != nil {
			return api.CreateDataSourceJSONRequestBody{}, "Validation: invalid hubspot data source payload."
		}
		return payload, ""
	case api.DataSourceTypeGit:
		config, credentials, err := parseGitFormWithPrivateHosts(c, allowPrivateDataSourceHosts)
		if err != "" {
			return api.CreateDataSourceJSONRequestBody{}, err
		}
		var payload api.CreateDataSourceJSONRequestBody
		setErr := payload.FromCreateGitDataSourceRequest(api.CreateGitDataSourceRequest{
			Name:        name,
			Slug:        apiSlugValue,
			Config:      config,
			Credentials: credentials,
		})
		if setErr != nil {
			return api.CreateDataSourceJSONRequestBody{}, "Validation: invalid git data source payload."
		}
		return payload, ""
	case api.DataSourceTypeGithub:
		repositoryID := strings.TrimSpace(c.FormValue("github_repository_id"))
		if repositoryID == "" {
			return api.CreateDataSourceJSONRequestBody{}, "Validation: select a GitHub repository."
		}
		config := api.GitHubRepositoryConfig{RepositoryId: repositoryID}
		if basePath := strings.TrimSpace(c.FormValue("git_base_path")); basePath != "" {
			config.BasePath = &basePath
		}
		readOnly := strings.TrimSpace(c.FormValue("github_access_mode")) != "write"
		var payload api.CreateDataSourceJSONRequestBody
		setErr := payload.FromCreateGitHubDataSourceRequest(api.CreateGitHubDataSourceRequest{
			Name: name, Slug: apiSlugValue, Config: config, ReadOnly: &readOnly,
		})
		if setErr != nil {
			return api.CreateDataSourceJSONRequestBody{}, "Validation: invalid github data source payload."
		}
		return payload, ""
	case api.DataSourceTypeGitlab:
		projectID := strings.TrimSpace(c.FormValue("gitlab_project_id"))
		if projectID == "" {
			return api.CreateDataSourceJSONRequestBody{}, "Validation: GitLab project ID is required."
		}
		config := api.GitLabRepositoryConfig{ProjectId: projectID}
		if basePath := strings.TrimSpace(c.FormValue("git_base_path")); basePath != "" {
			config.BasePath = &basePath
		}
		readOnly := dataSourceCreateFormDataFromRequest(c).GitLabReadOnly
		var payload api.CreateDataSourceJSONRequestBody
		setErr := payload.FromCreateGitLabDataSourceRequest(api.CreateGitLabDataSourceRequest{
			Name:     name,
			Slug:     apiSlugValue,
			Config:   config,
			ReadOnly: &readOnly,
		})
		if setErr != nil {
			return api.CreateDataSourceJSONRequestBody{}, "Validation: invalid gitlab data source payload."
		}
		return payload, ""
	default:
		return api.CreateDataSourceJSONRequestBody{}, validationUnsupportedTypeMessage
	}
}

func toTestDataSourceConnectionPayload(
	createPayload api.CreateDataSourceJSONRequestBody,
) (api.TestDataSourceConnectionJSONRequestBody, error) {
	value, err := createPayload.ValueByDiscriminator()
	if err != nil {
		return api.TestDataSourceConnectionJSONRequestBody{}, err
	}

	var payload api.TestDataSourceConnectionJSONRequestBody

	switch v := value.(type) {
	case api.CreateS3DataSourceRequest:
		err = payload.FromTestS3DataSourceConnectionRequest(api.TestS3DataSourceConnectionRequest{Config: v.Config, Credentials: v.Credentials})
	case api.CreatePostgresDataSourceRequest:
		err = payload.FromTestPostgresDataSourceConnectionRequest(api.TestPostgresDataSourceConnectionRequest{
			Config:      v.Config,
			Credentials: v.Credentials,
		})
	case api.CreateMySQLDataSourceRequest:
		err = payload.FromTestMySQLDataSourceConnectionRequest(api.TestMySQLDataSourceConnectionRequest{
			Config:      v.Config,
			Credentials: v.Credentials,
		})
	case api.CreateSnowflakeDataSourceRequest:
		err = payload.FromTestSnowflakeDataSourceConnectionRequest(api.TestSnowflakeDataSourceConnectionRequest{
			Config:      v.Config,
			Credentials: v.Credentials,
		})
	case api.CreateHTTPDataSourceRequest:
		err = payload.FromTestHTTPDataSourceConnectionRequest(api.TestHTTPDataSourceConnectionRequest{
			Config:      v.Config,
			Credentials: v.Credentials,
		})
	case api.CreateHubSpotDataSourceRequest:
		err = payload.FromTestHubSpotDataSourceConnectionRequest(api.TestHubSpotDataSourceConnectionRequest{
			Config:      v.Config,
			Credentials: v.Credentials,
		})
	case api.CreateMongoDBDataSourceRequest:
		err = payload.FromTestMongoDBDataSourceConnectionRequest(api.TestMongoDBDataSourceConnectionRequest{
			Config:      v.Config,
			Credentials: v.Credentials,
		})
	case api.CreateFTPDataSourceRequest:
		err = payload.FromTestFTPDataSourceConnectionRequest(api.TestFTPDataSourceConnectionRequest{
			Config:      v.Config,
			Credentials: v.Credentials,
		})
	case api.CreateGitDataSourceRequest:
		err = payload.FromTestGitDataSourceConnectionRequest(api.TestGitDataSourceConnectionRequest{
			Config:      v.Config,
			Credentials: v.Credentials,
		})
	case api.CreateGitHubDataSourceRequest:
		err = payload.FromTestGitHubDataSourceConnectionRequest(api.TestGitHubDataSourceConnectionRequest{Config: v.Config})
	case api.CreateGitLabDataSourceRequest:
		return api.TestDataSourceConnectionJSONRequestBody{}, xerror.InvalidInput(xerror.DataSource).
			Detail("GitLab project access tokens are validated when the data source is created")
	default:
		return api.TestDataSourceConnectionJSONRequestBody{}, xerror.Unexpected().
			Detailf("unsupported data source request variant %T", value)
	}
	if err != nil {
		return api.TestDataSourceConnectionJSONRequestBody{}, err
	}

	return payload, nil
}

// buildUpdateDataSourcePayloadWithPrivateHosts converts detail form fields into an API payload.
func buildUpdateDataSourcePayloadWithPrivateHosts(
	c fiber.Ctx,
	allowPrivateDataSourceHosts bool,
) (api.UpdateDataSourceJSONRequestBody, string) {
	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return api.UpdateDataSourceJSONRequestBody{}, validationNameRequiredMessage
	}

	typeInput := strings.TrimSpace(c.FormValue("type"))
	if typeInput == "" {
		return api.UpdateDataSourceJSONRequestBody{}, "Validation: data source type is missing."
	}

	payload := api.UpdateDataSourceJSONRequestBody{Name: &name}
	if parseCheckbox(c.FormValue("managed_provider")) {
		return payload, ""
	}

	switch api.DataSourceType(typeInput) {
	case api.DataSourceTypeGoogleWorkspace:
		return payload, ""
	case api.DataSourceTypePostgres:
		cfg, errMsg := parseSQLConfigFormWithPrivateHosts(c, 5432, allowPrivateDataSourceHosts)
		if errMsg != "" {
			return api.UpdateDataSourceJSONRequestBody{}, errMsg
		}
		payload.Config = &api.DataSourceConfig{}
		if err := payload.Config.FromPostgresConfig(cfg); err != nil {
			return api.UpdateDataSourceJSONRequestBody{}, "Validation: invalid postgres configuration."
		}
	case api.DataSourceTypeMysql:
		cfg, errMsg := parseMySQLConfigFormWithPrivateHosts(c, 3306, allowPrivateDataSourceHosts)
		if errMsg != "" {
			return api.UpdateDataSourceJSONRequestBody{}, errMsg
		}
		payload.Config = &api.DataSourceConfig{}
		if err := payload.Config.FromMySQLConfig(cfg); err != nil {
			return api.UpdateDataSourceJSONRequestBody{}, "Validation: invalid mysql configuration."
		}
	case api.DataSourceTypeMongodb:
		cfg, errMsg := parseMongoConfigFormWithPrivateHosts(c, 27017, allowPrivateDataSourceHosts)
		if errMsg != "" {
			return api.UpdateDataSourceJSONRequestBody{}, errMsg
		}
		payload.Config = &api.DataSourceConfig{}
		if err := payload.Config.FromMongoDBConfig(cfg); err != nil {
			return api.UpdateDataSourceJSONRequestBody{}, "Validation: invalid mongodb configuration."
		}
	case api.DataSourceTypeBigquery:
		cfg, errMsg := parseBigQueryConfigForm(c)
		if errMsg != "" {
			return api.UpdateDataSourceJSONRequestBody{}, errMsg
		}
		payload.Config = &api.DataSourceConfig{}
		if err := payload.Config.FromBigQueryConfig(cfg); err != nil {
			return api.UpdateDataSourceJSONRequestBody{}, "Validation: invalid bigquery configuration."
		}
	case api.DataSourceTypeSnowflake:
		cfg, errMsg := parseSnowflakeConfigForm(c)
		if errMsg != "" {
			return api.UpdateDataSourceJSONRequestBody{}, errMsg
		}
		payload.ReadOnly = snowflakeReadOnly(cfg)
		payload.Config = &api.DataSourceConfig{}
		if err := payload.Config.FromSnowflakeConfig(cfg); err != nil {
			return api.UpdateDataSourceJSONRequestBody{}, "Validation: invalid snowflake configuration."
		}
	case api.DataSourceTypeHttp:
		cfg, errMsg := parseHTTPBaseConfig(c, allowPrivateDataSourceHosts)
		if errMsg != "" {
			return api.UpdateDataSourceJSONRequestBody{}, errMsg
		}
		payload.Config = &api.DataSourceConfig{}
		if err := payload.Config.FromHTTPConfig(cfg); err != nil {
			return api.UpdateDataSourceJSONRequestBody{}, "Validation: invalid http configuration."
		}
	case api.DataSourceTypeHubspot:
		cfg, errMsg := parseHubSpotConfigForm(c)
		if errMsg != "" {
			return api.UpdateDataSourceJSONRequestBody{}, errMsg
		}
		payload.Config = &api.DataSourceConfig{}
		if err := payload.Config.FromHubSpotConfig(cfg); err != nil {
			return api.UpdateDataSourceJSONRequestBody{}, "Validation: invalid hubspot configuration."
		}
		readOnly := strings.TrimSpace(c.FormValue(hubSpotAccessModeField)) != hubSpotAccessModeWrite
		payload.ReadOnly = &readOnly
	case api.DataSourceTypeS3:
		cfg, errMsg := parseS3ConfigFormWithPrivateHosts(c, allowPrivateDataSourceHosts)
		if errMsg != "" {
			return api.UpdateDataSourceJSONRequestBody{}, errMsg
		}
		payload.Config = &api.DataSourceConfig{}
		if err := payload.Config.FromS3Config(cfg); err != nil {
			return api.UpdateDataSourceJSONRequestBody{}, "Validation: invalid s3 configuration."
		}
	case api.DataSourceTypeGit, api.DataSourceTypeGithub, api.DataSourceTypeGitlab:
		cfg, errMsg := parseGitConfigFormWithPrivateHosts(c, allowPrivateDataSourceHosts)
		if errMsg != "" {
			return api.UpdateDataSourceJSONRequestBody{}, errMsg
		}
		payload.Config = &api.DataSourceConfig{}
		if err := payload.Config.FromGitConfig(cfg); err != nil {
			return api.UpdateDataSourceJSONRequestBody{}, "Validation: invalid git configuration."
		}
	default:
		return api.UpdateDataSourceJSONRequestBody{}, validationUnsupportedTypeMessage
	}

	return payload, ""
}

// buildUpdateDataSourceCredentialsPayloadForDataSource derives credential
// parsing context from the persisted data source selected by slug.
func buildUpdateDataSourceCredentialsPayloadForDataSource(
	c fiber.Ctx,
	ds *api.DataSource,
) (api.UpdateDataSourceCredentialsJSONRequestBody, string) {
	if ds == nil {
		return api.UpdateDataSourceCredentialsJSONRequestBody{}, "Validation: data source is missing."
	}

	storedGitRepoURL := ""
	if dataSourceTypeIsGitLike(ds.Type) {
		cfg, err := ds.Config.AsGitConfig()
		if err != nil {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, "Validation: stored git configuration is invalid."
		}
		storedGitRepoURL = cfg.RepoUrl
	}

	return buildUpdateDataSourceCredentialsPayloadForType(c, ds.Type, storedGitRepoURL)
}

// buildUpdateDataSourceCredentialsPayloadForType builds a credential rotation
// payload for a server-selected data source type.
func buildUpdateDataSourceCredentialsPayloadForType(
	c fiber.Ctx,
	dataSourceType api.DataSourceType,
	storedGitRepoURL string,
) (api.UpdateDataSourceCredentialsJSONRequestBody, string) {
	payload := api.UpdateDataSourceCredentialsJSONRequestBody{}

	switch dataSourceType {
	case api.DataSourceTypePostgres:
		creds, errMsg := parseSQLCredentialsForm(c)
		if errMsg != "" {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, errMsg
		}
		if err := payload.Credentials.FromPostgresCredentials(creds); err != nil {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, "Validation: invalid postgres credentials."
		}
	case api.DataSourceTypeMysql:
		creds, errMsg := parseMySQLCredentialsForm(c)
		if errMsg != "" {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, errMsg
		}
		if err := payload.Credentials.FromMySQLCredentials(creds); err != nil {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, "Validation: invalid mysql credentials."
		}
	case api.DataSourceTypeMongodb:
		creds, errMsg := parseMongoCredentialsForm(c)
		if errMsg != "" {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, errMsg
		}
		if err := payload.Credentials.FromMongoDBCredentials(creds); err != nil {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, "Validation: invalid mongodb credentials."
		}
	case api.DataSourceTypeBigquery:
		return api.UpdateDataSourceCredentialsJSONRequestBody{}, "BigQuery credentials are managed through Google OAuth."
	case api.DataSourceTypeSnowflake:
		creds, errMsg := parseSnowflakeCredentialsForm(c)
		if errMsg != "" {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, errMsg
		}
		if err := payload.Credentials.FromSnowflakeCredentials(creds); err != nil {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, "Validation: invalid snowflake credentials."
		}
	case api.DataSourceTypeHttp:
		creds, errMsg := parseHTTPCredentialsForm(c)
		if errMsg != "" {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, errMsg
		}
		if creds.SecretHeaders == nil && creds.Oauth == nil && creds.ApiKey == nil && creds.ApiKeyHeader == nil && creds.BearerToken == nil && creds.Username == nil && creds.Password == nil {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, "Validation: provide at least one HTTP credential value."
		}
		if err := payload.Credentials.FromHTTPCredentials(creds); err != nil {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, "Validation: invalid http credentials."
		}
	case api.DataSourceTypeHubspot:
		creds, errMsg := parseHubSpotCredentialsForm(c)
		if errMsg != "" {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, errMsg
		}
		if err := payload.Credentials.FromHubSpotCredentials(creds); err != nil {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, "Validation: invalid hubspot credentials."
		}
	case api.DataSourceTypeS3:
		creds, errMsg := parseS3CredentialsForm(c)
		if errMsg != "" {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, errMsg
		}
		if err := payload.Credentials.FromS3Credentials(creds); err != nil {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, "Validation: invalid s3 credentials."
		}
	case api.DataSourceTypeGit, api.DataSourceTypeGithub, api.DataSourceTypeGitlab:
		creds, errMsg := parseGitCredentialsForUpdateForm(c, storedGitRepoURL)
		if errMsg != "" {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, errMsg
		}
		if err := payload.Credentials.FromGitCredentials(creds); err != nil {
			return api.UpdateDataSourceCredentialsJSONRequestBody{}, "Validation: invalid git credentials."
		}
	default:
		return api.UpdateDataSourceCredentialsJSONRequestBody{}, validationUnsupportedTypeMessage
	}

	return payload, ""
}

// dataSourceTypeIsGitLike reports whether credentials depend on a Git repo URL.
func dataSourceTypeIsGitLike(dataSourceType api.DataSourceType) bool {
	switch dataSourceType {
	case api.DataSourceTypeGit, api.DataSourceTypeGithub, api.DataSourceTypeGitlab:
		return true
	default:
		return false
	}
}

// parseGitCredentialsForUpdateForm validates credential rotation against the
// persisted repository URL loaded by the server-side rotate handler.
func parseGitCredentialsForUpdateForm(c fiber.Ctx, repoURL string) (api.GitCredentials, string) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return api.GitCredentials{}, "Validation: stored git repository URL is required for credential rotation."
	}
	if err := validateGitRepoURLForForm(repoURL); err != "" {
		return api.GitCredentials{}, err
	}

	authType, err := parseGitAuthTypeForm(c)
	if err != "" {
		return api.GitCredentials{}, err
	}
	if err := validateGitAuthTypeForRepoURL(repoURL, authType); err != "" {
		return api.GitCredentials{}, err
	}

	return parseGitCredentialsFormForAuthType(c, authType)
}

// parseSQLConfigFormWithPrivateHosts converts PostgreSQL update fields into API config.
func parseSQLConfigFormWithPrivateHosts(
	c fiber.Ctx,
	defaultPort int,
	allowPrivateDataSourceHosts bool,
) (api.PostgresConfig, string) {
	host := strings.TrimSpace(c.FormValue("host"))
	database := strings.TrimSpace(c.FormValue("database"))
	if host == "" || database == "" {
		return api.PostgresConfig{}, "Validation: host and database are required for SQL data sources."
	}

	port, err := parseOptionalInt(c.FormValue("port"), defaultPort)
	if err != "" {
		return api.PostgresConfig{}, validationPortMustBeIntegerMessage
	}

	connectTimeout, timeoutErr := parseOptionalPositiveIntPointer(c.FormValue("connect_timeout"))
	if timeoutErr != "" {
		return api.PostgresConfig{}, validationConnectTimeoutPositiveMessage
	}
	var sslMode *api.PostgresConfigSslMode
	if value := strings.TrimSpace(c.FormValue("sslmode")); value != "" {
		mode := api.PostgresConfigSslMode(value)
		if !mode.Valid() {
			return api.PostgresConfig{}, "Validation: invalid PostgreSQL SSL mode."
		}
		sslMode = &mode
	}

	return api.PostgresConfig{
		Host:              host,
		Port:              port,
		Database:          database,
		SslMode:           sslMode,
		ConnectTimeout:    connectTimeout,
		AllowPrivateHosts: privateHostsFormPointer(c, databaseAllowPrivateHostsField, allowPrivateDataSourceHosts),
	}, ""
}

// parseMySQLConfigFormWithPrivateHosts converts MySQL update fields into API config.
func parseMySQLConfigFormWithPrivateHosts(
	c fiber.Ctx,
	defaultPort int,
	allowPrivateDataSourceHosts bool,
) (api.MySQLConfig, string) {
	host := strings.TrimSpace(c.FormValue("host"))
	database := strings.TrimSpace(c.FormValue("database"))
	if host == "" || database == "" {
		return api.MySQLConfig{}, "Validation: host and database are required for SQL data sources."
	}

	port, err := parseOptionalInt(c.FormValue("port"), defaultPort)
	if err != "" {
		return api.MySQLConfig{}, validationPortMustBeIntegerMessage
	}

	connectTimeout, timeoutErr := parseOptionalPositiveIntPointer(c.FormValue("connect_timeout"))
	if timeoutErr != "" {
		return api.MySQLConfig{}, validationConnectTimeoutPositiveMessage
	}

	return api.MySQLConfig{
		Host:              host,
		Port:              port,
		Database:          database,
		ConnectTimeout:    connectTimeout,
		AllowPrivateHosts: privateHostsFormPointer(c, databaseAllowPrivateHostsField, allowPrivateDataSourceHosts),
	}, ""
}

// parseMongoConfigFormWithPrivateHosts converts MongoDB update fields into API config.
func parseMongoConfigFormWithPrivateHosts(
	c fiber.Ctx,
	defaultPort int,
	allowPrivateDataSourceHosts bool,
) (api.MongoDBConfig, string) {
	host := strings.TrimSpace(c.FormValue("host"))
	database := strings.TrimSpace(c.FormValue("database"))
	authDatabase := strings.TrimSpace(c.FormValue("auth_database"))
	if host == "" || database == "" {
		return api.MongoDBConfig{}, "Validation: host and database are required for MongoDB data sources."
	}

	port, err := parseOptionalInt(c.FormValue("port"), defaultPort)
	if err != "" {
		return api.MongoDBConfig{}, validationPortMustBeIntegerMessage
	}

	connectTimeout, timeoutErr := parseOptionalPositiveIntPointer(c.FormValue("connect_timeout"))
	if timeoutErr != "" {
		return api.MongoDBConfig{}, validationConnectTimeoutPositiveMessage
	}

	var authDatabasePtr *string
	if authDatabase != "" {
		authDatabasePtr = &authDatabase
	}

	return api.MongoDBConfig{
		Host:              host,
		Port:              port,
		Database:          database,
		AuthDatabase:      authDatabasePtr,
		ConnectTimeout:    connectTimeout,
		AllowPrivateHosts: privateHostsFormPointer(c, databaseAllowPrivateHostsField, allowPrivateDataSourceHosts),
	}, ""
}

// parseHTTPBaseConfig validates shared HTTP config fields and applies the private-host policy.
func parseHTTPBaseConfig(
	c fiber.Ctx,
	allowPrivateDataSourceHosts bool,
) (api.HTTPConfig, string) {
	baseURL := strings.TrimSpace(c.FormValue("base_url"))
	if baseURL == "" {
		return api.HTTPConfig{}, "Validation: base URL is required for HTTP data sources."
	}

	healthPath := strings.TrimSpace(c.FormValue("health_path"))
	timeoutSeconds, timeoutErr := parseOptionalPositiveIntPointer(c.FormValue("timeout_seconds"))
	if timeoutErr != "" {
		return api.HTTPConfig{}, validationTimeoutSecondsPositiveMessage
	}
	openAPIURL := strings.TrimSpace(c.FormValue("openapi_url"))
	openAPIFetchTimeout, openAPIFetchTimeoutErr := parseOptionalPositiveIntPointer(
		c.FormValue("openapi_fetch_timeout_seconds"),
	)
	if openAPIFetchTimeoutErr != "" {
		return api.HTTPConfig{}, "Validation: OpenAPI fetch timeout must be a positive integer."
	}

	var healthPathPtr *string
	if healthPath != "" {
		healthPathPtr = &healthPath
	}
	var openAPIURLPtr *string
	if openAPIURL != "" {
		openAPIURLPtr = &openAPIURL
	}
	return api.HTTPConfig{
		BaseUrl:                    baseURL,
		HealthPath:                 healthPathPtr,
		TimeoutSeconds:             timeoutSeconds,
		OpenapiUrl:                 openAPIURLPtr,
		OpenapiFetchTimeoutSeconds: openAPIFetchTimeout,
		AllowPrivateHosts:          allowPrivateHostsFormPointer(c, allowPrivateDataSourceHosts),
	}, ""
}

func parseHubSpotConfigForm(c fiber.Ctx) (api.HubSpotConfig, string) {
	timeoutSeconds, timeoutErr := parseOptionalPositiveIntPointer(c.FormValue("timeout_seconds"))
	if timeoutErr != "" {
		return api.HubSpotConfig{}, validationTimeoutSecondsPositiveMessage
	}

	return api.HubSpotConfig{TimeoutSeconds: timeoutSeconds}, ""
}

// parseS3ConfigFormWithPrivateHosts converts S3 fields into public API config.
func parseS3ConfigFormWithPrivateHosts(
	c fiber.Ctx,
	allowPrivateDataSourceHosts bool,
) (api.S3Config, string) {
	region := strings.TrimSpace(c.FormValue("region"))
	bucket := strings.TrimSpace(c.FormValue("bucket"))
	if region == "" || bucket == "" {
		return api.S3Config{}, "Validation: region and bucket are required for S3 data sources."
	}

	timeoutSeconds, timeoutErr := parseOptionalPositiveIntPointer(c.FormValue("timeout_seconds"))
	if timeoutErr != "" {
		return api.S3Config{}, validationTimeoutSecondsPositiveMessage
	}

	endpoint := strings.TrimSpace(c.FormValue("endpoint"))
	prefix := strings.TrimSpace(c.FormValue("prefix"))
	forcePathStyle := parseCheckbox(c.FormValue("force_path_style"))
	config := api.S3Config{
		Region:         region,
		Bucket:         bucket,
		ForcePathStyle: &forcePathStyle,
		TimeoutSeconds: timeoutSeconds,
	}
	if allowPrivateDataSourceHosts {
		allowPrivateHosts := parseCheckbox(c.FormValue("allow_private_hosts"))
		config.AllowPrivateHosts = &allowPrivateHosts
	}
	if endpoint != "" {
		config.Endpoint = &endpoint
	}
	if prefix != "" {
		config.Prefix = &prefix
	}

	return config, ""
}

// parseBigQueryConfigForm converts BigQuery form fields into the public API
// config shape without accepting backend-only endpoint routing values.
func parseBigQueryConfigForm(c fiber.Ctx) (api.BigQueryConfig, string) {
	projectID := strings.TrimSpace(c.FormValue("project_id"))
	if projectID == "" {
		return api.BigQueryConfig{}, "Validation: project ID is required for BigQuery data sources."
	}

	timeoutSeconds, timeoutErr := parseOptionalPositiveIntPointer(c.FormValue("timeout_seconds"))
	if timeoutErr != "" {
		return api.BigQueryConfig{}, validationTimeoutSecondsPositiveMessage
	}
	maximumBytesBilled, maximumBytesBilledErr := parseOptionalPositiveInt64Pointer(
		c.FormValue("maximum_bytes_billed"),
	)
	if maximumBytesBilledErr != "" {
		return api.BigQueryConfig{}, "Validation: maximum bytes billed must be a positive integer."
	}

	location := strings.TrimSpace(c.FormValue("location"))
	defaultDataset := strings.TrimSpace(c.FormValue("default_dataset"))
	config := api.BigQueryConfig{
		ProjectId:          projectID,
		TimeoutSeconds:     timeoutSeconds,
		MaximumBytesBilled: maximumBytesBilled,
	}
	if location != "" {
		config.Location = &location
	}
	if defaultDataset != "" {
		config.DefaultDataset = &defaultDataset
	}
	return config, ""
}

func parseSnowflakeConfigForm(c fiber.Ctx) (api.SnowflakeConfig, string) {
	account := strings.TrimSpace(c.FormValue("account"))
	warehouse := strings.TrimSpace(c.FormValue("warehouse"))
	database := strings.TrimSpace(c.FormValue("database"))
	schema := strings.TrimSpace(c.FormValue("schema"))
	if account == "" || warehouse == "" || database == "" || schema == "" {
		return api.SnowflakeConfig{}, "Validation: account, warehouse, database, and schema are required for Snowflake data sources."
	}
	connectTimeout, errMsg := parseOptionalPositiveIntPointer(c.FormValue("connect_timeout"))
	if errMsg != "" {
		return api.SnowflakeConfig{}, validationTimeoutSecondsPositiveMessage
	}
	config := api.SnowflakeConfig{
		Account: account, Warehouse: warehouse, Database: database, Schema: schema,
		ConnectTimeout: connectTimeout,
	}
	if role := strings.TrimSpace(c.FormValue("role")); role != "" {
		config.Role = &role
	}
	objects := []api.SnowflakeObjectGrant{}
	for _, name := range strings.Split(strings.ReplaceAll(c.FormValue("snowflake_select_tables"), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(name) == "" {
			continue
		}
		objects = append(objects, api.SnowflakeObjectGrant{Name: name, Privilege: api.SELECT})
	}
	for _, name := range strings.Split(strings.ReplaceAll(c.FormValue("snowflake_insert_tables"), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(name) == "" {
			continue
		}
		objects = append(objects, api.SnowflakeObjectGrant{Name: name, Privilege: api.INSERT})
	}
	if len(objects) > 0 {
		config.WifObjects = &objects
	}

	return config, ""
}

func parseSQLCredentialsForm(c fiber.Ctx) (api.PostgresCredentials, string) {
	username := strings.TrimSpace(c.FormValue("username"))
	password, hasPassword := formCredentialValue(c, "password")
	if username == "" || !hasPassword {
		return api.PostgresCredentials{}, validationCredentialsRequiredMessage
	}
	return api.PostgresCredentials{Username: username, Password: password}, ""
}

func parseMySQLCredentialsForm(c fiber.Ctx) (api.MySQLCredentials, string) {
	username := strings.TrimSpace(c.FormValue("username"))
	password, _ := formCredentialValue(c, "password")
	if username == "" {
		return api.MySQLCredentials{}, "Validation: username is required for MySQL credentials."
	}
	return api.MySQLCredentials{Username: username, Password: password}, ""
}

func parseMongoCredentialsForm(c fiber.Ctx) (api.MongoDBCredentials, string) {
	username := strings.TrimSpace(c.FormValue("username"))
	password, hasPassword := formCredentialValue(c, "password")
	if username == "" || !hasPassword {
		return api.MongoDBCredentials{}, validationCredentialsRequiredMessage
	}
	return api.MongoDBCredentials{Username: username, Password: password}, ""
}

func parseHTTPCredentialsForm(c fiber.Ctx) (api.HTTPCredentials, string) {
	headers := make(map[string]string)
	seenHeaders := make(map[string]bool)
	for i := 0; i < 8; i++ {
		name := strings.TrimSpace(c.FormValue(fmt.Sprintf("secret_header_name_%d", i)))
		value, present := formCredentialValue(c, fmt.Sprintf("secret_header_value_%d", i))
		if name == "" && !present {
			continue
		}
		if name == "" || !present || seenHeaders[strings.ToLower(name)] {
			return api.HTTPCredentials{}, "Validation: provide unique secret header names and values."
		}
		seenHeaders[strings.ToLower(name)] = true
		headers[name] = value
	}
	mode := strings.TrimSpace(c.FormValue("http_auth_type"))
	if mode != "" && mode != "static" {
		if mode != "authorization_code" && mode != "client_credentials" {
			return api.HTTPCredentials{}, "Validation: select a supported HTTP authentication method."
		}
		if _, ok := formCredentialValue(c, "bearer_token"); ok {
			return api.HTTPCredentials{}, "Validation: OAuth cannot be combined with static credentials."
		}
		if len(headers) > 0 || c.FormValue("api_key") != "" || c.FormValue("username") != "" || c.FormValue("password") != "" {
			return api.HTTPCredentials{}, "Validation: OAuth cannot be combined with static credentials."
		}
		fields := httpAuthFormDataFromRequest(c)
		secret, hasSecret := formCredentialValue(c, "http_oauth_client_secret")
		if fields.Issuer == "" || fields.TokenEndpoint == "" || fields.ClientID == "" || (mode == "authorization_code" && fields.AuthorizationEndpoint == "") || (mode == "client_credentials" && !hasSecret) {
			return api.HTTPCredentials{}, "Validation: complete the required OAuth app settings."
		}
		oauth := api.HTTPOAuthCredentials{Issuer: fields.Issuer, TokenEndpoint: fields.TokenEndpoint, ClientId: fields.ClientID, GrantType: api.HTTPOAuthCredentialsGrantType(mode), AuthorizationEndpoint: optionalStringPointer(fields.AuthorizationEndpoint), RevocationEndpoint: optionalStringPointer(fields.RevocationEndpoint), Audience: optionalStringPointer(fields.Audience)}
		if hasSecret {
			oauth.ClientSecret = &secret
		}
		if scopes := strings.Fields(fields.Scopes); len(scopes) > 0 {
			oauth.Scopes = &scopes
		}
		return api.HTTPCredentials{Oauth: &oauth}, ""
	}

	bearerToken, hasBearerToken := formCredentialValue(c, "bearer_token")
	username := strings.TrimSpace(c.FormValue("username"))
	password, hasPassword := formCredentialValue(c, "password")

	credentials := api.HTTPCredentials{}
	if len(headers) > 0 {
		credentials.SecretHeaders = &headers
	}
	if key, present := formCredentialValue(c, "api_key"); present {
		credentials.ApiKey = &key
		header := api.HTTPCredentialsApiKeyHeader(strings.TrimSpace(c.FormValue("api_key_header")))
		credentials.ApiKeyHeader = &header
	}
	if hasBearerToken {
		credentials.BearerToken = &bearerToken
	}
	if username != "" {
		credentials.Username = &username
	}
	if hasPassword {
		credentials.Password = &password
	}

	return credentials, ""
}

func parseHubSpotCredentialsForm(c fiber.Ctx) (api.HubSpotCredentials, string) {
	privateAppToken, hasPrivateAppToken := formCredentialValue(c, "private_app_token")
	if !hasPrivateAppToken {
		return api.HubSpotCredentials{}, "Validation: private app token is required for HubSpot data sources."
	}

	return api.HubSpotCredentials{PrivateAppToken: privateAppToken}, ""
}

// parseS3CredentialsForm extracts write-only static S3 credential fields.
func parseS3CredentialsForm(c fiber.Ctx) (api.S3Credentials, string) {
	accessKeyID := strings.TrimSpace(c.FormValue("access_key_id"))
	secretAccessKey, hasSecretAccessKey := formCredentialValue(c, "secret_access_key")
	if accessKeyID == "" || !hasSecretAccessKey {
		return api.S3Credentials{}, "Validation: access key ID and secret access key are required for S3 data sources."
	}

	credentials := api.S3Credentials{
		AccessKeyId:     &accessKeyID,
		SecretAccessKey: &secretAccessKey,
	}
	if sessionToken, hasSessionToken := formCredentialValue(c, "session_token"); hasSessionToken {
		credentials.SessionToken = &sessionToken
	}

	return credentials, ""
}

func parseSnowflakeCredentialsForm(c fiber.Ctx) (api.SnowflakeCredentials, string) {
	authType := strings.TrimSpace(c.FormValue("snowflake_auth_type"))
	if authType == "" {
		authType = snowflakeAuthTypeWorkloadIdentity
	}
	if authType != snowflakeAuthTypePassword && authType != snowflakeAuthTypeWorkloadIdentity {
		return api.SnowflakeCredentials{}, "Validation: Snowflake authentication must be password or workload identity."
	}
	var credentials api.SnowflakeCredentials
	if authType == snowflakeAuthTypeWorkloadIdentity {
		if err := credentials.FromSnowflakeWorkloadIdentityCredentials(api.SnowflakeWorkloadIdentityCredentials{}); err != nil {
			return api.SnowflakeCredentials{}, "Validation: invalid Snowflake workload identity credentials."
		}
		return credentials, ""
	}
	username := strings.TrimSpace(c.FormValue("username"))
	password, hasPassword := formCredentialValue(c, "password")
	if username == "" || !hasPassword {
		return api.SnowflakeCredentials{}, "Validation: username and password are required for Snowflake password authentication."
	}
	if err := credentials.FromSnowflakePasswordCredentials(api.SnowflakePasswordCredentials{
		Username: &username,
		Password: &password,
	}); err != nil {
		return api.SnowflakeCredentials{}, "Validation: invalid Snowflake password credentials."
	}
	return credentials, ""
}

// formCredentialValue returns write-only credential text exactly as submitted
// while using trimmed whitespace only to decide whether the field is present.
func formCredentialValue(c fiber.Ctx, name string) (string, bool) {
	value := c.FormValue(name)
	return value, strings.TrimSpace(value) != ""
}

func dataSourceUpdateFormDataFromDataSource(ds *api.DataSource) views.DataSourceUpdateFormData {
	if ds == nil {
		return views.DataSourceUpdateFormData{}
	}

	form := views.DataSourceUpdateFormData{
		Name:              ds.Name,
		Type:              string(ds.Type),
		HubSpotReadOnly:   ds.ReadOnly,
		GitAuthType:       "https",
		SnowflakeAuthType: "workload_identity",
	}

	switch ds.Type {
	case api.DataSourceTypeGoogleWorkspace:
		// Identity and consent change through their verified workflows; ordinary edits only rename.
		return form
	case api.DataSourceTypePostgres:
		cfg, err := ds.Config.AsPostgresConfig()
		if err != nil {
			return form
		}
		form.Host = cfg.Host
		form.Port = strconv.Itoa(cfg.Port)
		form.Database = cfg.Database
		if cfg.SslMode != nil {
			form.SSLMode = string(*cfg.SslMode)
		}
		if cfg.ConnectTimeout != nil {
			form.ConnectTimeout = strconv.Itoa(*cfg.ConnectTimeout)
		}
		if cfg.AllowPrivateHosts != nil {
			form.DatabaseAllowPrivateHosts = *cfg.AllowPrivateHosts
		}
	case api.DataSourceTypeMysql:
		cfg, err := ds.Config.AsMySQLConfig()
		if err != nil {
			return form
		}
		form.Host = cfg.Host
		form.Port = strconv.Itoa(cfg.Port)
		form.Database = cfg.Database
		if cfg.ConnectTimeout != nil {
			form.ConnectTimeout = strconv.Itoa(*cfg.ConnectTimeout)
		}
		if cfg.AllowPrivateHosts != nil {
			form.DatabaseAllowPrivateHosts = *cfg.AllowPrivateHosts
		}
	case api.DataSourceTypeBigquery:
		cfg, err := ds.Config.AsBigQueryConfig()
		if err != nil {
			return form
		}
		form.ProjectID = cfg.ProjectId
		if cfg.Location != nil {
			form.Location = *cfg.Location
		}
		if cfg.DefaultDataset != nil {
			form.DefaultDataset = *cfg.DefaultDataset
		}
		if cfg.TimeoutSeconds != nil {
			form.TimeoutSeconds = strconv.Itoa(*cfg.TimeoutSeconds)
		}
		if cfg.MaximumBytesBilled != nil {
			form.MaximumBytesBilled = strconv.FormatInt(*cfg.MaximumBytesBilled, 10)
		}
	case api.DataSourceTypeSnowflake:
		cfg, err := ds.Config.AsSnowflakeConfig()
		if err != nil {
			return form
		}
		if cfg.WifObjects != nil {
			for _, object := range *cfg.WifObjects {
				switch object.Privilege {
				case api.SELECT:
					form.SnowflakeSelectTables += object.Name + "\n"
				case api.INSERT:
					form.SnowflakeInsertTables += object.Name + "\n"
				}
			}
		}

		form.Account = cfg.Account
		form.Warehouse = cfg.Warehouse
		form.Database = cfg.Database
		form.Schema = cfg.Schema
		if cfg.Role != nil {
			form.Role = *cfg.Role
		}
		if cfg.ConnectTimeout != nil {
			form.ConnectTimeout = strconv.Itoa(*cfg.ConnectTimeout)
		}
	case api.DataSourceTypeMongodb:
		cfg, err := ds.Config.AsMongoDBConfig()
		if err != nil {
			return form
		}
		form.Host = cfg.Host
		form.Port = strconv.Itoa(cfg.Port)
		form.Database = cfg.Database
		if cfg.AuthDatabase != nil {
			form.AuthDatabase = *cfg.AuthDatabase
		}
		if cfg.ConnectTimeout != nil {
			form.ConnectTimeout = strconv.Itoa(*cfg.ConnectTimeout)
		}
		if cfg.AllowPrivateHosts != nil {
			form.DatabaseAllowPrivateHosts = *cfg.AllowPrivateHosts
		}
	case api.DataSourceTypeHttp:
		cfg, err := ds.Config.AsHTTPConfig()
		if err != nil {
			return form
		}
		form.BaseURL = cfg.BaseUrl
		if cfg.HealthPath != nil {
			form.HealthPath = *cfg.HealthPath
		}
		if cfg.TimeoutSeconds != nil {
			form.TimeoutSeconds = strconv.Itoa(*cfg.TimeoutSeconds)
		}
		if cfg.OpenapiUrl != nil {
			form.OpenAPIURL = *cfg.OpenapiUrl
		}
		if cfg.OpenapiFetchTimeoutSeconds != nil {
			form.OpenAPIFetchTimeout = strconv.Itoa(*cfg.OpenapiFetchTimeoutSeconds)
		}
		if cfg.AllowPrivateHosts != nil {
			form.HTTPAllowPrivateHosts = *cfg.AllowPrivateHosts
		}
	case api.DataSourceTypeHubspot:
		cfg, err := ds.Config.AsHubSpotConfig()
		if err != nil {
			return form
		}
		if cfg.TimeoutSeconds != nil {
			form.TimeoutSeconds = strconv.Itoa(*cfg.TimeoutSeconds)
		}
	case api.DataSourceTypeS3:
		cfg, err := ds.Config.AsS3Config()
		if err != nil {
			return form
		}
		form.S3Region = cfg.Region
		form.S3Bucket = cfg.Bucket
		if cfg.Endpoint != nil {
			form.S3Endpoint = *cfg.Endpoint
		}
		if cfg.Prefix != nil {
			form.S3Prefix = *cfg.Prefix
		}
		if cfg.ForcePathStyle != nil {
			form.S3ForcePathStyle = *cfg.ForcePathStyle
		}
		if cfg.AllowPrivateHosts != nil {
			form.S3AllowPrivateHosts = *cfg.AllowPrivateHosts
		}
		if cfg.TimeoutSeconds != nil {
			form.TimeoutSeconds = strconv.Itoa(*cfg.TimeoutSeconds)
		}
	case api.DataSourceTypeFtp:
		cfg, err := ds.Config.AsFTPConfig()
		if err != nil {
			return form
		}
		form.Host = cfg.Host
		form.Port = strconv.Itoa(cfg.Port)
		if cfg.ConnectTimeout != nil {
			form.ConnectTimeout = strconv.Itoa(*cfg.ConnectTimeout)
		}
	case api.DataSourceTypeGit, api.DataSourceTypeGithub, api.DataSourceTypeGitlab:
		if ds.Type != api.DataSourceTypeGit {
			managed, err := ds.Config.AsProviderGitConfig()
			if err == nil && managed.Repository.Id != "" {
				form.ManagedProvider = true
				form.ProviderRepositoryID = managed.Repository.Id
				form.ProviderRepositoryName = managed.Repository.FullName
				form.ProviderRepositoryURL = managed.Repository.WebUrl
				if managed.BasePath != nil {
					form.GitBasePath = *managed.BasePath
				}
				return form
			}
		}
		cfg, err := ds.Config.AsGitConfig()
		if err != nil {
			return form
		}
		form.GitRepoURL = cfg.RepoUrl
		form.ProviderSelectionPending = ds.Type != api.DataSourceTypeGit && strings.TrimSpace(cfg.RepoUrl) == ""
		if authType := gitRepoURLAuthType(cfg.RepoUrl); authType != "" {
			form.GitAuthType = authType
		}
		if cfg.DefaultBranch != nil {
			form.GitBranch = *cfg.DefaultBranch
		}
		if cfg.BasePath != nil {
			form.GitBasePath = *cfg.BasePath
		}
		if cfg.AllowPrivateHosts != nil {
			form.GitAllowPrivateHosts = *cfg.AllowPrivateHosts
		}
	}

	return form
}

func urlQueryEscape(value string) string {
	return url.QueryEscape(strings.TrimSpace(value))
}

// parseSQLFormWithPrivateHosts converts PostgreSQL create/test fields into API payload pieces.
func parseSQLFormWithPrivateHosts(
	c fiber.Ctx,
	defaultPort int,
	allowPrivateDataSourceHosts bool,
) (api.PostgresConfig, api.PostgresCredentials, string) {
	host := strings.TrimSpace(c.FormValue("host"))
	database := strings.TrimSpace(c.FormValue("database"))
	username := strings.TrimSpace(c.FormValue("username"))
	password, hasPassword := formCredentialValue(c, "password")

	if host == "" || database == "" || username == "" || !hasPassword {
		return api.PostgresConfig{}, api.PostgresCredentials{}, "Validation: host, database, username, and password are required for SQL data sources."
	}

	config, err := parseSQLConfigFormWithPrivateHosts(c, defaultPort, allowPrivateDataSourceHosts)
	if err != "" {
		return api.PostgresConfig{}, api.PostgresCredentials{}, err
	}

	return config, api.PostgresCredentials{
		Username: username,
		Password: password,
	}, ""
}

// parseMySQLFormWithPrivateHosts converts MySQL create/test fields into API payload pieces.
func parseMySQLFormWithPrivateHosts(
	c fiber.Ctx,
	defaultPort int,
	allowPrivateDataSourceHosts bool,
) (api.MySQLConfig, api.MySQLCredentials, string) {
	host := strings.TrimSpace(c.FormValue("host"))
	database := strings.TrimSpace(c.FormValue("database"))
	username := strings.TrimSpace(c.FormValue("username"))
	password, _ := formCredentialValue(c, "password")

	if host == "" || database == "" || username == "" {
		return api.MySQLConfig{}, api.MySQLCredentials{}, "Validation: host, database, and username are required for MySQL data sources."
	}

	port, err := parseOptionalInt(c.FormValue("port"), defaultPort)
	if err != "" {
		return api.MySQLConfig{}, api.MySQLCredentials{}, validationPortMustBeIntegerMessage
	}

	connectTimeout, timeoutErr := parseOptionalPositiveIntPointer(c.FormValue("connect_timeout"))
	if timeoutErr != "" {
		return api.MySQLConfig{}, api.MySQLCredentials{}, validationConnectTimeoutPositiveMessage
	}

	return api.MySQLConfig{
			Host:              host,
			Port:              port,
			Database:          database,
			ConnectTimeout:    connectTimeout,
			AllowPrivateHosts: privateHostsFormPointer(c, databaseAllowPrivateHostsField, allowPrivateDataSourceHosts),
		}, api.MySQLCredentials{
			Username: username,
			Password: password,
		}, ""
}

// parseMongoFormWithPrivateHosts converts MongoDB create/test fields into API payload pieces.
func parseMongoFormWithPrivateHosts(
	c fiber.Ctx,
	defaultPort int,
	allowPrivateDataSourceHosts bool,
) (api.MongoDBConfig, api.MongoDBCredentials, string) {
	host := strings.TrimSpace(c.FormValue("host"))
	database := strings.TrimSpace(c.FormValue("database"))
	username := strings.TrimSpace(c.FormValue("username"))
	password, hasPassword := formCredentialValue(c, "password")
	authDatabase := strings.TrimSpace(c.FormValue("auth_database"))

	if host == "" || database == "" || username == "" || !hasPassword {
		return api.MongoDBConfig{}, api.MongoDBCredentials{}, "Validation: host, database, username, and password are required for MongoDB data sources."
	}

	port, err := parseOptionalInt(c.FormValue("port"), defaultPort)
	if err != "" {
		return api.MongoDBConfig{}, api.MongoDBCredentials{}, validationPortMustBeIntegerMessage
	}

	connectTimeout, timeoutErr := parseOptionalPositiveIntPointer(c.FormValue("connect_timeout"))
	if timeoutErr != "" {
		return api.MongoDBConfig{}, api.MongoDBCredentials{}, validationConnectTimeoutPositiveMessage
	}

	var authDatabasePtr *string
	if authDatabase != "" {
		authDatabasePtr = &authDatabase
	}

	return api.MongoDBConfig{
			Host:              host,
			Port:              port,
			Database:          database,
			AuthDatabase:      authDatabasePtr,
			ConnectTimeout:    connectTimeout,
			AllowPrivateHosts: privateHostsFormPointer(c, databaseAllowPrivateHostsField, allowPrivateDataSourceHosts),
		}, api.MongoDBCredentials{
			Username: username,
			Password: password,
		}, ""
}

// parseHTTPFormWithPrivateHosts validates policy-gated HTTP config and attaches
// write-only credentials from the submitted form.
func parseHTTPFormWithPrivateHosts(
	c fiber.Ctx,
	allowPrivateDataSourceHosts bool,
) (api.HTTPConfig, api.HTTPCredentials, string) {
	config, err := parseHTTPBaseConfig(c, allowPrivateDataSourceHosts)
	if err != "" {
		return api.HTTPConfig{}, api.HTTPCredentials{}, err
	}

	credentials, errMsg := parseHTTPCredentialsForm(c)
	return config, credentials, errMsg
}

// allowPrivateHostsFormPointer reads the HTTP-specific private-host checkbox with legacy fallback.
func allowPrivateHostsFormPointer(c fiber.Ctx, allowPrivateDataSourceHosts bool) *bool {
	if !allowPrivateDataSourceHosts {
		return nil
	}
	allowPrivateHostsRaw := strings.TrimSpace(c.FormValue("http_allow_private_hosts"))
	if allowPrivateHostsRaw == "" {
		allowPrivateHostsRaw = strings.TrimSpace(c.FormValue("allow_private_hosts"))
	}
	if allowPrivateHostsRaw == "" {
		return nil
	}
	allowPrivateHosts := parseCheckbox(allowPrivateHostsRaw)
	return &allowPrivateHosts
}

// privateHostsFormPointer returns an explicit checkbox value when deployment policy permits it.
func privateHostsFormPointer(c fiber.Ctx, fieldName string, allowPrivateDataSourceHosts bool) *bool {
	if !allowPrivateDataSourceHosts {
		return nil
	}
	allowPrivateHosts := parseCheckbox(c.FormValue(fieldName))
	return &allowPrivateHosts
}

func parseHubSpotForm(c fiber.Ctx) (api.HubSpotConfig, api.HubSpotCredentials, string) {
	config, err := parseHubSpotConfigForm(c)
	if err != "" {
		return api.HubSpotConfig{}, api.HubSpotCredentials{}, err
	}

	credentials, err := parseHubSpotCredentialsForm(c)
	if err != "" {
		return api.HubSpotConfig{}, api.HubSpotCredentials{}, err
	}

	return config, credentials, ""
}

func parseSnowflakeForm(c fiber.Ctx) (api.SnowflakeConfig, api.SnowflakeCredentials, string) {
	config, errMsg := parseSnowflakeConfigForm(c)
	if errMsg != "" {
		return api.SnowflakeConfig{}, api.SnowflakeCredentials{}, errMsg
	}
	credentials, errMsg := parseSnowflakeCredentialsForm(c)
	return config, credentials, errMsg
}

// parseGitFormWithPrivateHosts combines Git config and write-only credentials.
func parseGitFormWithPrivateHosts(
	c fiber.Ctx,
	allowPrivateDataSourceHosts bool,
) (api.GitConfig, api.GitCredentials, string) {
	config, err := parseGitConfigFormWithPrivateHosts(c, allowPrivateDataSourceHosts)
	if err != "" {
		return api.GitConfig{}, api.GitCredentials{}, err
	}

	authType, err := parseGitAuthTypeForm(c)
	if err != "" {
		return api.GitConfig{}, api.GitCredentials{}, err
	}
	if err := validateGitAuthTypeForRepoURL(config.RepoUrl, authType); err != "" {
		return api.GitConfig{}, api.GitCredentials{}, err
	}

	credentials, err := parseGitCredentialsFormForAuthType(c, authType)
	if err != "" {
		return api.GitConfig{}, api.GitCredentials{}, err
	}

	return config, credentials, ""
}

// parseGitConfigFormWithPrivateHosts converts Git fields into public API config.
func parseGitConfigFormWithPrivateHosts(
	c fiber.Ctx,
	allowPrivateDataSourceHosts bool,
) (api.GitConfig, string) {
	repoURL := strings.TrimSpace(c.FormValue("git_repo_url"))
	if repoURL == "" {
		return api.GitConfig{}, "Validation: repository URL is required for Git data sources."
	}
	if errMsg := validateGitRepoURLForForm(repoURL); errMsg != "" {
		return api.GitConfig{}, errMsg
	}

	defaultBranch := strings.TrimSpace(c.FormValue("git_default_branch"))
	basePath := strings.TrimSpace(c.FormValue("git_base_path"))

	config := api.GitConfig{RepoUrl: repoURL}
	if allowPrivateDataSourceHosts {
		allowPrivateHosts := parseCheckbox(c.FormValue("git_allow_private_hosts"))
		config.AllowPrivateHosts = &allowPrivateHosts
	}
	if defaultBranch != "" {
		config.DefaultBranch = &defaultBranch
	}
	if basePath != "" {
		config.BasePath = &basePath
	}

	return config, ""
}

// validateGitRepoURLForForm rejects Git repository URLs that the connector cannot test.
func validateGitRepoURLForForm(repoURL string) string {
	repoURL = strings.TrimSpace(repoURL)
	if !strings.Contains(repoURL, "://") && strings.Contains(repoURL, "@") && strings.Contains(repoURL, ":") {
		at := strings.LastIndex(repoURL, "@")
		colon := strings.LastIndex(repoURL, ":")
		if at > 0 && colon > at && strings.Contains(repoURL[:at], ":") {
			return "Validation: repository URL must not include embedded credentials."
		}
		return ""
	}

	parsed, err := url.Parse(repoURL)
	if err != nil {
		return "Validation: repository URL is invalid."
	}
	if parsed.User != nil {
		_, hasPassword := parsed.User.Password()
		if parsed.Scheme != gitAuthTypeSSH || hasPassword {
			return "Validation: repository URL must not include embedded credentials."
		}
	}
	if parsed.Scheme != gitAuthTypeHTTPS && parsed.Scheme != gitAuthTypeSSH {
		return "Validation: repository URL must use HTTPS or SSH."
	}
	if parsed.Host == "" {
		return "Validation: repository URL must include a valid hostname."
	}

	return ""
}

// parseGitAuthTypeForm returns the selected Git auth type, defaulting to HTTPS.
func parseGitAuthTypeForm(c fiber.Ctx) (string, string) {
	authType := strings.TrimSpace(c.FormValue("git_auth_type"))
	if authType == "" {
		authType = gitAuthTypeHTTPS
	}

	if authType != gitAuthTypeHTTPS && authType != gitAuthTypeSSH {
		return "", "Validation: git auth type must be https or ssh."
	}

	return authType, ""
}

// parseGitCredentialsFormForAuthType builds the write-only Git credentials for
// a validated auth type while preserving secret field bytes.
func parseGitCredentialsFormForAuthType(c fiber.Ctx, authType string) (api.GitCredentials, string) {
	credentials := api.GitCredentials{AuthType: api.GitCredentialsAuthType(authType)}

	httpsUsername := strings.TrimSpace(c.FormValue("git_username"))
	httpsToken, hasHTTPSToken := formCredentialValue(c, "git_token")
	sshPrivateKey, hasSSHPrivateKey := formCredentialValue(c, "git_ssh_private_key")
	sshKnownHosts := strings.TrimSpace(c.FormValue("git_ssh_known_hosts"))
	sshPassphrase, hasSSHPassphrase := formCredentialValue(c, "git_ssh_passphrase")

	if authType == gitAuthTypeHTTPS {
		if httpsUsername != "" {
			credentials.Username = &httpsUsername
		}
		if hasHTTPSToken {
			credentials.Token = &httpsToken
		}
		return credentials, ""
	}

	if !hasSSHPrivateKey {
		return api.GitCredentials{}, "Validation: SSH private key is required for git SSH authentication."
	}
	if sshKnownHosts == "" {
		return api.GitCredentials{}, "Validation: SSH known_hosts is required for git SSH authentication."
	}

	credentials.SshPrivateKey = &sshPrivateKey
	credentials.SshKnownHosts = &sshKnownHosts
	if hasSSHPassphrase {
		credentials.SshPassphrase = &sshPassphrase
	}

	return credentials, ""
}

// validateGitAuthTypeForRepoURL prevents auth settings that Git will ignore for
// the submitted repository URL transport.
func validateGitAuthTypeForRepoURL(repoURL, authType string) string {
	switch gitRepoURLAuthType(repoURL) {
	case gitAuthTypeHTTPS:
		if authType != gitAuthTypeHTTPS {
			return "Validation: HTTPS repository URLs require HTTPS authentication."
		}
	case gitAuthTypeSSH:
		if authType != gitAuthTypeSSH {
			return "Validation: SSH repository URLs require SSH authentication."
		}
	}
	return ""
}

// gitRepoURLAuthType derives the Git credential transport implied by a repo URL.
func gitRepoURLAuthType(repoURL string) string {
	repoURL = strings.TrimSpace(repoURL)
	if !strings.Contains(repoURL, "://") && strings.Contains(repoURL, "@") && strings.Contains(repoURL, ":") {
		return gitAuthTypeSSH
	}

	parsed, err := url.Parse(repoURL)
	if err != nil {
		return ""
	}
	switch parsed.Scheme {
	case gitAuthTypeHTTPS:
		return gitAuthTypeHTTPS
	case gitAuthTypeSSH:
		return gitAuthTypeSSH
	default:
		return ""
	}
}

func parseCheckbox(value string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	return trimmed == "on" || trimmed == booleanTrueString || trimmed == "1" || trimmed == "yes"
}

func dataSourceShouldRenderSidePanel(c fiber.Ctx) bool {
	return isHTMXRequest(c) && strings.EqualFold(strings.TrimSpace(dataSourcePostFormValue(c, "return_to")), "list")
}

func dataSourceActiveEnvironmentSlug(c fiber.Ctx) string {
	if environmentSlug := strings.TrimSpace(dataSourcePostFormValue(c, "environment_slug")); environmentSlug != "" {
		return environmentSlug
	}
	return strings.TrimSpace(c.Query("environment_slug"))
}

func dataSourceReturnURL(c fiber.Ctx, organizationSlug, dataSourceSlug string) string {
	environmentSlug := dataSourceActiveEnvironmentSlug(c)
	if strings.EqualFold(strings.TrimSpace(dataSourcePostFormValue(c, "return_to")), "list") {
		return dataSourceListURL(organizationSlug, environmentSlug, dataSourceSlug)
	}
	detail, _ := url.Parse(views.DataSourceDetailHref(organizationSlug, environmentSlug, dataSourceSlug))
	values := detail.Query()
	switch section := c.Query("section"); section {
	case "overview", "operations", "schema", "settings":
		values.Set("section", section)
	}
	if listReturn := c.Query("list_return"); listReturn != "" {
		values.Set("list_return", listReturn)
	}
	detail.RawQuery = values.Encode()
	return detail.String()
}

// dataSourcePostFormValue reads URL-encoded form values after middleware has inspected the request body.
func dataSourcePostFormValue(c fiber.Ctx, name string) string {
	if c.Request().PostArgs().Has(name) {
		return string(c.Request().PostArgs().Peek(name))
	}
	return c.FormValue(name)
}

// dataSourceListURL builds the catalog redirect while preserving environment and selected-row context.
func dataSourceListURL(organizationSlug, environmentSlug, selectedDataSourceSlug string) string {
	baseURL := "/orgs/" + strings.TrimSpace(organizationSlug) + "/data-sources"
	query := url.Values{}
	if environmentSlug := strings.TrimSpace(environmentSlug); environmentSlug != "" {
		query.Set("environment_slug", environmentSlug)
	}
	if selectedDataSourceSlug := strings.TrimSpace(selectedDataSourceSlug); selectedDataSourceSlug != "" {
		query.Set("selected", selectedDataSourceSlug)
	}
	if len(query) == 0 {
		return baseURL
	}
	return baseURL + "?" + query.Encode()
}

func (r *Router) renderDataSourceSidePanel(
	c fiber.Ctx,
	orgSlug slug.Slug,
	dataSourceSlug string,
	activeEnvironmentSlug string,
	feedbackMessage string,
	feedbackKind string,
	_ int,
) error {
	return r.renderDataSourceSidePanelWithSchemaRefreshError(
		c,
		orgSlug,
		dataSourceSlug,
		activeEnvironmentSlug,
		feedbackMessage,
		feedbackKind,
		"",
	)
}

func (r *Router) renderDataSourceSidePanelWithSchemaRefreshError(
	c fiber.Ctx,
	orgSlug slug.Slug,
	dataSourceSlug string,
	activeEnvironmentSlug string,
	feedbackMessage string,
	feedbackKind string,
	schemaRefreshError string,
) error {
	setHTMLResponseStatus(c, http.StatusOK)

	selectedDataSource, schemaPanel, connectionInfo, updateFormData, _, selectedSummary, _, selectedUnauthorized := r.loadSelectedDataSourcePanel(
		c,
		orgSlug,
		dataSourceSlug,
	)
	if selectedUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if schemaRefreshError := strings.TrimSpace(schemaRefreshError); schemaRefreshError != "" {
		schemaPanel = dataSourceSchemaPanelData(selectedDataSource, schemaRefreshError)
	}
	if feedbackMessage == "" {
		feedbackMessage = selectedSummary
		feedbackKind = feedbackKindError
	}

	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)

	return renderTempl(
		c,
		views.DataSourceDetailPanelSlot(
			orgSlug.String(),
			activeEnvironmentSlug,
			selectedDataSource,
			schemaPanel,
			connectionInfo,
			updateFormData,
			permissions,
			feedbackMessage,
			feedbackKind,
		),
	)
}

func appendDataSourceReturnQuery(baseURL, key, value string) string {
	separator := "?"
	if strings.Contains(baseURL, "?") {
		separator = "&"
	}
	return baseURL + separator + url.QueryEscape(key) + "=" + urlQueryEscape(value)
}

func parseOptionalInt(input string, defaultValue int) (int, string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return defaultValue, ""
	}

	value, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, err.Error()
	}

	return value, ""
}

func parseOptionalPositiveIntPointer(input string) (*int, string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, ""
	}

	value, err := strconv.Atoi(trimmed)
	if err != nil || value <= 0 {
		return nil, "invalid"
	}

	return &value, ""
}

// parseOptionalPositiveInt64Pointer handles optional large numeric guardrails
// such as BigQuery maximumBytesBilled values.
func parseOptionalPositiveInt64Pointer(input string) (*int64, string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, ""
	}

	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || value <= 0 {
		return nil, "invalid"
	}

	return &value, ""
}

func (r *Router) renderDataSourcesPageWithError(
	c fiber.Ctx,
	orgSlug slug.Slug,
	status int,
	summary string,
	details string,
	formData views.DataSourceCreateFormData,
) error {
	if status < 100 || status >= 600 {
		status = http.StatusInternalServerError
	}
	if isHTMXRequest(c) && status >= http.StatusBadRequest {
		status = http.StatusOK
	}
	r.applyPrivateHostPolicyToCreateForm(&formData)
	r.loadManagedGitRepositories(c, orgSlug.String(), &formData)
	formData.WorkspacePrivacyURL = r.workspacePrivacyURL
	c.Status(status)

	params := &api.ListDataSourcesParams{}
	limit := api.LimitParam(20)
	params.Limit = &limit

	list, listStatus, errorResponse, listErr := r.listDataSourcesViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		params,
	)
	if listErr != nil {
		if listStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		fallback := backendErrorDetails(
			"reload data sources after failed action",
			listStatus,
			errorResponse,
			listErr,
		)
		r.logRenderedBackendAPIFailure(c, "reload data sources after failed action", errorResponse, listErr, orgSlug.String(), "")
		details = mergeErrorDetails(details, fallback)

		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Data sources",
			orgSlug.String(),
			summary,
			details,
			c.Get(fiber.HeaderCookie),
		)
		permissions := r.resolveOrganizationPermissions(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
		)

		return renderTempl(
			c,
			views.DataSourcesPage(
				shellData,
				[]api.DataSource{},
				[]api.DataSource{},
				false,
				"",
				"",
				orgSlug.String(),
				"",
				nil,
				views.DefaultDataSourceSchemaPanelData(),
				nil,
				views.DataSourceUpdateFormData{},
				formData,
				permissions,
				"",
				"",
				views.DataSourcePageFeedback{},
			),
		)
	}

	nextCursor := ""
	prevCursor := ""
	if list.NextCursor != nil {
		nextCursor = *list.NextCursor
	}
	if list.PrevCursor != nil {
		prevCursor = *list.PrevCursor
	}

	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Data sources",
		orgSlug.String(),
		summary,
		details,
		c.Get(fiber.HeaderCookie),
	)
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)

	return renderTempl(
		c,
		views.DataSourcesPage(
			shellData,
			list.DataSources,
			list.DataSources,
			list.HasMore,
			nextCursor,
			prevCursor,
			orgSlug.String(),
			"",
			nil,
			views.DefaultDataSourceSchemaPanelData(),
			nil,
			views.DataSourceUpdateFormData{},
			formData,
			permissions,
			"",
			"",
			views.DataSourcePageFeedback{},
		),
	)
}

// extractConnectionInfo extracts connection configuration from a data source
func extractConnectionInfo(ds *api.DataSource) *views.ConnectionInfo {
	if ds == nil {
		return nil
	}

	info := &views.ConnectionInfo{
		Type: string(ds.Type),
	}
	addField := func(key, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		info.Fields = append(info.Fields, views.ConnectionInfoField{Key: key, Value: value})
	}

	switch ds.Type {
	case api.DataSourceTypeGoogleWorkspace:
		cfg, err := ds.Config.AsGoogleWorkspaceConfig()
		if err != nil {
			return info
		}
		mode := "unknown"
		if cfg.AccessMode != nil {
			mode = string(*cfg.AccessMode)
		}
		addField("access_mode", mode)
	case api.DataSourceTypePostgres:
		cfg, err := ds.Config.AsPostgresConfig()
		if err != nil {
			return info
		}
		addField("host", cfg.Host)
		addField("port", strconv.Itoa(cfg.Port))
		addField("database", cfg.Database)
		if cfg.SslMode != nil {
			addField("ssl_mode", string(*cfg.SslMode))
		}
		if cfg.ConnectTimeout != nil {
			addField("connect_timeout", strconv.Itoa(*cfg.ConnectTimeout))
		}
		if cfg.AllowPrivateHosts != nil {
			addField("allow_private_hosts", strconv.FormatBool(*cfg.AllowPrivateHosts))
		}
	case api.DataSourceTypeMysql:
		cfg, err := ds.Config.AsMySQLConfig()
		if err != nil {
			return info
		}
		addField("host", cfg.Host)
		addField("port", strconv.Itoa(cfg.Port))
		addField("database", cfg.Database)
		if cfg.ConnectTimeout != nil {
			addField("connect_timeout", strconv.Itoa(*cfg.ConnectTimeout))
		}
		if cfg.AllowPrivateHosts != nil {
			addField("allow_private_hosts", strconv.FormatBool(*cfg.AllowPrivateHosts))
		}
	case api.DataSourceTypeBigquery:
		cfg, err := ds.Config.AsBigQueryConfig()
		if err != nil {
			return info
		}
		addField("project_id", cfg.ProjectId)
		if cfg.Location != nil {
			addField("location", *cfg.Location)
		}
		if cfg.DefaultDataset != nil {
			addField("default_dataset", *cfg.DefaultDataset)
		}
		if cfg.TimeoutSeconds != nil {
			addField("timeout_seconds", strconv.Itoa(*cfg.TimeoutSeconds))
		}
		if cfg.MaximumBytesBilled != nil {
			addField("maximum_bytes_billed", strconv.FormatInt(*cfg.MaximumBytesBilled, 10))
		}
	case api.DataSourceTypeSnowflake:
		cfg, err := ds.Config.AsSnowflakeConfig()
		if err != nil {
			return info
		}
		addField("account", cfg.Account)
		addField("warehouse", cfg.Warehouse)
		addField("database", cfg.Database)
		addField("schema", cfg.Schema)
		if cfg.Role != nil {
			addField("role", *cfg.Role)
		}
		if cfg.ConnectTimeout != nil {
			addField("connect_timeout", strconv.Itoa(*cfg.ConnectTimeout))
		}
	case api.DataSourceTypeMongodb:
		cfg, err := ds.Config.AsMongoDBConfig()
		if err != nil {
			return info
		}
		addField("host", cfg.Host)
		addField("port", strconv.Itoa(cfg.Port))
		addField("database", cfg.Database)
		if cfg.AuthDatabase != nil {
			addField("auth_database", *cfg.AuthDatabase)
		}
		if cfg.ConnectTimeout != nil {
			addField("connect_timeout", strconv.Itoa(*cfg.ConnectTimeout))
		}
		if cfg.AllowPrivateHosts != nil {
			addField("allow_private_hosts", strconv.FormatBool(*cfg.AllowPrivateHosts))
		}
	case api.DataSourceTypeHttp:
		cfg, err := ds.Config.AsHTTPConfig()
		if err != nil {
			return info
		}
		addField("base_url", cfg.BaseUrl)
		if cfg.HealthPath != nil {
			addField("health_path", *cfg.HealthPath)
		}
		if cfg.TimeoutSeconds != nil {
			addField("timeout_seconds", strconv.Itoa(*cfg.TimeoutSeconds))
		}
		if cfg.OpenapiUrl != nil {
			addField("openapi_url", *cfg.OpenapiUrl)
		}
		if cfg.OpenapiFetchTimeoutSeconds != nil {
			addField("openapi_fetch_timeout_seconds", strconv.Itoa(*cfg.OpenapiFetchTimeoutSeconds))
		}
	case api.DataSourceTypeHubspot:
		cfg, err := ds.Config.AsHubSpotConfig()
		if err != nil {
			return info
		}
		if ds.ReadOnly {
			addField("access", "read-only")
		} else {
			addField("access", "read and write")
		}
		if cfg.TimeoutSeconds != nil {
			addField("timeout_seconds", strconv.Itoa(*cfg.TimeoutSeconds))
		}
	case api.DataSourceTypeS3:
		cfg, err := ds.Config.AsS3Config()
		if err != nil {
			return info
		}
		addField("bucket", cfg.Bucket)
		addField("region", cfg.Region)
		if cfg.Endpoint != nil {
			addField("endpoint", *cfg.Endpoint)
		}
		if cfg.Prefix != nil {
			addField("prefix", *cfg.Prefix)
		}
		if cfg.ForcePathStyle != nil {
			addField("force_path_style", strconv.FormatBool(*cfg.ForcePathStyle))
		}
		if cfg.TimeoutSeconds != nil {
			addField("timeout_seconds", strconv.Itoa(*cfg.TimeoutSeconds))
		}
		if cfg.AllowPrivateHosts != nil {
			addField("allow_private_hosts", strconv.FormatBool(*cfg.AllowPrivateHosts))
		}
	case api.DataSourceTypeFtp:
		cfg, err := ds.Config.AsFTPConfig()
		if err != nil {
			return info
		}
		addField("host", cfg.Host)
		addField("port", strconv.Itoa(cfg.Port))
		if cfg.BasePath != nil {
			addField("base_path", *cfg.BasePath)
		}
		if cfg.ConnectTimeout != nil {
			addField("connect_timeout", strconv.Itoa(*cfg.ConnectTimeout))
		}
	case api.DataSourceTypeGit, api.DataSourceTypeGithub, api.DataSourceTypeGitlab:
		cfg, err := ds.Config.AsGitConfig()
		if err != nil {
			return info
		}
		addField("repo_url", cfg.RepoUrl)
		if cfg.DefaultBranch != nil {
			addField("default_branch", *cfg.DefaultBranch)
		}
		if cfg.BasePath != nil {
			addField("base_path", *cfg.BasePath)
		}
		if cfg.AllowPrivateHosts != nil {
			if *cfg.AllowPrivateHosts {
				addField("allow_private_hosts", booleanTrueString)
			} else {
				addField("allow_private_hosts", "false")
			}
		}
	}

	return info
}

// defaultDataSourceCreateFormData returns create-form defaults with deployment policy flags applied.
func (r *Router) defaultDataSourceCreateFormData() views.DataSourceCreateFormData {
	formData := views.DefaultDataSourceCreateFormData()
	formData.WorkspacePrivacyURL = r.workspacePrivacyURL
	r.applyPrivateHostPolicyToCreateForm(&formData)
	return formData
}

func (r *Router) loadManagedGitRepositories(c fiber.Ctx, orgSlug string, formData *views.DataSourceCreateFormData) {
	ctx, cookie := c.Context(), c.Get(fiber.HeaderCookie)
	if formData == nil {
		return
	}
	resp, err := r.apiClient.GetOrganizationGitHubAppConnectionWithResponse(ctx, orgSlug, frontendRequestEditor(ctx, cookie))
	if err == nil && resp.StatusCode() == http.StatusOK && resp.JSON200 != nil && resp.JSON200.SuspendedAt == nil && resp.JSON200.DeletedAt == nil {
		formData.GitHubConnectionAvailable = true
		formData.GitHubRepositories = resp.JSON200.Repositories
	}
	gitlab, err := r.apiClient.ListOrganizationGitLabRepositoriesWithResponse(ctx, orgSlug, frontendRequestEditor(ctx, cookie))
	if err == nil && gitlab.StatusCode() == http.StatusOK && gitlab.JSON200 != nil {
		formData.GitLabConnectionAvailable = true
		formData.GitLabRepositories = gitlab.JSON200.Repositories
		return
	}
	if err == nil && (gitlab.StatusCode() == http.StatusNotFound || gitlab.StatusCode() == http.StatusNoContent) {
		return
	}
	formData.GitLabRepositoryError = "Could not load GitLab repositories. Retry, or check access in organization connections."
	var payload *api.ErrorResponse
	if err == nil {
		err = unexpectedBackendAPIResponse("list GitLab repositories", gitlab.StatusCode())
		payload = cmp.Or(gitlab.JSON400, gitlab.JSON401, gitlab.JSON403, gitlab.JSON404, gitlab.JSONDefault)
	}
	r.logRenderedBackendAPIFailure(c, "list GitLab repositories", payload, err, orgSlug, "")
}

// dataSourceCreateFormDataFromRequest keeps submitted form values that are allowed by deployment policy.
func (r *Router) dataSourceCreateFormDataFromRequest(c fiber.Ctx) views.DataSourceCreateFormData {
	formData := dataSourceCreateFormDataFromRequest(c)
	formData.WorkspacePrivacyURL = r.workspacePrivacyURL
	r.applyPrivateHostPolicyToCreateForm(&formData)
	return formData
}

// dataSourceUpdateFormDataFromDataSource prepares detail forms with deployment policy flags applied.
func (r *Router) dataSourceUpdateFormDataFromDataSource(ds *api.DataSource) views.DataSourceUpdateFormData {
	formData := dataSourceUpdateFormDataFromDataSource(ds)
	formData.WorkspacePrivacyURL = r.workspacePrivacyURL
	r.applyPrivateHostPolicyToUpdateForm(&formData)
	return formData
}

// applyPrivateHostPolicyToCreateForm mutates create form state so disabled
// deployments neither render nor resubmit customer private-host values.
func (r *Router) applyPrivateHostPolicyToCreateForm(formData *views.DataSourceCreateFormData) {
	if formData == nil {
		return
	}
	formData.AllowPrivateDataSourceHosts = r.allowPrivateDataSourceHosts
	if !r.allowPrivateDataSourceHosts {
		formData.S3AllowPrivateHosts = false
		formData.HTTPAllowPrivateHosts = false
		formData.GitAllowPrivateHosts = false
		formData.DatabaseAllowPrivateHosts = false
	}
}

// applyPrivateHostPolicyToUpdateForm hides and clears private-host update values when disabled.
func (r *Router) applyPrivateHostPolicyToUpdateForm(formData *views.DataSourceUpdateFormData) {
	if formData == nil {
		return
	}
	formData.AllowPrivateDataSourceHosts = r.allowPrivateDataSourceHosts
	if !r.allowPrivateDataSourceHosts {
		formData.HTTPAllowPrivateHosts = false
		formData.S3AllowPrivateHosts = false
		formData.GitAllowPrivateHosts = false
		formData.DatabaseAllowPrivateHosts = false
	}
}

// applyPrivateHostPolicyToFormFields mutates standalone field state so URI and
// type-switch partials cannot render private-host values when disabled.
func (r *Router) applyPrivateHostPolicyToFormFields(fields *views.DataSourceFormFields) {
	if fields == nil {
		return
	}
	fields.AllowPrivateDataSourceHosts = r.allowPrivateDataSourceHosts
	if !r.allowPrivateDataSourceHosts {
		fields.HTTPAllowPrivateHosts = false
		fields.GitAllowPrivateHosts = false
		fields.DatabaseAllowPrivateHosts = false
	}
}

func dataSourceCreateFormDataFromRequest(c fiber.Ctx) views.DataSourceCreateFormData {
	typeInput := strings.TrimSpace(c.FormValue("type"))

	gitAuthType := strings.TrimSpace(c.FormValue("git_auth_type"))
	if gitAuthType == "" {
		gitAuthType = gitAuthTypeHTTPS
	}
	hubSpotAuthType := strings.TrimSpace(c.FormValue("hubspot_auth_type"))
	if hubSpotAuthType != "private_app" {
		hubSpotAuthType = "oauth2"
	}

	snowflakeAuthType := strings.TrimSpace(c.FormValue("snowflake_auth_type"))
	if snowflakeAuthType != snowflakeAuthTypePassword {
		snowflakeAuthType = snowflakeAuthTypeWorkloadIdentity
	}

	return views.DataSourceCreateFormData{
		HTTPAuth:                  httpAuthFormDataFromRequest(c),
		Name:                      strings.TrimSpace(c.FormValue("name")),
		Slug:                      strings.TrimSpace(c.FormValue("slug")),
		Type:                      typeInput,
		GitAuthType:               gitAuthType,
		GitLabProjectID:           strings.TrimSpace(c.FormValue("gitlab_project_id")),
		GitLabReadOnly:            strings.TrimSpace(c.FormValue(gitLabAccessModeField)) != gitLabAccessModeWrite,
		GitHubRepositoryID:        strings.TrimSpace(c.FormValue("github_repository_id")),
		GitHubReadOnly:            strings.TrimSpace(c.FormValue("github_access_mode")) != "write",
		HubSpotAuthType:           hubSpotAuthType,
		BigQueryReadOnly:          strings.TrimSpace(c.FormValue("bigquery_access_mode")) != "write",
		HubSpotReadOnly:           strings.TrimSpace(c.FormValue(hubSpotAccessModeField)) != hubSpotAccessModeWrite,
		SnowflakeAuthType:         snowflakeAuthType,
		SnowflakeSelectTables:     c.FormValue("snowflake_select_tables"),
		SnowflakeInsertTables:     c.FormValue("snowflake_insert_tables"),
		Host:                      strings.TrimSpace(c.FormValue("host")),
		Port:                      strings.TrimSpace(c.FormValue("port")),
		Database:                  strings.TrimSpace(c.FormValue("database")),
		SSLMode:                   strings.TrimSpace(c.FormValue("sslmode")),
		Account:                   strings.TrimSpace(c.FormValue("account")),
		Warehouse:                 strings.TrimSpace(c.FormValue("warehouse")),
		Schema:                    strings.TrimSpace(c.FormValue("schema")),
		Role:                      strings.TrimSpace(c.FormValue("role")),
		ProjectID:                 strings.TrimSpace(c.FormValue("project_id")),
		Location:                  strings.TrimSpace(c.FormValue("location")),
		DefaultDataset:            strings.TrimSpace(c.FormValue("default_dataset")),
		MaximumBytesBilled:        strings.TrimSpace(c.FormValue("maximum_bytes_billed")),
		Username:                  strings.TrimSpace(c.FormValue("username")),
		GitUsername:               strings.TrimSpace(c.FormValue("git_username")),
		GitRepoURL:                strings.TrimSpace(c.FormValue("git_repo_url")),
		GitBasePath:               strings.TrimSpace(c.FormValue("git_base_path")),
		GitBranch:                 strings.TrimSpace(c.FormValue("git_default_branch")),
		GitKnownHosts:             strings.TrimSpace(c.FormValue("git_ssh_known_hosts")),
		GitAllowPrivateHosts:      parseCheckbox(c.FormValue("git_allow_private_hosts")),
		DatabaseAllowPrivateHosts: parseCheckbox(c.FormValue(databaseAllowPrivateHostsField)),
		ConnectTimeout:            strings.TrimSpace(c.FormValue("connect_timeout")),
		AuthDatabase:              strings.TrimSpace(c.FormValue("auth_database")),
		S3Endpoint:                strings.TrimSpace(c.FormValue("endpoint")),
		S3Region:                  strings.TrimSpace(c.FormValue("region")),
		S3Bucket:                  strings.TrimSpace(c.FormValue("bucket")),
		S3Prefix:                  strings.TrimSpace(c.FormValue("prefix")),
		S3ForcePathStyle:          parseCheckbox(c.FormValue("force_path_style")),
		S3AllowPrivateHosts:       parseCheckbox(c.FormValue("allow_private_hosts")),
		BaseURL:                   strings.TrimSpace(c.FormValue("base_url")),
		HealthPath:                strings.TrimSpace(c.FormValue("health_path")),
		TimeoutSeconds:            strings.TrimSpace(c.FormValue("timeout_seconds")),
		OpenAPIURL:                strings.TrimSpace(c.FormValue("openapi_url")),
		OpenAPIFetchTimeout:       strings.TrimSpace(c.FormValue("openapi_fetch_timeout_seconds")),
		HTTPAllowPrivateHosts:     parseCheckbox(c.FormValue("http_allow_private_hosts")),
		PrivateAppToken:           strings.TrimSpace(c.FormValue("private_app_token")),
		TestMessage:               "",
		TestSuccess:               false,
	}
}

// handleDataSourceNewPage renders the "Add data source" page.
func (r *Router) handleDataSourceNewPage(c fiber.Ctx) error {
	orgSlug := c.Params("slug")
	if _, err := slug.New(orgSlug); err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	fields := r.defaultDataSourceFormFields()
	fields.OrgSlug = orgSlug

	return renderTempl(
		c,
		views.DataSourceNewPage(
			"Add data source",
			orgSlug,
			fields,
			"", "", "",
		),
	)
}

// defaultDataSourceFormFields returns standalone create fields with deployment policy applied.
func (r *Router) defaultDataSourceFormFields() views.DataSourceFormFields {
	fields := views.DataSourceFormFields{}
	r.applyPrivateHostPolicyToFormFields(&fields)
	return fields
}

// handleDataSourceFieldsPartial is an HTMX endpoint that returns the type-specific
// fields partial when the type selector changes.
func (r *Router) handleDataSourceFieldsPartial(c fiber.Ctx) error {
	dsTypeStr := strings.TrimSpace(c.FormValue("type"))
	dsType, ok := normalizeDataSourceType(dsTypeStr)
	if !ok {
		fields := r.defaultDataSourceFormFields()
		fields.OrgSlug = c.Params("slug")
		return renderTempl(c, views.DataSourceFields(fields, ""))
	}
	fields := views.DataSourceFormFields{DSType: dsType, OrgSlug: c.Params("slug")}
	r.applyPrivateHostPolicyToFormFields(&fields)
	return renderTempl(c, views.DataSourceFields(fields, ""))
}

// handleDataSourceParseURI parses URI values and returns the filled field partial.
func (r *Router) handleDataSourceParseURI(c fiber.Ctx) error {
	dsTypeStr := strings.TrimSpace(c.FormValue("type"))
	uri := strings.TrimSpace(c.FormValue("uri"))

	if uri == "" {
		dsType, _ := normalizeDataSourceType(dsTypeStr)
		fields := views.DataSourceFormFields{DSType: dsType, OrgSlug: c.Params("slug")}
		r.applyPrivateHostPolicyToFormFields(&fields)
		return renderTempl(
			c,
			views.DataSourceFields(
				fields,
				"Paste a URI to parse.",
			),
		)
	}

	fields, parsedType, inferred, parseErr := parseURIWithTypeFallback(dsTypeStr, uri)
	if parseErr != nil {
		msg := "Could not parse URI: " + parseErr.Error()
		dsType, _ := normalizeDataSourceType(dsTypeStr)
		if dsType == "" {
			dsType = parsedType
		}
		fields := views.DataSourceFormFields{DSType: dsType, OrgSlug: c.Params("slug")}
		r.applyPrivateHostPolicyToFormFields(&fields)
		return renderTempl(
			c,
			views.DataSourceFields(fields, msg),
		)
	}

	msg := "URI parsed successfully - review the fields below."
	if inferred {
		msg = fmt.Sprintf(
			"URI parsed successfully. Type inferred from URI scheme as %q.",
			parsedType,
		)
	}

	r.applyPrivateHostPolicyToFormFields(&fields)
	fields.OrgSlug = c.Params("slug")
	return renderTempl(
		c,
		views.DataSourceFields(fields, msg),
	)
}

type dataSourceURIParseJSONResponse struct {
	Success bool              `json:"success"`
	Message string            `json:"message"`
	Type    string            `json:"type"`
	Fields  map[string]string `json:"fields"`
}

// handleCreateDataSourceParseURI parses URI values for the /orgs datasource page and returns JSON.
func (r *Router) handleCreateDataSourceParseURI(c fiber.Ctx) error {
	dsTypeStr := strings.TrimSpace(c.FormValue("type"))
	uri := strings.TrimSpace(c.FormValue("uri"))

	if uri == "" {
		dsType, _ := normalizeDataSourceType(dsTypeStr)
		return c.Status(fiber.StatusBadRequest).JSON(dataSourceURIParseJSONResponse{
			Success: false,
			Message: "Paste a URI to parse.",
			Type:    string(dsType),
		})
	}

	fields, parsedType, inferred, parseErr := parseURIWithTypeFallback(dsTypeStr, uri)
	if parseErr != nil {
		msg := "Could not parse URI: " + parseErr.Error()
		dsType, _ := normalizeDataSourceType(dsTypeStr)
		if dsType == "" {
			dsType = parsedType
		}
		return c.Status(fiber.StatusBadRequest).JSON(dataSourceURIParseJSONResponse{
			Success: false,
			Message: msg,
			Type:    string(dsType),
		})
	}

	msg := "URI parsed successfully - review the fields below."
	if inferred {
		msg = fmt.Sprintf(
			"URI parsed successfully. Type inferred from URI scheme as %q.",
			parsedType,
		)
	}

	r.applyPrivateHostPolicyToFormFields(&fields)
	return c.JSON(dataSourceURIParseJSONResponse{
		Success: true,
		Message: msg,
		Type:    string(parsedType),
		Fields:  dataSourceURIParseJSONFields(fields),
	})
}

// dataSourceURIParseJSONFields preserves the legacy /orgs parse response shape.
func dataSourceURIParseJSONFields(fields views.DataSourceFormFields) map[string]string {
	return map[string]string{
		"host":          fields.Host,
		"port":          fields.Port,
		"database":      fields.Database,
		"sslmode":       fields.SSLMode,
		"username":      fields.Username,
		"password":      fields.Password,
		"auth_database": fields.AuthDatabase,
		"base_url":      fields.BaseURL,
		"health_path":   fields.HealthPath,
		"bearer_token":  fields.BearerToken,
	}
}

func parseURIWithTypeFallback(
	dsTypeStr string,
	uri string,
) (views.DataSourceFormFields, api.DataSourceType, bool, error) {
	selectedType, selectedOK := normalizeDataSourceType(dsTypeStr)
	if selectedOK {
		fields, err := parseURIForType(selectedType, uri)
		if err == nil {
			return fields, selectedType, false, nil
		}

		if xerror.Is(err, dsparser.ErrUnsupportedScheme) {
			inferredType, inferOK := inferDataSourceTypeFromURI(uri)
			if inferOK && inferredType != selectedType {
				fields, inferErr := parseURIForType(inferredType, uri)
				if inferErr == nil {
					return fields, inferredType, true, nil
				}
				return views.DataSourceFormFields{}, inferredType, true, inferErr
			}
		}

		return views.DataSourceFormFields{}, selectedType, false, err
	}

	inferredType, inferOK := inferDataSourceTypeFromURI(uri)
	if !inferOK {
		return views.DataSourceFormFields{}, "", false, xerror.Unexpected().Detail(
			"unsupported URI scheme; select a datasource type or use a supported scheme",
		)
	}

	fields, err := parseURIForType(inferredType, uri)
	if err != nil {
		return views.DataSourceFormFields{}, inferredType, true, err
	}

	return fields, inferredType, true, nil
}

// parseURIForType dispatches to the correct parser based on datasource type.
func parseURIForType(dsType api.DataSourceType, uri string) (views.DataSourceFormFields, error) {
	switch dsType {
	case api.DataSourceTypePostgres:
		f, err := dsparser.ParsePostgres(uri)
		if err != nil {
			return views.DataSourceFormFields{DSType: api.DataSourceTypePostgres}, err
		}
		return views.DataSourceFormFields{
			DSType:   api.DataSourceTypePostgres,
			Host:     f.Host,
			Port:     portString(f.Port),
			Database: f.Database,
			Username: f.Username,
			Password: f.Password,
			SSLMode:  f.SSLMode,
		}, nil
	case api.DataSourceTypeMysql:
		f, err := dsparser.ParseMySQL(uri)
		if err != nil {
			return views.DataSourceFormFields{DSType: api.DataSourceTypeMysql}, err
		}
		return views.DataSourceFormFields{
			DSType:   api.DataSourceTypeMysql,
			Host:     f.Host,
			Port:     portString(f.Port),
			Database: f.Database,
			Username: f.Username,
			Password: f.Password,
		}, nil
	case api.DataSourceTypeMongodb:
		f, err := dsparser.ParseMongoDB(uri)
		if err != nil {
			return views.DataSourceFormFields{DSType: api.DataSourceTypeMongodb}, err
		}
		return views.DataSourceFormFields{
			DSType:       api.DataSourceTypeMongodb,
			Host:         f.Host,
			Port:         portString(f.Port),
			Database:     f.Database,
			Username:     f.Username,
			Password:     f.Password,
			AuthDatabase: f.AuthDatabase,
		}, nil
	case api.DataSourceTypeHttp:
		f, err := dsparser.ParseHTTP(uri)
		if err != nil {
			return views.DataSourceFormFields{DSType: api.DataSourceTypeHttp}, err
		}
		return views.DataSourceFormFields{
			DSType:      api.DataSourceTypeHttp,
			BaseURL:     f.BaseURL,
			HealthPath:  f.HealthPath,
			BearerToken: f.BearerToken,
		}, nil
	default:
		return views.DataSourceFormFields{DSType: dsType},
			xerror.Unexpected().Detailf("unsupported datasource type: %q", dsType)
	}
}

func portString(port int) string {
	if port == 0 {
		return ""
	}
	return strconv.Itoa(port)
}

// handleDataSourceCreateSubmit processes the new datasource creation form.
func (r *Router) handleDataSourceCreateSubmit(c fiber.Ctx) error {
	orgSlug := c.Params("slug")
	if _, err := slug.New(orgSlug); err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	name := strings.TrimSpace(c.FormValue("name"))
	dsSlug := strings.TrimSpace(c.FormValue("slug"))
	dsTypeStr := strings.TrimSpace(c.FormValue("type"))

	if name == "" {
		c.Status(fiber.StatusBadRequest)
		return renderTempl(c, views.DataSourceNewPage(
			"Add data source", orgSlug,
			r.collectFormFields(c), "", "", "Name is required.",
		))
	}

	dsType, ok := normalizeDataSourceType(dsTypeStr)
	if !ok {
		c.Status(fiber.StatusBadRequest)
		return renderTempl(c, views.DataSourceNewPage(
			"Add data source", orgSlug,
			r.collectFormFields(c), "", "", "Select a datasource type.",
		))
	}
	if dsType == api.DataSourceTypeBigquery || dsType == api.DataSourceTypeS3 {
		query := url.Values{"type": {string(dsType)}, "name": {name}}
		if dsSlug != "" {
			query.Set("slug", dsSlug)
		}
		return redirectHTMXOrSeeOther(c, "/orgs/"+orgSlug+"/data-sources?"+query.Encode())
	}
	req, buildErr := buildCreateDataSourceRequest(
		name,
		dsSlug,
		dsType,
		c,
		r.allowPrivateDataSourceHosts,
	)
	if buildErr != nil {
		c.Status(fiber.StatusBadRequest)
		return renderTempl(c, views.DataSourceNewPage(
			"Add data source", orgSlug,
			r.collectFormFields(c), "", "", buildErr.Error(),
		))
	}

	created, status, errResp, err := r.createDataSourceViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug,
		req,
	)
	if err != nil {
		if status == fiber.StatusUnauthorized || status == fiber.StatusForbidden {
			return redirectToLogin(c, c.OriginalURL())
		}

		if status < 400 || status >= 600 {
			status = fiber.StatusInternalServerError
		}

		c.Status(status)
		msg := messageFromErrorResponse(errResp)
		if msg == "" {
			msg = fmt.Sprintf("Failed to create data source (status %d).", status)
		}
		r.logRenderedBackendAPIFailure(c, "create data source", errResp, err, orgSlug, "")

		return renderTempl(c, views.DataSourceNewPage(
			"Add data source", orgSlug,
			r.collectFormFields(c), "", "", msg,
		))
	}

	if created != nil && views.UsesHTTPOAuth(*created) {
		return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(views.DataSourceDetailHref(orgSlug, dataSourceActiveEnvironmentSlug(c), created.Slug), "section", "settings"))
	}
	if created != nil {
		return redirectHTMXOrSeeOther(c, views.DataSourceDetailHref(orgSlug, dataSourceActiveEnvironmentSlug(c), created.Slug))
	}
	return redirectHTMXOrSeeOther(c, "/dashboard")
}

// buildCreateDataSourceRequest builds the API create payload from form values.
func buildCreateDataSourceRequest(
	name, dsSlugStr string, dsType api.DataSourceType,
	c fiber.Ctx,
	allowPrivateDataSourceHosts bool,
) (api.CreateDataSourceRequest, error) {
	var req api.CreateDataSourceRequest
	var apiSlugValue *api.Slug
	if dsSlugStr != "" {
		value := dsSlugStr
		apiSlugValue = &value
	}

	switch dsType {
	case api.DataSourceTypePostgres:
		cfg, creds, errMsg := parseSQLFormWithPrivateHosts(c, 5432, allowPrivateDataSourceHosts)
		if errMsg != "" {
			return req, xerror.Unexpected().Detail(errMsg)
		}
		if err := req.FromCreatePostgresDataSourceRequest(api.CreatePostgresDataSourceRequest{
			Name:        name,
			Slug:        apiSlugValue,
			Config:      cfg,
			Credentials: creds,
		}); err != nil {
			return req, xerror.Unexpected().With(err)
		}
	case api.DataSourceTypeSnowflake:
		cfg, creds, errMsg := parseSnowflakeForm(c)
		if errMsg != "" {
			return req, xerror.Unexpected().Detail(errMsg)
		}
		if err := req.FromCreateSnowflakeDataSourceRequest(api.CreateSnowflakeDataSourceRequest{
			Name: name, Slug: apiSlugValue, Config: cfg, Credentials: creds, ReadOnly: snowflakeReadOnly(cfg),
		}); err != nil {
			return req, xerror.Unexpected().With(err)
		}
	case api.DataSourceTypeMysql:
		cfg, creds, errMsg := parseMySQLFormWithPrivateHosts(c, 3306, allowPrivateDataSourceHosts)
		if errMsg != "" {
			return req, xerror.Unexpected().Detail(errMsg)
		}
		if err := req.FromCreateMySQLDataSourceRequest(api.CreateMySQLDataSourceRequest{
			Name:        name,
			Slug:        apiSlugValue,
			Config:      cfg,
			Credentials: creds,
		}); err != nil {
			return req, xerror.Unexpected().With(err)
		}
	case api.DataSourceTypeBigquery:
		return req, xerror.InvalidInput(xerror.DataSource).Detail("BigQuery data sources must use Google OAuth.")
	case api.DataSourceTypeMongodb:
		cfg, creds, errMsg := parseMongoFormWithPrivateHosts(c, 27017, allowPrivateDataSourceHosts)
		if errMsg != "" {
			return req, xerror.Unexpected().Detail(errMsg)
		}
		if err := req.FromCreateMongoDBDataSourceRequest(api.CreateMongoDBDataSourceRequest{
			Name:        name,
			Slug:        apiSlugValue,
			Config:      cfg,
			Credentials: creds,
		}); err != nil {
			return req, xerror.Unexpected().With(err)
		}
	case api.DataSourceTypeHttp:
		cfg, creds, errMsg := parseHTTPFormWithPrivateHosts(c, allowPrivateDataSourceHosts)
		if errMsg != "" {
			return req, xerror.Unexpected().Detail(errMsg)
		}
		if err := req.FromCreateHTTPDataSourceRequest(api.CreateHTTPDataSourceRequest{
			Name:        name,
			Slug:        apiSlugValue,
			Config:      cfg,
			Credentials: creds,
		}); err != nil {
			return req, xerror.Unexpected().With(err)
		}
	case api.DataSourceTypeHubspot:
		cfg, creds, errMsg := parseHubSpotForm(c)
		if errMsg != "" {
			return req, xerror.Unexpected().Detail(errMsg)
		}
		readOnly := strings.TrimSpace(c.FormValue(hubSpotAccessModeField)) != hubSpotAccessModeWrite
		if err := req.FromCreateHubSpotDataSourceRequest(api.CreateHubSpotDataSourceRequest{
			Name:        name,
			Slug:        apiSlugValue,
			ReadOnly:    &readOnly,
			Config:      cfg,
			Credentials: creds,
		}); err != nil {
			return req, xerror.Unexpected().With(err)
		}
	case api.DataSourceTypeGit:
		cfg, creds, errMsg := parseGitFormWithPrivateHosts(c, allowPrivateDataSourceHosts)
		if errMsg != "" {
			return req, xerror.Unexpected().Detail(errMsg)
		}
		if err := req.FromCreateGitDataSourceRequest(api.CreateGitDataSourceRequest{
			Name:        name,
			Slug:        apiSlugValue,
			Config:      cfg,
			Credentials: creds,
		}); err != nil {
			return req, xerror.Unexpected().With(err)
		}
	case api.DataSourceTypeGithub:
		repositoryID := strings.TrimSpace(c.FormValue("github_repository_id"))
		if repositoryID == "" {
			return req, xerror.InvalidRequired(xerror.DataSource).Detail("select a GitHub repository")
		}
		cfg := api.GitHubRepositoryConfig{RepositoryId: repositoryID}
		if basePath := strings.TrimSpace(c.FormValue("git_base_path")); basePath != "" {
			cfg.BasePath = &basePath
		}
		readOnly := strings.TrimSpace(c.FormValue("github_access_mode")) != "write"
		if err := req.FromCreateGitHubDataSourceRequest(api.CreateGitHubDataSourceRequest{
			Name: name, Slug: apiSlugValue, Config: cfg, ReadOnly: &readOnly,
		}); err != nil {
			return req, xerror.Unexpected().With(err)
		}
	case api.DataSourceTypeGitlab:
		projectID := strings.TrimSpace(c.FormValue("gitlab_project_id"))
		if projectID == "" {
			return req, xerror.InvalidRequired(xerror.DataSource).Detail("GitLab project ID is required")
		}
		cfg := api.GitLabRepositoryConfig{ProjectId: projectID}
		if basePath := strings.TrimSpace(c.FormValue("git_base_path")); basePath != "" {
			cfg.BasePath = &basePath
		}
		readOnly := strings.TrimSpace(c.FormValue(gitLabAccessModeField)) != gitLabAccessModeWrite
		if err := req.FromCreateGitLabDataSourceRequest(api.CreateGitLabDataSourceRequest{
			Name:     name,
			Slug:     apiSlugValue,
			Config:   cfg,
			ReadOnly: &readOnly,
		}); err != nil {
			return req, xerror.Unexpected().With(err)
		}
	default:
		return req, xerror.Unexpected().Detailf("unsupported datasource type: %q", dsType)
	}

	return req, nil
}

// normalizeDataSourceType normalizes user input to a canonical api.DataSourceType.
// Handles "postgresql" → "postgres" for URI scheme compatibility.
func normalizeDataSourceType(t string) (api.DataSourceType, bool) {
	t = strings.TrimSpace(t)

	// Normalize postgresql to postgres since both schemes are accepted in URIs
	if t == "postgresql" {
		t = dataSourceTypePostgresString
	}

	switch api.DataSourceType(t) {
	case api.DataSourceTypeS3, api.DataSourceTypePostgres,
		api.DataSourceTypeMysql,
		api.DataSourceTypeBigquery,
		api.DataSourceTypeMongodb,
		api.DataSourceTypeHttp,
		api.DataSourceTypeHubspot,
		api.DataSourceTypeGit,
		api.DataSourceTypeGithub,
		api.DataSourceTypeGitlab:
		return api.DataSourceType(t), true
	default:
		return "", false
	}
}

func inferDataSourceTypeFromURI(rawURI string) (api.DataSourceType, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil {
		return "", false
	}

	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	switch scheme {
	case dataSourceTypePostgresString, "postgresql":
		return api.DataSourceTypePostgres, true
	case "mysql":
		return api.DataSourceTypeMysql, true
	case "mongodb", "mongodb+srv":
		return api.DataSourceTypeMongodb, true
	case "http", "https":
		return api.DataSourceTypeHttp, true
	default:
		return "", false
	}
}

// collectFormFields keeps submitted standalone create fields with deployment policy applied.
func (r *Router) collectFormFields(c fiber.Ctx) views.DataSourceFormFields {
	fields := collectFormFields(c)
	r.applyPrivateHostPolicyToFormFields(&fields)
	return fields
}

func collectFormFields(c fiber.Ctx) views.DataSourceFormFields {
	typeStr := strings.TrimSpace(c.FormValue("type"))
	dsType, _ := normalizeDataSourceType(
		typeStr,
	) // Use normalized type, invalid types become empty string

	return views.DataSourceFormFields{
		HTTPAuth:                  httpAuthFormDataFromRequest(c),
		Name:                      strings.TrimSpace(c.FormValue("name")),
		Slug:                      strings.TrimSpace(c.FormValue("slug")),
		DSType:                    dsType,
		Host:                      strings.TrimSpace(c.FormValue("host")),
		Port:                      strings.TrimSpace(c.FormValue("port")),
		Database:                  strings.TrimSpace(c.FormValue("database")),
		Account:                   strings.TrimSpace(c.FormValue("account")),
		Warehouse:                 strings.TrimSpace(c.FormValue("warehouse")),
		Schema:                    strings.TrimSpace(c.FormValue("schema")),
		Role:                      strings.TrimSpace(c.FormValue("role")),
		SnowflakeAuthType:         strings.TrimSpace(c.FormValue("snowflake_auth_type")),
		SnowflakeSelectTables:     c.FormValue("snowflake_select_tables"),
		SnowflakeInsertTables:     c.FormValue("snowflake_insert_tables"),
		ProjectID:                 strings.TrimSpace(c.FormValue("project_id")),
		Location:                  strings.TrimSpace(c.FormValue("location")),
		DefaultDataset:            strings.TrimSpace(c.FormValue("default_dataset")),
		MaximumBytesBilled:        strings.TrimSpace(c.FormValue("maximum_bytes_billed")),
		Username:                  strings.TrimSpace(c.FormValue("username")),
		Password:                  strings.TrimSpace(c.FormValue("password")),
		SSLMode:                   strings.TrimSpace(c.FormValue("sslmode")),
		AuthDatabase:              strings.TrimSpace(c.FormValue("auth_database")),
		BaseURL:                   strings.TrimSpace(c.FormValue("base_url")),
		HealthPath:                strings.TrimSpace(c.FormValue("health_path")),
		OpenAPIURL:                strings.TrimSpace(c.FormValue("openapi_url")),
		OpenAPIFetchTimeout:       strings.TrimSpace(c.FormValue("openapi_fetch_timeout_seconds")),
		BearerToken:               strings.TrimSpace(c.FormValue("bearer_token")),
		TimeoutSeconds:            strings.TrimSpace(c.FormValue("timeout_seconds")),
		HTTPAllowPrivateHosts:     parseCheckbox(c.FormValue("http_allow_private_hosts")),
		PrivateAppToken:           strings.TrimSpace(c.FormValue("private_app_token")),
		HubSpotReadOnly:           strings.TrimSpace(c.FormValue(hubSpotAccessModeField)) != hubSpotAccessModeWrite,
		GitRepoURL:                strings.TrimSpace(c.FormValue("git_repo_url")),
		GitDefaultBranch:          strings.TrimSpace(c.FormValue("git_default_branch")),
		GitBasePath:               strings.TrimSpace(c.FormValue("git_base_path")),
		GitAuthType:               strings.TrimSpace(c.FormValue("git_auth_type")),
		GitUsername:               strings.TrimSpace(c.FormValue("git_username")),
		GitSSHKnownHosts:          strings.TrimSpace(c.FormValue("git_ssh_known_hosts")),
		GitAllowPrivateHosts:      parseCheckbox(c.FormValue("git_allow_private_hosts")),
		DatabaseAllowPrivateHosts: parseCheckbox(c.FormValue(databaseAllowPrivateHostsField)),
	}
}

func snowflakeReadOnly(config api.SnowflakeConfig) *bool {
	if config.WifObjects == nil {
		return nil
	}
	readOnly := true
	for _, object := range *config.WifObjects {
		if object.Privilege == api.INSERT {
			readOnly = false
		}
	}
	return &readOnly
}

func httpAuthFormDataFromRequest(c fiber.Ctx) views.HTTPAuthFormData {
	return views.HTTPAuthFormData{
		Mode:                  strings.TrimSpace(c.FormValue("http_auth_type")),
		Issuer:                strings.TrimSpace(c.FormValue("http_oauth_issuer")),
		AuthorizationEndpoint: strings.TrimSpace(c.FormValue("http_oauth_authorization_endpoint")),
		TokenEndpoint:         strings.TrimSpace(c.FormValue("http_oauth_token_endpoint")),
		RevocationEndpoint:    strings.TrimSpace(c.FormValue("http_oauth_revocation_endpoint")),
		ClientID:              strings.TrimSpace(c.FormValue("http_oauth_client_id")),
		Scopes:                strings.TrimSpace(c.FormValue("http_oauth_scopes")),
		Audience:              strings.TrimSpace(c.FormValue("http_oauth_audience")),
	}
}

func (r *Router) handleConnectHTTPOAuthSubmit(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	source, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	response, err := r.apiClient.ConnectDataSourceHTTPOAuthWithResponse(c.Context(), org.String(), source.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(dataSourceReturnURL(c, org.String(), source.String()), "error", "Could not connect the HTTP API. Try again."))
	}
	if response.StatusCode() == http.StatusUnauthorized {
		return redirectToLogin(c, c.OriginalURL())
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		errorResponse := parseAPIErrorResponse(response.Body)
		message := formFeedbackFromErrorResponse(errorResponse, "Could not connect the HTTP API. Check the app settings and try again.")
		return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(dataSourceReturnURL(c, org.String(), source.String()), "error", message))
	}
	if response.JSON200.Install != nil {
		return redirectHTMXOrSeeOther(c, response.JSON200.Install.AuthorizationUrl)
	}
	return redirectHTMXOrSeeOther(c, appendDataSourceReturnQuery(dataSourceReturnURL(c, org.String(), source.String()), "provider_oauth", "success"))
}
