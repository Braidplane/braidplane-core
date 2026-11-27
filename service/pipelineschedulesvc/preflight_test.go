package pipelineschedulesvc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestDispatchSchedulePreflightsUsesOccurrenceLeadTime(t *testing.T) {
	t.Parallel()

	location, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	for _, lead := range []time.Duration{time.Hour, 6 * time.Hour} {
		t.Run(fmt.Sprint(lead), func(t *testing.T) {
			fire := time.Date(2026, time.June, 9, 9, 0, 0, 0, location)
			previous := fire.AddDate(0, 0, -1)
			trigger := newDueScheduleTrigger("acme", "prod", "daily-users", "sync-users", true, "0 9 * * *", "Europe/Warsaw", &previous)
			trigger.ScheduleConfig.PreflightLeadTime = lead
			repo := &recordingScheduleRepository{
				triggers:        []DueScheduleTrigger{trigger},
				preflightClaims: map[ScheduleFireExecutionRef]time.Time{},
			}
			producer := &recordingScheduleFireProducer{}
			svc := New(testutil.FakeTransactionManager{}, repo, &recordingPipelineExecutor{}, producer, nil)

			due := fire.Add(-lead)
			before, err := svc.DispatchDueSchedules(t.Context(), due.Add(-time.Second))
			require.NoError(t, err)
			require.Zero(t, before.PreflightsEnqueued)

			at, err := svc.DispatchDueSchedules(t.Context(), due)
			require.NoError(t, err)
			require.Equal(t, 1, at.PreflightsEnqueued)

			retry, err := svc.DispatchDueSchedules(t.Context(), due)
			require.NoError(t, err)
			require.Zero(t, retry.PreflightsEnqueued)
			require.Len(t, producer.preflights, 1)
			require.Empty(t, producer.calls)
			require.Equal(t, fire.UTC(), producer.preflights[0].ScheduledFireAt)
			for _, claimedAt := range repo.preflightClaims {
				require.Equal(t, due.UTC(), claimedAt)
			}
		})
	}
}

func (r *recordingScheduleRepository) ClaimSchedulePreflight(_ context.Context, _ postgres.DBTX, ref ScheduleFireExecutionRef, due time.Time) (bool, error) {
	if _, exists := r.preflightClaims[ref]; exists {
		return false, nil
	}
	r.preflightClaims[ref] = due
	return true, nil
}

func (p *recordingScheduleFireProducer) EnqueueSchedulePreflight(_ context.Context, _ postgres.DBTX, args jobs.PipelineSchedulePreflightArgs) (queue.QueuedJob, error) {
	p.preflights = append(p.preflights, args)
	return queue.QueuedJob{}, nil
}
