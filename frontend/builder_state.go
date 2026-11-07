package frontend

import (
	"github.com/Braidplane/braidplane-core/frontend/pipelinebuilder"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var errBuilderStateInvalid = xerror.BuilderStateInvalid()

const (
	builderKindQuery     = "query"
	builderKindTransform = "transform"
	builderKindSink      = "sink"

	builderPortRows    = "rows"
	builderPortPayload = "payload"

	builderStepKindQuery        = "query"
	builderDefaultOperationType = "sql"
)

type (
	builderState           = pipelinebuilder.State
	builderPipelineSpec    = pipelinebuilder.PipelineSpec
	builderStateContext    = pipelinebuilder.Context
	builderGraph           = pipelinebuilder.Graph
	builderGraphNode       = pipelinebuilder.GraphNode
	builderEdge            = pipelinebuilder.Edge
	builderBinding         = pipelinebuilder.Binding
	builderValidationIssue = pipelinebuilder.ValidationIssue
)

func resolveBuilderPipelineExportFormat(context builderStateContext) model.PipelineFormat {
	return pipelinebuilder.ResolvePipelineExportFormat(context)
}

func builderPipelineFormatFromPath(path string) (model.PipelineFormat, error) {
	return pipelinebuilder.PipelineFormatFromPath(path)
}

func serializeBuilderStateCanonical(input builderState) ([]byte, error) {
	return pipelinebuilder.SerializeStateCanonical(input)
}

func serializeBuilderStateComparable(input builderState) ([]byte, error) {
	return pipelinebuilder.SerializeStateComparable(input)
}

func serializeBuilderPipelineSpec(input builderState, format model.PipelineFormat) ([]byte, error) {
	return pipelinebuilder.SerializePipelineSpec(input, format)
}

func validateBuilderPipelineSpec(format model.PipelineFormat, input []byte) error {
	return pipelinebuilder.ValidatePipelineSpec(format, input)
}

func deserializeBuilderState(input []byte) (builderState, error) {
	return pipelinebuilder.DeserializeState(input)
}

func deserializeBuilderStatePermissive(input []byte) (builderState, error) {
	return pipelinebuilder.DeserializeStatePermissive(input)
}

func deserializeBuilderStateWithIssues(
	input []byte,
) (builderState, []builderValidationIssue, error) {
	return pipelinebuilder.DeserializeStateWithIssues(input)
}

func builderStateFromPipelineSpec(path string, data []byte) (builderState, error) {
	return pipelinebuilder.FromPipelineSpec(path, data)
}

func builderStateFromPipeline(pipeline *pipelinespec.Pipeline) (builderState, error) {
	return pipelinebuilder.FromPipeline(pipeline)
}

func builderTransformInputsForSources(
	existing map[string]pipelinespec.StepInput,
	sources []slug.Slug,
) (map[string]pipelinespec.StepInput, error) {
	return pipelinebuilder.TransformInputsForSources(existing, sources)
}

func resolveBuilderSinkBackend(operationType string) model.SinkBackend {
	return pipelinebuilder.ResolveSinkBackend(operationType)
}

func canonicalizeBuilderState(input builderState) builderState {
	return pipelinebuilder.CanonicalizeState(input)
}

func validateBuilderState(input builderState) error {
	return pipelinebuilder.ValidateState(input)
}

func builderStateToPipeline(state builderState) (*pipelinespec.Pipeline, error) {
	return pipelinebuilder.ToPipeline(state)
}

func builderStateToDraftPipeline(state builderState) (*pipelinespec.Pipeline, error) {
	return pipelinebuilder.ToDraftPipeline(state)
}

func inlineSQLFrom(operationInline map[string]any) string {
	return pipelinebuilder.InlineSQLFrom(operationInline)
}
