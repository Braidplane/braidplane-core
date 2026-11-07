package jobs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPipelineWebhookDeliveryArgsInsertOpts(t *testing.T) {
	t.Parallel()

	scheduledAt := time.Date(2026, 8, 21, 12, 0, 30, 0, time.UTC)
	args := PipelineWebhookDeliveryArgs{
		ReceiverID: "receiver-opaque", DeliveryID: "delivery-1", RunAttempt: 2, ScheduledAt: scheduledAt,
	}

	require.Equal(t, PipelineScheduleQueue, args.InsertOpts().Queue)
	require.Equal(t, "pipeline_webhook_delivery", args.Kind())
	require.Equal(t, scheduledAt, args.InsertOpts().ScheduledAt)
	require.True(t, args.InsertOpts().UniqueOpts.ByArgs)
}
