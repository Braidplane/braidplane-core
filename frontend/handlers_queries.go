package frontend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	workspace "github.com/Braidplane/braidplane-core/pkg/connector/googleworkspace"
	"github.com/Braidplane/braidplane-core/pkg/sinkbinding"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	httpPathRequiredMessage       = "HTTP path is required."
	queryStatementRequiredMessage = "Query statement is required."
)

func (r *Router) handleQueriesPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	return r.renderQueriesPage(
		c,
		orgSlug,
		strings.TrimSpace(c.Query("ds")),
		http.StatusOK,
		"",
		"",
		views.DefaultQueryEditorFormData(),
		nil,
		"",
	)
}

func (r *Router) handleQueryDetailPage(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	dataSourceSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}

	querySlug, err := slug.New(c.Params("querySlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid query slug")
	}

	return r.renderQueryDetailPage(
		c,
		orgSlug,
		dataSourceSlug,
		querySlug,
		http.StatusOK,
		"",
		"",
		nil,
		nil,
		"",
		"",
	)
}

func (r *Router) handleCreateQuerySubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	selectedDataSource := strings.TrimSpace(c.FormValue("ds"))
	if selectedDataSource == "" {
		return r.renderQueriesPage(
			c,
			orgSlug,
			"",
			http.StatusBadRequest,
			"Select a data source before creating a query.",
			"",
			queryEditorFormDataFromRequest(c),
			nil,
			"",
		)
	}

	dataSource, status, errorResponse, getErr := r.getDataSourceViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		selectedDataSource,
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		pageError := messageFromErrorResponse(errorResponse)
		if pageError == "" {
			pageError = "Something went wrong while loading the selected data source."
		}

		return r.renderQueriesPage(
			c,
			orgSlug,
			selectedDataSource,
			status,
			pageError,
			errorPopupDetails(errorResponse, fmt.Sprintf("get data source: %v", getErr)),
			queryEditorFormDataFromRequest(c),
			nil,
			"",
		)
	}

	payload, validationErr := buildCreateQueryPayload(c, dataSource.Type)
	if validationErr != "" {
		return r.renderQueriesPage(
			c,
			orgSlug,
			selectedDataSource,
			http.StatusBadRequest,
			validationErr,
			"",
			queryEditorFormDataFromRequest(c),
			nil,
			"",
		)
	}

	created, createStatus, createErrorResponse, createErr := r.createQueryViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		selectedDataSource,
		payload,
	)
	if createErr != nil {
		if createStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		pageError := messageFromErrorResponse(createErrorResponse)
		if pageError == "" {
			pageError = "Something went wrong while creating the query."
		}

		return r.renderQueriesPage(
			c,
			orgSlug,
			selectedDataSource,
			createStatus,
			pageError,
			errorPopupDetails(createErrorResponse, fmt.Sprintf("create query: %v", createErr)),
			queryEditorFormDataFromRequest(c),
			nil,
			"",
		)
	}

	_ = created
	return redirectHTMXOrSeeOther(
		c,
		views.QueriesListHref(orgSlug.String(), selectedDataSource, "", false),
	)
}

func (r *Router) handleUpdateQuerySubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	dataSourceSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}

	querySlug, err := slug.New(c.Params("querySlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid query slug")
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
			message = "Something went wrong while loading the selected data source."
		}
		if queryShouldRenderSidePanel(c) {
			formData := queryEditorFormDataFromRequest(c)
			return r.renderQuerySidePanel(
				c,
				orgSlug,
				dataSourceSlug,
				querySlug,
				status,
				message,
				"error",
				errorPopupDetails(errorResponse, fmt.Sprintf("get data source: %v", getErr)),
				&formData,
				nil,
				"",
				"",
			)
		}

		formData := queryEditorFormDataFromRequest(c)
		return r.renderQueryDetailPage(
			c,
			orgSlug,
			dataSourceSlug,
			querySlug,
			status,
			message,
			errorPopupDetails(errorResponse, fmt.Sprintf("get data source: %v", getErr)),
			&formData,
			nil,
			"",
			"",
		)
	}

	payload, validationErr := buildUpdateQueryPayload(c, dataSource.Type)
	if validationErr != "" {
		formData := queryEditorFormDataFromRequest(c)
		if queryShouldRenderSidePanel(c) {
			return r.renderQuerySidePanel(
				c,
				orgSlug,
				dataSourceSlug,
				querySlug,
				http.StatusBadRequest,
				validationErr,
				"error",
				"",
				&formData,
				nil,
				"",
				"",
			)
		}
		return r.renderQueryDetailPage(
			c,
			orgSlug,
			dataSourceSlug,
			querySlug,
			http.StatusBadRequest,
			validationErr,
			"",
			&formData,
			nil,
			"",
			"",
		)
	}

	// The editor owns statements and parameters; preserve API-authored contracts
	// until the dedicated attachment editor owns their replacement.
	existing, updateStatus, updateErrorResponse, updateErr := r.getQueryViaAPI(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), dataSourceSlug.String(), querySlug.String())
	var updated *api.Query
	if updateErr == nil {
		payload.ExpectedInputContract, payload.ExpectedInputContractRef = existing.ExpectedInputContract, existing.ExpectedInputContractRef
		payload.OutputContract, payload.OutputContractRef = existing.OutputContract, existing.OutputContractRef
		updated, updateStatus, updateErrorResponse, updateErr = r.updateQueryViaAPI(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), dataSourceSlug.String(), querySlug.String(), payload)
	}

	if updateErr != nil {
		if updateStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(updateErrorResponse)
		if message == "" {
			message = "Something went wrong while updating the query."
		}

		formData := queryEditorFormDataFromRequest(c)
		if queryShouldRenderSidePanel(c) {
			return r.renderQuerySidePanel(
				c,
				orgSlug,
				dataSourceSlug,
				querySlug,
				updateStatus,
				message,
				"error",
				errorPopupDetails(updateErrorResponse, fmt.Sprintf("update query: %v", updateErr)),
				&formData,
				nil,
				"",
				"",
			)
		}
		return r.renderQueryDetailPage(
			c,
			orgSlug,
			dataSourceSlug,
			querySlug,
			updateStatus,
			message,
			errorPopupDetails(updateErrorResponse, fmt.Sprintf("update query: %v", updateErr)),
			&formData,
			nil,
			"",
			"",
		)
	}

	if queryShouldRenderSidePanel(c) {
		formData := views.QueryEditorFormDataFromQuery(*updated)
		return r.renderQuerySidePanel(
			c,
			orgSlug,
			dataSourceSlug,
			querySlug,
			http.StatusOK,
			"Query saved.",
			"success",
			"",
			&formData,
			updated,
			"",
			"",
		)
	}

	return redirectHTMXOrSeeOther(
		c,
		queryUpdateReturnURL(c, orgSlug.String(), dataSourceSlug.String(), querySlug.String()),
	)
}

func (r *Router) handleDeleteQuerySubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	dataSourceSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}

	querySlug, err := slug.New(c.Params("querySlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid query slug")
	}

	deleteStatus, deleteErrorResponse, deleteErr := r.deleteQueryViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
		querySlug.String(),
	)
	if deleteErr != nil {
		if deleteStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(deleteErrorResponse)
		if message == "" {
			message = "Something went wrong while deleting the query."
		}

		if queryShouldRenderSidePanel(c) {
			return r.renderQuerySidePanel(
				c,
				orgSlug,
				dataSourceSlug,
				querySlug,
				deleteStatus,
				message,
				"error",
				errorPopupDetails(deleteErrorResponse, fmt.Sprintf("delete query: %v", deleteErr)),
				nil,
				nil,
				"",
				"",
			)
		}

		return r.renderQueryDetailPage(
			c,
			orgSlug,
			dataSourceSlug,
			querySlug,
			deleteStatus,
			message,
			errorPopupDetails(deleteErrorResponse, fmt.Sprintf("delete query: %v", deleteErr)),
			nil,
			nil,
			"",
			"",
		)
	}

	return redirectHTMXOrSeeOther(
		c,
		"/orgs/"+orgSlug.String()+"/queries?ds="+dataSourceSlug.String()+"&focus="+querySlug.String(),
	)
}

func (r *Router) handleRestoreQuerySubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	dataSourceSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}

	querySlug, err := slug.New(c.Params("querySlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid query slug")
	}

	restoreStatus, restoreErrorResponse, restoreErr := r.restoreQueryViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
		querySlug.String(),
	)
	if restoreErr != nil {
		if restoreStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(restoreErrorResponse)
		if message == "" {
			message = "Something went wrong while restoring the query."
		}

		return r.renderQueriesPage(
			c,
			orgSlug,
			dataSourceSlug.String(),
			restoreStatus,
			message,
			errorPopupDetails(restoreErrorResponse, fmt.Sprintf("restore query: %v", restoreErr)),
			views.DefaultQueryEditorFormData(),
			nil,
			"",
		)
	}

	return redirectHTMXOrSeeOther(
		c,
		"/orgs/"+orgSlug.String()+"/queries?ds="+dataSourceSlug.String()+"&focus="+querySlug.String(),
	)
}

func (r *Router) handlePurgeQuerySubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	dataSourceSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}

	querySlug, err := slug.New(c.Params("querySlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid query slug")
	}

	purgeStatus, purgeErrorResponse, purgeErr := r.purgeQueryViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
		querySlug.String(),
	)
	if purgeErr != nil {
		if purgeStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(purgeErrorResponse)
		if message == "" {
			message = "Something went wrong while purging the query."
		}

		return r.renderQueriesPage(
			c,
			orgSlug,
			dataSourceSlug.String(),
			purgeStatus,
			message,
			errorPopupDetails(purgeErrorResponse, fmt.Sprintf("purge query: %v", purgeErr)),
			views.DefaultQueryEditorFormData(),
			nil,
			"",
		)
	}

	return redirectHTMXOrSeeOther(
		c,
		"/orgs/"+orgSlug.String()+"/queries?ds="+dataSourceSlug.String()+"&deleted=true",
	)
}

func (r *Router) handleExecuteQuerySubmit(c fiber.Ctx) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	dataSourceSlug, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid data source slug")
	}

	querySlug, err := slug.New(c.Params("querySlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid query slug")
	}

	query, queryStatus, queryErrorResponse, queryErr := r.getQueryViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
		querySlug.String(),
	)
	if queryErr != nil {
		if queryStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(queryErrorResponse)
		if message == "" {
			message = "Something went wrong while loading query details for execution."
		}

		if isHTMXRequest(c) {
			setHTMLResponseStatus(c, http.StatusOK)
			return renderTempl(
				c,
				views.QueryExecutionFeedbackSlot(
					nil,
					message,
					errorPopupDetails(queryErrorResponse, fmt.Sprintf("get query: %v", queryErr)),
				),
			)
		}

		return r.renderQueriesPage(
			c,
			orgSlug,
			dataSourceSlug.String(),
			queryStatus,
			message,
			errorPopupDetails(queryErrorResponse, fmt.Sprintf("get query: %v", queryErr)),
			views.DefaultQueryEditorFormData(),
			nil,
			"",
		)
	}

	payload, validationErr := buildExecuteQueryPayload(c, *query)
	if validationErr != "" {
		status := http.StatusBadRequest
		if isHTMXRequest(c) {
			status = http.StatusOK
		}
		return r.renderQueryDetailPage(
			c,
			orgSlug,
			dataSourceSlug,
			querySlug,
			status,
			"",
			"",
			nil,
			nil,
			validationErr,
			"",
		)
	}

	result, executeStatus, executeErrorResponse, executeErr := r.executeQueryViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
		querySlug.String(),
		query.TimeoutSeconds,
		payload,
	)
	if executeErr != nil {
		if executeStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(executeErrorResponse)
		if message == "" {
			message = "Query execution failed."
		}

		status := executeStatus
		if isHTMXRequest(c) {
			status = http.StatusOK
		}
		return r.renderQueryDetailPage(
			c,
			orgSlug,
			dataSourceSlug,
			querySlug,
			status,
			"",
			"",
			nil,
			nil,
			message,
			backendErrorDetails(
				"execute query",
				executeStatus,
				executeErrorResponse,
				executeErr,
			),
		)
	}

	executionView := queryExecutionResultToView(querySlug.String(), *result)
	return r.renderQueryDetailPage(
		c,
		orgSlug,
		dataSourceSlug,
		querySlug,
		http.StatusOK,
		"",
		"",
		nil,
		executionView,
		"",
		"",
	)
}

func (r *Router) renderQueryDetailPage(
	c fiber.Ctx,
	orgSlug slug.Slug,
	dataSourceSlug slug.Slug,
	querySlug slug.Slug,
	status int,
	pageError string,
	pageErrorDetails string,
	editFormData *views.QueryEditorFormData,
	executionView *views.QueryExecutionResultView,
	executionError string,
	executionErrorDetails string,
) error {
	query, queryStatus, queryErrorResponse, queryErr := r.getQueryViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dataSourceSlug.String(),
		querySlug.String(),
	)
	if queryErr != nil {
		if queryStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(queryErrorResponse)
		if message == "" {
			message = "Something went wrong while loading query details."
		}

		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Query details",
			orgSlug.String(),
			message,
			errorPopupDetails(queryErrorResponse, fmt.Sprintf("get query: %v", queryErr)),
			c.Get(fiber.HeaderCookie),
		)
		shellData.Subtitle = "This route stays in place even when the query cannot be displayed."
		shellData.Breadcrumbs = buildOrganizationDetailBreadcrumbs(
			shellData.ActiveOrganizationName,
			orgSlug.String(),
			"Queries",
			views.QueriesListHref(orgSlug.String(), dataSourceSlug.String(), "", false),
			querySlug.String(),
		)

		c.Status(queryStatus)
		return renderTempl(
			c,
			views.QueryDetailUnavailablePage(
				shellData,
				orgSlug.String(),
				dataSourceSlug.String(),
				querySlug.String(),
				message,
			),
		)
	}

	if editFormData == nil {
		data := views.QueryEditorFormDataFromQuery(*query)
		editFormData = &data
	}

	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Query details",
		orgSlug.String(),
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Edit and execute in one focused view."
	shellData.Breadcrumbs = buildOrganizationDetailBreadcrumbs(
		shellData.ActiveOrganizationName,
		orgSlug.String(),
		"Queries",
		views.QueriesListHref(orgSlug.String(), dataSourceSlug.String(), "", false),
		query.Name,
	)

	setHTMLResponseStatus(c, status)
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	return renderTempl(
		c,
		views.QueryDetailPage(
			shellData,
			orgSlug.String(),
			dataSourceSlug.String(),
			*query,
			*editFormData,
			executionView,
			executionError,
			executionErrorDetails,
			permissions,
		),
	)
}

func (r *Router) renderQueriesPage(
	c fiber.Ctx,
	orgSlug slug.Slug,
	selectedDataSource string,
	status int,
	pageError string,
	pageErrorDetails string,
	createFormData views.QueryEditorFormData,
	executionView *views.QueryExecutionResultView,
	executionError string,
) error {
	if createFormData.RowLimit == "" {
		createFormData = views.DefaultQueryEditorFormData()
	}

	if selectedDataSource == "" {
		selectedDataSource = strings.TrimSpace(c.Query("ds"))
	}
	if queryError := strings.TrimSpace(c.Query("error")); queryError != "" && pageError == "" {
		pageError = queryError
	}
	showDeleted := strings.EqualFold(strings.TrimSpace(c.Query("deleted")), "true")

	focusQuerySlug := strings.TrimSpace(c.Query("focus"))
	if executionView != nil && executionView.QuerySlug != "" {
		focusQuerySlug = executionView.QuerySlug
	}
	selectedQuerySlug := strings.TrimSpace(c.Query("query"))

	dsParams := &api.ListDataSourcesParams{}
	dsLimit := api.LimitParam(100)
	dsParams.Limit = &dsLimit

	dsList, dsStatus, dsErrorResponse, dsErr := r.listDataSourcesViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		dsParams,
	)
	if dsErr != nil {
		if dsStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		if pageError == "" {
			message := messageFromErrorResponse(dsErrorResponse)
			if message == "" {
				message = "Something went wrong while loading data sources for queries."
			}
			pageError = message
		}

		pageErrorDetails = mergeErrorDetails(
			pageErrorDetails,
			errorPopupDetails(dsErrorResponse, fmt.Sprintf("list data sources: %v", dsErr)),
		)

		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Queries",
			orgSlug.String(),
			pageError,
			pageErrorDetails,
			c.Get(fiber.HeaderCookie),
		)
		shellData.Subtitle = "Reusable connector operations for source reads, sink payloads, validation, and direct execution."
		setHTMLResponseStatus(c, dsStatus)
		permissions := r.resolveOrganizationPermissions(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
		)
		return renderTempl(
			c,
			views.QueriesPage(
				shellData,
				nil,
				selectedDataSource,
				"",
				[]api.Query{},
				nil,
				views.QueryEditorFormData{},
				false,
				"",
				"",
				orgSlug.String(),
				createFormData,
				nil,
				executionError,
				focusQuerySlug,
				showDeleted,
				permissions,
			),
		)
	}

	queryableDataSources := filterQueryableDataSources(dsList.DataSources)

	if selectedDataSource != "" {
		if _, ok := findDataSourceBySlug(queryableDataSources, selectedDataSource); !ok {
			if selectedDS, found := findDataSourceBySlug(
				dsList.DataSources,
				selectedDataSource,
			); found && !isQueryEditorSupportedDataSourceType(selectedDS.Type) {
				if pageError == "" {
					pageError = "The query editor does not support this data source type."
				}
			}
			selectedDataSource = ""
		}
	}

	selectedDataSourceType := ""
	if selectedDS, ok := findDataSourceBySlug(queryableDataSources, selectedDataSource); ok {
		selectedDataSourceType = string(selectedDS.Type)
	}

	if selectedDataSource == "" && len(queryableDataSources) > 0 {
		selectedDataSource = queryableDataSources[0].Slug
		selectedDataSourceType = string(queryableDataSources[0].Type)
	}

	if operationID := c.Query("openapi_operation"); operationID != "" && c.Method() == fiber.MethodGet && selectedDataSource == c.Query("ds") && !showDeleted {
		dataSource, draftStatus, _, draftErr := r.getDataSourceViaAPI(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), selectedDataSource)
		if draftErr != nil {
			if draftStatus == fiber.StatusUnauthorized {
				return redirectToLogin(c, c.OriginalURL())
			}
			status = draftStatus
			pageError = "The imported operation could not be loaded."
		} else {
			var diagnostic string
			createFormData, diagnostic = openAPIOperationForm(dataSource, operationID)
			if diagnostic != "" {
				status = fiber.StatusBadRequest
				pageError = diagnostic
			}
		}
	}

	queries := []api.Query{}
	var selectedQuery *api.Query
	selectedQueryFormData := views.QueryEditorFormData{}
	hasMore := false
	nextCursor := ""
	prevCursor := ""

	if selectedDataSource != "" {
		queryParams := &api.ListQueriesParams{}
		queryLimit := api.LimitParam(20)
		queryParams.Limit = &queryLimit
		if showDeleted {
			includeDeleted := true
			queryParams.IncludeDeleted = &includeDeleted
		}

		cursor := strings.TrimSpace(c.Query("cursor"))
		if cursor != "" {
			queryParams.Cursor = &cursor
		}

		queryList, queryStatus, queryErrorResponse, queryErr := r.listQueriesViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			selectedDataSource,
			queryParams,
		)
		if queryErr != nil {
			if queryStatus == fiber.StatusUnauthorized {
				return redirectToLogin(c, c.OriginalURL())
			}

			if pageError == "" {
				message := messageFromErrorResponse(queryErrorResponse)
				if message == "" {
					message = "Something went wrong while loading queries."
				}
				pageError = message
			}

			pageErrorDetails = mergeErrorDetails(
				pageErrorDetails,
				errorPopupDetails(queryErrorResponse, fmt.Sprintf("list queries: %v", queryErr)),
			)
		} else {
			queries = queryList.Queries
			if showDeleted {
				queries = filterDeletedQueries(queries)
			}
			selectedQuery = findQueryBySlug(queries, selectedQuerySlug)
			if selectedQuery != nil {
				selectedQueryFormData = views.QueryEditorFormDataFromQuery(*selectedQuery)
			}
			hasMore = queryList.HasMore
			if queryList.NextCursor != nil {
				nextCursor = *queryList.NextCursor
			}
			if queryList.PrevCursor != nil {
				prevCursor = *queryList.PrevCursor
			}
		}
	}

	shellData := r.buildShellDataForOrganizationPage(
		c,
		"Queries",
		orgSlug.String(),
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Reusable connector operations for source reads, sink payloads, validation, and direct execution."
	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)
	setHTMLResponseStatus(c, status)
	return renderTempl(
		c,
		views.QueriesPage(
			shellData,
			queryableDataSources,
			selectedDataSource,
			selectedDataSourceType,
			queries,
			selectedQuery,
			selectedQueryFormData,
			hasMore,
			nextCursor,
			prevCursor,
			orgSlug.String(),
			createFormData,
			executionView,
			executionError,
			focusQuerySlug,
			showDeleted,
			permissions,
		),
	)
}

func filterDeletedQueries(queries []api.Query) []api.Query {
	filtered := make([]api.Query, 0, len(queries))
	for _, query := range queries {
		if query.DeletedAt != nil {
			filtered = append(filtered, query)
		}
	}
	return filtered
}

func findQueryBySlug(queries []api.Query, querySlug string) *api.Query {
	trimmed := strings.TrimSpace(querySlug)
	if trimmed == "" {
		return nil
	}

	for idx := range queries {
		if queries[idx].Slug == trimmed {
			return &queries[idx]
		}
	}
	return nil
}

func queryReturnToList(c fiber.Ctx) bool {
	return strings.EqualFold(strings.TrimSpace(c.FormValue("return_to")), "list")
}

func queryShouldRenderSidePanel(c fiber.Ctx) bool {
	return isHTMXRequest(c) && queryReturnToList(c)
}

// renderQuerySidePanel renders an HTMX-safe query detail fragment with feedback in the panel body.
func (r *Router) renderQuerySidePanel(
	c fiber.Ctx,
	orgSlug slug.Slug,
	dataSourceSlug slug.Slug,
	querySlug slug.Slug,
	status int,
	feedbackMessage string,
	feedbackKind string,
	feedbackDetails string,
	formData *views.QueryEditorFormData,
	query *api.Query,
	executionError string,
	executionErrorDetails string,
) error {
	// Preserve swap behavior for validation feedback and render status context in the panel content.
	_ = status
	setHTMLResponseStatus(c, http.StatusOK)

	selectedQuery := query
	if selectedQuery == nil {
		loaded, getStatus, errorResponse, getErr := r.getQueryViaAPI(
			c.Context(),
			c.Get(fiber.HeaderCookie),
			orgSlug.String(),
			dataSourceSlug.String(),
			querySlug.String(),
		)
		if getErr != nil {
			if getStatus == fiber.StatusUnauthorized {
				return redirectToLogin(c, c.OriginalURL())
			}
			if feedbackMessage == "" {
				message := messageFromErrorResponse(errorResponse)
				if message == "" {
					message = "Something went wrong while loading query details."
				}
				feedbackMessage = message
			}
		} else {
			selectedQuery = loaded
		}
	}

	selectedFormData := views.DefaultQueryEditorFormData()
	if formData != nil {
		selectedFormData = *formData
	} else if selectedQuery != nil {
		selectedFormData = views.QueryEditorFormDataFromQuery(*selectedQuery)
	}

	permissions := r.resolveOrganizationPermissions(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
	)

	return renderTempl(
		c,
		views.QuerySidePanelSlot(
			orgSlug.String(),
			dataSourceSlug.String(),
			selectedQuery,
			selectedFormData,
			nil,
			executionError,
			executionErrorDetails,
			permissions,
			feedbackMessage,
			feedbackKind,
			feedbackDetails,
		),
	)
}

func queryReturnURL(organizationSlug, dataSourceSlug, querySlug string) string {
	return "/orgs/" + organizationSlug + "/queries?ds=" + url.QueryEscape(dataSourceSlug) + "&query=" + url.QueryEscape(querySlug)
}

func queryUpdateReturnURL(c fiber.Ctx, organizationSlug, dataSourceSlug, querySlug string) string {
	if queryReturnToList(c) {
		return queryReturnURL(organizationSlug, dataSourceSlug, querySlug)
	}
	href := "/orgs/" + organizationSlug + "/data-sources/" + dataSourceSlug + "/queries/" + querySlug
	if listReturn := strings.TrimSpace(c.Query("list_return")); listReturn != "" {
		values := url.Values{}
		values.Set("list_return", listReturn)
		href += "?" + values.Encode()
	}
	return href
}

func isQueryEditorSupportedDataSourceType(dsType api.DataSourceType) bool {
	return views.DataSourceSupportsQueryEditor(dsType)
}

func filterQueryableDataSources(dataSources []api.DataSource) []api.DataSource {
	filtered := make([]api.DataSource, 0, len(dataSources))
	for _, ds := range dataSources {
		if isQueryEditorSupportedDataSourceType(ds.Type) {
			filtered = append(filtered, ds)
		}
	}

	return filtered
}

func findDataSourceBySlug(dataSources []api.DataSource, slug string) (api.DataSource, bool) {
	for _, ds := range dataSources {
		if ds.Slug == slug {
			return ds, true
		}
	}

	return api.DataSource{}, false
}

func (r *Router) listQueriesViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
	params *api.ListQueriesParams,
) (*api.QueryListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListQueriesWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
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
	return nil, resp.StatusCode(), errResp, unexpectedBackendAPIResponse("list queries", resp.StatusCode())
}

func (r *Router) createQueryViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
	payload api.CreateQueryJSONRequestBody,
) (*api.Query, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.CreateQueryWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
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
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"create query api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) getQueryViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
	querySlug string,
) (*api.Query, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetQueryWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
		querySlug,
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
		"get query api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) updateQueryViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
	querySlug string,
	payload api.UpdateQueryJSONRequestBody,
) (*api.Query, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.UpdateQueryWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
		querySlug,
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
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"update query api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) deleteQueryViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
	querySlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.DeleteQueryWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
		querySlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"delete query api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) restoreQueryViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
	querySlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.RestoreQueryWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
		querySlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"restore query api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) purgeQueryViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
	querySlug string,
) (int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.PurgeQueryWithResponse(
		ctx,
		organizationSlug,
		dataSourceSlug,
		querySlug,
		frontendRequestEditor(ctx, cookieHeader),
	)
	if err != nil {
		return http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusNoContent {
		return http.StatusNoContent, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"purge query api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) executeQueryViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	dataSourceSlug string,
	querySlug string,
	timeoutSeconds int,
	payload api.ExecuteQueryJSONRequestBody,
) (*api.QueryExecutionResult, int, *api.ErrorResponse, error) {
	requestCtx, cancel := frontendAPIExecutionContext(ctx, timeoutSeconds)
	defer cancel()

	resp, err := r.apiClient.ExecuteQueryWithResponse(
		requestCtx,
		organizationSlug,
		dataSourceSlug,
		querySlug,
		payload,
		frontendRequestEditor(requestCtx, cookieHeader),
	)
	if err != nil {
		return nil, http.StatusInternalServerError, nil, xerror.Unexpected().With(err)
	}

	if resp.StatusCode() == http.StatusOK && resp.JSON200 != nil {
		return resp.JSON200, http.StatusOK, nil, nil
	}

	errResp := parseAPIErrorResponse(resp.Body)
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"execute query api returned status %d",
		resp.StatusCode(),
	)
}

func queryEditorFormDataFromRequest(c fiber.Ctx) views.QueryEditorFormData {
	const maxFormParameterRows = 100

	formData := views.DefaultQueryEditorFormData()
	formData.Name = strings.TrimSpace(c.FormValue("name"))
	formData.Slug = strings.TrimSpace(c.FormValue("slug"))
	formData.Description = strings.TrimSpace(c.FormValue("description"))
	formData.StatementType = strings.TrimSpace(c.FormValue("statement_type"))
	formData.WorkspaceResource = c.FormValue("workspace_resource")
	formData.WorkspaceSpreadsheetID = c.FormValue("workspace_spreadsheet_id")
	formData.WorkspaceFileID = c.FormValue("workspace_file_id")
	formData.WorkspaceFolderID = c.FormValue("workspace_folder_id")
	formData.WorkspaceDriveID = c.FormValue("workspace_drive_id")
	formData.WorkspaceName = c.FormValue("workspace_name")
	formData.WorkspaceFormat = c.FormValue("workspace_format")
	formData.WorkspaceContent = c.FormValue("workspace_content")
	formData.WorkspaceMatchText = c.FormValue("workspace_match_text")
	formData.WorkspaceMIMEType = c.FormValue("workspace_mime_type")
	formData.WorkspacePageToken = c.FormValue("workspace_page_token")
	formData.WorkspaceDelimiter = c.FormValue("workspace_delimiter")
	formData.WorkspaceHasHeader = c.FormValue("workspace_has_header")
	formData.WorkspaceWorksheetID = c.FormValue("workspace_worksheet_id")
	formData.WorkspaceRange = c.FormValue("workspace_range")
	formData.WorkspaceOperation = c.FormValue("workspace_operation")
	formData.WorkspaceColumns = c.FormValue("workspace_columns")
	formData.WorkspaceRows = c.FormValue("workspace_rows")
	formData.Statement = strings.TrimSpace(c.FormValue("statement"))
	if value := c.FormValue("sql_format"); value != "" {
		formData.SQLFormat = value
	}
	formData.HTTPMethod = strings.TrimSpace(c.FormValue("http_method"))
	formData.HTTPPath = strings.TrimSpace(c.FormValue("http_path"))
	formData.HTTPQueryParamRows = httpKeyValueRowsFromRequest(
		c,
		"http_query_param_key",
		"http_query_param_value",
	)
	formData.HTTPHeaderRows = httpKeyValueRowsFromRequest(
		c,
		"http_header_key",
		"http_header_value",
	)
	formData.HTTPBody = strings.TrimSpace(c.FormValue("http_body"))
	formData.HTTPPagination = strings.TrimSpace(c.FormValue("http_pagination"))
	formData.HTTPWriteRetry = strings.TrimSpace(c.FormValue("http_write_retry"))
	formData.HTTPAcknowledgement = httpAcknowledgementFormFromRequest(c)
	_, formData.HTTPAcknowledgement.Error = httpAcknowledgementFromForm(formData.HTTPAcknowledgement)
	formData.MongoCollection = strings.TrimSpace(c.FormValue("mongo_collection"))
	formData.MongoOperation = strings.TrimSpace(c.FormValue("mongo_operation"))
	formData.MongoFilter = strings.TrimSpace(c.FormValue("mongo_filter"))
	formData.MongoSort = strings.TrimSpace(c.FormValue("mongo_sort"))
	formData.MongoProjection = strings.TrimSpace(c.FormValue("mongo_projection"))
	formData.MongoLimitValue = strings.TrimSpace(c.FormValue("mongo_limit_value"))
	formData.MongoDocument = strings.TrimSpace(c.FormValue("mongo_document"))
	formData.HubSpotObjectType = strings.TrimSpace(c.FormValue("hubspot_object_type"))
	formData.HubSpotOperation = strings.TrimSpace(c.FormValue("hubspot_operation"))
	formData.HubSpotProperties = strings.TrimSpace(c.FormValue("hubspot_properties"))
	formData.HubSpotQuery = strings.TrimSpace(c.FormValue("hubspot_query"))
	formData.HubSpotAfter = strings.TrimSpace(c.FormValue("hubspot_after"))
	formData.HubSpotSorts = strings.TrimSpace(c.FormValue("hubspot_sorts"))
	formData.HubSpotFilterGroups = strings.TrimSpace(c.FormValue("hubspot_filter_groups"))
	formData.HubSpotIDProperty = strings.TrimSpace(c.FormValue("hubspot_id_property"))
	formData.HubSpotMapping = strings.TrimSpace(c.FormValue("hubspot_property_mapping"))
	formData.HubSpotRows = strings.TrimSpace(c.FormValue("hubspot_rows"))

	formData.FTPPath = strings.TrimSpace(c.FormValue("ftp_path"))
	formData.FTPOperation = strings.TrimSpace(c.FormValue("ftp_operation"))
	formData.FTPFormat = strings.TrimSpace(c.FormValue("ftp_format"))
	formData.FTPDelimiter = strings.TrimSpace(c.FormValue("ftp_delimiter"))
	formData.FTPSheet = strings.TrimSpace(c.FormValue("ftp_sheet"))
	formData.FTPRange = strings.TrimSpace(c.FormValue("ftp_range"))
	formData.FTPContent = strings.TrimSpace(c.FormValue("ftp_content"))
	formData.S3Bucket = strings.TrimSpace(c.FormValue("s3_bucket"))
	formData.S3Key = strings.TrimSpace(c.FormValue("s3_key"))
	formData.S3Operation = strings.TrimSpace(c.FormValue("s3_operation"))
	formData.S3Format = strings.TrimSpace(c.FormValue("s3_format"))
	formData.S3Delimiter = strings.TrimSpace(c.FormValue("s3_delimiter"))
	formData.S3Sheet = strings.TrimSpace(c.FormValue("s3_sheet"))
	formData.S3Range = strings.TrimSpace(c.FormValue("s3_range"))
	formData.GitPath = strings.TrimSpace(c.FormValue("git_path"))
	formData.GitOperation = strings.TrimSpace(c.FormValue("git_operation"))
	formData.GitFormat = strings.TrimSpace(c.FormValue("git_format"))
	formData.GitDelimiter = strings.TrimSpace(c.FormValue("git_delimiter"))
	formData.GitSheet = strings.TrimSpace(c.FormValue("git_sheet"))
	formData.GitRange = strings.TrimSpace(c.FormValue("git_range"))
	formData.GitBranch = strings.TrimSpace(c.FormValue("git_branch"))
	formData.GitMessage = strings.TrimSpace(c.FormValue("git_message"))
	formData.GitContent = strings.TrimSpace(c.FormValue("git_content"))
	formData.FTPHasHeader = c.FormValue("ftp_has_header") == "on"
	formData.FTPOverwrite = c.FormValue("ftp_overwrite") == "on"
	formData.S3HasHeader = c.FormValue("s3_has_header") == "on"
	formData.GitHasHeader = c.FormValue("git_has_header") == "on"

	if rowLimit := strings.TrimSpace(c.FormValue("row_limit")); rowLimit != "" {
		formData.RowLimit = rowLimit
	}
	if timeoutSeconds := strings.TrimSpace(c.FormValue("timeout_seconds")); timeoutSeconds != "" {
		formData.TimeoutSeconds = timeoutSeconds
	}

	// For the form read-back path, collect all rows without a hard limit and
	// then truncate to maxFormParameterRows. This prevents silently losing
	// parameters when the count is slightly over the limit while the hard
	// limit is still enforced in the payload-building path.
	rows, _ := queryParametersFromRequest(c, 0)
	if len(rows) > maxFormParameterRows {
		rows = rows[:maxFormParameterRows]
	}
	if len(rows) > 0 {
		formData.Parameters = rows
	}

	return formData
}

func buildCreateQueryPayload(
	c fiber.Ctx,
	dataSourceType api.DataSourceType,
) (api.CreateQueryJSONRequestBody, string) {
	payload := api.NewQuery{}

	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return payload, "Query name is required."
	}
	payload.Name = name

	if rawSlug := strings.TrimSpace(c.FormValue("slug")); rawSlug != "" {
		parsedSlug, err := slug.New(rawSlug)
		if err != nil {
			return payload, "Query slug is invalid."
		}
		slugValue := parsedSlug.String()
		payload.Slug = &slugValue
	}

	if description := strings.TrimSpace(c.FormValue("description")); description != "" {
		payload.Description = &description
	}

	statement, statementErr := buildQueryStatementFromRequest(c, dataSourceType)
	if statementErr != "" {
		return payload, statementErr
	}
	payload.QueryStatement = statement

	statementText, statementMarshalErr := statementTextForValidation(statement)
	if statementMarshalErr != "" {
		return payload, statementMarshalErr
	}

	if dataSourceType != api.DataSourceTypeHttp {
		maxRowLimit := 10000
		if dataSourceType == api.DataSourceTypeHubspot {
			maxRowLimit = 200
		}

		rowLimit, rowLimitErr := parsePositiveInt(
			c.FormValue("row_limit"),
			100,
			maxRowLimit,
			"Row limit",
		)
		if rowLimitErr != "" {
			return payload, rowLimitErr
		}
		payload.RowLimit = &rowLimit
	}

	timeoutSeconds, timeoutErr := parsePositiveInt(
		c.FormValue("timeout_seconds"),
		30,
		300,
		"Timeout",
	)
	if timeoutErr != "" {
		return payload, timeoutErr
	}
	payload.TimeoutSeconds = &timeoutSeconds

	parameters, paramsErr := buildQueryParametersFromRequest(c)
	if paramsErr != "" {
		return payload, paramsErr
	}

	if paramsValidationErr := validateQueryParametersAgainstStatement(
		parameters,
		statementText,
	); paramsValidationErr != "" {
		return payload, paramsValidationErr
	}

	if len(parameters) > 0 {
		payload.Parameters = &parameters
	}

	return payload, ""
}

func buildUpdateQueryPayload(
	c fiber.Ctx,
	dataSourceType api.DataSourceType,
) (api.UpdateQueryJSONRequestBody, string) {
	payload := api.UpdateQuery{}

	name := strings.TrimSpace(c.FormValue("name"))
	if name == "" {
		return payload, "Query name is required."
	}
	payload.Name = name

	if description := strings.TrimSpace(c.FormValue("description")); description != "" {
		payload.Description = &description
	}

	statement, statementErr := buildQueryStatementFromRequest(c, dataSourceType)
	if statementErr != "" {
		return payload, statementErr
	}
	payload.QueryStatement = statement

	statementText, statementMarshalErr := statementTextForValidation(statement)
	if statementMarshalErr != "" {
		return payload, statementMarshalErr
	}

	if dataSourceType != api.DataSourceTypeHttp {
		maxRowLimit := 10000
		if dataSourceType == api.DataSourceTypeHubspot {
			maxRowLimit = 200
		}

		rowLimit, rowLimitErr := parsePositiveInt(
			c.FormValue("row_limit"),
			100,
			maxRowLimit,
			"Row limit",
		)
		if rowLimitErr != "" {
			return payload, rowLimitErr
		}
		payload.RowLimit = &rowLimit
	}

	timeoutSeconds, timeoutErr := parsePositiveInt(
		c.FormValue("timeout_seconds"),
		30,
		300,
		"Timeout",
	)
	if timeoutErr != "" {
		return payload, timeoutErr
	}
	payload.TimeoutSeconds = &timeoutSeconds

	parameters, paramsErr := buildQueryParametersFromRequest(c)
	if paramsErr != "" {
		return payload, paramsErr
	}

	if paramsValidationErr := validateQueryParametersAgainstStatement(
		parameters,
		statementText,
	); paramsValidationErr != "" {
		return payload, paramsValidationErr
	}

	payload.Parameters = &parameters

	return payload, ""
}

func buildQueryStatementFromRequest(
	c fiber.Ctx,
	dataSourceType api.DataSourceType,
) (api.QueryStatement, string) {
	var statement api.QueryStatement

	switch dataSourceType {
	case api.DataSourceTypeGoogleWorkspace:

		value := workspace.Statement{Type: "google_workspace", Resource: strings.TrimSpace(c.FormValue("workspace_resource")), Operation: strings.TrimSpace(c.FormValue("workspace_operation"))}
		if value.Operation == "" {
			value.Operation = "read"
		}
		binding := func(key string) json.RawMessage {
			v := strings.TrimSpace(c.FormValue(key))
			if v == "{{input}}" {
				return json.RawMessage(`"{{input}}"`)
			}
			return json.RawMessage(v)
		}
		switch {
		case value.Resource == "spreadsheet":
			id, err := strconv.Atoi(c.FormValue("workspace_worksheet_id"))
			if err != nil || id < 0 {
				return statement, "Choose a valid worksheet ID."
			}
			value.SpreadsheetID = strings.TrimSpace(c.FormValue("workspace_spreadsheet_id"))
			value.WorksheetID = &id
			value.Range = strings.TrimSpace(c.FormValue("workspace_range"))
			if value.Operation == "update" {
				if err := json.Unmarshal([]byte(c.FormValue("workspace_columns")), &value.Columns); err != nil {
					return statement, "Columns must be an ordered JSON array."
				}
				value.Rows = binding("workspace_rows")
			}
		case value.IsNative():
			if value.Operation == "create" {
				value.FolderID = c.FormValue("workspace_folder_id")
				value.Name = c.FormValue("workspace_name")
			} else {
				value.FileID = c.FormValue("workspace_file_id")
				if value.Operation == "update" {
					value.MatchText = c.FormValue("workspace_match_text")
					value.Content = binding("workspace_content")
					if len(value.Content) == 0 {
						value.Content = json.RawMessage(`""`)
					}
				}
			}
		default:
			if value.Operation == "list" {
				value.FolderID = c.FormValue("workspace_folder_id")
				value.DriveID = c.FormValue("workspace_drive_id")
				value.PageToken = c.FormValue("workspace_page_token")
				value.MIMEType = c.FormValue("workspace_mime_type")
			} else {
				value.Format = c.FormValue("workspace_format")
				if value.Operation == "create" {
					value.FolderID = c.FormValue("workspace_folder_id")
					value.Name = c.FormValue("workspace_name")
				} else {
					value.FileID = c.FormValue("workspace_file_id")
				}
				if value.Operation != "read" {
					value.Content = binding("workspace_content")
					value.MIMEType = c.FormValue("workspace_mime_type")
				}
				if value.Format == "csv" {
					value.Delimiter = c.FormValue("workspace_delimiter")
					if v := c.FormValue("workspace_has_header"); v != "" {
						h, err := strconv.ParseBool(v)
						if err != nil {
							return statement, "Choose a CSV header policy."
						}
						value.HasHeader = &h
					}
				}
			}
		}

		raw, err := json.Marshal(value)
		if err != nil {
			return statement, "Invalid Workspace operation."
		}
		if _, err := workspace.DecodeStatement(raw); err != nil {
			return statement, err.Error()
		}
		if err := statement.UnmarshalJSON(raw); err != nil {
			return statement, "Invalid Workspace operation."
		}

	case api.DataSourceTypePostgres:
		statementText := strings.TrimSpace(c.FormValue("statement"))
		if statementText == "" {
			return api.QueryStatement{}, queryStatementRequiredMessage
		}
		format := api.PostgresStatementFormat(c.FormValue("sql_format"))
		if format == "" {
			format = api.PostgresStatementFormatRows
		}
		if !format.Valid() {
			return api.QueryStatement{}, "Choose rows or a JSON file for the PostgreSQL result."
		}

		if err := statement.FromPostgresStatement(api.PostgresStatement{
			Type:   api.PostgresStatementTypePostgres,
			Sql:    statementText,
			Format: &format,
		}); err != nil {
			return api.QueryStatement{}, "Query statement could not be encoded for PostgreSQL."
		}
	case api.DataSourceTypeMysql:
		statementText := strings.TrimSpace(c.FormValue("statement"))
		if statementText == "" {
			return api.QueryStatement{}, queryStatementRequiredMessage
		}

		if err := statement.FromMySQLStatement(api.MySQLStatement{
			Type: api.MySQLStatementTypeMysql,
			Sql:  statementText,
		}); err != nil {
			return api.QueryStatement{}, "Query statement could not be encoded for MySQL."
		}
	case api.DataSourceTypeBigquery:
		statementText := strings.TrimSpace(c.FormValue("statement"))
		if statementText == "" {
			return api.QueryStatement{}, queryStatementRequiredMessage
		}

		operation := api.BigQueryStatementOperationQuery
		if err := statement.FromBigQueryStatement(api.BigQueryStatement{
			Type:      api.BigQueryStatementTypeBigquery,
			Operation: &operation,
			Sql:       &statementText,
		}); err != nil {
			return api.QueryStatement{}, "Query statement could not be encoded for BigQuery."
		}
	case api.DataSourceTypeSnowflake:
		statementText := strings.TrimSpace(c.FormValue("statement"))
		if statementText == "" {
			return api.QueryStatement{}, queryStatementRequiredMessage
		}
		if err := statement.FromSnowflakeStatement(api.SnowflakeStatement{
			Type: api.SnowflakeStatementTypeSnowflake,
			Sql:  statementText,
		}); err != nil {
			return api.QueryStatement{}, "Query statement could not be encoded for Snowflake."
		}
	case api.DataSourceTypeFtp:
		ftpPath := strings.TrimSpace(c.FormValue("ftp_path"))
		if ftpPath == "" {
			return api.QueryStatement{}, "Path is required."
		}

		ftpStmt := api.FTPStatement{
			Type: api.FTPStatementTypeFtp,
			Path: ftpPath,
		}

		if op := strings.TrimSpace(c.FormValue("ftp_operation")); op != "" {
			opEnum := api.FTPStatementOperation(op)
			ftpStmt.Operation = &opEnum
		}
		ftpFormat := strings.TrimSpace(c.FormValue("ftp_format"))
		if ftpFormat != "" {
			formatEnum := api.FTPStatementFormat(ftpFormat)
			ftpStmt.Format = &formatEnum
		}
		if delim := strings.TrimSpace(c.FormValue("ftp_delimiter")); delim != "" {
			if len([]rune(delim)) != 1 {
				return api.QueryStatement{}, "Delimiter must be a single character."
			}
			ftpStmt.Delimiter = &delim
		}
		if sheet := strings.TrimSpace(c.FormValue("ftp_sheet")); sheet != "" {
			ftpStmt.Sheet = &sheet
		}
		if cellRange := strings.TrimSpace(c.FormValue("ftp_range")); cellRange != "" {
			ftpStmt.Range = &cellRange
		}
		if hasHeader := queryHasHeaderValue(c.FormValue("ftp_has_header"), ftpFormat, ftpPath); hasHeader != nil {
			ftpStmt.HasHeader = hasHeader
		}
		overwrite := c.FormValue("ftp_overwrite") == "on"
		ftpStmt.Overwrite = &overwrite

		if raw := strings.TrimSpace(c.FormValue("ftp_content")); raw != "" {
			var content api.FTPStatement_Content
			if err := json.Unmarshal([]byte(raw), &content); err != nil {
				return api.QueryStatement{}, "Content must be valid JSON."
			}
			ftpStmt.Content = &content
		}

		if err := statement.FromFTPStatement(ftpStmt); err != nil {
			return api.QueryStatement{}, "Statement could not be encoded."
		}
	case api.DataSourceTypeS3:
		if raw := strings.TrimSpace(c.FormValue("statement")); raw != "" {
			var s3Statement api.S3Statement
			if json.Unmarshal([]byte(raw), &s3Statement) != nil || s3Statement.Type != api.S3StatementTypeS3 {
				return statement, "Enter an S3 operation as a JSON object with type s3."
			}
			if err := statement.UnmarshalJSON([]byte(raw)); err != nil {
				return statement, "S3 statement could not be encoded."
			}
			return statement, ""
		}
		key := strings.TrimSpace(c.FormValue("s3_key"))
		if key == "" {
			return api.QueryStatement{}, "Object key is required."
		}
		if operation := strings.TrimSpace(c.FormValue("s3_operation")); operation != "" && operation != "read" {
			return api.QueryStatement{}, "S3 queries support read operations only."
		}

		operation := api.S3StatementOperationRead
		s3Stmt := api.S3Statement{Type: api.S3StatementTypeS3, Key: &key, Operation: &operation}
		if bucket := strings.TrimSpace(c.FormValue("s3_bucket")); bucket != "" {
			s3Stmt.Bucket = &bucket
		}
		s3Format := strings.TrimSpace(c.FormValue("s3_format"))
		if s3Format != "" {
			format := api.S3StatementFormat(s3Format)
			s3Stmt.Format = &format
		}
		if delimiter := strings.TrimSpace(c.FormValue("s3_delimiter")); delimiter != "" {
			if len([]rune(delimiter)) != 1 {
				return api.QueryStatement{}, "Delimiter must be a single character."
			}
			s3Stmt.Delimiter = &delimiter
		}
		if sheet := strings.TrimSpace(c.FormValue("s3_sheet")); sheet != "" {
			s3Stmt.Sheet = &sheet
		}
		if cellRange := strings.TrimSpace(c.FormValue("s3_range")); cellRange != "" {
			s3Stmt.Range = &cellRange
		}
		if hasHeader := queryHasHeaderValue(c.FormValue("s3_has_header"), s3Format, key); hasHeader != nil {
			s3Stmt.HasHeader = hasHeader
		}
		if err := statement.FromS3Statement(s3Stmt); err != nil {
			return api.QueryStatement{}, "Statement could not be encoded."
		}
	case api.DataSourceTypeGit, api.DataSourceTypeGithub, api.DataSourceTypeGitlab:
		gitPath := strings.TrimSpace(c.FormValue("git_path"))
		if gitPath == "" {
			return api.QueryStatement{}, "Path is required."
		}

		gitStmt := api.GitStatement{
			Type: api.GitStatementTypeGit,
			Path: gitPath,
		}

		if op := strings.TrimSpace(c.FormValue("git_operation")); op != "" {
			opEnum := api.GitStatementOperation(op)
			gitStmt.Operation = &opEnum
		}
		gitFormat := strings.TrimSpace(c.FormValue("git_format"))
		if gitFormat != "" {
			formatEnum := api.GitStatementFormat(gitFormat)
			gitStmt.Format = &formatEnum
		}
		if delim := strings.TrimSpace(c.FormValue("git_delimiter")); delim != "" {
			if len([]rune(delim)) != 1 {
				return api.QueryStatement{}, "Delimiter must be a single character."
			}
			gitStmt.Delimiter = &delim
		}
		if sheet := strings.TrimSpace(c.FormValue("git_sheet")); sheet != "" {
			gitStmt.Sheet = &sheet
		}
		if cellRange := strings.TrimSpace(c.FormValue("git_range")); cellRange != "" {
			gitStmt.Range = &cellRange
		}
		if hasHeader := queryHasHeaderValue(c.FormValue("git_has_header"), gitFormat, gitPath); hasHeader != nil {
			gitStmt.HasHeader = hasHeader
		}
		if branch := strings.TrimSpace(c.FormValue("git_branch")); branch != "" {
			gitStmt.Branch = &branch
		}
		if msg := strings.TrimSpace(c.FormValue("git_message")); msg != "" {
			gitStmt.Message = &msg
		}

		if raw := strings.TrimSpace(c.FormValue("git_content")); raw != "" {
			var content api.GitStatement_Content
			if err := json.Unmarshal([]byte(raw), &content); err != nil {
				return api.QueryStatement{}, "Content must be valid JSON."
			}
			gitStmt.Content = &content
		}

		if err := statement.FromGitStatement(gitStmt); err != nil {
			return api.QueryStatement{}, "Statement could not be encoded."
		}
	case api.DataSourceTypeHttp:
		httpStatement, httpErrMsg := buildHTTPStatementFromRequest(c)
		if httpErrMsg != "" {
			return api.QueryStatement{}, httpErrMsg
		}

		if err := statement.FromHTTPStatement(httpStatement); err != nil {
			return api.QueryStatement{}, "Query statement could not be encoded for HTTP."
		}
	case api.DataSourceTypeMongodb:
		mongoStatement, mongoErrMsg := buildMongoDBStatementFromRequest(c)
		if mongoErrMsg != "" {
			return api.QueryStatement{}, mongoErrMsg
		}

		if err := statement.FromMongoDBStatement(mongoStatement); err != nil {
			return api.QueryStatement{}, "Query statement could not be encoded for MongoDB."
		}
	case api.DataSourceTypeHubspot:
		hubspotStatement, hubspotErrMsg := buildHubSpotStatementFromRequest(c)
		if hubspotErrMsg != "" {
			return api.QueryStatement{}, hubspotErrMsg
		}

		if err := statement.FromHubSpotStatement(hubspotStatement); err != nil {
			return api.QueryStatement{}, "Query statement could not be encoded for HubSpot."
		}
	default:
		return api.QueryStatement{}, "Unsupported data source type for query editor."
	}

	return statement, ""
}

func buildHTTPStatementFromRequest(c fiber.Ctx) (api.HTTPStatement, string) {
	method := strings.TrimSpace(c.FormValue("http_method"))
	if method == "" {
		method = string(api.HTTPStatementMethodGET)
	}

	pathInput := strings.TrimSpace(c.FormValue("http_path"))
	if pathInput == "" {
		return api.HTTPStatement{}, httpPathRequiredMessage
	}

	path, pathQueryParamsValue, pathQueryParamsErr := parseHTTPPathAndQueryParams(pathInput)
	if pathQueryParamsErr != "" {
		return api.HTTPStatement{}, pathQueryParamsErr
	}

	statementMap := map[string]any{
		"type":   "http",
		"method": method,
		"path":   path,
	}

	queryParamsValue, queryParamsErr := parseHTTPKeyValueMapFromRequest(
		c,
		"http_query_param_key",
		"http_query_param_value",
		"HTTP query params",
	)
	if queryParamsErr != "" {
		return api.HTTPStatement{}, queryParamsErr
	}
	for key, value := range pathQueryParamsValue {
		if _, exists := queryParamsValue[key]; !exists {
			queryParamsValue[key] = value
		}
	}
	if len(queryParamsValue) > 0 {
		statementMap["query_params"] = queryParamsValue
	}

	headersValue, headersErr := parseHTTPKeyValueMapFromRequest(
		c,
		"http_header_key",
		"http_header_value",
		"HTTP headers",
	)
	if headersErr != "" {
		return api.HTTPStatement{}, headersErr
	}
	if len(headersValue) > 0 {
		statementMap["headers"] = headersValue
	}

	if value, message := httpAcknowledgementFromForm(httpAcknowledgementFormFromRequest(c)); message != "" {
		return api.HTTPStatement{}, message
	} else if value != nil {
		statementMap["acknowledgement"] = value
	}

	if pagination := strings.TrimSpace(c.FormValue("http_pagination")); pagination != "" {
		if !json.Valid([]byte(pagination)) {
			return api.HTTPStatement{}, "HTTP pagination must be valid JSON."
		}
		// Decode the generated fields without its union unmarshaler so unknown options fail.
		type paginationFields api.HTTPPagination
		var value *paginationFields
		decoder := json.NewDecoder(strings.NewReader(pagination))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&value); err != nil || value == nil {
			return api.HTTPStatement{}, "HTTP pagination must match the supported cursor, page-number or next-link configuration."
		}
		statementMap["pagination"] = value
	}
	if policy := strings.TrimSpace(c.FormValue("http_write_retry")); policy != "" {
		var value *api.HTTPWriteRetry
		decoder := json.NewDecoder(strings.NewReader(policy))
		decoder.DisallowUnknownFields()
		if !json.Valid([]byte(policy)) {
			return api.HTTPStatement{}, "HTTP write retry must be valid JSON."
		}
		if err := decoder.Decode(&value); err != nil || value == nil {
			return api.HTTPStatement{}, "HTTP write retry must match the supported provider contract configuration."
		}
		statementMap["write_retry"] = value
	}

	if bodyText := strings.TrimSpace(c.FormValue("http_body")); bodyText != "" {
		var bodyValue any
		if err := json.Unmarshal([]byte(bodyText), &bodyValue); err != nil {
			return api.HTTPStatement{}, "HTTP body must be valid JSON."
		}
		statementMap["body"] = bodyValue
	}

	raw, err := json.Marshal(statementMap)
	if err != nil {
		return api.HTTPStatement{}, "Query statement could not be encoded for HTTP."
	}

	httpStatement, err := parseHTTPStatement(string(raw))
	if err != nil {
		return api.HTTPStatement{}, "HTTP statement must be valid JSON."
	}

	return httpStatement, ""
}

func parseHTTPPathAndQueryParams(pathInput string) (string, map[string]string, string) {
	pathText := strings.TrimSpace(pathInput)
	before, after, ok := strings.Cut(pathText, "?")
	if !ok {
		if pathText == "" {
			return "", nil, httpPathRequiredMessage
		}
		return pathText, map[string]string{}, ""
	}

	basePath := strings.TrimSpace(before)
	if basePath == "" {
		return "", nil, httpPathRequiredMessage
	}

	queryText := after
	if hashIndex := strings.Index(queryText, "#"); hashIndex >= 0 {
		queryText = queryText[:hashIndex]
	}

	if strings.TrimSpace(queryText) == "" {
		return basePath, map[string]string{}, ""
	}

	parsedQuery, err := url.ParseQuery(queryText)
	if err != nil {
		return "", nil, "HTTP path query string must be valid."
	}

	params := make(map[string]string, len(parsedQuery))
	for key, values := range parsedQuery {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			return "", nil, "HTTP path query params key cannot be empty."
		}
		if len(values) > 1 {
			return "", nil, fmt.Sprintf(
				"HTTP path query params contains duplicate key %s.",
				trimmedKey,
			)
		}

		value := ""
		if len(values) == 1 {
			value = values[0]
		}
		params[trimmedKey] = value
	}

	return basePath, params, ""
}

func buildMongoDBStatementFromRequest(c fiber.Ctx) (api.MongoDBStatement, string) {
	collection := strings.TrimSpace(c.FormValue("mongo_collection"))
	if collection == "" {
		return api.MongoDBStatement{}, "MongoDB collection is required."
	}

	operation := strings.TrimSpace(c.FormValue("mongo_operation"))
	if operation == "" {
		operation = string(api.MongoDBStatementOperationFind)
	}

	statementMap := map[string]any{
		"type":       "mongodb",
		"collection": collection,
		"operation":  operation,
	}

	if filterText := strings.TrimSpace(c.FormValue("mongo_filter")); filterText != "" {
		filterValue, err := parseJSONObjectInput(filterText)
		if err != nil {
			return api.MongoDBStatement{}, "MongoDB filter must be a valid JSON object."
		}
		statementMap["filter"] = filterValue
	}

	if sortText := strings.TrimSpace(c.FormValue("mongo_sort")); sortText != "" {
		sortValue, err := parseJSONObjectInput(sortText)
		if err != nil {
			return api.MongoDBStatement{}, "MongoDB sort must be a valid JSON object."
		}
		statementMap["sort"] = sortValue
	}

	if projectionText := strings.TrimSpace(c.FormValue("mongo_projection")); projectionText != "" {
		projectionValue, err := parseJSONObjectInput(projectionText)
		if err != nil {
			return api.MongoDBStatement{}, "MongoDB projection must be a valid JSON object."
		}
		statementMap["projection"] = projectionValue
	}

	if limitValueText := strings.TrimSpace(c.FormValue("mongo_limit_value")); limitValueText != "" {
		limitValue, err := strconv.Atoi(limitValueText)
		if err != nil || limitValue < 1 {
			return api.MongoDBStatement{}, "MongoDB limit value must be a positive integer."
		}
		statementMap["limit_value"] = limitValue
	}

	if documentText := strings.TrimSpace(c.FormValue("mongo_document")); documentText != "" {
		documentValue, err := parseJSONObjectInput(documentText)
		if err != nil {
			return api.MongoDBStatement{}, "MongoDB document must be a valid JSON object."
		}
		statementMap["document"] = documentValue
	}

	raw, err := json.Marshal(statementMap)
	if err != nil {
		return api.MongoDBStatement{}, "Query statement could not be encoded for MongoDB."
	}

	mongoStatement, err := parseMongoDBStatement(string(raw))
	if err != nil {
		return api.MongoDBStatement{}, "MongoDB statement must be valid JSON."
	}

	return mongoStatement, ""
}

func statementTextForValidation(statement api.QueryStatement) (string, string) {
	raw, err := json.Marshal(statement)
	if err != nil {
		return "", "Query statement could not be serialized for parameter validation."
	}

	return string(raw), ""
}

func parseJSONObjectInput(text string) (map[string]any, error) {
	var value map[string]any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, err
	}

	return value, nil
}

func httpKeyValueRowsFromRequest(
	c fiber.Ctx,
	keyField string,
	valueField string,
) []views.QueryKeyValueFormData {
	keys := c.Request().PostArgs().PeekMulti(keyField)
	values := c.Request().PostArgs().PeekMulti(valueField)

	maxRows := max(len(values), len(keys))

	rows := make([]views.QueryKeyValueFormData, 0, maxRows)
	for index := range maxRows {
		keyText := ""
		if index < len(keys) {
			keyText = strings.TrimSpace(string(keys[index]))
		}

		valueText := ""
		if index < len(values) {
			valueText = strings.TrimSpace(string(values[index]))
		}

		if keyText == "" && valueText == "" {
			continue
		}

		rows = append(rows, views.QueryKeyValueFormData{
			Key:   keyText,
			Value: valueText,
		})
	}

	if len(rows) == 0 {
		return []views.QueryKeyValueFormData{{
			Key:   "",
			Value: "",
		}}
	}

	return rows
}

func parseHTTPKeyValueMapFromRequest(
	c fiber.Ctx,
	keyField string,
	valueField string,
	label string,
) (map[string]any, string) {
	keys := c.Request().PostArgs().PeekMulti(keyField)
	values := c.Request().PostArgs().PeekMulti(valueField)

	maxRows := max(len(values), len(keys))

	result := map[string]any{}
	for index := range maxRows {
		keyText := ""
		if index < len(keys) {
			keyText = strings.TrimSpace(string(keys[index]))
		}

		valueText := ""
		if index < len(values) {
			valueText = strings.TrimSpace(string(values[index]))
		}

		if keyText == "" && valueText == "" {
			continue
		}
		if keyText == "" {
			return nil, fmt.Sprintf("%s key cannot be empty.", label)
		}
		if _, exists := result[keyText]; exists {
			return nil, fmt.Sprintf("%s contains duplicate key %s.", label, keyText)
		}

		result[keyText] = valueText
	}

	return result, ""
}

func parseHTTPStatement(statementText string) (api.HTTPStatement, error) {
	var parsed api.HTTPStatement
	if err := json.Unmarshal([]byte(statementText), &parsed); err != nil {
		return api.HTTPStatement{}, err
	}

	if parsed.Method == "" {
		parsed.Method = api.HTTPStatementMethodGET
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	if parsed.Type == "" {
		parsed.Type = api.HTTPStatementTypeHttp
	}

	return parsed, nil
}

func parseMongoDBStatement(statementText string) (api.MongoDBStatement, error) {
	var parsed api.MongoDBStatement
	if err := json.Unmarshal([]byte(statementText), &parsed); err != nil {
		return api.MongoDBStatement{}, err
	}

	if parsed.Collection == "" {
		parsed.Collection = "collection"
	}
	if parsed.Operation == "" {
		parsed.Operation = api.MongoDBStatementOperationFind
	}
	if parsed.Type == "" {
		parsed.Type = api.MongoDBStatementTypeMongodb
	}

	return parsed, nil
}

func buildHubSpotStatementFromRequest(c fiber.Ctx) (api.HubSpotStatement, string) {
	objectType := strings.TrimSpace(c.FormValue("hubspot_object_type"))
	if objectType != "contacts" &&
		objectType != "companies" &&
		objectType != "deals" &&
		!hubSpotCustomObjectTypeRegexp.MatchString(objectType) {
		return api.HubSpotStatement{}, "HubSpot object must be contacts, companies, deals, or a custom object type ID such as 2-7282133."
	}

	operationValue := strings.TrimSpace(c.FormValue("hubspot_operation"))
	if operationValue == "" {
		operationValue = string(api.HubSpotStatementOperationSearch)
	}

	operation := api.HubSpotStatementOperation(operationValue)
	switch operation {
	case api.HubSpotStatementOperationList, api.HubSpotStatementOperationSearch, api.HubSpotStatementOperationUpsert:
	default:
		return api.HubSpotStatement{}, "HubSpot operation must be list, search, or upsert."
	}

	statement := api.HubSpotStatement{
		Type:       api.HubSpotStatementTypeHubspot,
		ObjectType: objectType,
		Operation:  &operation,
	}

	if rowLimit := strings.TrimSpace(c.FormValue("row_limit")); rowLimit != "" {
		limit, limitErr := parsePositiveInt(rowLimit, 100, 200, "Row limit")
		if limitErr != "" {
			return api.HubSpotStatement{}, limitErr
		}
		statement.Limit = &limit
	}

	properties := splitTrimmedList(c.FormValue("hubspot_properties"))
	if len(properties) > 0 {
		statement.Properties = &properties
	}

	if after := strings.TrimSpace(c.FormValue("hubspot_after")); after != "" {
		statement.After = &after
	}

	if operation == api.HubSpotStatementOperationSearch {
		if queryText := strings.TrimSpace(c.FormValue("hubspot_query")); queryText != "" {
			statement.Query = &queryText
		}

		sorts := splitTrimmedList(c.FormValue("hubspot_sorts"))
		if len(sorts) > 0 {
			statement.Sorts = &sorts
		}

		if rawFilterGroups := strings.TrimSpace(
			c.FormValue("hubspot_filter_groups"),
		); rawFilterGroups != "" {
			var filterGroups []api.HubSpotFilterGroup
			if err := json.Unmarshal([]byte(rawFilterGroups), &filterGroups); err != nil {
				return api.HubSpotStatement{}, "HubSpot filter groups must be valid JSON."
			}
			statement.FilterGroups = &filterGroups
		}
	}
	if operation == api.HubSpotStatementOperationUpsert {
		idProperty := strings.TrimSpace(c.FormValue("hubspot_id_property"))
		if idProperty == "" {
			return api.HubSpotStatement{}, "HubSpot upsert ID property is required."
		}
		var mapping map[string]string
		if err := json.Unmarshal([]byte(c.FormValue("hubspot_property_mapping")), &mapping); err != nil || len(mapping) == 0 {
			return api.HubSpotStatement{}, "HubSpot upsert property mapping must be a non-empty JSON object."
		}
		rowsValue := strings.TrimSpace(c.FormValue("hubspot_rows"))
		if rowsValue == "" {
			rowsValue = "{{input}}"
		}
		var rows api.HubSpotStatement_Rows
		if err := rows.FromHubSpotStatementRows2(rowsValue); err != nil {
			return api.HubSpotStatement{}, "HubSpot upsert rows binding is invalid."
		}
		statement.IdProperty = &idProperty
		statement.PropertyMapping = &mapping
		statement.Rows = &rows
	}

	return statement, ""
}

var hubSpotCustomObjectTypeRegexp = regexp.MustCompile(`^2-[1-9][0-9]*$`)

func buildQueryParametersFromRequest(c fiber.Ctx) ([]api.QueryParameter, string) {
	rows, errMsg := queryParametersFromRequest(c, 100)
	if errMsg != "" {
		return nil, errMsg
	}
	parameters := make([]api.QueryParameter, 0, len(rows))

	for i := range rows {
		name := strings.TrimSpace(rows[i].Name)
		if name == "" {
			continue
		}

		typeValue := strings.TrimSpace(rows[i].Type)
		if typeValue == "" {
			typeValue = string(api.QueryParameterDataTypeText)
		}

		dataType := api.QueryParameterDataType(typeValue)
		if !isSupportedQueryParameterType(dataType) {
			return nil, "Parameter data type is invalid."
		}

		parameter := api.QueryParameter{
			Name:       name,
			DataType:   dataType,
			IsRequired: func() *bool { v := rows[i].Required; return &v }(),
		}

		if defaultValue := strings.TrimSpace(rows[i].DefaultValue); defaultValue != "" {
			parameter.DefaultValue = &defaultValue
		}

		if description := strings.TrimSpace(rows[i].Description); description != "" {
			parameter.Description = &description
		}

		parameters = append(parameters, parameter)
	}

	return parameters, ""
}

func queryParametersFromRequest(
	c fiber.Ctx,
	maxAllowed int,
) ([]views.QueryParameterFormData, string) {
	names := postMultiValues(c, "param_name")
	types := postMultiValues(c, "param_type")
	required := postMultiValues(c, "param_required")
	defaults := postMultiValues(c, "param_default")
	descriptions := postMultiValues(c, "param_description")
	sources := postMultiValues(c, "param_source")

	maxLen := maxInt(
		len(names),
		len(types),
		len(required),
		len(defaults),
		len(descriptions),
		len(sources),
	)
	if maxLen == 0 {
		return nil, ""
	}
	if maxAllowed > 0 && maxLen > maxAllowed {
		return nil, fmt.Sprintf("Too many parameters. Maximum allowed is %d.", maxAllowed)
	}

	rows := make([]views.QueryParameterFormData, 0, maxLen)
	for i := range maxLen {
		rowType := valueAt(types, i)
		if rowType == "" {
			rowType = string(api.QueryParameterDataTypeText)
		}

		rows = append(rows, views.QueryParameterFormData{
			Name:         valueAt(names, i),
			Type:         rowType,
			Required:     strings.EqualFold(valueAt(required, i), "true"),
			DefaultValue: valueAt(defaults, i),
			Description:  valueAt(descriptions, i),
			Source:       normalizeParamSource(valueAt(sources, i)),
		})
	}

	return rows, ""
}

func buildExecuteQueryPayload(
	c fiber.Ctx,
	query api.Query,
) (api.ExecuteQueryJSONRequestBody, string) {
	parameters := make(map[string]any, len(query.Parameters))

	for i := range query.Parameters {
		parameter := query.Parameters[i]
		field := views.QueryExecutionFieldName(parameter.Name)
		raw := strings.TrimSpace(c.FormValue(field))
		required := views.QueryParameterRequired(parameter)

		if raw == "" {
			if required {
				return api.ExecuteQueryRequest{}, fmt.Sprintf(
					"Parameter %s is required.",
					parameter.Name,
				)
			}
			continue
		}

		value, err := convertExecutionParameter(raw, parameter.DataType)
		if err != nil {
			return api.ExecuteQueryRequest{}, fmt.Sprintf(
				"Parameter %s is invalid: %s",
				parameter.Name,
				err.Error(),
			)
		}

		parameters[parameter.Name] = value
	}

	return api.ExecuteQueryRequest{Parameters: parameters}, ""
}

func convertExecutionParameter(
	raw string,
	dataType api.QueryParameterDataType,
) (any, error) {
	switch dataType {
	case api.QueryParameterDataTypeInteger:
		value, err := strconv.Atoi(raw)
		if err != nil {
			return nil, xerror.Unexpected().Detail("expected integer")
		}
		return value, nil
	case api.QueryParameterDataTypeBoolean:
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, xerror.Unexpected().Detail("expected boolean (true/false)")
		}
		return value, nil
	case api.QueryParameterDataTypeDate, api.QueryParameterDataTypeText, api.QueryParameterDataTypeUuid:
		return raw, nil
	default:
		return nil, xerror.Unexpected().Detail("unsupported type")
	}
}

func parsePositiveInt(raw string, fallback, maxValue int, fieldLabel string) (int, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fallback, ""
	}

	value, err := strconv.Atoi(trimmed)
	if err != nil || value <= 0 {
		return 0, fieldLabel + " must be a positive integer."
	}
	if maxValue > 0 && value > maxValue {
		return 0, fmt.Sprintf("%s must be less than or equal to %d.", fieldLabel, maxValue)
	}

	return value, ""
}

func isSupportedQueryParameterType(v api.QueryParameterDataType) bool {
	switch v {
	case api.QueryParameterDataTypeText, api.QueryParameterDataTypeInteger, api.QueryParameterDataTypeBoolean, api.QueryParameterDataTypeDate, api.QueryParameterDataTypeUuid:
		return true
	default:
		return false
	}
}

func postMultiValues(c fiber.Ctx, key string) []string {
	rawValues := c.RequestCtx().PostArgs().PeekMulti(key)
	values := make([]string, 0, len(rawValues))
	for i := range rawValues {
		values = append(values, strings.TrimSpace(string(rawValues[i])))
	}
	return values
}

func splitTrimmedList(raw string) []string {
	parts := strings.FieldsFunc(raw, func(ch rune) bool {
		return ch == ',' || ch == '\n' || ch == '\r'
	})

	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		if value == "" {
			continue
		}
		values = append(values, value)
	}

	return values
}

func valueAt(values []string, index int) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	return strings.TrimSpace(values[index])
}

func maxInt(values ...int) int {
	maxValue := 0
	for _, value := range values {
		if value > maxValue {
			maxValue = value
		}
	}
	return maxValue
}

const queryAutoValue = "auto"

func normalizeParamSource(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == queryAutoValue {
		return queryAutoValue
	}
	return "manual"
}

// queryHasHeaderValue preserves spreadsheet auto-detection when the checkbox is unchecked.
func queryHasHeaderValue(rawValue, format, filePath string) *bool {
	if rawValue == "on" {
		hasHeader := true
		return &hasHeader
	}

	if isSpreadsheetQueryFormat(format, filePath) {
		return nil
	}

	hasHeader := false
	return &hasHeader
}

// isSpreadsheetQueryFormat reports whether the form points at spreadsheet parsing.
func isSpreadsheetQueryFormat(format, filePath string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "spreadsheet":
		return true
	case "", queryAutoValue:
		return isSpreadsheetQueryPath(filePath)
	default:
		return false
	}
}

// isSpreadsheetQueryPath mirrors connector workbook extension detection for form serialization.
func isSpreadsheetQueryPath(filePath string) bool {
	lowerPath := strings.ToLower(strings.TrimSpace(filePath))
	return strings.HasSuffix(lowerPath, ".xlam") ||
		strings.HasSuffix(lowerPath, ".xlsm") ||
		strings.HasSuffix(lowerPath, ".xlsx") ||
		strings.HasSuffix(lowerPath, ".xltm") ||
		strings.HasSuffix(lowerPath, ".xltx")
}

func validateQueryParametersAgainstStatement(
	parameters []api.QueryParameter,
	statement string,
) string {
	configured := make(map[string]struct{}, len(parameters)+1)
	for i := range parameters {
		name := strings.TrimSpace(parameters[i].Name)
		if name == "" {
			continue
		}

		if _, exists := configured[name]; exists {
			return fmt.Sprintf("Parameter %s is duplicated.", name)
		}
		configured[name] = struct{}{}
	}
	configured[sinkbinding.Input] = struct{}{}

	for _, placeholder := range extractStatementPlaceholders(statement) {
		if _, exists := configured[placeholder]; !exists {
			return fmt.Sprintf(
				"Parameter %s is referenced in statement but not configured.",
				placeholder,
			)
		}
	}

	return ""
}

var statementParamRegexp = regexp.MustCompile(`\{\{([a-zA-Z_][a-zA-Z0-9_]*)\}\}`)

func extractStatementPlaceholders(statement string) []string {
	matches := statementParamRegexp.FindAllStringSubmatch(statement, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(matches))
	placeholders := make([]string, 0, len(matches))
	for i := range matches {
		name := matches[i][1]
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		placeholders = append(placeholders, name)
	}

	return placeholders
}

func queryExecutionResultToView(
	querySlug string,
	result api.QueryExecutionResult,
) *views.QueryExecutionResultView {
	discriminator, err := result.Discriminator()
	if err == nil {
		resultType := api.ExecutionResultResultType(discriminator)
		switch resultType {
		case api.ExecutionResultResultTypeSql:
			if sqlResult, parseErr := result.AsSQLExecutionResult(); parseErr == nil {
				if sqlResult.Payload.File != nil {
					rawPayload := ""
					if raw, err := json.MarshalIndent(sqlResult.Payload.File, "", "  "); err == nil {
						rawPayload = string(raw)
					}
					errorMessage := ""
					if sqlResult.Error != nil {
						errorMessage = *sqlResult.Error
					}
					return &views.QueryExecutionResultView{
						QuerySlug: querySlug, Type: sqlResult.Type, Success: sqlResult.Success,
						DurationMs: sqlResult.DurationMs, Error: errorMessage, Raw: rawPayload,
						Summary: []string{
							fmt.Sprintf("Serialized row count: %d", sqlResult.Payload.RowCount),
							fmt.Sprintf("Size: %d bytes", sqlResult.Payload.File.SizeBytes),
						},
					}
				}
				columns := []string{}
				if sqlResult.Payload.ColumnNames != nil {
					columns = append(columns, (*sqlResult.Payload.ColumnNames)...)
				}

				if len(columns) == 0 && len(sqlResult.Payload.Rows) > 0 {
					for key := range sqlResult.Payload.Rows[0] {
						columns = append(columns, key)
					}
					sort.Strings(columns)
				}

				rows := make([]map[string]string, 0, len(sqlResult.Payload.Rows))
				for i := range sqlResult.Payload.Rows {
					row := make(map[string]string, len(columns))
					for _, column := range columns {
						if value := sqlResult.Payload.Rows[i][column]; value != nil {
							row[column] = fmt.Sprintf("%v", value)
						} else {
							row[column] = "NULL"
						}
					}
					rows = append(rows, row)
				}

				errorMessage := ""
				if sqlResult.Error != nil {
					errorMessage = *sqlResult.Error
				}

				return &views.QueryExecutionResultView{
					QuerySlug:  querySlug,
					Type:       sqlResult.Type,
					Success:    sqlResult.Success,
					DurationMs: sqlResult.DurationMs,
					Error:      errorMessage,
					Columns:    columns,
					Rows:       rows,
					Summary: []string{
						fmt.Sprintf("Row count: %d", sqlResult.Payload.RowCount),
					},
				}
			}

		case api.ExecutionResultResultTypeHttp:
			if httpResult, parseErr := result.AsHTTPExecutionResult(); parseErr == nil {
				rawPayload := ""
				rawPayloadValue := httpResult.Payload.Body
				summary := []string{
					fmt.Sprintf("Status code: %d", httpResult.Payload.StatusCode),
				}
				if httpResult.Payload.File != nil {
					rawPayloadValue = httpResult.Payload.File
					if httpResult.Payload.File.Filename != nil &&
						strings.TrimSpace(*httpResult.Payload.File.Filename) != "" {
						summary = append(summary, fmt.Sprintf("Filename: %s", *httpResult.Payload.File.Filename))
					}
					summary = append(
						summary,
						fmt.Sprintf("MIME type: %s", httpResult.Payload.File.MimeType),
						fmt.Sprintf("Size: %d bytes", httpResult.Payload.File.SizeBytes),
					)
				}
				if payload, marshalErr := json.MarshalIndent(
					rawPayloadValue,
					"",
					"  ",
				); marshalErr == nil {
					rawPayload = string(payload)
				}

				errorMessage := ""
				if httpResult.Error != nil {
					errorMessage = *httpResult.Error
				}

				return &views.QueryExecutionResultView{
					QuerySlug:  querySlug,
					Type:       httpResult.Type,
					Success:    httpResult.Success,
					DurationMs: httpResult.DurationMs,
					Error:      errorMessage,
					Summary:    summary,
					Raw:        rawPayload,
				}
			}

		case api.ExecutionResultResultTypeMongodb:
			if mongoResult, parseErr := result.AsMongoDBExecutionResult(); parseErr == nil {
				rawPayload := ""
				if payload, marshalErr := json.MarshalIndent(
					mongoResult.Payload,
					"",
					"  ",
				); marshalErr == nil {
					rawPayload = string(payload)
				}

				errorMessage := ""
				if mongoResult.Error != nil {
					errorMessage = *mongoResult.Error
				}

				return &views.QueryExecutionResultView{
					QuerySlug:  querySlug,
					Type:       mongoResult.Type,
					Success:    mongoResult.Success,
					DurationMs: mongoResult.DurationMs,
					Error:      errorMessage,
					Raw:        rawPayload,
				}
			}

		case api.ExecutionResultResultTypeObjectRows:
			if objectRowsResult, parseErr := result.AsObjectRowsExecutionResult(); parseErr == nil {
				columns := append([]string(nil), objectRowsResult.Payload.ColumnNames...)
				if len(columns) == 0 && len(objectRowsResult.Payload.Rows) > 0 {
					for key := range objectRowsResult.Payload.Rows[0] {
						columns = append(columns, key)
					}
					sort.Strings(columns)
				}

				rows := make([]map[string]string, 0, len(objectRowsResult.Payload.Rows))
				for i := range objectRowsResult.Payload.Rows {
					row := make(map[string]string, len(columns))
					for _, column := range columns {
						value := objectRowsResult.Payload.Rows[i][column]
						row[column] = fmt.Sprintf("%v", value)
						switch value.(type) {
						case map[string]any, []any:
							if encoded, encodeErr := json.Marshal(value); encodeErr == nil {
								row[column] = string(encoded)
							}
						}
					}
					rows = append(rows, row)
				}

				errorMessage := ""
				if objectRowsResult.Error != nil {
					errorMessage = *objectRowsResult.Error
				}

				summary := []string{
					fmt.Sprintf("Object type: %s", objectRowsResult.Payload.ObjectType),
					fmt.Sprintf("Row count: %d", objectRowsResult.Payload.RowCount),
					fmt.Sprintf("Has more: %t", objectRowsResult.Payload.HasMore),
				}
				if objectRowsResult.Payload.Operation != nil &&
					strings.TrimSpace(*objectRowsResult.Payload.Operation) != "" {
					summary = append(
						summary,
						fmt.Sprintf("Operation: %s", *objectRowsResult.Payload.Operation),
					)
				}
				if objectRowsResult.Payload.NextCursor != nil &&
					strings.TrimSpace(*objectRowsResult.Payload.NextCursor) != "" {
					summary = append(
						summary,
						fmt.Sprintf("Next cursor: %s", *objectRowsResult.Payload.NextCursor),
					)
				}

				return &views.QueryExecutionResultView{
					QuerySlug:  querySlug,
					Type:       objectRowsResult.Type,
					Success:    objectRowsResult.Success,
					DurationMs: objectRowsResult.DurationMs,
					Error:      errorMessage,
					Columns:    columns,
					Rows:       rows,
					Summary:    summary,
				}
			}

		case api.ExecutionResultResultTypeFtp:
			if ftpResult, parseErr := result.AsFTPExecutionResult(); parseErr == nil {
				errorMessage := ""
				if ftpResult.Error != nil {
					errorMessage = *ftpResult.Error
				}

				rawPayload := ""
				if payload, marshalErr := json.MarshalIndent(
					ftpResult.Payload,
					"",
					"  ",
				); marshalErr == nil {
					rawPayload = string(payload)
				}

				summary := []string{}
				if ftpResult.Payload.Operation != "" {
					summary = append(
						summary,
						fmt.Sprintf("Operation: %s", ftpResult.Payload.Operation),
					)
				}
				if ftpResult.Payload.Format != "" {
					summary = append(
						summary,
						fmt.Sprintf("Format: %s", ftpResult.Payload.Format),
					)
				}
				if ftpResult.Payload.TargetPath != nil &&
					strings.TrimSpace(*ftpResult.Payload.TargetPath) != "" {
					summary = append(
						summary,
						fmt.Sprintf("Path: %s", *ftpResult.Payload.TargetPath),
					)
				}
				if ftpResult.Payload.Sheet != nil &&
					strings.TrimSpace(*ftpResult.Payload.Sheet) != "" {
					summary = append(summary, fmt.Sprintf("Sheet: %s", *ftpResult.Payload.Sheet))
				}
				if ftpResult.Payload.Range != nil &&
					strings.TrimSpace(*ftpResult.Payload.Range) != "" {
					summary = append(summary, fmt.Sprintf("Range: %s", *ftpResult.Payload.Range))
				}
				if ftpResult.Payload.RowCount != nil {
					summary = append(
						summary,
						fmt.Sprintf("Row count: %d", *ftpResult.Payload.RowCount),
					)
				}

				return &views.QueryExecutionResultView{
					QuerySlug:  querySlug,
					Type:       ftpResult.Type,
					Success:    ftpResult.Success,
					DurationMs: ftpResult.DurationMs,
					Error:      errorMessage,
					Summary:    summary,
					Raw:        rawPayload,
				}
			}

		case api.ExecutionResultResultTypeGit:
			if gitResult, parseErr := result.AsGitExecutionResult(); parseErr == nil {
				errorMessage := ""
				if gitResult.Error != nil {
					errorMessage = *gitResult.Error
				}

				rawPayload := ""
				if payload, marshalErr := json.MarshalIndent(
					gitResult.Payload,
					"",
					"  ",
				); marshalErr == nil {
					rawPayload = string(payload)
				}

				summary := []string{}
				summary = append(summary, fmt.Sprintf("Format: %s", gitResult.Payload.Format))
				if gitResult.Payload.Sheet != nil &&
					strings.TrimSpace(*gitResult.Payload.Sheet) != "" {
					summary = append(summary, fmt.Sprintf("Sheet: %s", *gitResult.Payload.Sheet))
				}
				if gitResult.Payload.Range != nil &&
					strings.TrimSpace(*gitResult.Payload.Range) != "" {
					summary = append(summary, fmt.Sprintf("Range: %s", *gitResult.Payload.Range))
				}
				if gitResult.Payload.RowCount != nil {
					summary = append(
						summary,
						fmt.Sprintf("Row count: %d", *gitResult.Payload.RowCount),
					)
				}

				return &views.QueryExecutionResultView{
					QuerySlug:  querySlug,
					Type:       gitResult.Type,
					Success:    gitResult.Success,
					DurationMs: gitResult.DurationMs,
					Error:      errorMessage,
					Summary:    summary,
					Raw:        rawPayload,
				}
			}

		case api.ExecutionResultResultTypeWorkspaceFile:
			if file, err := result.AsWorkspaceFileExecutionResult(); err == nil {
				raw, marshalErr := json.MarshalIndent(file.Payload, "", "  ")
				if marshalErr != nil {
					raw = []byte("Result payload could not be rendered.")
				}
				summary := []string{fmt.Sprintf("Operation: %s", file.Payload.Operation), fmt.Sprintf("Format: %s", file.Payload.Format)}
				if file.Payload.FileId != nil {
					summary = append(summary, "File ID: "+*file.Payload.FileId)
				}
				if file.Payload.Name != nil {
					summary = append(summary, "Name: "+*file.Payload.Name)
				}
				errorMessage := ""
				if file.Error != nil {
					errorMessage = *file.Error
				}
				return &views.QueryExecutionResultView{QuerySlug: querySlug, Type: file.Type, Success: file.Success, DurationMs: file.DurationMs, Error: errorMessage, Summary: summary, Raw: string(raw)}
			}

		case api.ExecutionResultResultTypeS3:
			if s3Result, parseErr := result.AsS3ExecutionResult(); parseErr == nil {
				errorMessage := ""
				if s3Result.Error != nil {
					errorMessage = *s3Result.Error
				}

				rawPayload := ""
				if payload, marshalErr := json.MarshalIndent(
					s3Result.Payload,
					"",
					"  ",
				); marshalErr == nil {
					rawPayload = string(payload)
				}

				summary := []string{}
				if s3Result.Payload.Operation != "" {
					summary = append(
						summary,
						fmt.Sprintf("Operation: %s", s3Result.Payload.Operation),
					)
				}
				if s3Result.Payload.Format != "" {
					summary = append(
						summary,
						fmt.Sprintf("Format: %s", s3Result.Payload.Format),
					)
				}
				if s3Result.Payload.TargetPath != nil &&
					strings.TrimSpace(*s3Result.Payload.TargetPath) != "" {
					summary = append(
						summary,
						fmt.Sprintf("Key: %s", *s3Result.Payload.TargetPath),
					)
				}
				if s3Result.Payload.Sheet != nil &&
					strings.TrimSpace(*s3Result.Payload.Sheet) != "" {
					summary = append(summary, fmt.Sprintf("Sheet: %s", *s3Result.Payload.Sheet))
				}
				if s3Result.Payload.Range != nil &&
					strings.TrimSpace(*s3Result.Payload.Range) != "" {
					summary = append(summary, fmt.Sprintf("Range: %s", *s3Result.Payload.Range))
				}
				if s3Result.Payload.RowCount != nil {
					summary = append(
						summary,
						fmt.Sprintf("Row count: %d", *s3Result.Payload.RowCount),
					)
				}

				return &views.QueryExecutionResultView{
					QuerySlug:  querySlug,
					Type:       s3Result.Type,
					Success:    s3Result.Success,
					DurationMs: s3Result.DurationMs,
					Error:      errorMessage,
					Summary:    summary,
					Raw:        rawPayload,
				}
			}
		}
	}

	return &views.QueryExecutionResultView{
		QuerySlug: querySlug,
		Type:      "unknown",
		Success:   false,
		Error:     "Unsupported execution result payload.",
	}
}
