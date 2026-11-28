package executionbroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
	"github.com/Braidplane/braidplane-core/service/proofevidencesvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
)

type credentialHandoffEvent struct {
	resource        slug.Slug
	entity          model.EventEntityType
	requestID       uuid.UUID
	kind            string
	version         string
	parentOperation string
}

func (e credentialHandoffEvent) EventRecord() (eventrecorder.Record, error) {
	metadata := map[string]any{
		"operation": e.kind, "operation_id": e.requestID.String(),
		"outcome": "enveloped", "version": e.version,
	}
	if e.parentOperation != "" {
		metadata["parent_operation_id"] = e.parentOperation
	}
	return eventrecorder.Record{
		SlugPrefix:    "evt-credential-broker-handoff",
		EventClass:    model.EventClassSecurityAudit,
		EntityType:    e.entity,
		EntitySlug:    e.resource,
		Action:        model.EventActionRead,
		SchemaVersion: model.CurrentEventSchemaVersion,
		Metadata:      metadata,
	}, nil
}

func (b *Broker) recordCredentialHandoff(ctx context.Context, org slug.Slug, requestID uuid.UUID, kind string, payload any) error {
	var source slug.Slug
	var reference string
	unversioned := false
	switch request := payload.(type) {
	case queriessvc.ConnectorExecutionRequest:
		source, reference = request.DataSourceSlug, request.CredentialReference
		if reference == "" {
			reference = request.CredentialVersion
		}
	case FTPArtifactRequest:
		source, reference = request.DataSourceSlug, request.CredentialReference
	case proofevidencesvc.ExecutionRequest:
		source, reference = request.Request.CredentialDataSourceSlug, request.Request.CredentialVersion
	case datasourcessvc.ConnectionTestRequest:
		source = request.DataSourceSlug
		unversioned = len(request.CredentialsJSON) != 0
	case datasourcessvc.SchemaIntrospectionRequest:
		source, reference = request.DataSourceSlug, request.CredentialReference
	case datasourcessvc.GitBranchRequest:
		source, reference = gitAuditReference(request.Auth)
	case pipelinegit.RemoteWriteRequest:
		source, reference = gitAuditReference(request.Auth)
	case pipelinegit.RemoteDeleteRequest:
		source, reference = gitAuditReference(request.Auth)
	case pipelinegit.RemoteProposalRequest:
		source, reference = gitAuditReference(request.Auth)
	case pipelinegit.RemoteProposalRefreshRequest:
		source, reference = gitAuditReference(request.Auth)
	case pipelinegit.RemoteRepositoryRequest:
		source, reference = gitAuditReference(request.Auth)
	case pipelinegit.RemoteSourceRequest:
		source, reference = gitAuditReference(request.Auth)
		if source == "" {
			source = request.DataSourceSlug
		}
	}
	if reference == "" && !unversioned {
		return nil
	}
	entity, resource := model.EventEntityTypeDataSource, source
	if resource == "" {
		entity, resource = model.EventEntityTypeOrganization, org
	}
	if b.audit == nil {
		return credentialssvc.ErrCredentialAuditUnavailable
	}
	actor := reqctx.AuditActor(ctx)
	if actor.Type == model.EventActorTypeUnknown {
		actor = model.SystemEventActor()
	}
	version := ""
	if reference != "" {
		digest := sha256.Sum256([]byte(reference))
		version = hex.EncodeToString(digest[:])
	}
	ctx = reqctx.WithRequestID(ctx, requestID.String())
	_, err := b.audit.Record(ctx, org, actor, credentialHandoffEvent{
		entity: entity, resource: resource, requestID: requestID, kind: kind, version: version,
		parentOperation: reqctx.AuditOperation(ctx),
	})
	if err != nil {
		return credentialssvc.ErrCredentialAuditUnavailable
	}
	return nil
}

func gitAuditReference(auth datasourcessvc.GitExecutionAuth) (slug.Slug, string) {
	return slug.Slug(auth.GitDataSourceSlug), auth.CredentialReference
}
