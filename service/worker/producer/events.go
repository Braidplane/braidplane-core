package producer

import (
	"context"
	"log/slog"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

// EventProducer enqueues durable event persistence for background execution.
type EventProducer interface {
	EnqueueRecord(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		actor model.EventActor,
		event eventrecorder.Event,
	) (EventRecordJob, bool)
	EnqueueRequired(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		actor model.EventActor,
		event eventrecorder.Event,
	) (EventRecordJob, error)
}

type EventRecordJob struct {
	Queued    queue.QueuedJob
	EventSlug slug.Slug
}

type eventProducer struct {
	enqueuer queue.Enqueuer
	clk      clock.Clock
}

func NewEventProducer(enqueuer queue.Enqueuer, clk clock.Clock) (EventProducer, error) {
	if enqueuer == nil {
		return nil, xerror.Unexpected().Detail("queue enqueuer is required")
	}
	if clk == nil {
		return nil, xerror.Unexpected().Detail("clock is required")
	}

	return &eventProducer{enqueuer: enqueuer, clk: clk}, nil
}

func (p *eventProducer) EnqueueRecord(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) (EventRecordJob, bool) {
	attrs := eventLogAttrs(orgSlug, event)
	job, err := p.enqueue(ctx, tx, orgSlug, actor, event, false)
	if err != nil {
		attrs = append(attrs, slog.Any("error", err))
		slog.LogAttrs(ctx, slog.LevelWarn, "durable event enqueue failed", attrs...)
		return EventRecordJob{}, false
	}
	return job, true
}

func (p *eventProducer) EnqueueRequired(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) (EventRecordJob, error) {
	return p.enqueue(ctx, tx, orgSlug, actor, event, true)
}

func (p *eventProducer) enqueue(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
	required bool,
) (EventRecordJob, error) {
	if p == nil || p.enqueuer == nil {
		return EventRecordJob{}, xerror.Unexpected().Detail("durable event producer unavailable")
	}

	params, err := eventrecorder.ParamsFor(orgSlug, actor, event)
	if err != nil {
		return EventRecordJob{}, err
	}
	if params.OccurredAt.IsZero() {
		params.OccurredAt = p.clk.Now().UTC()
	}
	params, err = eventssvc.PrepareCreateEventParams(ctx, params)
	if err != nil {
		return EventRecordJob{}, err
	}
	if required && params.EventClass != model.EventClassSecurityAudit {
		return EventRecordJob{}, xerror.InvalidInput(xerror.Event).
			Detail("required durable event must use the security audit class")
	}

	queued, err := p.enqueuer.EnqueueTx(ctx, tx, queue.EnqueueRequest{
		Args: jobs.EventRecordArgs{
			OrgSlug:       params.OrgSlug,
			Slug:          params.Slug,
			EventClass:    params.EventClass,
			RequestID:     params.RequestID,
			SchemaVersion: params.SchemaVersion,
			ActorType:     params.ActorType,
			ActorSlug:     params.ActorSlug,
			EntityType:    params.EntityType,
			EntitySlug:    params.EntitySlug,
			Action:        params.Action,
			OccurredAt:    params.OccurredAt,
			Metadata:      params.Metadata,
		},
		Queue: jobs.EventsQueue,
	})
	if err != nil {
		return EventRecordJob{}, xerror.Wrapf("enqueue durable event: %w", err)
	}
	return EventRecordJob{Queued: queued, EventSlug: params.Slug}, nil
}

func eventLogAttrs(orgSlug slug.Slug, event eventrecorder.Event) []slog.Attr {
	attrs := []slog.Attr{slog.String("org_slug", orgSlug.String())}
	if event == nil {
		return attrs
	}

	record, err := event.EventRecord()
	if err != nil {
		return append(attrs, slog.Any("record_error", err))
	}

	attrs = append(attrs,
		slog.String("entity_type", record.EntityType.String()),
		slog.String("entity_slug", record.EntitySlug.String()),
		slog.String("action", record.Action.String()),
	)
	return attrs
}
