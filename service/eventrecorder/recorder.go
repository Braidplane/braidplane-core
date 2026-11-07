package eventrecorder

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

// Event is a typed domain event that the recorder can persist as a canonical
// durable event row.
type Event interface {
	EventRecord() (Record, error)
}

type Record struct {
	SlugPrefix    slug.Slug
	EventClass    model.EventClass
	EntityType    model.EventEntityType
	EntitySlug    slug.Slug
	Action        model.EventAction
	OccurredAt    time.Time
	SchemaVersion int
	Metadata      map[string]any
}

type eventService interface {
	CreateEvent(ctx context.Context, params eventssvc.CreateEventParams) (*model.Event, error)
}

type Recorder interface {
	Record(
		ctx context.Context,
		orgSlug slug.Slug,
		actor model.EventActor,
		event Event,
	) (*model.Event, error)
}

type recorder struct {
	events eventService
}

func New(events eventService) Recorder {
	return &recorder{events: events}
}

func (r *recorder) Record(
	ctx context.Context,
	orgSlug slug.Slug,
	actor model.EventActor,
	event Event,
) (*model.Event, error) {
	params, err := ParamsFor(orgSlug, actor, event)
	if err != nil {
		return nil, err
	}
	return r.events.CreateEvent(ctx, params)
}

func ParamsFor(
	orgSlug slug.Slug,
	actor model.EventActor,
	event Event,
) (eventssvc.CreateEventParams, error) {
	if event == nil {
		return eventssvc.CreateEventParams{}, xerror.InvalidRequired(xerror.Event)
	}
	record, err := event.EventRecord()
	if err != nil {
		return eventssvc.CreateEventParams{}, err
	}
	return createEventParams(orgSlug, actor, record)
}

func createEventParams(
	orgSlug slug.Slug,
	actor model.EventActor,
	record Record,
) (eventssvc.CreateEventParams, error) {
	eventSlug, err := slug.WithRandomSuffix(record.SlugPrefix, 12)
	if err != nil {
		return eventssvc.CreateEventParams{}, xerror.Wrapf("generate event slug: %w", err)
	}

	return eventssvc.CreateEventParams{
		OrgSlug:       orgSlug,
		Slug:          eventSlug,
		EventClass:    record.EventClass,
		ActorType:     actor.Type,
		ActorSlug:     actor.Slug,
		EntityType:    record.EntityType,
		EntitySlug:    &record.EntitySlug,
		Action:        record.Action,
		OccurredAt:    record.OccurredAt,
		SchemaVersion: record.SchemaVersion,
		Metadata:      record.Metadata,
	}, nil
}
