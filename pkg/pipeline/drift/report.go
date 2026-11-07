package drift

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/pipeline/diff"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
)

var stepFieldPattern = regexp.MustCompile(`^step\[([^\]]+)\]\.(.+)$`)

type Report struct {
	Type     ReportType     `json:"type"`
	Status   ReportStatus   `json:"status"`
	Severity ReportSeverity `json:"severity"`
	Summary  string         `json:"summary"`
	Changes  []Change       `json:"changes"`
	Impacts  []Impact       `json:"impacts"`
}

type Change struct {
	Kind       string         `json:"kind"`
	Path       string         `json:"path"`
	Before     string         `json:"before"`
	After      string         `json:"after"`
	Severity   ReportSeverity `json:"severity"`
	ReasonCode string         `json:"reason_code"`
}

type Impact struct {
	Kind         string         `json:"kind"`
	PipelineSlug string         `json:"pipeline_slug,omitempty"`
	StepSlug     string         `json:"step_slug,omitempty"`
	EdgeSlug     string         `json:"edge_slug,omitempty"`
	Severity     ReportSeverity `json:"severity"`
	Message      string         `json:"message"`
}

// ContractImpact compares a deployed pipeline with a candidate and returns a reportable impact
// summary. It only reports real semantic changes, leaving unchanged specs without a report.
func ContractImpact(before, after *pipelinespec.Pipeline) *Report {
	if before == nil || after == nil {
		return nil
	}

	diffResult := diff.Diff(before, after)
	if !diffResult.HasChanges {
		return nil
	}

	check := diff.Check(diffResult)
	report := &Report{
		Type:     ReportTypeContractImpact,
		Status:   ReportStatusOpen,
		Severity: severityString(check.Severity),
		Summary:  summaryFor(check),
	}

	pipelineSlug := after.Metadata.Slug.String()
	for _, fieldChange := range diffResult.Metadata {
		severity := severityForFieldChange(check, fieldChange)
		report.Changes = append(report.Changes, Change{
			Kind:       changeKind(fieldChange),
			Path:       fieldChange.Field,
			Before:     fieldChange.Before,
			After:      fieldChange.After,
			Severity:   severity,
			ReasonCode: reasonCode(fieldChange),
		})
		report.Impacts = append(
			report.Impacts,
			impactsForChange(after, pipelineSlug, fieldChange, severity)...,
		)
	}
	for _, stepDiff := range diffResult.Steps {
		for _, fieldChange := range stepDiff.Fields {
			severity := severityForFieldChange(check, fieldChange)
			change := Change{
				Kind:       changeKind(fieldChange),
				Path:       fieldChange.Field,
				Before:     fieldChange.Before,
				After:      fieldChange.After,
				Severity:   severity,
				ReasonCode: reasonCode(fieldChange),
			}
			report.Changes = append(report.Changes, change)
			report.Impacts = append(report.Impacts, impactsForChange(after, pipelineSlug, fieldChange, severity)...)
		}
		if stepDiff.Kind == diff.StepRemoved {
			severity := ReportSeverityBreaking
			report.Changes = append(report.Changes, Change{
				Kind:       "step_removed",
				Path:       fmt.Sprintf("step[%s]", stepDiff.Slug),
				Before:     "present",
				After:      "<none>",
				Severity:   severity,
				ReasonCode: "step_removed",
			})
			report.Impacts = append(report.Impacts, Impact{
				Kind:         "step",
				PipelineSlug: pipelineSlug,
				StepSlug:     stepDiff.Slug,
				Severity:     severity,
				Message:      fmt.Sprintf("step %q was removed", stepDiff.Slug),
			})
		}
	}

	report.Impacts = dedupeImpacts(report.Impacts)
	if len(report.Impacts) == 0 {
		report.Impacts = append(report.Impacts, Impact{
			Kind:         "pipeline",
			PipelineSlug: pipelineSlug,
			Severity:     report.Severity,
			Message:      "pipeline contract changed",
		})
	}
	return report
}

func dedupeImpacts(impacts []Impact) []Impact {
	out := make([]Impact, 0, len(impacts))
	seen := make(map[string]int, len(impacts))
	for _, impact := range impacts {
		key := impactKey(impact)
		if idx, ok := seen[key]; ok {
			if impact.Severity > out[idx].Severity {
				out[idx].Severity = impact.Severity
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, impact)
	}
	return out
}

func impactKey(impact Impact) string {
	return strings.Join([]string{
		impact.Kind,
		impact.PipelineSlug,
		impact.StepSlug,
		impact.EdgeSlug,
		impact.Message,
	}, "\x00")
}

func severityString(value diff.Severity) ReportSeverity {
	switch value {
	case diff.SeverityBreaking:
		return ReportSeverityBreaking
	case diff.SeverityWarning:
		return ReportSeverityWarning
	default:
		return ReportSeveritySafe
	}
}

func summaryFor(check *diff.CheckResult) string {
	if check == nil || len(check.Findings) == 0 {
		return "no contract impact detected"
	}
	return fmt.Sprintf("%d contract impact finding(s), highest severity %s", len(check.Findings), check.Severity)
}

func severityForFieldChange(check *diff.CheckResult, change diff.FieldChange) ReportSeverity {
	switch change.Field {
	case "metadata.slug":
		return ReportSeverityBreaking
	case "metadata.name":
		return ReportSeveritySafe
	}
	if check == nil {
		return ReportSeveritySafe
	}
	needle := change.Field
	for _, finding := range check.Findings {
		if strings.Contains(finding.Message, needle) {
			return severityString(finding.Severity)
		}
	}
	return severityString(check.Severity)
}

func changeKind(change diff.FieldChange) string {
	if change.Before == "<none>" {
		return "addition"
	}
	if change.After == "<none>" {
		return "removal"
	}
	switch {
	case strings.HasSuffix(change.Field, ".type"):
		return "type_change"
	case strings.HasSuffix(change.Field, ".required"):
		return "required_change"
	case strings.HasSuffix(change.Field, ".nullable"):
		return "nullable_change"
	case strings.HasSuffix(change.Field, ".representation"):
		return "representation_change"
	case strings.HasSuffix(change.Field, ".root_shape"):
		return "root_shape_change"
	case change.Field == "metadata.slug":
		return "pipeline_slug_change"
	case change.Field == "metadata.name":
		return "pipeline_name_change"
	default:
		return "change"
	}
}

func reasonCode(change diff.FieldChange) string {
	return strings.ReplaceAll(changeKind(change), "_change", "_changed")
}

func impactsForChange(
	pipeline *pipelinespec.Pipeline,
	pipelineSlug string,
	change diff.FieldChange,
	severity ReportSeverity,
) []Impact {
	matches := stepFieldPattern.FindStringSubmatch(change.Field)
	if len(matches) != 3 {
		return []Impact{{
			Kind:         "pipeline",
			PipelineSlug: pipelineSlug,
			Severity:     severity,
			Message:      "pipeline metadata changed",
		}}
	}

	stepSlug := matches[1]
	impacts := []Impact{{
		Kind:         "step",
		PipelineSlug: pipelineSlug,
		StepSlug:     stepSlug,
		Severity:     severity,
		Message:      fmt.Sprintf("step %q contract changed", stepSlug),
	}}

	for _, downstream := range downstreamSteps(pipeline, stepSlug) {
		edgeSlug := fmt.Sprintf("%s->%s", stepSlug, downstream)
		impacts = append(impacts, Impact{
			Kind:         "edge",
			PipelineSlug: pipelineSlug,
			StepSlug:     downstream,
			EdgeSlug:     edgeSlug,
			Severity:     severity,
			Message:      fmt.Sprintf("downstream edge %s may be affected", edgeSlug),
		})
	}

	impacts = append(impacts, Impact{
		Kind:         "unknown_consumer",
		PipelineSlug: pipelineSlug,
		StepSlug:     stepSlug,
		Severity:     ReportSeverityWarning,
		Message:      "external app or consumer impact is unknown in the MVP",
	})
	return impacts
}

func downstreamSteps(pipeline *pipelinespec.Pipeline, producerSlug string) []string {
	if pipeline == nil {
		return nil
	}

	var out []string
	for _, step := range pipeline.Steps {
		if step.Transform != nil {
			for _, input := range step.Transform.Inputs {
				if input.FromStep.String() == producerSlug {
					out = append(out, step.Slug.String())
					break
				}
			}
		}
		if step.Sink != nil && step.Sink.Input != nil && step.Sink.Input.FromStep.String() == producerSlug {
			out = append(out, step.Slug.String())
		}
	}
	return out
}
