package consumer

import (
	"context"
	"log/slog"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

type eventService interface {
	CreateEvent(ctx context.Context, params eventssvc.CreateEventParams) (*model.Event, error)
}

func NewEventRecordWorker(
	events eventService,
	logger *slog.Logger,
) river.Worker[jobs.EventRecordArgs] {
	return &eventRecordWorker{
		events: events,
		logger: logger,
	}
}

type eventRecordWorker struct {
	river.WorkerDefaults[jobs.EventRecordArgs]

	events eventService
	logger *slog.Logger
}

func (w *eventRecordWorker) Work(
	ctx context.Context,
	job *river.Job[jobs.EventRecordArgs],
) error {
	args := job.Args
	if w.events == nil {
		return xerror.Unexpected().Detail("durable event service is required")
	}

	w.logger.InfoContext(
		ctx,
		"event record job started",
		slog.String("organization_slug", args.OrgSlug.String()),
		slog.String("event_slug", args.Slug.String()),
	)

	_, err := w.events.CreateEvent(ctx, eventssvc.CreateEventParams{
		OrgSlug:       args.OrgSlug,
		Slug:          args.Slug,
		EventClass:    args.EventClass,
		RequestID:     args.RequestID,
		SchemaVersion: args.SchemaVersion,
		ActorType:     args.ActorType,
		ActorSlug:     args.ActorSlug,
		EntityType:    args.EntityType,
		EntitySlug:    args.EntitySlug,
		Action:        args.Action,
		OccurredAt:    args.OccurredAt,
		Metadata:      args.Metadata,
	})
	if err != nil {
		if xerror.Is(err, eventssvc.ErrSlugTaken) {
			w.logger.InfoContext(ctx, "event record job skipped duplicate slug")
			return nil
		}
		w.logger.ErrorContext(ctx, "event record job failed", slog.Any("error", err))
		return err
	}

	w.logger.InfoContext(ctx, "event record job succeeded")
	return nil
}
