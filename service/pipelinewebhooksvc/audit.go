package pipelinewebhooksvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres/pipelinewebhooks"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

type webhookKeyUse struct {
	receiver  pipelinewebhooks.Receiver
	operation slug.Slug
	version   int
}

func (e webhookKeyUse) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{
		SlugPrefix:    "evt-webhook-key-use",
		EventClass:    model.EventClassSecurityAudit,
		EntityType:    model.EventEntityTypePipelineTrigger,
		EntitySlug:    e.receiver.TriggerSlug,
		Action:        model.EventActionRead,
		SchemaVersion: model.CurrentEventSchemaVersion,
		Metadata: map[string]any{
			"operation":        "webhook_delivery",
			"operation_id":     e.operation.String(),
			"project_slug":     e.receiver.ProjectSlug.String(),
			"environment_slug": e.receiver.EnvironmentSlug.String(),
			"version":          e.version,
			"outcome":          "verified",
		},
	}, nil
}

func (s *Service) recordWebhookKeyUse(ctx context.Context, receiver pipelinewebhooks.Receiver, operation slug.Slug, version int) error {
	ctx = reqctx.WithRequestID(ctx, operation.String())
	if _, err := s.audit.Record(ctx, receiver.OrgSlug, model.ExternalEventActor(), webhookKeyUse{receiver, operation, version}); err != nil {
		return xerror.Wrapf("record webhook key use: %w", err)
	}
	return nil
}
