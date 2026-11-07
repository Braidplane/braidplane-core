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
	"github.com/Braidplane/braidplane-core/service/pipelineschedulesvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestPipelineScheduleWorkers(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.June, 9, 12, 0, 0, 0, time.UTC)
	dispatchErr := xerror.Unexpected().Detail("schedule store unavailable")
	executionErr := xerror.Unexpected().Detail("pipeline runtime unavailable")
	args := jobs.PipelineScheduleFireExecutionArgs{
		OrganizationSlug: must.Slug("acme"),
		ProjectSlug:      must.Slug("default"),
		EnvironmentSlug:  must.Slug("prod"),
		TriggerSlug:      must.Slug("daily-users"),
		TriggerRevision:  7,
		ScheduledFireAt:  time.Date(2026, time.June, 9, 7, 0, 0, 0, time.UTC),
	}

	tests := []struct {
		name         string
		workerType   string
		now          time.Time
		args         jobs.PipelineScheduleFireExecutionArgs
		dispatchErr  error
		executionErr error
		wantErr      error
	}{
		{
			name:       "DispatchSuccess",
			workerType: "dispatch",
			now:        now,
		},
		{
			name:        "DispatchError",
			workerType:  "dispatch",
			now:         now,
			dispatchErr: dispatchErr,
			wantErr:     dispatchErr,
		},
		{
			name:       "FireSuccess",
			workerType: "fire",
			args:       args,
		},
		{
			name:         "FireError",
			workerType:   "fire",
			args:         args,
			executionErr: executionErr,
			wantErr:      executionErr,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			scheduler := &recordingScheduleDispatcher{
				err:          test.dispatchErr,
				executionErr: test.executionErr,
			}
			var err error
			switch test.workerType {
			case "dispatch":
				worker := NewPipelineScheduleDispatchWorker(
					scheduler,
					fixedScheduleDispatchClock{now: test.now},
					slog.New(slog.DiscardHandler),
				)
				err = worker.Work(context.Background(), &river.Job[jobs.PipelineScheduleDispatchArgs]{
					Args: jobs.PipelineScheduleDispatchArgs{},
				})
			case "fire":
				worker := NewPipelineScheduleFireExecutionWorker(scheduler, slog.New(slog.DiscardHandler))
				err = worker.Work(context.Background(), &river.Job[jobs.PipelineScheduleFireExecutionArgs]{
					Args: test.args,
				})
			default:
				t.Fatalf("unknown worker type %q", test.workerType)
			}

			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
			}

			if test.workerType == "dispatch" {
				require.Equal(t, 1, scheduler.calls)
				require.True(t, scheduler.lastNow.Equal(test.now))
				return
			}
			require.Equal(t, 1, scheduler.executionCalls)
			require.Equal(t, test.args, scheduler.lastExecutionArgs)
		})
	}
}

func TestPipelineScheduleDispatchWorkerPanicsForNilService(t *testing.T) {
	t.Parallel()

	require.PanicsWithValue(
		t,
		"nil scheduleDispatchService passed to NewPipelineScheduleDispatchWorker",
		func() {
			NewPipelineScheduleDispatchWorker(nil, fixedScheduleDispatchClock{}, slog.New(slog.DiscardHandler))
		},
	)
}

func TestPipelineScheduleFireExecutionWorkerPanicsForNilService(t *testing.T) {
	t.Parallel()

	require.PanicsWithValue(
		t,
		"nil scheduleFireExecutionService passed to NewPipelineScheduleFireExecutionWorker",
		func() {
			NewPipelineScheduleFireExecutionWorker(nil, slog.New(slog.DiscardHandler))
		},
	)
}

type recordingScheduleDispatcher struct {
	calls             int
	lastNow           time.Time
	err               error
	executionCalls    int
	lastExecutionArgs jobs.PipelineScheduleFireExecutionArgs
	executionErr      error
}

func (d *recordingScheduleDispatcher) DispatchDueSchedules(
	_ context.Context,
	now time.Time,
) (pipelineschedulesvc.DispatchResult, error) {
	d.calls++
	d.lastNow = now
	return pipelineschedulesvc.DispatchResult{}, d.err
}

func (d *recordingScheduleDispatcher) ExecuteClaimedScheduleFire(
	_ context.Context,
	args jobs.PipelineScheduleFireExecutionArgs,
) error {
	d.executionCalls++
	d.lastExecutionArgs = args
	return d.executionErr
}

type fixedScheduleDispatchClock struct {
	now time.Time
}

func (c fixedScheduleDispatchClock) Now() time.Time {
	return c.now
}
