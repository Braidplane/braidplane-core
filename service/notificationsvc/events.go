package notificationsvc

import (
	"context"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

type notificationLifecycleEvent struct {
	recordSlug       slug.Slug
	action           model.EventAction
	lifecycleState   model.NotificationLifecycleState
	notificationType model.NotificationType
	severity         model.NotificationSeverity
	occurredAt       time.Time
}

// EventRecord builds the durable audit event for one manual notification
// lifecycle action.
func (e notificationLifecycleEvent) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{
		SlugPrefix: "evt-notification-" + slug.Slug(e.action.String()),
		EntityType: model.EventEntityTypeNotification,
		EntitySlug: e.recordSlug,
		Action:     e.action,
		OccurredAt: e.occurredAt,
		Metadata: map[string]any{
			"lifecycle_state":   e.lifecycleState.String(),
			"notification_type": e.notificationType.String(),
			"severity":          e.severity.String(),
		},
	}, nil
}

type alertDestinationSecurityEvent struct {
	destinationSlug slug.Slug
	action          model.EventAction
	destinationType model.AlertDestinationType
	enabled         bool
}

func (e alertDestinationSecurityEvent) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{
		SlugPrefix:    "evt-alert-destination-" + slug.Slug(e.action.String()),
		EventClass:    model.EventClassSecurityAudit,
		SchemaVersion: model.CurrentEventSchemaVersion,
		EntityType:    model.EventEntityTypeAlertDestination,
		EntitySlug:    e.destinationSlug,
		Action:        e.action,
		Metadata: map[string]any{
			"type":    e.destinationType.String(),
			"enabled": e.enabled,
		},
	}, nil
}

type alertPolicySecurityEvent struct {
	policySlug       slug.Slug
	action           model.EventAction
	scope            model.AlertPolicyScope
	enabled          bool
	destinationCount int
}

func (e alertPolicySecurityEvent) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{
		SlugPrefix:    "evt-alert-policy-" + slug.Slug(e.action.String()),
		EventClass:    model.EventClassSecurityAudit,
		SchemaVersion: model.CurrentEventSchemaVersion,
		EntityType:    model.EventEntityTypeAlertPolicy,
		EntitySlug:    e.policySlug,
		Action:        e.action,
		Metadata: map[string]any{
			"scope":             e.scope.String(),
			"enabled":           e.enabled,
			"destination_count": e.destinationCount,
		},
	}, nil
}

func (s *service) enqueueRequiredAlertManagementAuditEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) error {
	if s.eventProducer == nil {
		return xerror.Unexpected().Detail("required alert management audit event producer is unavailable")
	}
	if err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, event); err != nil {
		return xerror.Wrapf("enqueue required alert management audit event: %w", err)
	}
	return nil
}
