package producer

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/reqctx"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

var errFakeTxQueryNotImplemented = xerror.Unexpected().Detail("fake tx query not implemented")

type testEvent struct {
	record eventrecorder.Record
}

func (e testEvent) EventRecord() (eventrecorder.Record, error) {
	return e.record, nil
}

func TestEventProducerEnqueuesRecord(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{
		queued: queue.QueuedJob{
			Kind:  jobs.EventRecordArgs{}.Kind(),
			Queue: jobs.EventsQueue,
			State: "queued",
		},
	}
	tx := &fakeTx{}
	producer, err := NewEventProducer(enqueuer, clock.New())
	require.NoError(t, err)

	actorSlug := slug.Slug("etl-worker")
	entitySlug := slug.Slug("daily-users-exec-001")
	occurredAt := time.Date(2026, time.May, 20, 10, 0, 0, 0, time.UTC)
	eventJob, ok := producer.EnqueueRecord(
		context.Background(),
		tx,
		slug.Slug("acme"),
		model.APIKeyEventActor(actorSlug),
		testEvent{record: eventrecorder.Record{
			SlugPrefix: slug.Slug("evt-query-execute"),
			EntityType: model.EventEntityTypeQueryExecution,
			EntitySlug: entitySlug,
			Action:     model.EventActionExecute,
			OccurredAt: occurredAt,
			Metadata: map[string]any{
				"query_slug": "daily-users",
			},
		}},
	)

	require.True(t, ok)
	require.Equal(t, enqueuer.queued, eventJob.Queued)
	require.NotEmpty(t, eventJob.EventSlug)
	require.Equal(t, 1, enqueuer.calls)
	require.Equal(t, tx, enqueuer.tx)
	require.Equal(t, jobs.EventsQueue, enqueuer.req.Queue)
	require.Equal(t, jobs.EventRecordArgs{
		OrgSlug:       slug.Slug("acme"),
		Slug:          enqueuer.req.Args.(jobs.EventRecordArgs).Slug,
		EventClass:    model.EventClassActivity,
		SchemaVersion: model.CurrentEventSchemaVersion,
		ActorType:     model.EventActorTypeAPIKey,
		ActorSlug:     &actorSlug,
		EntityType:    model.EventEntityTypeQueryExecution,
		EntitySlug:    &entitySlug,
		Action:        model.EventActionExecute,
		OccurredAt:    occurredAt,
		Metadata: map[string]any{
			"query_slug": "daily-users",
		},
	}, enqueuer.req.Args)
}

func TestEventProducerEnqueueRequiredValidatesAndCorrelatesSecurityAudit(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{
		queued: queue.QueuedJob{
			Kind:  jobs.EventRecordArgs{}.Kind(),
			Queue: jobs.EventsQueue,
			State: "queued",
		},
	}
	producer, err := NewEventProducer(enqueuer, clock.New())
	require.NoError(t, err)
	requestID := "req-security-123"
	actorSlug := slug.Slug("owner")
	entitySlug := slug.Slug("deploy-key")

	job, err := producer.EnqueueRequired(
		reqctx.WithRequestID(context.Background(), requestID),
		&fakeTx{},
		slug.Slug("acme"),
		model.UserEventActor(actorSlug),
		testEvent{record: eventrecorder.Record{
			SlugPrefix:    slug.Slug("evt-api-key-rotate"),
			EventClass:    model.EventClassSecurityAudit,
			EntityType:    model.EventEntityTypeAPIKey,
			EntitySlug:    entitySlug,
			Action:        model.EventActionRotate,
			SchemaVersion: model.CurrentEventSchemaVersion,
		}},
	)

	require.NoError(t, err)
	require.NotEmpty(t, job.EventSlug)
	args := enqueuer.req.Args.(jobs.EventRecordArgs)
	require.Equal(t, model.EventClassSecurityAudit, args.EventClass)
	require.NotNil(t, args.RequestID)
	require.Equal(t, requestID, *args.RequestID)
	require.Equal(t, model.CurrentEventSchemaVersion, args.SchemaVersion)
}

func TestEventProducerEnqueueRequiredRejectsUnsafeMetadataBeforeHandoff(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{}
	producer, err := NewEventProducer(enqueuer, clock.New())
	require.NoError(t, err)

	_, err = producer.EnqueueRequired(
		context.Background(),
		&fakeTx{},
		slug.Slug("acme"),
		model.UserEventActor(slug.Slug("owner")),
		testEvent{record: eventrecorder.Record{
			SlugPrefix:    slug.Slug("evt-api-key-rotate"),
			EventClass:    model.EventClassSecurityAudit,
			EntityType:    model.EventEntityTypeAPIKey,
			EntitySlug:    slug.Slug("deploy-key"),
			Action:        model.EventActionRotate,
			SchemaVersion: model.CurrentEventSchemaVersion,
			Metadata:      map[string]any{"token": "must-not-persist"},
		}},
	)

	require.ErrorContains(t, err, "security audit metadata contains unsafe key: token")
	require.Zero(t, enqueuer.calls)
}

func TestEventProducerStampsOccurredAtWhenRecordLeavesItUnset(t *testing.T) {
	t.Parallel()

	enqueuer := &recordingEnqueuer{
		queued: queue.QueuedJob{
			Kind:  jobs.EventRecordArgs{}.Kind(),
			Queue: jobs.EventsQueue,
			State: "queued",
		},
	}
	tx := &fakeTx{}
	now := time.Date(2026, time.May, 22, 15, 30, 0, 0, time.UTC)
	producer, err := NewEventProducer(enqueuer, fixedClock{at: now})
	require.NoError(t, err)

	_, ok := producer.EnqueueRecord(
		context.Background(),
		tx,
		slug.Slug("acme"),
		model.SystemEventActor(),
		testEvent{record: eventrecorder.Record{
			SlugPrefix: slug.Slug("evt-pipeline-create"),
			EntityType: model.EventEntityTypePipeline,
			EntitySlug: slug.Slug("daily-users"),
			Action:     model.EventActionCreate,
		}},
	)

	require.True(t, ok)
	args := enqueuer.req.Args.(jobs.EventRecordArgs)
	require.Equal(t, now, args.OccurredAt)
}

func TestEventProducerLogsAndReturnsFalseWhenEnqueueFails(t *testing.T) {
	t.Parallel()

	enqueueErr := xerror.Unexpected().Detail("queue unavailable")
	tx := &fakeTx{}
	producer, err := NewEventProducer(&recordingEnqueuer{err: enqueueErr}, clock.New())
	require.NoError(t, err)
	actorSlug := slug.Slug("etl-worker")

	eventJob, ok := producer.EnqueueRecord(
		context.Background(),
		tx,
		slug.Slug("acme"),
		model.APIKeyEventActor(actorSlug),
		testEvent{record: eventrecorder.Record{
			SlugPrefix: slug.Slug("evt-query-execute"),
			EntityType: model.EventEntityTypeQueryExecution,
			EntitySlug: slug.Slug("daily-users-exec-001"),
			Action:     model.EventActionExecute,
		}},
	)

	require.Empty(t, eventJob)
	require.False(t, ok)
}

func TestEventProducerEnqueueRequiredReturnsHandoffFailure(t *testing.T) {
	t.Parallel()

	enqueueErr := xerror.Unexpected().Detail("queue unavailable")
	producer, err := NewEventProducer(&recordingEnqueuer{err: enqueueErr}, clock.New())
	require.NoError(t, err)

	job, err := producer.EnqueueRequired(
		context.Background(),
		&fakeTx{},
		slug.Slug("acme"),
		model.UserEventActor(slug.Slug("owner")),
		testEvent{record: eventrecorder.Record{
			SlugPrefix:    slug.Slug("evt-role-update"),
			EventClass:    model.EventClassSecurityAudit,
			EntityType:    model.EventEntityTypeRole,
			EntitySlug:    slug.Slug("editor"),
			Action:        model.EventActionUpdate,
			SchemaVersion: model.CurrentEventSchemaVersion,
		}},
	)

	require.Empty(t, job)
	require.ErrorContains(t, err, "enqueue durable event")
	require.ErrorContains(t, err, "queue unavailable")
}

func TestNewEventProducerRequiresEnqueuer(t *testing.T) {
	t.Parallel()

	producer, err := NewEventProducer(nil, clock.New())

	require.Nil(t, producer)
	require.ErrorContains(t, err, "queue enqueuer is required")
}

func TestNewEventProducerRequiresClock(t *testing.T) {
	t.Parallel()

	producer, err := NewEventProducer(&recordingEnqueuer{}, nil)

	require.Nil(t, producer)
	require.ErrorContains(t, err, "clock is required")
}

type fixedClock struct {
	at time.Time
}

func (c fixedClock) Now() time.Time {
	return c.at
}

type fakeTx struct{}

func (*fakeTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (*fakeTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errFakeTxQueryNotImplemented
}

func (*fakeTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}
