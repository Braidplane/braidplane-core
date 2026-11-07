package apikeyssvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

type apiKeyCreatedEvent struct {
	apiKey model.APIKey
}

func (e apiKeyCreatedEvent) EventRecord() (eventrecorder.Record, error) {
	return apiKeyRecord(
		"evt-api-key-create",
		e.apiKey.Slug,
		model.EventActionCreate,
		map[string]any{"role_slug": e.apiKey.Role.String()},
	), nil
}

type apiKeyUpdatedEvent struct {
	apiKeySlug slug.Slug
}

func (e apiKeyUpdatedEvent) EventRecord() (eventrecorder.Record, error) {
	return apiKeyRecord("evt-api-key-update", e.apiKeySlug, model.EventActionUpdate, nil), nil
}

type apiKeyRevokedEvent struct {
	apiKeySlug slug.Slug
}

func (e apiKeyRevokedEvent) EventRecord() (eventrecorder.Record, error) {
	return apiKeyRecord("evt-api-key-revoke", e.apiKeySlug, model.EventActionRevoke, nil), nil
}

type apiKeyDeletedEvent struct {
	apiKeySlug slug.Slug
}

// EventRecord builds a durable event for API key metadata deletion.
func (e apiKeyDeletedEvent) EventRecord() (eventrecorder.Record, error) {
	return apiKeyRecord("evt-api-key-delete", e.apiKeySlug, model.EventActionDelete, nil), nil
}

type apiKeyRotatedEvent struct {
	apiKeySlug slug.Slug
}

func (e apiKeyRotatedEvent) EventRecord() (eventrecorder.Record, error) {
	return apiKeyRecord("evt-api-key-rotate", e.apiKeySlug, model.EventActionRotate, nil), nil
}

func apiKeyRecord(
	slugPrefix slug.Slug,
	apiKeySlug slug.Slug,
	action model.EventAction,
	metadata map[string]any,
) eventrecorder.Record {
	return eventrecorder.Record{
		SlugPrefix:    slugPrefix,
		EventClass:    model.EventClassSecurityAudit,
		SchemaVersion: model.CurrentEventSchemaVersion,
		EntityType:    model.EventEntityTypeAPIKey,
		EntitySlug:    apiKeySlug,
		Action:        action,
		Metadata:      metadata,
	}
}

func (s *service) recordAPIKeyEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) error {
	if s.eventProducer == nil {
		return xerror.Unexpected().Detail("required API key audit event producer is unavailable")
	}
	_, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, event)
	if err != nil {
		return xerror.Wrapf("enqueue required API key audit event: %w", err)
	}
	return nil
}
