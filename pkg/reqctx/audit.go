package reqctx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

type (
	auditOperationKey struct{}
	auditActorKey     struct{}
)

// WithAuditOperation binds credential releases to a server-owned request or job identity.
func WithAuditOperation(ctx context.Context, id string) context.Context {
	digest := sha256.Sum256([]byte(id))
	return context.WithValue(ctx, auditOperationKey{}, hex.EncodeToString(digest[:]))
}

func AuditOperation(ctx context.Context) string {
	id, _ := ctx.Value(auditOperationKey{}).(string)
	return id
}

func WithAuditActor(ctx context.Context, actor model.EventActor) context.Context {
	return context.WithValue(ctx, auditActorKey{}, actor)
}

func AuditActor(ctx context.Context) model.EventActor {
	actor, _ := ctx.Value(auditActorKey{}).(model.EventActor)
	return actor
}
