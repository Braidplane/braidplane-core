package views

import (
	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/runlineage"
)

type BuilderRunEvidenceData struct {
	Runs        []api.PipelineRunMetadata
	SelectedRun string
	NextCursor  string
	HasCursor   bool
	Run         *api.PipelineRunRecord
	RunHref     string
	Message     string
	Nodes       []runlineage.Node
	Checks      []api.PipelineRunStructuralCheck
}

func builderRunListed(data BuilderRunEvidenceData) bool {
	for _, run := range data.Runs {
		if run.RunSlug == data.SelectedRun {
			return true
		}
	}
	return false
}
