package pipelinebuilder

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/validate"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

var errBuilderStateInvalid = xerror.BuilderStateInvalid()

const (
	builderPortRows    = "rows"
	builderPortPayload = "payload"

	builderStepKindQuery        = "query"
	builderDefaultOperationType = "sql"
)

type State struct {
	PipelineSpec PipelineSpec `json:"pipeline_spec"     validate:"required"`
	Graph        Graph        `json:"graph"             validate:"required"`
	Context      Context      `json:"context,omitempty"`
}

type comparableState struct {
	PipelineSpec PipelineSpec `json:"pipeline_spec"`
	Graph        Graph        `json:"graph"`
}

type PipelineSpec struct {
	Metadata pipelinespec.Metadata `json:"metadata" validate:"required"`
	Steps    []pipelinespec.Step   `json:"steps"    validate:"dive"`
}

type namedStepInput struct {
	name  string
	input pipelinespec.StepInput
}

type Context struct {
	ExpectedVersionID     *string               `json:"expected_spec_hash,omitempty"`
	CurrentVersionID      *string               `json:"current_version_spec_hash,omitempty"`
	DraftBasedOnVersionID *string               `json:"draft_based_on_spec_hash,omitempty"`
	SourceFormat          *model.PipelineFormat `json:"source_format,omitempty"`
	ExportFormat          *model.PipelineFormat `json:"export_format,omitempty"`
	Deterministic         bool                  `json:"deterministic"`
	TimeoutSecs           *int                  `json:"timeout_seconds,omitempty"`
	RecordMeta            bool                  `json:"record_run_metadata"`
	TargetOrgSlug         string                `json:"target_org_slug,omitempty"`
	TargetProjectSlug     string                `json:"target_project_slug,omitempty"`
	TargetEnvironmentSlug string                `json:"target_environment_slug,omitempty"`
}

type Graph struct {
	Nodes    []GraphNode `json:"nodes"    validate:"dive"`
	Edges    []Edge      `json:"edges"    validate:"dive"`
	Bindings []Binding   `json:"bindings" validate:"dive"`
}

type GraphNode struct {
	StepSlug slug.Slug `json:"step_slug" validate:"required,slug"`
}

type Edge struct {
	FromStep slug.Slug `json:"from_step" validate:"required,slug"`
	ToStep   slug.Slug `json:"to_step"   validate:"required,slug"`
}

type Binding struct {
	FromStep slug.Slug `json:"from_step" validate:"required,slug"`
	FromPort string    `json:"from_port" validate:"required,min=1"`
	ToStep   slug.Slug `json:"to_step"   validate:"required,slug"`
	ToPort   string    `json:"to_port"   validate:"required,min=1"`
}

type ValidationIssue struct {
	Field   string
	Code    string
	Message string
}

func ResolvePipelineExportFormat(context Context) model.PipelineFormat {
	if context.ExportFormat != nil && *context.ExportFormat != model.PipelineFormatUnknown {
		return *context.ExportFormat
	}
	if context.SourceFormat != nil && *context.SourceFormat != model.PipelineFormatUnknown {
		return *context.SourceFormat
	}
	return model.PipelineFormatYAML
}

func PipelineFormatFromPath(path string) (model.PipelineFormat, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return model.PipelineFormatYAML, nil
	case ".json":
		return model.PipelineFormatJSON, nil
	default:
		return model.PipelineFormatUnknown, errBuilderStateInvalid.Detailf(
			"unsupported builder pipeline format in path %q",
			path,
		)
	}
}

// SerializeStateCanonical emits a stable JSON representation for diffs/reviews.
// We canonicalize and validate first so all writers produce the same output shape.
func SerializeStateCanonical(input State) ([]byte, error) {
	canonical := CanonicalizeState(input)
	if err := ValidateState(canonical); err != nil {
		return nil, err
	}

	output, err := json.MarshalIndent(canonical, "", "  ")
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return output, nil
}

func SerializeStateComparable(input State) ([]byte, error) {
	canonical := CanonicalizeState(input)
	if err := ValidateState(canonical); err != nil {
		return nil, err
	}

	output, err := json.MarshalIndent(comparableState{
		PipelineSpec: canonical.PipelineSpec,
		Graph:        canonical.Graph,
	}, "", "  ")
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return output, nil
}

func SerializePipelineSpec(input State, format model.PipelineFormat) ([]byte, error) {
	canonical := CanonicalizeState(input)
	if err := ValidateState(canonical); err != nil {
		return nil, err
	}

	pipeline, err := ToPipeline(canonical)
	if err != nil {
		return nil, err
	}

	switch format {
	case model.PipelineFormatYAML:
		output, err := yaml.Marshal(pipeline)
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		return output, nil
	case model.PipelineFormatJSON:
		output, err := json.MarshalIndent(pipeline, "", "  ")
		if err != nil {
			return nil, xerror.Unexpected().With(err)
		}
		return output, nil
	default:
		return nil, errBuilderStateInvalid.Detailf("unsupported export format %q", format)
	}
}

func ValidatePipelineSpec(format model.PipelineFormat, input []byte) error {
	if len(strings.TrimSpace(string(input))) == 0 {
		return errBuilderStateInvalid.Detail("empty pipeline spec")
	}

	var filename string
	switch format {
	case model.PipelineFormatYAML:
		filename = "builder-export.pipeline.yaml"
	case model.PipelineFormatJSON:
		filename = "builder-export.pipeline.json"
	default:
		return errBuilderStateInvalid.Detailf("unsupported export format %q", format)
	}

	if _, err := pipelinevalidate.Load(filename, input); err != nil {
		return errBuilderStateInvalid.Detailf("pipeline validation failed: %w", err)
	}

	return nil
}

// DeserializeState loads state from JSON and normalizes it immediately.
// Canonicalization on read keeps round-trips deterministic across UI/code boundaries.
func DeserializeState(input []byte) (State, error) {
	state, _, err := DeserializeStateWithIssues(input)
	if err != nil {
		return State{}, err
	}

	return state, nil
}

func DeserializeStatePermissive(input []byte) (State, error) {
	if len(strings.TrimSpace(string(input))) == 0 {
		return State{}, errBuilderStateInvalid.Detail("empty payload")
	}

	var state State
	if err := json.Unmarshal(input, &state); err != nil {
		return State{}, xerror.Unexpected().With(err)
	}

	return CanonicalizeState(state), nil
}

func DeserializeStateWithIssues(
	input []byte,
) (State, []ValidationIssue, error) {
	if len(strings.TrimSpace(string(input))) == 0 {
		return State{}, nil, errBuilderStateInvalid.Detail("empty payload")
	}

	var state State
	if err := json.Unmarshal(input, &state); err != nil {
		return State{}, nil, xerror.Unexpected().With(err)
	}

	state = CanonicalizeState(state)
	issues := ValidateStateIssues(state)
	if len(issues) > 0 {
		return State{}, issues, errBuilderStateInvalid.Detailf("%s", issues[0].Message)
	}

	return state, nil, nil
}

// FromPipelineSpec loads a pipeline spec and converts it into builder graph state.
// This is the spec-to-visual entry point used by import flows.
func FromPipelineSpec(path string, data []byte) (State, error) {
	if strings.TrimSpace(path) == "" {
		return State{}, errBuilderStateInvalid.Detail("pipeline path is required")
	}

	sourceFormat, err := PipelineFormatFromPath(path)
	if err != nil {
		return State{}, err
	}

	pipeline, err := pipelinevalidate.Load(path, data)
	if err != nil {
		return State{}, errBuilderStateInvalid.Detailf("load pipeline spec: %w", err)
	}

	state, err := FromPipeline(pipeline)
	if err != nil {
		return State{}, err
	}
	state.Context.SourceFormat = &sourceFormat
	state.Context.ExportFormat = &sourceFormat

	return CanonicalizeState(state), nil
}

// FromPipeline maps a validated pipeline spec into builder graph state.
// This establishes the visual load path for supported step kinds in a deterministic form.
func FromPipeline(pipeline *pipelinespec.Pipeline) (State, error) {
	if pipeline == nil {
		return State{}, errBuilderStateInvalid.Detail("pipeline is required")
	}

	nodes := make([]GraphNode, 0, len(pipeline.Steps))
	edges := make([]Edge, 0, len(pipeline.Steps))
	bindings := make([]Binding, 0, len(pipeline.Steps))

	// Create nodes without coordinates; layout is computed deterministically
	for _, step := range pipeline.Steps {
		nodes = append(nodes, GraphNode{
			StepSlug: step.Slug,
		})
	}

	for _, step := range pipeline.Steps {
		if step.Transform == nil {
			continue
		}
		for _, namedInput := range sortedTransformInputs(step.Transform) {
			edges = append(
				edges,
				Edge{FromStep: namedInput.input.FromStep, ToStep: step.Slug},
			)
			bindings = append(bindings, Binding{
				FromStep: namedInput.input.FromStep,
				FromPort: builderPortRows,
				ToStep:   step.Slug,
				ToPort:   namedInput.name,
			})
		}
	}

	for _, step := range pipeline.Steps {
		if step.Sink == nil || step.Sink.Input == nil {
			continue
		}
		edges = append(edges, Edge{FromStep: step.Sink.Input.FromStep, ToStep: step.Slug})
		bindings = append(bindings, Binding{
			FromStep: step.Sink.Input.FromStep,
			FromPort: builderPortRows,
			ToStep:   step.Slug,
			ToPort:   builderPortPayload,
		})
	}

	state := CanonicalizeState(State{
		PipelineSpec: PipelineSpec{
			Metadata: pipeline.Metadata,
			Steps:    cloneSteps(pipeline.Steps),
		},
		Graph: Graph{
			Nodes:    nodes,
			Edges:    edges,
			Bindings: bindings,
		},
	})

	if err := ValidateState(state); err != nil {
		return State{}, err
	}

	return state, nil
}

func sortedTransformInputs(transform *pipelinespec.TransformSpec) []namedStepInput {
	if transform == nil || len(transform.Inputs) == 0 {
		return nil
	}
	names := make([]string, 0, len(transform.Inputs))
	for name := range transform.Inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	inputs := make([]namedStepInput, 0, len(names))
	for _, name := range names {
		inputs = append(inputs, namedStepInput{
			name:  name,
			input: transform.Inputs[name],
		})
	}
	return inputs
}

func deduplicateSources(sources []slug.Slug) []slug.Slug {
	if len(sources) == 0 {
		return nil
	}
	seen := make(map[slug.Slug]struct{}, len(sources))
	deduped := make([]slug.Slug, 0, len(sources))
	for _, source := range sources {
		if _, exists := seen[source]; exists {
			continue
		}
		seen[source] = struct{}{}
		deduped = append(deduped, source)
	}
	sort.Slice(deduped, func(i, j int) bool {
		return deduped[i].String() < deduped[j].String()
	})
	return deduped
}

func TransformInputsForSources(
	existing map[string]pipelinespec.StepInput,
	sources []slug.Slug,
) (map[string]pipelinespec.StepInput, error) {
	inputs := make(map[string]pipelinespec.StepInput, len(sources))
	usedNames := make(map[string]struct{}, len(sources))
	namesBySource := make(map[slug.Slug][]string, len(existing))
	for name, input := range existing {
		namesBySource[input.FromStep] = append(namesBySource[input.FromStep], name)
	}
	for source := range namesBySource {
		sort.Strings(namesBySource[source])
	}
	nextNameIndex := make(map[slug.Slug]int, len(namesBySource))
	for _, source := range sources {
		matchingNames := namesBySource[source]
		if len(matchingNames) == 0 {
			return nil, errBuilderStateInvalid.Detailf(
				"upstream sources require explicit transform input names; step %q is not named in transform.inputs",
				source,
			)
		}
		nameIndex := nextNameIndex[source]
		inputName := matchingNames[min(nameIndex, len(matchingNames)-1)]
		if nameIndex < len(matchingNames)-1 {
			nextNameIndex[source] = nameIndex + 1
		}
		baseName := inputName
		for suffix := 2; ; suffix++ {
			if _, exists := usedNames[inputName]; !exists {
				break
			}
			inputName = fmt.Sprintf("%s_%d", baseName, suffix)
		}
		usedNames[inputName] = struct{}{}
		inputs[inputName] = pipelinespec.StepInput{FromStep: source}
	}
	return inputs, nil
}

// mergeTransformInputs overlays graph-derived inputs onto declared inputs without dropping declared ports.
func mergeTransformInputs(
	declared map[string]pipelinespec.StepInput,
	graph map[string]pipelinespec.StepInput,
) map[string]pipelinespec.StepInput {
	inputs := make(map[string]pipelinespec.StepInput, len(declared)+len(graph))
	for name, input := range declared {
		inputs[name] = input
	}
	for name, input := range graph {
		inputs[name] = input
	}
	return inputs
}

// ResolveSinkBackend returns the catalog-defined sink backend for a builder operation type.
func ResolveSinkBackend(operationType string) model.SinkBackend {
	if backend, ok := pipelinespec.SinkBackendForOperationType(strings.ToLower(strings.TrimSpace(operationType))); ok {
		return backend
	}
	return model.SinkBackendUnknown
}

// CanonicalizeState normalizes values and sorts unordered graph collections.
// Pipeline step and graph node order are authored source state and must remain stable across a visual round trip.
func CanonicalizeState(input State) State {
	context := input.Context
	context.TargetOrgSlug = strings.TrimSpace(context.TargetOrgSlug)
	context.TargetProjectSlug = strings.TrimSpace(context.TargetProjectSlug)
	context.TargetEnvironmentSlug = strings.TrimSpace(context.TargetEnvironmentSlug)
	pipelineSpec := canonicalizePipelineSpec(input.PipelineSpec)
	graph := canonicalizeGraph(input.Graph)
	return State{
		PipelineSpec: pipelineSpec,
		Graph:        graph,
		Context:      context,
	}
}

// ValidateState enforces structural integrity before save/export.
// We fail fast on invalid slugs or broken references to prevent corrupt graph state.
func ValidateState(input State) error {
	issues := ValidateStateIssues(input)
	if len(issues) == 0 {
		return nil
	}

	return errBuilderStateInvalid.Detailf("%s", issues[0].Message)
}

func ValidateStateIssues(input State) []ValidationIssue {
	if err := validate.Default().Struct(input); err != nil {
		return []ValidationIssue{{
			Field:   "builder_state",
			Code:    "INVALID_STRUCTURE",
			Message: err.Error(),
		}}
	}

	stepBySlug := make(map[slug.Slug]pipelinespec.Step, len(input.PipelineSpec.Steps))
	for i, step := range input.PipelineSpec.Steps {
		if _, exists := stepBySlug[step.Slug]; exists {
			return []ValidationIssue{{
				Field:   fmt.Sprintf("pipeline_spec.steps[%d].slug", i),
				Code:    "DUPLICATE_STEP_SLUG",
				Message: fmt.Sprintf("duplicate step slug %q", step.Slug),
			}}
		}
		if step.Transform != nil && strings.TrimSpace(step.Transform.SQL) == "" {
			return []ValidationIssue{{
				Field:   fmt.Sprintf("pipeline_spec.steps[%d].transform.sql", i),
				Code:    "TRANSFORM_SQL_REQUIRED",
				Message: fmt.Sprintf("transform step %q requires non-empty SQL", step.Slug),
			}}
		}
		stepBySlug[step.Slug] = step
	}

	nodeByStep := make(map[slug.Slug]GraphNode, len(input.Graph.Nodes))
	incomingEdgeCount := make(map[slug.Slug]int, len(input.Graph.Edges))
	seenSingleTargetPorts := make(map[string]struct{}, len(input.Graph.Bindings))
	for i, node := range input.Graph.Nodes {
		if _, exists := nodeByStep[node.StepSlug]; exists {
			return []ValidationIssue{{
				Field:   fmt.Sprintf("graph.nodes[%d].step_slug", i),
				Code:    "DUPLICATE_GRAPH_NODE_STEP_SLUG",
				Message: fmt.Sprintf("duplicate graph node step slug %q", node.StepSlug),
			}}
		}
		if _, exists := stepBySlug[node.StepSlug]; !exists {
			return []ValidationIssue{
				{
					Field: fmt.Sprintf("graph.nodes[%d].step_slug", i),
					Code:  "GRAPH_NODE_STEP_NOT_FOUND",
					Message: fmt.Sprintf(
						"graph node step %q not found in pipeline_spec.steps",
						node.StepSlug,
					),
				},
			}
		}
		// Nodes are validated by reference; positions are computed at render time
		nodeByStep[node.StepSlug] = node
	}

	for i, step := range input.PipelineSpec.Steps {
		if _, exists := nodeByStep[step.Slug]; exists {
			continue
		}
		return []ValidationIssue{{
			Field:   fmt.Sprintf("pipeline_spec.steps[%d].slug", i),
			Code:    "STEP_GRAPH_NODE_REQUIRED",
			Message: fmt.Sprintf("step %q must have a graph node", step.Slug),
		}}
	}

	for i, edge := range input.Graph.Edges {
		fromStep, exists := stepBySlug[edge.FromStep]
		if !exists {
			return []ValidationIssue{{
				Field:   fmt.Sprintf("graph.edges[%d].from_step", i),
				Code:    "EDGE_SOURCE_NOT_FOUND",
				Message: fmt.Sprintf("edge source step %q not found", edge.FromStep),
			}}
		}
		toStep, exists := stepBySlug[edge.ToStep]
		if !exists {
			return []ValidationIssue{{
				Field:   fmt.Sprintf("graph.edges[%d].to_step", i),
				Code:    "EDGE_TARGET_NOT_FOUND",
				Message: fmt.Sprintf("edge target step %q not found", edge.ToStep),
			}}
		}
		if fromStep.Sink != nil {
			return []ValidationIssue{
				{
					Field: fmt.Sprintf("graph.edges[%d].from_step", i),
					Code:  "SINK_OUTGOING_DEPENDENCY_FORBIDDEN",
					Message: fmt.Sprintf(
						"sink step %q cannot have outgoing dependencies",
						edge.FromStep,
					),
				},
			}
		}
		if toStep.Query != nil {
			return []ValidationIssue{
				{
					Field: fmt.Sprintf("graph.edges[%d].to_step", i),
					Code:  "QUERY_INCOMING_DEPENDENCY_FORBIDDEN",
					Message: fmt.Sprintf(
						"query step %q cannot have incoming dependencies",
						edge.ToStep,
					),
				},
			}
		}
		if toStep.Input != nil {
			return []ValidationIssue{
				{
					Field: fmt.Sprintf("graph.edges[%d].to_step", i),
					Code:  "INPUT_INCOMING_DEPENDENCY_FORBIDDEN",
					Message: fmt.Sprintf(
						"input step %q cannot have incoming dependencies",
						edge.ToStep,
					),
				},
			}
		}
		incomingEdgeCount[edge.ToStep] += 1
		if toStep.Sink != nil && incomingEdgeCount[edge.ToStep] > 1 {
			return []ValidationIssue{
				{
					Field: fmt.Sprintf("graph.edges[%d].to_step", i),
					Code:  "MULTIPLE_INCOMING_DEPENDENCIES",
					Message: fmt.Sprintf(
						"step %q cannot have more than one incoming dependency",
						edge.ToStep,
					),
				},
			}
		}
	}
	if cyclicStep, ok := cyclicGraphStep(input.Graph.Edges); ok {
		return []ValidationIssue{{
			Field:   "graph.edges",
			Code:    "CYCLIC_STEP_DEPENDENCY",
			Message: fmt.Sprintf("step dependency cycle includes %q", cyclicStep),
		}}
	}

	for i, binding := range input.Graph.Bindings {
		fromStep, fromExists := stepBySlug[binding.FromStep]
		if !fromExists {
			return []ValidationIssue{{
				Field:   fmt.Sprintf("graph.bindings[%d].from_step", i),
				Code:    "BINDING_SOURCE_NOT_FOUND",
				Message: fmt.Sprintf("binding source step %q not found", binding.FromStep),
			}}
		}
		toStep, toExists := stepBySlug[binding.ToStep]
		if !toExists {
			return []ValidationIssue{{
				Field:   fmt.Sprintf("graph.bindings[%d].to_step", i),
				Code:    "BINDING_TARGET_NOT_FOUND",
				Message: fmt.Sprintf("binding target step %q not found", binding.ToStep),
			}}
		}

		if toStep.Query != nil {
			return []ValidationIssue{{
				Field:   fmt.Sprintf("graph.bindings[%d].to_step", i),
				Code:    "BINDING_TARGET_QUERY_FORBIDDEN",
				Message: fmt.Sprintf("query step %q cannot be a binding target", binding.ToStep),
			}}
		}
		if toStep.Input != nil {
			return []ValidationIssue{{
				Field:   fmt.Sprintf("graph.bindings[%d].to_step", i),
				Code:    "BINDING_TARGET_INPUT_FORBIDDEN",
				Message: fmt.Sprintf("input step %q cannot be a binding target", binding.ToStep),
			}}
		}
		if fromStep.Sink != nil {
			return []ValidationIssue{{
				Field:   fmt.Sprintf("graph.bindings[%d].from_step", i),
				Code:    "BINDING_SOURCE_SINK_FORBIDDEN",
				Message: fmt.Sprintf("sink step %q cannot be a binding source", binding.FromStep),
			}}
		}

		if toStep.Transform != nil {
			if !isStepOutputProducer(fromStep) {
				return []ValidationIssue{
					{
						Field: fmt.Sprintf("graph.bindings[%d].from_step", i),
						Code:  "STEP_BINDING_SOURCE_KIND_INVALID",
						Message: fmt.Sprintf(
							"transform step %q must be bound from a step node",
							binding.ToStep,
						),
					},
				}
			}
			if binding.FromPort != builderPortRows {
				return []ValidationIssue{
					{
						Field: fmt.Sprintf("graph.bindings[%d].from_port", i),
						Code:  "STEP_BINDING_SOURCE_PORT_INVALID",
						Message: fmt.Sprintf(
							"step %q expects source port %q",
							binding.ToStep,
							builderPortRows,
						),
					},
				}
			}
			if strings.TrimSpace(binding.ToPort) == "" {
				return []ValidationIssue{
					{
						Field: fmt.Sprintf("graph.bindings[%d].to_port", i),
						Code:  "STEP_BINDING_TARGET_PORT_INVALID",
						Message: fmt.Sprintf(
							"transform step %q expects a named input port",
							binding.ToStep,
						),
					},
				}
			}
			if len(toStep.Transform.Inputs) > 0 {
				if _, exists := toStep.Transform.Inputs[binding.ToPort]; !exists {
					return []ValidationIssue{
						{
							Field: fmt.Sprintf("graph.bindings[%d].to_port", i),
							Code:  "STEP_BINDING_TARGET_PORT_INVALID",
							Message: fmt.Sprintf(
								"transform step %q does not define input port %q",
								binding.ToStep,
								binding.ToPort,
							),
						},
					}
				}
			}
		}

		if toStep.Sink != nil {
			if !isStepOutputProducer(fromStep) {
				return []ValidationIssue{
					{
						Field: fmt.Sprintf("graph.bindings[%d].from_step", i),
						Code:  "SINK_BINDING_SOURCE_KIND_INVALID",
						Message: fmt.Sprintf(
							"sink step %q must be bound from a step node",
							binding.ToStep,
						),
					},
				}
			}
			if binding.FromPort != builderPortRows {
				return []ValidationIssue{
					{
						Field: fmt.Sprintf("graph.bindings[%d].from_port", i),
						Code:  "SINK_BINDING_SOURCE_PORT_INVALID",
						Message: fmt.Sprintf(
							"sink step %q expects source port %q",
							binding.ToStep,
							builderPortRows,
						),
					},
				}
			}
			if binding.ToPort != builderPortPayload {
				return []ValidationIssue{
					{
						Field: fmt.Sprintf("graph.bindings[%d].to_port", i),
						Code:  "SINK_BINDING_TARGET_PORT_INVALID",
						Message: fmt.Sprintf(
							"sink step %q expects target port %q",
							binding.ToStep,
							builderPortPayload,
						),
					},
				}
			}
		}

		targetKey := fmt.Sprintf("%s:%s", binding.ToStep, binding.ToPort)
		if _, exists := seenSingleTargetPorts[targetKey]; exists {
			return []ValidationIssue{
				{
					Field: fmt.Sprintf("graph.bindings[%d].to_port", i),
					Code:  "MULTIPLE_BINDINGS_FOR_TARGET_PORT",
					Message: fmt.Sprintf(
						"multiple bindings for input port %q on step %q are not allowed",
						binding.ToPort,
						binding.ToStep,
					),
				},
			}
		}
		seenSingleTargetPorts[targetKey] = struct{}{}
	}

	if len(input.Graph.Bindings) > 0 {
		for i, step := range input.PipelineSpec.Steps {
			if step.Transform == nil {
				continue
			}

			if len(step.Transform.Inputs) > 0 {
				for _, namedInput := range sortedTransformInputs(step.Transform) {
					inputBindingFound := false
					for _, binding := range input.Graph.Bindings {
						if binding.ToStep == step.Slug &&
							binding.FromPort == builderPortRows &&
							binding.ToPort == namedInput.name {
							inputBindingFound = true
							break
						}
					}
					if !inputBindingFound {
						return []ValidationIssue{
							{
								Field: fmt.Sprintf("pipeline_spec.steps[%d].transform.inputs.%s", i, namedInput.name),
								Code:  "STEP_BINDING_REQUIRED",
								Message: fmt.Sprintf(
									"transform step %q must have an input binding for %q",
									step.Slug,
									namedInput.name,
								),
							},
						}
					}
				}
				continue
			}

			inputBindingFound := false
			for _, binding := range input.Graph.Bindings {
				if binding.ToStep == step.Slug && binding.FromPort == builderPortRows {
					inputBindingFound = true
					break
				}
			}
			if !inputBindingFound {
				return []ValidationIssue{
					{
						Field: fmt.Sprintf("pipeline_spec.steps[%d].slug", i),
						Code:  "STEP_BINDING_REQUIRED",
						Message: fmt.Sprintf(
							"transform step %q must have an input binding",
							step.Slug,
						),
					},
				}
			}
		}
	}

	// Every sink node must have exactly one upstream step dependency.
	// When bindings are present, it must have a payload binding from a step.
	// When only edges are used, it must have exactly one incoming step edge.
	for i, step := range input.PipelineSpec.Steps {
		if step.Sink == nil {
			continue
		}

		if len(input.Graph.Bindings) > 0 {
			payloadBindingCount := 0
			for _, binding := range input.Graph.Bindings {
				if binding.ToStep == step.Slug && binding.ToPort == builderPortPayload {
					payloadBindingCount++
				}
			}
			if payloadBindingCount == 0 {
				return []ValidationIssue{
					{
						Field: fmt.Sprintf("pipeline_spec.steps[%d].slug", i),
						Code:  "SINK_PAYLOAD_BINDING_REQUIRED",
						Message: fmt.Sprintf(
							"sink step %q must have a %q payload binding from a step",
							step.Slug,
							builderPortPayload,
						),
					},
				}
			}
		} else {
			stepIncomingCount := 0
			for _, edge := range input.Graph.Edges {
				if edge.ToStep != step.Slug {
					continue
				}
				if fromStep, ok := stepBySlug[edge.FromStep]; ok && isStepOutputProducer(fromStep) {
					stepIncomingCount++
				}
			}
			if stepIncomingCount == 0 {
				return []ValidationIssue{
					{
						Field: fmt.Sprintf("pipeline_spec.steps[%d].slug", i),
						Code:  "SINK_UPSTREAM_STEP_REQUIRED",
						Message: fmt.Sprintf(
							"sink step %q must have at least one incoming step dependency",
							step.Slug,
						),
					},
				}
			}
		}
	}

	return nil
}

func cyclicGraphStep(edges []Edge) (slug.Slug, bool) {
	adjacency := make(map[slug.Slug][]slug.Slug, len(edges))
	for _, edge := range edges {
		adjacency[edge.FromStep] = append(adjacency[edge.FromStep], edge.ToStep)
	}

	const (
		visiting = iota + 1
		visited
	)
	state := make(map[slug.Slug]int, len(adjacency))
	var visit func(slug.Slug) (slug.Slug, bool)
	visit = func(stepSlug slug.Slug) (slug.Slug, bool) {
		switch state[stepSlug] {
		case visiting:
			return stepSlug, true
		case visited:
			return "", false
		}
		state[stepSlug] = visiting
		for _, next := range adjacency[stepSlug] {
			if cycleStep, ok := visit(next); ok {
				return cycleStep, true
			}
		}
		state[stepSlug] = visited
		return "", false
	}
	for stepSlug := range adjacency {
		if cycleStep, ok := visit(stepSlug); ok {
			return cycleStep, true
		}
	}
	return "", false
}

// ToPipeline reconstructs a Pipeline spec from builder graph state.
// The authored pipeline step order is preserved while graph bindings update dependencies.
func ToPipeline(state State) (*pipelinespec.Pipeline, error) {
	return toPipelineWithMode(state, true)
}

// ToDraftPipeline reconstructs a draft Pipeline spec from builder graph state.
func ToDraftPipeline(state State) (*pipelinespec.Pipeline, error) {
	return toPipelineWithMode(state, false)
}

func toPipelineWithMode(state State, strict bool) (*pipelinespec.Pipeline, error) {
	state = CanonicalizeState(state)
	if strict {
		if err := ValidateState(state); err != nil {
			return nil, err
		}
	}

	stepBySlug := make(map[slug.Slug]pipelinespec.Step, len(state.PipelineSpec.Steps))
	for _, step := range state.PipelineSpec.Steps {
		stepBySlug[step.Slug] = canonicalizeStep(step)
	}

	type edgeKey struct {
		from slug.Slug
		to   slug.Slug
	}
	edgeSet := make(map[edgeKey]struct{}, len(state.Graph.Edges))
	for _, edge := range state.Graph.Edges {
		edgeSet[edgeKey{from: edge.FromStep, to: edge.ToStep}] = struct{}{}
	}

	sourcesForTransform := make(map[slug.Slug][]slug.Slug, len(state.Graph.Bindings))
	inputsForTransform := make(map[slug.Slug]map[string]pipelinespec.StepInput, len(state.Graph.Bindings))
	sinkInputByStep := make(map[slug.Slug]slug.Slug, len(state.Graph.Bindings))
	if len(state.Graph.Bindings) > 0 {
		for _, binding := range state.Graph.Bindings {
			toStep, toOK := stepBySlug[binding.ToStep]
			fromStep, fromOK := stepBySlug[binding.FromStep]
			if !toOK || !fromOK {
				continue
			}
			if toStep.Transform != nil && isStepOutputProducer(fromStep) {
				inputName := strings.TrimSpace(binding.ToPort)
				if inputName == "" || binding.FromPort != builderPortRows {
					continue
				}
				if _, hasEdge := edgeSet[edgeKey{from: binding.FromStep, to: binding.ToStep}]; !hasEdge {
					if strict {
						return nil, errBuilderStateInvalid.Detailf(
							"binding from %q to %q has no corresponding edge",
							binding.FromStep,
							binding.ToStep,
						)
					}
					continue
				}
				sourcesForTransform[binding.ToStep] = append(
					sourcesForTransform[binding.ToStep],
					binding.FromStep,
				)
				if _, exists := inputsForTransform[binding.ToStep]; !exists {
					inputsForTransform[binding.ToStep] = make(map[string]pipelinespec.StepInput)
				}
				inputsForTransform[binding.ToStep][inputName] = pipelinespec.StepInput{FromStep: binding.FromStep}
				continue
			}
			if toStep.Sink == nil || !isStepOutputProducer(fromStep) {
				continue
			}
			if binding.ToPort != builderPortPayload || binding.FromPort != builderPortRows {
				continue
			}
			if _, hasEdge := edgeSet[edgeKey{from: binding.FromStep, to: binding.ToStep}]; !hasEdge {
				if strict {
					return nil, errBuilderStateInvalid.Detailf(
						"sink payload binding from %q to %q has no corresponding edge",
						binding.FromStep,
						binding.ToStep,
					)
				}
				continue
			}
			sinkInputByStep[binding.ToStep] = binding.FromStep
		}
	} else {
		for _, edge := range state.Graph.Edges {
			fromStep, fromOK := stepBySlug[edge.FromStep]
			toStep, toOK := stepBySlug[edge.ToStep]
			if !fromOK || !toOK {
				continue
			}
			if isStepOutputProducer(fromStep) && toStep.Transform != nil {
				sourcesForTransform[edge.ToStep] = append(
					sourcesForTransform[edge.ToStep],
					edge.FromStep,
				)
				continue
			}
			if isStepOutputProducer(fromStep) && toStep.Sink != nil {
				sinkInputByStep[edge.ToStep] = edge.FromStep
			}
		}
	}

	orderedSteps := make([]pipelinespec.Step, 0, len(stepBySlug))
	seen := make(map[slug.Slug]struct{})

	appendStep := func(stepSlug slug.Slug) error {
		if _, exists := seen[stepSlug]; exists {
			return nil
		}
		step, ok := stepBySlug[stepSlug]
		if !ok {
			if strict {
				return errBuilderStateInvalid.Detailf(
					"step %q not found in pipeline_spec.steps",
					stepSlug,
				)
			}
			return nil
		}
		switch {
		case step.Input != nil:
		case step.Transform != nil:
			transform := step.Transform
			sourceSteps := deduplicateSources(sourcesForTransform[stepSlug])
			if namedInputs, exists := inputsForTransform[stepSlug]; exists && len(namedInputs) > 0 {
				if strict {
					for _, namedInput := range sortedTransformInputs(transform) {
						inputName := namedInput.name
						if _, exists := namedInputs[inputName]; !exists {
							return errBuilderStateInvalid.Detailf(
								"transform step %q is missing graph binding for declared input %q",
								stepSlug,
								inputName,
							)
						}
					}
				}
				transform.Inputs = mergeTransformInputs(transform.Inputs, namedInputs)
			} else if len(sourceSteps) > 0 {
				inputs, err := TransformInputsForSources(transform.Inputs, sourceSteps)
				if err != nil {
					return err
				}
				transform.Inputs = inputs
			} else if strict && len(transform.Inputs) == 0 {
				return errBuilderStateInvalid.Detailf(
					"transform step %q is missing upstream input in graph",
					stepSlug,
				)
			}
			step.Transform = transform
		case step.Sink != nil:
			sink := step.Sink
			sourceStep, hasSource := sinkInputByStep[stepSlug]
			if hasSource {
				sink.Input = &pipelinespec.StepInput{FromStep: sourceStep}
			} else if strict && sink.Input == nil {
				return errBuilderStateInvalid.Detailf(
					"sink step %q is missing upstream input in graph",
					stepSlug,
				)
			}
			step.Sink = sink
		default:
			if step.Query == nil {
				if strict {
					return errBuilderStateInvalid.Detailf(
						"query step %q is missing query block",
						stepSlug,
					)
				}
				step.Query = &pipelinespec.QueryRef{OperationType: builderDefaultOperationType}
			}
		}
		orderedSteps = append(orderedSteps, step)
		seen[stepSlug] = struct{}{}
		return nil
	}

	for _, sourceStep := range state.PipelineSpec.Steps {
		if err := appendStep(sourceStep.Slug); err != nil {
			return nil, err
		}
	}

	return &pipelinespec.Pipeline{
		Metadata: pipelinespec.Metadata{
			Slug: state.PipelineSpec.Metadata.Slug,
			Name: state.PipelineSpec.Metadata.Name,
		},
		Steps: orderedSteps,
	}, nil
}

func canonicalizePipelineSpec(input PipelineSpec) PipelineSpec {
	steps := cloneSteps(input.Steps)
	for i := range steps {
		steps[i] = canonicalizeStep(steps[i])
	}
	return PipelineSpec{
		Metadata: pipelinespec.Metadata{
			Slug: slug.FromString(strings.TrimSpace(input.Metadata.Slug.String())),
			Name: strings.TrimSpace(input.Metadata.Name),
		},
		Steps: steps,
	}
}

func canonicalizeGraph(input Graph) Graph {
	nodes := append([]GraphNode(nil), input.Nodes...)
	edges := append([]Edge(nil), input.Edges...)
	bindings := append([]Binding(nil), input.Bindings...)
	// Canonicalize node slugs; positions are no longer stored
	for i := range nodes {
		nodes[i].StepSlug = slug.FromString(strings.TrimSpace(nodes[i].StepSlug.String()))
	}
	for i := range edges {
		edges[i].FromStep = slug.FromString(strings.TrimSpace(edges[i].FromStep.String()))
		edges[i].ToStep = slug.FromString(strings.TrimSpace(edges[i].ToStep.String()))
	}
	for i := range bindings {
		bindings[i].FromStep = slug.FromString(strings.TrimSpace(bindings[i].FromStep.String()))
		bindings[i].FromPort = strings.TrimSpace(bindings[i].FromPort)
		bindings[i].ToStep = slug.FromString(strings.TrimSpace(bindings[i].ToStep.String()))
		bindings[i].ToPort = strings.TrimSpace(bindings[i].ToPort)
	}
	// Edges and bindings are unordered. Nodes retain authored pipeline order;
	// positions are computed without changing their serialization order.
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].FromStep != edges[j].FromStep {
			return edges[i].FromStep.String() < edges[j].FromStep.String()
		}
		return edges[i].ToStep.String() < edges[j].ToStep.String()
	})
	sort.Slice(bindings, func(i, j int) bool {
		if bindings[i].FromStep != bindings[j].FromStep {
			return bindings[i].FromStep.String() < bindings[j].FromStep.String()
		}
		if bindings[i].FromPort != bindings[j].FromPort {
			return bindings[i].FromPort < bindings[j].FromPort
		}
		if bindings[i].ToStep != bindings[j].ToStep {
			return bindings[i].ToStep.String() < bindings[j].ToStep.String()
		}
		return bindings[i].ToPort < bindings[j].ToPort
	})
	return Graph{
		Nodes:    nodes,
		Edges:    edges,
		Bindings: bindings,
	}
}

func cloneSteps(steps []pipelinespec.Step) []pipelinespec.Step {
	if len(steps) == 0 {
		return nil
	}
	cloned := make([]pipelinespec.Step, len(steps))
	for i := range steps {
		cloned[i] = canonicalizeStep(steps[i])
	}
	return cloned
}

func isStepOutputProducer(step pipelinespec.Step) bool {
	return step.Input != nil || step.Query != nil || step.Transform != nil
}

func canonicalizeStep(step pipelinespec.Step) pipelinespec.Step {
	canonical := pipelinespec.Step{
		Kind: strings.TrimSpace(step.Kind),
		Slug: slug.FromString(strings.TrimSpace(step.Slug.String())),
		Name: strings.TrimSpace(step.Name),
	}
	if step.Input != nil {
		canonical.Input = &pipelinespec.InputSpec{
			OutputContract:    cloneSchemaContract(step.Input.OutputContract),
			OutputContractRef: cloneODCSContractRef(step.Input.OutputContractRef),
		}
	}
	if step.Query != nil {
		canonical.Query = &pipelinespec.QueryRef{
			DataSourceSlug:    slug.FromString(strings.TrimSpace(step.Query.DataSourceSlug.String())),
			OperationType:     strings.ToLower(strings.TrimSpace(step.Query.OperationType)),
			OperationRef:      slug.FromString(strings.TrimSpace(step.Query.OperationRef.String())),
			OperationInline:   cloneOperationInline(step.Query.OperationInline),
			OutputContract:    cloneSchemaContract(step.Query.OutputContract),
			OutputContractRef: cloneODCSContractRef(step.Query.OutputContractRef),
			Parameters:        cloneOperationInline(step.Query.Parameters),
			Incremental:       cloneIncrementalSourceContract(step.Query.Incremental),
		}
		if canonical.Query.OperationRef != "" {
			canonical.Query.OperationInline = nil
		}
	}
	if step.Transform != nil {
		canonical.Transform = &pipelinespec.TransformSpec{
			SQL:               step.Transform.SQL,
			OutputKind:        step.Transform.OutputKind,
			Inputs:            make(map[string]pipelinespec.StepInput, len(step.Transform.Inputs)),
			InputContract:     cloneSchemaContract(step.Transform.InputContract),
			InputContractRef:  cloneODCSContractRef(step.Transform.InputContractRef),
			OutputContract:    cloneSchemaContract(step.Transform.OutputContract),
			OutputContractRef: cloneODCSContractRef(step.Transform.OutputContractRef),
		}
		if step.Transform.InputContracts != nil {
			canonical.Transform.InputContracts = make(map[string]*pipelinespec.InputContractAttachment, len(step.Transform.InputContracts))
			for name, attachment := range step.Transform.InputContracts {
				if attachment == nil {
					canonical.Transform.InputContracts[name] = nil
					continue
				}
				canonical.Transform.InputContracts[name] = &pipelinespec.InputContractAttachment{
					Contract:    cloneSchemaContract(attachment.Contract),
					ContractRef: cloneODCSContractRef(attachment.ContractRef),
				}
			}
		}
		for _, namedInput := range sortedTransformInputs(step.Transform) {
			canonical.Transform.Inputs[namedInput.name] = pipelinespec.StepInput{
				FromStep: slug.FromString(strings.TrimSpace(namedInput.input.FromStep.String())),
			}
		}
	}
	if step.Sink != nil {
		canonical.Sink = &pipelinespec.SinkSpec{
			EmptyInputPolicy: step.Sink.EmptyInputPolicy,
			InputContract:    cloneSchemaContract(step.Sink.InputContract),
			InputContractRef: cloneODCSContractRef(step.Sink.InputContractRef),
			DataSourceSlug:   slug.FromString(strings.TrimSpace(step.Sink.DataSourceSlug.String())),
			OperationType:    strings.ToLower(strings.TrimSpace(step.Sink.OperationType)),
			OperationRef:     slug.FromString(strings.TrimSpace(step.Sink.OperationRef.String())),
			OperationInline:  cloneOperationInline(step.Sink.OperationInline),
			Config:           cloneOperationInline(step.Sink.Config),
			IdempotencyClass: step.Sink.IdempotencyClass,
		}
		if step.Sink.Input != nil {
			canonical.Sink.Input = &pipelinespec.StepInput{
				FromStep: slug.FromString(strings.TrimSpace(step.Sink.Input.FromStep.String())),
			}
		}
		if canonical.Sink.OperationRef != "" {
			canonical.Sink.OperationInline = nil
		}
	}
	return canonical
}

// cloneIncrementalSourceContract clones an IncrementalSourceContract so
// canonicalization can mutate copies without sharing source state.
func cloneIncrementalSourceContract(
	contract *pipelinespec.IncrementalSourceContract,
) *pipelinespec.IncrementalSourceContract {
	if contract == nil {
		return nil
	}

	var checkpointKind *model.IncrementalCheckpointKind
	if contract.CheckpointKind != nil {
		clonedKind := *contract.CheckpointKind
		checkpointKind = &clonedKind
	}

	ordering := make([]string, 0, len(contract.Ordering))
	for _, field := range contract.Ordering {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			ordering = append(ordering, trimmed)
		}
	}

	cloned := &pipelinespec.IncrementalSourceContract{
		CheckpointKind: checkpointKind,
		Field:          strings.TrimSpace(contract.Field),
		Cursor:         strings.TrimSpace(contract.Cursor),
		LookbackWindow: strings.TrimSpace(contract.LookbackWindow),
		Ordering:       ordering,
		DedupeKey:      strings.TrimSpace(contract.DedupeKey),
	}

	if cloned.CheckpointKind == nil &&
		cloned.Field == "" &&
		cloned.Cursor == "" &&
		cloned.LookbackWindow == "" &&
		len(cloned.Ordering) == 0 &&
		cloned.DedupeKey == "" {
		return nil
	}

	return cloned
}

func cloneOperationInline(operationInline map[string]any) map[string]any {
	if len(operationInline) == 0 {
		return nil
	}

	raw, err := json.Marshal(operationInline)
	if err != nil {
		return nil
	}

	var cloned map[string]any
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil
	}

	return cloned
}

func cloneSchemaContract(contract *payload.Contract) *payload.Contract {
	if contract == nil {
		return nil
	}

	cloned := payload.CloneContract(contract)
	cloned.Name = strings.TrimSpace(cloned.Name)
	for i := range cloned.Fields {
		cloned.Fields[i].Name = strings.TrimSpace(cloned.Fields[i].Name)
		cloned.Fields[i].Description = strings.TrimSpace(cloned.Fields[i].Description)
	}

	if cloned.Name == "" && len(cloned.Fields) == 0 &&
		cloned.Representation == model.ContractRepresentationUnknown &&
		cloned.RootShape == model.ContractRootShapeUnknown {
		return nil
	}

	return cloned
}

func InlineSQLFrom(operationInline map[string]any) string {
	if len(operationInline) == 0 {
		return ""
	}

	value, ok := operationInline["sql"]
	if !ok {
		return ""
	}

	text, ok := value.(string)
	if !ok {
		return ""
	}

	return strings.TrimSpace(text)
}

func cloneODCSContractRef(ref *pipelinespec.ODCSContractRef) *pipelinespec.ODCSContractRef {
	if ref == nil {
		return nil
	}
	cloned := *ref
	return &cloned
}
