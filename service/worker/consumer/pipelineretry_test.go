package consumer

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/must"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestPipelineTriggerRetryExecutionWorker(t *testing.T) {
	t.Parallel()

	executionErr := xerror.Unexpected().Detail("retry attempt store unavailable")
	args := jobs.PipelineTriggerRetryExecutionArgs{
		OrganizationSlug: must.Slug("acme"),
		ProjectSlug:      must.Slug("default"),
		EnvironmentSlug:  must.Slug("prod"),
		RetryAttemptSlug: must.Slug("retry-fire-daily-users-20260609-2"),
		ScheduledAt:      time.Date(2026, time.June, 9, 7, 1, 0, 0, time.UTC),
	}
	tests := []struct {
		name    string
		err     error
		wantErr error
	}{
		{
			name: "SuccessfulDispatch",
		},
		{
			name:    "ExecutionErrorReturns",
			err:     executionErr,
			wantErr: executionErr,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dispatcher := &recordingRetryDispatcher{err: test.err}
			worker := NewPipelineTriggerRetryExecutionWorker(dispatcher, slog.New(slog.DiscardHandler))

			err := worker.Work(context.Background(), &river.Job[jobs.PipelineTriggerRetryExecutionArgs]{
				Args: args,
			})

			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, dispatcher.calls)
			require.Equal(t, args, dispatcher.lastArgs)
		})
	}
}

func TestPipelineTriggerRetryExecutionWorkerPanicsForNilService(t *testing.T) {
	t.Parallel()

	require.PanicsWithValue(
		t,
		"nil pipelineRetryDispatcher passed to NewPipelineTriggerRetryExecutionWorker",
		func() {
			NewPipelineTriggerRetryExecutionWorker(nil, slog.New(slog.DiscardHandler))
		},
	)
}

type recordingRetryDispatcher struct {
	calls    int
	lastArgs jobs.PipelineTriggerRetryExecutionArgs
	err      error
}

func (d *recordingRetryDispatcher) ExecuteTriggerRetryAttempt(
	_ context.Context,
	args jobs.PipelineTriggerRetryExecutionArgs,
) error {
	d.calls++
	d.lastArgs = args
	return d.err
}
