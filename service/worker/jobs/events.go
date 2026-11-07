package jobs

import (
	"time"

	"github.com/riverqueue/river"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const EventsQueue = "events"

// EventRecordArgs is the durable River payload for persisting a canonical
// durable event row after the source transaction commits.
type EventRecordArgs struct {
	OrgSlug       slug.Slug             `json:"org_slug"`
	Slug          slug.Slug             `json:"slug"`
	EventClass    model.EventClass      `json:"event_class"`
	RequestID     *string               `json:"request_id,omitempty"`
	SchemaVersion int                   `json:"schema_version"`
	ActorType     model.EventActorType  `json:"actor_type"`
	ActorSlug     *slug.Slug            `json:"actor_slug,omitempty"`
	EntityType    model.EventEntityType `json:"entity_type"`
	EntitySlug    *slug.Slug            `json:"entity_slug,omitempty"`
	Action        model.EventAction     `json:"action"`
	OccurredAt    time.Time             `json:"occurred_at,omitempty"`
	Metadata      map[string]any        `json:"metadata,omitempty"`
}

func (EventRecordArgs) Kind() string {
	return "event_record"
}

func (EventRecordArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: EventsQueue}
}
