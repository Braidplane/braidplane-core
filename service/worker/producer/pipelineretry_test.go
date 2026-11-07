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

func TestPipelineRetryProducerEnqueuesTriggerRetryExecution(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{
		queued: queue.QueuedJob{
			Kind:  "pipeline_trigger_retry_execution",
			Queue: jobs.PipelineRetryQueue,
			State: "queued",
		},
	}
	producer, err := NewPipelineRetryProducer(enqueuer)
	require.NoError(t, err)
	args := jobs.PipelineTriggerRetryExecutionArgs{
		OrganizationSlug: must.Slug("acme"),
		ProjectSlug:      must.Slug("default"),
		EnvironmentSlug:  must.Slug("prod"),
		RetryAttemptSlug: must.Slug("retry-fire-daily-users-20260609-2"),
		ScheduledAt:      time.Date(2026, time.June, 9, 7, 1, 0, 0, time.UTC),
	}

	queued, err := producer.EnqueueTriggerRetryExecution(context.Background(), nil, args)

	require.NoError(t, err)
	require.Equal(t, enqueuer.queued, queued)
	require.Equal(t, 1, enqueuer.calls)
	require.Nil(t, enqueuer.tx)
	require.Equal(t, jobs.PipelineRetryQueue, enqueuer.req.Queue)
	require.Equal(t, args, enqueuer.req.Args)
}

func TestPipelineRetryProducerReturnsEnqueueError(t *testing.T) {
	t.Parallel()

	enqueueErr := producerTestError("queue unavailable")
	producer, err := NewPipelineRetryProducer(&recordingEnqueuer{err: enqueueErr})
	require.NoError(t, err)

	queued, err := producer.EnqueueTriggerRetryExecution(context.Background(), nil, jobs.PipelineTriggerRetryExecutionArgs{
		OrganizationSlug: must.Slug("acme"),
		ProjectSlug:      must.Slug("default"),
		EnvironmentSlug:  must.Slug("prod"),
		RetryAttemptSlug: must.Slug("retry-fire-daily-users-20260609-2"),
		ScheduledAt:      time.Date(2026, time.June, 9, 7, 1, 0, 0, time.UTC),
	})

	require.Empty(t, queued)
	require.ErrorIs(t, err, enqueueErr)
}

func TestNewPipelineRetryProducerRequiresEnqueuer(t *testing.T) {
	t.Parallel()

	producer, err := NewPipelineRetryProducer(nil)

	require.Nil(t, producer)
	require.ErrorContains(t, err, "queue enqueuer is required")
}
