package e2e

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil/pipelinefixture"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
)

func materializePipelines(
	t *testing.T,
	ctx context.Context,
	store pipelinesvc.PipelineStore,
	orgSlug, projectSlug slug.Slug,
	pipelines ...*pipelinespec.Pipeline,
) {
	t.Helper()
	require.NoError(t, pipelinefixture.Materialize(ctx, store, orgSlug, projectSlug, pipelines...))
}
