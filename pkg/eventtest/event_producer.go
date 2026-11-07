package eventtest

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

type eventService interface {
	CreateEvent(ctx context.Context, params eventssvc.CreateEventParams) (*model.Event, error)
}

type producer struct {
	events eventService
}

func NewProducer(events eventService) workerproducer.EventProducer {
	return producer{events: events}
}

func (p producer) EnqueueRecord(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) (workerproducer.EventRecordJob, bool) {
	params, err := eventrecorder.ParamsFor(orgSlug, actor, event)
	if err != nil {
		return workerproducer.EventRecordJob{}, false
	}
	if p.events == nil {
		return workerproducer.EventRecordJob{EventSlug: params.Slug}, true
	}
	created, err := p.events.CreateEvent(ctx, params)
	if err != nil {
		return workerproducer.EventRecordJob{}, false
	}
	if created != nil {
		return workerproducer.EventRecordJob{EventSlug: created.Slug}, true
	}
	return workerproducer.EventRecordJob{EventSlug: params.Slug}, true
}

func (p producer) EnqueueRequired(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) (workerproducer.EventRecordJob, error) {
	params, err := eventrecorder.ParamsFor(orgSlug, actor, event)
	if err != nil {
		return workerproducer.EventRecordJob{}, err
	}
	params, err = eventssvc.PrepareCreateEventParams(ctx, params)
	if err != nil {
		return workerproducer.EventRecordJob{}, err
	}
	if params.EventClass != model.EventClassSecurityAudit {
		return workerproducer.EventRecordJob{}, xerror.InvalidInput(xerror.Event).
			Detail("required durable event must use the security audit class")
	}
	if p.events == nil {
		return workerproducer.EventRecordJob{EventSlug: params.Slug}, nil
	}
	created, err := p.events.CreateEvent(ctx, params)
	if err != nil {
		return workerproducer.EventRecordJob{}, err
	}
	if created != nil {
		return workerproducer.EventRecordJob{EventSlug: created.Slug}, nil
	}
	return workerproducer.EventRecordJob{EventSlug: params.Slug}, nil
}
