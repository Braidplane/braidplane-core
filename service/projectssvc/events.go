package projectssvc

import (
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
)

type projectEvent struct {
	projectSlug slug.Slug
	action      model.EventAction
}

func (e projectEvent) EventRecord() (eventrecorder.Record, error) {
	return eventrecorder.Record{
		SlugPrefix:    "evt-project-" + slug.Slug(e.action.String()),
		EventClass:    model.EventClassSecurityAudit,
		EntityType:    model.EventEntityTypeProject,
		EntitySlug:    e.projectSlug,
		Action:        e.action,
		SchemaVersion: model.CurrentEventSchemaVersion,
	}, nil
}
