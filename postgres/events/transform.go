package events

import (
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/dbgen"
)

func toEvent(row dbgen.Event) *Event {
	return &Event{
		Slug:          row.Slug,
		EventClass:    parseEventClass(row.EventClass),
		ActorType:     parseActorType(row.ActorType),
		ActorSlug:     optionalSlug(row.ActorSlug),
		EntityType:    parseEntityType(row.EntityType),
		EntitySlug:    optionalSlug(row.EntitySlug),
		Action:        parseAction(row.Action),
		OccurredAt:    row.OccurredAt.Time,
		RequestID:     postgres.FromNullableText(row.RequestID),
		SchemaVersion: int(row.SchemaVersion),
		Metadata:      row.Metadata,
		CreatedAt:     row.CreatedAt.Time,
	}
}

func parseEventClass(raw string) model.EventClass {
	value, err := model.EventClassString(raw)
	if err != nil {
		return model.EventClassUnknown
	}
	return value
}

func parseActorType(raw string) model.EventActorType {
	value, err := model.EventActorTypeString(raw)
	if err != nil {
		return model.EventActorTypeUnknown
	}
	return value
}

func parseEntityType(raw string) model.EventEntityType {
	value, err := model.EventEntityTypeString(raw)
	if err != nil {
		return model.EventEntityTypeUnknown
	}
	return value
}

func parseAction(raw string) model.EventAction {
	value, err := model.EventActionString(raw)
	if err != nil {
		return model.EventActionUnknown
	}
	return value
}

func optionalSlug(value pgtype.Text) *slug.Slug {
	if !value.Valid {
		return nil
	}
	parsed := slug.FromString(value.String)
	return &parsed
}
