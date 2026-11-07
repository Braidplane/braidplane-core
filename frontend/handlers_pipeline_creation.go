package frontend

import (
	"net/http"
	"strings"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (r *Router) handleBuilderCreatePipeline(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("Invalid organization.")
	}
	project, err := slug.New(c.Params("projectSlug"))
	if err != nil {
		return c.Status(http.StatusBadRequest).SendString("Invalid project.")
	}
	cookie := c.Get(fiber.HeaderCookie)
	if !r.resolveOrganizationPermissions(c.Context(), cookie, org.String()).Can("pipelines", "create") {
		return c.Status(http.StatusForbidden).JSON(fiber.Map{"message": "Pipeline creation needs access."})
	}
	state, _, err := deserializeBuilderStateWithIssues(c.Body())
	if err != nil || state.PipelineSpec.Metadata.Slug == views.BuilderDraftPipelineSlug || strings.TrimSpace(state.PipelineSpec.Metadata.Name) == "" {
		return c.Status(http.StatusUnprocessableEntity).JSON(fiber.Map{"message": "Name the pipeline, use a unique slug and resolve graph validation issues."})
	}
	pipeline, err := builderStateToPipeline(state)
	if err != nil {
		return c.Status(http.StatusUnprocessableEntity).JSON(fiber.Map{"message": "Resolve graph validation issues before proposing the pipeline."})
	}
	spec, err := pipelineToAPIPipelineSpec(pipeline)
	if err != nil {
		return c.Status(http.StatusUnprocessableEntity).JSON(fiber.Map{"message": "The pipeline definition could not be prepared."})
	}
	response, err := r.apiClient.CreatePipelineProposalWithResponse(c.Context(), org.String(), project.String(), spec, frontendRequestEditor(c.Context(), cookie))
	if err != nil {
		return c.Status(http.StatusBadGateway).JSON(fiber.Map{"message": "The proposal outcome could not be loaded. Retry the same definition to recover its progress."})
	}
	if response.JSON200 == nil {
		return c.Status(response.StatusCode()).JSON(fiber.Map{"message": dashboardErrorMessage("The pipeline could not be proposed. Check repository access and retry.", parseAPIErrorResponse(response.Body))})
	}
	if response.JSON200.ProjectSlug != project.String() || response.JSON200.PipelineSlug != spec.Metadata.Slug {
		return c.Status(http.StatusBadGateway).JSON(fiber.Map{"message": "The proposal returned an unexpected identity."})
	}
	if _, err := slug.New(response.JSON200.Slug); err != nil {
		return c.Status(http.StatusBadGateway).JSON(fiber.Map{"message": "The proposal returned an invalid identity."})
	}
	return c.JSON(fiber.Map{"progress_url": views.PipelineCreationProgressHref(org.String(), project.String(), response.JSON200.Slug, requestRuntimeEnvironmentSlug(c))})
}

func (r *Router) handleQueryPipelineSetup(c fiber.Ctx) error {
	for _, key := range []string{"slug", "dsSlug", "querySlug"} {
		if _, err := slug.New(c.Params(key)); err != nil {
			return c.Status(http.StatusBadRequest).SendString("Invalid query context.")
		}
	}
	org, source, querySlug := c.Params("slug"), c.Params("dsSlug"), c.Params("querySlug")
	cookie := c.Get(fiber.HeaderCookie)
	data := views.QueryPipelineSetupData{Org: org, DataSource: source, Project: strings.TrimSpace(c.Query("project")), ParameterValues: map[string]string{}}
	data.Permissions = r.resolveOrganizationPermissions(c.Context(), cookie, org)
	query, status, _, err := r.getQueryViaAPI(c.Context(), cookie, org, source, querySlug)
	if err != nil || query == nil {
		if status < http.StatusBadRequest {
			status = http.StatusBadGateway
		}
		return c.Status(status).SendString("The saved query could not be loaded.")
	}
	if query.DeletedAt != nil {
		return c.Status(http.StatusConflict).SendString("Restore the saved query before building a pipeline.")
	}
	data.Query, data.Name, data.QueryRevision = query, query.Name+" pipeline", query.UpdatedAt.Format(time.RFC3339Nano)
	if c.Method() == http.MethodPost {
		data.Project, data.Name, data.Slug = strings.TrimSpace(c.FormValue("project")), strings.TrimSpace(c.FormValue("name")), strings.TrimSpace(c.FormValue("pipeline_slug"))
		for _, parameter := range query.Parameters {
			data.ParameterValues[parameter.Name] = c.FormValue(views.QueryExecutionFieldName(parameter.Name))
		}
		if !data.Permissions.Can("pipelines", "create") {
			return c.Status(http.StatusForbidden).SendString("Pipeline creation needs access.")
		}
	}
	projects, _, _, listErr := r.listProjectsViaAPI(c.Context(), cookie, org)
	if listErr != nil {
		data.Error = "Projects could not be loaded. Refresh to try again."
	} else {
		data.Projects = projects.Projects
	}
	if data.Project != "" {
		if _, err := slug.New(data.Project); err != nil {
			return c.Status(http.StatusBadRequest).SendString("Invalid project.")
		}
		project, _, _, projectErr := r.getProjectViaAPI(c.Context(), cookie, org, data.Project)
		if projectErr != nil || project == nil {
			data.Error = "The selected project could not be loaded. Check access and try again."
		} else {
			data.ProjectAvailable, data.BackendConfigured = true, project.PipelineBackend != nil
		}
	}
	if c.Method() == http.MethodPost && data.Error == "" {
		switch {
		case data.Project == "" || !data.BackendConfigured:
			data.Error = "Choose a project and connect its pipeline repository first."
		case c.FormValue("query_revision") != data.QueryRevision:
			data.Error = "The saved query changed. Review its current definition before proposing again."
		case data.Name == "":
			data.Error = "Pipeline name is required."
		default:
			payload, message := buildExecuteQueryPayload(c, *query)
			data.Error = message
			if message == "" {
				spec, specErr := queryPipelineSpec(*query, data.Name, data.Slug, payload.Parameters)
				if specErr != nil {
					data.Error = "Check the pipeline slug and whether this query supports pipeline sources."
				} else {
					response, createErr := r.apiClient.CreatePipelineProposalWithResponse(c.Context(), org, data.Project, spec, frontendRequestEditor(c.Context(), cookie))
					switch {
					case createErr != nil:
						data.Error = "The proposal outcome could not be loaded. Submit the same definition to recover its progress."
					case response.JSON200 == nil:
						data.Error = dashboardErrorMessage("The pipeline could not be proposed. Check repository access and try again.", parseAPIErrorResponse(response.Body))
					case response.JSON200.ProjectSlug != data.Project || response.JSON200.PipelineSlug != spec.Metadata.Slug:
						data.Error = "The proposal returned an unexpected identity."
					default:
						return c.Redirect().Status(http.StatusSeeOther).To(views.PipelineCreationProgressHref(org, data.Project, response.JSON200.Slug, ""))
					}
				}
			}
		}
	}
	if data.Error != "" {
		c.Status(http.StatusUnprocessableEntity)
	}
	shell := r.buildShellDataForOrganizationPage(c, "Build pipeline", org, "", "", cookie)
	return renderTempl(c, views.QueryPipelineSetupPage(shell, data))
}

func queryPipelineSpec(query api.Query, name, rawSlug string, parameters map[string]any) (api.PipelineSpec, error) {
	pipelineSlug, err := slug.FromName(name)
	if rawSlug != "" {
		pipelineSlug, err = slug.New(rawSlug)
	}
	if err != nil {
		return api.PipelineSpec{}, err
	}
	connector, err := query.QueryStatement.Discriminator()
	if err != nil {
		return api.PipelineSpec{}, err
	}
	for _, capability := range pipelinespec.AllConnectorCapabilities() {
		if capability.DataSourceType.String() == connector && capability.SupportsPipelineSource {
			return pipelineToAPIPipelineSpec(&pipelinespec.Pipeline{Metadata: pipelinespec.Metadata{Slug: pipelineSlug, Name: name}, Steps: []pipelinespec.Step{
				{Kind: "query", Slug: "fetch", Query: &pipelinespec.QueryRef{DataSourceSlug: "source", OperationType: capability.QueryOperationType, OperationRef: slug.Slug(query.Slug), Parameters: parameters}},
			}})
		}
	}
	return api.PipelineSpec{}, fiber.NewError(http.StatusUnprocessableEntity, "Unsupported pipeline source.")
}

func (r *Router) handlePipelineCreationProgress(c fiber.Ctx) error {
	for _, key := range []string{"slug", "projectSlug", "creationSlug"} {
		if _, err := slug.New(c.Params(key)); err != nil {
			return c.Status(http.StatusBadRequest).SendString("Invalid proposal context.")
		}
	}
	org, project, creation := c.Params("slug"), c.Params("projectSlug"), c.Params("creationSlug")
	cookie := c.Get(fiber.HeaderCookie)
	response, err := r.apiClient.GetPipelineCreationProposalWithResponse(c.Context(), org, project, creation, frontendRequestEditor(c.Context(), cookie))
	if err != nil {
		return c.Status(http.StatusBadGateway).SendString("Proposal progress could not be loaded. Refresh to try again.")
	}
	if response.JSON200 == nil {
		return c.Status(response.StatusCode()).SendString("Proposal progress is unavailable. Check access and try again.")
	}
	record := response.JSON200
	if record.Slug != creation || record.ProjectSlug != project {
		return c.Status(http.StatusBadGateway).SendString("The proposal returned an unexpected identity.")
	}
	data := views.PipelineCreationProgressData{Org: org, Project: project, Creation: record, CanRetry: r.resolveOrganizationPermissions(c.Context(), cookie, org).Can("pipelines", "create")}
	if c.Method() == http.MethodPost {
		if !data.CanRetry {
			return c.Status(http.StatusForbidden).SendString("Pipeline creation needs access.")
		}
		retry, retryErr := r.apiClient.CreatePipelineProposalWithResponse(c.Context(), org, project, record.Spec, frontendRequestEditor(c.Context(), cookie))
		switch {
		case retryErr != nil:
			data.Error = "Retry outcome could not be loaded. Refresh progress before retrying again."
		case retry.JSON200 == nil:
			data.Error = dashboardErrorMessage("The recorded proposal could not be retried. Check repository access.", parseAPIErrorResponse(retry.Body))
		case retry.JSON200.Slug != creation || retry.JSON200.ProjectSlug != project || retry.JSON200.SpecHash != record.SpecHash:
			data.Error = "Retry returned an unexpected proposal identity."
		default:
			return c.Redirect().Status(http.StatusSeeOther).To(views.PipelineCreationProgressHref(org, project, creation, requestRuntimeEnvironmentSlug(c)))
		}
	}
	version, versionErr := r.apiClient.GetPipelineVersionWithResponse(c.Context(), org, project, record.PipelineSlug, record.SpecHash, frontendRequestEditor(c.Context(), cookie))
	switch {
	case versionErr != nil:
		data.VersionError = "Imported version readiness could not be checked. Refresh to try again."
	case version.JSON200 != nil && version.JSON200.SpecHash == record.SpecHash && version.JSON200.PipelineSlug == record.PipelineSlug:
		data.VersionAvailable = true
	case version.StatusCode() != http.StatusNotFound:
		data.VersionError = "The exact proposed version could not be verified. Check access and refresh."
	}
	shell := r.buildShellDataForProjectPage(c, "Pipeline proposal", org, project, "", "", cookie)
	data.Environment = shell.ActiveEnvironmentSlug
	c.SetContext(views.WithDefinitionInspection(c.Context(), views.DefinitionInspection{Authority: "Recorded creation proposal", Hash: record.SpecHash, Spec: &record.Spec, Notice: "Proposed content; not an imported canonical version."}))
	return renderTempl(c, views.PipelineCreationProgressPage(shell, data))
}

func (r *Router) handlePipelineCreationList(c fiber.Ctx) error {
	org, project, ok := projectActionSlugs(c)
	if !ok {
		return fiber.ErrBadRequest
	}
	params := &api.ListPipelineCreationProposalsParams{}
	limit := api.LimitParam(20)
	params.Limit = &limit
	if cursor := c.Query("cursor"); cursor != "" {
		if _, err := slug.New(cursor); err != nil {
			return fiber.ErrBadRequest
		}
		params.Cursor = &cursor
	}
	cookie := c.Get(fiber.HeaderCookie)
	data := views.PipelineCreationListData{Org: org.String(), Project: project.String()}
	response, err := r.apiClient.ListPipelineCreationProposalsWithResponse(c.Context(), org.String(), project.String(), params, frontendRequestEditor(c.Context(), cookie))
	switch {
	case err != nil:
		data.Error = "Creation proposals could not be loaded. Refresh to try again."
		c.Status(http.StatusBadGateway)
	case response.JSON200 == nil:
		data.Error = "Creation proposals are unavailable. Check pipeline read access and refresh."
		c.Status(response.StatusCode())
	default:
		data.Page = response.JSON200
	}
	shell := r.buildShellDataForProjectPage(c, "Creation proposals", org.String(), project.String(), "", "", cookie)
	data.Environment = shell.ActiveEnvironmentSlug
	return renderTempl(c, views.PipelineCreationListPage(shell, data))
}
