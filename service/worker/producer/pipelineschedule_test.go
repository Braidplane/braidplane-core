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

func TestPipelineScheduleProducerRoutesFinalizationsToOwnerQueue(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		args queue.JobArgs
		want string
	}{
		{args: jobs.PipelineScheduleFireExecutionArgs{FinalizeOnly: true}, want: jobs.PipelineScheduleQueue},
		{args: jobs.PipelineWebhookDeliveryArgs{FinalizeOnly: true}, want: jobs.PipelineScheduleQueue},
		{args: jobs.PipelineTriggerRetryExecutionArgs{FinalizeOnly: true}, want: jobs.PipelineRetryQueue},
	} {
		enqueuer := &recordingEnqueuer{}
		producer, err := NewPipelineScheduleProducer(enqueuer)
		require.NoError(t, err)

		_, err = producer.EnqueueRunFinalization(t.Context(), nil, test.args)
		require.NoError(t, err)
		require.Equal(t, queue.EnqueueRequest{Args: test.args, Queue: test.want}, enqueuer.req)
	}
}

func TestPipelineWebhookProducerRoutesRetentionCleanupToScheduleQueue(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{}
	producer, err := NewPipelineWebhookProducer(enqueuer)
	require.NoError(t, err)
	args := jobs.PipelineWebhookDeliveryArgs{
		ReceiverID: "receiver", DeliveryID: "delivery", RunAttempt: 1, Cleanup: true,
		ScheduledAt: time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC),
	}

	_, err = producer.EnqueuePipelineWebhookDelivery(t.Context(), nil, args)
	require.NoError(t, err)
	require.Equal(t, queue.EnqueueRequest{Args: args, Queue: jobs.PipelineScheduleQueue}, enqueuer.req)
	require.Equal(t, args.ScheduledAt, args.InsertOpts().ScheduledAt)
}

type producerTestError string

func (e producerTestError) Error() string {
	return string(e)
}
