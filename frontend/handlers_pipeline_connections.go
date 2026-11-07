package frontend

import (
	"net/http"
	"strings"

	"github.com/gofiber/fiber/v3"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/views"
)

const pipelineConnectionsMaxPages = 5

func (r *Router) loadPipelineConnections(c fiber.Ctx, org, project, environment, pipeline string) (views.PipelineConnections, bool) {
	connections := views.PipelineConnections{CatalogHref: views.ProjectSchedulesListHrefWithEnvironment(org, project, environment)}
	if strings.TrimSpace(environment) == "" {
		connections.Message = "Select a runtime target to inspect current completion subscriptions."
		return connections, false
	}

	params := &api.ListPipelineTriggersParams{Limit: new(api.LimitParam(100))}
	triggers := make([]api.PipelineTrigger, 0)
	for page := 0; page < pipelineConnectionsMaxPages; page++ {
		list, status, _, err := r.listPipelineTriggersViaAPI(c.Context(), c.Get(fiber.HeaderCookie), org, project, environment, params)
		if err != nil {
			if status == http.StatusUnauthorized {
				return connections, true
			}
			connections.Unavailable = true
			connections.Message = "Current completion subscriptions are unavailable. Try again."
			return connections, false
		}
		triggers = append(triggers, list.PipelineTriggers...)
		if !list.HasMore {
			break
		}
		if page == pipelineConnectionsMaxPages-1 || list.NextCursor == nil || *list.NextCursor == "" || (params.Cursor != nil && *params.Cursor == *list.NextCursor) {
			connections.Partial = true
			break
		}
		params.Cursor = list.NextCursor
	}

	edges := make([]api.PipelineTrigger, 0, len(triggers))
	adjacent := map[string][]int{}
	for _, trigger := range triggers {
		if trigger.Type != api.PipelineTriggerTypeCompletion || trigger.Completion == nil {
			continue
		}
		index := len(edges)
		edges = append(edges, trigger)
		adjacent[trigger.Completion.UpstreamPipelineSlug] = append(adjacent[trigger.Completion.UpstreamPipelineSlug], index)
		adjacent[trigger.PipelineSlug] = append(adjacent[trigger.PipelineSlug], index)
	}
	seenNodes := map[string]bool{pipeline: true}
	seenEdges := map[int]bool{}
	nodeOrder := []string{pipeline}
	edgeOrder := []int{}
	queue := []string{pipeline}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, index := range adjacent[current] {
			if seenEdges[index] {
				continue
			}
			seenEdges[index] = true
			edgeOrder = append(edgeOrder, index)
			edge := edges[index]
			for _, candidate := range []string{edge.Completion.UpstreamPipelineSlug, edge.PipelineSlug} {
				if !seenNodes[candidate] {
					seenNodes[candidate] = true
					nodeOrder = append(nodeOrder, candidate)
					queue = append(queue, candidate)
				}
			}
		}
	}
	for _, node := range nodeOrder {
		connections.Nodes = append(connections.Nodes, views.PipelineConnectionNode{
			Slug:     node,
			Selected: node == pipeline,
			Href:     views.PipelineConnectionsHref(org, project, environment, node),
		})
	}
	for _, index := range edgeOrder {
		trigger := edges[index]
		connections.Edges = append(connections.Edges, views.PipelineConnectionEdge{
			TriggerSlug: trigger.Slug,
			TriggerHref: views.PipelineScheduleDetailHref(org, project, environment, trigger.Slug),
			Revision:    trigger.Revision,
			Condition:   string(trigger.Completion.Condition),
			State:       views.PipelineConnectionState(trigger.Enabled, trigger.Completion.ExecutionGrantEnabled),
			From:        trigger.Completion.UpstreamPipelineSlug,
			To:          trigger.PipelineSlug,
		})
	}
	if len(connections.Nodes) > 40 || len(connections.Edges) > 100 {
		connections.Truncated = true
		connections.Nodes = connections.Nodes[:min(len(connections.Nodes), 40)]
		allowed := map[string]bool{}
		for _, node := range connections.Nodes {
			allowed[node.Slug] = true
		}
		filtered := connections.Edges[:0]
		for _, edge := range connections.Edges {
			if allowed[edge.From] && allowed[edge.To] && len(filtered) < 100 {
				filtered = append(filtered, edge)
			}
		}
		connections.Edges = filtered
	}
	views.SortPipelineConnections(&connections)
	views.PipelineConnectionsCanvas(&connections)
	return connections, false
}
