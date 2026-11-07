package pipelineruntime

import (
	"context"
	"testing"

	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/stretchr/testify/require"
)

func TestPrepareCheckpointBootstrapUsesResolvedInlineIdentityWithoutExecuting(t *testing.T) {
	t.Parallel()
	store := newIncrementalCheckpointTestRunStore(nil)
	executor := &incrementalCheckpointQueryExecutor{}
	runner := mustNewIncrementalCheckpointRunner(t, executor, store, runnerNoopSinkRuntime{})
	pipeline := incrementalCheckpointPipeline()
	query := pipeline.Steps[0].Query
	query.OperationRef = ""
	query.OperationInline = map[string]any{"type": "postgres", "sql": "SELECT id, updated_at FROM users WHERE id > {{runtime.input.min_id}}"}
	lookups, err := runner.PrepareCheckpointBootstrap(context.Background(), RunRequest{OrgSlug: "acme", ProjectSlug: "default", EnvironmentSlug: "dev", Pipeline: pipeline, PipelineVersionID: 42, RuntimeInput: map[string]any{"min_id": 10}})
	require.NoError(t, err)
	require.Len(t, lookups, 1)
	expected, _, err := incrementalCheckpointLookupForStep(pipeline.Metadata.Slug, 42, pipelinerun.DefaultIncrementalCheckpointNamespace, pipeline.Steps[0], slug.Slug(""), map[string]any{"type": "postgres", "sql": "SELECT id, updated_at FROM users WHERE id > 10"}, true)
	require.NoError(t, err)
	require.Equal(t, expected, lookups[0])
	require.Contains(t, query.OperationInline["sql"], "runtime.input.min_id")
	require.Empty(t, executor.checkpointInputs)
	require.Empty(t, store.persistedRuns)
}
