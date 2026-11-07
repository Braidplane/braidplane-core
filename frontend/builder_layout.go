package frontend

import "github.com/Braidplane/braidplane-core/frontend/pipelinebuilder"

type position = pipelinebuilder.Position

func layoutGraph(spec *builderPipelineSpec) map[string]position {
	return pipelinebuilder.LayoutGraph(spec)
}
