package eventssvc

import (
	"math"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/validate"
)

// validateCreateEventInput validates event enum contracts and actor/entity slug requirements.
func validateCreateEventInput(params CreateEventParams) error {
	if err := validate.Default().Struct(params); err != nil {
		return ErrInvalidInput.With(err)
	}

	if actorTypeRequiresSlug(params.ActorType) {
		if params.ActorSlug == nil || *params.ActorSlug == "" {
			return ErrInvalidInput.Detail("actor_slug is required")
		}
	}
	if params.EntitySlug == nil || *params.EntitySlug == "" {
		return ErrInvalidInput.Detail("entity_slug is required")
	}
	if params.EventClass != model.EventClassActivity &&
		params.EventClass != model.EventClassSecurityAudit {
		return ErrInvalidInput.Detail("event_class is invalid")
	}
	if params.SchemaVersion < 1 || params.SchemaVersion > math.MaxInt32 {
		return ErrInvalidInput.Detail("schema_version must fit a positive PostgreSQL integer")
	}
	if params.RequestID != nil && len(*params.RequestID) > 255 {
		return ErrInvalidInput.Detail("request_id must not exceed 255 bytes")
	}

	return nil
}

func validateListEventsFilters(filters ListEventsFilters) error {
	if filters.EventClass != nil &&
		*filters.EventClass != model.EventClassActivity &&
		*filters.EventClass != model.EventClassSecurityAudit {
		return ErrInvalidInput.Detail("event_class filter is invalid")
	}
	if filters.RequestID != nil {
		if len(*filters.RequestID) == 0 {
			return ErrInvalidInput.Detail("request_id filter must not be empty")
		}
		if len(*filters.RequestID) > 255 {
			return ErrInvalidInput.Detail("request_id filter must not exceed 255 bytes")
		}
	}
	if filters.OccurredFrom != nil &&
		filters.OccurredBefore != nil &&
		!filters.OccurredFrom.Before(*filters.OccurredBefore) {
		return ErrInvalidInput.Detail("occurred_from must be before occurred_before")
	}
	return nil
}

// actorTypeRequiresSlug reports whether an actor type must carry a durable actor slug.
func actorTypeRequiresSlug(actorType model.EventActorType) bool {
	switch actorType {
	case model.EventActorTypeUser,
		model.EventActorTypeAPIKey:
		return true
	default:
		return false
	}
}
