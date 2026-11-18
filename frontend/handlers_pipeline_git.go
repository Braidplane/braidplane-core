package frontend

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/apierror"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (r *Router) handlePipelineSync(c fiber.Ctx) error {
	org, project, ok := projectActionSlugs(c)
	if !ok {
		return c.SendStatus(http.StatusBadRequest)
	}
	permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), org.String())
	if !permissions.Can("pipelines", "update") {
		return r.renderPipelineGitAccessDenied(c, org.String(), project.String(), permissions.Can("pipelines", "read"),
			"You need permission to update pipelines to sync from Git.")
	}
	data := views.PipelineSyncView{Org: org.String(), Project: project.String(), Environment: c.Query("environment_slug")}
	data.Pipeline = c.Query("pipeline_slug")
	if c.Method() == http.MethodPost {
		data.Pipeline = c.FormValue("pipeline_slug")
		data.Environment = c.FormValue("environment_slug")
	}
	if data.Pipeline != "" {
		if _, err := slug.New(data.Pipeline); err != nil {
			return c.SendStatus(http.StatusBadRequest)
		}
	}
	projectRecord, status, apiError, err := r.getProjectViaAPI(c.Context(), c.Get(fiber.HeaderCookie), data.Org, data.Project)
	if err != nil {
		if status == http.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		data.Error = pipelineActionError(apiError, "Project Git configuration is unavailable.")
	} else {
		data.Backend = projectRecord.PipelineBackend
	}
	if c.Method() == http.MethodPost && data.Backend != nil {
		preview := c.FormValue("action") == "preview"
		if !preview && c.FormValue("action") != "apply" {
			return c.SendStatus(http.StatusBadRequest)
		}
		directory := data.Backend.PipelineDir
		if directory == "" {
			directory = "pipelines"
		}
		req := api.PipelineReconcileRequest{DryRun: &preview, PipelineDir: &directory}
		if data.Pipeline != "" {
			req.PipelineSlugs = &[]api.Slug{data.Pipeline}
		}
		if !preview {
			if c.FormValue("pipeline_dir") != directory {
				data.Error = "Git directory changed since preview. Preview again before applying."
			}
			expected := api.PipelineReconcileExpectedSource{
				DataSourceSlug: c.FormValue("data_source_slug"), Repo: c.FormValue("repo"), Ref: c.FormValue("ref"), Commit: c.FormValue("commit"),
			}
			if _, err := slug.New(expected.DataSourceSlug); err != nil || expected.Repo == "" || expected.Ref == "" || expected.Commit == "" {
				data.Error = "Preview Git changes before applying."
			} else {
				req.ExpectedSource = &expected
			}
		}
		if data.Error == "" {
			resp, callErr := r.apiClient.ReconcilePipelinesWithResponse(c.Context(), data.Org, data.Project, req, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
			switch {
			case callErr != nil:
				data.Error = "Sync outcome could not be confirmed. Preview again to inspect current state before retrying."
			case resp.StatusCode() == http.StatusUnauthorized:
				return redirectToLogin(c, c.OriginalURL())
			case resp.StatusCode() == http.StatusOK && resp.JSON200 != nil:
				data.Report = resp.JSON200
			case resp.StatusCode() == http.StatusConflict:
				data.Error = "Git source changed since preview. Preview again before applying."
			default:
				data.Error = pipelineActionError(parseAPIErrorResponse(resp.Body), "Sync could not be completed. Preview again to inspect current state before retrying.")
			}
		}
	}
	shell := r.buildPipelineShellData(c, data.Org, data.Project, "", "")
	return renderTempl(c, views.PipelineSyncPage(shell, data))
}

// The API supplies public error messages; raw provider and per-file errors are never rendered here.
func pipelineActionError(response *api.ErrorResponse, fallback string) string {
	if message := messageFromErrorResponse(response); message != "" {
		return message
	}
	return fallback
}

func (r *Router) renderPipelineGitAccessDenied(c fiber.Ctx, org, project string, canRead bool, message string) error {
	backHref, backLabel := organizationsPath, "Back to organizations"
	shellProject := ""
	if canRead {
		shellProject = project
		backHref = "/orgs/" + org + "/projects/" + project + "/pipelines"
		backLabel = "Back to pipelines"
	}
	shell := r.buildPipelineShellData(c, org, shellProject, "", "")
	shell.Title = "Access denied"
	shell.Subtitle = ""
	c.Set(fiber.HeaderCacheControl, "no-store")
	setPipelineGitAccessDeniedStatus(c)
	return renderTempl(c, views.PipelineGitAccessDenied(shell, message, backHref, backLabel))
}

func setPipelineGitAccessDeniedStatus(c fiber.Ctx) {
	if isHTMXRequest(c) {
		c.Set("X-Braidplane-Pipeline-Access-Denied", "true")
	}
	c.Status(http.StatusForbidden)
}

func pipelineGitAuthorizationRefused(status int, response *api.ErrorResponse) bool {
	return status == http.StatusForbidden && response != nil && response.Error.Code == apierror.CodeAuthForbidden
}

func (r *Router) handlePipelineDeploy(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	org, project, ok := projectActionSlugs(c)
	pipeline, err := slug.New(c.Params("pipelineSlug"))
	if !ok || err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), org.String())
	canUpdate := permissions.Can("pipelines", "update")
	if !permissions.Can("pipelines", "read") {
		return r.renderPipelineGitAccessDenied(c, org.String(), project.String(), false, "You need permission to read pipelines to inspect deployments.")
	}
	const updateRequired = "You need permission to update pipelines to compile or deploy a version."
	updateDenied := c.Method() == http.MethodPost && !canUpdate
	backendDenied := false
	data := views.PipelineDeployView{CanCompile: canUpdate, Org: org.String(), Project: project.String(), Pipeline: pipeline.String()}
	hash := c.Query("spec_hash")
	data.Environment = c.Query("environment_slug")
	if c.Method() == http.MethodPost {
		hash = c.FormValue("spec_hash")
		data.Environment = c.FormValue("environment_slug")
	}
	if strings.TrimSpace(hash) == "" {
		if updateDenied {
			return r.renderPipelineGitAccessDenied(c, data.Org, data.Project, true, updateRequired)
		}
		data.Error = "Select an immutable version from the pipeline details."
		c.Status(http.StatusBadRequest)
		return renderTempl(c, views.PipelineDeployPage(r.buildPipelineShellData(c, data.Org, data.Project, "", ""), data))
	}
	ctx, cookie := c.Context(), c.Get(fiber.HeaderCookie)
	version, status, apiError, err := r.getPipelineVersionViaAPI(ctx, cookie, data.Org, data.Project, data.Pipeline, hash)
	if err != nil {
		if status == http.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		if updateDenied {
			return r.renderPipelineGitAccessDenied(c, data.Org, data.Project, true, updateRequired)
		}
		data.Error = pipelineActionError(apiError, "The selected pipeline version is unavailable.")
		return renderTempl(c, views.PipelineDeployPage(r.buildPipelineShellData(c, data.Org, data.Project, "", ""), data))
	}
	data.Version = version
	environments, status, apiError, err := r.listProjectEnvironmentsViaAPI(ctx, cookie, data.Org, data.Project)
	if err != nil {
		if status == http.StatusUnauthorized {
			return redirectToLogin(c, c.OriginalURL())
		}
		data.Error = pipelineActionError(apiError, "Enabled environments are unavailable.")
	} else {
		data.Environments = environments.ProjectEnvironments
		if data.Environment == "" && c.Method() == http.MethodGet {
			data.Environment = selectDefaultProjectEnvironmentSlug(data.Environments)
		}
		data.TargetEnabled = projectEnvironmentSlugIsEnabled(data.Environments, data.Environment)
		if !data.TargetEnabled {
			data.Error = "Choose an environment enabled for this project."
		} else {
			for _, environment := range data.Environments {
				if environment.EnvironmentSlug == data.Environment {
					data.Protected = environment.EnvironmentProtected
				}
			}
		}
	}
	if data.Error == "" {
		current, status, apiError, err := r.getPipelineDeploymentViaAPI(ctx, cookie, data.Org, data.Project, data.Environment, data.Pipeline)
		switch {
		case err == nil:
			data.Current = current
		case status == http.StatusUnauthorized:
			return redirectToLogin(c, c.OriginalURL())
		case status != http.StatusNotFound:
			data.Error = pipelineActionError(apiError, "Current deployment is unavailable. Refresh before deploying.")
		}
	}
	data.CanDeploy = canUpdate && data.Error == ""
	if data.Current != nil {
		data.ExpectedRevision = data.Current.Revision
	}
	data.CompiledDigest, data.ResolutionDigest = c.Query("compiled_plan_digest"), c.Query("resolution_digest")
	if c.Method() == http.MethodPost {
		data.CompiledDigest, data.ResolutionDigest = c.FormValue("compiled_plan_digest"), c.FormValue("resolution_digest")
	} else if c.Request().URI().QueryArgs().Has("review_from_environment") {
		data.ReviewTarget = true
		from := c.Query("review_from_environment")
		if from != data.Environment {
			if data.Error == "" && data.ResolutionDigest != "" {
				query := url.Values{"review_from_environment": {from}, "rollback_selection_cleared": {"true"}}
				if data.CompiledDigest != "" {
					query.Set("compiled_plan_digest", data.CompiledDigest)
				}
				target := views.PipelineDeployHref(data.Org, data.Project, data.Environment, data.Pipeline, data.Version.SpecHash)
				return c.Redirect().Status(http.StatusSeeOther).To(target + "&" + query.Encode())
			}
			data.RollbackSelectionCleared = c.Query("rollback_selection_cleared") == "true"
		}
	}
	if data.CanDeploy && c.Method() == http.MethodPost && c.FormValue("action") == "compile" && !data.CanCompile {
		data.Error = "New contract compilation is disabled. Retained plans remain inspectable."
	}
	if data.CanDeploy && data.CanCompile && c.Method() == http.MethodPost && c.FormValue("action") == "compile" {
		data.ProjectionJSON = strings.TrimSpace(c.FormValue("projection"))
		data.ResourceScopesJSON = strings.TrimSpace(c.FormValue("resource_scopes"))
		body := api.PipelineCompilationRequest{}
		if data.ProjectionJSON != "" {
			body.Projection = new(api.ODCSProjectionSelection)
			decoder := json.NewDecoder(bytes.NewBufferString(data.ProjectionJSON))
			decoder.DisallowUnknownFields()
			if len(data.ProjectionJSON) > odcs.MaxDocumentBytes || odcs.CheckRequestJSON([]byte(data.ProjectionJSON)) != nil || decoder.Decode(body.Projection) != nil || decoder.Decode(new(any)) != io.EOF {
				data.Error = "Enter one valid projection selection JSON object within the 1 MiB limit. Documents and source provenance cannot be supplied."
				data.InvalidCompileField = "projection"
				data.CanDeploy = false
				return renderTempl(c, views.PipelineDeployPage(r.buildPipelineShellData(c, data.Org, data.Project, "", ""), data))
			}
		}
		if data.ResourceScopesJSON != "" {
			var scopes []api.PipelineResourceScopeSelection
			decoder := json.NewDecoder(bytes.NewBufferString(data.ResourceScopesJSON))
			decoder.DisallowUnknownFields()
			if len(data.ResourceScopesJSON) > odcs.MaxDocumentBytes || odcs.CheckRequestJSON([]byte(data.ResourceScopesJSON)) != nil || decoder.Decode(&scopes) != nil || decoder.Decode(new(any)) != io.EOF || scopes == nil || len(scopes) > 128 {
				data.Error = "Enter one valid resource scope JSON array within the 1 MiB limit, with at most 128 selections. Use non-secret policy values only."
				data.InvalidCompileField = "resource_scopes"
				data.CanDeploy = false
				return renderTempl(c, views.PipelineDeployPage(r.buildPipelineShellData(c, data.Org, data.Project, "", ""), data))
			}
			body.ResourceScopes = &scopes
		}
		resp, callErr := r.apiClient.CompilePipelineVersionWithResponse(ctx, data.Org, data.Project, data.Pipeline, data.Version.SpecHash, &api.CompilePipelineVersionParams{EnvironmentSlug: &data.Environment}, body, frontendRequestEditor(ctx, cookie))
		switch {
		case callErr != nil:
			data.Error = "Compilation outcome could not be confirmed. Inspect a retained digest before retrying."
		case resp.StatusCode() == http.StatusUnauthorized:
			return redirectToLogin(c, c.OriginalURL())
		case resp.StatusCode() == http.StatusOK && resp.JSON200 != nil:
			data.Compiled, data.CompiledDigest = resp.JSON200, resp.JSON200.Digest
			data.ResolutionDigest = ""
		default:
			apiErr := parseAPIErrorResponse(resp.Body)
			backendDenied = pipelineGitAuthorizationRefused(resp.StatusCode(), apiErr)
			data.Error = pipelineActionError(apiErr, "Compilation is unavailable for this retained version.")
		}
	}
	if data.Error == "" && data.CompiledDigest != "" && data.ResolutionDigest != "" {
		data.Error = "Select a compiled plan or a rollback resolution, one at a time."
	}
	if data.ResolutionDigest == "" && data.CompiledDigest != "" && data.Compiled == nil {
		resp, callErr := r.apiClient.GetPipelineCompiledPlanWithResponse(ctx, data.Org, data.Project, data.Pipeline, data.CompiledDigest, frontendRequestEditor(ctx, cookie))
		switch {
		case callErr != nil:
			data.Error = "Retained compiled plan is unavailable."
		case resp.StatusCode() == http.StatusUnauthorized:
			return redirectToLogin(c, c.OriginalURL())
		case resp.StatusCode() == http.StatusOK && resp.JSON200 != nil:
			data.Compiled = resp.JSON200
		default:
			data.Error = pipelineActionError(parseAPIErrorResponse(resp.Body), "Retained compiled plan is unavailable or restricted.")
		}
	}
	if data.Compiled != nil && data.Version != nil && data.Compiled.SpecHash != data.Version.SpecHash {
		data.Error = "The selected compiled plan belongs to a different immutable version."
	}
	if data.Compiled != nil && data.Compiled.Status != api.PipelineCompiledPlanStatusAvailable {
		data.CanDeploy = false
	}
	if data.Error != "" {
		data.CanDeploy = false
	}
	if c.Method() == http.MethodPost && c.FormValue("action") != "compile" && data.CanDeploy {
		req := api.PipelineDeploymentRequest{
			SpecHash:                         data.Version.SpecHash,
			CompiledPlanDigest:               optionalStringPointer(data.CompiledDigest),
			ResolutionDigest:                 optionalStringPointer(data.ResolutionDigest),
			ExpectedCurrentSpecHash:          optionalStringPointer(c.FormValue("expected_current_spec_hash")),
			ProtectedEnvironmentConfirmation: optionalStringPointer(c.FormValue("protected_environment_confirmation")),
		}
		revision, revisionErr := strconv.ParseInt(c.FormValue("expected_revision"), 10, 64)
		if revisionErr != nil || revision < 0 {
			data.Error = "Review the deployment revision before deploying."
			data.CanDeploy = false
		} else {
			req.ExpectedRevision = &revision
		}
		// Do not silently replace the version the operator saw if another deployment intervened.
		currentHash := ""
		if data.Current != nil {
			currentHash = data.Current.CurrentVersionSpecHash
		}
		if data.Error == "" && c.FormValue("expected_current_spec_hash") != currentHash {
			data.Error = "Deployment changed since this form was opened. Review the current version before retrying."
			data.CanDeploy = false
			data.ReviewDeployment = true
		} else if data.Error == "" {
			resp, callErr := r.apiClient.DeployPipelineWithResponse(ctx, data.Org, data.Project, data.Environment, data.Pipeline, req, frontendRequestEditor(ctx, cookie))
			switch {
			case callErr != nil:
				data.Error = "Deployment outcome could not be confirmed. Refresh deployment state before retrying."
				data.CanDeploy = false
				data.ReviewDeployment = true
			case resp.StatusCode() == http.StatusUnauthorized:
				return redirectToLogin(c, c.OriginalURL())
			case resp.StatusCode() == http.StatusOK && resp.JSON200 != nil:
				data.Deployed = resp.JSON200
			case resp.StatusCode() == http.StatusConflict:
				data.Error = "Deployment changed. Refresh deployment state before retrying."
				data.CanDeploy = false
				data.ReviewDeployment = true
			default:
				apiErr := parseAPIErrorResponse(resp.Body)
				backendDenied = pipelineGitAuthorizationRefused(resp.StatusCode(), apiErr)
				data.Error = pipelineActionError(apiErr, "Deployment failed. Check environment bindings and retry the selected version.")
			}
		}
	}
	if updateDenied {
		data.Error = strings.TrimSpace(updateRequired + " " + data.Error)
	}
	if updateDenied || backendDenied {
		setPipelineGitAccessDeniedStatus(c)
		data.CanCompile, data.CanDeploy = false, false
		data.ReviewDeployment = true
	}
	shell := r.buildPipelineShellData(c, data.Org, data.Project, "", "")
	return renderTempl(c, views.PipelineDeployPage(shell, data))
}
