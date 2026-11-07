package views

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCurrentConnectionLayoutFollowsDependencyOrder(t *testing.T) {
	graph := PipelineConnections{Nodes: []PipelineConnectionNode{{Slug: "a"}, {Slug: "b"}, {Slug: "z"}}, Edges: []PipelineConnectionEdge{{From: "a", To: "b"}, {From: "z", To: "a"}}}
	PipelineConnectionsCanvas(&graph)
	require.Less(t, graph.Nodes[2].X, graph.Nodes[0].X)
	require.Less(t, graph.Nodes[0].X, graph.Nodes[1].X)
}
