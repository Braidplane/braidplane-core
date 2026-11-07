package frontend

import (
	"net/url"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/gofiber/fiber/v3"
)

func (r *Router) loadPipelineRunPreview(c fiber.Ctx, org, project, environment, pipeline string) views.PipelineRunPreview {
	c.Set(fiber.HeaderCacheControl, "no-store")
	preview := views.PipelineRunPreview{SelectedSlug: c.Query("run_slug")}
	if environment == "" || project == "" {
		return preview
	}
	limit := api.LimitParam(10)
	sortBy, order := api.ListPipelineRunsParamsSortByRunCreatedAt, api.ListPipelineRunsParamsOrderDesc
	list, _, _, err := r.listPipelineRunsViaAPI(c.Context(), c.Get(fiber.HeaderCookie), org, project, environment, &api.ListPipelineRunsParams{Limit: &limit, PipelineSlug: &pipeline, SortBy: &sortBy, Order: &order})
	if err != nil {
		preview.HistoryUnavailable = true
	} else {
		for _, run := range list.Runs {
			if run.PipelineSlug != pipeline || run.EnvironmentSlug != environment {
				preview.HistoryUnavailable = true
				preview.Message = "Run history does not match the selected target."
				return preview
			}
		}
		preview.Runs = list.Runs
		if len(list.Runs) > 0 {
			preview.LatestSlug = list.Runs[0].RunSlug
		}
	}
	if preview.SelectedSlug == "" {
		preview.SelectedSlug = preview.LatestSlug
	}
	if preview.SelectedSlug == "" {
		return preview
	}
	if _, err := slug.New(preview.SelectedSlug); err != nil {
		preview.Message = "Selected run is invalid."
		return preview
	}
	run, _, _, err := r.getPipelineRunViaAPI(c.Context(), c.Get(fiber.HeaderCookie), org, project, environment, preview.SelectedSlug)
	if err != nil {
		preview.Message = "Run evidence is unavailable, access was denied, or retention removed it."
		return preview
	}
	if run.Metadata.RunSlug != preview.SelectedSlug || run.Metadata.PipelineSlug != pipeline || run.Metadata.EnvironmentSlug != environment {
		preview.Message = "Run evidence does not match the selected pipeline and environment."
		return preview
	}
	preview.Run = run
	runHref := "/orgs/" + org + "/projects/" + project + "/runs/" + preview.SelectedSlug + "?" + url.Values{"environment_slug": {environment}}.Encode()
	preview.Lineage, _ = r.loadPipelineRunLineage(c.Context(), c.Get(fiber.HeaderCookie), org, project, *run, "", runHref)
	return preview
}
