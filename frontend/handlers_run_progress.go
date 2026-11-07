package frontend

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Braidplane/braidplane-core/pkg/xerror"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
	"github.com/gofiber/fiber/v3"
)

var errRunProgressUnavailable = xerror.Unavailable(xerror.PipelineRun)

func (r *Router) getRunProgressViaAPI(ctx context.Context, cookie, org, project, environment, run string, after int64) (*api.PipelineRunProgress, int, error) {
	limit := 100
	response, err := r.apiClient.GetPipelineRunProgressWithResponse(ctx, org, projectSlugForAPI(project), environment, run, &api.GetPipelineRunProgressParams{AfterEvent: &after, Limit: &limit}, frontendRequestEditor(ctx, cookie))
	if err != nil {
		return nil, http.StatusServiceUnavailable, err
	}
	if response.StatusCode() == http.StatusOK {
		if response.JSON200 == nil {
			return nil, http.StatusServiceUnavailable, errRunProgressUnavailable
		}
		return response.JSON200, http.StatusOK, nil
	}
	return nil, response.StatusCode(), errRunProgressUnavailable
}

func runProgressShouldPoll(status api.PipelineRunMetadataStatus, attempts []api.PipelineRunTriggerRetryAttempt, section string) bool {
	if section == "definition" || section == "completion" {
		return false
	}
	if status == "running" || status == "queued" {
		return true
	}
	if section == "attempts" {
		for _, attempt := range attempts {
			if attempt.Status == "queued" || attempt.Status == "running" {
				return true
			}
		}
	}
	return false
}

func unchangedProgressPoll(c fiber.Ctx, progress *api.PipelineRunProgress) bool {
	return c.Query("progress_poll") == "1" && c.Query("section") != "attempts" && progress != nil &&
		c.Query("progress_revision") == strconv.FormatInt(progress.Revision, 10) &&
		c.Query("progress_events") == strconv.FormatInt(progress.EventCount, 10) &&
		c.Query("progress_status") == string(progress.Metadata.Status)
}

func renderRunProgressPoll(c fiber.Ctx, detailHref string, progress *api.PipelineRunProgress) error {
	c.Set(fiber.HeaderCacheControl, "no-store")
	return renderTempl(c, views.PipelineRunProgressPanel(detailHref, views.RunProgressInspection{Evidence: progress, Poll: runProgressShouldPoll(progress.Metadata.Status, nil, c.Query("section")), VersionRecorded: progress.Metadata.PipelineVersionSpecHash != ""}))
}
