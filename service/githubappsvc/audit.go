package githubappsvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/uuid"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

type githubAppTokenUse struct {
	organization                       slug.Slug
	operation, version, scope, outcome string
}

func (e githubAppTokenUse) EventRecord() (eventrecorder.Record, error) {
	digest := sha256.Sum256([]byte(e.operation + "/" + e.version + "/" + e.scope + "/" + e.outcome))
	return eventrecorder.Record{
		Slug:          slug.Slug("evt-github-app-release-" + hex.EncodeToString(digest[:])),
		EventClass:    model.EventClassSecurityAudit,
		EntityType:    model.EventEntityTypeOrganization,
		EntitySlug:    e.organization,
		Action:        model.EventActionRead,
		SchemaVersion: model.CurrentEventSchemaVersion,
		Metadata: map[string]any{
			"operation":        "github_app",
			"operation_id":     e.operation,
			"version":          e.version,
			"repository_scope": e.scope,
			"outcome":          e.outcome,
		},
	}, nil
}

func (s *Service) recordTokenUse(ctx context.Context, connection *Connection, repositoryIDs []int64, outcome string) error {
	operation := githubAppAuditOperation(ctx)

	actor := reqctx.AuditActor(ctx)
	if actor.Type == model.EventActorTypeUnknown {
		actor = model.SystemEventActor()
	}
	ctx = reqctx.WithRequestID(postgres.WithoutTransaction(ctx), operation)
	_, err := s.audit.Record(ctx, connection.Organization, actor, githubAppTokenUse{
		organization: connection.Organization, operation: operation, version: connection.auditVersion(),
		scope: digestRepositoryIDs(nil, repositoryIDs), outcome: outcome,
	})
	if err != nil && !xerror.Is(err, eventssvc.ErrSlugTaken) {
		return credentialssvc.ErrCredentialAuditUnavailable
	}
	return nil
}

func (c *Connection) auditVersion() string {
	revision := strconv.AppendInt(nil, c.InstallationID, 10)
	revision = c.updatedAt.UTC().AppendFormat(append(revision, '/'), time.RFC3339Nano)
	ids := make([]int64, 0, len(c.Repositories))
	for _, binding := range c.Repositories {
		ids = append(ids, binding.ID)
	}
	return digestRepositoryIDs(revision, ids)
}

func digestRepositoryIDs(prefix []byte, repositoryIDs []int64) string {
	ids := slices.Clone(repositoryIDs)
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		prefix = strconv.AppendInt(append(prefix, '/'), id, 10)
	}
	digest := sha256.Sum256(prefix)
	return hex.EncodeToString(digest[:])
}

type githubAppConnectionChangedEvent struct {
	organization    slug.Slug
	change, version string
}

func (e githubAppConnectionChangedEvent) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{
		SlugPrefix: "evt-github-app-connection-update", EventClass: model.EventClassSecurityAudit,
		SchemaVersion: model.CurrentEventSchemaVersion, EntityType: model.EventEntityTypeOrganization,
		EntitySlug: e.organization, Action: model.EventActionUpdate,
		Metadata: map[string]any{"change": e.change, "version": e.version},
	}, nil
}

func (s *Service) recordConnectionChange(ctx context.Context, tx postgres.DBTX, org slug.Slug, actor model.EventActor, change string) error {
	stored, err := s.Get(ctx, org)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	if stored == nil {
		return xerror.Unexpected().Detail("GitHub App connection revision is unavailable")
	}
	ctx = reqctx.WithRequestID(ctx, githubAppAuditOperation(ctx))
	_, err = s.events.EnqueueRequired(ctx, tx, org, actor, githubAppConnectionChangedEvent{org, change, stored.auditVersion()})
	return err
}

func githubAppAuditOperation(ctx context.Context) string {
	if operation := reqctx.AuditOperation(ctx); operation != "" {
		return operation
	}
	return uuid.New().String()
}
