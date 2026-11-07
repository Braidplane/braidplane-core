// Package format owns canonical pipeline spec formatting for editor, builder,
// preview, and CLI workflows.
package format

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	diagnosticUnresolvedStepReference = "unresolved_step_reference"
	refPrefix                         = "$steps."
	formatJSON                        = "json"
	formatYAML                        = "yaml"
)

// Diagnostic reports formatter-only issues that affected canonical ordering.
type Diagnostic struct {
	Code               string `json:"code"`
	Message            string `json:"message"`
	StepSlug           string `json:"step_slug"`
	ReferencedStepSlug string `json:"referenced_step_slug"`
}

// Result is the formatted pipeline spec plus non-fatal formatter diagnostics.
type Result struct {
	Spec        pipelinespec.Pipeline `json:"spec"`
	Diagnostics []Diagnostic          `json:"diagnostics"`
}

// Format returns a copy of the pipeline with steps ordered for stable visual layout.
// It intentionally does not call pipeline validation: formatting must tolerate
// draft-like specs and report only diagnostics for references that affect
// ordering.
func Format(pipeline *pipelinespec.Pipeline) (Result, error) {
	if pipeline == nil {
		return Result{}, xerror.InvalidRequired(xerror.Pipeline)
	}

	spec := clonePipeline(pipeline)
	formatter := newFormatter(spec.Steps)
	spec.Steps = formatter.orderedSteps()

	return Result{
		Spec:        spec,
		Diagnostics: formatter.diagnostics,
	}, nil
}

// FormatJSON returns canonical indented JSON for the formatted pipeline spec.
func FormatJSON(pipeline *pipelinespec.Pipeline) ([]byte, []Diagnostic, error) {
	result, err := Format(pipeline)
	if err != nil {
		return nil, nil, err
	}

	payload, err := json.MarshalIndent(result.Spec, "", "  ")
	if err != nil {
		return nil, nil, xerror.InvalidInput(xerror.JSON).With(err)
	}

	return payload, result.Diagnostics, nil
}

// FormatData parses serialized JSON or YAML pipeline data using the explicit
// format requested by the caller and emits formatted data in the same format.
func FormatData(format string, data []byte) ([]byte, []Diagnostic, error) {
	normalizedFormat := strings.ToLower(strings.TrimSpace(format))
	pipeline, err := loadFormatPipeline(normalizedFormat, data)
	if err != nil {
		var xe *xerror.Error
		if xerror.As(err, &xe) {
			return nil, nil, err
		}
		return nil, nil, xerror.InvalidInput(xerror.Pipeline).With(err)
	}

	result, err := Format(pipeline)
	if err != nil {
		return nil, nil, err
	}

	switch normalizedFormat {
	case formatJSON:
		payload, err := json.MarshalIndent(result.Spec, "", "  ")
		if err != nil {
			return nil, nil, xerror.InvalidInput(xerror.JSON).With(err)
		}
		return payload, result.Diagnostics, nil
	case formatYAML:
		payload, err := yaml.Marshal(result.Spec)
		if err != nil {
			return nil, nil, xerror.InvalidInput(xerror.JSON).With(err)
		}
		return payload, result.Diagnostics, nil
	default:
		return nil, nil, pipelinespec.ErrUnsupportedFormat.Detailf("%q", format)
	}
}

func loadFormatPipeline(format string, data []byte) (*pipelinespec.Pipeline, error) {
	switch format {
	case formatJSON:
		var pipeline pipelinespec.Pipeline
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if err := decoder.Decode(&pipeline); err != nil {
			return nil, err
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				return nil, xerror.InvalidInput(xerror.Pipeline).Detailf("multiple JSON values")
			}
			return nil, err
		}
		return &pipeline, nil
	case formatYAML:
		if err := rejectExtraYAMLDocuments(data); err != nil {
			return nil, err
		}
		return pipelinespec.Load("pipeline.pipeline."+formatYAML, data)
	default:
		return nil, pipelinespec.ErrUnsupportedFormat.Detailf("%q", format)
	}
}

func rejectExtraYAMLDocuments(data []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var first yaml.Node
	if err := decoder.Decode(&first); err != nil {
		return err
	}

	var second yaml.Node
	if err := decoder.Decode(&second); !xerror.Is(err, io.EOF) {
		if err == nil {
			return xerror.InvalidInput(xerror.Pipeline).Detailf("multiple YAML documents")
		}
		return err
	}
	return nil
}

func clonePipeline(pipeline *pipelinespec.Pipeline) pipelinespec.Pipeline {
	spec := *pipeline
	spec.Steps = append([]pipelinespec.Step(nil), pipeline.Steps...)
	return spec
}

type formatter struct {
	steps       []pipelinespec.Step
	indexBySlug map[string]int
	edges       []formatEdge
	diagnostics []Diagnostic
}

type formatEdge struct {
	from string
	to   string
}

type sortableStep struct {
	step          pipelinespec.Step
	originalIndex int
}

func newFormatter(steps []pipelinespec.Step) *formatter {
	f := &formatter{
		steps:       steps,
		indexBySlug: make(map[string]int, len(steps)),
		edges:       make([]formatEdge, 0, len(steps)*2),
	}

	for index, step := range steps {
		if _, exists := f.indexBySlug[step.Slug.String()]; !exists {
			f.indexBySlug[step.Slug.String()] = index
		}
	}
	for _, step := range steps {
		f.collectStepEdges(step)
	}

	return f
}

func (f *formatter) orderedSteps() []pipelinespec.Step {
	knownKinds := map[string]struct{}{"input": {}, "query": {}, "transform": {}, "sink": {}}
	items := make([]sortableStep, 0, len(f.steps))
	for index, step := range f.steps {
		if _, ok := knownKinds[step.Kind]; !ok {
			continue
		}
		items = append(items, sortableStep{step: step, originalIndex: index})
	}

	ordered := stepsFromSortable(f.orderKnownSteps(items))
	for _, step := range f.steps {
		if _, ok := knownKinds[step.Kind]; ok {
			continue
		}
		ordered = append(ordered, step)
	}

	return ordered
}

func (f *formatter) orderKnownSteps(items []sortableStep) []sortableStep {
	// Use the spec-local DAG as the hard constraint, then use visual kind bands
	// and producer positions only as tie-breakers among steps that are ready.
	// This keeps query -> transform -> sink layouts where possible without
	// moving a downstream query ahead of the transform it reads from.
	itemBySlug := make(map[string]sortableStep, len(items))
	indegree := make(map[string]int, len(items))
	outgoing := make(map[string][]string, len(items))
	for _, item := range items {
		itemBySlug[item.step.Slug.String()] = item
		indegree[item.step.Slug.String()] = 0
	}
	for _, edge := range f.edges {
		if _, ok := itemBySlug[edge.from]; !ok {
			continue
		}
		if _, ok := itemBySlug[edge.to]; !ok {
			continue
		}
		indegree[edge.to]++
		outgoing[edge.from] = append(outgoing[edge.from], edge.to)
	}

	placedOrder := make(map[string]int, len(items))
	ready := make([]sortableStep, 0, len(items))
	for _, item := range items {
		if indegree[item.step.Slug.String()] == 0 {
			ready = append(ready, item)
		}
	}
	f.sortReadySteps(ready, placedOrder)

	ordered := make([]sortableStep, 0, len(items))
	for len(ready) > 0 {
		current := ready[0]
		ready = ready[1:]
		ordered = append(ordered, current)
		placedOrder[current.step.Slug.String()] = len(ordered) - 1

		next := outgoing[current.step.Slug.String()]
		sort.Slice(next, func(i, j int) bool {
			return f.lessReadyStep(itemBySlug[next[i]], itemBySlug[next[j]], placedOrder)
		})
		for _, nextSlug := range next {
			indegree[nextSlug]--
			if indegree[nextSlug] == 0 {
				ready = append(ready, itemBySlug[nextSlug])
				f.sortReadySteps(ready, placedOrder)
			}
		}
	}

	if len(ordered) != len(items) {
		// Cycles belong to validation. Formatting falls back to deterministic
		// visual order instead of rejecting draft specs.
		items = append([]sortableStep(nil), items...)
		sortSortableSteps(items)
		return items
	}

	return ordered
}

func sortSortableSteps(items []sortableStep) {
	sort.SliceStable(items, func(i, j int) bool {
		return lessSortableStep(items[i], items[j])
	})
}

func (f *formatter) sortReadySteps(items []sortableStep, placedOrder map[string]int) {
	sort.SliceStable(items, func(i, j int) bool {
		return f.lessReadyStep(items[i], items[j], placedOrder)
	})
}

func (f *formatter) lessReadyStep(left, right sortableStep, placedOrder map[string]int) bool {
	leftKindRank := kindRank(left.step.Kind)
	rightKindRank := kindRank(right.step.Kind)
	if leftKindRank != rightKindRank {
		return leftKindRank < rightKindRank
	}

	leftUpstream, leftHasUpstream := f.placedUpstreamRank(left.step.Slug, placedOrder)
	rightUpstream, rightHasUpstream := f.placedUpstreamRank(right.step.Slug, placedOrder)
	if leftHasUpstream != rightHasUpstream {
		return leftHasUpstream
	}
	if leftHasUpstream && leftUpstream != rightUpstream {
		return leftUpstream < rightUpstream
	}

	return lessSortableStep(left, right)
}

func kindRank(kind string) int {
	switch kind {
	case "input":
		return 0
	case "query":
		return 1
	case "transform":
		return 2
	case "sink":
		return 3
	default:
		return 4
	}
}

func lessSortableStep(left, right sortableStep) bool {
	if left.originalIndex != right.originalIndex {
		return left.originalIndex < right.originalIndex
	}
	return left.step.Slug.String() < right.step.Slug.String()
}

func (f *formatter) placedUpstreamRank(stepSlug slug.Slug, placedOrder map[string]int) (float64, bool) {
	indexes := make([]int, 0)
	for _, edge := range f.edges {
		if edge.to != stepSlug.String() {
			continue
		}
		index, ok := placedOrder[edge.from]
		if !ok {
			continue
		}
		indexes = append(indexes, index)
	}
	if len(indexes) == 0 {
		return 0, false
	}
	sort.Ints(indexes)
	return average(indexes), true
}

func (f *formatter) collectStepEdges(step pipelinespec.Step) {
	switch {
	case step.Transform != nil:
		inputs := sortedTransformInputs(step.Transform)
		for _, input := range inputs {
			f.addEdge(step.Slug, input.fromStep.String())
		}
	case step.Sink != nil && step.Sink.Input != nil:
		f.addEdge(step.Slug, step.Sink.Input.FromStep.String())
	}

	if step.Query != nil {
		for _, referencedStepSlug := range collectReferencedStepSlugs(step.Query.Parameters) {
			f.addEdge(step.Slug, referencedStepSlug)
		}
	}
}

func (f *formatter) addEdge(toStepSlug slug.Slug, fromStepSlug string) {
	if fromStepSlug == "" {
		return
	}
	if _, ok := f.indexBySlug[fromStepSlug]; !ok {
		// Missing references affect ordering but should not make formatting a
		// second runtime validator. Surface them as diagnostics and keep going.
		f.diagnostics = append(f.diagnostics, Diagnostic{
			Code:               diagnosticUnresolvedStepReference,
			Message:            "step reference could not be resolved while formatting the pipeline spec",
			StepSlug:           toStepSlug.String(),
			ReferencedStepSlug: fromStepSlug,
		})
		return
	}
	f.edges = append(f.edges, formatEdge{from: fromStepSlug, to: toStepSlug.String()})
}

type namedStepInput struct {
	name     string
	fromStep slug.Slug
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
		inputs = append(inputs, namedStepInput{name: name, fromStep: transform.Inputs[name].FromStep})
	}
	return inputs
}

func collectReferencedStepSlugs(params map[string]any) []string {
	seen := make(map[string]struct{})
	for _, value := range params {
		collectReferencedStepSlugsFromValue(value, seen)
	}

	references := make([]string, 0, len(seen))
	for reference := range seen {
		references = append(references, reference)
	}
	sort.Strings(references)
	return references
}

func collectReferencedStepSlugsFromValue(value any, seen map[string]struct{}) {
	s, ok := value.(string)
	if ok && strings.HasPrefix(s, refPrefix) {
		rest := strings.TrimPrefix(s, refPrefix)
		dotIdx := strings.Index(rest, ".")
		if dotIdx > 0 {
			seen[rest[:dotIdx]] = struct{}{}
		}
		return
	}

	switch typed := value.(type) {
	case map[string]any:
		for _, nested := range typed {
			collectReferencedStepSlugsFromValue(nested, seen)
		}
	case []any:
		for _, nested := range typed {
			collectReferencedStepSlugsFromValue(nested, seen)
		}
	}
}

func stepsFromSortable(items []sortableStep) []pipelinespec.Step {
	steps := make([]pipelinespec.Step, 0, len(items))
	for _, item := range items {
		steps = append(steps, item.step)
	}
	return steps
}

func average(values []int) float64 {
	var total int
	for _, value := range values {
		total += value
	}
	return float64(total) / float64(len(values))
}
