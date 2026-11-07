package pipelineruntime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/must"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
)

func TestRunOutcome_DerivesServiceFacingMetadata(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.May, 13, 10, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(75 * time.Millisecond)
	projectSlug := must.Slug("default")
	environmentSlug := must.Slug("prod")
	pipeline := &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: must.Slug("users-sync"),
			Name: "Users Sync",
		},
		Steps: []pipelinespec.Step{
			{
				Kind: "query",
				Slug: must.Slug("fetch-users"),
				Query: &pipelinespec.QueryRef{
					DataSourceSlug: must.Slug("users-db"),
					OperationType:  "query",
					OperationRef:   must.Slug("list-users"),
				},
			},
			{
				Kind: "sink",
				Slug: must.Slug("persist-users"),
				Sink: &pipelinespec.SinkSpec{
					IdempotencyClass: pipelinespec.SinkIdempotencyClassIdempotentWithKey,
					Input:            &pipelinespec.StepInput{FromStep: must.Slug("fetch-users")},
					DataSourceSlug:   must.Slug("warehouse"),
					OperationType:    "query",
					OperationRef:     must.Slug("insert-users"),
				},
			},
		},
	}
	runResult := &pipelinerun.RunResult{
		Metadata: pipelinerun.RunMetadata{
			ProjectSlug:       projectSlug,
			EnvironmentSlug:   environmentSlug,
			RunSlug:           "users-sync-run-001",
			Status:            "succeeded",
			DurationMs:        endedAt.Sub(startedAt).Milliseconds(),
			StepCount:         len(pipeline.Steps),
			PipelineVersionID: 123,
		},
		Timeline: pipelinerun.RunTimeline{
			RunSlug:         "users-sync-run-001",
			PipelineSlug:    pipeline.Metadata.Slug,
			ProjectSlug:     projectSlug,
			EnvironmentSlug: environmentSlug,
			Status:          "succeeded",
			Events: []pipelinerun.TimelineEvent{
				{At: startedAt, Type: "run_started", Status: "running"},
				{At: endedAt, Type: "run_completed", Status: "succeeded"},
			},
		},
	}

	outcome, err := newRunOutcome(pipeline, runResult)

	require.NoError(t, err)
	require.Equal(t, int64(123), outcome.Metadata.PipelineVersionID)
	require.Equal(t, pipeline.Metadata.Slug, outcome.Metadata.PipelineSlug)
	require.Equal(t, projectSlug, outcome.Metadata.ProjectSlug)
	require.Equal(t, environmentSlug, outcome.Metadata.EnvironmentSlug)
	require.Equal(t, "users-sync-run-001", outcome.Metadata.RunSlug)
	require.Equal(t, "succeeded", outcome.Metadata.Status)
	require.Equal(t, int64(75), outcome.Metadata.DurationMs)
	require.Equal(t, 2, outcome.Metadata.StepCount)
	require.Equal(t, "persist-users", outcome.Metadata.StepSlug)
	require.True(t, outcome.Metadata.StartedAt.Equal(startedAt))
	require.NotNil(t, outcome.Metadata.EndedAt)
	require.True(t, outcome.Metadata.EndedAt.Equal(endedAt))
	require.Equal(t, runResult.Timeline, outcome.Timeline)
}
