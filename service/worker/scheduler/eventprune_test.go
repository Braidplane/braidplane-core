package scheduler

import (
	"reflect"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/service/worker/jobs"
)

func TestPeriodicJobsIncludesEventPruneJob(t *testing.T) {
	t.Parallel()

	periodicJobs, err := PeriodicJobs()
	require.NoError(t, err)
	require.Len(t, periodicJobs, 2)

	jobIDs := make([]string, 0, len(periodicJobs))
	for _, periodicJob := range periodicJobs {
		jobIDs = append(jobIDs, periodicJobID(t, periodicJob))
	}
	require.Contains(t, jobIDs, jobs.EventPruneArgs{}.Kind())
}

func TestEventPruneScheduleUsesUTCMidnight(t *testing.T) {
	t.Parallel()

	schedule, err := eventPruneSchedule()
	require.NoError(t, err)
	now := time.Date(2026, time.July, 22, 23, 30, 0, 0, time.FixedZone("CEST", 2*60*60))

	require.Equal(t, time.Date(2026, time.July, 23, 0, 0, 0, 0, time.UTC), schedule.Next(now).UTC())
}

func periodicJobID(t *testing.T, job *river.PeriodicJob) string {
	t.Helper()

	opts := reflect.ValueOf(job).Elem().FieldByName("opts")
	require.False(t, opts.IsNil())
	return opts.Elem().FieldByName("ID").String()
}
