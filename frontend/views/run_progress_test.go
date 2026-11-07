package views

import (
	"bytes"
	"net/url"
	"testing"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestProgressPollPreservesExactSelectionAndShowsUnknownLiveness(t *testing.T) {
	ctx := WithEntityPageURL(t.Context(), "/orgs/acme/projects/ops/runs/attempt-2?environment_slug=staging&section=steps&lineage_step=normalize&list_return=%2Fscoped-list")
	now := time.Now().UTC()
	progress := &api.PipelineRunProgress{Revision: 7, EventCount: 205, Metadata: api.PipelineRunMetadata{RunSlug: "attempt-2", Status: "running", StepCount: 4, StartedAt: now.Add(-time.Minute)}, WorkerLiveness: "stale", ServerTime: now, CompletedSteps: 2, PendingSteps: 1}
	href, err := url.Parse(runProgressHref(ctx, "/orgs/acme/projects/ops/runs/attempt-2?environment_slug=staging", progress))
	require.NoError(t, err)
	require.Equal(t, "normalize", href.Query().Get("lineage_step"))
	require.Equal(t, "steps", href.Query().Get("section"))
	require.Equal(t, "staging", href.Query().Get("environment_slug"))
	require.Equal(t, "205", href.Query().Get("progress_events"))
	require.Equal(t, "7", href.Query().Get("progress_revision"))
	require.Equal(t, "/scoped-list", href.Query().Get("list_return"))
	var output bytes.Buffer
	require.NoError(t, PipelineRunProgressPanel(href.Path, RunProgressInspection{Evidence: progress, Poll: true}).Render(ctx, &output))
	require.Contains(t, output.String(), "liveness unknown")
	require.Contains(t, output.String(), "A quiet step")
	require.Contains(t, output.String(), "2 completed")
	require.Contains(t, output.String(), `hx-trigger="every 2s"`)
	require.NotContains(t, output.String(), "50%")
}
