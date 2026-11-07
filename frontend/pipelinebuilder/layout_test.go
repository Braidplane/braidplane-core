package pipelinebuilder

import (
	"testing"

	"github.com/stretchr/testify/require"

	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestLayoutGraphHandlesNilAndEmptySpecs(t *testing.T) {
	t.Parallel()

	require.Empty(t, LayoutGraph(nil))
	require.Empty(t, LayoutGraph(&PipelineSpec{}))
}

func TestLayoutGraphBucketsStepsByKindAndPreservesOrder(t *testing.T) {
	t.Parallel()

	spec := &PipelineSpec{
		Steps: []pipelinespec.Step{
			{Slug: slug.Slug("event"), Kind: "input"},
			{Slug: slug.Slug("fetch-users"), Kind: "query"},
			{Slug: slug.Slug("normalize-users"), Kind: "transform"},
			{Slug: slug.Slug("fetch-orders"), Kind: "query"},
			{Slug: slug.Slug("persist-users"), Kind: "sink"},
			{Slug: slug.Slug("custom-step"), Kind: "custom"},
		},
	}

	require.Equal(t, map[string]Position{
		"event":           {X: 2, Y: 1},
		"fetch-users":     {X: 2, Y: 4},
		"fetch-orders":    {X: 2, Y: 7},
		"custom-step":     {X: 2, Y: 10},
		"normalize-users": {X: 9, Y: 1},
		"persist-users":   {X: 16, Y: 1},
	}, LayoutGraph(spec))
}
