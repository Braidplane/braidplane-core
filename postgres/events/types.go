package events

import (
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateParams struct {
	Slug          slug.Slug
	EventClass    model.EventClass
	ActorType     model.EventActorType
	ActorSlug     *slug.Slug
	EntityType    model.EventEntityType
	EntitySlug    *slug.Slug
	Action        model.EventAction
	OccurredAt    time.Time
	RequestID     *string
	SchemaVersion int32
	Metadata      json.RawMessage
}

type ListFilters struct {
	EventClass     *model.EventClass
	RequestID      *string
	OccurredFrom   *time.Time
	OccurredBefore *time.Time
	ActorType      *model.EventActorType
	ActorSlug      *slug.Slug
	EntityType     *model.EventEntityType
	EntitySlug     *slug.Slug
	Action         *model.EventAction
}

type SourceMetadataFilters struct {
	SourceKind      *model.EventEntityType
	SourceAction    *model.EventAction
	ProjectSlug     *slug.Slug
	EnvironmentSlug *slug.Slug
}

type Event struct {
	Slug          slug.Slug
	EventClass    model.EventClass
	ActorType     model.EventActorType
	ActorSlug     *slug.Slug
	EntityType    model.EventEntityType
	EntitySlug    *slug.Slug
	Action        model.EventAction
	OccurredAt    time.Time
	RequestID     *string
	SchemaVersion int
	Metadata      json.RawMessage
	CreatedAt     time.Time
}
