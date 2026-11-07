package views

import (
	"fmt"
	"net/url"
	"path"
	"sort"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

type connectedRunNode struct {
	Record   api.PipelineConnectedRun
	X, Y     int
	Href     string
	Selected bool
}

type connectedRunEdge struct {
	Decision api.PipelineCompletionDecision
	Path     string
	X, Y     int
}

type connectedRunGraph struct {
	Nodes         []connectedRunNode
	Edges         []connectedRunEdge
	Decisions     []api.PipelineCompletionDecision
	Width, Height int
	Anchor        string
	Selected      *api.PipelineConnectedRun
}

func connectedSelectionHref(base, root, target string) string {
	u, _ := url.Parse(base)
	u.Path = path.Join(path.Dir(u.Path), target)
	q := u.Query()
	q.Set("connected_run", root)
	u.RawQuery = q.Encode()
	return u.String()
}

func connectedGraph(data *api.PipelineRunConnected, selected, base string) connectedRunGraph {
	g := connectedRunGraph{Width: 720, Height: 300}
	if data == nil {
		return g
	}
	nodes := append([]api.PipelineConnectedRun(nil), data.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Completion.RootRunSlug < nodes[j].Completion.RootRunSlug })
	indexes := map[string]int{}
	ranks := map[string]int{}
	indegree := map[string]int{}
	next := map[string][]string{}
	for i, n := range nodes {
		indexes[n.Completion.RootRunSlug] = i
	}
	seen := map[string]bool{}
	for _, n := range nodes {
		ds := append([]api.PipelineCompletionDecision{}, n.Completion.Children...)
		if n.Completion.Parent != nil {
			ds = append(ds, *n.Completion.Parent)
		}
		for _, d := range ds {
			key := fmt.Sprintf("%s/%s/%d", d.RootRunSlug, d.TriggerSlug, d.TriggerRevision)
			if seen[key] {
				continue
			}
			seen[key] = true
			g.Decisions = append(g.Decisions, d)
			if d.ChildRunSlug == nil {
				continue
			}
			_, from := indexes[d.RootRunSlug]
			_, to := indexes[*d.ChildRunSlug]
			if from && to {
				next[d.RootRunSlug] = append(next[d.RootRunSlug], *d.ChildRunSlug)
				indegree[*d.ChildRunSlug]++
			}
		}
	}
	queue := []string{}
	for _, n := range nodes {
		if indegree[n.Completion.RootRunSlug] == 0 {
			queue = append(queue, n.Completion.RootRunSlug)
		}
	}
	for len(queue) > 0 {
		root := queue[0]
		queue = queue[1:]
		for _, child := range next[root] {
			ranks[child] = max(ranks[child], ranks[root]+1)
			indegree[child]--
			if indegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	rows := map[int]int{}
	for _, n := range nodes {
		root := n.Completion.RootRunSlug
		rank := ranks[root]
		if rank == 0 && g.Anchor == "" {
			g.Anchor = root
		}
		row := rows[rank]
		rows[rank]++
		node := connectedRunNode{Record: n, X: 32 + rank*440, Y: 32 + row*160, Href: connectedSelectionHref(base, root, connectedRunTarget(n.Completion)), Selected: root == selected}
		g.Width = max(g.Width, node.X+292)
		g.Height = max(g.Height, node.Y+150)
		g.Nodes = append(g.Nodes, node)
		if node.Selected {
			selectedRecord := n
			g.Selected = &selectedRecord
		}
	}
	for _, d := range g.Decisions {
		if d.ChildRunSlug == nil {
			continue
		}
		from, ok := indexes[d.RootRunSlug]
		to, ok2 := indexes[*d.ChildRunSlug]
		if !ok || !ok2 {
			continue
		}
		a, b := g.Nodes[from], g.Nodes[to]
		x1, y1, x2, y2 := a.X+260, a.Y+55, b.X, b.Y+55
		mid := (x1 + x2) / 2
		g.Edges = append(g.Edges, connectedRunEdge{Decision: d, Path: fmt.Sprintf("M %d %d C %d %d %d %d %d %d", x1, y1, mid, y1, mid, y2, x2, y2), X: mid, Y: (y1 + y2) / 2})
	}
	return g
}

func connectedRunState(e api.PipelineRunCompletion) string {
	if e.Closed {
		return completionRootState(e)
	}
	status := ""
	if len(e.Attempts) > 0 {
		status = e.Attempts[len(e.Attempts)-1].Status
	}
	switch {
	case status == "running":
		return "Executing"
	case e.WaitingForRetry:
		return "Retry waiting"
	case status == "queued":
		return "Queued"
	case status != "":
		return "Logical closure unavailable · last attempt " + status
	default:
		return "Execution evidence unavailable"
	}
}

func connectedPending(data *api.PipelineRunConnected) bool {
	if data == nil {
		return false
	}
	for _, n := range data.Nodes {
		if completionPending(&n.Completion) {
			return true
		}
	}
	return false
}

func connectedSelectedRoot(data *api.PipelineRunConnected, selected, run string) string {
	if selected != "" {
		return selected
	}
	for _, n := range data.Nodes {
		if n.Completion.RootRunSlug == run {
			return run
		}
		for _, a := range n.Completion.Attempts {
			if a.RunSlug == run {
				return n.Completion.RootRunSlug
			}
		}
	}
	return ""
}

// Retention can remove the root attempt before a later retry expires.
func connectedRunTarget(evidence api.PipelineRunCompletion) string {
	if connectedAttemptRecorded(evidence, evidence.RootRunSlug) {
		return evidence.RootRunSlug
	}
	if len(evidence.Attempts) > 0 {
		return evidence.Attempts[len(evidence.Attempts)-1].RunSlug
	}
	return evidence.RootRunSlug
}

func connectedAttemptRecorded(evidence api.PipelineRunCompletion, run string) bool {
	for _, attempt := range evidence.Attempts {
		if attempt.RunSlug == run {
			return true
		}
	}
	return false
}
