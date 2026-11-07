package jobs

import "github.com/riverqueue/river"

const EventMaintenanceQueue = "event_maintenance"

type EventPruneArgs struct{}

func (EventPruneArgs) Kind() string {
	return "event_prune"
}

func (EventPruneArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: EventMaintenanceQueue}
}
