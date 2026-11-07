package eventssvc

import (
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	repoevents "github.com/Braidplane/braidplane-core/postgres/events"
)

func toEvent(event *repoevents.Event) (*model.Event, error) {
	if event == nil {
		return nil, xerror.Unexpected().Detail("event is required")
	}

	metadata := map[string]any{}
	if len(event.Metadata) > 0 {
		if err := json.Unmarshal(event.Metadata, &metadata); err != nil {
			return nil, xerror.Wrapf("decode event metadata: %w", ErrInvalidInput.With(err))
		}
	}

	return &model.Event{
		Slug:          event.Slug,
		EventClass:    event.EventClass,
		ActorType:     event.ActorType,
		ActorSlug:     event.ActorSlug,
		EntityType:    event.EntityType,
		EntitySlug:    event.EntitySlug,
		Action:        event.Action,
		OccurredAt:    event.OccurredAt,
		RequestID:     event.RequestID,
		SchemaVersion: event.SchemaVersion,
		Metadata:      metadata,
		CreatedAt:     event.CreatedAt,
	}, nil
}
