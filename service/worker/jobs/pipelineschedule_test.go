package jobs

import (
	"testing"
	"time"

	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/must"
)

func TestPipelineScheduleDispatchArgsUsesScheduleQueue(t *testing.T) {
	t.Parallel()

	args := PipelineScheduleDispatchArgs{}
	opts := args.InsertOpts()

	require.Equal(t, "pipeline_schedule_dispatch", args.Kind())
	require.Equal(t, PipelineScheduleQueue, opts.Queue)
}

func TestPipelineScheduleFireExecutionArgsUsesScheduleQueueAndUniqueFireIdentity(t *testing.T) {
	t.Parallel()

	args := PipelineScheduleFireExecutionArgs{
		OrganizationSlug: must.Slug("acme"),
		ProjectSlug:      must.Slug("default"),
		EnvironmentSlug:  must.Slug("prod"),
		TriggerSlug:      must.Slug("daily-users"),
		TriggerRevision:  7,
		ScheduledFireAt:  time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC),
	}
	opts := args.InsertOpts()

	require.Equal(t, "pipeline_schedule_fire_execution", args.Kind())
	require.Equal(t, PipelineScheduleQueue, opts.Queue)
	require.True(t, opts.UniqueOpts.ByArgs)
	require.ElementsMatch(t, rivertype.UniqueOptsByStateDefault(), opts.UniqueOpts.ByState)
}
