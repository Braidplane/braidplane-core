package jobs

import (
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// PipelineWebhookDeliveryArgs identifies one encrypted accepted delivery.
type PipelineWebhookDeliveryArgs struct {
	FinalizeOnly bool      `json:"finalize_only,omitempty" river:"unique"`
	ReceiverID   string    `json:"receiver_id" river:"unique"`
	DeliveryID   string    `json:"delivery_id" river:"unique"`
	RunAttempt   int       `json:"run_attempt" river:"unique"`
	Cleanup      bool      `json:"cleanup,omitempty" river:"unique"`
	ScheduledAt  time.Time `json:"scheduled_at"`
}

func (PipelineWebhookDeliveryArgs) Kind() string { return "pipeline_webhook_delivery" }

func (a PipelineWebhookDeliveryArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: PipelineScheduleQueue, ScheduledAt: a.ScheduledAt,
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: rivertype.UniqueOptsByStateDefault()},
	}
}
