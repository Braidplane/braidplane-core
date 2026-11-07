package producer

import (
	"context"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type notificationDeliveryProducer struct {
	enqueuer queue.Enqueuer
	queue    string
}

// NewNotificationDeliveryProducer creates a producer for the default delivery queue.
func NewNotificationDeliveryProducer(enqueuer queue.Enqueuer) (notificationsvc.NotificationDeliveryProducer, error) {
	return NewNotificationDeliveryProducerForQueue(enqueuer, jobs.NotificationDeliveryQueue)
}

// NewNotificationDeliveryProducerForQueue creates a producer for tests and custom worker queues.
func NewNotificationDeliveryProducerForQueue(
	enqueuer queue.Enqueuer,
	queueName string,
) (notificationsvc.NotificationDeliveryProducer, error) {
	if enqueuer == nil {
		return nil, xerror.Unexpected().Detail("queue enqueuer is required")
	}
	if strings.TrimSpace(queueName) == "" {
		return nil, xerror.Unexpected().Detail("notification delivery queue is required")
	}

	return &notificationDeliveryProducer{enqueuer: enqueuer, queue: queueName}, nil
}

// EnqueueNotificationDelivery inserts one notification delivery job in tx.
func (p *notificationDeliveryProducer) EnqueueNotificationDelivery(
	ctx context.Context,
	tx postgres.DBTX,
	params notificationsvc.NotificationDeliveryJobParams,
) (queue.QueuedJob, error) {
	return p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args: jobs.NotificationDeliveryArgs{
			Test:             params.Test,
			OrganizationSlug: params.OrgSlug,
			RecordSlug:       params.RecordSlug,
			DestinationSlug:  params.DestinationSlug,
			AlertPolicySlug:  params.AlertPolicySlug,
		},
		Queue: p.queue,
	})
}
