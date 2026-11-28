package pipelineruntime

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

func TestArtifactExecutionStaysDisabled(t *testing.T) {
	producer := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{{Sink: &pipelinespec.SinkSpec{ArtifactDelivery: true}}}}
	consumer := &pipelinespec.Pipeline{Steps: []pipelinespec.Step{{Query: &pipelinespec.QueryRef{ArtifactInput: true}}}}
	// A zero runner cannot perform I/O. The refusal must precede dependency use.
	runner := &Runner{}
	for _, pipeline := range []*pipelinespec.Pipeline{producer, consumer} {
		for _, test := range []struct {
			name    string
			source  model.PipelineRunSourceType
			options pipelinerun.IncrementalRunOptions
		}{
			{name: "preview"},
			{name: "manual", source: model.PipelineRunSourceTypeManual},
			{name: "api", source: model.PipelineRunSourceTypeAPI},
			{name: "schedule", source: model.PipelineRunSourceTypeSchedule},
			{name: "webhook", source: model.PipelineRunSourceTypeWebhook},
			{name: "completion", source: model.PipelineRunSourceTypeCompletion},
			{name: "backfill", source: model.PipelineRunSourceTypeAPI, options: pipelinerun.IncrementalRunOptions{BackfillSeriesSlug: "backfill"}},
		} {
			t.Run(test.name, func(t *testing.T) {
				out, err := runner.Run(t.Context(), RunRequest{
					Pipeline: pipeline, Source: pipelinerun.RunSourceSummary{Type: test.source}, Incremental: test.options,
				})
				require.Nil(t, out)
				require.ErrorContains(t, err, "artifact handoff is not enabled")
			})
		}
		runner.store = &fakeRunStore{checkpoints: map[string]pipelinerun.ReplayCheckpoint{
			"original": {Pipeline: pipeline, Results: map[slug.Slug]*pipelinerun.StepResult{"source": {}}},
		}}
		out, err := runner.Replay(t.Context(), ReplayRequest{OrgSlug: "test", SourceRunSlug: "original", InputMode: ReplayInputModePinned})
		require.Nil(t, out)
		require.ErrorIs(t, err, ErrReplayUnsafe)
	}
}
