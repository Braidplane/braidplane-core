package eventssvc

import (
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

type CreateEventParams struct {
	OrgSlug       slug.Slug `validate:"required"`
	Slug          slug.Slug `validate:"required"`
	EventClass    model.EventClass
	ActorType     model.EventActorType `validate:"eventactortype"`
	ActorSlug     *slug.Slug
	EntityType    model.EventEntityType `validate:"evententitytype"`
	EntitySlug    *slug.Slug
	Action        model.EventAction `validate:"eventaction"`
	OccurredAt    time.Time
	RequestID     *string
	SchemaVersion int
	Metadata      map[string]any
}

type ListEventsFilters struct {
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
	Scope           model.AlertPolicyScope
	ProjectSlug     *slug.Slug
	EnvironmentSlug *slug.Slug
	SourceKind      *model.EventEntityType
	SourceAction    *model.EventAction
}
