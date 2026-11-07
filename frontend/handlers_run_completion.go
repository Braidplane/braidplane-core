package frontend

import (
	"context"
	"net/http"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
)

func (r *Router) loadPipelineRunCompletion(ctx context.Context, cookie, org, project, environment, run, pipeline string) views.RunCompletionInspection {
	response, err := r.apiClient.GetPipelineRunCompletionWithResponse(ctx, org, projectSlugForAPI(project), environment, run, frontendRequestEditor(ctx, cookie))
	if err != nil {
		return views.RunCompletionInspection{ErrorSummary: "Completion evidence could not be loaded. Try again."}
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		return views.RunCompletionInspection{ErrorSummary: "Completion evidence is unavailable. Check access to this run and try again."}
	}
	inspection := views.RunCompletionInspection{Evidence: response.JSON200}
	params := &api.ListPipelineTriggersParams{Limit: new(api.LimitParam(100))}
	for {
		list, _, _, err := r.listPipelineTriggersViaAPI(ctx, cookie, org, project, environment, params)
		if err != nil {
			inspection.CurrentSubscriptions = nil
			return inspection
		}
		for _, trigger := range list.PipelineTriggers {
			if trigger.Type == api.PipelineTriggerTypeCompletion && trigger.Completion != nil && trigger.Completion.UpstreamPipelineSlug == pipeline {
				inspection.CurrentSubscriptions = append(inspection.CurrentSubscriptions, trigger)
			}
		}
		if !list.HasMore {
			inspection.CurrentSubscriptionsLoaded = true
			return inspection
		}
		if list.NextCursor == nil || *list.NextCursor == "" || (params.Cursor != nil && *params.Cursor == *list.NextCursor) {
			inspection.CurrentSubscriptions = nil
			return inspection
		}
		params.Cursor = list.NextCursor
	}
}

func (r *Router) loadConnectedRuns(ctx context.Context, cookie, org, project, environment, run string) views.RunCompletionInspection {
	response, err := r.apiClient.GetPipelineRunConnectedWithResponse(ctx, org, projectSlugForAPI(project), environment, run, frontendRequestEditor(ctx, cookie))
	if err != nil || response.JSON200 == nil {
		return views.RunCompletionInspection{ErrorSummary: "Connected evidence could not be loaded. Check access and refresh to retry."}
	}
	return views.RunCompletionInspection{Connected: response.JSON200}
}
