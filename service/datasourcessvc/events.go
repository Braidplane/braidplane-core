package datasourcessvc

import (
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

type dataSourceEvent struct {
	dataSourceSlug slug.Slug
	dataSourceType model.DataSourceType
	action         model.EventAction
}

func (e dataSourceEvent) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{
		SlugPrefix:    "evt-data-source-" + slug.Slug(e.action.String()),
		EventClass:    model.EventClassSecurityAudit,
		EntityType:    model.EventEntityTypeDataSource,
		EntitySlug:    e.dataSourceSlug,
		Action:        e.action,
		SchemaVersion: model.CurrentEventSchemaVersion,
		Metadata: map[string]any{
			"data_source_type": e.dataSourceType.String(),
		},
	}, nil
}
