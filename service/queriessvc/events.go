package queriessvc

import (
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

type queryEvent struct {
	dataSourceSlug slug.Slug
	querySlug      slug.Slug
	action         model.EventAction
}

func (e queryEvent) EventRecord() (eventrecorder.Record, error) {
	record := eventrecorder.Record{
		SlugPrefix: "evt-query-" + slug.Slug(e.action.String()),
		EntityType: model.EventEntityTypeQuery,
		EntitySlug: e.querySlug,
		Action:     e.action,
		Metadata: map[string]any{
			"data_source_slug": e.dataSourceSlug.String(),
		},
	}
	if e.action == model.EventActionDelete || e.action == model.EventActionPurge {
		record.EventClass = model.EventClassSecurityAudit
		record.SchemaVersion = model.CurrentEventSchemaVersion
	}
	return record, nil
}

type queryExecutedEvent struct {
	dataSourceSlug slug.Slug
	querySlug      slug.Slug
	executionSlug  slug.Slug
	status         string
	parameterKeys  []string
	rowCount       *int
}

func (e queryExecutedEvent) EventRecord() (eventrecorder.Record, error) {
	metadata := map[string]any{
		"query_slug":       e.querySlug.String(),
		"data_source_slug": e.dataSourceSlug.String(),
		"status":           e.status,
		"parameter_keys":   e.parameterKeys,
	}
	if e.rowCount != nil {
		metadata["row_count"] = *e.rowCount
	}

	return eventrecorder.Record{
		SlugPrefix: "evt-query-execute",
		EntityType: model.EventEntityTypeQueryExecution,
		EntitySlug: e.executionSlug,
		Action:     model.EventActionExecute,
		Metadata:   metadata,
	}, nil
}
