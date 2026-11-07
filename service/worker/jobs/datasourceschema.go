package jobs

import (
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const DataSourceSchemaImportQueue = "data_source_schema_import"

// DataSourceSchemaImportArgs is the durable River payload for one schema import.
// Its unique args make enqueueing the same attempt idempotent; cross-request
// coalescing for a data source is handled before enqueue by StartImportByDataSourceSlug.
type DataSourceSchemaImportArgs struct {
	OrganizationSlug slug.Slug        `json:"organization_slug" river:"unique"`
	DataSourceSlug   slug.Slug        `json:"data_source_slug" river:"unique"`
	AttemptID        string           `json:"attempt_id" river:"unique"`
	RequestedAt      time.Time        `json:"requested_at"`
	Actor            model.EventActor `json:"actor"`
}

// Kind returns the stable River job kind for data-source schema imports.
func (DataSourceSchemaImportArgs) Kind() string {
	return "data_source_schema_import"
}

// InsertOpts schedules data-source schema imports on their dedicated queue.
// UniqueOpts.ByArgs prevents reinserting the exact same attempt, not separate refresh requests.
func (DataSourceSchemaImportArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: DataSourceSchemaImportQueue,
		UniqueOpts: river.UniqueOpts{
			ByArgs:  true,
			ByState: rivertype.UniqueOptsByStateDefault(),
		},
	}
}
