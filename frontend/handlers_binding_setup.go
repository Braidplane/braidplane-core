package frontend

import (
	"net/http"
	"net/url"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (r *Router) handlePipelineBindingSetup(c fiber.Ctx) error {
	return r.renderPipelineBindingSetup(c, http.StatusOK, "")
}

func (r *Router) renderPipelineBindingSetup(c fiber.Ctx, statusCode int, message string) error {
	org, project, ok := projectActionSlugs(c)
	pipeline, err := slug.New(c.Params("pipelineSlug"))
	if !ok || err != nil {
		return c.SendStatus(http.StatusBadRequest)
	}
	environment := c.Query("environment_slug")
	if environment != "" {
		if _, err := slug.New(environment); err != nil {
			return c.SendStatus(http.StatusBadRequest)
		}
	}
	permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), org.String())
	if !permissions.Can("pipelines", "read") {
		return c.SendStatus(http.StatusForbidden)
	}
	hash := c.Query("spec_hash")
	if strings.TrimSpace(hash) == "" {
		return c.Status(http.StatusBadRequest).SendString("select a pipeline version")
	}
	data := views.PipelineBindingSetupView{
		Org: org.String(), Project: project.String(), Pipeline: pipeline.String(), Environment: environment,
		SpecHash: hash, Permissions: permissions, Error: message,
	}
	data.ReturnHref = pipelineBindingSetupReturnHref(c, data.Org, data.Project, data.Pipeline, environment, hash)
	version, status, _, err := r.getPipelineVersionViaAPI(c.Context(), c.Get(fiber.HeaderCookie), data.Org, data.Project, data.Pipeline, hash)
	if err != nil {
		if status == http.StatusUnauthorized {
			return redirectToLogin(c, pipelineBindingSetupReloadHref(c, data.Org, data.Project, data.Pipeline))
		}
		data.Error = "Selected pipeline version could not be loaded. Return and retry."
	} else {
		// Only requirements come from the selected version; no current/deployed identity is overwritten.
		data.Definition = &api.Pipeline{Slug: data.Pipeline, Spec: version.Spec}
		var unauthorized bool
		data.Bindings, unauthorized = r.loadPipelineBindingContext(c, data.Org, data.Project)
		if unauthorized {
			return redirectToLogin(c, pipelineBindingSetupReloadHref(c, data.Org, data.Project, data.Pipeline))
		}
	}
	if environment != "" {
		history, status, _, err := r.listPipelineValidationResultsViaAPI(c.Context(), c.Get(fiber.HeaderCookie), data.Org, data.Project, environment, data.Pipeline, 5)
		if status == http.StatusUnauthorized {
			return redirectToLogin(c, pipelineBindingSetupReloadHref(c, data.Org, data.Project, data.Pipeline))
		}
		data.ValidationUnavailable = err != nil
		if history != nil {
			data.ValidationResults = history.ValidationResults
		}
	}
	shell := r.buildPipelineShellData(c, data.Org, data.Project, "", "")
	if shell.ActiveEnvironmentSlug != environment {
		// Keep an absent or disabled selection visible instead of silently showing the default target.
		for i := range shell.Environments {
			shell.Environments[i].IsActive = false
		}
		name := environment
		if name == "" {
			name = "Select runtime target"
		}
		shell.Environments = append([]views.ShellEnvironmentOption{{Slug: environment, Name: name, IsActive: true, Href: views.PipelineBindingSetupHref(data.Org, data.Project, environment, data.Pipeline, hash, data.ReturnHref)}}, shell.Environments...)
		shell.ActiveEnvironmentSlug, shell.ActiveEnvironmentName = environment, name
		shell.ActiveEnvironmentColor, shell.ActiveEnvironmentProtected = "", false
		shell.NavItems = appendEnvironmentToProjectRuntimeNav(shell.NavItems, environment)
	}
	setHTMLResponseStatus(c, statusCode)
	return renderTempl(c, views.PipelineBindingSetupPage(shell, data))
}

// Match the existing exact scoped route policy and retain only the selected context.
func pipelineBindingSetupReturnHref(c fiber.Ctx, org, project, pipeline, environment, hash string) string {
	fallback := views.PipelineDeployHref(org, project, environment, pipeline, hash)
	expected, _ := url.Parse(views.PipelineDetailHrefWithEnvironment(org, project, environment, pipeline))
	candidate, err := url.Parse(safeReturnTo(c.Query("binding_return")))
	if err != nil || candidate == nil || candidate.Path == "" || candidate.User != nil {
		return fallback
	}
	creationPrefix := "/orgs/" + org + "/projects/" + project + "/pipeline-creations/"
	if strings.HasPrefix(candidate.Path, creationPrefix) && candidate.Query().Get("environment_slug") == environment {
		creation, err := slug.New(strings.TrimPrefix(candidate.Path, creationPrefix))
		if err == nil {
			return views.PipelineCreationProgressHref(org, project, creation.String(), environment)
		}
	}
	if candidate.Path != expected.Path && candidate.Path != expected.Path+"/deploy" {
		return fallback
	}
	if candidate.Query().Get("environment_slug") != environment {
		return fallback
	}
	if candidate.Path == expected.Path+"/deploy" && candidate.Query().Get("spec_hash") != hash {
		return fallback
	}
	values := url.Values{}
	if environment != "" {
		values.Set("environment_slug", environment)
	}
	if candidate.Path == expected.Path+"/deploy" {
		values.Set("spec_hash", hash)
	} else {
		// Detail sections have no selected-version semantics; retain the caller's focus only.
		for _, key := range []string{"section", "list_return"} {
			if value := candidate.Query().Get(key); value != "" {
				values.Set(key, value)
			}
		}
	}
	candidate.RawQuery = values.Encode()
	return candidate.String()
}

func pipelineBindingSetupReloadHref(c fiber.Ctx, org, project, pipeline string) string {
	environment, hash := c.Query("environment_slug"), c.Query("spec_hash")
	back := pipelineBindingSetupReturnHref(c, org, project, pipeline, environment, hash)
	href := views.PipelineBindingSetupHref(org, project, environment, pipeline, hash, back)
	if ref := c.Params("dataSourceRefSlug"); ref != "" {
		href += "#" + views.DataSourceBindingFormID("binding-cell", pipeline, ref, c.Params("environmentSlug"))
	}
	return href
}
