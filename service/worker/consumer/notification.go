package consumer

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/xerror"

	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

// NotificationDeliveryService executes one notification dispatch.
type NotificationDeliveryService interface {
	CreateCandidate(context.Context, notificationsvc.CreateCandidateParams) (*notificationsvc.CreateCandidateResult, error)
	DeliverNotification(ctx context.Context, params notificationsvc.DeliverNotificationParams) error
}

// NewNotificationDeliveryWorker creates the worker that executes queued notification delivery.
func NewNotificationDeliveryWorker(
	service NotificationDeliveryService,
	logger *slog.Logger,
) river.Worker[jobs.NotificationDeliveryArgs] {
	if service == nil {
		panic("nil NotificationDeliveryService passed to NewNotificationDeliveryWorker")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &notificationDeliveryWorker{
		service: service,
		logger:  logger,
	}
}

type notificationDeliveryWorker struct {
	river.WorkerDefaults[jobs.NotificationDeliveryArgs]

	service NotificationDeliveryService
	logger  *slog.Logger
}

// Work executes one queued notification dispatch and returns dependency errors so River can retry.
func (w *notificationDeliveryWorker) Work(
	ctx context.Context,
	job *river.Job[jobs.NotificationDeliveryArgs],
) error {
	args := job.Args
	params := notificationsvc.DeliverNotificationParams{
		Test:            args.Test,
		OrgSlug:         args.OrganizationSlug,
		RecordSlug:      args.RecordSlug,
		DestinationSlug: args.DestinationSlug,
		AlertPolicySlug: args.AlertPolicySlug,
	}

	w.logger.InfoContext(
		ctx,
		"notification delivery job started",
		slog.String("organization_slug", args.OrganizationSlug.String()),
		slog.String("record_slug", args.RecordSlug.String()),
		slog.String("destination_slug", args.DestinationSlug.String()),
		slog.String("alert_policy_slug", args.AlertPolicySlug.String()),
	)
	if err := w.service.DeliverNotification(ctx, params); err != nil {
		var deliveryErr *notificationsvc.DeliveryError
		if xerror.As(err, &deliveryErr) {
			if !deliveryErr.Retryable {
				return xerror.Wrapf("cancel notification delivery: %w", river.JobCancel(err))
			}
			if deliveryErr.RetryAfter > 0 {
				return xerror.Wrapf("snooze notification delivery: %w", river.JobSnooze(deliveryErr.RetryAfter))
			}
		}
		w.logger.ErrorContext(ctx, "notification delivery job failed", slog.Any("error", err))
		return err
	}

	w.logger.InfoContext(ctx, "notification delivery job succeeded")
	return nil
}
