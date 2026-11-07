package consumer

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestEventRecordWorkerPersistsDurableEvent(t *testing.T) {
	t.Parallel()

	events := &recordingEventWorkerService{}
	worker := NewEventRecordWorker(events, slog.New(slog.DiscardHandler))
	actorSlug := slug.Slug("etl-worker")
	entitySlug := slug.Slug("daily-users-exec-001")
	occurredAt := time.Date(2026, time.May, 20, 10, 0, 0, 0, time.UTC)
	requestID := "req-event-worker"

	err := worker.Work(context.Background(), &river.Job[jobs.EventRecordArgs]{
		Args: jobs.EventRecordArgs{
			OrgSlug:       slug.Slug("acme"),
			Slug:          slug.Slug("evt-query-execute-001"),
			EventClass:    model.EventClassSecurityAudit,
			RequestID:     &requestID,
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
		},
	})

	require.NoError(t, err)
	require.Len(t, events.calls, 1)
	require.Equal(t, eventssvc.CreateEventParams{
		OrgSlug:       slug.Slug("acme"),
		Slug:          slug.Slug("evt-query-execute-001"),
		EventClass:    model.EventClassSecurityAudit,
		RequestID:     &requestID,
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
	}, events.calls[0])
}

func TestEventRecordWorkerReturnsEventPersistenceErrorForRetry(t *testing.T) {
	t.Parallel()

	recordErr := xerror.Unexpected().Detail("event store unavailable")
	worker := NewEventRecordWorker(
		&recordingEventWorkerService{err: recordErr},
		slog.New(slog.DiscardHandler),
	)

	err := worker.Work(context.Background(), &river.Job[jobs.EventRecordArgs]{
		Args: jobs.EventRecordArgs{
			OrgSlug:    slug.Slug("acme"),
			Slug:       slug.Slug("evt-query-execute-001"),
			ActorType:  model.EventActorTypeSystem,
			EntityType: model.EventEntityTypeQueryExecution,
			Action:     model.EventActionExecute,
		},
	})

	require.ErrorIs(t, err, recordErr)
}

func TestEventRecordWorkerTreatsDuplicateEventSlugAsSuccess(t *testing.T) {
	t.Parallel()

	worker := NewEventRecordWorker(
		&recordingEventWorkerService{err: eventssvc.ErrSlugTaken},
		slog.New(slog.DiscardHandler),
	)

	err := worker.Work(context.Background(), &river.Job[jobs.EventRecordArgs]{
		Args: jobs.EventRecordArgs{
			OrgSlug:    slug.Slug("acme"),
			Slug:       slug.Slug("evt-query-execute-001"),
			ActorType:  model.EventActorTypeSystem,
			EntityType: model.EventEntityTypeQueryExecution,
			Action:     model.EventActionExecute,
		},
	})

	require.NoError(t, err)
}

type recordingEventWorkerService struct {
	calls []eventssvc.CreateEventParams
	err   error
}

func (s *recordingEventWorkerService) CreateEvent(
	_ context.Context,
	params eventssvc.CreateEventParams,
) (*model.Event, error) {
	s.calls = append(s.calls, params)
	if s.err != nil {
		return nil, s.err
	}
	return &model.Event{Slug: params.Slug}, nil
}
