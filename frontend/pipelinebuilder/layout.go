package pipelinebuilder

import (
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
)

// Position represents X,Y coordinates for a node in the builder graph.
type Position struct {
	X int
	Y int
}

// LayoutGraph computes deterministic positions for all nodes in the builder graph.
//
// Algorithm:
// 1. Assign each step to a fixed column by kind (query/transform/sink)
// 2. Within each column, preserve pipeline spec order (pipeline.steps array)
// 3. Assign Y by position within the kind bucket using fixed row spacing
func LayoutGraph(spec *PipelineSpec) map[string]Position {
	if spec == nil || len(spec.Steps) == 0 {
		return make(map[string]Position)
	}

	// Group steps by kind bucket while preserving spec order.
	type bucketNode struct {
		step pipelinespec.Step
	}

	buckets := map[string][]bucketNode{
		"query":     make([]bucketNode, 0),
		"transform": make([]bucketNode, 0),
		"sink":      make([]bucketNode, 0),
	}
	for _, step := range spec.Steps {
		switch step.Kind {
		case "input", "query":
			buckets["query"] = append(buckets["query"], bucketNode{step: step})
		case "transform":
			buckets["transform"] = append(buckets["transform"], bucketNode{step: step})
		case "sink":
			buckets["sink"] = append(buckets["sink"], bucketNode{step: step})
		default:
			// Fallback unknown kinds to query column for deterministic placement.
			buckets["query"] = append(buckets["query"], bucketNode{step: step})
		}
	}

	// Assign positions
	positions := make(map[string]Position, len(spec.Steps))
	const (
		queryColX = 2
		transColX = 9
		sinkColX  = 16
		firstRowY = 1
		rowGapY   = 3
	)

	placeBucket := func(nodes []bucketNode, x int) {
		for i, node := range nodes {
			y := firstRowY + i*rowGapY
			positions[node.step.Slug.String()] = Position{X: x, Y: y}
		}
	}

	placeBucket(buckets["query"], queryColX)
	placeBucket(buckets["transform"], transColX)
	placeBucket(buckets["sink"], sinkColX)

	return positions
}
