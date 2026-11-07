package frontend

import (
	"net/http"
	"net/url"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (r *Router) handleBuilderRunEvidence(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	for _, key := range []string{"org_slug", "project_slug", "environment_slug", "pipeline_slug", "from_step", "to_step"} {
		if _, err := slug.New(c.Query(key)); err != nil {
			return c.SendStatus(http.StatusBadRequest)
		}
	}
	org, project, environment := c.Query("org_slug"), c.Query("project_slug"), c.Query("environment_slug")
	pipeline, from, to := c.Query("pipeline_slug"), c.Query("from_step"), c.Query("to_step")
	runSlug := c.Query("run_slug")
	if runSlug != "" {
		if _, err := slug.New(runSlug); err != nil {
			return c.SendStatus(http.StatusBadRequest)
		}
	}
	limit := api.LimitParam(20)
	params := &api.ListPipelineRunsParams{Limit: &limit, PipelineSlug: &pipeline}
	if cursor := c.Query("cursor"); cursor != "" {
		params.Cursor = &cursor
	}
	list, status, _, err := r.listPipelineRunsViaAPI(c.Context(), c.Get(fiber.HeaderCookie), org, project, environment, params)
	if err != nil {
		return c.SendStatus(status)
	}
	data := views.BuilderRunEvidenceData{Runs: list.Runs, SelectedRun: runSlug, HasCursor: c.Query("cursor") != ""}
	if list.NextCursor != nil {
		data.NextCursor = *list.NextCursor
	}
	for _, run := range list.Runs {
		if run.PipelineSlug != pipeline || run.EnvironmentSlug != environment {
			return c.SendStatus(http.StatusBadGateway)
		}
	}
	if runSlug == "" {
		return renderTempl(c, views.BuilderRunEvidence(data))
	}
	run, status, _, err := r.getPipelineRunViaAPI(c.Context(), c.Get(fiber.HeaderCookie), org, project, environment, runSlug)
	if err != nil {
		return c.SendStatus(status)
	}
	if run.Metadata.RunSlug != runSlug || run.Metadata.PipelineSlug != pipeline || run.Metadata.EnvironmentSlug != environment {
		return c.SendStatus(http.StatusBadGateway)
	}
	data.Run = run
	data.RunHref = "/orgs/" + org + "/projects/" + project + "/runs/" + runSlug + "?" + url.Values{"environment_slug": {environment}, "section": {"steps"}, "lineage_step": {to}}.Encode()
	lineage, _ := r.loadPipelineRunLineage(c.Context(), c.Get(fiber.HeaderCookie), org, project, *run, to, data.RunHref)
	if !lineage.Available {
		data.Message = lineage.Message
		return renderTempl(c, views.BuilderRunEvidence(data))
	}
	connected := false
	for _, edge := range lineage.Edges {
		if edge.FromSlug == from && edge.ToSlug == to {
			connected = true
			break
		}
	}
	if !connected {
		data.Message = "This connection was not present in the run's executed version."
		return renderTempl(c, views.BuilderRunEvidence(data))
	}
	for _, node := range lineage.Nodes {
		if node.Slug == from || node.Slug == to {
			data.Nodes = append(data.Nodes, node)
		}
	}
	if run.Timeline.StructuralChecks != nil {
		for _, check := range *run.Timeline.StructuralChecks {
			if check.Step == from || check.Step == to {
				data.Checks = append(data.Checks, check)
			}
		}
	}
	return renderTempl(c, views.BuilderRunEvidence(data))
}
