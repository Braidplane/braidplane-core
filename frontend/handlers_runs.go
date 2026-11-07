package frontend

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/runlineage"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func (r *Router) handleProjectPipelineRunsPage(c fiber.Ctx) error {
	projectSlug, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	return r.handlePipelineRunsPageWithProject(c, projectSlug.String())
}

func (r *Router) handlePipelineRunsPageWithProject(c fiber.Ctx, activeProjectSlug string) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}
	environmentSlug, environmentStatus, err := r.resolveRuntimeEnvironmentSlug(c)
	if err != nil {
		if environmentStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if strings.TrimSpace(requestRuntimeEnvironmentSlug(c)) == "" {
			return r.renderPipelineRunsPageDefaultEnvironmentError(c, orgSlug.String(), activeProjectSlug, environmentStatus, err)
		}
		return c.Status(environmentStatus).SendString(err.Error())
	}

	statusFilter := strings.TrimSpace(c.Query("status"))
	pipelineSlugFilter := strings.TrimSpace(c.Query("pipeline_slug"))
	modeFilter := strings.TrimSpace(c.Query("mode"))
	backfillSeriesFilter := strings.TrimSpace(c.Query("backfill_series_slug"))

	params := &api.ListPipelineRunsParams{}
	limit := api.LimitParam(20)
	params.Limit = &limit

	cursor := strings.TrimSpace(c.Query("cursor"))
	if cursor != "" {
		cursorParam := cursor
		params.Cursor = &cursorParam
	}

	direction := strings.TrimSpace(c.Query("direction"))
	switch direction {
	case pageDirectionPrev:
		d := api.ListPipelineRunsParamsDirection(api.PageDirectionParamPrev)
		params.Direction = &d
	case pageDirectionNext:
		d := api.ListPipelineRunsParamsDirection(api.PageDirectionParamNext)
		params.Direction = &d
	}

	if statusFilter != "" {
		s := api.ListPipelineRunsParamsStatus(statusFilter)
		params.Status = &s
	}

	if pipelineSlugFilter != "" {
		pipelineSlug := pipelineSlugFilter
		params.PipelineSlug = &pipelineSlug
	}
	if modeFilter != "" {
		mode := api.PipelineRunMode(modeFilter)
		params.Mode = &mode
	}
	if backfillSeriesFilter != "" {
		backfillSeriesSlug := backfillSeriesFilter
		params.BackfillSeriesSlug = &backfillSeriesSlug
	}

	runList, status, errorResponse, listErr := r.listPipelineRunsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlugForAPI(activeProjectSlug),
		environmentSlug.String(),
		params,
	)
	if listErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while loading pipeline runs."
		}

		shellData := r.buildRunsShellData(
			c,
			orgSlug.String(),
			activeProjectSlug,
			environmentSlug.String(),
			message,
			backendErrorDetails("list pipeline runs", status, errorResponse, listErr),
		)

		c.Status(status)
		return renderTempl(
			c,
			views.PipelineRunsPage(
				shellData,
				orgSlug.String(),
				activeProjectSlug,
				[]api.PipelineRunMetadata{},
				false,
				"",
				"",
				cursor,
				direction,
				environmentSlug.String(),
				statusFilter,
				pipelineSlugFilter,
				modeFilter,
				backfillSeriesFilter,
				views.PipelineRunSelectedDetail{},
			),
		)
	}

	nextCursor := ""
	if runList.NextCursor != nil {
		nextCursor = *runList.NextCursor
	}
	prevCursor := ""
	if runList.PrevCursor != nil {
		prevCursor = *runList.PrevCursor
	}
	selectedRunSlug := strings.TrimSpace(c.Query("run_slug"))
	if selectedRunSlug == "" && c.Query("preview") != "closed" && len(runList.Runs) > 0 {
		selectedRunSlug = runList.Runs[0].RunSlug
	}
	selectedDetail := r.loadPipelineRunSelectedDetail(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		activeProjectSlug,
		environmentSlug.String(),
		selectedRunSlug,
	)

	shellData := r.buildRunsShellData(c, orgSlug.String(), activeProjectSlug, environmentSlug.String(), "", "")

	return renderTempl(
		c,
		views.PipelineRunsPage(
			shellData,
			orgSlug.String(),
			activeProjectSlug,
			runList.Runs,
			runList.HasMore,
			nextCursor,
			prevCursor,
			cursor,
			direction,
			environmentSlug.String(),
			statusFilter,
			pipelineSlugFilter,
			modeFilter,
			backfillSeriesFilter,
			selectedDetail,
		),
	)
}

func (r *Router) handleProjectPipelineRunDetailPage(c fiber.Ctx) error {
	projectSlug, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid project slug")
	}

	return r.handlePipelineRunDetailPageWithProject(c, projectSlug.String())
}

func (r *Router) handlePipelineRunDetailPageWithProject(c fiber.Ctx, activeProjectSlug string) error {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid organization slug")
	}

	runSlug, err := slug.New(c.Params("runSlug"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("invalid run slug")
	}
	environmentSlug, environmentStatus, err := r.resolveRuntimeEnvironmentSlug(c)
	if err != nil {
		if environmentStatus == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if strings.TrimSpace(requestRuntimeEnvironmentSlug(c)) == "" {
			return r.renderPipelineRunDetailPageDefaultEnvironmentError(c, orgSlug.String(), activeProjectSlug, runSlug.String(), environmentStatus, err)
		}
		return c.Status(environmentStatus).SendString(err.Error())
	}

	after, parseProgressErr := strconv.ParseInt(c.Query("progress_events", "0"), 10, 64)
	if parseProgressErr != nil || after < 0 {
		return c.Status(http.StatusBadRequest).SendString("invalid progress cursor")
	}
	progress, progressStatus, progressErr := r.getRunProgressViaAPI(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), activeProjectSlug, environmentSlug.String(), runSlug.String(), after)
	if progressStatus == http.StatusBadRequest && after > 0 {
		progress, progressStatus, progressErr = r.getRunProgressViaAPI(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), activeProjectSlug, environmentSlug.String(), runSlug.String(), 0)
	}
	if c.Query("progress_poll") == "1" && progressErr != nil {
		if progressStatus == http.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		return c.Status(progressStatus).SendString("Progress unavailable; retained evidence has not been replaced.")
	}
	if unchangedProgressPoll(c, progress) {
		return renderRunProgressPoll(c, "/orgs/"+orgSlug.String()+"/projects/"+activeProjectSlug+"/runs/"+runSlug.String()+"?environment_slug="+environmentSlug.String(), progress)
	}
	runRecord, status, errorResponse, getErr := r.getPipelineRunViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlugForAPI(activeProjectSlug),
		environmentSlug.String(),
		runSlug.String(),
	)
	if getErr != nil {
		if status == fiber.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}

		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while loading run details."
		}

		shellData := r.buildRunDetailShellData(
			c,
			orgSlug.String(),
			activeProjectSlug,
			environmentSlug.String(),
			runSlug.String(),
			message,
			errorPopupDetails(errorResponse, fmt.Sprintf("get run details: %v", getErr)),
		)

		c.Status(status)
		return renderTempl(
			c,
			views.PipelineRunDetailUnavailablePage(
				shellData,
				orgSlug.String(),
				activeProjectSlug,
				environmentSlug.String(),
				runSlug.String(),
				message,
			),
		)
	}
	retryAttempts, retryAttemptsUnavailable, retryAttemptsErrorSummary := r.loadPipelineRunTriggerRetryAttempts(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		activeProjectSlug,
		environmentSlug.String(),
		runSlug.String(),
	)
	lineage, inspection := r.loadPipelineRunLineage(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		activeProjectSlug,
		*runRecord,
		strings.TrimSpace(c.Query("lineage_step")),
		c.OriginalURL(),
	)

	c.Set(fiber.HeaderCacheControl, "no-store")
	c.SetContext(views.WithDefinitionInspection(c.Context(), inspection))
	if user, _, _, err := r.getProfileViaAPI(c.Context(), c.Get(fiber.HeaderCookie)); err == nil && user != nil && user.Timezone != nil {
		c.SetContext(views.WithRunTimezone(c.Context(), *user.Timezone))
	}
	shellData := r.buildRunDetailShellData(c, orgSlug.String(), activeProjectSlug, environmentSlug.String(), runSlug.String(), c.Query("recovery_error"), "")
	permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String())
	if lineage.SelectedIndex >= 0 {
		node := lineage.Nodes[lineage.SelectedIndex]
		actions := r.loadRunStepActions(c, orgSlug.String(), environmentSlug.String(), node, permissions)
		if node.Kind == "transform" && permissions.Can("pipelines", "update") {
			current, _, _, err := r.getPipelineViaAPI(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), activeProjectSlug, runRecord.Metadata.PipelineSlug)
			if err == nil && current != nil {
				query := url.Values{"org_slug": {orgSlug.String()}, "project_slug": {activeProjectSlug}, "pipeline_slug": {runRecord.Metadata.PipelineSlug}, "target_environment_slug": {environmentSlug.String()}}
				actions.PipelineHref = "/builder?" + query.Encode()
			} else {
				actions.Message = "Current pipeline is unavailable or deleted. The executed definition remains historical evidence."
			}
		}
		c.SetContext(views.WithRunStepActions(c.Context(), actions))
	}

	c.SetContext(views.WithRunProgress(c.Context(), views.RunProgressInspection{Evidence: progress, Poll: runProgressShouldPoll(runRecord.Metadata.Status, retryAttempts, c.Query("section")), VersionRecorded: runRecord.Metadata.PipelineVersionSpecHash != ""}))
	completion := views.RunCompletionInspection{}
	if c.Query("section") == "connected" {
		completion = r.loadConnectedRuns(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), activeProjectSlug, environmentSlug.String(), runSlug.String())
	}
	if c.Query("section") == "completion" {
		completion = r.loadPipelineRunCompletion(c.Context(), c.Get(fiber.HeaderCookie), orgSlug.String(), activeProjectSlug, environmentSlug.String(), runSlug.String(), runRecord.Metadata.PipelineSlug)
	}

	return renderTempl(
		c,
		views.PipelineRunDetailPage(
			shellData,
			orgSlug.String(),
			activeProjectSlug,
			environmentSlug.String(),
			*runRecord,
			retryAttempts,
			retryAttemptsUnavailable,
			retryAttemptsErrorSummary,
			lineage,
			permissions,
			completion,
		),
	)
}

func (r *Router) loadPipelineRunSelectedDetail(
	ctx context.Context,
	cookieHeader string,
	orgSlug string,
	activeProjectSlug string,
	environmentSlug string,
	runSlug string,
) views.PipelineRunSelectedDetail {
	selected := views.PipelineRunSelectedDetail{Slug: strings.TrimSpace(runSlug)}
	if selected.Slug == "" {
		return selected
	}
	if _, err := slug.New(selected.Slug); err != nil {
		selected.ErrorSummary = "Run details are unavailable."
		return selected
	}

	runRecord, _, errorResponse, getErr := r.getPipelineRunViaAPI(
		ctx,
		cookieHeader,
		orgSlug,
		activeProjectSlug,
		environmentSlug,
		selected.Slug,
	)
	if getErr != nil {
		message := messageFromErrorResponse(errorResponse)
		if message == "" {
			message = "Something went wrong while loading run details."
		}
		selected.ErrorSummary = message
		return selected
	}

	selected.Run = runRecord
	return selected
}

func (r *Router) loadPipelineRunLineage(
	ctx context.Context,
	cookieHeader string,
	orgSlug string,
	activeProjectSlug string,
	run api.PipelineRunRecord,
	requestedStepSlug string,
	baseHref string,
) (runlineage.View, views.DefinitionInspection) {
	inspection := views.DefinitionInspection{Authority: "Executed version", Hash: run.Metadata.PipelineVersionSpecHash}
	specHash := strings.TrimSpace(run.Metadata.PipelineVersionSpecHash)
	if specHash == "" {
		inspection.Message = "The exact pipeline version was not recorded for this run."
		return runlineage.Unavailable(inspection.Message), inspection
	}

	version, status, _, getErr := r.getPipelineVersionViaAPI(
		ctx,
		cookieHeader,
		orgSlug,
		activeProjectSlug,
		run.Metadata.PipelineSlug,
		specHash,
	)
	if getErr != nil {
		inspection.Message = definitionUnavailable(status)
		return runlineage.Unavailable(inspection.Message), inspection
	}

	if version.SpecHash != specHash {
		inspection.Message = "The returned definition does not match this run's recorded version."
		return runlineage.Unavailable(inspection.Message), inspection
	}
	inspection.Spec = &version.Spec
	if !version.CreatedAt.IsZero() {
		inspection.Created = version.CreatedAt.String()
	}
	if version.GitCommitSha != nil {
		inspection.Commit = *version.GitCommitSha
	}
	view, err := runlineage.Build(*version, run, requestedStepSlug, baseHref)
	if err != nil {
		return runlineage.Unavailable("The immutable pipeline version could not be projected as execution topology."), inspection
	}
	if view.SelectedIndex >= 0 {
		inspection.SelectedStep = view.Nodes[view.SelectedIndex].Slug
	}
	return view, inspection
}

func (r *Router) loadPipelineRunTriggerRetryAttempts(
	ctx context.Context,
	cookieHeader string,
	orgSlug string,
	activeProjectSlug string,
	environmentSlug string,
	runSlug string,
) ([]api.PipelineRunTriggerRetryAttempt, bool, string) {
	retryAttempts, _, errorResponse, retryErr := r.listPipelineRunTriggerRetryAttemptsViaAPI(
		ctx,
		cookieHeader,
		orgSlug,
		activeProjectSlug,
		environmentSlug,
		runSlug,
	)
	if retryErr == nil {
		return retryAttempts.RetryAttempts, false, ""
	}

	message := messageFromErrorResponse(errorResponse)
	if message == "" {
		message = "Retry diagnostics are unavailable."
	}
	return nil, true, message
}

func (r *Router) buildRunsShellData(
	c fiber.Ctx,
	orgSlug string,
	activeProjectSlug string,
	environmentSlug string,
	pageError string,
	pageErrorDetails string,
) views.AuthenticatedShellData {
	var shellData views.AuthenticatedShellData
	if strings.TrimSpace(activeProjectSlug) != "" {
		shellData = r.buildShellDataForProjectPage(
			c,
			"Pipeline runs",
			orgSlug,
			activeProjectSlug,
			pageError,
			pageErrorDetails,
			c.Get(fiber.HeaderCookie),
		)
		shellData.Subtitle = "Pipeline runs across this project, including recent failures and replay context."
		return preserveRunsEnvironmentInShellData(shellData, environmentSlug)
	}

	shellData = r.buildShellDataForOrganizationPage(
		c,
		"Pipeline runs",
		orgSlug,
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Pipeline runs across this organization, including recent failures and replay context."
	return preserveRunsEnvironmentInShellData(shellData, environmentSlug)
}

func (r *Router) renderPipelineRunsPageDefaultEnvironmentError(
	c fiber.Ctx,
	orgSlug string,
	activeProjectSlug string,
	status int,
	err error,
) error {
	pageError := projectEnvironmentsLoadErrorMessage
	if status == fiber.StatusBadRequest {
		pageError = "Select a project runtime target before viewing pipeline runs."
	}
	statusFilter := strings.TrimSpace(c.Query("status"))
	pipelineSlugFilter := strings.TrimSpace(c.Query("pipeline_slug"))
	shellData := r.buildRunsShellData(
		c,
		orgSlug,
		activeProjectSlug,
		"",
		pageError,
		fmt.Sprintf("resolve default runtime environment: %v", err),
	)

	c.Status(status)
	return renderTempl(
		c,
		views.PipelineRunsPage(
			shellData,
			orgSlug,
			activeProjectSlug,
			[]api.PipelineRunMetadata{},
			false,
			"",
			"",
			"",
			"",
			"",
			statusFilter,
			pipelineSlugFilter,
			"",
			"",
			views.PipelineRunSelectedDetail{},
		),
	)
}

func (r *Router) renderPipelineRunDetailPageDefaultEnvironmentError(
	c fiber.Ctx,
	orgSlug string,
	activeProjectSlug string,
	runSlug string,
	status int,
	err error,
) error {
	pageError := projectEnvironmentsLoadErrorMessage
	if status == fiber.StatusBadRequest {
		pageError = "Select a project runtime target before viewing run details."
	}
	shellData := r.buildRunDetailShellData(
		c,
		orgSlug,
		activeProjectSlug,
		"",
		runSlug,
		pageError,
		fmt.Sprintf("resolve default runtime environment: %v", err),
	)

	c.Status(status)
	return renderTempl(
		c,
		views.PipelineRunDetailUnavailablePage(
			shellData,
			orgSlug,
			activeProjectSlug,
			"",
			runSlug,
			pageError,
		),
	)
}

func (r *Router) buildRunDetailShellData(
	c fiber.Ctx,
	orgSlug string,
	activeProjectSlug string,
	environmentSlug string,
	runSlug string,
	pageError string,
	pageErrorDetails string,
) views.AuthenticatedShellData {
	if strings.TrimSpace(activeProjectSlug) == "" {
		shellData := r.buildShellDataForOrganizationPage(
			c,
			"Run details",
			orgSlug,
			pageError,
			pageErrorDetails,
			c.Get(fiber.HeaderCookie),
		)
		shellData.Subtitle = "Timeline and diagnostics for one pipeline execution."
		shellData.Breadcrumbs = buildOrganizationDetailBreadcrumbs(
			shellData.ActiveOrganizationName,
			orgSlug,
			"Runs",
			"/organizations/"+orgSlug+"/projects",
			runSlug,
		)
		return preserveRunsEnvironmentInShellData(shellData, environmentSlug)
	}

	shellData := r.buildShellDataForProjectPage(
		c,
		"Run details",
		orgSlug,
		activeProjectSlug,
		pageError,
		pageErrorDetails,
		c.Get(fiber.HeaderCookie),
	)
	shellData.Subtitle = "Timeline and diagnostics for one pipeline execution."
	runsHref := "/orgs/" + orgSlug + "/projects/" + strings.TrimSpace(activeProjectSlug) + "/runs"
	environmentRunsHref := appendRunsEnvironmentQuery(runsHref, environmentSlug)
	shellData.Breadcrumbs = buildProjectBreadcrumbs(
		environmentRunsHref,
		orgSlug,
		shellData.ActiveOrganizationName,
		activeProjectSlug,
		shellData.ActiveProjectName,
	)
	if len(shellData.Breadcrumbs) > 0 {
		shellData.Breadcrumbs[len(shellData.Breadcrumbs)-1].Href = environmentRunsHref
	}
	shellData.Breadcrumbs = append(shellData.Breadcrumbs, views.BreadcrumbItem{Label: runSlug})
	return preserveRunsEnvironmentInShellData(shellData, environmentSlug)
}

func preserveRunsEnvironmentInShellData(
	shellData views.AuthenticatedShellData,
	environmentSlug string,
) views.AuthenticatedShellData {
	environmentSlug = strings.TrimSpace(environmentSlug)
	if environmentSlug == "" {
		return shellData
	}

	for i := range shellData.NavItems {
		item := &shellData.NavItems[i]
		if item.Label != "Runs" || !item.Enabled || strings.TrimSpace(item.Href) == "" {
			continue
		}

		item.Href = appendRunsEnvironmentQuery(item.Href, environmentSlug)
	}
	return shellData
}

func appendRunsEnvironmentQuery(href, environmentSlug string) string {
	if strings.TrimSpace(href) == "" || strings.TrimSpace(environmentSlug) == "" {
		return href
	}

	parsed, err := url.Parse(href)
	if err != nil {
		return href
	}

	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return href
	}
	values.Set("environment_slug", strings.TrimSpace(environmentSlug))
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

func (r *Router) listPipelineRunsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	params *api.ListPipelineRunsParams,
) (*api.PipelineRunListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListPipelineRunsWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		environmentSlug,
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
	return nil, resp.StatusCode(), errResp, xerror.Unexpected().Detailf(
		"list pipeline runs api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) getPipelineRunViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	runSlug string,
) (*api.PipelineRunRecord, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetPipelineRunWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		environmentSlug,
		runSlug,
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
		"get pipeline run api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) getPipelineVersionViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	pipelineSlug string,
	specHash string,
) (*api.PipelineVersion, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.GetPipelineVersionWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		pipelineSlug,
		specHash,
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
		"get pipeline version api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) listPipelineRunTriggerRetryAttemptsViaAPI(
	ctx context.Context,
	cookieHeader string,
	organizationSlug string,
	projectSlug string,
	environmentSlug string,
	runSlug string,
) (*api.PipelineRunTriggerRetryAttemptListResponse, int, *api.ErrorResponse, error) {
	resp, err := r.apiClient.ListPipelineRunTriggerRetryAttemptsWithResponse(
		ctx,
		organizationSlug,
		projectSlugForAPI(projectSlug),
		environmentSlug,
		runSlug,
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
		"list pipeline run trigger retry attempts api returned status %d",
		resp.StatusCode(),
	)
}

func (r *Router) resolveRuntimeEnvironmentSlug(c fiber.Ctx) (slug.Slug, int, error) {
	rawEnvironmentSlug := requestRuntimeEnvironmentSlug(c)
	if rawEnvironmentSlug != "" {
		environmentSlug, err := slug.New(rawEnvironmentSlug)
		if err != nil {
			return "", fiber.StatusBadRequest, err
		}
		return environmentSlug, fiber.StatusOK, nil
	}

	defaultEnvironmentSlug, status, defaultErr := r.defaultRuntimeEnvironmentSlug(c)
	if defaultErr != nil {
		return "", status, defaultErr
	}

	return defaultEnvironmentSlug, fiber.StatusOK, nil
}

func requiredRuntimeEnvironmentSlug(c fiber.Ctx) (slug.Slug, error) {
	raw := requestRuntimeEnvironmentSlug(c)
	if raw == "" {
		return "", xerror.InvalidRequired(xerror.ProjectEnvironment).Detail("environment_slug is required")
	}

	return slug.New(raw)
}

func optionalRuntimeEnvironmentSlug(c fiber.Ctx) (string, error) {
	raw := requestRuntimeEnvironmentSlug(c)
	if raw == "" {
		return "", nil
	}

	environmentSlug, err := slug.New(raw)
	if err != nil {
		return "", err
	}
	return environmentSlug.String(), nil
}

// requestRuntimeEnvironmentSlug reads the explicit project runtime target from query or form values.
func requestRuntimeEnvironmentSlug(c fiber.Ctx) string {
	if environment := c.Params("environmentSlug"); environment != "" {
		return environment
	}
	if c.Path() == builderPath {
		raw := strings.TrimSpace(c.Query("target_environment_slug"))
		if raw == "" {
			raw = strings.TrimSpace(c.FormValue("target_environment_slug"))
		}
		if raw == "" {
			raw = strings.TrimSpace(c.Query("environment_slug"))
		}
		if raw == "" {
			raw = strings.TrimSpace(c.FormValue("environment_slug"))
		}
		return raw
	}

	raw := strings.TrimSpace(c.Query("environment_slug"))
	if raw == "" {
		raw = strings.TrimSpace(c.FormValue("environment_slug"))
	}
	if raw == "" {
		raw = strings.TrimSpace(c.Query("target_environment_slug"))
	}
	if raw == "" {
		raw = strings.TrimSpace(c.FormValue("target_environment_slug"))
	}
	return raw
}

func (r *Router) defaultRuntimeEnvironmentSlug(c fiber.Ctx) (slug.Slug, int, error) {
	orgSlug, err := slug.New(c.Params("slug"))
	if err != nil {
		return "", fiber.StatusBadRequest, err
	}
	projectSlug, err := requiredProjectSlug(c)
	if err != nil {
		return "", fiber.StatusBadRequest, err
	}

	projectEnvironments, status, _, listErr := r.listProjectEnvironmentsViaAPI(
		c.Context(),
		c.Get(fiber.HeaderCookie),
		orgSlug.String(),
		projectSlug,
	)
	if listErr != nil {
		return "", status, listErr
	}

	defaultEnvironmentSlug := selectDefaultProjectEnvironmentSlug(projectEnvironments.ProjectEnvironments)
	rememberedEnvironmentSlug := r.dashboardRequestedEnvironmentSlug(c, orgSlug.String(), projectSlug)
	if projectEnvironmentSlugIsEnabled(projectEnvironments.ProjectEnvironments, rememberedEnvironmentSlug) {
		defaultEnvironmentSlug = rememberedEnvironmentSlug
	}
	if defaultEnvironmentSlug == "" {
		return "", fiber.StatusBadRequest, xerror.InvalidRequired(xerror.ProjectEnvironment).Detail("environment_slug is required")
	}

	environmentSlug, err := slug.New(defaultEnvironmentSlug)
	if err != nil {
		return "", fiber.StatusBadRequest, err
	}
	return environmentSlug, fiber.StatusOK, nil
}

func (r *Router) handleRecoverPipelineRunSubmit(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	project, err := requiredProjectSlug(c)
	if err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	run, err := slug.New(c.Params("runSlug"))
	if err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	environment, err := slug.New(c.FormValue("environment_slug"))
	if err != nil {
		return c.SendStatus(fiber.StatusBadRequest)
	}
	permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), org.String())
	if !permissions.Can("pipelines", "execute") {
		return c.SendStatus(fiber.StatusForbidden)
	}
	payload := api.RecoverPipelineRunRequest{}
	if value := strings.TrimSpace(c.FormValue("protected_environment_confirmation")); value != "" {
		confirmation, err := slug.New(value)
		if err != nil {
			return c.SendStatus(fiber.StatusBadRequest)
		}
		value := confirmation.String()
		payload.ProtectedEnvironmentConfirmation = &value
	}
	requestCtx, cancel := frontendAPIExecutionContext(c.Context(), 0)
	defer cancel()
	response, err := r.apiClient.RecoverPipelineRunWithResponse(requestCtx, org.String(), project, environment.String(), run.String(), payload, frontendRequestEditor(requestCtx, c.Get(fiber.HeaderCookie)))
	back := fmt.Sprintf("/orgs/%s/projects/%s/runs/%s?environment_slug=%s&section=attempts", org, project, run, environment)
	if err != nil {
		return redirectHTMXOrSeeOther(c, back+"&recovery_error="+url.QueryEscape("Recovery response unavailable. Inspect the run and provider outcome before trying again."))
	}
	if response.JSON200 != nil {
		return redirectHTMXOrSeeOther(c, fmt.Sprintf("/orgs/%s/projects/%s/runs/%s?environment_slug=%s", org, project, response.JSON200.Metadata.RunSlug, environment))
	}
	message := formFeedbackFromErrorResponse(parseAPIErrorResponse(response.Body), "Recovery was refused. Verify retained inputs, permissions and the provider outcome.")
	return redirectHTMXOrSeeOther(c, back+"&recovery_error="+url.QueryEscape(message))
}

func (r *Router) loadRunStepActions(c fiber.Ctx, org, environment string, node runlineage.Node, permissions views.OrganizationPermissions) views.RunStepActions {
	actions := views.RunStepActions{}
	if node.Kind == "transform" {
		return actions
	}
	dataSource, query := "", ""
	if node.Snapshot != nil {
		if node.Snapshot.Input.DataSourceSlug != nil {
			dataSource = *node.Snapshot.Input.DataSourceSlug
		}
		if node.Snapshot.Input.OperationRef != nil {
			query = *node.Snapshot.Input.OperationRef
		}
	}
	if query == "" && !node.Planned.UsesInline {
		query = node.Planned.OperationRef
	}
	if dataSource == "" && node.Failure != nil && node.Failure.DataSourceSlug != nil {
		dataSource = *node.Failure.DataSourceSlug
	}
	if dataSource == "" {
		actions.Message = "Current connection unavailable: no resolved data source was recorded. Logical references do not identify a current connection."
		return actions
	}
	if !permissions.Can("datasources", "read") {
		actions.Message = "Current connection is restricted by your permissions."
		return actions
	}
	source, status, _, err := r.getDataSourceViaAPI(c.Context(), c.Get(fiber.HeaderCookie), org, dataSource)
	if err != nil || source == nil {
		actions.Message = "Current connection unavailable or deleted. " + definitionUnavailable(status)
		return actions
	}
	actions.DataSourceHref = views.DataSourceDetailHref(org, environment, dataSource)
	if query != "" && permissions.Can("queries", "read") {
		current, _, _, err := r.getQueryViaAPI(c.Context(), c.Get(fiber.HeaderCookie), org, dataSource, query)
		if err == nil && current != nil && current.DeletedAt == nil {
			actions.QueryHref = "/orgs/" + url.PathEscape(org) + "/data-sources/" + url.PathEscape(dataSource) + "/queries/" + url.PathEscape(query) + "?environment_slug=" + url.QueryEscape(environment)
		} else {
			actions.Message = "Current saved query is unavailable or deleted. Its recorded identity remains historical evidence."
		}
	}
	if query != "" && !permissions.Can("queries", "read") {
		actions.Message = "Current saved query is restricted by your permissions."
	}
	return actions
}
