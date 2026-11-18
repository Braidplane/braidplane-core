package credentialssvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

type credentialAuditEvent struct {
	owner       CredentialOwner
	action      model.EventAction
	version     string
	outcome     string
	operation   string
	operationID string
}

func (e credentialAuditEvent) EventRecord() (eventrecorder.Record, error) {
	var entity model.EventEntityType
	var resource slug.Slug
	switch e.owner.Kind {
	case CredentialOwnerKindDataSource:
		entity, resource = model.EventEntityTypeDataSource, e.owner.DataSourceSlug
	case CredentialOwnerKindEnvironmentSecret:
		entity, resource = model.EventEntityTypeEnvironment, e.owner.EnvironmentSlug
	case CredentialOwnerKindOrganizationGitLab:
		entity, resource = model.EventEntityTypeOrganization, e.owner.OrgSlug
	case CredentialOwnerKindNotificationConnection:
		entity, resource = model.EventEntityTypeNotification, e.owner.NotificationConnectionSlug
	default:
		return eventrecorder.Record{}, ErrInvalidInput
	}
	metadata := map[string]any{"owner_kind": e.owner.Kind.String(), "version": e.version, "outcome": e.outcome}
	var eventSlug slug.Slug
	if e.operationID != "" {
		metadata["operation"] = e.operation
		metadata["operation_id"] = e.operationID
		identity := strings.Join([]string{e.operation, e.operationID, entity.String(), resource.String(), e.version, e.outcome}, "\x00")
		eventSlug = slug.Slug("evt-credential-release-" + auditRevision(identity))
	}
	return eventrecorder.Record{
		Slug:          eventSlug,
		SlugPrefix:    "evt-credential-lifecycle",
		EventClass:    model.EventClassSecurityAudit,
		EntityType:    entity,
		EntitySlug:    resource,
		Action:        e.action,
		SchemaVersion: model.CurrentEventSchemaVersion,
		Metadata:      metadata,
	}, nil
}

func (s *service) recordCredentialLifecycle(ctx context.Context, owner CredentialOwner, actor model.EventActor, action model.EventAction, metadata CredentialMetadata, outcome string) error {
	revision := AuditCredentialVersion(metadata.Slug, metadata.UpdatedAt)
	ctx = reqctx.WithRequestID(ctx, runtimeAuditOperation(ctx))
	_, err := s.audit.Record(ctx, owner.OrgSlug, normalizeEventActor(actor), credentialAuditEvent{
		owner: owner, action: action, version: revision, outcome: outcome,
	})
	if err != nil {
		return xerror.Wrapf("record credential lifecycle audit: %w", err)
	}
	return nil
}

func (s *service) recordOAuthLifecycle(ctx context.Context, install CredentialInstall, actor model.EventActor, metadata CredentialMetadata, outcome string) error {
	action := model.EventActionCreate
	if install.Intent == OAuthInstallIntentRotate {
		action = model.EventActionRotate
	}
	return s.recordCredentialLifecycle(ctx, install.Owner, actor, action, metadata, outcome)
}

func (s *service) recordCredentialRelease(ctx context.Context, owner CredentialOwner, actor model.EventActor, operation, operationID, version, outcome string) error {
	if version != "" {
		version = auditRevision(version)
	}
	// An already committed action for this operation, resource, revision and outcome satisfies a retry.
	ctx = reqctx.WithRequestID(postgres.WithoutTransaction(ctx), operationID)
	_, err := s.audit.Record(ctx, owner.OrgSlug, normalizeEventActor(actor), credentialAuditEvent{
		owner: owner, action: model.EventActionRead, version: version, outcome: outcome, operation: operation, operationID: operationID,
	})
	if err != nil && !xerror.Is(err, eventssvc.ErrSlugTaken) {
		return ErrCredentialAuditUnavailable
	}
	return nil
}

func auditRevision(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func runtimeAuditOperation(ctx context.Context) string {
	if id := reqctx.AuditOperation(ctx); id != "" {
		return id
	}
	return uuid.New().String()
}

// AuditCredentialVersion identifies a stored revision without exposing its internal slug.
func AuditCredentialVersion(credential slug.Slug, updatedAt time.Time) string {
	// Environment credential slugs contain the environment variable name.
	return auditRevision(runtimeCredentialVersion(credential, updatedAt))
}
