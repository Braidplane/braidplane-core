package rolessvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

type roleCreatedEvent struct {
	role            model.OrgRole
	permissionCount int
}

func (e roleCreatedEvent) EventRecord() (eventrecorder.Record, error) {
	return roleRecord(
		"evt-role-create",
		e.role.Slug,
		model.EventActionCreate,
		map[string]any{"permission_count": e.permissionCount},
	), nil
}

type roleUpdatedEvent struct {
	roleSlug        slug.Slug
	permissionCount int
}

func (e roleUpdatedEvent) EventRecord() (eventrecorder.Record, error) {
	return roleRecord(
		"evt-role-update",
		e.roleSlug,
		model.EventActionUpdate,
		map[string]any{"permission_count": e.permissionCount},
	), nil
}

type roleDeletedEvent struct {
	roleSlug slug.Slug
}

func (e roleDeletedEvent) EventRecord() (eventrecorder.Record, error) {
	return roleRecord("evt-role-delete", e.roleSlug, model.EventActionDelete, nil), nil
}

type roleRestoredEvent struct {
	roleSlug slug.Slug
}

func (e roleRestoredEvent) EventRecord() (eventrecorder.Record, error) {
	return roleRecord("evt-role-restore", e.roleSlug, model.EventActionRestore, nil), nil
}

func roleRecord(
	slugPrefix slug.Slug,
	roleSlug slug.Slug,
	action model.EventAction,
	metadata map[string]any,
) eventrecorder.Record {
	return eventrecorder.Record{
		SlugPrefix:    slugPrefix,
		EventClass:    model.EventClassSecurityAudit,
		SchemaVersion: model.CurrentEventSchemaVersion,
		EntityType:    model.EventEntityTypeRole,
		EntitySlug:    roleSlug,
		Action:        action,
		Metadata:      metadata,
	}
}

func (s *service) enqueueRequiredRoleAuditEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) error {
	if s.eventProducer == nil {
		return xerror.Unexpected().Detail("required role audit event producer is unavailable")
	}
	if _, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, event); err != nil {
		return xerror.Wrapf("enqueue required role audit event: %w", err)
	}
	return nil
}
