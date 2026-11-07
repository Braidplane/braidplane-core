package e2e

import (
	"context"

	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	workerhostsvc "github.com/Braidplane/braidplane-core/service/worker"
)

// workerE2EQueueConcurrency returns positive registrar capacities for worker-host integration tests.
func workerE2EQueueConcurrency() workerhostsvc.QueueConcurrency {
	return workerhostsvc.QueueConcurrency{
		PipelineGit:            1,
		Email:                  1,
		NotificationDelivery:   1,
		Events:                 1,
		EventMaintenance:       1,
		PipelineSchedule:       4,
		PipelineRetry:          4,
		PipelineRun:            4,
		DataSourceSchemaImport: 2,
	}
}

type noopNotificationDeliveryService struct{}

func (noopNotificationDeliveryService) CreateCandidate(context.Context, notificationsvc.CreateCandidateParams) (*notificationsvc.CreateCandidateResult, error) {
	return &notificationsvc.CreateCandidateResult{}, nil
}

// DeliverNotification satisfies the required worker dependency for tests that do not enqueue notification jobs.
func (noopNotificationDeliveryService) DeliverNotification(
	context.Context,
	notificationsvc.DeliverNotificationParams,
) error {
	return nil
}
