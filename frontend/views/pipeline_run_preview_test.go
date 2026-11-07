package views

import (
	"bytes"
	"context"
	"net/url"
	"testing"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/stretchr/testify/require"
)

func TestPipelineRunPreviewVersionAndSelection(t *testing.T) {
	deployments := []PipelineDeploymentBadge{{EnvironmentSlug: "dev", FullSpecHash: "new-version-hash", ShortSpecHash: "new-version"}}
	require.Contains(t, pipelineRunVersionLabel("executed-version", deployments, "dev", false), "deployed now new-version")
	require.Contains(t, pipelineRunVersionLabel("new-version-hash", deployments, "dev", false), "current deployment")
	require.Contains(t, pipelineRunVersionLabel("executed-version", deployments, "test", false), "no current deployment")
	require.Contains(t, pipelineRunVersionLabel("executed-version", deployments, "dev", true), "deployment unavailable")
	require.Equal(t, "Executed version not recorded", pipelineRunVersionLabel("", deployments, "dev", false))
	selected, err := url.Parse(pipelineRunSelectionHref("/orgs/acme/projects/ops/pipelines/orders?environment_slug=dev&list_return=catalog&lineage_step=old", "older"))
	require.NoError(t, err)
	require.Equal(t, "dev", selected.Query().Get("environment_slug"))
	require.Equal(t, "catalog", selected.Query().Get("list_return"))
	require.Equal(t, "older", selected.Query().Get("run_slug"))
	require.Empty(t, selected.Query().Get("lineage_step"))
}

func TestPipelineRunPreviewRenderedStates(t *testing.T) {
	for _, tt := range []struct {
		name, environment, want string
		preview                 PipelineRunPreview
	}{
		{name: "no target", want: "Select a runtime target"},
		{name: "empty target", environment: "dev", want: "No runs in dev"},
		{name: "denied history", environment: "dev", want: "Run history unavailable", preview: PipelineRunPreview{HistoryUnavailable: true}},
		{name: "retained evidence", environment: "dev", want: "Execution topology unavailable", preview: PipelineRunPreview{LatestSlug: "latest", SelectedSlug: "latest", Run: &api.PipelineRunRecord{Metadata: api.PipelineRunMetadata{RunSlug: "latest"}}}},
		{name: "selected historical", environment: "dev", want: "Selected historical run in dev", preview: PipelineRunPreview{LatestSlug: "latest", SelectedSlug: "older"}},
		{name: "latest unknown", environment: "dev", want: "Selected run in dev", preview: PipelineRunPreview{HistoryUnavailable: true, SelectedSlug: "older"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, PipelineEnvironmentRunPreview("acme", "ops", tt.environment, "orders", "/pipeline", tt.preview, nil, false, true).Render(context.Background(), &out))
			require.Contains(t, out.String(), tt.want)
			require.NotContains(t, out.String(), "No runs in test")
		})
	}
}
