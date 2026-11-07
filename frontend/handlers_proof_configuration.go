package frontend

import (
	"cmp"
	"net/http"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (r *Router) handleProofConfigurationPage(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	ds := c.Params("dsSlug")
	if ds == "" {
		target := "/organizations/" + org.String() + "/connections"
		if fiber.Query[bool](c, "saved") {
			target += "?saved=true"
		}
		return redirectHTMXOrSeeOther(c, target+"#proof")
	}
	if _, err := slug.New(ds); err != nil {
		return fiber.ErrBadRequest
	}
	response, err := r.apiClient.GetDataSourceProofConfigurationWithResponse(c.Context(), org.String(), ds, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return fiber.ErrServiceUnavailable
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return c.SendStatus(response.StatusCode())
	}
	configurations := []api.DataSourceProofConfiguration{*response.JSON200}
	feedback := ""
	if fiber.Query[bool](c, "saved") {
		feedback = "Proof configuration saved."
	}
	return r.renderProofConfigurations(c, org, ds, configurations, feedback, false)
}

func (r *Router) renderProofConfigurations(c fiber.Ctx, org slug.Slug, ds string, configurations []api.DataSourceProofConfiguration, feedback string, failed bool) error {
	shell := r.buildShellDataForOrganizationPage(c, "Proof configuration", org.String(), "", "", c.Get(fiber.HeaderCookie))
	shell.Breadcrumbs = []views.BreadcrumbItem{{Label: shell.ActiveOrganizationName, Href: "/organizations/" + org.String()}, {Label: "Proof configuration"}}
	if ds != "" {
		shell.Breadcrumbs = []views.BreadcrumbItem{{Label: "Data sources", Href: "/orgs/" + org.String() + "/data-sources"}, {Label: ds, Href: "/orgs/" + org.String() + "/data-sources/" + ds}, {Label: "Proof"}}
	}
	permissions := r.resolveOrganizationPermissions(c.Context(), c.Get(fiber.HeaderCookie), org.String())
	return renderTempl(c, views.ProofConfigurationPage(shell, org.String(), ds, configurations, feedback, failed, permissions.Can("datasources", "update") && permissions.Can("pipelines", "update")))
}

func (r *Router) handleProofConfigurationSave(c fiber.Ctx) error {
	org, err := slug.New(c.Params("slug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	ds, err := slug.New(c.Params("dsSlug"))
	if err != nil {
		return fiber.ErrBadRequest
	}
	current, err := r.apiClient.GetDataSourceProofConfigurationWithResponse(c.Context(), org.String(), ds.String(), frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return fiber.ErrServiceUnavailable
	}
	if current.StatusCode() != http.StatusOK || current.JSON200 == nil {
		return c.SendStatus(current.StatusCode())
	}
	settings := api.DataSourceProofSettings{Enabled: c.FormValue("enabled") == "on", PublishChecks: c.FormValue("publish_checks") == "on", ExpectedRepositoryId: c.FormValue("expected_repository_id"), Targets: []api.ProofAssessmentTarget{}}
	for _, target := range current.JSON200.AvailableTargets {
		key := target.ProjectSlug + ":" + target.EnvironmentSlug
		for _, line := range strings.Split(c.FormValue("paths:"+key), "\n") {
			pipelinePath := strings.TrimSpace(line)
			if pipelinePath == "" {
				continue
			}
			settings.Targets = append(settings.Targets, api.ProofAssessmentTarget{ProjectSlug: target.ProjectSlug, EnvironmentSlug: target.EnvironmentSlug, TargetRef: c.FormValue("ref:" + key), PipelinePath: pipelinePath})
		}
	}
	response, err := r.apiClient.SetDataSourceProofConfigurationWithResponse(c.Context(), org.String(), ds.String(), settings, frontendRequestEditor(c.Context(), c.Get(fiber.HeaderCookie)))
	if err != nil {
		return fiber.ErrServiceUnavailable
	}
	if response.StatusCode() != http.StatusOK {
		message := messageFromErrorResponse(cmp.Or(response.JSON400, response.JSON401, response.JSON403, response.JSON404, response.JSON409, response.JSONDefault))
		if message == "" {
			message = "Proof configuration could not be saved. Check repository access and assessment targets."
		}
		draft := *current.JSON200
		draft.RepositoryId = settings.ExpectedRepositoryId
		draft.Enabled = settings.Enabled
		draft.PublishChecks = settings.PublishChecks
		draft.Targets = settings.Targets
		c.Status(response.StatusCode())
		return r.renderProofConfigurations(c, org, ds.String(), []api.DataSourceProofConfiguration{draft}, message, true)
	}
	target := "/orgs/" + org.String() + "/data-sources/" + ds.String() + "/proof?saved=true"
	if c.FormValue("return_to") == "organization" {
		target = "/organizations/" + org.String() + "/connections?saved=true#proof"
	}
	return redirectHTMXOrSeeOther(c, target)
}
