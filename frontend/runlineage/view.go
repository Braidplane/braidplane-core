// Package runlineage projects an immutable pipeline version and persisted run
// facts into the read-only lineage shown on run detail surfaces.
package runlineage

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	pipelinegraph "github.com/Braidplane/braidplane-core/pkg/pipeline/graph"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	StatusPending    = "pending"
	StatusNotReached = "not_reached"
	StatusUnknown    = "unknown"
	StatusRunning    = "running"
	StatusSucceeded  = "succeeded"
	StatusFailed     = "failed"
	StatusCanceled   = "canceled"

	nodeWidth  = 208
	nodeHeight = 116
	columnGap  = 92
	rowGap     = 32
	paddingX   = 28
	paddingY   = 44
)

// View is the complete, secret-safe projection used by run lineage templates.
type View struct {
	Available        bool
	Message          string
	VersionHash      string
	VersionShortHash string
	Nodes            []Node
	Edges            []Edge
	SelectedIndex    int
	CanvasWidth      int
	CanvasHeight     int
}

// Node combines one exact-version pipeline step with persisted facts for that
// step. Planned steps with no facts remain explicitly marked not reached.
type Node struct {
	Blocked      bool
	Slug         string
	Name         string
	Kind         string
	Planned      PlannedEvidence
	Status       string
	Href         string
	X            int
	Y            int
	DurationMs   int64
	StartedAt    *time.Time
	EndedAt      *time.Time
	Snapshot     *api.PipelineRunStepSnapshot
	FailedCheck  *api.PipelineRunStructuralCheck
	Boundary     string
	FailureInput string
	FailureFrom  string
	Failure      *api.PipelineRunFailureContext
	Incremental  *api.PipelineRunIncrementalSourceDiagnostic
	Upstream     []string
	Downstream   []string
	Selected     bool
}

// PlannedEvidence is the secret-safe step configuration from the exact
// immutable pipeline version executed by the run.
type PlannedEvidence struct {
	DataSourceRef string
	OperationType string
	OperationRef  string
	UsesInline    bool
	Inputs        []PlannedInput
	OutputKind    string
	ParameterKeys []string
}

// PlannedInput identifies one named upstream binding without its payload.
type PlannedInput struct {
	Name         string
	FromStepSlug string
}

// Edge is one normalized dependency between two lineage nodes.
type Edge struct {
	Href        string
	FromSlug    string
	ToSlug      string
	Kinds       []string
	InputNames  []string
	Tone        string
	Dashed      bool
	Path        string
	ArrowPoints string
	Label       string
	LabelX      int
	LabelY      int
}

// Unavailable returns a nonfatal lineage state. Run facts outside the graph
// remain usable when the exact historical topology cannot be loaded.
func Unavailable(message string) View {
	return View{Message: strings.TrimSpace(message), SelectedIndex: -1}
}

// Build derives lineage from the exact immutable version recorded on the run.
// It intentionally does not validate the historical spec against today's full
// authoring schema; graph normalization supplies the topology contract needed
// by this read-only projection.
func Build(version api.PipelineVersion, run api.PipelineRunRecord, requestedStepSlug, baseHref string) (View, error) {
	var pipeline pipelinespec.Pipeline
	encodedSpec, err := json.Marshal(version.Spec)
	if err != nil {
		return View{}, xerror.Unexpected().With(err).Detail("encode pipeline version")
	}
	if err := json.Unmarshal(encodedSpec, &pipeline); err != nil {
		return View{}, xerror.Unexpected().With(err).Detail("decode pipeline version")
	}

	normalized, err := pipelinegraph.Normalize(&pipeline)
	if err != nil {
		return View{}, xerror.Unexpected().With(err).Detail("normalize pipeline version")
	}

	steps := normalized.TopologicalSteps()
	view := View{
		Available:        true,
		VersionHash:      version.SpecHash,
		VersionShortHash: version.ShortSpecHash,
		Nodes:            make([]Node, 0, len(steps)),
		SelectedIndex:    -1,
	}

	nodeIndex := make(map[string]int, len(steps))
	for _, step := range steps {
		stepSlug := step.Slug.String()
		nodeIndex[stepSlug] = len(view.Nodes)
		view.Nodes = append(view.Nodes, Node{
			Slug:    stepSlug,
			Name:    strings.TrimSpace(step.Name),
			Kind:    step.Kind,
			Planned: plannedEvidence(step),
			Status:  initialStatus(run.Metadata.Status),
			Href:    stepHref(baseHref, stepSlug, version.SpecHash),
		})
	}

	applyRunFacts(&view, run, nodeIndex)
	layout(&view, normalized, nodeIndex)
	for index := range view.Nodes {
		node := &view.Nodes[index]
		if node.Status != StatusNotReached {
			continue
		}
		for _, upstream := range node.Upstream {
			parent := view.Nodes[nodeIndex[upstream]]
			if parent.Status == StatusFailed || parent.Blocked {
				node.Blocked = true
			}
		}
	}
	baseURL, _ := url.Parse(baseHref)
	if requestedStepSlug != "" || baseURL == nil || baseURL.Query().Get("definition_scope") != "whole" {
		selectNode(&view, run, requestedStepSlug)
	}

	return view, nil
}

func applyRunFacts(view *View, run api.PipelineRunRecord, nodeIndex map[string]int) {
	for _, event := range ChronologicalEvents(run.Timeline.Events) {
		if event.Step == nil {
			continue
		}
		index, ok := nodeIndex[*event.Step]
		if !ok {
			continue
		}

		node := &view.Nodes[index]
		switch strings.TrimSpace(event.Type) {
		case "step_started":
			node.Status = StatusRunning
			if node.StartedAt == nil {
				startedAt := event.At
				node.StartedAt = &startedAt
			}
		case "step_succeeded":
			node.Status = StatusSucceeded
			endedAt := event.At
			node.EndedAt = &endedAt
		case "step_failed":
			node.Status = StatusFailed
			endedAt := event.At
			node.EndedAt = &endedAt
		}
	}

	for snapshotIndex := range run.Timeline.Snapshots {
		snapshot := &run.Timeline.Snapshots[snapshotIndex]
		index, ok := nodeIndex[snapshot.Step]
		if !ok {
			continue
		}
		node := &view.Nodes[index]
		node.Snapshot = snapshot
		if snapshot.Output != nil {
			node.DurationMs = snapshot.Output.DurationMs
		}
		if node.Status == StatusPending || node.Status == StatusNotReached {
			node.Status = StatusUnknown
		}
	}

	if run.Timeline.Failure != nil {
		if index, ok := nodeIndex[run.Timeline.Failure.Step]; ok {
			node := &view.Nodes[index]
			node.Status = StatusFailed
			node.Failure = run.Timeline.Failure
			if node.EndedAt == nil {
				endedAt := run.Timeline.Failure.At
				node.EndedAt = &endedAt
			}
		}
	}

	if run.Timeline.StructuralChecks != nil {
		for _, check := range *run.Timeline.StructuralChecks {
			index, ok := nodeIndex[check.Step]
			if !ok || check.Status != api.PipelineRunStructuralCheckStatusFailed {
				continue
			}
			node := &view.Nodes[index]
			node.FailedCheck = &check
			switch check.Phase {
			case api.PipelineRunStructuralCheckPhaseProducerOutput:
				node.Boundary = "output"
			case api.PipelineRunStructuralCheckPhaseNormalizedInput:
				node.Boundary = "input"
				if check.Input != nil {
					node.FailureInput = *check.Input
					for _, input := range node.Planned.Inputs {
						if input.Name == *check.Input && (check.Target == nil || *check.Target == input.FromStepSlug) {
							node.FailureFrom = input.FromStepSlug
						}
					}
				}
			case api.PipelineRunStructuralCheckPhaseEventAdmission:
				node.Boundary = "output"
			case api.PipelineRunStructuralCheckPhasePreSink:
				node.Boundary = "input"
				if node.Kind == "sink" && len(node.Planned.Inputs) == 1 {
					node.FailureFrom = node.Planned.Inputs[0].FromStepSlug
				}

			}
		}
	}
	for index := range view.Nodes {
		node := &view.Nodes[index]
		if node.Boundary == "" && node.Failure != nil && node.Failure.ReasonCode != nil && *node.Failure.ReasonCode == "source_result_shape_mismatch" {
			node.Boundary = "output"
		}
	}

	for diagnosticIndex := range run.IncrementalDiagnostics.Sources {
		diagnostic := &run.IncrementalDiagnostics.Sources[diagnosticIndex]
		if index, ok := nodeIndex[diagnostic.SourceStepSlug]; ok {
			view.Nodes[index].Incremental = diagnostic
		}
	}

	for index := range view.Nodes {
		node := &view.Nodes[index]
		if node.DurationMs == 0 && node.StartedAt != nil && node.EndedAt != nil {
			node.DurationMs = node.EndedAt.Sub(*node.StartedAt).Milliseconds()
		}
		if node.Status == StatusRunning && run.Metadata.Status == api.PipelineRunMetadataStatusCanceled {
			node.Status = StatusCanceled
		} else if node.Status == StatusRunning && run.Metadata.Status != api.PipelineRunMetadataStatusRunning {
			node.Status = StatusUnknown
		}
	}

	if run.Metadata.Status == api.PipelineRunMetadataStatusRunning {
		currentStepSlug := strings.TrimSpace(run.Metadata.StepSlug)
		if index, ok := nodeIndex[currentStepSlug]; ok && view.Nodes[index].Status == StatusPending {
			view.Nodes[index].Status = StatusRunning
		}
	}
}

func layout(view *View, normalized *pipelinegraph.Graph, nodeIndex map[string]int) {
	rank := make(map[string]int, len(view.Nodes))
	for _, edge := range normalized.Edges() {
		fromSlug := edge.FromStepSlug.String()
		toSlug := edge.ToStepSlug.String()
		candidate := rank[fromSlug] + 1
		if candidate > rank[toSlug] {
			rank[toSlug] = candidate
		}
	}

	byRank := make(map[int][]int)
	maxRank := 0
	maxRows := 1
	for index := range view.Nodes {
		nodeRank := rank[view.Nodes[index].Slug]
		byRank[nodeRank] = append(byRank[nodeRank], index)
		maxRank = max(maxRank, nodeRank)
		maxRows = max(maxRows, len(byRank[nodeRank]))
	}

	contentHeight := maxRows*nodeHeight + (maxRows-1)*rowGap
	view.CanvasWidth = max(720, paddingX*2+(maxRank+1)*nodeWidth+maxRank*columnGap)
	view.CanvasHeight = max(260, paddingY*2+contentHeight)

	for nodeRank := 0; nodeRank <= maxRank; nodeRank++ {
		indexes := byRank[nodeRank]
		columnHeight := len(indexes)*nodeHeight + max(0, len(indexes)-1)*rowGap
		columnTop := paddingY + (contentHeight-columnHeight)/2
		for row, index := range indexes {
			view.Nodes[index].X = paddingX + nodeRank*(nodeWidth+columnGap)
			view.Nodes[index].Y = columnTop + row*(nodeHeight+rowGap)
		}
	}

	type edgeKey struct {
		from string
		to   string
	}
	edgeGroups := make(map[edgeKey][]pipelinegraph.Edge, len(normalized.Edges()))
	edgeKeys := make([]edgeKey, 0, len(normalized.Edges()))
	for _, normalizedEdge := range normalized.Edges() {
		key := edgeKey{from: normalizedEdge.FromStepSlug.String(), to: normalizedEdge.ToStepSlug.String()}
		if _, exists := edgeGroups[key]; !exists {
			edgeKeys = append(edgeKeys, key)
		}
		edgeGroups[key] = append(edgeGroups[key], normalizedEdge)
	}

	for _, key := range edgeKeys {
		normalizedEdges := edgeGroups[key]
		normalizedEdge := normalizedEdges[0]
		fromIndex, fromOK := nodeIndex[normalizedEdge.FromStepSlug.String()]
		toIndex, toOK := nodeIndex[normalizedEdge.ToStepSlug.String()]
		if !fromOK || !toOK {
			continue
		}
		from := &view.Nodes[fromIndex]
		to := &view.Nodes[toIndex]
		from.Downstream = append(from.Downstream, to.Slug)
		to.Upstream = append(to.Upstream, from.Slug)

		x1 := from.X + nodeWidth
		y1 := from.Y + nodeHeight/2
		x2 := to.X
		y2 := to.Y + nodeHeight/2
		midX := x1 + (x2-x1)/2
		kinds := make([]string, 0, len(normalizedEdges))
		inputNames := make([]string, 0, len(normalizedEdges))
		queryParameterOnly := true
		for _, edge := range normalizedEdges {
			kind := string(edge.Kind)
			if !contains(kinds, kind) {
				kinds = append(kinds, kind)
			}
			if edge.InputName != "" {
				inputNames = append(inputNames, edge.InputName)
			}
			if edge.Kind != pipelinegraph.EdgeKindQueryParameter {
				queryParameterOnly = false
			}
		}
		sort.Strings(kinds)
		sort.Strings(inputNames)
		if queryParameterOnly && len(inputNames) == 0 {
			inputNames = queryParameterNames(normalized, normalizedEdge)
		}
		tone := edgeTone(from.Status, to.Status)
		if to.Boundary == "input" && to.FailureFrom == from.Slug && !queryParameterOnly {
			tone = StatusFailed
		}
		label := edgeLabel(queryParameterOnly, inputNames)
		if tone == StatusFailed {
			label += " · Failed input"
		}
		view.Edges = append(view.Edges, Edge{
			Href:        to.Href,
			FromSlug:    from.Slug,
			ToSlug:      to.Slug,
			Kinds:       kinds,
			InputNames:  inputNames,
			Tone:        tone,
			Dashed:      queryParameterOnly,
			Path:        fmt.Sprintf("M %d %d C %d %d, %d %d, %d %d", x1, y1, midX, y1, midX, y2, x2, y2),
			ArrowPoints: fmt.Sprintf("%d,%d %d,%d %d,%d", x2, y2, x2-8, y2-5, x2-8, y2+5),
			Label:       label,
			LabelX:      midX,
			LabelY:      (y1+y2)/2 - 12,
		})
	}

	for index := range view.Nodes {
		sort.Strings(view.Nodes[index].Upstream)
		sort.Strings(view.Nodes[index].Downstream)
	}
}

func queryParameterNames(normalized *pipelinegraph.Graph, edge pipelinegraph.Edge) []string {
	step, ok := normalized.Step(edge.ToStepSlug)
	if !ok || step.Query == nil {
		return nil
	}

	names := make([]string, 0, len(step.Query.Parameters))
	for name, value := range step.Query.Parameters {
		if valueReferencesStep(value, edge.FromStepSlug.String()) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func valueReferencesStep(value any, stepSlug string) bool {
	switch typed := value.(type) {
	case string:
		return strings.HasPrefix(typed, "$steps."+stepSlug+".")
	case map[string]any:
		for _, nested := range typed {
			if valueReferencesStep(nested, stepSlug) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if valueReferencesStep(nested, stepSlug) {
				return true
			}
		}
	}
	return false
}

func plannedEvidence(step pipelinespec.Step) PlannedEvidence {
	switch step.Kind {
	case "query":
		if step.Query == nil {
			return PlannedEvidence{}
		}
		parameterKeys := make([]string, 0, len(step.Query.Parameters))
		for parameterKey := range step.Query.Parameters {
			parameterKeys = append(parameterKeys, parameterKey)
		}
		sort.Strings(parameterKeys)
		return PlannedEvidence{
			DataSourceRef: step.Query.DataSourceSlug.String(),
			OperationType: strings.TrimSpace(step.Query.OperationType),
			OperationRef:  step.Query.OperationRef.String(),
			UsesInline:    len(step.Query.OperationInline) > 0,
			ParameterKeys: parameterKeys,
		}
	case "transform":
		if step.Transform == nil {
			return PlannedEvidence{}
		}
		inputNames := make([]string, 0, len(step.Transform.Inputs))
		for inputName := range step.Transform.Inputs {
			inputNames = append(inputNames, inputName)
		}
		sort.Strings(inputNames)
		inputs := make([]PlannedInput, 0, len(inputNames))
		for _, inputName := range inputNames {
			inputs = append(inputs, PlannedInput{
				Name:         inputName,
				FromStepSlug: step.Transform.Inputs[inputName].FromStep.String(),
			})
		}
		outputKind := step.Transform.OutputKind.String()
		if outputKind == "unknown" {
			outputKind = ""
		}
		return PlannedEvidence{
			Inputs:     inputs,
			OutputKind: outputKind,
		}
	case "sink":
		if step.Sink == nil {
			return PlannedEvidence{}
		}
		inputs := make([]PlannedInput, 0, 1)
		if step.Sink.Input != nil && step.Sink.Input.FromStep != "" {
			inputs = append(inputs, PlannedInput{
				Name:         "input",
				FromStepSlug: step.Sink.Input.FromStep.String(),
			})
		}
		return PlannedEvidence{
			DataSourceRef: step.Sink.DataSourceSlug.String(),
			OperationType: strings.TrimSpace(step.Sink.OperationType),
			OperationRef:  step.Sink.OperationRef.String(),
			UsesInline:    len(step.Sink.OperationInline) > 0,
			Inputs:        inputs,
		}
	default:
		return PlannedEvidence{}
	}
}

func edgeLabel(queryParameterOnly bool, inputNames []string) string {
	if queryParameterOnly {
		if len(inputNames) > 0 {
			return "param: " + strings.Join(inputNames, ", ")
		}
		return "parameter"
	}
	if len(inputNames) > 0 {
		return strings.Join(inputNames, ", ")
	}
	return "payload"
}

func initialStatus(runStatus api.PipelineRunMetadataStatus) string {
	switch runStatus {
	case api.PipelineRunMetadataStatusQueued, api.PipelineRunMetadataStatusRunning:
		return StatusPending
	case api.PipelineRunMetadataStatusSucceeded:
		return StatusUnknown
	default:
		return StatusNotReached
	}
}

func selectNode(view *View, run api.PipelineRunRecord, requestedStepSlug string) {
	candidates := []string{strings.TrimSpace(requestedStepSlug)}
	if strings.TrimSpace(requestedStepSlug) != "" {
		for index := range view.Nodes {
			if view.Nodes[index].Slug == requestedStepSlug {
				view.Nodes[index].Selected = true
				view.SelectedIndex = index
				return
			}
		}
		return
	}
	if run.Timeline.Failure != nil {
		candidates = append(candidates, run.Timeline.Failure.Step)
	}
	for _, node := range view.Nodes {
		if node.Status == StatusRunning {
			candidates = append(candidates, node.Slug)
			break
		}
	}
	candidates = append(candidates, strings.TrimSpace(run.Metadata.StepSlug))
	for index := len(view.Nodes) - 1; index >= 0; index-- {
		if view.Nodes[index].Status == StatusSucceeded {
			candidates = append(candidates, view.Nodes[index].Slug)
			break
		}
	}
	if len(view.Nodes) > 0 {
		candidates = append(candidates, view.Nodes[0].Slug)
	}

	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		for index := range view.Nodes {
			if view.Nodes[index].Slug == candidate {
				view.Nodes[index].Selected = true
				view.SelectedIndex = index
				return
			}
		}
	}
}

func stepHref(baseHref, stepSlug, specHash string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseHref))
	if err != nil {
		return baseHref
	}
	query := url.Values{}
	for _, key := range []string{"environment_slug", "definition", "spec_hash", "definition_view", "panel", "list_return"} {
		if value := parsed.Query().Get(key); value != "" {
			query.Set(key, value)
		}
	}
	query.Set("spec_hash", specHash)
	query.Set("definition", "executed")
	query.Set("lineage_step", stepSlug)
	query.Set("section", "steps")
	parsed.RawQuery = query.Encode()
	parsed.Fragment = "lineage-step-evidence"
	return parsed.String()
}

func edgeTone(fromStatus, toStatus string) string {
	switch {
	case toStatus == StatusRunning:
		return StatusRunning
	case fromStatus == StatusSucceeded && toStatus == StatusSucceeded:
		return StatusSucceeded
	default:
		return StatusNotReached
	}
}

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// ChronologicalEvents orders original instants without mutating retained API evidence.
func ChronologicalEvents(events []api.PipelineRunTimelineEvent) []api.PipelineRunTimelineEvent {
	ordered := append([]api.PipelineRunTimelineEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })
	return ordered
}
