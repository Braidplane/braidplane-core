package jobs

import (
	"slices"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type ProofObservationMaintenanceArgs struct{}

func (ProofObservationMaintenanceArgs) Kind() string { return "proof_observation_maintenance" }
func (ProofObservationMaintenanceArgs) InsertOpts() river.InsertOpts {
	// A completed sweep must not suppress the next interval; queued/running sweeps
	// coalesce so a busy maintenance queue cannot build an unbounded backlog.
	states := slices.DeleteFunc(rivertype.UniqueOptsByStateDefault(), func(state rivertype.JobState) bool { return state == rivertype.JobStateCompleted })
	return river.InsertOpts{Queue: EventMaintenanceQueue, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: states}}
}
