package reqctx

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
)

type auditOperationKey struct{}
type auditActorKey struct{}

// WithAuditOperation binds credential releases to a server-owned request or job identity.
func WithAuditOperation(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, auditOperationKey{}, id)
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
