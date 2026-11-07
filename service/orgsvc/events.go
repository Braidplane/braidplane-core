package orgsvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

type organizationCreatedEvent struct {
	orgSlug slug.Slug
}

func (e organizationCreatedEvent) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{
		SlugPrefix:    "evt-organization-create",
		EventClass:    model.EventClassSecurityAudit,
		SchemaVersion: model.CurrentEventSchemaVersion,
		EntityType:    model.EventEntityTypeOrganization,
		EntitySlug:    e.orgSlug,
		Action:        model.EventActionCreate,
	}, nil
}

type organizationUpdatedEvent struct {
	orgSlug slug.Slug
}

func (e organizationUpdatedEvent) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{
		SlugPrefix:    "evt-organization-update",
		EventClass:    model.EventClassSecurityAudit,
		SchemaVersion: model.CurrentEventSchemaVersion,
		EntityType:    model.EventEntityTypeOrganization,
		EntitySlug:    e.orgSlug,
		Action:        model.EventActionUpdate,
	}, nil
}

type organizationRetentionUpdatedEvent struct {
	orgSlug slug.Slug
	policy  model.OrganizationRetentionPolicy
}

func (e organizationRetentionUpdatedEvent) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{
		SlugPrefix:    "evt-organization-retention-update",
		EventClass:    model.EventClassSecurityAudit,
		SchemaVersion: model.CurrentEventSchemaVersion,
		EntityType:    model.EventEntityTypeOrganization,
		EntitySlug:    e.orgSlug,
		Action:        model.EventActionUpdate,
		Metadata: map[string]any{
			"query_execution_retention_days":   e.policy.QueryExecutionRetentionDays,
			"replay_checkpoint_retention_days": e.policy.ReplayCheckpointRetentionDays,
			"run_history_retention_days":       e.policy.RunHistoryRetentionDays,
			"event_log_retention_days":         e.policy.EventLogRetentionDays,
		},
	}, nil
}

func (s *service) enqueueRequiredOrganizationAuditEvent(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) error {
	if s.eventProducer == nil {
		return xerror.Unexpected().Detail("required organization audit event producer is unavailable")
	}
	if _, err := s.eventProducer.EnqueueRequired(ctx, tx, orgSlug, actor, event); err != nil {
		return xerror.Wrapf("enqueue required organization audit event: %w", err)
	}
	return nil
}
