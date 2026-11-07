package consumer

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/retentionsvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestEventPruneWorkerPrunesCustomerData(t *testing.T) {
	t.Parallel()

	called := false
	startedAt := time.Date(2026, time.July, 23, 0, 0, 1, 0, time.UTC)
	worker := NewEventPruneWorker(
		eventPruneFunc(func(_ context.Context, execution retentionsvc.MaintenanceExecution) (retentionsvc.Result, error) {
			called = true
			require.Equal(t, retentionsvc.MaintenanceExecution{
				SourceJobID: 71,
				Attempt:     2,
				StartedAt:   startedAt,
			}, execution)
			return retentionsvc.Result{Events: 4}, nil
		}),
		slog.New(slog.DiscardHandler),
	)

	err := worker.Work(context.Background(), eventPruneJob(71, 2, startedAt))
	require.NoError(t, err)
	require.True(t, called)
}

func TestEventPruneWorkerReturnsPruneErrorForRetry(t *testing.T) {
	t.Parallel()

	pruneErr := xerror.Unexpected().Detail("event store unavailable")
	worker := NewEventPruneWorker(
		eventPruneFunc(func(context.Context, retentionsvc.MaintenanceExecution) (retentionsvc.Result, error) {
			return retentionsvc.Result{}, pruneErr
		}),
		slog.New(slog.DiscardHandler),
	)

	err := worker.Work(context.Background(), eventPruneJob(72, 1, time.Now().UTC()))
	require.ErrorIs(t, err, pruneErr)
}

func TestEventPruneWorkerRejectsMissingPrivateJobMetadata(t *testing.T) {
	t.Parallel()

	worker := NewEventPruneWorker(
		eventPruneFunc(func(context.Context, retentionsvc.MaintenanceExecution) (retentionsvc.Result, error) {
			t.Fatal("service should not be called")
			return retentionsvc.Result{}, nil
		}),
		slog.New(slog.DiscardHandler),
	)

	err := worker.Work(t.Context(), &river.Job[jobs.EventPruneArgs]{Args: jobs.EventPruneArgs{}})

	require.ErrorContains(t, err, "retention worker job metadata is invalid")
}

type eventPruneFunc func(context.Context, retentionsvc.MaintenanceExecution) (retentionsvc.Result, error)

func (f eventPruneFunc) PruneExpiredCustomerData(
	ctx context.Context,
	execution retentionsvc.MaintenanceExecution,
) (retentionsvc.Result, error) {
	return f(ctx, execution)
}

func eventPruneJob(id int64, attempt int, attemptedAt time.Time) *river.Job[jobs.EventPruneArgs] {
	return &river.Job[jobs.EventPruneArgs]{
		JobRow: &rivertype.JobRow{
			ID:          id,
			Attempt:     attempt,
			AttemptedAt: &attemptedAt,
		},
		Args: jobs.EventPruneArgs{},
	}
}
