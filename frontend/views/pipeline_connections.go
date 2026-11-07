package views

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

type pipelineConnectionsContextKey struct{}

// PipelineConnections is the current, environment-scoped completion-trigger topology.
// It deliberately contains definitions, not execution evidence.
type PipelineConnections struct {
	Nodes        []PipelineConnectionNode
	Edges        []PipelineConnectionEdge
	Unavailable  bool
	Partial      bool
	Truncated    bool
	CatalogHref  string
	Message      string
	CanvasWidth  int
	CanvasHeight int
}

type PipelineConnectionNode struct {
	Selected bool
	Slug     string
	Href     string
	X        int
	Y        int
}

type PipelineConnectionEdge struct {
	TriggerSlug string
	TriggerHref string
	Revision    int64
	Condition   string
	State       string
	From        string
	To          string
	Path        string
	ArrowPoints string
	LabelX      int
	LabelY      int
}

func WithPipelineConnections(ctx context.Context, connections PipelineConnections) context.Context {
	return context.WithValue(ctx, pipelineConnectionsContextKey{}, connections)
}

func pipelineConnections(ctx context.Context) PipelineConnections {
	connections, _ := ctx.Value(pipelineConnectionsContextKey{}).(PipelineConnections)
	return connections
}

func PipelineConnectionState(enabled, executionGrantEnabled bool) string {
	if !enabled {
		return "Disabled"
	}
	if !executionGrantEnabled {
		return "Revoked"
	}
	return "Enabled"
}

func pipelineConnectionStateBadgeClass(state string) string {
	switch state {
	case "Enabled":
		return "badge badge-success badge-outline badge-xs"
	case "Revoked":
		return "badge badge-error badge-outline badge-xs"
	default:
		return "badge badge-warning badge-outline badge-xs"
	}
}

func pipelineConnectionConditionLabel(condition string) string {
	if condition == "final_failure" {
		return "Final failure"
	}
	return "Success"
}

func pipelineConnectionNodeHref(connections PipelineConnections, slug string) string {
	for _, node := range connections.Nodes {
		if node.Slug == slug {
			return node.Href
		}
	}
	return ""
}

func PipelineConnectionsCanvas(connections *PipelineConnections) {
	if len(connections.Nodes) == 0 {
		return
	}
	levels := map[string]int{}
	indegree := map[string]int{}
	next := map[string][]string{}
	for _, edge := range connections.Edges {
		next[edge.From] = append(next[edge.From], edge.To)
		indegree[edge.To]++
	}
	queue := []string{}
	for _, node := range connections.Nodes {
		if indegree[node.Slug] == 0 {
			queue = append(queue, node.Slug)
		}
	}
	for len(queue) > 0 {
		from := queue[0]
		queue = queue[1:]
		for _, to := range next[from] {
			levels[to] = max(levels[to], levels[from]+1)
			indegree[to]--
			if indegree[to] == 0 {
				queue = append(queue, to)
			}
		}
	}
	byLevel := map[int][]int{}
	maxLevel := 0
	for i := range connections.Nodes {
		level := levels[connections.Nodes[i].Slug]
		byLevel[level] = append(byLevel[level], i)
		if level > maxLevel {
			maxLevel = level
		}
	}
	for level, indexes := range byLevel {
		for row, index := range indexes {
			connections.Nodes[index].X = 32 + level*440
			connections.Nodes[index].Y = 32 + row*148
		}
	}
	positions := map[string]PipelineConnectionNode{}
	maxRows := 1
	for _, indexes := range byLevel {
		maxRows = max(maxRows, len(indexes))
		for _, index := range indexes {
			positions[connections.Nodes[index].Slug] = connections.Nodes[index]
		}
	}
	connections.CanvasWidth = max(560, 64+(maxLevel+1)*440)
	connections.CanvasHeight = max(196, 64+maxRows*148)
	for i := range connections.Edges {
		from, to := positions[connections.Edges[i].From], positions[connections.Edges[i].To]
		startX, startY := from.X+208, from.Y+58
		endX, endY := to.X, to.Y+58
		midX := (startX + endX) / 2
		connections.Edges[i].Path = fmt.Sprintf("M%d %d C%d %d %d %d %d %d", startX, startY, midX, startY, midX, endY, endX, endY)
		connections.Edges[i].ArrowPoints = fmt.Sprintf("%d,%d %d,%d %d,%d", endX, endY, endX-9, endY-5, endX-9, endY+5)
		connections.Edges[i].LabelX = midX
		connections.Edges[i].LabelY = (startY + endY) / 2
	}
}

func SortPipelineConnections(connections *PipelineConnections) {
	sort.Slice(connections.Nodes, func(i, j int) bool { return connections.Nodes[i].Slug < connections.Nodes[j].Slug })
	sort.Slice(connections.Edges, func(i, j int) bool {
		left, right := connections.Edges[i], connections.Edges[j]
		return strings.Join([]string{left.From, left.To, left.TriggerSlug}, "\x00") < strings.Join([]string{right.From, right.To, right.TriggerSlug}, "\x00")
	})
}

func PipelineConnectionsHref(org, project, environment, pipeline string) string {
	u, _ := url.Parse(pipelineDetailHrefWithEnvironment(org, project, environment, pipeline))
	q := u.Query()
	q.Set("section", "connections")
	u.RawQuery = q.Encode()
	return u.String()
}
