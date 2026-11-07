package jobs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/must"
)

func TestPipelineTriggerRetryExecutionArgs(t *testing.T) {
	t.Parallel()

	scheduledAt := time.Date(2026, time.June, 9, 7, 1, 0, 0, time.UTC)
	tests := []struct {
		name string
		args PipelineTriggerRetryExecutionArgs
	}{
		{
			name: "DefaultScheduledRetry",
			args: PipelineTriggerRetryExecutionArgs{
				OrganizationSlug: must.Slug("acme"),
				ProjectSlug:      must.Slug("default"),
				EnvironmentSlug:  must.Slug("prod"),
				RetryAttemptSlug: must.Slug("retry-fire-daily-users-20260609-2"),
				ScheduledAt:      scheduledAt,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			opts := test.args.InsertOpts()

			require.Equal(t, "pipeline_retry", PipelineRetryQueue)
			require.Equal(t, "pipeline_trigger_retry_execution", test.args.Kind())
			require.Equal(t, PipelineRetryQueue, opts.Queue)
			require.True(t, opts.ScheduledAt.Equal(scheduledAt))
			require.True(t, opts.UniqueOpts.ByArgs)
		})
	}
}
