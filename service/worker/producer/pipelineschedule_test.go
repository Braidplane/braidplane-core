package producer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestPipelineScheduleProducer(t *testing.T) {
	t.Parallel()

	args := jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: must.Slug("acme"),
		ProjectSlug:      must.Slug("default"),
		EnvironmentSlug:  must.Slug("prod"),
		TriggerSlug:      must.Slug("daily-users"),
		TriggerRevision:  7,
		ScheduledFireAt:  time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC),
	}
	enqueueErr := producerTestError("queue unavailable")
	tests := []struct {
		name          string
		enqueuer      *recordingEnqueuer
		args          jobs.PipelineScheduleFireExecutionArgs
		expectedQueue queue.QueuedJob
		errContains   string
		wantErr       error
	}{
		{
			name: "EnqueuesFireExecution",
			enqueuer: &recordingEnqueuer{
				queued: queue.QueuedJob{
					Kind:  "pipeline_schedule_fire_execution",
					Queue: jobs.PipelineScheduleQueue,
					State: "queued",
				},
			},
			args: args,
			expectedQueue: queue.QueuedJob{
				Kind:  "pipeline_schedule_fire_execution",
				Queue: jobs.PipelineScheduleQueue,
				State: "queued",
			},
		},
		{
			name:     "ReturnsEnqueueError",
			enqueuer: &recordingEnqueuer{err: enqueueErr},
			args:     args,
			wantErr:  enqueueErr,
		},
		{
			name:        "RequiresEnqueuer",
			errContains: "queue enqueuer is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var enqueuer queue.Enqueuer
			if test.enqueuer != nil {
				enqueuer = test.enqueuer
			}
			producer, err := NewPipelineScheduleProducer(enqueuer)
			if test.errContains != "" {
				require.Nil(t, producer)
				require.ErrorContains(t, err, test.errContains)
				return
			}
			require.NoError(t, err)

			queued, err := producer.EnqueueScheduleFireExecution(context.Background(), nil, test.args)
			if test.wantErr != nil {
				require.Empty(t, queued)
				require.ErrorIs(t, err, test.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, test.expectedQueue, queued)
			require.Equal(t, 1, test.enqueuer.calls)
			require.Nil(t, test.enqueuer.tx)
			require.Equal(t, jobs.PipelineScheduleQueue, test.enqueuer.req.Queue)
			require.Equal(t, test.args, test.enqueuer.req.Args)
			require.Equal(t, jobs.PipelineScheduleFireExecutionArgs{}.Kind(), test.enqueuer.req.Args.Kind())
		})
	}
}

type producerTestError string

func (e producerTestError) Error() string {
	return string(e)
}
