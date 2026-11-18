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
	version        string
}

func (e dataSourceEvent) EventRecord() (eventrecorder.Record, error) {
	metadata := map[string]any{"data_source_type": e.dataSourceType.String()}
	if e.version != "" {
		metadata["version"] = e.version
	}
	return eventrecorder.Record{
		SlugPrefix:    "evt-data-source-" + slug.Slug(e.action.String()),
		EventClass:    model.EventClassSecurityAudit,
		EntityType:    model.EventEntityTypeDataSource,
		EntitySlug:    e.dataSourceSlug,
		Action:        e.action,
		SchemaVersion: model.CurrentEventSchemaVersion,
		Metadata:      metadata,
	}, nil
}
