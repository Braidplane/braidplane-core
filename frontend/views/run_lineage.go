package views

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/frontend/runlineage"
)

const (
	lineageUnavailableFact = "Unavailable"
	lineageStepKindQuery   = "query"
)

func lineageStatusLabel(status string) string {
	switch status {
	case runlineage.StatusPending:
		return "Pending"
	case runlineage.StatusNotReached:
		return "No recorded execution"
	case runlineage.StatusUnknown:
		return "Evidence unavailable"
	case runlineage.StatusRunning:
		return "Running"
	case runlineage.StatusSucceeded:
		return "Succeeded"
	case runlineage.StatusFailed:
		return "Failed"
	case runlineage.StatusCanceled:
		return "Canceled"
	default:
		return "Evidence unavailable"
	}
}

func lineageStatusBadgeClass(status string) string {
	switch status {
	case runlineage.StatusSucceeded:
		return "badge badge-success badge-sm gap-1"
	case runlineage.StatusFailed:
		return "badge badge-error badge-sm gap-1"
	case runlineage.StatusRunning:
		return "badge badge-info badge-sm gap-1"
	default:
		return "badge badge-ghost badge-sm gap-1"
	}
}

func lineageKindLabel(kind string) string {
	switch strings.TrimSpace(kind) {
	case lineageStepKindQuery:
		return "Source"
	case pipelineSpecStepKindTransform:
		return "Transform"
	case pipelineSpecStepKindSink:
		return "Sink"
	}

	words := strings.Fields(strings.ReplaceAll(strings.TrimSpace(kind), "_", " "))
	for index, word := range words {
		if word == "" {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + word[1:]
	}
	if len(words) == 0 {
		return "Step"
	}
	return strings.Join(words, " ")
}

func lineageKindBadgeClass(kind string) string {
	switch strings.TrimSpace(kind) {
	case lineageStepKindQuery:
		return "badge badge-primary badge-outline badge-xs"
	case pipelineSpecStepKindTransform:
		return "badge badge-info badge-outline badge-xs"
	case pipelineSpecStepKindSink:
		return "badge badge-success badge-outline badge-xs"
	default:
		return "badge badge-ghost badge-xs"
	}
}

func lineageKindRailClass(kind string) string {
	switch strings.TrimSpace(kind) {
	case lineageStepKindQuery:
		return "block h-1 bg-primary"
	case pipelineSpecStepKindTransform:
		return "block h-1 bg-info"
	case pipelineSpecStepKindSink:
		return "block h-1 bg-success"
	default:
		return "block h-1 bg-base-300"
	}
}

func lineageKindDotClass(kind string) string {
	switch strings.TrimSpace(kind) {
	case lineageStepKindQuery:
		return "h-2.5 w-2.5 rounded-full bg-primary"
	case pipelineSpecStepKindTransform:
		return "h-2.5 w-2.5 rounded-full bg-info"
	case pipelineSpecStepKindSink:
		return "h-2.5 w-2.5 rounded-full bg-success"
	default:
		return "h-2.5 w-2.5 rounded-full bg-base-300"
	}
}

func lineageKindCount(nodes []runlineage.Node, kind string) int {
	count := 0
	for _, node := range nodes {
		if node.Kind == kind {
			count++
		}
	}
	return count
}

func lineageNodeClass(node runlineage.Node) string {
	return "pipeline-graph-node group relative block h-full w-full overflow-hidden rounded-xl border text-left shadow-sm transition hover:shadow-md focus-visible:ring-2 focus-visible:ring-primary " + lineageNodeStateClass(node)
}

func lineageCompactNodeClass(node runlineage.Node) string {
	return "relative rounded-lg border px-3 py-3 " + lineageNodeStateClass(node)
}

func lineageNodeStateClass(node runlineage.Node) string {
	selection := ""
	if node.Selected {
		selection = " outline outline-2 outline-offset-2 outline-base-content/50"
	}
	switch node.Status {
	case runlineage.StatusFailed:
		return "border-error bg-base-100 hover:border-error" + selection
	case runlineage.StatusRunning:
		return "border-2 border-info bg-info/10 hover:border-info" + selection
	case runlineage.StatusSucceeded:
		return "border-success/60 bg-base-100 hover:border-success" + selection
	case runlineage.StatusPending:
		return "border-dashed border-base-300 bg-base-100 hover:border-base-content/50" + selection
	default:
		return "border-base-300 bg-base-100 hover:border-base-content/50" + selection
	}
}

func lineageEdgeClass(edge runlineage.Edge) string {
	switch edge.Tone {
	case runlineage.StatusFailed:
		return "text-error"
	case runlineage.StatusRunning:
		return "text-info"
	case runlineage.StatusSucceeded:
		return "text-success"
	default:
		return "text-base-content/25"
	}
}

func lineageEdgeLabelClass(edge runlineage.Edge) string {
	base := "absolute z-[5] max-w-20 rounded border border-base-300 bg-base-100/95 px-1.5 py-0.5 font-mono text-[10px] leading-4 text-base-content/70 shadow-sm"
	if edge.Tone == runlineage.StatusFailed {
		return base + " whitespace-normal break-words text-center border-error text-error font-semibold focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary"
	}
	if edge.Dashed {
		return base + " truncate border-dashed"
	}
	return base + " truncate"
}

func lineageStepName(node runlineage.Node) string {
	if strings.TrimSpace(node.Name) != "" {
		return strings.TrimSpace(node.Name)
	}
	return node.Slug
}

func lineageSelectedNode(view runlineage.View) *runlineage.Node {
	if view.SelectedIndex < 0 || view.SelectedIndex >= len(view.Nodes) {
		return nil
	}
	return &view.Nodes[view.SelectedIndex]
}

func lineageDurationLabel(node runlineage.Node) string {
	if node.DurationMs <= 0 {
		return "-"
	}
	return runDurationLabel(node.DurationMs)
}

func lineageOutputLabel(snapshot *api.PipelineRunStepSnapshot) string {
	if snapshot == nil || snapshot.Output == nil {
		return "Output evidence unavailable"
	}
	output := snapshot.Output
	switch runOutputDetailsKind(output) {
	case "rows":
		if details, ok := runRowsOutputDetails(output); ok {
			return strconv.Itoa(details.RowCount) + " rows"
		}
	case "documents":
		details, err := output.Details.AsPipelineRunDocumentsOutputDetails()
		if err == nil && details.DocumentCount != nil {
			return strconv.Itoa(*details.DocumentCount) + " documents"
		}
	case "http_response":
		details, err := output.Details.AsPipelineRunHTTPResponseOutputDetails()
		if err == nil {
			return "HTTP " + strconv.Itoa(details.StatusCode)
		}
	case "file_read":
		if details, ok := runFileReadOutputDetails(output); ok {
			if details.RowCount != nil {
				return strconv.Itoa(*details.RowCount) + " rows read"
			}
			if details.TargetPath != nil && strings.TrimSpace(*details.TargetPath) != "" {
				return "Read " + strings.TrimSpace(*details.TargetPath)
			}
		}
	case "file_write":
		if details, ok := runFileWriteOutputDetails(output); ok {
			if details.WrittenRows != nil {
				return strconv.Itoa(*details.WrittenRows) + " rows written"
			}
			if details.WrittenBytes != nil {
				return strconv.Itoa(*details.WrittenBytes) + " bytes written"
			}
			if strings.TrimSpace(details.TargetPath) != "" {
				return "Wrote " + strings.TrimSpace(details.TargetPath)
			}
		}
	}

	resultType := strings.TrimSpace(output.ResultType)
	vendorType := strings.TrimSpace(output.VendorType)
	switch {
	case resultType != "" && vendorType != "":
		return resultType + " / " + vendorType
	case resultType != "":
		return resultType
	case vendorType != "":
		return vendorType
	default:
		return "Output metadata unavailable"
	}
}

func lineageDependencyLabel(edge runlineage.Edge) string {
	prefix := ""
	if edge.Tone == runlineage.StatusFailed {
		prefix = "Failed input boundary: "
	}
	kinds := make([]string, 0, len(edge.Kinds))
	for _, kind := range edge.Kinds {
		switch kind {
		case "query_parameter":
			kinds = append(kinds, "parameter")
		case "step_input":
			kinds = append(kinds, "step input")
		default:
			kinds = append(kinds, strings.ReplaceAll(kind, "_", " "))
		}
	}
	if len(edge.InputNames) > 0 {
		return prefix + fmt.Sprintf("%s to %s via %s (%s)", edge.FromSlug, edge.ToSlug, strings.Join(kinds, " and "), strings.Join(edge.InputNames, ", "))
	}
	return prefix + fmt.Sprintf("%s to %s via %s", edge.FromSlug, edge.ToSlug, strings.Join(kinds, " and "))
}

func lineageNodeDependencies(node runlineage.Node) string {
	if len(node.Upstream) == 0 {
		return "Pipeline entry"
	}
	return "From " + strings.Join(node.Upstream, ", ")
}

func lineageDataSourceLabel(snapshot *api.PipelineRunStepSnapshot) string {
	dataSourceSlug := runSnapshotDataSourceSlug(snapshot)
	if dataSourceSlug == "" {
		return "-"
	}
	return dataSourceSlug
}

func lineageParameterKeysLabel(snapshot *api.PipelineRunStepSnapshot) string {
	if snapshot == nil || len(snapshot.Input.ParameterKeys) == 0 {
		return "None recorded"
	}
	return strings.Join(snapshot.Input.ParameterKeys, ", ")
}

type lineageEvidenceFact struct {
	Label string
	Value string
}

func lineagePlannedOperationLabel(planned runlineage.PlannedEvidence) string {
	operationType := strings.TrimSpace(planned.OperationType)
	operationRef := strings.TrimSpace(planned.OperationRef)
	switch {
	case operationRef != "" && planned.UsesInline:
		return lineageUnavailableFact
	case operationType != "" && operationRef != "":
		return operationType + " / " + operationRef
	case operationType != "" && planned.UsesInline:
		return operationType + " / inline"
	case operationRef != "":
		return operationRef
	case operationType != "":
		return operationType
	default:
		return lineageUnavailableFact
	}
}

func lineagePlannedInputsLabel(inputs []runlineage.PlannedInput, named bool) string {
	if len(inputs) == 0 {
		return "None"
	}
	labels := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if named && strings.TrimSpace(input.Name) != "" {
			labels = append(labels, strings.TrimSpace(input.Name)+" ← "+strings.TrimSpace(input.FromStepSlug))
			continue
		}
		labels = append(labels, strings.TrimSpace(input.FromStepSlug))
	}
	return strings.Join(labels, ", ")
}

func lineagePlannedFacts(node runlineage.Node) []lineageEvidenceFact {
	switch strings.TrimSpace(node.Kind) {
	case lineageStepKindQuery:
		parameterKeys := "None configured"
		if len(node.Planned.ParameterKeys) > 0 {
			parameterKeys = strings.Join(node.Planned.ParameterKeys, ", ")
		}
		return []lineageEvidenceFact{
			{Label: "Data source ref", Value: fallbackLineageFact(node.Planned.DataSourceRef)},
			{Label: "Operation", Value: lineagePlannedOperationLabel(node.Planned)},
			{Label: "Parameter names", Value: parameterKeys},
		}
	case pipelineSpecStepKindTransform:
		return []lineageEvidenceFact{
			{Label: "Input bindings", Value: lineagePlannedInputsLabel(node.Planned.Inputs, true)},
			{Label: "Output kind", Value: fallbackLineageFact(node.Planned.OutputKind)},
		}
	case pipelineSpecStepKindSink:
		return []lineageEvidenceFact{
			{Label: "Input", Value: lineagePlannedInputsLabel(node.Planned.Inputs, false)},
			{Label: "Destination ref", Value: fallbackLineageFact(node.Planned.DataSourceRef)},
			{Label: "Operation", Value: lineagePlannedOperationLabel(node.Planned)},
		}
	default:
		return []lineageEvidenceFact{{Label: "Configuration", Value: lineageUnavailableFact}}
	}
}

func lineageObservedFacts(node runlineage.Node) []lineageEvidenceFact {
	facts := []lineageEvidenceFact{}
	if node.StartedAt != nil && node.EndedAt == nil {
		facts = append(facts, lineageEvidenceFact{Label: "Completion", Value: "Started; no completion recorded"})
	}
	if node.Snapshot == nil {
		return append(facts, lineageEvidenceFact{Label: "Run evidence", Value: "No persisted snapshot"})
	}
	if dataSource := lineageDataSourceLabel(node.Snapshot); dataSource != "-" {
		facts = append(facts, lineageEvidenceFact{Label: "Resolved data source", Value: dataSource})
	}
	if operation := runOperationLabel(node.Snapshot); operation != "-" {
		facts = append(facts, lineageEvidenceFact{Label: "Operation", Value: operation})
	}
	if len(node.Snapshot.Input.ParameterKeys) > 0 {
		facts = append(facts, lineageEvidenceFact{Label: "Parameter names", Value: lineageParameterKeysLabel(node.Snapshot)})
	}
	facts = append(facts, lineageEvidenceFact{Label: "Output", Value: lineageOutputLabel(node.Snapshot)})
	return facts
}

func fallbackLineageFact(value string) string {
	if strings.TrimSpace(value) == "" {
		return lineageUnavailableFact
	}
	return strings.TrimSpace(value)
}

func lineageEvidenceSummary(facts []lineageEvidenceFact) string {
	parts := make([]string, 0, len(facts))
	for _, fact := range facts {
		parts = append(parts, fact.Label+": "+fact.Value)
	}
	return strings.Join(parts, " · ")
}

func lineagePlannedSummary(node runlineage.Node) string {
	return lineageEvidenceSummary(lineagePlannedFacts(node))
}

func lineageObservedSummary(node runlineage.Node) string {
	return lineageEvidenceSummary(lineageObservedFacts(node))
}

func lineagePayloadPreview(node runlineage.Node) string {
	return "Planned · " + lineagePlannedSummary(node) + " · Observed · " + lineageObservedSummary(node)
}

func lineageNodeAriaLabel(node runlineage.Node) string {
	return fmt.Sprintf(
		"%s step %s, %s. %s. Select for full evidence.",
		lineageKindLabel(node.Kind),
		lineageStepName(node),
		lineageNodeStatusLabel(node),
		lineagePayloadPreview(node),
	)
}

func lineageNodeStatusLabel(node runlineage.Node) string {
	if node.Blocked {
		return "Blocked"
	}
	return lineageStatusLabel(node.Status)
}

// RunStepActions links recorded identities to separately authorized current resources.
type RunStepActions struct {
	PipelineHref   string
	DataSourceHref string
	QueryHref      string
	Message        string
}
type runStepActionsKey struct{}

func WithRunStepActions(ctx context.Context, actions RunStepActions) context.Context {
	return context.WithValue(ctx, runStepActionsKey{}, actions)
}

func runStepActions(ctx context.Context) RunStepActions {
	actions, _ := ctx.Value(runStepActionsKey{}).(RunStepActions)
	return actions
}

func lineagePortLabel(node runlineage.Node, port string) string {
	if node.Boundary == port {
		return "Failed " + port + " boundary at " + node.Slug
	}
	return port + " port at " + node.Slug
}

func lineagePortClass(node runlineage.Node, port string) string {
	base := "pointer-events-none absolute top-1/2 z-20 flex h-4 w-4 -translate-y-1/2 items-center justify-center rounded-full border-2 bg-base-100 text-[7px] "
	if port == "input" {
		base += "-left-2 "
	} else {
		base += "-right-2 "
	}
	if node.Boundary == port {
		return base + "border-error text-error"
	}
	if port == "input" {
		return base + "border-success text-success"
	}
	return base + "border-primary text-primary"
}

func lineageDefinitionActionLabel(node runlineage.Node) string {
	if node.Failure != nil {
		if node.Failure.ReasonCode != nil && *node.Failure.ReasonCode == api.S3ObjectWriteFailed {
			return "Review executed S3 write"
		}
		if node.Boundary == "output" {
			return "Compare executed output contract"
		}
	}
	return "Inspect executed step & contract"
}
