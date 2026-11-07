package notificationsvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

// LifecycleEventProducer avoids an import cycle with service/worker/producer:
// notificationsvc needs only this enqueue capability, while worker producer
// already imports notificationsvc for notification delivery jobs. Broader
// package refactoring would be disproportionate for this narrow lifecycle-audit path.
type LifecycleEventProducer interface {
	EnqueueRecord(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		actor model.EventActor,
		event eventrecorder.Event,
	)
	EnqueueRequired(
		ctx context.Context,
		tx postgres.DBTX,
		orgSlug slug.Slug,
		actor model.EventActor,
		event eventrecorder.Event,
	) error
}

// LifecycleEventProducerFunc adapts a function to LifecycleEventProducer.
type LifecycleEventProducerFunc func(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
)

// EnqueueRecord calls the wrapped producer function.
func (f LifecycleEventProducerFunc) EnqueueRecord(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) {
	f(ctx, tx, orgSlug, actor, event)
}

// EnqueueRequired preserves the legacy function adapter for call sites that
// intentionally cannot fail. Production adapters must provide their own
// fail-closed implementation.
func (f LifecycleEventProducerFunc) EnqueueRequired(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) error {
	f(ctx, tx, orgSlug, actor, event)
	return nil
}
