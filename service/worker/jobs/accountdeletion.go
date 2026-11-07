package jobs

import (
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/riverqueue/river"
)

type AccountDeletionArgs struct {
	Request slug.Slug `json:"request"`
}

func (AccountDeletionArgs) Kind() string { return "account_deletion" }
func (AccountDeletionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: EventMaintenanceQueue, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}
